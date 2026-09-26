package grok

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

// 本文件是 Grok 家族的 fixture（矩阵逐家族验收 5 项：身份与兼容性 / 配置与所有权 /
// 生命周期 / 恢复与一致性 / 验收记录）。全部在 t.TempDir() 作为 HOME 根下运行，
// 不触碰真实 ~/.grok；Inspect 一律注入桩，绝不执行真实 grok 二进制。
// 证据来源见 docs/adapters-batch-2.md §3 与 §8。

func probeVersion(version string) kit.VersionProbe {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "version" {
			return `{"currentVersion":"` + version + ` (04b7ffed98c6)","channel":"unknown"}` + "\n", nil
		}
		return "grok " + version + " (04b7ffed98c6)\n", nil
	}
}

// probeVersionJSONBroken 模拟 `version --json` 不可用（旧版本/参数不支持），
// 只有回退形态 `--version` 可用。
func probeVersionJSONBroken(version string) kit.VersionProbe {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "version" {
			return "", errors.New("error: unexpected argument '--json' found")
		}
		return "grok " + version + " (04b7ffed98c6)\n", nil
	}
}

func probeMissing() kit.VersionProbe {
	return func(_ context.Context, bin string, _ ...string) (string, error) {
		return "", &exec.Error{Name: bin, Err: exec.ErrNotFound}
	}
}

func probeGarbage() kit.VersionProbe {
	return func(_ context.Context, _ string, _ ...string) (string, error) {
		return "not a version line\n", nil
	}
}

func inspectUnavailable(_ context.Context, _ string) (string, error) {
	return "", errors.New("grok not installed")
}

// inspectWithLayers 模拟 `grok inspect --json` 的 configSources.layers。
func inspectWithLayers(roles ...string) InspectProbe {
	return func(_ context.Context, _ string) (string, error) {
		layers := make([]string, 0, len(roles))
		for _, r := range roles {
			layers = append(layers, `{"role":"`+r+`","path":"/etc/grok/requirements.toml"}`)
		}
		return `{"grokVersion":"1.0.30","configSources":{"layers":[` + strings.Join(layers, ",") + `]}}`, nil
	}
}

func newTestAdapter(version string) *Adapter {
	return &Adapter{Probe: probeVersion(version), Inspect: inspectUnavailable}
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

// baselineConfig 是 §3.2 记录的本机真实形状（testdata/config.toml）。
func baselineConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func baselineHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".grok", "config.toml"), baselineConfig(t))
	return home
}

func fullDesired(t *testing.T) adapter.AgentDesiredState {
	t.Helper()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.0.30",
		Config:  json.RawMessage(`{"model":"grok-4.6","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_GROK_KEY"}}`),
		MCP: map[string]adapter.MCPEntry{
			"local-tools": {Command: "/usr/local/bin/my-mcp", Args: []string{"--stdio"},
				EnvRefs: map[string]string{"SERVICE_TOKEN": "SERVICE_TOKEN"}},
		},
		Rules:  map[string]adapter.RulesEntry{"global": {Content: "Follow repository instructions."}},
		Skills: map[string]domain.SkillDesired{"superpowers": {ContentDigest: digest}},
	}
}

// TestDetectDistinguishesMissingFromUnparseable 覆盖矩阵验收 1。
func TestDetectDistinguishesMissingFromUnparseable(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	a := newTestAdapter("1.0.30")
	d, err := a.Detect(ctx, home)
	if err != nil || !d.Installed || d.Version != "1.0.30" {
		t.Fatalf("installed detect = %+v, err=%v", d, err)
	}
	if d.ConfigPath != filepath.Join(home, ".grok", "config.toml") {
		t.Fatalf("config path = %q", d.ConfigPath)
	}

	a = &Adapter{Probe: probeMissing(), Inspect: inspectUnavailable}
	d, err = a.Detect(ctx, home)
	if err != nil || d.Installed {
		t.Fatalf("missing binary must be Installed=false without error, got %+v err=%v", d, err)
	}

	a = &Adapter{Probe: probeGarbage(), Inspect: inspectUnavailable}
	if _, err = a.Detect(ctx, home); err == nil {
		t.Fatal("unparseable version output must be an explicit error, not a silent 未安装")
	}
}

// TestVersionSourcesAreCLIOnly：`~/.grok/version.json` 本机落后实际二进制 5 个
// 补丁版本，明确不得作为版本来源（§3.1）。同时覆盖 `version --json` 缺失时回退
// `grok --version`，以及回退形态不符时显式报错。
func TestVersionSourcesAreCLIOnly(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	// 写入一个"诱饵" version.json：任何实现若读它就会得到 1.0.25。
	writeFile(t, filepath.Join(home, ".grok", "version.json"), `{"version":"1.0.25","channel":"unknown"}`)

	a := newTestAdapter("1.0.30")
	d, err := a.Detect(ctx, home)
	if err != nil || !d.Installed || d.Version != "1.0.30" {
		t.Fatalf("version.json must not be a version source: %+v err=%v", d, err)
	}

	a = &Adapter{Probe: probeVersionJSONBroken("1.0.30"), Inspect: inspectUnavailable}
	v, err := a.probeVersion(ctx)
	if err != nil || v != "1.0.30" {
		t.Fatalf("fallback to `grok --version` failed: v=%q err=%v", v, err)
	}

	a = &Adapter{Probe: probeGarbage(), Inspect: inspectUnavailable}
	if _, err := a.probeVersion(ctx); err == nil {
		t.Fatal("malformed `grok --version` output must fail explicitly")
	}
}

// TestMergeWritePreservesUnmanagedAndIsIdempotent 是核心 fixture：渲染 → 读取 →
// 未托管保留（含表内未托管键）→ 幂等（重复 reconcile 无实质变更）。
func TestMergeWritePreservesUnmanagedAndIsIdempotent(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter("1.0.30")
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
	for _, want := range []string{"config", "mcp", "rules", "skills"} {
		if !steps[want] {
			t.Fatalf("plan missing step %q: %+v", want, changes)
		}
	}
	if steps["version"] {
		t.Fatalf("version already equal must not be planned: %+v", changes)
	}

	// 物化 skill 工件（FetchArtifact 路径未接；缓存缺失时 Apply 必须显式失败）。
	digest := desired.Skills["superpowers"].ContentDigest
	if err := os.MkdirAll(kit.SkillCacheDir(home, "superpowers", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}

	text := readFile(t, filepath.Join(home, ".grok", "config.toml"))
	for _, keep := range []string{
		"# 本机 ~/.grok/config.toml 的真实形状",
		`installer = "npm"`,
		`default_skills_installs_purged = true`,
		`[[marketplace.sources]]`,
		`git = "https://github.com/xai-org/plugin-marketplace.git"`,
		`default_reasoning_effort = "xhigh"`,
		`max_thoughts_width = 120`,
		`fork_secondary_model = "grok-4.6"`,
		`compact_mode = false`,
	} {
		if !strings.Contains(text, keep) {
			t.Fatalf("unmanaged content lost: %q\n---\n%s", keep, text)
		}
	}
	for _, want := range []string{
		`[models]`,
		`default = "fleet"`,
		`[model.fleet]`,
		`model = "grok-4.6"`,
		`base_url = "https://api.example.com/v1"`,
		`env_key = "FLEET_GROK_KEY"`,
		`api_backend = "chat_completions"`,
		`[mcp_servers.local-tools]`,
		`command = "/usr/local/bin/my-mcp"`,
		`[mcp_servers.local-tools.env]`,
		`SERVICE_TOKEN = "${SERVICE_TOKEN}"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("managed value missing: %q\n---\n%s", want, text)
		}
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

// TestDriftOnlyFromManagedFields 覆盖矩阵验收 3：受管改动触发 drift，
// 未托管改动不触发误报。
func TestDriftOnlyFromManagedFields(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	path := filepath.Join(home, ".grok", "config.toml")
	a := newTestAdapter("1.0.30")
	// 让 models.default 已指向 Fleet 命名空间，期望侧只点名 model id。
	writeFile(t, path, strings.Replace(baselineConfig(t), `default = "grok-4.6"`, `default = "fleet"`, 1))
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.0.30",
		Config:  json.RawMessage(`{"model":"grok-4.6"}`),
	}
	// [model.fleet] 尚未写入 → 仍应 drift（受管键缺席）。
	before, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if before.DesiredProjectionDigest == before.ObservedProjectionDigest {
		t.Fatal("missing [model.fleet] must be drift")
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, before)); err != nil {
		t.Fatal(err)
	}
	converged, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if converged.DesiredProjectionDigest != converged.ObservedProjectionDigest {
		t.Fatal("expected convergence after apply")
	}

	// 未托管改动：不得触发 drift。
	writeFile(t, path, strings.Replace(readFile(t, path), `yolo = false`, `yolo = true`, 1))
	unmanaged, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged.ObservedProjectionDigest != converged.ObservedProjectionDigest {
		t.Fatal("unmanaged edit must not change the managed projection (FR-8.3/§10.3)")
	}

	// 表内未托管键的改动：同样不得触发 drift（[models] 只受管 default）。
	writeFile(t, path, strings.Replace(readFile(t, path), `default_reasoning_effort = "xhigh"`, `default_reasoning_effort = "low"`, 1))
	unmanaged, err = a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged.ObservedProjectionDigest != converged.ObservedProjectionDigest {
		t.Fatal("unmanaged key inside a managed table must not trigger drift")
	}

	// 受管改动：必须触发 drift 并产出计划。
	writeFile(t, path, strings.Replace(readFile(t, path), `default = "fleet"`, `default = "someone-else"`, 1))
	drifted, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.ObservedProjectionDigest == drifted.DesiredProjectionDigest {
		t.Fatal("managed edit must be detected as drift")
	}
	changes, err := a.Plan(ctx, home, desired, drifted)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Key != keyModel {
		t.Fatalf("expected single config.model change, got %+v", changes)
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

// TestEnvRefsRenderNativelyAndConverge：批次一遗留缺口 3 的降级路径验证点——
// Grok 原生支持 `${VAR}`，envRefs 必须渲染为原生间接引用并收敛。
func TestEnvRefsRenderNativelyAndConverge(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.0.30",
		MCP: map[string]adapter.MCPEntry{
			"needs-env": {Command: "mcp-server",
				EnvRefs: map[string]string{"TOKEN": "SERVICE_TOKEN", "OTHER": "OTHER_TOKEN"}},
		},
	}
	if err := a.Validate(ctx, home, desired); err != nil {
		t.Fatalf("native envRefs must be accepted for grok: %v", err)
	}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv)); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, filepath.Join(home, ".grok", "config.toml"))
	for _, want := range []string{
		`[mcp_servers.needs-env]`,
		`[mcp_servers.needs-env.env]`,
		`TOKEN = "${SERVICE_TOKEN}"`,
		`OTHER = "${OTHER_TOKEN}"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("envRefs not rendered as native ${VAR}: %q\n%s", want, text)
		}
	}
	// 不加引号的裸值也不能出现（值永不落盘，只写变量名）。
	if strings.Contains(text, `SERVICE_TOKEN = "SERVICE_TOKEN"`) {
		t.Fatal("envRefs must be written as ${VAR} indirection, not a literal value")
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatalf("envRefs entry did not converge: %+v", inv2.ManagedProjection)
	}
	// 去掉 envRefs 后，陈旧 env 子表必须被清理，且仍收敛。
	desired.MCP["needs-env"] = adapter.MCPEntry{Command: "mcp-server"}
	inv3, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv3)); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, filepath.Join(home, ".grok", "config.toml")); strings.Contains(text, "mcp_servers.needs-env.env") {
		t.Fatalf("stale managed env sub-table must be removed:\n%s", text)
	}
}

// TestMCPEntryWithoutArgsConverges 是批次一复核缺陷 1 的家族回归：省略 args 的
// 投影两侧必须同形，否则永不收敛。
func TestMCPEntryWithoutArgsConverges(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.0.30",
		MCP:     map[string]adapter.MCPEntry{"no-args": {Command: "/usr/local/bin/my-mcp"}},
	}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv)); err != nil {
		t.Fatal(err)
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatalf("no-args MCP entry must converge: %+v", inv2.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, desired, inv2); err != nil || len(cs) != 0 {
		t.Fatalf("second reconcile must be a no-op: %+v err=%v", cs, err)
	}
}

// TestValidateRejectsUnverifiedBeforeWrite：未验证/不可实现的能力必须在任何写入
// 之前被拒绝（矩阵：不得静默跳过或写入后才报成功）。
func TestValidateRejectsUnverifiedBeforeWrite(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	path := filepath.Join(home, ".grok", "config.toml")
	before := readFile(t, path)

	// 非法的 env 变量名（不是环境变量名）→ 显式拒绝。
	bad := adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.0.30",
		MCP: map[string]adapter.MCPEntry{
			"needs-env": {Command: "mcp-server", EnvRefs: map[string]string{"TOKEN": "not-a-var"}},
		},
	}
	a := newTestAdapter("1.0.30")
	err := a.Validate(ctx, home, bad)
	var ve *adapter.ValidateError
	if !errors.As(err, &ve) || ve.Capability != adapter.CapabilityMCP {
		t.Fatalf("expected ValidateError for mcp envRefs, got %v", err)
	}

	// provider 缺 model：Grok 的 BYOK 条目必须给出模型 id。
	err = a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		Config: json.RawMessage(`{"provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"K"}}`)})
	if err == nil || !strings.Contains(err.Error(), "config.model is required") {
		t.Fatalf("provider without model must be rejected, got %v", err)
	}

	// 未验证的 OS：非 linux 明确拒绝。
	unsupported := &Adapter{Probe: probeVersion("1.0.30"), Inspect: inspectUnavailable, GOOS: "darwin"}
	if err := unsupported.Validate(ctx, home, bad); err == nil {
		t.Fatal("unverified OS must be rejected")
	}

	// MCP 名字非法。
	if err := a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		MCP: map[string]adapter.MCPEntry{"bad name": {Command: "x"}}}); err == nil {
		t.Fatal("unsafe mcp name must be rejected")
	}
	// command 缺失。
	if err := a.Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		MCP: map[string]adapter.MCPEntry{"x": {}}}); err == nil {
		t.Fatal("mcp entry without command must be rejected")
	}

	// 校验失败不得留下任何写入产物。
	if got := readFile(t, path); got != before {
		t.Fatal("validate must not modify the config")
	}
	if _, err := os.Stat(filepath.Join(home, ".grok", "skills")); !os.IsNotExist(err) {
		t.Fatalf("validate must not create the skills dir: %v", err)
	}
}

// TestValidateRejectsUnparseableConfig：既有配置不可解析时不得进入变更阶段。
func TestValidateRejectsUnparseableConfig(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".grok", "config.toml"), "[models\ndefault = ")
	a := newTestAdapter("1.0.30")
	if err := a.Validate(context.Background(), home, adapter.AgentDesiredState{Family: ID, Version: "1.0.30"}); err == nil {
		t.Fatal("unparseable existing config must be rejected before any write")
	}
}

// TestSkillLinkRequiresMaterializedArtifact：未物化工件必须显式失败（不静默跳过）。
func TestSkillLinkRequiresMaterializedArtifact(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.0.30",
		Skills:  map[string]domain.SkillDesired{"superpowers": {ContentDigest: digest}},
	}
	a := newTestAdapter("1.0.30")
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err == nil {
		t.Fatal("missing artifact must fail explicitly, not be silently skipped")
	}
	cache := kit.SkillCacheDir(home, "superpowers", digest)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("apply with materialized artifact: %v", err)
	}
	if got := kit.ReadSkillDigest(filepath.Join(home, ".grok", "skills", "superpowers")); got != digest {
		t.Fatalf("skill digest = %q, want %q", got, digest)
	}
}

// TestRulesManagedBlockPreservesUserContentAndRollback：rules 只替换受管块，
// 块外内容逐字节保留；受管块回退只改块内。
func TestRulesManagedBlockPreservesUserContentAndRollback(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	rulesPath := filepath.Join(home, ".grok", "rules", rulesFileName)
	writeFile(t, rulesPath, "# 我的规则\n\n正文\n")
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.0.30",
		Rules:   map[string]adapter.RulesEntry{"global": {Content: "Fleet 规则 v1"}},
	}
	a := newTestAdapter("1.0.30")
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv)); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, rulesPath)
	if !strings.Contains(text, "# 我的规则") || !strings.Contains(text, "正文") {
		t.Fatalf("user content lost:\n%s", text)
	}
	if !strings.Contains(text, kit.BlockBegin) || !strings.Contains(text, "Fleet 规则 v1") {
		t.Fatalf("managed block missing:\n%s", text)
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if cs, err := a.Plan(ctx, home, desired, inv2); err != nil || len(cs) != 0 {
		t.Fatalf("rules plan not empty: %+v err=%v", cs, err)
	}

	// 受管块回退（外部编辑后的 MergeManaged 路径，§5.3 契约 3）。
	if err := a.MergeManaged(home, filepath.Join(".grok", "rules", rulesFileName),
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

// TestManagedFilesAndMergeManagedRollback 覆盖矩阵验收 4 的两条回退分支：
// ManagedFiles 声明 + 受管键级回退（配置）与受管块回退（rules）。
func TestManagedFilesAndMergeManagedRollback(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter("1.0.30")
	desired := fullDesired(t)
	digest := desired.Skills["superpowers"].ContentDigest
	if err := os.MkdirAll(kit.SkillCacheDir(home, "superpowers", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv)); err != nil {
		t.Fatal(err)
	}

	files, err := a.ManagedFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		filepath.Join(".grok", "config.toml"):          true,
		filepath.Join(".grok", "rules", rulesFileName): true,
	}
	if len(files) != len(want) {
		t.Fatalf("ManagedFiles = %v", files)
	}
	for _, f := range files {
		if !want[f] {
			t.Fatalf("unexpected managed file %q", f)
		}
	}

	// ExtractManaged 只提取受管键：表内未托管键（default_reasoning_effort）不进备份。
	content := []byte(readFile(t, filepath.Join(home, ".grok", "config.toml")))
	managed, err := a.ExtractManaged(content)
	if err != nil {
		t.Fatal(err)
	}
	models, _ := managed["models"].(map[string]any)
	if _, ok := models["default_reasoning_effort"]; ok {
		t.Fatalf("unmanaged key must not be extracted: %+v", managed)
	}
	if models["default"] != providerID {
		t.Fatalf("managed models.default not extracted: %+v", managed)
	}
	rulesManaged, err := a.ExtractManaged([]byte(readFile(t, filepath.Join(home, ".grok", "rules", rulesFileName))))
	if err != nil {
		t.Fatal(err)
	}
	if rulesManaged["agentFleetRules"] != "Follow repository instructions." {
		t.Fatalf("rules managed block not extracted: %+v", rulesManaged)
	}

	// 受管键级回退：模拟备份时的受管键，仅还原受管键，未托管内容保持。
	if err := a.MergeManaged(home, filepath.Join(".grok", "config.toml"), map[string]any{
		"models": map[string]any{"default": "fleet"},
		"model.fleet": map[string]any{
			"name": providerName, "model": "grok-4.5",
			"base_url": "https://rollback.example/v1", "env_key": "ROLLBACK_KEY",
			"api_backend": apiBackend,
		},
	}); err != nil {
		t.Fatalf("merge managed: %v", err)
	}
	text := readFile(t, filepath.Join(home, ".grok", "config.toml"))
	if !strings.Contains(text, `base_url = "https://rollback.example/v1"`) || !strings.Contains(text, `env_key = "ROLLBACK_KEY"`) {
		t.Fatalf("managed keys not restored:\n%s", text)
	}
	for _, keep := range []string{
		"# 本机 ~/.grok/config.toml 的真实形状", `installer = "npm"`,
		`[[marketplace.sources]]`, `default_reasoning_effort = "xhigh"`, `compact_mode = false`,
	} {
		if !strings.Contains(text, keep) {
			t.Fatalf("unmanaged content lost in managed-key rollback: %q\n%s", keep, text)
		}
	}

	// rules 文件受管块回退。
	if err := a.MergeManaged(home, filepath.Join(".grok", "rules", rulesFileName),
		map[string]any{"agentFleetRules": "rolled back rules"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(home, ".grok", "rules", rulesFileName)); !strings.Contains(got, "rolled back rules") {
		t.Fatalf("rules rollback failed:\n%s", got)
	}
}

// TestManagedBlockWritePreservesSymlink：rules 文件可能是软链（批次一复核 #3 的
// 同类形态），原子写必须写穿链接而不是把链接换成普通文件（护栏 #3）。
func TestManagedBlockWritePreservesSymlink(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	target := filepath.Join(home, ".config", "plexus", "personal", "rules", "global.md")
	writeFile(t, target, "# 全局规则\n\n用户正文\n")
	link := filepath.Join(home, ".grok", "rules", rulesFileName)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		Rules: map[string]adapter.RulesEntry{"global": {Content: "Fleet 规则"}}}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rules symlink was replaced by a regular file (mode=%v)", fi.Mode())
	}
	got := readFile(t, target)
	if !strings.Contains(got, "# 全局规则") || !strings.Contains(got, "用户正文") || !strings.Contains(got, "Fleet 规则") {
		t.Fatalf("write-through symlink content wrong:\n%s", got)
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatal("rules through symlink did not converge")
	}
}

// TestUnmanagedMCPEntriesArePreserved：未点名的 [mcp_servers.*] 条目必须原样保留。
func TestUnmanagedMCPEntriesArePreserved(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".grok", "config.toml")
	writeFile(t, path, baselineConfig(t)+`
[mcp_servers.user-owned]
command = "user-mcp"
args = ["--x"]
tool_timeout_sec = 42
`)
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		MCP: map[string]adapter.MCPEntry{"fleet-tools": {Command: "/usr/local/bin/my-mcp"}}}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, mustPlan(t, a, ctx, home, desired, inv)); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	for _, keep := range []string{`[mcp_servers.user-owned]`, `command = "user-mcp"`, `args = ["--x"]`, `tool_timeout_sec = 42`} {
		if !strings.Contains(text, keep) {
			t.Fatalf("unmanaged mcp entry lost: %q\n%s", keep, text)
		}
	}
}

// TestHigherLayerRequirementPinIsDistinguishable（§3.4/§6 缺口 3）：requirements.toml
// 的 pin 类键压过用户层时，必须是**可区分的健康态**——不报 Reconciled、不排无效变更、
// 也不进入"写成功→verify 失败→回滚"的循环。
func TestHigherLayerRequirementPinIsDistinguishable(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	writeFile(t, filepath.Join(home, ".grok", "requirements.toml"), "[models]\ndefault = \"pinned-model\"\n")
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		Config: json.RawMessage(`{"model":"grok-4.6"}`)}

	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv.DesiredProjectionDigest == inv.ObservedProjectionDigest {
		t.Fatal("an organization pin must not be reported as Reconciled")
	}
	markers, ok := inv.ManagedProjection[overrideMarkerKey].(map[string]any)
	if !ok || markers[keyModel] == nil {
		t.Fatalf("observed projection must carry the override marker: %+v", inv.ManagedProjection)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Key == keyModel {
			t.Fatalf("pinned key must not be planned for a futile user-layer write: %+v", changes)
		}
	}
	err = a.HealthCheck(ctx, home, desired)
	var over *HigherLayerOverrideError
	if !errors.As(err, &over) {
		t.Fatalf("expected distinguishable HigherLayerOverrideError, got %v", err)
	}
	if over.Key != keyModel || !strings.Contains(over.Layer, "requirements.toml") {
		t.Fatalf("override error must name key and layer: %+v", over)
	}
}

// TestHigherLayerOutsideHomeIsReported：inspect 报告 requirements 层但 $GROK_HOME
// 内没有该文件（/etc/grok 或 MDM 下发）时，值不可读——仍如实报"被覆盖、值未知"。
func TestHigherLayerOutsideHomeIsReported(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := &Adapter{Probe: probeVersion("1.0.30"), Inspect: inspectWithLayers("managed", "user", "requirements")}
	desired := adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		Config: json.RawMessage(`{"model":"grok-4.6"}`)}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	markers, _ := inv.ManagedProjection[overrideMarkerKey].(map[string]any)
	if markers[keyModel] == nil {
		t.Fatalf("outside-home requirements layer must be reported: %+v", inv.ManagedProjection)
	}
	err = a.HealthCheck(ctx, home, desired)
	var over *HigherLayerOverrideError
	if !errors.As(err, &over) || !strings.Contains(over.Layer, "outside $GROK_HOME") {
		t.Fatalf("expected outside-home override error, got %v", err)
	}
}

// TestManagedConfigLayerIsNotAnOverride：managed_config.toml 位于用户 config.toml
// **之前**，且其值只在 `Managed: fleet` 的键上胜出；Fleet 只写 `Managed: user` 的键，
// 因此它不得被当作覆盖（厂商原文：Their value applies, except features.remote_fetch）。
func TestManagedConfigLayerIsNotAnOverride(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	// 部署方层设了 models.default，但用户层（Fleet 写的）应当胜出。
	writeFile(t, filepath.Join(home, ".grok", "managed_config.toml"),
		"[models]\ndefault = \"managed-model\"\n[features]\nremote_fetch = true\n")
	writeFile(t, filepath.Join(home, ".grok", "config.toml"),
		"[models]\ndefault = \"fleet\"\n[model.fleet]\nmodel = \"grok-4.6\"\n")
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		Config: json.RawMessage(`{"model":"grok-4.6"}`)}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := inv.ManagedProjection[overrideMarkerKey]; ok {
		t.Fatalf("managed_config must not be treated as an override: %+v", inv.ManagedProjection)
	}
	if inv.DesiredProjectionDigest != inv.ObservedProjectionDigest {
		t.Fatalf("user layer wins over managed_config; expected convergence: %+v", inv.ManagedProjection)
	}
	if err := a.HealthCheck(ctx, home, desired); err != nil {
		t.Fatalf("health must pass when the user layer wins: %v", err)
	}
}

// TestDesiredScopedProjection：期望未点名的键不进受管投影（用户自己设的 model
// 不应在 Fleet 不管理它时造成 drift）。
func TestDesiredScopedProjection(t *testing.T) {
	home := baselineHome(t)
	ctx := context.Background()
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{Family: ID, Version: "1.0.30"}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.ManagedProjection) != 0 {
		t.Fatalf("no managed keys requested, projection must be empty: %+v", inv.ManagedProjection)
	}
	if inv.DesiredProjectionDigest != inv.ObservedProjectionDigest {
		t.Fatal("nothing managed must never drift")
	}
	// 反向对照：一旦点名 model，同一文件必须立刻被判 drift。
	scoped := adapter.AgentDesiredState{Family: ID, Version: "1.0.30",
		Config: json.RawMessage(`{"model":"grok-4.6"}`)}
	drifted, err := a.Inventory(ctx, home, scoped)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.ObservedProjectionDigest == drifted.DesiredProjectionDigest {
		t.Fatal("once model is managed, the same file must drift")
	}
	if drifted.DesiredProjectionDigest == inv.DesiredProjectionDigest {
		t.Fatal("desired projection must differ between scoped and unscoped desired state")
	}
}

// TestApplyVersionMismatchFailsExplicitly：安装/升级不在片内，版本不匹配必须
// 显式失败，绝不谎报成功。
func TestApplyVersionMismatchFailsExplicitly(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := newTestAdapter("1.0.30")
	desired := adapter.AgentDesiredState{Family: ID, Version: "1.0.31"}
	err := a.Apply(ctx, home, desired, []adapter.Change{{Family: ID, Step: "version", Key: "version",
		From: "1.0.30", To: "1.0.31"}})
	if err == nil || !strings.Contains(err.Error(), "command installer") {
		t.Fatalf("version mismatch must fail explicitly, got %v", err)
	}
}
