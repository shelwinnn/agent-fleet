package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/kit"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 本文件是 Claude 家族的 fixture（矩阵逐家族验收 5 项：身份与兼容性 / 配置与所有权 /
// 生命周期 / 恢复与一致性 / 验收记录）。全部在 t.TempDir() 作为 HOME 根下运行，
// 不触碰真实 ~/.claude、~/.claude.json；Probe/Doctor/ManagedSettingsDir 一律注入，
// 绝不执行真实 claude 二进制、绝不读真实 /etc/claude-code。
// 证据来源见 docs/adapters-batch-2.md §1 与 §9。

// testProbe 同时充当 `--version` 与 `doctor` 的桩：doctor 首行给 install method + 版本。
func testProbe(version string) CommandProbe {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "doctor" {
			return "Claude Code doctor\n\nRunning: npm-global (" + version + ")\n" +
				"Managed settings (remote): not fetched — not available with a custom ANTHROPIC_BASE_URL\n" +
				"Organization policy: not fetched with a custom ANTHROPIC_BASE_URL\n", nil
		}
		return version + " (Claude Code)\n", nil
	}
}

// probeDoctorFallback 模拟 `claude --version` 形态不符、只有 `claude doctor` 可用。
func probeDoctorFallback(version string) CommandProbe {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "doctor" {
			return "Claude Code doctor\n\nRunning: npm-global (" + version + ")\n", nil
		}
		return "unexpected --version output\n", nil
	}
}

func probeMissing() CommandProbe {
	return func(_ context.Context, bin string, _ ...string) (string, error) {
		return "", &exec.Error{Name: bin, Err: exec.ErrNotFound}
	}
}

func probeGarbage() CommandProbe {
	return func(_ context.Context, _ string, _ ...string) (string, error) {
		return "not a version line\n", nil
	}
}

// newTestAdapter 返回注入了桩的适配器；ManagedSettingsDir 落在临时 home 内，
// 保证绝不读真实 /etc/claude-code（护栏 #12）。
func newTestAdapter(home, version string) *Adapter {
	return &Adapter{
		Probe:              testProbe(version),
		Doctor:             testProbe(version),
		ManagedSettingsDir: filepath.Join(home, ".claude-managed"),
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// baselineSettings 是本机真实形状（顶层 env/model/statusLine/enabledPlugins；
// env 内既有受管端点键也有必须未托管的凭据键，值全部替换为占位符）。
func baselineSettings(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func baselineHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), baselineSettings(t))
	return home
}

func fullDesired(t *testing.T) adapter.AgentDesiredState {
	t.Helper()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return adapter.AgentDesiredState{
		Family:  ID,
		Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6",` +
			`"provider":{"endpoint":"https://api.example.com/v1"}}`),
		Rules:  map[string]adapter.RulesEntry{"global": {Content: "Follow repository instructions."}},
		Skills: map[string]domain.SkillDesired{"superpowers": {ContentDigest: digest}},
	}
}

func mustPlan(t *testing.T, a *Adapter, ctx context.Context, home string, desired adapter.AgentDesiredState, inv adapter.AgentObservedState) []adapter.Change {
	t.Helper()
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

// TestDetectDistinguishesMissingFromUnparseable 覆盖矩阵验收 1。
func TestDetectDistinguishesMissingFromUnparseable(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	a := newTestAdapter(home, "2.1.270")
	d, err := a.Detect(ctx, home)
	if err != nil || !d.Installed || d.Version != "2.1.270" {
		t.Fatalf("installed detect = %+v, err=%v", d, err)
	}
	if d.ConfigPath != filepath.Join(home, ".claude", "settings.json") {
		t.Fatalf("config path = %q", d.ConfigPath)
	}

	a = &Adapter{Probe: probeMissing(), Doctor: probeMissing(), ManagedSettingsDir: filepath.Join(home, ".managed")}
	d, err = a.Detect(ctx, home)
	if err != nil || d.Installed {
		t.Fatalf("missing binary must be Installed=false without error, got %+v err=%v", d, err)
	}

	a = &Adapter{Probe: probeGarbage(), Doctor: probeGarbage(), ManagedSettingsDir: filepath.Join(home, ".managed")}
	if _, err = a.Detect(ctx, home); err == nil {
		t.Fatal("unparseable version output must be an explicit error, not a silent 未安装")
	}
}

// TestVersionProbeFallsBackToDoctor：`claude doctor` 首行 `Running: <method> (<semver>)`
// 是唯一备用探测；两种形态都不符时显式报错（§1.1）。
func TestVersionProbeFallsBackToDoctor(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	a := &Adapter{Probe: probeDoctorFallback("2.1.270"), Doctor: probeDoctorFallback("2.1.270"),
		ManagedSettingsDir: filepath.Join(home, ".managed")}
	v, err := a.probeVersion(ctx, home)
	if err != nil || v != "2.1.270" {
		t.Fatalf("doctor fallback failed: v=%q err=%v", v, err)
	}

	a = &Adapter{Probe: probeGarbage(), Doctor: probeGarbage(), ManagedSettingsDir: filepath.Join(home, ".managed")}
	if _, err := a.probeVersion(ctx, home); err == nil {
		t.Fatal("both malformed `--version` and `doctor` outputs must fail explicitly")
	}
}

// TestMergeWritePreservesUnmanagedAndIsIdempotent 是核心 fixture：渲染 → 读取 →
// 未托管保留（含 env 内嵌套键）→ 幂等（重复 reconcile 无实质变更）。
func TestMergeWritePreservesUnmanagedAndIsIdempotent(t *testing.T) {
	home := baselineHome(t)
	// rules 文件带用户内容，验证块外逐字节保留。
	rulesPath := filepath.Join(home, ".claude", "CLAUDE.md")
	writeFile(t, rulesPath, "# 我的全局规则\n\n用户正文\n")
	ctx := context.Background()
	a := newTestAdapter(home, "2.1.270")
	desired := fullDesired(t)

	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv.DesiredProjectionDigest == inv.ObservedProjectionDigest {
		t.Fatal("expected drift before apply")
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]bool{}
	for _, c := range changes {
		steps[c.Step] = true
	}
	for _, want := range []string{"config", "rules", "skills"} {
		if !steps[want] {
			t.Fatalf("plan missing step %q: %+v", want, changes)
		}
	}
	if steps["version"] || steps["mcp"] {
		t.Fatalf("unexpected version/mcp step: %+v", changes)
	}

	// 物化 skill 工件（FetchArtifact 路径未接；缓存缺失时 Apply 必须显式失败）。
	digest := desired.Skills["superpowers"].ContentDigest
	if err := os.MkdirAll(kit.SkillCacheDir(home, "superpowers", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// 未托管的顶层键与 env 内未托管键必须逐键保留（凭据值只存在于输入与文件中，
	// 绝不进期望状态/投影）。
	settings := readFile(t, filepath.Join(home, ".claude", "settings.json"))
	for _, keep := range []string{
		`"ANTHROPIC_AUTH_TOKEN": "sk-ant-REPLACED-PLACEHOLDER"`,
		`"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"`,
		`"API_TIMEOUT_MS": "600000"`,
		`"statusLine"`,
		`"command": "~/.claude/statusline.sh"`,
		`"enabledPlugins"`,
		`"superpowers@claude-plugins-official": true`,
	} {
		if !strings.Contains(settings, keep) {
			t.Fatalf("unmanaged content lost: %q\n---\n%s", keep, settings)
		}
	}
	for _, want := range []string{
		`"model": "claude-sonnet-4-6"`,
		`"ANTHROPIC_BASE_URL": "https://api.example.com/v1"`,
	} {
		if !strings.Contains(settings, want) {
			t.Fatalf("managed value missing: %q\n---\n%s", want, settings)
		}
	}
	rules := readFile(t, rulesPath)
	if !strings.Contains(rules, "# 我的全局规则") || !strings.Contains(rules, "用户正文") ||
		!strings.Contains(rules, kit.BlockBegin) || !strings.Contains(rules, "Follow repository instructions.") {
		t.Fatalf("rules managed block did not preserve user content:\n%s", rules)
	}

	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatalf("apply did not converge: desired=%s observed=%s\n%+v",
			inv2.DesiredProjectionDigest, inv2.ObservedProjectionDigest, inv2.ManagedProjection)
	}
	changes2, err := a.Plan(ctx, home, desired, inv2)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes2) != 0 {
		t.Fatalf("second reconcile must be a no-op (FR-9.3), got %+v", changes2)
	}
	if err := a.HealthCheck(ctx, home, desired); err != nil {
		t.Fatalf("health check: %v", err)
	}
}

// TestMergeWriteMatchesGoldenBytes：整份 settings.json 与黄金文件逐字节比对，
// 锁定未托管键与排序形态，并含尾换行。UPDATE_GOLDEN=1 时重新生成。
func TestMergeWriteMatchesGoldenBytes(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "2.1.270",
		Config:  json.RawMessage(`{"model":"claude-sonnet-4-6","provider":{"endpoint":"https://api.example.com/v1"}}`),
	}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv)); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(home, ".claude", "settings.json"))
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("trailing newline was dropped:\n%q", got)
	}
	goldenPath := filepath.Join("testdata", "settings.after.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := readFile(t, goldenPath)
	if got != want {
		t.Fatalf("settings.json differs from golden bytes (未托管内容必须保留)\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestDriftOnlyFromManagedFields 覆盖矩阵验收 3：受管改动触发 drift，
// 未托管改动（含 env 内嵌套键）不触发误报。
func TestDriftOnlyFromManagedFields(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	path := filepath.Join(home, ".claude", "settings.json")
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "2.1.270",
		Config:  json.RawMessage(`{"model":"claude-sonnet-4-6","provider":{"endpoint":"https://api.example.com/v1"}}`),
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))); err != nil {
		t.Fatal(err)
	}
	converged, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if converged.DesiredProjectionDigest != converged.ObservedProjectionDigest {
		t.Fatal("expected convergence after apply")
	}

	// 未托管顶层键改动：不得触发 drift。
	writeFile(t, path, strings.Replace(readFile(t, path), `"type": "command"`, `"type": "static"`, 1))
	unmanaged, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged.ObservedProjectionDigest != converged.ObservedProjectionDigest {
		t.Fatal("unmanaged top-level edit must not change the managed projection (FR-8.3/§10.3)")
	}

	// env 内未托管键改动（与受管 ANTHROPIC_BASE_URL 同处一个 env 对象）：同样不得触发 drift。
	writeFile(t, path, strings.Replace(readFile(t, path), `"API_TIMEOUT_MS": "600000"`, `"API_TIMEOUT_MS": "120000"`, 1))
	unmanaged, err = a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged.ObservedProjectionDigest != converged.ObservedProjectionDigest {
		t.Fatal("unmanaged key inside the managed env object must not trigger drift")
	}

	// 受管改动：model 与受管端点分别必须触发 drift 并产出计划。
	writeFile(t, path, strings.Replace(readFile(t, path), `"model": "claude-sonnet-4-6"`, `"model": "someone-else"`, 1))
	drifted, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.ObservedProjectionDigest == drifted.DesiredProjectionDigest {
		t.Fatal("managed model edit must be detected as drift")
	}
	changes, err := a.Plan(ctx, home, desired, drifted)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Key != keyModel {
		t.Fatalf("expected single config.model change, got %+v", changes)
	}

	// 先恢复 model 再单独改受管端点：避免上一处受管 drift 叠加。
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Replace(readFile(t, path), `"ANTHROPIC_BASE_URL": "https://api.example.com/v1"`,
		`"ANTHROPIC_BASE_URL": "https://elsewhere.example.com"`, 1))
	drifted, err = a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	changes, err = a.Plan(ctx, home, desired, drifted)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Key != keyProvider {
		t.Fatalf("expected single config.provider change, got %+v", changes)
	}
}

func mustInventory(t *testing.T, a *Adapter, ctx context.Context, home string, desired adapter.AgentDesiredState) adapter.AgentObservedState {
	t.Helper()
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

// TestValidateRejectsUnverifiedBeforeWrite：MCP（裁决 d）与未设计的 apiKeyEnv 映射
// 必须在任何写入之前被拒绝，且零写入产物（矩阵：不得静默跳过或写入后才报成功）。
func TestValidateRejectsUnverifiedBeforeWrite(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	before := readFile(t, settingsPath)
	a := newTestAdapter(home, "2.1.270")

	// MCP 请求：声明 unsupported，显式拒绝（禁止静默跳过）。CheckCapabilities 一次性
	// 汇总拒绝原因，故按消息断言。
	err := a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		MCP: map[string]adapter.MCPEntry{"local-tools": {Command: "mcp-server"}}})
	if err == nil || !strings.Contains(err.Error(), `capability "mcp" is unsupported`) {
		t.Fatalf("MCP request must be rejected as unsupported, got %v", err)
	}
	var ve *adapter.ValidateError

	// provider 缺 endpoint。
	err = a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"provider":{"apiKeyEnv":"K"}}`)})
	if err == nil || !strings.Contains(err.Error(), "provider.endpoint is required") {
		t.Fatalf("provider without endpoint must be rejected, got %v", err)
	}

	// 非空 apiKeyEnv：apiKeyHelper ↔ apiKeyEnv 映射未设计 → 显式拒绝。
	err = a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"m","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_KEY"}}`)})
	if !errors.As(err, &ve) || ve.Capability != adapter.CapabilityModelProvider || ve.State != adapter.SupportUnverified {
		t.Fatalf("apiKeyEnv must be rejected as unverified, got %v", err)
	}

	// 非法 skill 名（路径穿越）。
	err = a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Skills: map[string]domain.SkillDesired{"../evil": {ContentDigest: "sha256:x"}}})
	if err == nil {
		t.Fatal("unsafe skill name must be rejected")
	}

	// 未验证的 OS：非 linux 明确拒绝。
	unsupported := &Adapter{Probe: testProbe("2.1.270"), Doctor: testProbe("2.1.270"), GOOS: "darwin",
		ManagedSettingsDir: filepath.Join(home, ".managed")}
	if err := unsupported.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "2.1.270"}); err == nil {
		t.Fatal("unverified OS must be rejected")
	}

	// 校验失败不得留下任何写入产物。
	if got := readFile(t, settingsPath); got != before {
		t.Fatal("validate must not modify settings.json")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("validate must not create the skills dir: %v", err)
	}
}

// TestValidateRejectsUnparseableSettings：既有配置不可解析、或 env 不是对象
// （破坏嵌套键级所有权）时不得进入变更阶段。
func TestValidateRejectsUnparseableSettings(t *testing.T) {
	ctx := context.Background()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":`)
	a := newTestAdapter(home, "2.1.270")
	if err := a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "2.1.270"}); err == nil {
		t.Fatal("unparseable existing settings.json must be rejected before any write")
	}

	home2 := t.TempDir()
	writeFile(t, filepath.Join(home2, ".claude", "settings.json"), `{"env":"not-an-object"}`)
	a2 := newTestAdapter(home2, "2.1.270")
	if err := a2.Validate(ctx, home2, adapter.AgentDesiredState{Family: ID, Version: "2.1.270"}); err == nil {
		t.Fatal("non-object env must be rejected (nested key ownership is impossible)")
	}

	home3 := t.TempDir()
	writeFile(t, filepath.Join(home3, ".claude-managed", "managed-settings.json"), `{"model":`)
	a3 := newTestAdapter(home3, "2.1.270")
	if err := a3.Validate(ctx, home3, adapter.AgentDesiredState{Family: ID, Version: "2.1.270"}); err == nil {
		t.Fatal("unparseable managed-settings.json must be rejected before any write")
	}
}

// TestMCPDeclarationIsUnsupported：能力声明必须如实反映 KM-32 裁决 (d)。
func TestMCPDeclarationIsUnsupported(t *testing.T) {
	decls := (&Adapter{}).Capabilities()
	for _, d := range decls {
		if d.Capability != adapter.CapabilityMCP {
			continue
		}
		if d.State != adapter.SupportUnsupported {
			t.Fatalf("mcp state = %q, want unsupported", d.State)
		}
		if !strings.Contains(d.Reason, "~/.claude.json") {
			t.Fatalf("mcp reason must name the app-owned file: %q", d.Reason)
		}
		return
	}
	t.Fatal("mcp capability declaration is missing")
}

// TestSkillLinksAreSymlinkSetAndIdempotent：本机 skills/ 是软链集合，重复 reconcile
// 必须无实质变更（§1.5 第 6 项）。
func TestSkillLinksAreSymlinkSetAndIdempotent(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	const digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Skills: map[string]domain.SkillDesired{"superpowers": {ContentDigest: digest}}}
	a := newTestAdapter(home, "2.1.270")
	if err := os.MkdirAll(kit.SkillCacheDir(home, "superpowers", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".claude", "skills", "superpowers")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("skill destination must be a symlink, mode=%v", fi.Mode())
	}
	if got := kit.ReadSkillDigest(link); got != digest {
		t.Fatalf("skill digest = %q, want %q", got, digest)
	}
	inv2 := mustInventory(t, a, ctx, home, desired)
	if cs, err := a.Plan(ctx, home, desired, inv2); err != nil || len(cs) != 0 {
		t.Fatalf("skills reconcile must be idempotent: %+v err=%v", cs, err)
	}
}

// TestSkillLinkRequiresMaterializedArtifact：未物化工件必须显式失败（不静默跳过）。
func TestSkillLinkRequiresMaterializedArtifact(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	const digest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Skills: map[string]domain.SkillDesired{"superpowers": {ContentDigest: digest}}}
	a := newTestAdapter(home, "2.1.270")
	changes := mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))
	if err := a.Apply(ctx, home, desired, changes); err == nil {
		t.Fatal("missing artifact must fail explicitly, not be silently skipped")
	}
	if err := os.MkdirAll(kit.SkillCacheDir(home, "superpowers", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("apply with materialized artifact: %v", err)
	}
}

// TestRulesManagedBlockPreservesUserContentAndRollback：rules 只替换受管块，
// 块外内容逐字节保留；受管块回退只改块内。
func TestRulesManagedBlockPreservesUserContentAndRollback(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	rulesPath := filepath.Join(home, ".claude", "CLAUDE.md")
	writeFile(t, rulesPath, "# 我的规则\n\n正文\n")
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "2.1.270",
		Rules:   map[string]adapter.RulesEntry{"global": {Content: "Fleet 规则 v1"}},
	}
	a := newTestAdapter(home, "2.1.270")
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, rulesPath)
	if !strings.Contains(text, "# 我的规则") || !strings.Contains(text, "正文") {
		t.Fatalf("user content lost:\n%s", text)
	}
	if !strings.Contains(text, kit.BlockBegin) || !strings.Contains(text, "Fleet 规则 v1") {
		t.Fatalf("managed block missing:\n%s", text)
	}
	if cs, err := a.Plan(ctx, home, desired, mustInventory(t, a, ctx, home, desired)); err != nil || len(cs) != 0 {
		t.Fatalf("rules plan not empty: %+v err=%v", cs, err)
	}

	// 受管块回退（外部编辑后的 MergeManaged 路径，§5.3 契约 3）。
	if err := a.MergeManaged(home, filepath.Join(".claude", "CLAUDE.md"),
		map[string]any{"agentFleetRules": "Fleet 规则 v0"}); err != nil {
		t.Fatal(err)
	}
	text = readFile(t, rulesPath)
	if !strings.Contains(text, "Fleet 规则 v0") || strings.Contains(text, "Fleet 规则 v1") {
		t.Fatalf("managed-block rollback did not restore block only:\n%s", text)
	}
	if !strings.Contains(text, "# 我的规则") {
		t.Fatalf("rollback lost user content:\n%s", text)
	}
}

// TestManagedBlockWritePreservesSymlink：本机 `~/.claude/CLAUDE.md` 是软链，
// 原子写必须写穿链接而不是把链接换成普通文件（护栏 #3 / 批次一复核轮 #3）。
func TestManagedBlockWritePreservesSymlink(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	target := filepath.Join(home, ".config", "plexus", "personal", "rules", "global.md")
	writeFile(t, target, "# 全局规则\n\n用户正文\n")
	link := filepath.Join(home, ".claude", "CLAUDE.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Rules: map[string]adapter.RulesEntry{"global": {Content: "Fleet 规则"}}}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))); err != nil {
		t.Fatalf("apply: %v", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("CLAUDE.md symlink was replaced by a regular file (mode=%v)", fi.Mode())
	}
	got := readFile(t, target)
	if !strings.Contains(got, "# 全局规则") || !strings.Contains(got, "用户正文") || !strings.Contains(got, "Fleet 规则") {
		t.Fatalf("write-through symlink content wrong:\n%s", got)
	}
	if inv2 := mustInventory(t, a, ctx, home, desired); inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatal("rules through symlink did not converge")
	}
}

// TestManagedFilesAndMergeManagedRollback 覆盖矩阵验收 4 的两条回退分支：
// ManagedFiles 声明 + 受管键级回退（settings.json）与受管块回退（CLAUDE.md）。
func TestManagedFilesAndMergeManagedRollback(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter(home, "2.1.270")
	desired := fullDesired(t)
	digest := desired.Skills["superpowers"].ContentDigest
	if err := os.MkdirAll(kit.SkillCacheDir(home, "superpowers", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))); err != nil {
		t.Fatal(err)
	}

	files, err := a.ManagedFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		filepath.Join(".claude", "settings.json"): true,
		filepath.Join(".claude", "CLAUDE.md"):     true,
	}
	if len(files) != len(want) {
		t.Fatalf("ManagedFiles = %v", files)
	}
	for _, f := range files {
		if !want[f] {
			t.Fatalf("unexpected managed file %q", f)
		}
	}

	// ExtractManaged 只提取受管键：未托管的 env 凭据键绝不进备份。
	managed, err := a.ExtractManaged([]byte(readFile(t, filepath.Join(home, ".claude", "settings.json"))))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := managed["env.ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("unmanaged credential key must never be extracted: %+v", managed)
	}
	if managed["model"] != "claude-sonnet-4-6" {
		t.Fatalf("managed model not extracted: %+v", managed)
	}
	if managed["env."+baseURLKey] != "https://api.example.com/v1" {
		t.Fatalf("managed env endpoint not extracted: %+v", managed)
	}
	if strings.Contains(strings.Join(keysOf(managed), ","), "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("credential key leaked into extraction: %+v", managed)
	}
	rulesManaged, err := a.ExtractManaged([]byte(readFile(t, filepath.Join(home, ".claude", "CLAUDE.md"))))
	if err != nil {
		t.Fatal(err)
	}
	if rulesManaged["agentFleetRules"] != "Follow repository instructions." {
		t.Fatalf("rules managed block not extracted: %+v", rulesManaged)
	}

	// 受管键级回退：模拟备份时的受管键，仅还原受管键，未托管内容保持（含 env 凭据键）。
	if err := a.MergeManaged(home, filepath.Join(".claude", "settings.json"), map[string]any{
		"model":              "claude-sonnet-4-5",
		"env." + baseURLKey:  "https://rollback.example.com/v1",
		"env.ANTHROPIC_AUTH": "should-not-appear",
	}); err == nil {
		t.Fatal("unknown managed key must make MergeManaged fail explicitly, not skip it")
	}
	if err := a.MergeManaged(home, filepath.Join(".claude", "settings.json"), map[string]any{
		"model":             "claude-sonnet-4-5",
		"env." + baseURLKey: "https://rollback.example.com/v1",
	}); err != nil {
		t.Fatalf("merge managed: %v", err)
	}
	text := readFile(t, filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(text, `"model": "claude-sonnet-4-5"`) ||
		!strings.Contains(text, `"ANTHROPIC_BASE_URL": "https://rollback.example.com/v1"`) {
		t.Fatalf("managed keys not restored:\n%s", text)
	}
	if !strings.Contains(text, `"ANTHROPIC_AUTH_TOKEN": "sk-ant-REPLACED-PLACEHOLDER"`) {
		t.Fatalf("unmanaged credential key lost in managed-key rollback:\n%s", text)
	}

	// rules 文件受管块回退。
	if err := a.MergeManaged(home, filepath.Join(".claude", "CLAUDE.md"),
		map[string]any{"agentFleetRules": "rolled back rules"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(home, ".claude", "CLAUDE.md")); !strings.Contains(got, "rolled back rules") {
		t.Fatalf("rules rollback failed:\n%s", got)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDesiredScopedProjection：期望未点名的键不进受管投影（用户自己设的 model
// 不应在 Fleet 不管理它时造成 drift）。
func TestDesiredScopedProjection(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270"}
	inv := mustInventory(t, a, ctx, home, desired)
	if len(inv.ManagedProjection) != 0 {
		t.Fatalf("no managed keys requested, projection must be empty: %+v", inv.ManagedProjection)
	}
	if inv.DesiredProjectionDigest != inv.ObservedProjectionDigest {
		t.Fatal("nothing managed must never drift")
	}
	// 反向对照：一旦点名 model，同一文件必须立刻被判 drift。
	scoped := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6"}`)}
	drifted := mustInventory(t, a, ctx, home, scoped)
	if drifted.ObservedProjectionDigest == drifted.DesiredProjectionDigest {
		t.Fatal("once model is managed, the same file must drift")
	}
}

// TestApplyVersionMismatchFailsExplicitly：安装/升级不在片内，版本不匹配必须
// 显式失败，绝不谎报成功。
func TestApplyVersionMismatchFailsExplicitly(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.271"}
	err := a.Apply(ctx, home, desired, []adapter.Change{{Family: ID, Step: "version", Key: "version",
		From: "2.1.270", To: "2.1.271"}})
	if err == nil || !strings.Contains(err.Error(), "command installer") {
		t.Fatalf("version mismatch must fail explicitly, got %v", err)
	}
}

// ---- 更高配置层（managed-settings.json）----

// TestHigherLayerManagedSettingsOverrideIsDistinguishable（§1.4/§6 缺口 3）：
// managed-settings.json 的受管键压过用户层时，必须是**可区分的健康态**——
// 不报 Reconciled、整片空计划、不进入"写成功→verify 失败→回滚"的循环。
func TestHigherLayerManagedSettingsOverrideIsDistinguishable(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	writeFile(t, filepath.Join(home, ".claude-managed", "managed-settings.json"),
		`{"model":"org-pinned-model"}`)
	rulesPath := filepath.Join(home, ".claude", "CLAUDE.md")
	writeFile(t, rulesPath, "# user rules\n")
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6","provider":{"endpoint":"https://api.example.com/v1"}}`),
		Rules:  map[string]adapter.RulesEntry{"global": {Content: "fleet rules"}}}

	inv := mustInventory(t, a, ctx, home, desired)
	if inv.DesiredProjectionDigest == inv.ObservedProjectionDigest {
		t.Fatal("an organization pin must not be reported as Reconciled")
	}
	markers, ok := inv.ManagedProjection[overrideMarkerKey].(map[string]any)
	if !ok || markers[keyModel] == nil {
		t.Fatalf("observed projection must carry the override marker: %+v", inv.ManagedProjection)
	}
	if got := inv.ManagedProjection["model"]; got != "org-pinned-model" {
		t.Fatalf("observed model must be the effective managed value, got %v", got)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("any override must yield an empty family plan: %+v", changes)
	}
	// 空计划零写入：rules 文件不得被动过。
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, rulesPath); strings.Contains(got, kit.BlockBegin) || strings.Contains(got, "fleet rules") {
		t.Fatalf("empty plan must not write rules:\n%s", got)
	}
	err = a.HealthCheck(ctx, home, desired)
	var over *HigherLayerOverrideError
	if !errors.As(err, &over) {
		t.Fatalf("expected distinguishable HigherLayerOverrideError, got %v", err)
	}
	if over.Key != keyModel || !strings.Contains(over.Layer, "managed-settings.json") {
		t.Fatalf("override error must name key and layer: %+v", over)
	}
}

// TestManagedSettingsBaseURLOverrideIsReported：env.ANTHROPIC_BASE_URL 被更高层
// 覆盖时同样给出可区分态（不是只有 model）。
func TestManagedSettingsBaseURLOverrideIsReported(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	writeFile(t, filepath.Join(home, ".claude-managed", "managed-settings.json"),
		`{"env":{"ANTHROPIC_BASE_URL":"https://org.example.com/v1"}}`)
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6","provider":{"endpoint":"https://api.example.com/v1"}}`)}
	inv := mustInventory(t, a, ctx, home, desired)
	markers, _ := inv.ManagedProjection[overrideMarkerKey].(map[string]any)
	if markers[keyProvider] == nil {
		t.Fatalf("base URL override must be reported: %+v", inv.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, desired, inv); err != nil || len(cs) != 0 {
		t.Fatalf("override must yield an empty plan: %+v err=%v", cs, err)
	}
	err := a.HealthCheck(ctx, home, desired)
	var over *HigherLayerOverrideError
	if !errors.As(err, &over) || over.Key != keyProvider {
		t.Fatalf("expected provider override error, got %v", err)
	}
}

// TestEmptyManagedSettingsIsNotAnOverride：文件存在但为空（占位文件）不算覆盖，
// 不冻结写入；反向对照证明真正的用户层 drift 仍照常排变更。
func TestEmptyManagedSettingsIsNotAnOverride(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	writeFile(t, filepath.Join(home, ".claude-managed", "managed-settings.json"), "")
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6"}`)}
	inv := mustInventory(t, a, ctx, home, desired)
	if _, ok := inv.ManagedProjection[overrideMarkerKey]; ok {
		t.Fatalf("an empty managed-settings.json must not be reported as an override: %+v", inv.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, desired, inv); err != nil || len(cs) == 0 {
		t.Fatalf("real user-layer drift must still be planned: %+v err=%v", cs, err)
	}
}

// TestManagedSettingsSameValueIsNotAnOverride：更高层设了受管键但值相同，不算覆盖。
func TestManagedSettingsSameValueIsNotAnOverride(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6","provider":{"endpoint":"https://api.example.com/v1"}}`)}
	// 先让用户层收敛，再放一个同值的更高层文件。
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, mustInventory(t, a, ctx, home, desired))); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".claude-managed", "managed-settings.json"),
		`{"model":"claude-sonnet-4-6","env":{"ANTHROPIC_BASE_URL":"https://api.example.com/v1"}}`)
	inv := mustInventory(t, a, ctx, home, desired)
	if _, ok := inv.ManagedProjection[overrideMarkerKey]; ok {
		t.Fatalf("same effective value must not be treated as an override: %+v", inv.ManagedProjection)
	}
	if inv.DesiredProjectionDigest != inv.ObservedProjectionDigest {
		t.Fatalf("same effective value must converge: %+v", inv.ManagedProjection)
	}
	if err := a.HealthCheck(ctx, home, desired); err != nil {
		t.Fatalf("health must pass with an equal managed layer: %v", err)
	}
}

// TestManagedSettingsFragmentsMergeAlphabetically：官方支持
// `managed-settings.d/*.json` drop-in 片段，字母序后者覆盖前者。
func TestManagedSettingsFragmentsMergeAlphabetically(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	dir := filepath.Join(home, ".claude-managed", "managed-settings.d")
	writeFile(t, filepath.Join(dir, "10-base.json"), `{"model":"first-pin"}`)
	writeFile(t, filepath.Join(dir, "20-override.json"), `{"model":"second-pin"}`)
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6"}`)}
	inv := mustInventory(t, a, ctx, home, desired)
	if got := inv.ManagedProjection["model"]; got != "second-pin" {
		t.Fatalf("later fragment must win, got %v (%+v)", got, inv.ManagedProjection)
	}
}

// TestRemoteManagedSettingsAreReported：远程/服务端层无法读取生效值，但
// `claude doctor` 的固定前缀行给出保守信号——报"被覆盖、值不可读"，不报 Reconciled。
func TestRemoteManagedSettingsAreReported(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	remoteDoctor := func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "doctor" {
			return "Claude Code doctor\n\nRunning: npm-global (2.1.270)\n" +
				"Managed settings (remote): fetched from admin console\n", nil
		}
		return "2.1.270 (Claude Code)\n", nil
	}
	a := &Adapter{Probe: remoteDoctor, Doctor: remoteDoctor, ManagedSettingsDir: filepath.Join(home, ".claude-managed")}
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6"}`)}
	inv := mustInventory(t, a, ctx, home, desired)
	markers, _ := inv.ManagedProjection[overrideMarkerKey].(map[string]any)
	if markers[keyModel] == nil {
		t.Fatalf("remote managed settings must be reported: %+v", inv.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, desired, inv); err != nil || len(cs) != 0 {
		t.Fatalf("remote override must yield an empty plan: %+v err=%v", cs, err)
	}
	err := a.HealthCheck(ctx, home, desired)
	var over *HigherLayerOverrideError
	if !errors.As(err, &over) || !strings.Contains(over.Layer, "remote") {
		t.Fatalf("expected remote override error, got %v", err)
	}
}

// TestRemoteNotFetchedIsNotAnOverride：本机实测 doctor 的两行都是
// "not fetched ..."，不得被误判为覆盖（否则本机整天停写）。
func TestRemoteNotFetchedIsNotAnOverride(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter(home, "2.1.270")
	desired := adapter.AgentDesiredState{Family: ID, Version: "2.1.270",
		Config: json.RawMessage(`{"model":"claude-sonnet-4-6"}`)}
	inv := mustInventory(t, a, ctx, home, desired)
	if _, ok := inv.ManagedProjection[overrideMarkerKey]; ok {
		t.Fatalf("not-fetched remote settings must not be an override: %+v", inv.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, desired, inv); err != nil || len(cs) == 0 {
		t.Fatalf("user-layer drift must still be planned: %+v err=%v", cs, err)
	}
}
