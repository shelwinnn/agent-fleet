package opencode

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

// OpenCode 家族 fixture（矩阵验收 2）。本机未安装该家族，故 fixture 只验证
// 本适配器的读写/所有权/拒绝语义；能力结论的文档证据见 docs/adapters-batch-1.md。

const sampleConfig = `{
  // 用户注释（JSONC）：已知在合并写后不保留，见未验证/限制清单
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "theme": "tokyonight",
  "mcp": {
    "user-owned": { "type": "local", "command": ["user-mcp"], "enabled": true }
  }
}
`

func probeVersion(v string) kit.VersionProbe {
	return func(_ context.Context, _ string, _ ...string) (string, error) { return v + "\n", nil }
}

func probeMissing() kit.VersionProbe {
	return func(_ context.Context, bin string, _ ...string) (string, error) {
		return "", &exec.Error{Name: bin, Err: exec.ErrNotFound}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func desired() adapter.AgentDesiredState {
	return adapter.AgentDesiredState{
		Family:  ID,
		Version: "1.18.32",
		Config:  json.RawMessage(`{"model":"gpt-5.6","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_OPENAI_KEY"}}`),
		MCP: map[string]adapter.MCPEntry{
			"local-tools": {Command: "npx", Args: []string{"-y", "my-mcp"},
				EnvRefs: map[string]string{"SERVICE_TOKEN": "SERVICE_TOKEN"}},
		},
		Rules: map[string]adapter.RulesEntry{"global": {Content: "Follow repository instructions."}},
	}
}

func TestDetectDistinguishesMissingFromUnparseable(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d, err := a.Detect(ctx, home)
	if err != nil || !d.Installed || d.Version != "1.18.32" {
		t.Fatalf("detect = %+v err=%v", d, err)
	}
	if d.ConfigPath != filepath.Join(home, ".config", "opencode", "opencode.json") {
		t.Fatalf("config path = %q", d.ConfigPath)
	}
	a = &Adapter{Probe: probeMissing()}
	if d, err = a.Detect(ctx, home); err != nil || d.Installed {
		t.Fatalf("missing binary → Installed=false, no error; got %+v err=%v", d, err)
	}
	a = &Adapter{Probe: probeVersion("opencode 1.18.32 (build abc)")}
	if _, err = a.Detect(ctx, home); err == nil {
		t.Fatal("unparseable/format-unknown output must be an explicit error, not a guess")
	}
}

func TestMergeWritePreservesUnmanagedAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	write(t, path, sampleConfig)
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d := desired()

	inv, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if inv.DesiredProjectionDigest == inv.ObservedProjectionDigest {
		t.Fatal("expected drift before apply")
	}
	changes, err := a.Plan(ctx, home, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}

	doc, err := decodeJSONC([]byte(read(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	if doc["theme"] != "tokyonight" {
		t.Fatalf("unmanaged keys lost: %+v", doc)
	}
	// $schema 已在样例中：必须原样保留（而不是被改写）。
	if doc["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("existing $schema must be preserved: %v", doc["$schema"])
	}
	if doc["model"] != "fleet/gpt-5.6" {
		t.Fatalf("model = %v", doc["model"])
	}
	providers, _ := doc["provider"].(map[string]any)
	block, _ := providers[providerID].(map[string]any)
	opts, _ := block["options"].(map[string]any)
	if opts["baseURL"] != "https://api.example.com/v1" || opts["apiKey"] != "{env:FLEET_OPENAI_KEY}" {
		t.Fatalf("provider block = %+v", block)
	}
	if _, ok := block["models"].(map[string]any)["gpt-5.6"]; !ok {
		t.Fatalf("provider models missing: %+v", block["models"])
	}
	servers, _ := doc["mcp"].(map[string]any)
	if _, ok := servers["user-owned"]; !ok {
		t.Fatalf("unmanaged mcp server lost: %+v", servers)
	}
	managed, _ := servers["local-tools"].(map[string]any)
	env, _ := managed["environment"].(map[string]any)
	if env["SERVICE_TOKEN"] != "{env:SERVICE_TOKEN}" {
		t.Fatalf("envRefs must render as {env:VAR} indirection: %+v", managed)
	}
	cmd, _ := managed["command"].([]any)
	if len(cmd) != 3 || cmd[0] != "npx" {
		t.Fatalf("command array = %+v", cmd)
	}
	rulesText := read(t, filepath.Join(home, ".config", "opencode", "AGENTS.md"))
	if !strings.Contains(rulesText, kit.BlockBegin) || !strings.Contains(rulesText, "Follow repository instructions.") {
		t.Fatalf("rules block missing:\n%s", rulesText)
	}

	inv2, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatalf("not converged: %+v", inv2.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, d, inv2); err != nil || len(cs) != 0 {
		t.Fatalf("second reconcile must be a no-op: %+v err=%v", cs, err)
	}
	if err := a.HealthCheck(ctx, home, d); err != nil {
		t.Fatalf("health: %v", err)
	}
}

func TestDriftOnlyFromManagedFields(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	write(t, path, sampleConfig)
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d := desired()
	inv, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatal(err)
	}
	base, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}

	after := read(t, path)
	write(t, path, strings.Replace(after, `"theme": "tokyonight"`, `"theme": "gruvbox"`, 1))
	unmanaged, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged.ObservedProjectionDigest != base.ObservedProjectionDigest {
		t.Fatal("unmanaged edit changed the managed projection")
	}

	after = read(t, path)
	write(t, path, strings.Replace(after, `"model": "fleet/gpt-5.6"`, `"model": "someone/else"`, 1))
	drifted, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.ObservedProjectionDigest == drifted.DesiredProjectionDigest {
		t.Fatal("managed edit not detected")
	}
	cs, err := a.Plan(ctx, home, d, drifted)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Key != "config.model" {
		t.Fatalf("expected single model change, got %+v", cs)
	}
}

func TestValidateRejectsBeforeWrite(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	write(t, path, sampleConfig)
	before := read(t, path)
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d := desired()

	// OpenCode 的 Skill 目录名有硬约束（docs/skills）。
	bad := d
	bad.Skills = map[string]domain.SkillDesired{"Bad_Name": {ContentDigest: "sha256:cc"}}
	err := a.Validate(ctx, home, bad)
	var ve *adapter.ValidateError
	if !errors.As(err, &ve) || ve.Capability != adapter.CapabilitySkills {
		t.Fatalf("invalid skill name must be rejected, got %v", err)
	}

	// 未验证 OS。
	other := &Adapter{Probe: probeVersion("1.18.32"), GOOS: "windows"}
	if err := other.Validate(ctx, home, d); err == nil {
		t.Fatal("unverified OS must be rejected")
	}
	if got := read(t, path); got != before {
		t.Fatal("validate must not write")
	}
}

func TestSkillLinkRequiresMaterializedArtifact(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	const digest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	d := adapter.AgentDesiredState{Family: ID, Version: "1.18.32",
		Skills: map[string]domain.SkillDesired{"code-review": {ContentDigest: digest}}}
	a := &Adapter{Probe: probeVersion("1.18.32")}
	inv, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err == nil {
		t.Fatal("missing artifact must fail explicitly")
	}
	if err := os.MkdirAll(kit.SkillCacheDir(home, "code-review", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := kit.ReadSkillDigest(filepath.Join(home, ".config", "opencode", "skills", "code-review")); got != digest {
		t.Fatalf("skill link digest = %q", got)
	}
	inv2, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatal("skill link not converged")
	}
}

// 新建配置（文件不存在）时必须补上官方 $schema，便于用户校验（docs/config）。
func TestApplyOnMissingFileAddsSchema(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d := adapter.AgentDesiredState{Family: ID, Version: "1.18.32", Config: json.RawMessage(`{"model":"gpt-5.6"}`)}
	inv, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatal(err)
	}
	doc, err := decodeJSONC([]byte(read(t, filepath.Join(home, ".config", "opencode", "opencode.json"))))
	if err != nil {
		t.Fatal(err)
	}
	if doc["$schema"] != schemaURL || doc["model"] != "gpt-5.6" {
		t.Fatalf("new config = %+v", doc)
	}
}

// 回归（核查缺陷 1）：MCP 条目省略 args 必须收敛（OpenCode 侧把 command 与 args
// 拼成一个切片，本条锁住该行为不被改坏）。
func TestMCPEntryWithoutArgsConverges(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d := adapter.AgentDesiredState{Family: ID, Version: "1.18.32",
		MCP: map[string]adapter.MCPEntry{"no-args": {Command: "/usr/local/bin/my-mcp"}}}
	inv, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	inv2, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if inv2.DesiredProjectionDigest != inv2.ObservedProjectionDigest {
		t.Fatalf("no-args MCP entry must converge: %+v", inv2.ManagedProjection)
	}
	if cs, err := a.Plan(ctx, home, d, inv2); err != nil || len(cs) != 0 {
		t.Fatalf("second reconcile must be a no-op: %+v err=%v", cs, err)
	}
}

// 回归（核查缺陷 5）：受管 MCP 的 environment 必须从**本机文件**读取，
// 外部改动要产生 drift（用期望值回填会让 FR-8 判据失效）。
func TestManagedMCPEnvironmentDriftIsDetected(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	write(t, path, sampleConfig)
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d := adapter.AgentDesiredState{Family: ID, Version: "1.18.32",
		MCP: map[string]adapter.MCPEntry{
			"local-tools": {Command: "npx", Args: []string{"-y", "my-mcp"},
				EnvRefs: map[string]string{"SERVICE_TOKEN": "SERVICE_TOKEN"}},
		}}
	inv, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	converged, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if converged.DesiredProjectionDigest != converged.ObservedProjectionDigest {
		t.Fatalf("environment did not converge: %+v", converged.ManagedProjection)
	}

	// 外部把受管 env 从 {env:VAR} 改成字面量。
	after := read(t, path)
	write(t, path, strings.Replace(after, `"{env:SERVICE_TOKEN}"`, `"literal-secret-token"`, 1))
	drifted, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.ObservedProjectionDigest == drifted.DesiredProjectionDigest {
		t.Fatalf("external edit of managed MCP environment must drift: %+v", drifted.ManagedProjection)
	}
	cs, err := a.Plan(ctx, home, d, drifted)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Step != "mcp" || cs[0].Key != "mcp.local-tools" {
		t.Fatalf("expected a single mcp change, got %+v", cs)
	}
}

// 覆盖（核查缺陷 6）：ManagedFiles / ExtractManaged / MergeManaged 的
// opencode.json 与 AGENTS.md 两条回退分支此前零测试引用。
func TestManagedFilesAndMergeManagedRollback(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	write(t, path, sampleConfig)
	a := &Adapter{Probe: probeVersion("1.18.32")}
	d := desired()
	inv, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := a.Plan(ctx, home, d, inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatal(err)
	}

	files, err := a.ManagedFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(".config", "opencode", "opencode.json"), filepath.Join(".config", "opencode", "AGENTS.md")}
	if len(files) != len(want) {
		t.Fatalf("managed files = %v", files)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Fatalf("managed files = %v, want %v", files, want)
		}
	}

	// 受管键提取（备份粒度）：model 与 provider.fleet 必须在集合内。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := a.ExtractManaged(raw)
	if err != nil {
		t.Fatal(err)
	}
	if managed["model"] != "fleet/gpt-5.6" {
		t.Fatalf("extracted model = %v", managed["model"])
	}
	if _, ok := managed["provider."+providerID]; !ok {
		t.Fatalf("extracted managed keys missing provider: %+v", managed)
	}

	// 外部编辑：改掉受管键 → 受管字段级回退只还原受管键，未托管键保留。
	edited := strings.Replace(read(t, path), `"model": "fleet/gpt-5.6"`, `"model": "user/other"`, 1)
	write(t, path, edited)
	if err := a.MergeManaged(home, want[0], managed); err != nil {
		t.Fatalf("merge managed: %v", err)
	}
	doc, err := decodeJSONC([]byte(read(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	if doc["model"] != "fleet/gpt-5.6" {
		t.Fatalf("managed model not rolled back: %v", doc["model"])
	}
	if doc["theme"] != "tokyonight" {
		t.Fatalf("unmanaged key lost in managed rollback: %+v", doc)
	}

	// AGENTS.md 分支：只还原受管块，块外内容保留。
	rulesPath := filepath.Join(home, ".config", "opencode", "AGENTS.md")
	write(t, rulesPath, "# 用户内容\n\n"+kit.RenderManagedBlock("旧规则"))
	if err := a.MergeManaged(home, want[1], map[string]any{"agentFleetRules": "回退规则"}); err != nil {
		t.Fatal(err)
	}
	rulesText := read(t, rulesPath)
	if !strings.Contains(rulesText, "# 用户内容") || !strings.Contains(rulesText, "回退规则") ||
		strings.Contains(rulesText, "旧规则") {
		t.Fatalf("managed block rollback wrong:\n%s", rulesText)
	}
}
