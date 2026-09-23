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
	if doc["theme"] != "tokyonight" || doc["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("unmanaged keys lost: %+v", doc)
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
