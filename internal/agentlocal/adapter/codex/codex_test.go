package codex

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

// 本文件是 Codex 家族的 fixture（矩阵验收 2：渲染 / 读取 / 未托管保留 /
// 未支持能力写入前拒绝）。全部在 t.TempDir() 作为 HOME 根下运行，
// 不触碰真实 ~/.codex。证据来源见 docs/adapters-batch-1.md「Codex」。

const sampleConfig = `# 用户注释必须保留
model = "gpt-5.5"
approval_policy = "on-request"

[projects."/home/u/work"]
trust_level = "trusted"

[mcp_servers.serena]
command = "uvx"
args = ["serena", "start-mcp-server"] # 行尾注释也要保留
`

func probeVersion(version string) kit.VersionProbe {
	return func(_ context.Context, _ string, _ ...string) (string, error) {
		return "codex-cli " + version + "\n", nil
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDetectDistinguishesMissingFromUnparseable 覆盖矩阵验收 1 的可区分性。
func TestDetectDistinguishesMissingFromUnparseable(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()

	a := &Adapter{Probe: probeVersion("0.154.0")}
	d, err := a.Detect(ctx, home)
	if err != nil || !d.Installed || d.Version != "0.154.0" {
		t.Fatalf("installed detect = %+v, err=%v", d, err)
	}
	if d.ConfigPath != filepath.Join(home, ".codex", "config.toml") {
		t.Fatalf("config path = %q", d.ConfigPath)
	}

	a = &Adapter{Probe: probeMissing()}
	d, err = a.Detect(ctx, home)
	if err != nil || d.Installed {
		t.Fatalf("missing binary must be Installed=false without error, got %+v err=%v", d, err)
	}

	a = &Adapter{Probe: probeGarbage()}
	if _, err = a.Detect(ctx, home); err == nil {
		t.Fatal("unparseable version output must be an explicit error, not a silent 未安装")
	}
}

// TestMergeWritePreservesUnmanagedAndIsIdempotent 是核心 fixture：
// 渲染 → 读取 → 未托管保留 → 幂等（重复 reconcile 无实质变更）。
func TestMergeWritePreservesUnmanagedAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), sampleConfig)

	a := &Adapter{Probe: probeVersion("0.154.0")}
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "0.154.0",
		Config:  json.RawMessage(`{"model":"gpt-5.6","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_OPENAI_KEY"}}`),
		MCP: map[string]adapter.MCPEntry{
			"local-tools": {Command: "/usr/local/bin/my-mcp", Args: []string{"--stdio"}},
		},
		Rules: map[string]adapter.RulesEntry{"global": {Content: "Follow repository instructions and preserve tests."}},
	}

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
	for _, want := range []string{"config", "mcp", "rules"} {
		if !steps[want] {
			t.Fatalf("plan missing step %q: %+v", want, changes)
		}
	}
	if steps["version"] {
		t.Fatalf("version already equal must not be planned: %+v", changes)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, keep := range []string{
		"# 用户注释必须保留",
		`approval_policy = "on-request"`,
		`[projects."/home/u/work"]`,
		`trust_level = "trusted"`,
		`[mcp_servers.serena]`,
		`args = ["serena", "start-mcp-server"] # 行尾注释也要保留`,
	} {
		if !strings.Contains(text, keep) {
			t.Fatalf("unmanaged content lost: %q\n---\n%s", keep, text)
		}
	}
	for _, want := range []string{
		`model = "gpt-5.6"`,
		`model_provider = "fleet"`,
		`[model_providers.fleet]`,
		`base_url = "https://api.example.com/v1"`,
		`env_key = "FLEET_OPENAI_KEY"`,
		`wire_api = "responses"`,
		`[mcp_servers.local-tools]`,
		`command = "/usr/local/bin/my-mcp"`,
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
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".codex", "config.toml")
	writeFile(t, path, sampleConfig)
	a := &Adapter{Probe: probeVersion("0.154.0")}
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "0.154.0",
		Config:  json.RawMessage(`{"model":"gpt-5.5"}`),
	}
	before, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if before.DesiredProjectionDigest != before.ObservedProjectionDigest {
		t.Fatal("model already matches; expected no drift")
	}

	// 未托管改动：不得触发 drift。
	writeFile(t, path, strings.Replace(sampleConfig, `approval_policy = "on-request"`, `approval_policy = "never"`, 1))
	unmanaged, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged.ObservedProjectionDigest != before.ObservedProjectionDigest {
		t.Fatal("unmanaged edit must not change the managed projection (FR-8.3/§10.3)")
	}

	// 受管改动：必须触发 drift 并产出计划。
	writeFile(t, path, strings.Replace(sampleConfig, `model = "gpt-5.5"`, `model = "someone-else"`, 1))
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
	if len(changes) != 1 || changes[0].Key != "config.model" {
		t.Fatalf("expected single config.model change, got %+v", changes)
	}
}

// TestDesiredScopedProjection：期望未点名的键不进受管投影（用户自己设的
// model 不应在 Fleet 不管理它时造成 drift）。
func TestDesiredScopedProjection(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), sampleConfig)
	a := &Adapter{Probe: probeVersion("0.154.0")}
	desired := adapter.AgentDesiredState{Family: ID, Version: "0.154.0"}
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
	// 反向：一旦期望点名 model，同一个文件必须立刻被判 drift（证明上一条不是
	// "两侧摘要都为空所以恒等"的空断言）。
	scoped := adapter.AgentDesiredState{Family: ID, Version: "0.154.0",
		Config: json.RawMessage(`{"model":"gpt-5.6"}`)}
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

// TestRulesManagedBlockPreservesUserContent：标记块只替换块内内容（FR-5.1/护栏 #3）。
func TestRulesManagedBlockPreservesUserContent(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	agentsPath := filepath.Join(home, ".codex", "AGENTS.md")
	writeFile(t, agentsPath, "# 我的全局指令\n\n正文\n")
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "0.154.0",
		Rules:   map[string]adapter.RulesEntry{"global": {Content: "Fleet 规则 v1"}},
	}
	a := &Adapter{Probe: probeVersion("0.154.0")}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, agentsPath)
	if !strings.Contains(text, "# 我的全局指令") || !strings.Contains(text, "正文") {
		t.Fatalf("user content lost:\n%s", text)
	}
	if !strings.Contains(text, kit.BlockBegin) || !strings.Contains(text, "Fleet 规则 v1") {
		t.Fatalf("managed block missing:\n%s", text)
	}

	// 幂等：同一期望再次 plan 无变更。
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatal("rules block not converged")
	}
	if cs, err := a.Plan(ctx, home, desired, inv2); err != nil || len(cs) != 0 {
		t.Fatalf("rules plan not empty: %+v err=%v", cs, err)
	}

	// 受管块回退（外部编辑后的 MergeManaged 路径，§5.3 契约 3）。
	if err := a.MergeManaged(home, filepath.Join(".codex", "AGENTS.md"), map[string]any{"agentFleetRules": "Fleet 规则 v0"}); err != nil {
		t.Fatal(err)
	}
	text = readFile(t, agentsPath)
	if !strings.Contains(text, "Fleet 规则 v0") || strings.Contains(text, "Fleet 规则 v1") {
		t.Fatalf("managed-key rollback did not restore block only:\n%s", text)
	}
	if !strings.Contains(text, "# 我的全局指令") {
		t.Fatalf("rollback lost user content:\n%s", text)
	}
}

// TestSkillLinkRequiresMaterializedArtifact：未物化工件必须显式失败（不静默跳过）。
func TestSkillLinkRequiresMaterializedArtifact(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "0.154.0",
		Skills:  map[string]domain.SkillDesired{"superpowers": {ContentDigest: digest}},
	}
	a := &Adapter{Probe: probeVersion("0.154.0")}
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
	link := filepath.Join(home, ".codex", "skills", "superpowers")
	if got := kit.ReadSkillDigest(link); got != digest {
		t.Fatalf("skill digest = %q, want %q", got, digest)
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatal("skill link not converged")
	}
}

// TestValidateRejectsUnverifiedBeforeWrite：envRefs 与未验证 OS 必须在
// 任何写入之前被拒绝（矩阵：不得静默跳过或写入后才报成功）。
func TestValidateRejectsUnverifiedBeforeWrite(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".codex", "config.toml")
	writeFile(t, path, sampleConfig)
	before := readFile(t, path)

	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "0.154.0",
		MCP: map[string]adapter.MCPEntry{
			"needs-env": {Command: "mcp-server", EnvRefs: map[string]string{"TOKEN": "SERVICE_TOKEN"}},
		},
	}
	a := &Adapter{Probe: probeVersion("0.154.0")}
	err := a.Validate(ctx, home, desired)
	if err == nil {
		t.Fatal("envRefs must be rejected as unverified for codex")
	}
	var ve *adapter.ValidateError
	if !errors.As(err, &ve) || ve.Capability != adapter.CapabilityMCP {
		t.Fatalf("expected ValidateError for mcp, got %v", err)
	}
	if got := readFile(t, path); got != before {
		t.Fatal("validate must not write anything")
	}

	unsupported := &Adapter{Probe: probeVersion("0.154.0"), GOOS: "darwin"}
	if err := unsupported.Validate(ctx, home, desired); err == nil {
		t.Fatal("unverified OS must be rejected")
	}

	// 校验失败不得留下任何写入产物（agentd 目录下只应有校验前存在的文件）。
	if _, err := os.Stat(filepath.Join(home, ".codex", "skills")); !os.IsNotExist(err) {
		t.Fatalf("validate must not create the skills dir: %v", err)
	}
	if got := readFile(t, path); got != before {
		t.Fatal("validate must not modify the config")
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

// 回归（核查缺陷 1）：MCP 条目省略 args 是常见 profile 写法，两侧投影必须同形，
// 否则机器会永远停在 verify 失败 + 回滚。
func TestMCPEntryWithoutArgsConverges(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := &Adapter{Probe: probeVersion("0.154.0")}
	desired := adapter.AgentDesiredState{
		Family:  ID,
		Version: "0.154.0",
		MCP: map[string]adapter.MCPEntry{
			"no-args": {Command: "/usr/local/bin/my-mcp"},
		},
	}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatalf("no-args MCP entry must converge: desired=%s observed=%s (%+v)",
			inv2.DesiredProjectionDigest, inv2.ObservedProjectionDigest, inv2.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, desired, inv2); err != nil || len(cs) != 0 {
		t.Fatalf("second reconcile must be a no-op: %+v err=%v", cs, err)
	}
}

// 回归（核查缺陷 3）：~/.codex/AGENTS.md 常是软链（本机真实布局）。原子写必须
// 写穿链接，而不是把链接换成普通文件（护栏 #3）。
func TestManagedBlockWritePreservesSymlink(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	target := filepath.Join(home, ".config", "plexus", "personal", "rules", "global.md")
	writeFile(t, target, "# 全局规则\n\n用户正文\n")
	link := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	a := &Adapter{Probe: probeVersion("0.154.0")}
	desired := adapter.AgentDesiredState{Family: ID, Version: "0.154.0",
		Rules: map[string]adapter.RulesEntry{"global": {Content: "Fleet 规则"}}}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("AGENTS.md symlink was replaced by a regular file (mode=%v)", fi.Mode())
	}
	got := readFile(t, target)
	if !strings.Contains(got, "# 全局规则") || !strings.Contains(got, "用户正文") {
		t.Fatalf("user content lost:\n%s", got)
	}
	if !strings.Contains(got, kit.BlockBegin) || !strings.Contains(got, "Fleet 规则") {
		t.Fatalf("managed block not written through the symlink:\n%s", got)
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatal("rules through symlink did not converge")
	}
}

// 回归（核查缺陷 6）：config.toml 的受管键级回退（外部编辑场景）此前没有测试执行。
func TestMergeManagedRestoresOnlyManagedTOMLKeys(t *testing.T) {
	home := t.TempDir()
	a := &Adapter{Probe: probeVersion("0.154.0")}
	path := filepath.Join(home, ".codex", "config.toml")
	// 模拟"备份时"的受管键：model=gpt-5.5、provider endpoint=A。
	writeFile(t, path, sampleConfig)
	if err := a.MergeManaged(home, filepath.Join(".codex", "config.toml"), map[string]any{
		"model": "gpt-5.5",
		"model_providers.fleet": map[string]any{
			"name": "agent-fleet", "base_url": "https://a.example/v1", "wire_api": "responses",
		},
	}); err != nil {
		t.Fatalf("merge managed: %v", err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, `model = "gpt-5.5"`) {
		t.Fatalf("managed model key not restored:\n%s", text)
	}
	if !strings.Contains(text, "[model_providers.fleet]") || !strings.Contains(text, `base_url = "https://a.example/v1"`) {
		t.Fatalf("managed provider table not restored:\n%s", text)
	}
	// 未托管键必须原样保留。
	for _, keep := range []string{"# 用户注释必须保留", `approval_policy = "on-request"`, `[projects."/home/u/work"]`, `[mcp_servers.serena]`} {
		if !strings.Contains(text, keep) {
			t.Fatalf("unmanaged content lost in managed-key rollback: %q\n%s", keep, text)
		}
	}
}
