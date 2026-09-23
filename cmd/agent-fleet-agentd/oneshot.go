// oneshot 子命令（架构 v1.1.2 §5.1/§9.3/§7.4、FR-12.4/FR-12.7/FR-12.8）：
//
//	agent-fleet-agentd oneshot inventory [--home P] [--data-dir P]
//	agent-fleet-agentd oneshot plan      --bundle P --operation-id ID [--staging P]
//	agent-fleet-agentd oneshot apply     --bundle P --operation-id ID [--plan-digest D] [--staging P]
//
// 与控制面共用**同一个** reconciler 与**同一把**节点执行权锁（AD-5/§5.6）：
// oneshot 不是"另一条实现路径"，只是同一实现的同步调用形态。
//
// 结果与 operationId 持久化绑定（state 目录下的 result-<opId>.json）：同一
// operationId 重复执行直接回放既有终态，绝不启动第二条流水线（FR-13.8 的
// oneshot 对应物）。stdout 输出单个 JSON 文档（控制面据此解析），日志走 stderr。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/codex"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/omp"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/opencode"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/inventory"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/reconciler"
	"github.com/shelwinnn/agent-fleet/internal/bundle"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// oneshot 退出码（控制面据此区分"节点产出了结果"与"基础设施错误"）：
//
//	0 = 产出了结果（Succeeded 或 Failed 都算；结果以 reason 字段区分）
//	2 = bundle 校验/路径/摘要失败（拒绝执行，零变更）
//	3 = 执行权被占用（NodeBusy/StaleExecutionLock，§5.6 规则 4/5）
//	1 = 其它基础设施错误（无法读取/写入本地状态等）
const (
	exitResult    = 0
	exitInfra     = 1
	exitBundle    = 2
	exitExecution = 3
)

// exitError 把退出码与错误一起上抛（main 据此退出；错误本身仍可 errors.As）。
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// withExit 给错误附加退出码（nil 安全）。
func withExit(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: code, err: err}
}

// oneshotResult 是 oneshot plan/apply 的 stdout 文档（控制面解析）。
type oneshotResult struct {
	OperationID      string          `json:"operationId"`
	Phase            string          `json:"phase"`
	Reason           string          `json:"reason,omitempty"`
	Message          string          `json:"message,omitempty"`
	TerminalModifier string          `json:"terminalModifier,omitempty"`
	PlanDigest       string          `json:"planDigest,omitempty"`
	Baseline         *baselineJSON   `json:"baseline,omitempty"`
	ReadOnly         bool            `json:"readOnly"`
	Verify           *verifyJSON     `json:"verify,omitempty"`
	ObservedState    json.RawMessage `json:"observedState,omitempty"`
	StartedAt        time.Time       `json:"startedAt"`
	FinishedAt       time.Time       `json:"finishedAt"`
	// BundleDigest / SnapshotDigest 便于控制面核对"节点校验的正是它构建的那一份"。
	BundleDigest   string `json:"bundleDigest,omitempty"`
	SnapshotDigest string `json:"snapshotDigest,omitempty"`
	// Error 仅用于基础设施错误（exit != 0 时），不替代 reason。
	Error string `json:"error,omitempty"`
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

// planRecord 是 oneshot plan 落在 staging 的计划记录：apply 据此比对基线
// （FR-12.7：确认的对象是具体计划，apply 前必须重取观测、重算 plan、比对基线）。
type planRecord struct {
	OperationID    string       `json:"operationId"`
	PlanDigest     string       `json:"planDigest"`
	Baseline       baselineJSON `json:"baseline"`
	Generation     int64        `json:"generation"`
	SnapshotDigest string       `json:"snapshotDigest"`
	BundleDigest   string       `json:"bundleDigest"`
	CreatedAt      time.Time    `json:"createdAt"`
}

type oneshotFlags struct {
	bundlePath string
	opID       string
	planDigest string
	stagingDir string
	home       string
	dataDir    string
}

func registerOneshotFlags(fs *flag.FlagSet) *oneshotFlags {
	f := &oneshotFlags{}
	fs.StringVar(&f.bundlePath, "bundle", "", "bundle 目录（plan/apply 必填）")
	fs.StringVar(&f.opID, "operation-id", "", "控制面下发的 operationId（必填；结果与它持久化绑定）")
	fs.StringVar(&f.planDigest, "plan-digest", "", "apply：操作者确认的 planDigest（FR-12.7）")
	fs.StringVar(&f.stagingDir, "staging", "", "staging 根（默认 <home>/.local/share/agent-fleet/staging）")
	fs.StringVar(&f.home, "home", "", "受管内容解析根（护栏 #12；默认 $HOME）")
	fs.StringVar(&f.dataDir, "data-dir", "", "节点数据目录（默认 <home>/.local/share/agent-fleet）")
	return f
}

func (f *oneshotFlags) resolveHome() (string, error) {
	if f.home != "" {
		return f.home, nil
	}
	return os.UserHomeDir()
}

func (f *oneshotFlags) resolveDataDir(home string) string {
	if f.dataDir != "" {
		return f.dataDir
	}
	return filepath.Join(home, ".local", "share", "agent-fleet")
}

func (f *oneshotFlags) resolveStaging(home string) string {
	if f.stagingDir != "" {
		return f.stagingDir
	}
	return filepath.Join(f.resolveDataDir(home), "staging")
}

// cmdOneshotInventory 输出观测状态 JSON（§5.1；SSH-only 路径的 inventory 入口）。
func cmdOneshotInventory(args []string) error {
	fs := flag.NewFlagSet("oneshot inventory", flag.ExitOnError)
	f := registerOneshotFlags(fs)
	fs.Parse(args) //nolint:errcheck // ExitOnError
	home, err := f.resolveHome()
	if err != nil {
		return err
	}
	collector := newOneshotCollector(home, f.resolveDataDir(home))
	obs, err := collector.Collect(context.Background())
	if err != nil {
		return err
	}
	return writeJSON(os.Stdout, obs)
}

// cmdOneshotPlan 校验 bundle → 取得执行权 → 只读流水线（validate→inventory→plan）
// → 记录基线 → 输出计划（含 planDigest）。节点**不持有**控制面互斥；执行权在
// 本进程退出前释放（§6.4 契约：AwaitingConfirmation 期间节点不持有执行权）。
func cmdOneshotPlan(args []string) error {
	fs := flag.NewFlagSet("oneshot plan", flag.ExitOnError)
	f := registerOneshotFlags(fs)
	fs.Parse(args) //nolint:errcheck // ExitOnError
	home, err := f.resolveHome()
	if err != nil {
		return err
	}
	dataDir := f.resolveDataDir(home)
	staging := f.resolveStaging(home)
	if err := requireOneshotInputs(f); err != nil {
		return err
	}
	man, exitCode, err := verifyBundle(f.bundlePath)
	if err != nil {
		emitOneshotError(f.opID, exitCode, err)
		return withExit(exitCode, err)
	}
	collector := newOneshotCollector(home, dataDir)
	cacheDir := filepath.Join(dataDir, "skills")
	if _, err := bundle.InstallArtifacts(f.bundlePath, cacheDir, man, bundle.DefaultLimits()); err != nil {
		emitOneshotError(f.opID, exitBundle, err)
		return withExit(exitBundle, err)
	}
	lock := reconciler.NewExecutionLock(dataDir)
	if err := lock.Acquire("oneshot", f.opID); err != nil {
		emitOneshotError(f.opID, exitCodeForLock(err), err)
		return withExit(exitCodeForLock(err), err)
	}
	defer lock.Release() //nolint:errcheck // 释放失败只影响锁文件残留，不改变结果

	exec := newOneshotExecutor(collector)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res := exec.Run(ctx, home, reconciler.Execution{
		OperationID:  f.opID,
		Generation:   man.Generation,
		SnapshotJSON: man.Snapshot,
		ReadOnly:     true, // FR-9.7：只读流水线绝不变更
		Cancelled:    func() bool { return cancelRequested(staging, f.opID) },
		Progress:     oneshotProgress(f.opID),
	})
	out := resultToOneshot(f.opID, &res, man)
	out.ReadOnly = true
	out.ObservedState = observationFor(collector, f.opID, &res)
	if err := writePlanRecord(staging, f.opID, res, man); err != nil {
		// 计划记录写不进去 → 后续 apply 无法比对基线，此刻必须显式失败而不是
		// 让操作者确认一份无法执行的计划。
		emitOneshotError(f.opID, exitInfra, err)
		return withExit(exitInfra, err)
	}
	if err := writeJSON(os.Stdout, out); err != nil {
		return err
	}
	return nil
}

// cmdOneshotApply 落实 §9.3/FR-12.7：
//  1. 幂等：同一 operationId 已有终态结果 → 直接回放，不启动第二条流水线；
//  2. 校验 bundle（路径/名称/摘要/预算）；
//  3. 读取 plan 记录；控制面传来的 planDigest 必须与它一致（确认的是**具体计划**）；
//  4. 取得执行权（被占用即 NodeBusy 失败，不排队，§5.6 规则 4）；
//  5. 重取观测、重算 plan、比对基线（摘要 + inventorySeq），不一致 → 零变更
//     ReplanRequired；
//  6. 结果写入 result-<opId>.json（与 operationId 持久化绑定）后再释放执行权。
func cmdOneshotApply(args []string) error {
	fs := flag.NewFlagSet("oneshot apply", flag.ExitOnError)
	f := registerOneshotFlags(fs)
	fs.Parse(args) //nolint:errcheck // ExitOnError
	home, err := f.resolveHome()
	if err != nil {
		return err
	}
	dataDir := f.resolveDataDir(home)
	staging := f.resolveStaging(home)
	if err := requireOneshotInputs(f); err != nil {
		return err
	}
	// 幂等重放优先于一切校验：已终结的操作不得因 bundle 被清理而变成"另一个结果"。
	if prev, ok := loadOneshotResult(staging, f.opID); ok {
		fmt.Fprintln(os.Stderr, "oneshot apply: replaying recorded result (idempotent)")
		return writeJSON(os.Stdout, prev)
	}
	man, exitCode, err := verifyBundle(f.bundlePath)
	if err != nil {
		emitOneshotError(f.opID, exitCode, err)
		return withExit(exitCode, err)
	}
	plan, err := loadPlanRecord(staging, f.opID)
	if err != nil {
		// 没有计划记录（或记录不可解析）→ 必须重新 plan 与重新确认（FR-12.7）。
		e := domain.Coded(domain.ReasonReplanRequired, "%v", err)
		emitOneshotError(f.opID, exitResult, e)
		return writeJSON(os.Stdout, failedResult(f.opID, domain.ReasonReplanRequired, e.Error()))
	}
	if f.planDigest != "" && plan.PlanDigest != f.planDigest {
		e := domain.Coded(domain.ReasonReplanRequired,
			"confirmed planDigest %s does not match the recorded plan %s", f.planDigest, plan.PlanDigest)
		emitOneshotError(f.opID, exitResult, e)
		return writeJSON(os.Stdout, failedResult(f.opID, domain.ReasonReplanRequired, e.Error()))
	}
	if plan.BundleDigest != man.BundleDigest {
		e := domain.Coded(domain.ReasonReplanRequired,
			"bundle changed since plan (%s != %s)", plan.BundleDigest, man.BundleDigest)
		emitOneshotError(f.opID, exitResult, e)
		return writeJSON(os.Stdout, failedResult(f.opID, domain.ReasonReplanRequired, e.Error()))
	}
	collector := newOneshotCollector(home, dataDir)
	cacheDir := filepath.Join(dataDir, "skills")
	if _, err := bundle.InstallArtifacts(f.bundlePath, cacheDir, man, bundle.DefaultLimits()); err != nil {
		emitOneshotError(f.opID, exitBundle, err)
		return withExit(exitBundle, err)
	}
	// 期望快照落盘：后续周期 inventory（daemon 模式）以同一代作为期望侧输入。
	if err := collector.SaveDesired(man.Snapshot); err != nil {
		emitOneshotError(f.opID, exitInfra, err)
		return withExit(exitInfra, err)
	}
	lock := reconciler.NewExecutionLock(dataDir)
	if err := lock.Acquire("oneshot", f.opID); err != nil {
		emitOneshotError(f.opID, exitCodeForLock(err), err)
		return withExit(exitCodeForLock(err), err)
	}
	exec := newOneshotExecutor(collector)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res := exec.Run(ctx, home, reconciler.Execution{
		OperationID:  f.opID,
		Generation:   man.Generation,
		SnapshotJSON: man.Snapshot,
		RequireBaseline: &reconciler.Baseline{
			ObservedProjectionDigest: plan.Baseline.ObservedProjectionDigest,
			InventorySeq:             plan.Baseline.InventorySeq,
		},
		Cancelled: func() bool { return cancelRequested(staging, f.opID) },
		Progress:  oneshotProgress(f.opID),
	})
	out := resultToOneshot(f.opID, &res, man)
	out.ObservedState = observationFor(collector, f.opID, &res)
	// 结果先落盘（绑定 operationId），再释放执行权（§5.6 规则 2：释放的依据是
	// "进程已完成并记录了结果"）。
	if err := writeOneshotResult(staging, f.opID, out); err != nil {
		emitOneshotError(f.opID, exitInfra, err)
		lock.Release() //nolint:errcheck
		return withExit(exitInfra, err)
	}
	if err := lock.Release(); err != nil {
		fmt.Fprintln(os.Stderr, "oneshot apply: release execution lock:", err)
	}
	return writeJSON(os.Stdout, out)
}

// ---- 装配与工具 ----

func requireOneshotInputs(f *oneshotFlags) error {
	if f.bundlePath == "" {
		return errors.New("oneshot: --bundle is required")
	}
	if !validOnehotOpID(f.opID) {
		return fmt.Errorf("oneshot: --operation-id %q is missing or invalid", f.opID)
	}
	return nil
}

// validOnehotOpID 与 sshtransport.ValidOperationID 同规则（避免节点侧再引控制面包）。
func validOnehotOpID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return !(len(id) >= 2 && id[0] == '.' && id[1] == '.')
}

func newOneshotCollector(home, dataDir string) *inventory.Collector {
	reg := adapter.NewRegistry()
	reg.Register(codex.New())
	reg.Register(omp.New())
	reg.Register(opencode.New())
	return &inventory.Collector{
		DataDir: dataDir, AgentdVersion: agentdVersion, Registry: reg, Home: home,
	}
}

func newOneshotExecutor(collector *inventory.Collector) *reconciler.Executor {
	return reconciler.New(reconciler.Options{
		Registry: collector.AdapterRegistry(),
		Seq:      collector.NextSeq,
		Now:      time.Now,
	})
}

// oneshotProgress 把流水线步骤进度写到 stderr（stdout 只承载结果 JSON）。
func oneshotProgress(opID string) func(step, phase, message string) {
	return func(step, phase, message string) {
		fmt.Fprintf(os.Stderr, "oneshot[%s] %s %s %s\n", opID, step, phase, message)
	}
}

// observationFor 组装随结果上行的观测（§4.4 门禁条件 2/3/4 的证据形态）：
// 投影/序/健康与流水线证据同源，operationId 与其持久化绑定。
func observationFor(collector *inventory.Collector, opID string, res *reconciler.Result) json.RawMessage {
	ev := &domain.VerifyEvidence{AdapterHealth: domain.AdapterHealthSkipped}
	if res.Verify != nil {
		ev = res.Verify
	} else if res.Projection != nil {
		ev = &domain.VerifyEvidence{
			DesiredProjectionDigest:  res.Projection.DesiredDigest,
			ObservedProjectionDigest: res.Projection.ObservedDigest,
			CanonicalizationVersion:  res.Projection.CanonicalizationVersion,
			InventorySeq:             res.Projection.Seq,
			AdapterHealth:            domain.AdapterHealthSkipped,
		}
	}
	obs := collector.CollectForOperation(opID, ev)
	b, err := json.Marshal(obs)
	if err != nil {
		return nil
	}
	return b
}

// verifyBundle 校验 bundle 并把 §30 reason 映射为退出码。
func verifyBundle(dir string) (*bundle.Manifest, int, error) {
	man, err := bundle.Verify(dir, bundle.DefaultLimits())
	if err != nil {
		return nil, exitBundle, err
	}
	return man, exitResult, nil
}

func exitCodeForLock(err error) int {
	var held *reconciler.LockHeldError
	if errors.As(err, &held) {
		return exitExecution
	}
	var stale *reconciler.StaleLockError
	if errors.As(err, &stale) {
		return exitExecution
	}
	return exitInfra
}

// cancelRequested 观察取消标记（§5.1：SSH-only 没有 CancelOperation 通道，控制面
// 经 SSH 落标记文件；取消只在阶段边界生效，FR-13.9）。
func cancelRequested(staging, opID string) bool {
	if staging == "" || opID == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(staging, "cancel-"+opID+".json"))
	return err == nil
}

func resultToOneshot(opID string, res *reconciler.Result, man *bundle.Manifest) *oneshotResult {
	out := &oneshotResult{
		OperationID:      opID,
		Phase:            res.Phase,
		Reason:           res.Reason,
		Message:          res.Message,
		TerminalModifier: res.TerminalModifier,
		PlanDigest:       res.PlanDigest,
		ReadOnly:         res.ReadOnly,
		StartedAt:        res.StartedAt,
		FinishedAt:       res.FinishedAt,
	}
	if man != nil {
		out.BundleDigest = man.BundleDigest
		out.SnapshotDigest = man.SnapshotDigest
	}
	if res.Baseline != nil {
		out.Baseline = &baselineJSON{
			ObservedProjectionDigest: res.Baseline.ObservedProjectionDigest,
			InventorySeq:             res.Baseline.InventorySeq,
		}
	}
	if res.Verify != nil {
		out.Verify = &verifyJSON{
			DesiredProjectionDigest:  res.Verify.DesiredProjectionDigest,
			ObservedProjectionDigest: res.Verify.ObservedProjectionDigest,
			CanonicalizationVersion:  res.Verify.CanonicalizationVersion,
			InventorySeq:             res.Verify.InventorySeq,
			AdapterHealth:            res.Verify.AdapterHealth,
		}
	}
	return out
}

func failedResult(opID, reason, message string) *oneshotResult {
	now := time.Now().UTC()
	return &oneshotResult{
		OperationID: opID, Phase: domain.OperationPhaseFailed, Reason: reason,
		Message: message, StartedAt: now, FinishedAt: now,
	}
}

// emitOneshotError 把基础设施错误写成 stdout JSON（控制面即使看到非零退出码也能
// 取得 reason code），并打一行 stderr 日志。
func emitOneshotError(opID string, exitCode int, err error) {
	reason := domain.ReasonOf(err)
	out := failedResult(opID, reason, err.Error())
	out.Error = err.Error()
	_ = writeJSON(os.Stdout, out)
	fmt.Fprintf(os.Stderr, "oneshot error (exit=%d): %s\n", exitCode, err)
}

func writeJSON(w *os.File, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writePlanRecord 原子写计划记录（staging 固定根下，与 bundle/ 分离）。
func writePlanRecord(staging, opID string, res reconciler.Result, man *bundle.Manifest) error {
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	rec := planRecord{
		OperationID: opID, PlanDigest: res.PlanDigest, Generation: man.Generation,
		SnapshotDigest: man.SnapshotDigest, BundleDigest: man.BundleDigest,
		CreatedAt: time.Now().UTC(),
	}
	if res.Baseline != nil {
		rec.Baseline = baselineJSON{
			ObservedProjectionDigest: res.Baseline.ObservedProjectionDigest,
			InventorySeq:             res.Baseline.InventorySeq,
		}
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(staging, "plan-"+opID+".json"), append(b, '\n'), 0o600)
}

func loadPlanRecord(staging, opID string) (*planRecord, error) {
	b, err := os.ReadFile(filepath.Join(staging, "plan-"+opID+".json"))
	if err != nil {
		return nil, fmt.Errorf("plan record for %s is unavailable: %w", opID, err)
	}
	var rec planRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("plan record for %s is unparseable: %w", opID, err)
	}
	if rec.PlanDigest == "" {
		return nil, fmt.Errorf("plan record for %s carries no planDigest", opID)
	}
	return &rec, nil
}

func writeOneshotResult(staging, opID string, out *oneshotResult) error {
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(staging, "result-"+opID+".json"), append(b, '\n'), 0o600)
}

func loadOneshotResult(staging, opID string) (*oneshotResult, bool) {
	b, err := os.ReadFile(filepath.Join(staging, "result-"+opID+".json"))
	if err != nil {
		return nil, false
	}
	var out oneshotResult
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	if out.OperationID != opID || out.Phase == "" {
		return nil, false
	}
	return &out, true
}
