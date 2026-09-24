// Package reconciler 实现节点本地 reconciler 共享核心（架构 v1.1.2 §5.3/§9.7）：
// 13 阶段固定流水线，daemon 与 oneshot 模式复用同一实现（AD-5）。取消语义
// （可中断阶段 5–10 / 不可中断阶段 11–13）、失败恢复（可变步骤 5–12，含外部
// 编辑冲突检测与受管字段级回退）、apply 前基线比对（FR-12.7）都在本包落实。
package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 流水线步骤名（§5.3 阶段名，进 OperationProgress 与步骤审计）。
const (
	StepValidate  = "validate"
	StepInventory = "inventory"
	StepPlan      = "plan"
	StepBackup    = "backup"
	StepVersion   = "version"
	StepConfig    = "config"
	StepSkills    = "skills"
	StepMCP       = "mcp"
	StepRules     = "rules"
	StepHealth    = "health"
	StepVerify    = "verify"
	StepCommit    = "commit"
)

// Baseline 是 plan 时的观测基线（FR-12.7：apply 前重算并与计划基线比对）。
// inventorySeq 标识"计划基于哪次采集"，内容判据是受管投影摘要——节点 seq 单调
// 递增，apply 的重采 seq 必然更大，故只比较摘要。
type Baseline struct {
	ObservedProjectionDigest string `json:"observedProjectionDigest"`
	InventorySeq             int64  `json:"inventorySeq"`
}

// Execution 是一次流水线执行的输入。
type Execution struct {
	OperationID string
	Generation  int64
	// SnapshotJSON 是自包含完整快照（FR-13.6：ExecuteOperation 一律携带完整快照）。
	SnapshotJSON []byte
	// ReadOnly 为 true 时只执行阶段 1–3（FR-9.7 automatic reconcile 的只读部分：
	// validate → inventory → plan），绝不进入任何变更阶段。
	ReadOnly bool
	// RequireBaseline 非空时在阶段 2 后比对基线（FR-12.7）：不一致 → 零变更、
	// Failed(ReplanRequired)。
	RequireBaseline *Baseline
	// Cancelled 在每个阶段边界轮询（FR-13.9：取消仅在阶段边界生效）。
	Cancelled func() bool
	// Progress 上报步骤进度（daemon 转发为 OperationProgress）。
	Progress func(step, phase, message string)
}

// Result 是流水线终态（上报为 OperationResult）。OperationID 由调用方回填
// （outbox 条目自证身份，FR-13.7 重发时携带）。
type Result struct {
	OperationID string
	// Generation 是本操作针对的期望代（随结果上行；outbox 重发同样携带，
	// 使重发结果与首次上报等价）。
	Generation       int64
	Phase            string // Succeeded | Failed
	Reason           string
	Message          string
	TerminalModifier string
	Verify           *domain.VerifyEvidence
	PlanDigest       string
	// Baseline 是计划所基于的观测基线（FR-12.7：确认窗口内的 apply 以此比对）。
	Baseline *Baseline
	// Projection 是本次流水线观测的双侧投影摘要与序列（§7.1）。只读路径
	// （oneshot plan）据此组装观测上报；它**不**进 Verify，避免把"看过一眼"
	// 当成"收敛证据"（§6.2 的 Reconciled=True 需要 apply 后观测）。
	Projection *Projection
	ReadOnly   bool
	StartedAt  time.Time
	FinishedAt time.Time
}

// Projection 是一次观测的双侧投影摘要与序列（只读路径与观测上报共用）。
type Projection struct {
	DesiredDigest           string
	ObservedDigest          string
	CanonicalizationVersion string
	Seq                     int64
}

// AggregatedProjection 是一次 inventory 的多家族聚合投影（ADR-1：单机单判据）。
type AggregatedProjection struct {
	DesiredDigest           string
	ObservedDigest          string
	CanonicalizationVersion string
	Seq                     int64
	Families                map[string]adapter.AgentObservedState
}

// FileOwner 是适配器的可选扩展接口（§5.3 备份/恢复契约的适配器侧义务）：
// 声明受管文件、从内容提取受管键、以及"只还原受管键"的合并回退。
type FileOwner interface {
	ManagedFiles(home string) ([]string, error)
	ExtractManaged(content []byte) (map[string]any, error)
	// MergeManaged 只把受管键还原为备份中的值，保留文件中其余（未托管）内容。
	MergeManaged(home, relPath string, managed map[string]any) error
}

// Options 是 Executor 装配参数。
type Options struct {
	Registry *adapter.Registry
	// Seq 返回节点本地单调递增的 inventorySeq（FR-8.7；与周期上报共用同一序列）。
	Seq func() (int64, error)
	Now func() time.Time
}

// Executor 执行流水线。
type Executor struct {
	opts Options
}

func New(opts Options) *Executor {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Executor{opts: opts}
}

// Run 执行一次流水线到终态。任何失败都已含恢复尝试的结果（§14.2：
// 停止后续 → 恢复 → 再 inventory → 同时上报原始失败与恢复结果）。
func (e *Executor) Run(ctx context.Context, home string, ex Execution) Result {
	res := Result{StartedAt: e.opts.Now().UTC(), ReadOnly: ex.ReadOnly, Generation: ex.Generation}
	cancelled := func() bool { return ex.Cancelled != nil && ex.Cancelled() }
	progress := func(step, phase, msg string) {
		if ex.Progress != nil {
			ex.Progress(step, phase, msg)
		}
	}
	fail := func(reason, msg string) Result {
		res.Phase = domain.OperationPhaseFailed
		res.Reason = reason
		res.Message = msg
		res.FinishedAt = e.opts.Now().UTC()
		return res
	}
	cancelledResult := func(stage string) Result {
		res.Phase = domain.OperationPhaseFailed
		res.Reason = "Cancelled"
		res.TerminalModifier = domain.ModifierCancelled
		res.Message = "cancelled at stage boundary (" + stage + ")"
		res.FinishedAt = e.opts.Now().UTC()
		return res
	}

	// —— 阶段 1：validate（schema/版本校验）——
	progress(StepValidate, "Running", "")
	snap, err := domain.ParseSnapshot(ex.SnapshotJSON)
	if err != nil {
		return fail(domain.ReasonDesiredStateInvalid, fmt.Sprintf("snapshot unparseable: %v", err))
	}
	if snap.Desired.SchemaVersion == "" {
		return fail(domain.ReasonDesiredStateInvalid, "snapshot missing schemaVersion")
	}
	families := sortedAgentFamilies(snap)
	if len(families) == 0 {
		return fail(domain.ReasonDesiredStateInvalid, "snapshot has no managed agent family")
	}
	agents, err := adapter.DesiredStatesFromSnapshot(snap)
	if err != nil {
		return fail(domain.ReasonDesiredStateInvalid, err.Error())
	}
	// 适配器侧能力前置校验（矩阵：未支持/未验证能力必须在**任何写入之前**
	// 明确拒绝，禁止静默跳过；阶段 1 早于阶段 4 备份与阶段 5–9 写入）。
	for _, f := range families {
		ad, err := e.opts.Registry.Get(f)
		if err != nil {
			return fail(domain.ReasonDesiredStateInvalid, err.Error())
		}
		if err := ad.Validate(ctx, home, agents[f]); err != nil {
			return fail(domain.ReasonDesiredStateInvalid, err.Error())
		}
	}
	progress(StepValidate, "Succeeded", fmt.Sprintf("generation=%d families=%v", ex.Generation, families))

	// 取消边界（§9.7：阶段 1–4 之间直接终止，尚无变更、无需恢复）。
	if cancelled() {
		return cancelledResult("1-4, no changes made")
	}

	// —— 阶段 2：inventory current state（记录 seq 与基线摘要）——
	progress(StepInventory, "Running", "")
	proj, err := e.inventoryAll(ctx, home, agents)
	if err != nil {
		return fail(domain.ProjectionVersionMismatch, err.Error())
	}
	progress(StepInventory, "Succeeded", fmt.Sprintf("seq=%d observed=%s", proj.Seq, short(proj.ObservedDigest)))
	res.Baseline = &Baseline{ObservedProjectionDigest: proj.ObservedDigest, InventorySeq: proj.Seq}
	res.Projection = projection(proj)

	// FR-12.7 / T8：apply 前基线比对——重算观测与计划基线不一致即拒绝、零变更。
	// 基线判据含两项（FR-12.7 点名"观测受管摘要 + inventorySeq"）：
	//  1. 受管投影摘要必须相同（内容没被别人改过）；
	//  2. 本次采集的 inventorySeq 必须**大于**计划时的 seq——它是"apply 真的重新
	//     采集了观测"的证明。只比摘要会漏掉"复用了缓存观测"这一情形：节点若直接
	//     拿 state/last-observed.json 里的旧摘要来比对，摘要相同却被当成新鲜证据。
	//     seq 是节点本地单调序（FR-8.7），故只可能是"更小/相等 = 没重采"。
	if ex.RequireBaseline != nil {
		if proj.ObservedDigest != ex.RequireBaseline.ObservedProjectionDigest {
			return fail(domain.ReasonReplanRequired,
				fmt.Sprintf("baseline changed since plan (plan=%s now=%s); re-plan and re-confirm required",
					short(ex.RequireBaseline.ObservedProjectionDigest), short(proj.ObservedDigest)))
		}
		if proj.Seq <= ex.RequireBaseline.InventorySeq {
			return fail(domain.ReasonReplanRequired,
				fmt.Sprintf("observation was not re-collected for apply (plan seq=%d now seq=%d); re-plan and re-confirm required",
					ex.RequireBaseline.InventorySeq, proj.Seq))
		}
	}

	// —— 阶段 3：calculate plan（产出 planDigest；空计划仍走阶段 10/12，§5.3 契约）——
	progress(StepPlan, "Running", "")
	var changes []adapter.Change
	for _, f := range families {
		ad, _ := e.opts.Registry.Get(f)
		c, err := ad.Plan(ctx, home, agents[f], proj.Families[f])
		if err != nil {
			return fail(domain.ReasonDesiredStateInvalid, fmt.Sprintf("plan %s: %v", f, err))
		}
		changes = append(changes, c...)
	}
	res.PlanDigest = PlanDigest(ex.Generation, proj, changes)
	progress(StepPlan, "Succeeded", fmt.Sprintf("changes=%d digest=%s", len(changes), short(res.PlanDigest)))

	if ex.ReadOnly {
		// FR-9.7：只读部分到此为止，不得进入任何变更阶段。
		res.Phase = domain.OperationPhaseSucceeded
		res.FinishedAt = e.opts.Now().UTC()
		return res
	}

	backupDir := filepath.Join(home, ".local", "share", "agent-fleet", "backups", ex.OperationID)
	// backedUp 标记"阶段 4 真的做过备份"。空计划（changes 为空）没有任何写入，
	// 也就没有备份：此时 5–10 的取消/健康失败不得去读不存在的 manifest——那会
	// 把一台未被改动的机器误报成 Failed(RestoreConflict)，服务端还会据此置
	// Degraded=True（§14.2 只对"恢复也失败"置降级）。
	backedUp := false
	// restore 执行备份还原并组装终态：modifier 为 Cancelled 时终态修饰置位
	//（FR-13.9），否则为普通失败原因。
	restore := func(reason, modifier, origMsg string) Result {
		if !backedUp {
			// 零变更：跳过恢复，按原原因终结（修饰符照常保留）。
			out := fail(reason, origMsg+"; no changes were applied, nothing to restore")
			out.TerminalModifier = modifier
			return out
		}
		// —— 失败/取消恢复：备份还原（含外部编辑冲突检测，§5.3 契约）——
		progress("restore", "Running", "restoring from backup after "+reason)
		msg, restoreErr := e.restoreFromBackup(home, backupDir, progress)
		if restoreErr != nil {
			// 不可恢复错误：显式失败（上层置 Degraded=True），绝不静默覆盖。
			return fail(domain.ReasonRestoreConflict,
				fmt.Sprintf("%s; restore failed: %v (machine left uncertain)", origMsg, restoreErr))
		}
		// 恢复后再 inventory（§14.2），证据随结果上报。
		if proj2, invErr := e.inventoryAll(ctx, home, agents); invErr == nil {
			proj = proj2
			res.Verify = evidence(proj, domain.AdapterHealthSkipped)
		}
		out := fail(reason, fmt.Sprintf("%s; restored from backup: %s", origMsg, msg))
		out.TerminalModifier = modifier
		return out
	}

	// —— 阶段 4：backup managed files ——
	if cancelled() {
		return cancelledResult("1-4, no changes made")
	}
	progress(StepBackup, "Running", "")
	if len(changes) > 0 {
		if err := e.backupManagedFiles(home, backupDir, families); err != nil {
			return fail(domain.ReasonConfigWriteFailed, fmt.Sprintf("backup failed: %v", err))
		}
		backedUp = true
	}
	progress(StepBackup, "Succeeded", backupDir)

	// —— 阶段 5–9：可变步骤（version/config/skills/mcp/rules，逐家族合并写）——
	stepOrder := []string{StepVersion, StepConfig, StepSkills, StepMCP, StepRules}
	for _, step := range stepOrder {
		if cancelled() {
			// 可中断阶段 5–10：取消走标准备份恢复路径（§9.7）。
			return restore("Cancelled", domain.ModifierCancelled, "cancelled during mutable stages")
		}
		var stepChanges []adapter.Change
		for _, c := range changes {
			if c.Step == step {
				stepChanges = append(stepChanges, c)
			}
		}
		if len(stepChanges) == 0 {
			continue // 空计划跳过 5–9，但 10/12 必须执行（§5.3 空计划语义）
		}
		progress(step, "Running", fmt.Sprintf("%d change(s)", len(stepChanges)))
		for _, f := range families {
			var fc []adapter.Change
			for _, c := range stepChanges {
				if c.Family == f {
					fc = append(fc, c)
				}
			}
			if len(fc) == 0 {
				continue
			}
			ad, _ := e.opts.Registry.Get(f)
			if err := ad.Apply(ctx, home, agents[f], fc); err != nil {
				return restore(stepFailureReason(step), "", fmt.Sprintf("apply %s failed: %v", step, err))
			}
		}
		progress(step, "Succeeded", "")
	}

	// —— 阶段 10：adapter health checks（空计划也必须执行，§5.3 契约）——
	if cancelled() {
		return restore("Cancelled", domain.ModifierCancelled, "cancelled during mutable stages")
	}
	progress(StepHealth, "Running", "")
	health := domain.AdapterHealthPassed
	var healthErr error
	for _, f := range families {
		ad, _ := e.opts.Registry.Get(f)
		if err := ad.HealthCheck(ctx, home, agents[f]); err != nil {
			health, healthErr = domain.AdapterHealthFailed, err
			break
		}
	}
	if healthErr != nil {
		res.Verify = evidence(proj, health)
		return restore(domain.ReasonHealthCheckFailed, "", fmt.Sprintf("health check failed: %v", healthErr))
	}
	progress(StepHealth, "Succeeded", "")

	// 取消在不可中断阶段 11–13 不生效（§9.7：避免在验证/提交阶段制造半状态）。

	// —— 阶段 11：inventory again ——
	progress(StepInventory, "Running", "post-apply")
	proj2, err := e.inventoryAll(ctx, home, agents)
	if err != nil {
		res.Verify = evidence(proj, health)
		return restore(domain.ProjectionVersionMismatch, "", fmt.Sprintf("post-apply inventory failed: %v", err))
	}
	proj = proj2
	res.Projection = projection(proj)

	// —— 阶段 12：verify desired == observed（投影摘要比对；不等 → VerifyFailed）——
	progress(StepVerify, "Running", "")
	res.Verify = evidence(proj, health)
	if proj.DesiredDigest != proj.ObservedDigest {
		return restore(domain.ReasonVerifyFailed, "",
			fmt.Sprintf("post-apply verify failed: desired=%s observed=%s",
				short(proj.DesiredDigest), short(proj.ObservedDigest)))
	}
	progress(StepVerify, "Succeeded", fmt.Sprintf("digest=%s", short(proj.DesiredDigest)))

	// —— 阶段 13：commit success ——
	res.Phase = domain.OperationPhaseSucceeded
	res.FinishedAt = e.opts.Now().UTC()
	progress(StepCommit, "Succeeded", "")
	return res
}

// inventoryAll 对全部家族执行双侧投影并聚合为单机单判据（ADR-1）。
// 任一家族 canonicalizationVersion 不一致 → 错误（服务端置 Unknown + 报
// ProjectionVersionMismatch，不产生 drift 判定，§7.1）。
func (e *Executor) inventoryAll(ctx context.Context, home string, agents map[string]adapter.AgentDesiredState) (AggregatedProjection, error) {
	seq, err := e.opts.Seq()
	if err != nil {
		return AggregatedProjection{}, fmt.Errorf("inventory seq: %w", err)
	}
	proj := AggregatedProjection{
		Seq:      seq,
		Families: map[string]adapter.AgentObservedState{},
	}
	desiredSet := map[string]string{}
	observedSet := map[string]string{}
	for f, da := range agents {
		ad, err := e.opts.Registry.Get(f)
		if err != nil {
			return proj, err
		}
		obs, err := ad.Inventory(ctx, home, da)
		if err != nil {
			return proj, fmt.Errorf("inventory %s: %w", f, err)
		}
		if proj.CanonicalizationVersion == "" {
			proj.CanonicalizationVersion = obs.CanonicalizationVersion
		} else if proj.CanonicalizationVersion != obs.CanonicalizationVersion {
			return proj, fmt.Errorf("canonicalizationVersion mismatch across families: %s vs %s",
				proj.CanonicalizationVersion, obs.CanonicalizationVersion)
		}
		proj.Families[f] = obs
		desiredSet[f] = obs.DesiredProjectionDigest
		observedSet[f] = obs.ObservedProjectionDigest
	}
	proj.DesiredDigest = combineDigests(desiredSet)
	proj.ObservedDigest = combineDigests(observedSet)
	return proj, nil
}

// combineDigests 把多家族摘要聚合为单机判据：对 {family: digest} 规范化 JSON
// 再取 SHA-256（同源同规则，不引入第二套比较语义）。
func combineDigests(set map[string]string) string {
	b, err := desiredstate.CanonicalJSON(set)
	if err != nil {
		return "sha256:error"
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum)
}

// PlanDigest 对（代、基线、变更集）计算确定性摘要：确认的对象是具体计划（§9.3）。
func PlanDigest(generation int64, proj AggregatedProjection, changes []adapter.Change) string {
	input := map[string]any{
		"generation": generation,
		"baseline": map[string]any{
			"observedProjectionDigest": proj.ObservedDigest,
			"inventorySeq":             proj.Seq,
		},
		"changes": changes,
	}
	b, err := desiredstate.CanonicalJSON(input)
	if err != nil {
		return "sha256:error"
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("sha256:%x", sum)
}

// backupManagedFiles 把各适配器声明的受管文件复制到 backups/<op-id>/，
// 附恢复所需元数据（路径、权限、整文件摘要、受管键值，§5.3 契约 1/§16.3）。
func (e *Executor) backupManagedFiles(home, backupDir string, families []string) error {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	manifest := backupManifest{OperationID: filepath.Base(backupDir), Files: []backupFile{}}
	for _, f := range families {
		ad, err := e.opts.Registry.Get(f)
		if err != nil {
			return err
		}
		owner, ok := ad.(FileOwner)
		if !ok {
			continue // 未声明受管文件的适配器无可备份内容
		}
		files, err := owner.ManagedFiles(home)
		if err != nil {
			return err
		}
		for _, rel := range files {
			abs := filepath.Join(home, rel)
			raw, err := os.ReadFile(abs)
			if os.IsNotExist(err) {
				// 备份时不存在的文件也登记：操作创建它后失败恢复时须删除
				//（"回到操作前状态"包含"回到不存在"）。
				manifest.Files = append(manifest.Files, backupFile{Path: rel, Absent: true})
				continue
			}
			if err != nil {
				return fmt.Errorf("backup %s: %w", rel, err)
			}
			info, err := os.Stat(abs)
			if err != nil {
				return fmt.Errorf("backup stat %s: %w", rel, err)
			}
			managed, err := owner.ExtractManaged(raw)
			if err != nil {
				return fmt.Errorf("backup managed keys %s: %w", rel, err)
			}
			if err := os.MkdirAll(filepath.Join(backupDir, filepath.Dir(rel)), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(backupDir, rel), raw, info.Mode().Perm()); err != nil {
				return fmt.Errorf("backup write %s: %w", rel, err)
			}
			sum := sha256.Sum256(raw)
			manifest.Files = append(manifest.Files, backupFile{
				Path: rel, Mode: int(info.Mode().Perm()),
				SHA256: fmt.Sprintf("sha256:%x", sum), Managed: managed,
			})
		}
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(backupDir, "manifest.json"), b, 0o644)
}

// restoreFromBackup 落实 §5.3 恢复契约：
//  1. 当前文件与备份逐字节相同 → 整文件还原（与操作前完全一致）；
//  2. 不同（存在外部编辑）→ 受管字段级回退（只还原受管键，保留未托管编辑）；
//  3. 不可行（文件不可解析等）→ 显式错误（上层报 RestoreConflict + Degraded）。
//
// 绝不静默整文件覆盖。
func (e *Executor) restoreFromBackup(home, backupDir string, progress func(step, phase, msg string)) (string, error) {
	raw, err := os.ReadFile(filepath.Join(backupDir, "manifest.json"))
	if err != nil {
		return "", fmt.Errorf("backup manifest unreadable: %w", err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("backup manifest unparseable: %w", err)
	}
	if len(manifest.Files) == 0 {
		return "no managed files were backed up (empty plan)", nil
	}
	var notes []string
	for _, bf := range manifest.Files {
		abs := filepath.Join(home, bf.Path)
		if bf.Absent {
			// 备份时不存在：操作若已创建，删除以回到操作前状态。
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("remove created file %s: %w", bf.Path, err)
			}
			notes = append(notes, bf.Path+": removed file created by operation (absent before)")
			continue
		}
		cur, readErr := os.ReadFile(abs)
		if readErr != nil && !os.IsNotExist(readErr) {
			return "", fmt.Errorf("read %s: %w", bf.Path, readErr)
		}
		sum := sha256.Sum256(cur)
		curDigest := fmt.Sprintf("sha256:%x", sum)
		backupContent, err := os.ReadFile(filepath.Join(backupDir, bf.Path))
		if err != nil {
			return "", fmt.Errorf("backup copy of %s unreadable: %w", bf.Path, err)
		}
		switch {
		case os.IsNotExist(readErr):
			// 备份时存在而当前缺失 → 整文件还原。
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(abs, backupContent, os.FileMode(bf.Mode)); err != nil {
				return "", err
			}
			notes = append(notes, bf.Path+": restored missing file")
		case curDigest == bf.SHA256:
			// 无外部编辑：整文件还原（还原后与操作前逐字节一致）。
			if err := os.WriteFile(abs, backupContent, os.FileMode(bf.Mode)); err != nil {
				return "", err
			}
			notes = append(notes, bf.Path+": whole-file restore (unchanged since backup)")
		default:
			// 存在外部编辑：受管字段级回退，保留未托管当前值（A5/T7）。
			owner := e.ownerFor(home, bf.Path)
			if owner == nil {
				return "", fmt.Errorf("%s: external edit and no managed-key restorer available", bf.Path)
			}
			if err := owner.MergeManaged(home, bf.Path, bf.Managed); err != nil {
				return "", fmt.Errorf("%s: managed-key rollback infeasible: %w", bf.Path, err)
			}
			notes = append(notes, bf.Path+": managed-key rollback (unmanaged edits preserved)")
		}
	}
	out := ""
	for i, n := range notes {
		if i > 0 {
			out += "; "
		}
		out += n
	}
	return out, nil
}

// ownerFor 找到声明了该受管文件的适配器（FileOwner）。
func (e *Executor) ownerFor(home, relPath string) FileOwner {
	for _, f := range e.opts.Registry.Families() {
		ad, err := e.opts.Registry.Get(f)
		if err != nil {
			continue
		}
		owner, ok := ad.(FileOwner)
		if !ok {
			continue
		}
		files, err := owner.ManagedFiles(home)
		if err != nil {
			continue
		}
		for _, p := range files {
			if p == relPath {
				return owner
			}
		}
	}
	return nil
}

// projection 把聚合投影映射为上报用的摘要三元组（只读路径用）。
func projection(proj AggregatedProjection) *Projection {
	return &Projection{
		DesiredDigest:           proj.DesiredDigest,
		ObservedDigest:          proj.ObservedDigest,
		CanonicalizationVersion: proj.CanonicalizationVersion,
		Seq:                     proj.Seq,
	}
}

func evidence(proj AggregatedProjection, health string) *domain.VerifyEvidence {
	return &domain.VerifyEvidence{
		DesiredProjectionDigest:  proj.DesiredDigest,
		ObservedProjectionDigest: proj.ObservedDigest,
		CanonicalizationVersion:  proj.CanonicalizationVersion,
		InventorySeq:             proj.Seq,
		AdapterHealth:            health,
	}
}

func stepFailureReason(step string) string {
	switch step {
	case StepVersion:
		return domain.ReasonVersionVerificationFailed
	default:
		return domain.ReasonConfigWriteFailed
	}
}

func sortedAgentFamilies(snap *domain.DesiredStateSnapshot) []string {
	out := make([]string, 0, len(snap.Desired.Agents))
	for f := range snap.Desired.Agents {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func short(digest string) string {
	if len(digest) > 19 {
		return digest[:19] + "…"
	}
	return digest
}

type backupManifest struct {
	OperationID string       `json:"operationId"`
	Files       []backupFile `json:"files"`
}

type backupFile struct {
	Path string `json:"path"`
	Mode int    `json:"mode,omitempty"`
	// SHA256 是备份时整文件内容摘要（恢复的"外部编辑"判据）。
	SHA256 string `json:"sha256,omitempty"`
	// Absent 标记备份时该文件不存在（恢复时应删除操作创建的副本）。
	Absent  bool           `json:"absent,omitempty"`
	Managed map[string]any `json:"managed,omitempty"`
}
