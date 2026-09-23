// Package sshops 实现 SSH-only 路径的操作编排（架构 v1.1.2 §4.2/§4.5/§7.4/§9.3、
// FR-12.4/FR-12.7/FR-12.8）：探测 → 构建并上传不可变操作 bundle → `agentd oneshot
// plan` → 操作者确认（planDigest）→ `agentd oneshot apply` → 清理 staging。
//
// 边界：本包只做"经 SSH 驱动节点上的同一套 reconciler"，不实现 SSH CA/密钥生命周期、
// 不支持密码认证、不做 Web SSH 终端、不代理交互式会话（§2.6 非目标）。
package sshops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/bundle"
	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/sshtransport"
)

// InventorySink 是观测落库 + Machine 条件置位的入口（由 machine 控制器实现，
// §4.2：InventoryReady/lastInventoryAt/inventorySeq 的唯一置位方是它）。
type InventorySink interface {
	OnInventory(ctx context.Context, rec domain.ObservedStateRecord, obs domain.ObservedState) error
}

// Config 是编排器装配参数。
type Config struct {
	Transport *sshtransport.Transport
	Machines  domain.MachineRepository
	Status    domain.MachineStatusRepository
	Ops       domain.OperationRepository
	Inventory InventorySink
	// Artifacts 是 Skill 工件来源（控制面工件缓存，§4.7）。
	Artifacts bundle.ArtifactSource
	// AgentdBinDir 存放按平台预构建的 agentd 二进制（agentd-<goos>-<goarch>），
	// 用于节点上缺 agentd 时上传临时兼容二进制（§7.4/FR-12.4）。
	AgentdBinDir string
	// OperatorHome 是 include 安装目标所在的操作者 home（默认取运行用户 home）。
	OperatorHome string
	// GeneratedDir 是生成产物目录（data-dir 下 generated/ssh/，§7.7）。
	GeneratedDir string
	Limits       bundle.Limits
	Log          *slog.Logger
	Now          func() time.Time
}

// Orchestrator 是 SSH 路径的编排器。
type Orchestrator struct {
	cfg Config
}

func New(cfg Config) *Orchestrator {
	if cfg.Limits == (bundle.Limits{}) {
		cfg.Limits = bundle.DefaultLimits()
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Orchestrator{cfg: cfg}
}

// ProbeReport 是一次 SSH 探测的产物（§4.2 职责 3）。
type ProbeReport struct {
	Target        *sshtransport.Target
	OS            string
	Arch          string
	Hostname      string
	HomeDir       string
	AgentdVersion string
	AgentdPath    string // PATH 上的 agentd 或 staging 中的临时二进制
}

// sshFailure 构造 SSH 路径的明确失败（节点未启动或已交回明确结论）。
func sshFailure(reason, format string, args ...any) *reconcile.SSHFailure {
	return reconcile.NewSSHFailure(reason, format, args...)
}

// opResultJSON 是 node 侧 oneshot 的 stdout 文档（与 cmd/agent-fleet-agentd/oneshot.go
// 的 oneshotResult 同形；这里是控制面侧的解析目标）。
type opResultJSON struct {
	OperationID      string          `json:"operationId"`
	Phase            string          `json:"phase"`
	Reason           string          `json:"reason"`
	Message          string          `json:"message"`
	TerminalModifier string          `json:"terminalModifier"`
	PlanDigest       string          `json:"planDigest"`
	Baseline         *baselineJSON   `json:"baseline"`
	ReadOnly         bool            `json:"readOnly"`
	Verify           *verifyJSON     `json:"verify"`
	ObservedState    json.RawMessage `json:"observedState"`
	StartedAt        time.Time       `json:"startedAt"`
	FinishedAt       time.Time       `json:"finishedAt"`
	BundleDigest     string          `json:"bundleDigest"`
	Error            string          `json:"error"`
}

type baselineJSON struct {
	ObservedProjectionDigest string `json:"observedProjectionDigest"`
	InventorySeq             int64  `json:"inventorySeq"`
}

type verifyJSON struct {
	DesiredProjectionDigest  string `json:"desiredProjectionDigest"`
	ObservedProjectionDigest string `json:"observedProjectionDigest"`
	CanonicalizationVersion  string `json:"canonicalizationVersion"`
	InventorySeq             int64  `json:"inventorySeq"`
	AdapterHealth            string `json:"adapterHealth"`
}

// Probe 执行 §4.2 职责 3 的 SSH 探测：`ssh -G` 解析生效配置 + 远端采集
// OS/arch/home/agentd 版本。调用方负责把结论写入 Machine status。
//
// 本方法**不**上传临时二进制（探测应当是廉价只读动作）；缺 agentd 时只报告平台
// 与 homeDir，真正的临时二进制上传发生在 prepare（plan/apply/inventory 路径）。
func (o *Orchestrator) Probe(ctx context.Context, machine string) (*ProbeReport, error) {
	alias, err := o.alias(ctx, machine)
	if err != nil {
		return nil, err
	}
	target, err := o.cfg.Transport.Resolve(ctx, alias)
	if err != nil {
		return nil, err
	}
	rep := &ProbeReport{Target: target}
	// 平台：uname（静态 argv，不经本地 shell）。
	if out, err := o.cfg.Transport.Run(ctx, alias, []string{"uname", "-s", "-m"}); err == nil {
		fields := strings.Fields(string(out.Stdout))
		if len(fields) >= 2 {
			rep.OS, rep.Arch = normalizeOS(fields[0]), normalizeArch(fields[1])
		}
	}
	// homeDir：printenv 读节点进程环境里的 HOME（与 agentd 自身会看到的同一个值；
	// 静态 argv，不需要远端 shell 展开变量），失败再退回登录 shell 的 cwd。
	if out, err := o.cfg.Transport.Run(ctx, alias, []string{"printenv", "HOME"}); err == nil {
		if h := strings.TrimSpace(string(out.Stdout)); strings.HasPrefix(h, "/") {
			rep.HomeDir = h
		}
	}
	if rep.HomeDir == "" {
		if out, err := o.cfg.Transport.Run(ctx, alias, []string{"pwd"}); err == nil {
			rep.HomeDir = strings.TrimSpace(string(out.Stdout))
		}
	}
	if rep.HomeDir == "" {
		return rep, sshFailure(domain.ReasonRemoteCommandFailed,
			"could not determine remote home directory on %s", alias)
	}
	layout, err := sshtransport.NewRemoteLayout(rep.HomeDir)
	if err != nil {
		return rep, err
	}
	// 节点已安装 agentd 时顺带取主机名/版本（缺 agentd 不影响探测结论）。
	if path, ok := o.agentdOnPath(ctx, alias); ok {
		rep.AgentdPath = path
		if obs, err := o.runInventory(ctx, alias, path, layout); err == nil {
			applyMachineInfo(rep, obs)
		}
	}
	return rep, nil
}

// agentdOnPath 判断节点 PATH 上是否已有 agentd（不上传任何东西）。
func (o *Orchestrator) agentdOnPath(ctx context.Context, alias string) (string, bool) {
	if _, err := o.cfg.Transport.Run(ctx, alias, []string{"agent-fleet-agentd", "version"}); err == nil {
		return "agent-fleet-agentd", true
	}
	return "", false
}

// ProbeAndRecord 探测并把结论写入 Machine status（HTTP 探测端点的实现）。
func (o *Orchestrator) ProbeAndRecord(ctx context.Context, machine string) error {
	rep, err := o.Probe(ctx, machine)
	if err != nil {
		if rerr := o.RecordProbeFailure(ctx, machine, err); rerr != nil {
			o.cfg.Log.Error("record probe failure", "machine_id", machine, "err", rerr)
		}
		return err
	}
	return o.StoreProbe(ctx, machine, rep)
}

// Plan 构建并上传 bundle、执行 `oneshot plan`，返回 planDigest 与观测
// （§9.3：计划回显给操作者确认，确认对象是具体计划）。
func (o *Orchestrator) Plan(ctx context.Context, machine, opID string, generation int64, snapJSON []byte) (*reconcile.SSHPlanOutcome, error) {
	alias, layout, agentdPath, err := o.prepare(ctx, machine, opID)
	if err != nil {
		return nil, err
	}
	if _, err := o.uploadBundle(ctx, alias, layout, snapJSON); err != nil {
		return nil, err
	}
	argv := []string{agentdPath, "oneshot", "plan", "--bundle", layout.BundleDir(),
		"--operation-id", opID, "--staging", layout.Root(), "--home", layout.HomeDir}
	out, err := o.cfg.Transport.Run(ctx, alias, argv)
	res, parseErr := parseResult(out.Stdout)
	if err != nil && parseErr != nil {
		return nil, o.transportFailure(err, "oneshot plan")
	}
	if parseErr != nil {
		return nil, sshFailure(domain.ReasonRemoteCommandFailed,
			"oneshot plan produced unparseable output: %v", parseErr)
	}
	if res.Phase != domain.OperationPhaseSucceeded {
		// 节点已给出明确的失败结论（例如 bundle 校验失败）：进程已退出，
		// 其输入可以清理，操作按该 reason 终结。
		return nil, &reconcile.SSHFailure{Reason: reasonOr(res.Reason, res.Error), Message: res.Message}
	}
	if res.PlanDigest == "" || res.Baseline == nil {
		return nil, sshFailure(domain.ReasonRemoteCommandFailed,
			"oneshot plan returned no planDigest/baseline")
	}
	if err := o.storeObservationJSON(ctx, machine, res.ObservedState, opID, generation); err != nil {
		o.cfg.Log.Warn("store plan observation failed", "machine_id", machine, "operation_id", opID, "err", err)
	}
	return &reconcile.SSHPlanOutcome{
		PlanDigest:     res.PlanDigest,
		BaselineDigest: res.Baseline.ObservedProjectionDigest,
		BaselineSeq:    res.Baseline.InventorySeq,
		ObservedState:  res.ObservedState,
		Generation:     generation,
	}, nil
}

// Apply 上传 bundle 并执行 `oneshot apply`（携操作者确认的 planDigest）。
// 节点侧会重取观测、重算 plan、比对基线；不一致即零变更 ReplanRequired（FR-12.7）。
func (o *Orchestrator) Apply(ctx context.Context, machine, opID, planDigest string, generation int64, snapJSON []byte) (*reconcile.SSHApplyOutcome, error) {
	alias, layout, agentdPath, err := o.prepare(ctx, machine, opID)
	if err != nil {
		return nil, err
	}
	if _, err := o.uploadBundle(ctx, alias, layout, snapJSON); err != nil {
		return nil, err
	}
	argv := []string{agentdPath, "oneshot", "apply", "--bundle", layout.BundleDir(),
		"--operation-id", opID, "--staging", layout.Root(), "--home", layout.HomeDir}
	if planDigest != "" {
		argv = append(argv, "--plan-digest", planDigest)
	}
	out, err := o.cfg.Transport.Run(ctx, alias, argv)
	res, parseErr := parseResult(out.Stdout)
	if err != nil && parseErr != nil {
		return nil, o.transportFailure(err, "oneshot apply")
	}
	if parseErr != nil {
		return nil, sshFailure(domain.ReasonRemoteCommandFailed,
			"oneshot apply produced unparseable output: %v", parseErr)
	}
	// 节点交回结果 = 进程已退出 = 可以清理其输入（§4.5 规则 3）。
	result := &reconcile.SSHApplyOutcome{
		Phase: res.Phase, Reason: reasonOr(res.Reason, res.Error), Message: res.Message,
		TerminalModifier: res.TerminalModifier, PlanDigest: res.PlanDigest,
		ObservedState: res.ObservedState,
		StartedAt:     res.StartedAt, FinishedAt: res.FinishedAt, NodeConfirmed: true,
	}
	if res.Verify != nil {
		result.Verify = &domain.VerifyEvidence{
			DesiredProjectionDigest:  res.Verify.DesiredProjectionDigest,
			ObservedProjectionDigest: res.Verify.ObservedProjectionDigest,
			CanonicalizationVersion:  res.Verify.CanonicalizationVersion,
			InventorySeq:             res.Verify.InventorySeq,
			AdapterHealth:            res.Verify.AdapterHealth,
		}
	}
	if result.Phase == "" {
		return nil, sshFailure(domain.ReasonRemoteCommandFailed, "oneshot apply returned no phase")
	}
	// 结果与 operationId 绑定：确认交回的结果确实属于本次操作。
	if res.OperationID != "" && res.OperationID != opID {
		return nil, sshFailure(domain.ReasonRemoteCommandFailed,
			"oneshot apply returned result for %s, expected %s", res.OperationID, opID)
	}
	if err := o.storeObservationJSON(ctx, machine, res.ObservedState, opID, generation); err != nil {
		o.cfg.Log.Warn("store apply observation failed", "machine_id", machine, "operation_id", opID, "err", err)
	}
	o.cleanupAfterRun(ctx, alias, layout, opID, result.NodeConfirmed)
	return result, nil
}

// Cancel 经 SSH 落一个取消标记（§5.1：SSH-only 无 CancelOperation 通道），
// 节点流水线在阶段边界观察它（FR-13.9：取消只在阶段边界生效，且必须由节点
// 确认停止——控制面不单方面改终态）。
func (o *Orchestrator) Cancel(ctx context.Context, machine, opID string) error {
	alias, layout, _, err := o.prepare(ctx, machine, opID)
	if err != nil {
		return err
	}
	if _, err := o.cfg.Transport.Run(ctx, alias, []string{"install", "-d", "-m", "700", layout.Root()}); err != nil {
		return err
	}
	if _, err := o.cfg.Transport.Run(ctx, alias,
		[]string{"touch", layout.CancelFile(opID)}); err != nil {
		return err
	}
	o.cfg.Log.Info("ssh cancel marker written", "machine_id", machine, "operation_id", opID)
	return nil
}

// ---- 内部步骤 ----

// prepare 解析 alias/homeDir，做 staging 清理判定并创建目录树。
func (o *Orchestrator) prepare(ctx context.Context, machine, opID string) (string, sshtransport.RemoteLayout, string, error) {
	alias, err := o.alias(ctx, machine)
	if err != nil {
		return "", sshtransport.RemoteLayout{}, "", err
	}
	home, err := o.homeDir(ctx, machine)
	if err != nil {
		return "", sshtransport.RemoteLayout{}, "", err
	}
	layout, err := sshtransport.NewRemoteLayout(home)
	if err != nil {
		return "", sshtransport.RemoteLayout{}, "", err
	}
	// §4.5 清理规则 1–2：只在"不存在引用该路径的未决操作"时清理陈旧内容。
	if dec := sshtransport.DecideCleanup(o.previousOp(ctx, machine, opID)); dec.Clean {
		if _, err := o.cfg.Transport.Run(ctx, alias, layout.CleanBundleArgv()); err != nil {
			o.cfg.Log.Warn("staging cleanup failed (not an operation failure, §4.5 规则 4)",
				"machine_id", machine, "err", err)
		}
	} else {
		o.cfg.Log.Warn("staging cleanup skipped", "machine_id", machine, "reason", dec.Reason)
	}
	if _, err := o.cfg.Transport.Run(ctx, alias, layout.EnsureArgv()); err != nil {
		return "", layout, "", err
	}
	agentdPath, err := o.ensureAgentd(ctx, alias, layout.HomeDir, "", "")
	if err != nil {
		return "", layout, "", err
	}
	return alias, layout, agentdPath, nil
}

// uploadBundle 在控制面构建 bundle（§7.4 执行序第 1 步）并 scp 上传到固定 staging。
func (o *Orchestrator) uploadBundle(ctx context.Context, alias string, layout sshtransport.RemoteLayout, snapJSON []byte) (*bundle.Manifest, error) {
	snap, err := domain.ParseSnapshot(snapJSON)
	if err != nil {
		return nil, domain.Coded(domain.ReasonDesiredStateInvalid, "%v", err)
	}
	tmp, err := os.MkdirTemp("", "agent-fleet-bundle-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	// 本地目录字面量命名为 bundle：scp -r <dir> <staging>/ 会得到
	// <staging>/bundle（固定路径），不依赖 "src/." 这类目录内容语义。
	localBundle := filepath.Join(tmp, bundle.DirName)
	src := o.cfg.Artifacts
	if src == nil {
		src = bundle.EmptySource{}
	}
	man, err := bundle.Build(localBundle, snap, src, o.cfg.Limits)
	if err != nil {
		return nil, err
	}
	if err := o.cfg.Transport.Upload(ctx, alias, localBundle, layout.Root()+"/"); err != nil {
		return nil, err
	}
	return man, nil
}

// runInventory 执行 `oneshot inventory` 并解析观测 JSON。
func (o *Orchestrator) runInventory(ctx context.Context, alias, agentdPath string, layout sshtransport.RemoteLayout) (*domain.ObservedState, error) {
	// --home 必须与控制面认定的远端 home 一致：受管内容根与工件缓存都挂在它下面。
	argv := []string{agentdPath, "oneshot", "inventory", "--staging", layout.Root(), "--home", layout.HomeDir}
	out, err := o.cfg.Transport.Run(ctx, alias, argv)
	if err != nil {
		return nil, err
	}
	var obs domain.ObservedState
	if err := json.Unmarshal(out.Stdout, &obs); err != nil {
		return nil, sshFailure(domain.ReasonRemoteCommandFailed, "inventory output unparseable: %v", err)
	}
	return &obs, nil
}

// ensureAgentd 返回远端可执行的 agentd 路径：优先节点已安装的（PATH），否则
// 上传控制面的临时兼容二进制（按探测到的 GOOS/GOARCH 选择，**节点侧校验 digest
// 后**才执行，FR-12.8）。
func (o *Orchestrator) ensureAgentd(ctx context.Context, alias, home, osName, arch string) (string, error) {
	if _, err := o.cfg.Transport.Run(ctx, alias, []string{"agent-fleet-agentd", "version"}); err == nil {
		return "agent-fleet-agentd", nil
	}
	if osName == "" || arch == "" {
		out, err := o.cfg.Transport.Run(ctx, alias, []string{"uname", "-s", "-m"})
		if err != nil {
			return "", err
		}
		fields := strings.Fields(string(out.Stdout))
		if len(fields) < 2 {
			return "", sshFailure(domain.ReasonUnsupportedPlatform, "uname returned %q", string(out.Stdout))
		}
		osName, arch = normalizeOS(fields[0]), normalizeArch(fields[1])
	}
	if err := supportedPlatform(osName, arch); err != nil {
		return "", err
	}
	if home == "" {
		return "", sshFailure(domain.ReasonUnsupportedPlatform,
			"agentd is not installed on the node and its home directory is unknown, so no temporary binary can be staged")
	}
	layout, err := sshtransport.NewRemoteLayout(home)
	if err != nil {
		return "", err
	}
	local := filepath.Join(o.cfg.AgentdBinDir, fmt.Sprintf("agentd-%s-%s", osName, arch))
	if o.cfg.AgentdBinDir == "" {
		return "", sshFailure(domain.ReasonUnsupportedPlatform,
			"no agentd binary directory configured (--agentd-bin-dir) for %s/%s", osName, arch)
	}
	digest, size, err := fileDigest(local)
	if err != nil {
		return "", sshFailure(domain.ReasonInstallerFailed, "agentd binary for %s/%s: %v", osName, arch, err)
	}
	remote := layout.AgentdBinary(digest)
	if _, err := o.cfg.Transport.Run(ctx, alias, []string{"test", "-x", remote}); err == nil {
		return remote, nil
	}
	if _, err := o.cfg.Transport.Run(ctx, alias, []string{"install", "-d", "-m", "700", layout.BinDir()}); err != nil {
		return "", err
	}
	if err := o.cfg.Transport.Upload(ctx, alias, local, remote); err != nil {
		return "", err
	}
	// 节点侧 digest 校验：不校验可执行文件的摘要就执行它，等于把"SSH 已加密"
	// 当作完整性证明（FR-12.8 明确禁止）。
	if err := o.verifyRemoteDigest(ctx, alias, remote, digest); err != nil {
		return "", err
	}
	if _, err := o.cfg.Transport.Run(ctx, alias, []string{"chmod", "700", remote}); err != nil {
		return "", err
	}
	o.cfg.Log.Info("temporary agentd uploaded", "machine_alias", alias, "platform", osName+"/"+arch,
		"digest", digest, "bytes", size)
	return remote, nil
}

// verifyRemoteDigest 在节点上核对刚上传文件的 SHA-256（sha256sum 或 shasum）。
func (o *Orchestrator) verifyRemoteDigest(ctx context.Context, alias, remote, want string) error {
	wantHex := strings.TrimPrefix(want, "sha256:")
	for _, argv := range [][]string{
		{"sha256sum", remote},
		{"shasum", "-a", "256", remote},
		{"openssl", "dgst", "-sha256", remote},
	} {
		out, err := o.cfg.Transport.Run(ctx, alias, argv)
		if err != nil {
			continue
		}
		fields := strings.Fields(string(out.Stdout))
		if len(fields) == 0 {
			continue
		}
		got := strings.ToLower(strings.TrimPrefix(fields[0], "SHA256("))
		if strings.HasPrefix(got, wantHex) || strings.Contains(strings.ToLower(string(out.Stdout)), wantHex) {
			return nil
		}
		return sshFailure(domain.ReasonInstallerFailed,
			"uploaded agentd digest mismatch: want %s got %s", want, fields[0])
	}
	return sshFailure(domain.ReasonInstallerFailed,
		"no digest tool (sha256sum/shasum/openssl) available on the node to verify the uploaded agentd binary")
}

// cleanupAfterRun 落实 §4.5 清理规则 3：收到节点结果（进程已退出）即清理该次输入；
// 未确认节点停止时**保留**输入。清理失败不是操作失败，但要告警（规则 4）。
func (o *Orchestrator) cleanupAfterRun(ctx context.Context, alias string, layout sshtransport.RemoteLayout, opID string, nodeConfirmed bool) {
	dec := sshtransport.PostRunCleanup(nodeConfirmed)
	if !dec.Clean {
		o.cfg.Log.Warn("staging retained", "operation_id", opID, "reason", dec.Reason)
		return
	}
	if _, err := o.cfg.Transport.Run(ctx, alias, layout.CleanBundleArgv()); err != nil {
		o.cfg.Log.Warn("staging cleanup after run failed (operation result unaffected)",
			"operation_id", opID, "err", err)
	}
}

// transportFailure 把传输层错误翻译为 SSH 路径的失败语义：在远端命令**启动前**
// 失败（DNS/host-key/认证/连接）→ 可安全判 Failed；已经启动但结果不可知 → Unknown。
func (o *Orchestrator) transportFailure(err error, phase string) error {
	reason := domain.ReasonOf(err)
	switch reason {
	case domain.ReasonDNSResolveFailed, domain.ReasonHostKeyVerificationFailed,
		domain.ReasonAuthenticationFailed, domain.ReasonConnectionTimeout:
		// 连接阶段失败：远端命令不可能已经开始执行。
		return &reconcile.SSHFailure{Reason: reason, Message: phase + ": " + err.Error()}
	case domain.ReasonRemoteCommandFailed:
		// 远端命令已启动而失败：可能已产生部分写入，保守置 Unknown，
		// 保留输入等待节点结果重报或操作者显式处置（FR-15.4）。
		return &reconcile.SSHFailure{Reason: reason, Message: phase + ": " + err.Error(), Unknown: true}
	default:
		return &reconcile.SSHFailure{Reason: reason, Message: phase + ": " + err.Error()}
	}
}

// alias 解析机器使用的 SSH 目标名：显式 hostAlias 优先，否则用机器名（OpenSSH
// 别名即机器名，操作者在自己的配置里指向真实主机）。
func (o *Orchestrator) alias(ctx context.Context, machine string) (string, error) {
	m, err := o.cfg.Machines.Get(ctx, machine)
	if err != nil {
		return "", err
	}
	core, err := machineCore(m)
	if err != nil {
		return "", err
	}
	if core.SSH.HostAlias != "" {
		return core.SSH.HostAlias, nil
	}
	return machine, nil
}

func (o *Orchestrator) homeDir(ctx context.Context, machine string) (string, error) {
	m, err := o.cfg.Machines.Get(ctx, machine)
	if err != nil {
		return "", err
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		return "", err
	}
	if st.HomeDir != "" {
		return st.HomeDir, nil
	}
	// 尚未探测过：先探测一次（探测会把 homeDir 写入 status）。
	rep, err := o.Probe(ctx, machine)
	if err != nil {
		return "", err
	}
	if err := o.recordProbe(ctx, machine, rep); err != nil {
		return "", err
	}
	return rep.HomeDir, nil
}

// recordProbe 把探测结论写入 Machine status（SSHReachable 的唯一置位方是 Machine
// 控制器；本方法只是编排入口的转发，HTTP 层与 reconcile 层共用）。
func (o *Orchestrator) recordProbe(ctx context.Context, machine string, rep *ProbeReport) error {
	now := o.cfg.Now()
	return o.cfg.Status.UpdateStatus(ctx, machine, func(st *domain.MachineStatus) error {
		st.LastProbeAt = &now
		if rep.OS != "" {
			st.OS = rep.OS
		}
		if rep.Arch != "" {
			st.Arch = rep.Arch
		}
		if rep.Hostname != "" {
			st.Hostname = rep.Hostname
		}
		if rep.HomeDir != "" {
			st.HomeDir = rep.HomeDir
		}
		if rep.AgentdVersion != "" {
			st.AgentdVersion = rep.AgentdVersion
		}
		st.SetCondition(domain.ConditionSSHReachable, domain.ConditionTrue, "SSHProbeSucceeded",
			"ssh probe succeeded", now)
		return nil
	})
}

// RecordProbeFailure 写入 SSHReachable=False 与 §30.1 的 reason（§4.2 条件表）。
func (o *Orchestrator) RecordProbeFailure(ctx context.Context, machine string, err error) error {
	now := o.cfg.Now()
	reason := domain.ReasonOf(err)
	return o.cfg.Status.UpdateStatus(ctx, machine, func(st *domain.MachineStatus) error {
		st.LastProbeAt = &now
		st.SetCondition(domain.ConditionSSHReachable, domain.ConditionFalse, reason, err.Error(), now)
		return nil
	})
}

// StoreProbe 是"探测成功后写 status"的公开入口（HTTP 层用）。
func (o *Orchestrator) StoreProbe(ctx context.Context, machine string, rep *ProbeReport) error {
	return o.recordProbe(ctx, machine, rep)
}

func (o *Orchestrator) storeObservationJSON(ctx context.Context, machine string, payload json.RawMessage, opID string, generation int64) error {
	if len(payload) == 0 || o.cfg.Inventory == nil {
		return nil
	}
	var obs domain.ObservedState
	if err := json.Unmarshal(payload, &obs); err != nil {
		return err
	}
	if obs.OperationID == "" {
		obs.OperationID = opID
	}
	rec := domain.ObservedStateRecord{
		Machine:            machine,
		InventorySeq:       obs.InventorySeq,
		OperationID:        obs.OperationID,
		ObservedGeneration: generation,
		Payload:            payload,
		RecordedAt:         o.cfg.Now(),
	}
	return o.cfg.Inventory.OnInventory(ctx, rec, obs)
}

// previousOp 找该机最近一条"不是当前操作"的操作（清理判定用，§4.5 规则 1）。
func (o *Orchestrator) previousOp(ctx context.Context, machine, currentOpID string) *domain.Operation {
	if o.cfg.Ops == nil {
		return nil
	}
	ops, err := o.cfg.Ops.ListByMachine(ctx, machine)
	if err != nil {
		return nil
	}
	var candidates []*domain.Operation
	for _, op := range ops {
		if op.Metadata.Name == currentOpID {
			continue
		}
		candidates = append(candidates, op)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Metadata.CreationTimestamp.Before(candidates[j].Metadata.CreationTimestamp)
	})
	return candidates[len(candidates)-1]
}

func machineCore(m *domain.Machine) (domain.MachineCoreSpec, error) {
	var core domain.MachineCoreSpec
	if len(m.SpecJSON()) == 0 {
		return core, nil
	}
	if err := json.Unmarshal(m.SpecJSON(), &core); err != nil {
		return core, domain.Coded(domain.ReasonInvalid, "machine %s spec: %v", m.Metadata.Name, err)
	}
	return core, nil
}

func applyMachineInfo(rep *ProbeReport, obs *domain.ObservedState) {
	if obs.Machine == nil {
		return
	}
	rep.OS, rep.Arch = obs.Machine.OS, obs.Machine.Arch
	rep.Hostname, rep.HomeDir = obs.Machine.Hostname, obs.Machine.HomeDir
}

func normalizeOS(s string) string {
	switch strings.ToLower(s) {
	case "linux":
		return "linux"
	case "darwin":
		return "darwin"
	default:
		return strings.ToLower(s)
	}
}

func normalizeArch(s string) string {
	switch strings.ToLower(s) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return strings.ToLower(s)
	}
}

func supportedPlatform(goos, goarch string) error {
	switch goos {
	case "linux", "darwin":
	default:
		return sshtransport.UnsupportedPlatformError(goos, goarch, "only linux and darwin are supported")
	}
	switch goarch {
	case "amd64", "arm64":
		return nil
	default:
		return sshtransport.UnsupportedPlatformError(goos, goarch, "unsupported architecture")
	}
}

func parseResult(stdout []byte) (*opResultJSON, error) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return nil, errors.New("empty stdout")
	}
	// 容错：取最后一个 JSON 对象（stdout 可能被远端 shell 的提示污染）。
	if idx := strings.LastIndex(trimmed, "{"); idx > 0 {
		if res, err := decodeResult(trimmed[idx:]); err == nil {
			return res, nil
		}
	}
	return decodeResult(trimmed)
}

func decodeResult(s string) (*opResultJSON, error) {
	var res opResultJSON
	if err := json.Unmarshal([]byte(s), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// reasonOr 取节点给出的 reason；节点未给码时按"远端命令失败"归类（不虚报具体原因）。
func reasonOr(reason, _ string) string {
	if reason != "" {
		return reason
	}
	return domain.ReasonRemoteCommandFailed
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), info.Size(), nil
}

// ---- OpenSSH include 导出（FR-12.6/§7.7） ----

// RenderInclude 渲染 `~/.ssh/agent-fleet.conf`：仅为 Fleet 持有显式连接字段的
// 机器导出；经既有 hostAlias 导入的机器不重复导出（§7.7 渲染来源）。
func (o *Orchestrator) RenderInclude(ctx context.Context) (*sshtransport.IncludeRender, error) {
	machines, err := o.cfg.Machines.List(ctx)
	if err != nil {
		return nil, err
	}
	return sshtransport.RenderInclude(machines, o.cfg.Now())
}

// InstallInclude 是"显式确认后安装/更新 include 文件"（FR-12.6 第三个动作）：
// 只写 ~/.ssh/agent-fleet.conf，**绝不**改写操作者主配置；同时在 data-dir 的
// generated/ssh/ 下留一份同内容产物（§7.7 产物路径），便于审计与 diff。
func (o *Orchestrator) InstallInclude(ctx context.Context, confirm bool) (string, error) {
	if !confirm {
		return "", domain.Coded(domain.ReasonInvalid, "include install requires explicit confirmation")
	}
	render, err := o.RenderInclude(ctx)
	if err != nil {
		return "", err
	}
	home := o.cfg.OperatorHome
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", domain.Coded(domain.ReasonConfigWriteFailed, "resolve operator home: %v", err)
		}
		home = h
	}
	target := sshtransport.InstallTarget(home)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", domain.Coded(domain.ReasonConfigWriteFailed, "create %s: %v", filepath.Dir(target), err)
	}
	if err := atomicWriteFile(target, []byte(render.Content), 0o600); err != nil {
		return "", domain.Coded(domain.ReasonConfigWriteFailed, "write %s: %v", target, err)
	}
	if o.cfg.GeneratedDir != "" {
		gen := filepath.Join(o.cfg.GeneratedDir, "ssh", "agent-fleet.conf")
		if err := os.MkdirAll(filepath.Dir(gen), 0o755); err != nil {
			return "", domain.Coded(domain.ReasonConfigWriteFailed, "create %s: %v", filepath.Dir(gen), err)
		}
		if err := atomicWriteFile(gen, []byte(render.Content), 0o644); err != nil {
			return "", domain.Coded(domain.ReasonConfigWriteFailed, "write %s: %v", gen, err)
		}
	}
	o.cfg.Log.Info("openssh include installed", "path", target,
		"included_hosts", len(render.Included), "skipped_hosts", len(render.Skipped))
	return target, nil
}

// atomicWriteFile 原子写（同目录临时文件 + rename），避免半个配置文件。
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
