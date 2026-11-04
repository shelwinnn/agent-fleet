package e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 本文件是 KM-51 的链路用例：每一环真实（REST → 编排控制器 → gRPC mTLS →
// NodeExecutor → NpmEngine → fake npm 受控注册表/前缀），断言页面可观察的
// REST 状态与目标环境文件系统上的实际结果。隔离纪律见包文档与 harness_test.go。

// scanOnce 推进一轮编排（页面轮询背后是控制面的扫描循环；测试里用显式单步
// 保持确定性）。
func (h *harness) scanOnce() {
	h.t.Helper()
	if err := h.lc.ScanOnce(context.Background()); err != nil {
		h.t.Fatalf("lifecycle scan: %v", err)
	}
}

// runThroughChain 是主路径的完整动作序列：REST 创建 → 确认 → 扫描派发 → 等待
// 终态，返回终态响应（页面从创建响应开始跟踪，到轮询出终态）。
func (h *harness) runThroughChain(opType, key, version string, targets []domain.LifecycleTarget) map[string]any {
	h.t.Helper()
	code, body := h.createOperation(opType, key, version, targets)
	if code != http.StatusCreated {
		h.t.Fatalf("create %s operation = %d: %v", opType, code, body)
	}
	opID := opIDOf(h.t, body)
	if code, body := h.confirmOperation(opID); code != http.StatusOK {
		h.t.Fatalf("confirm %s = %d: %v", opID, code, body)
	}
	h.scanOnce()
	return h.waitOperation(opID)
}

// TestE2EMainPathsInstallUpgradeUninstall：浏览器发起 → 控制面 → 节点 → 目标
// 的三条主路径（安装 / 升级 / 卸载），外加一次检查更新。断言全部落在页面
// 可观察的 REST 字段与目标前缀的实际文件状态上。
func TestE2EMainPathsInstallUpgradeUninstall(t *testing.T) {
	env := e2eEnv(t)
	for _, v := range []string{"0.44.0", "0.45.0"} {
		if err := env.Publish(codexPackage, v, "codex", "codex-cli "+v); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-main"
	h.seedMachine(machine)
	h.connectNode(machine)

	// 页面的意图写入：PUT desired（present@0.44.0）→ GET 可读回。
	code, body := h.restDo(http.MethodPut, "/api/v1/machines/"+machine+"/lifecycle/desired",
		`{"entries":{"codex":{"intent":"present","version":"0.44.0","method":"npm","package":"@openai/codex"}}}`)
	if code != http.StatusOK {
		t.Fatalf("put desired = %d: %v", code, body)
	}
	code, body = h.restDo(http.MethodGet, "/api/v1/machines/"+machine+"/lifecycle/desired", "")
	if code != http.StatusOK {
		t.Fatalf("get desired = %d: %v", code, body)
	}
	entries, _ := body["entries"].(map[string]any)
	if _, ok := entries["codex"]; !ok {
		t.Fatalf("desired entry missing after put: %v", body)
	}

	// 1) 安装 0.44.0：创建响应 pending 且未确认（预览确认前不得派发）。
	code, body = h.createOperation(domain.LifecycleOpInstall, "e2e-install-1", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if code != http.StatusCreated || body["duplicate"] != false {
		t.Fatalf("create install = %d duplicate=%v: %v", code, body["duplicate"], body)
	}
	installID := opIDOf(t, body)
	code, body = h.getOperation(installID)
	if code != http.StatusOK {
		t.Fatalf("get install = %d: %v", code, body)
	}
	if status := body["status"].(map[string]any); status["confirmedAt"] != nil {
		t.Fatalf("confirmedAt must be empty before preview confirm: %v", status["confirmedAt"])
	}
	if state, _ := targetState(t, body, codexPackage); state != domain.TargetPending {
		t.Fatalf("fresh install target state = %s, want pending", state)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "" {
		t.Fatalf("nothing installed yet, prefix has %s", got)
	}

	// 预览确认 → 派发执行 → 终态 succeeded + 实测版本证据；目标前缀真实落位。
	if code, body := h.confirmOperation(installID); code != http.StatusOK {
		t.Fatalf("confirm install = %d: %v", code, body)
	}
	h.scanOnce()
	body = h.waitOperation(installID)
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("install result = %s/%s: %v", state, reason, body)
	}
	if ev := evidenceVersion(t, body, codexPackage); ev != "0.44.0" {
		t.Fatalf("install evidence version = %q, want 0.44.0", ev)
	}
	if spec := body["spec"].(map[string]any); spec["version"] != "0.44.0" {
		t.Fatalf("spec version drifted: %v", spec["version"])
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "0.44.0" {
		t.Fatalf("installed version on target = %q, want 0.44.0", got)
	}
	if _, err := os.Stat(shimPath(env.Prefix, "codex")); err != nil {
		t.Fatalf("codex shim missing after install: %v", err)
	}

	// 2) 检查更新（latest=0.45.0）：逐目标 succeeded，证据携带上游版本与查询
	// 时间——页面据此展示"有更新可用"，而不是把已知版本当最新。
	body = h.runThroughChain(domain.LifecycleOpCheckUpdate, "e2e-check-1", "",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("check-update result = %s/%s: %v", state, reason, body)
	}
	if ev := evidenceVersion(t, body, codexPackage); ev != "0.45.0" {
		t.Fatalf("check-update upstream version = %q, want 0.45.0", ev)
	}
	if msg, _ := firstResult(t, body, codexPackage)["message"].(string); msg == "" {
		t.Fatal("check-update evidence must carry channel/queriedAt message")
	}

	// 3) 升级到固定版本 0.45.0：实际版本随之变化。
	body = h.runThroughChain(domain.LifecycleOpUpgrade, "e2e-upgrade-1", "0.45.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("upgrade result = %s/%s: %v", state, reason, body)
	}
	if ev := evidenceVersion(t, body, codexPackage); ev != "0.45.0" {
		t.Fatalf("upgrade evidence version = %q, want 0.45.0", ev)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "0.45.0" {
		t.Fatalf("installed version after upgrade = %q, want 0.45.0", got)
	}

	// 4) 卸载（按已识别来源）：实例确已消失，程序本体文件清空。
	body = h.runThroughChain(domain.LifecycleOpUninstall, "e2e-uninstall-1", "",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedAbsent {
		t.Fatalf("uninstall result = %s/%s: %v", state, reason, body)
	}
	if ev := evidenceVersion(t, body, codexPackage); ev != "" {
		t.Fatalf("uninstall evidence must have empty version, got %q", ev)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "" {
		t.Fatalf("instance still present after uninstall: %s", got)
	}
	if _, err := os.Stat(shimPath(env.Prefix, "codex")); err == nil {
		t.Fatal("codex shim still present after uninstall")
	}
}

// TestE2EFixedVersionSurvivesMidExecutionDrift：创建时固定 0.44.0，注入
// "执行中出现新版本"（引擎进入安装的瞬间上游发布 0.46.0 并把 latest 指过去）
// ——本次目标与实测结果不得改变；更新在界面上可见，但不会被顺带执行。
func TestE2EFixedVersionSurvivesMidExecutionDrift(t *testing.T) {
	env := e2eEnv(t)
	if err := env.Publish(codexPackage, "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-pin"
	h.seedMachine(machine)
	pump := h.connectNode(machine)

	// 故障注入：安装命令进入引擎的那一刻，上游漂移到 0.46.0。
	pump.engine.setOnInstall(func(string) {
		if err := env.Publish(codexPackage, "0.46.0", "codex", "codex-cli 0.46.0"); err != nil {
			t.Errorf("publish drift version: %v", err)
		}
		if err := env.SetLatest(codexPackage, "0.46.0"); err != nil {
			t.Errorf("flip latest tag: %v", err)
		}
	})

	body := h.runThroughChain(domain.LifecycleOpInstall, "e2e-pin-1", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("pinned install = %s/%s: %v", state, reason, body)
	}
	if spec := body["spec"].(map[string]any); spec["version"] != "0.44.0" {
		t.Fatalf("fixed version changed in operation record: %v", spec["version"])
	}
	if ev := evidenceVersion(t, body, codexPackage); ev != "0.44.0" {
		t.Fatalf("verified version = %q, want 0.44.0 despite upstream drift", ev)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "0.44.0" {
		t.Fatalf("target has %q, want 0.44.0 — drift leaked into execution", got)
	}

	// 更新可见（检查更新读上游 0.46.0），但目标未被顺带改变——固定版本意图
	// 与"有更新可用"是两回事。
	code, check := h.restDo(http.MethodPost, "/api/v1/agents/codex/upstream-versions/check", "")
	if code != http.StatusOK {
		t.Fatalf("upstream check = %d: %v", code, check)
	}
	raw := upstreamNpmRaw(t, check)
	if raw != "0.46.0" {
		t.Fatalf("upstream report shows %q, want drifted 0.46.0", raw)
	}
}

// TestE2EUpstreamQueryFailureUnknownVsCacheFallback：上游查询失败必须全链路
// 可见且可区分——没成功查过 → "未知"（无缓存回退）；成功查过再失败 →
// failed + 最近一次成功缓存及其时间。解析失败的版本串不得当成功。
func TestE2EUpstreamQueryFailureUnknownVsCacheFallback(t *testing.T) {
	env := e2eEnv(t)
	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-upstream"
	h.seedMachine(machine) // CheckUpdate 只读上游、不经节点：无需连线

	// 1) 上游返回不可解释的版本串（解析失败）且从未成功查过：
	//    CheckUpdate 目标 failed(discovery-failed)——未知不得计成功。
	if err := env.SetLatest(codexPackage, "cheese"); err != nil {
		t.Fatal(err)
	}
	body := h.runThroughChain(domain.LifecycleOpCheckUpdate, "e2e-up-unknown", "",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetFailed || reason != domain.ReasonDiscoveryFailed {
		t.Fatalf("unparseable upstream must fail the check, got %s/%s: %v", state, reason, body)
	}

	// 2) 上游正常一次（进缓存），随后查询失败：REST 报告 failed +
	//    LastSuccess（版本 + queriedAt）——"缓存回退"与"未知"可区分。
	if err := env.SetLatest(codexPackage, "0.44.0"); err != nil {
		t.Fatal(err)
	}
	code, report := h.restDo(http.MethodPost, "/api/v1/agents/codex/upstream-versions/check", "")
	if code != http.StatusOK {
		t.Fatalf("upstream check = %d: %v", code, report)
	}
	if raw := upstreamNpmRaw(t, report); raw != "0.44.0" {
		t.Fatalf("controlled source returned %q, want 0.44.0", raw)
	}

	if err := env.SetLatest(codexPackage, "cheese"); err != nil {
		t.Fatal(err)
	}
	code, report = h.restDo(http.MethodPost, "/api/v1/agents/codex/upstream-versions/check", "")
	if code != http.StatusOK {
		t.Fatalf("upstream check after failure = %d: %v", code, report)
	}
	src := upstreamNpmSource(t, report)
	if src["status"] != "failed" {
		t.Fatalf("npm source status = %v, want failed", src["status"])
	}
	failure, _ := src["failure"].(map[string]any)
	if failure["reason"] != "version_parse_failed" {
		t.Fatalf("failure reason = %v, want version_parse_failed", failure["reason"])
	}
	last, _ := src["lastSuccess"].(map[string]any)
	if last == nil {
		t.Fatalf("failed check must surface last-success cache: %v", src)
	}
	lastVer, _ := last["version"].(map[string]any)
	if lastVer["raw"] != "0.44.0" {
		t.Fatalf("cached version = %v, want 0.44.0", lastVer["raw"])
	}
	if at, _ := last["queriedAt"].(string); at == "" {
		t.Fatal("cached success must carry queriedAt (cache never masquerades as a fresh query)")
	}

	// 3) 同一失败状态下再走 CheckUpdate 操作：目标 failed(discovery-failed)，
	//    message 携带缓存时间（页面把"未知 ≠ 最新"讲清楚）。
	body = h.runThroughChain(domain.LifecycleOpCheckUpdate, "e2e-up-cache", "",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetFailed || reason != domain.ReasonDiscoveryFailed {
		t.Fatalf("failed query must fail the check op, got %s/%s", state, reason)
	}
	res := firstResult(t, body, codexPackage)
	if msg, _ := res["message"].(string); msg == "" {
		t.Fatal("discovery failure must carry a reason message for the page")
	}

	// 4) 真实的查询命令失败（.fail-view 注入：非零退出）同链路可见：
	//    REST 报告 failed(query_failed) 且缓存回退仍在——"查不到"是逐源业务
	//    结果，不是传输层错误；解除注入后查询恢复。
	if err := env.SetFlag(".fail-view", true); err != nil {
		t.Fatal(err)
	}
	code, report = h.restDo(http.MethodPost, "/api/v1/agents/codex/upstream-versions/check", "")
	if code != http.StatusOK {
		t.Fatalf("upstream check under injected failure = %d: %v", code, report)
	}
	src = upstreamNpmSource(t, report)
	if src["status"] != "failed" {
		t.Fatalf("npm source status under .fail-view = %v, want failed", src["status"])
	}
	failure, _ = src["failure"].(map[string]any)
	if failure["reason"] != "query_failed" {
		t.Fatalf("failure reason = %v, want query_failed (real command failure)", failure["reason"])
	}
	if _, ok := src["lastSuccess"].(map[string]any); !ok {
		t.Fatalf("cache fallback must survive a command failure too: %v", src)
	}
	if err := env.SetFlag(".fail-view", false); err != nil {
		t.Fatal(err)
	}
	// 解除注入并把 latest 还原为可解析版本 → 查询恢复（缓存被新鲜结果刷新）。
	if err := env.SetLatest(codexPackage, "0.44.0"); err != nil {
		t.Fatal(err)
	}
	code, report = h.restDo(http.MethodPost, "/api/v1/agents/codex/upstream-versions/check", "")
	if code != http.StatusOK {
		t.Fatalf("upstream check after clearing flag = %d: %v", code, report)
	}
	if raw := upstreamNpmRaw(t, report); raw != "0.44.0" {
		t.Fatalf("recovery check = %q, want 0.44.0", raw)
	}
}

// TestE2EPartialFailureRetryOnlyFailedLinkedToOriginal：两个目标一个失败——
// 逐目标结果各自落定；仅确定失败目标可重试（成功目标重试被拒）；重试操作
// 关联原操作、继承确认、只带失败目标；成功目标不因重试而再执行。
func TestE2EPartialFailureRetryOnlyFailedLinkedToOriginal(t *testing.T) {
	env := e2eEnv(t)
	for _, pkg := range []string{codexPackage, altPackage} {
		if err := env.Publish(pkg, "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-partial"
	h.seedMachine(machine)
	pump := h.connectNode(machine)

	// 故障注入：alt 包的引擎调用失败（真实引擎语义不变，只有这一个包失败）。
	pump.engine.setFailInstall(map[string]string{altPackage: "injected engine failure for alt package"})

	code, body := h.createOperation(domain.LifecycleOpInstall, "e2e-partial-1", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage), codexTarget(machine, altPackage)})
	if code != http.StatusCreated {
		t.Fatalf("create partial op = %d: %v", code, body)
	}
	origID := opIDOf(t, body)
	if code, body := h.confirmOperation(origID); code != http.StatusOK {
		t.Fatalf("confirm = %d: %v", code, body)
	}
	h.scanOnce()
	body = h.waitOperation(origID)

	state, reason := targetState(t, body, codexPackage)
	if state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("main target = %s/%s, want succeeded: %v", state, reason, body)
	}
	state, reason = targetState(t, body, altPackage)
	if state != domain.TargetFailed || reason != domain.ReasonOpFailed {
		t.Fatalf("alt target = %s/%s, want failed(op-failed): %v", state, reason, body)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "0.44.0" {
		t.Fatalf("main target not installed: %q", got)
	}
	if got := installedVersion(t, env.Prefix, altPackage); got != "" {
		t.Fatalf("failed target must not be installed: %q", got)
	}

	// 成功目标不可重试（仅确定失败目标可重试）。
	code, body = h.retryOperation(origID, []domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if code != http.StatusConflict {
		t.Fatalf("retry succeeded target = %d, want 409: %v", code, body)
	}

	// 失败目标重试：关联原操作（retryOf）、继承确认时刻、只携带失败目标。
	code, body = h.retryOperation(origID, []domain.LifecycleTarget{codexTarget(machine, altPackage)})
	if code != http.StatusCreated {
		t.Fatalf("retry failed target = %d: %v", code, body)
	}
	retryID := opIDOf(t, body)
	if body["retryOf"] != origID {
		t.Fatalf("retryOf = %v, want %s", body["retryOf"], origID)
	}
	if status := body["status"].(map[string]any); status["confirmedAt"] == nil {
		t.Fatal("retry of an approved operation must inherit confirmation (no re-confirm)")
	}
	results, _ := body["status"].(map[string]any)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("retry carries %d targets, want only the failed one", len(results))
	}

	// 解除注入 → 重试派发（首派带重发现：alt 缺席 → 前提成立 → 执行）→ 成功。
	pump.engine.setFailInstall(nil)
	h.scanOnce()
	retryBody := h.waitOperation(retryID)
	if state, reason := targetState(t, retryBody, altPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("retry result = %s/%s: %v", state, reason, retryBody)
	}
	if got := installedVersion(t, env.Prefix, altPackage); got != "0.44.0" {
		t.Fatalf("alt target not installed after retry: %q", got)
	}

	// 成功目标未被重试再执行（保留成功结果）；失败目标恰好执行两次（失败 +
	// 重试成功）。
	if calls := pump.engine.installCalls(codexPackage); calls != 1 {
		t.Fatalf("succeeded target executed %d times, want 1 (must not re-run on retry)", calls)
	}
	if calls := pump.engine.installCalls(altPackage); calls != 2 {
		t.Fatalf("failed target executed %d times, want 2 (initial failure + retry)", calls)
	}

	// 原操作的逐目标结果不被重试改写（追溯链完整：原样保留失败证据）。
	_, orig := h.getOperation(origID)
	if state, _ := targetState(t, orig, altPackage); state != domain.TargetFailed {
		t.Fatalf("original op mutated by retry: %v", orig)
	}
}

// TestE2EOfflineBeforeCommitNeverAutoExecutes：提交前离线的目标标记未执行，
// 节点上线后旧意图不自动执行（not_executed 无出边）；新操作走在线路径正常执行。
func TestE2EOfflineBeforeCommitNeverAutoExecutes(t *testing.T) {
	env := e2eEnv(t)
	if err := env.Publish(codexPackage, "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-offline"
	h.seedMachine(machine) // 节点不连线 = 提交前离线

	code, body := h.createOperation(domain.LifecycleOpInstall, "e2e-offline-1", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %v", code, body)
	}
	offlineID := opIDOf(t, body)
	if code, body := h.confirmOperation(offlineID); code != http.StatusOK {
		t.Fatalf("confirm = %d: %v", code, body)
	}
	h.scanOnce()

	state, reason := targetState(t, mustGet(t, h, offlineID), codexPackage)
	if state != domain.TargetNotExecuted || reason != domain.ReasonTargetOffline {
		t.Fatalf("offline target = %s/%s, want not_executed(target-offline)", state, reason)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "" {
		t.Fatalf("offline intent executed anyway: prefix has %q", got)
	}

	// 节点上线：旧意图不得复活；not_executed 保持原样。
	h.connectNode(machine)
	h.scanOnce()
	body = mustGet(t, h, offlineID)
	state, reason = targetState(t, body, codexPackage)
	if state != domain.TargetNotExecuted || reason != domain.ReasonTargetOffline {
		t.Fatalf("after node online, old intent = %s/%s, want still not_executed", state, reason)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "" {
		t.Fatalf("old intent auto-executed after reconnect: prefix has %q", got)
	}

	// 同一环境下新建操作走在线路径：成功。证明"不执行旧意图"不是链路坏了。
	body = h.runThroughChain(domain.LifecycleOpInstall, "e2e-offline-2", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("fresh online op = %s/%s: %v", state, reason, body)
	}
	if got := installedVersion(t, env.Prefix, codexPackage); got != "0.44.0" {
		t.Fatalf("fresh op did not install: %q", got)
	}
	// 旧意图至终不被执行。
	if state, _ := targetState(t, mustGet(t, h, offlineID), codexPackage); state != domain.TargetNotExecuted {
		t.Fatal("old intent executed after a later operation — not_executed must have no outgoing edge")
	}
}

// TestE2EResultLostShowsUnknownAndVerifyFirst：节点执行了但结果回不来——目标
// 显示 unknown(timeout-no-result)，不是成功；不可直接重试（先核对）；而目标
// 环境的真实状态如实存在（页面的"先核对"就是对照这个）。
func TestE2EResultLostShowsUnknownAndVerifyFirst(t *testing.T) {
	env := e2eEnv(t)
	if err := env.Publish(codexPackage, "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	// 可推进的控制器时钟：执行窗口用真实的默认预算（秒级 proto deadline 不受
	// 挤压），结果确认丢失后把时钟拨过 deadline+宽限，超时扫描确定性触发。
	var deltaNS atomic.Int64
	h := newHarness(t, env, harnessOptions{clock: func() time.Time {
		return time.Now().Add(time.Duration(deltaNS.Load()))
	}})
	const machine = "ws-lost"
	h.seedMachine(machine)
	pump := h.connectNode(machine)
	pump.dropResults.Store(true) // 故障注入：执行但结果永不上报

	code, body := h.createOperation(domain.LifecycleOpInstall, "e2e-lost-1", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %v", code, body)
	}
	opID := opIDOf(t, body)
	if code, cbody := h.confirmOperation(opID); code != http.StatusOK {
		t.Fatalf("confirm = %d: %v", code, cbody)
	}
	h.scanOnce() // 派发 running；节点执行但结果被丢弃

	// 给执行留足真实时间（默认预算 10 分钟，fake npm 毫秒级完成）。
	deadline := time.Now().Add(waitDeadline)
	for installedVersion(t, env.Prefix, codexPackage) != "0.44.0" {
		if time.Now().After(deadline) {
			t.Fatal("node execution did not land within wait deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 把控制器时钟拨过派发 deadline（10min）+ 宽限（2min）→ 扫描判 unknown。
	deltaNS.Store((13 * time.Minute).Nanoseconds())
	h.scanOnce()
	body = mustGet(t, h, opID)
	state, reason := targetState(t, body, codexPackage)
	if state != domain.TargetUnknown || reason != domain.ReasonTimeoutNoResult {
		t.Fatalf("lost-result target = %s/%s, want unknown(timeout-no-result): %v", state, reason, body)
	}
	if phase := body["status"].(map[string]any)["phase"]; phase != domain.OperationPhaseCompleted {
		t.Fatalf("unknown must settle the operation, phase = %v", phase)
	}

	// 未知不是确定失败：重试被拒（先核对实际状态，不得盲目重执行）。
	code, body = h.retryOperation(opID, []domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if code != http.StatusConflict {
		t.Fatalf("retry unknown target = %d, want 409: %v", code, body)
	}

	// 真实状态：目标确实装上了（执行发生过）——"未知"与实际状态的对照正是
	// 操作者核对的对象；断言它存在，防止把未知误渲染成"未变化"。
	if got := installedVersion(t, env.Prefix, codexPackage); got != "0.44.0" {
		t.Fatalf("target actual state = %q, want 0.44.0 (execution happened, only the result was lost)", got)
	}

	// "先核对"的正向闭环：操作者面对未知，经一次全新操作（fresh op，重新
	// 确认、重新执行、重新发现）核对实际状态——核验证据必须与磁盘实况一致，
	// 而丢失的结果既没有被复用，原操作也没有被再次派发。
	pump.dropResults.Store(false)
	body = h.runThroughChain(domain.LifecycleOpInstall, "e2e-lost-verify", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("verify op = %s/%s: %v", state, reason, body)
	}
	if ev := evidenceVersion(t, body, codexPackage); ev != "0.44.0" {
		t.Fatalf("verify evidence = %q, want the rediscovered actual version 0.44.0", ev)
	}
	if got := pump.dispatches.Load(); got != 2 {
		t.Fatalf("original op must never be re-dispatched; node saw %d dispatches, want exactly 2 (lost op + fresh verify op)", got)
	}
}

// TestE2EDuplicateSubmissionDeduplicated：重复提交同一请求复用同一操作；
// 节点恰好收到一次派发、引擎恰好执行一次——重复提交不产生重复变更。
func TestE2EDuplicateSubmissionDeduplicated(t *testing.T) {
	env := e2eEnv(t)
	if err := env.Publish(codexPackage, "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-dup"
	h.seedMachine(machine)
	pump := h.connectNode(machine)

	targets := []domain.LifecycleTarget{codexTarget(machine, codexPackage)}
	code1, body1 := h.createOperation(domain.LifecycleOpInstall, "e2e-dup-key", "0.44.0", targets)
	code2, body2 := h.createOperation(domain.LifecycleOpInstall, "e2e-dup-key", "0.44.0", targets)
	if code1 != http.StatusCreated || body1["duplicate"] != false {
		t.Fatalf("first submission = %d duplicate=%v", code1, body1["duplicate"])
	}
	if code2 != http.StatusOK || body2["duplicate"] != true {
		t.Fatalf("duplicate submission = %d duplicate=%v, want 200/true", code2, body2["duplicate"])
	}
	if opIDOf(t, body1) != opIDOf(t, body2) {
		t.Fatalf("duplicate submission created a second operation: %s vs %s", opIDOf(t, body1), opIDOf(t, body2))
	}
	opID := opIDOf(t, body1)

	if code, cbody := h.confirmOperation(opID); code != http.StatusOK {
		t.Fatalf("confirm = %d: %v", code, cbody)
	}
	h.scanOnce()
	time.Sleep(200 * time.Millisecond)
	// 复扫：无论此刻操作处于 running 还是已终态，都不得产生第二次派发
	// （running 不在派发集合；终态目标无出边）——去重归因由下面的
	// dispatches==1 断言给出。
	h.scanOnce()
	body := h.waitOperation(opID)
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("dedup install = %s/%s: %v", state, reason, body)
	}

	if got := pump.dispatches.Load(); got != 1 {
		t.Fatalf("node received %d dispatches, want exactly 1", got)
	}
	if calls := pump.engine.installCalls(codexPackage); calls != 1 {
		t.Fatalf("engine executed install %d times, want exactly 1", calls)
	}
}

// TestE2EDataProtectionFullChainAndOtherInstanceUntouched：安装/升级/卸载走
// 完整浏览器→目标链路，前后对比用户数据目录（内容、权限、符号链接、悬空链）
// 零变更；第二实例（另一前缀）全程不受影响。
func TestE2EDataProtectionFullChainAndOtherInstanceUntouched(t *testing.T) {
	env := e2eEnv(t)
	for _, v := range []string{"0.44.0", "0.45.0"} {
		if err := env.Publish(codexPackage, v, "codex", "codex-cli "+v); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.SeedCodexData(); err != nil {
		t.Fatal(err)
	}
	// 第二实例：另一前缀里的既有安装（多实例场景的"其他实例"）。
	if err := env.InstallDirect(env.Prefix2, codexPackage+"@0.44.0"); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(env.Home, ".codex")
	sharedDir := filepath.Join(env.Home, "shared")
	beforeData := snapshotOf(t, dataDir)
	beforeShared := snapshotOf(t, sharedDir)
	beforeOther := snapshotOf(t, env.Prefix2)

	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-data"
	h.seedMachine(machine)
	h.connectNode(machine)

	// 完整链路三连：安装 → 升级 → 卸载（每步都经 REST 创建/确认/派发/回灌）。
	steps := []struct {
		typ, key, version string
	}{
		{domain.LifecycleOpInstall, "e2e-data-install", "0.44.0"},
		{domain.LifecycleOpUpgrade, "e2e-data-upgrade", "0.45.0"},
		{domain.LifecycleOpUninstall, "e2e-data-uninstall", ""},
	}
	for _, s := range steps {
		body := h.runThroughChain(s.typ, s.key, s.version,
			[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
		if state, _ := targetState(t, body, codexPackage); state != domain.TargetSucceeded {
			t.Fatalf("step %s did not succeed: %v", s.typ, body)
		}
	}

	// 用户数据零变更（含 0600 凭据、相对软链、悬空软链）。
	assertNoDiff(t, "~/.codex user data", beforeData, snapshotOf(t, dataDir))
	assertNoDiff(t, "shared skill dir", beforeShared, snapshotOf(t, sharedDir))
	// 其他实例零变更，且真实存在过（对比器不是恒真）。
	assertNoDiff(t, "second instance prefix", beforeOther, snapshotOf(t, env.Prefix2))
	if got := installedVersion(t, env.Prefix2, codexPackage); got != "0.44.0" {
		t.Fatalf("other instance disturbed: %q", got)
	}
	if _, err := os.Stat(shimPath(env.Prefix2, "codex")); err != nil {
		t.Fatalf("other instance shim missing: %v", err)
	}
	// 主前缀清空（卸载确实发生，操作不是空转）。
	if got := installedVersion(t, env.Prefix, codexPackage); got != "" {
		t.Fatalf("primary instance still present after uninstall: %q", got)
	}
}

// TestE2EVersionGateRejectsNonExactAndZeroValueUpstream：版本闸的链路级证明——
// 非精确版本在创建即拒（不落库）；上游不可解析/零值版本不得以成功形态出现在
// 任何层面；非 SemVer 但可按来源规则解释的版本如实表达（scheme=source_rule）。
func TestE2EVersionGateRejectsNonExactAndZeroValueUpstream(t *testing.T) {
	env := e2eEnv(t)
	h := newHarness(t, env, harnessOptions{})
	const machine = "ws-gate"
	h.seedMachine(machine)

	// 非精确版本：REST 创建即 400（domain.IsExactVersion 闸），不产生操作。
	for _, bad := range []string{"latest", "*", "1.2", "v0.44.0", ">=1.0.0", ""} {
		code, body := h.createOperation(domain.LifecycleOpInstall, "e2e-gate-"+bad, bad,
			[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
		if code != http.StatusBadRequest {
			t.Fatalf("version %q: create = %d, want 400: %v", bad, code, body)
		}
		if reason, _ := body["reason"].(string); reason != domain.ReasonInvalid {
			t.Fatalf("version %q: reason = %v, want Invalid", bad, body["reason"])
		}
	}
	// 卸载/检查更新不得携带版本。
	code, body := h.createOperation(domain.LifecycleOpUninstall, "e2e-gate-un-ver", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if code != http.StatusBadRequest {
		t.Fatalf("uninstall with version = %d, want 400", code)
	}
	code, body = h.createOperation(domain.LifecycleOpCheckUpdate, "e2e-gate-cu-ver", "0.44.0",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if code != http.StatusBadRequest {
		t.Fatalf("check-update with version = %d, want 400", code)
	}

	// 上游不可解析版本串：REST 报告显式 failed（version_parse_failed），
	// 没有"成功"形态的版本出现——零值/垃圾不冒充结果。
	if err := env.SetLatest(codexPackage, "cheese"); err != nil {
		t.Fatal(err)
	}
	code, report := h.restDo(http.MethodPost, "/api/v1/agents/codex/upstream-versions/check", "")
	if code != http.StatusOK {
		t.Fatalf("upstream check = %d: %v", code, report)
	}
	src := upstreamNpmSource(t, report)
	if src["status"] != "failed" {
		t.Fatalf("unparseable upstream status = %v, want failed", src["status"])
	}
	if failure, _ := src["failure"].(map[string]any); failure["reason"] != "version_parse_failed" {
		t.Fatalf("failure reason = %v, want version_parse_failed", failure["reason"])
	}
	if _, ok := src["version"]; ok {
		t.Fatal("unparseable upstream must not surface a version object")
	}

	// 非 SemVer 但可按来源规则解释：可表达（scheme=source_rule，Raw 原样保留），
	// CheckUpdate 目标 succeeded 且证据带原样版本——不静默降级、不误判失败。
	if err := env.SetLatest(codexPackage, "1.2.3_1"); err != nil {
		t.Fatal(err)
	}
	code, report = h.restDo(http.MethodPost, "/api/v1/agents/codex/upstream-versions/check", "")
	if code != http.StatusOK {
		t.Fatalf("upstream check = %d: %v", code, report)
	}
	src = upstreamNpmSource(t, report)
	if src["status"] != "ok" {
		t.Fatalf("source-rule version status = %v, want ok", src["status"])
	}
	ver, _ := src["version"].(map[string]any)
	if ver["raw"] != "1.2.3_1" || ver["scheme"] != "source_rule" {
		t.Fatalf("version = %v, want raw 1.2.3_1 with scheme source_rule", ver)
	}
	body = h.runThroughChain(domain.LifecycleOpCheckUpdate, "e2e-gate-check", "",
		[]domain.LifecycleTarget{codexTarget(machine, codexPackage)})
	if state, reason := targetState(t, body, codexPackage); state != domain.TargetSucceeded || reason != domain.ReasonVerifiedPresent {
		t.Fatalf("source-rule check-update = %s/%s: %v", state, reason, body)
	}
	if ev := evidenceVersion(t, body, codexPackage); ev != "1.2.3_1" {
		t.Fatalf("check-update evidence = %q, want raw source-rule version 1.2.3_1", ev)
	}
}

// ---- 小工具 ----

func mustGet(t *testing.T, h *harness, opID string) map[string]any {
	t.Helper()
	code, body := h.getOperation(opID)
	if code != http.StatusOK {
		t.Fatalf("get operation %s = %d: %v", opID, code, body)
	}
	return body
}

// upstreamNpmSource 取上游报告里 npm 来源的对象。
func upstreamNpmSource(t *testing.T, report map[string]any) map[string]any {
	t.Helper()
	sources, _ := report["sources"].([]any)
	for _, s := range sources {
		src, _ := s.(map[string]any)
		if src["method"] == "npm" {
			return src
		}
	}
	t.Fatalf("npm source missing in upstream report: %v", report)
	return nil
}

func upstreamNpmRaw(t *testing.T, report map[string]any) string {
	t.Helper()
	src := upstreamNpmSource(t, report)
	ver, _ := src["version"].(map[string]any)
	raw, _ := ver["raw"].(string)
	return raw
}
