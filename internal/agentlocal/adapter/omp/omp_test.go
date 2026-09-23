package omp

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

// OMP 家族 fixture（矩阵验收 2）。全部在 t.TempDir() 作为 HOME 根下运行。
// 证据来源见 docs/adapters-batch-1.md「OMP」。

const sampleConfig = `# 用户注释必须保留
symbolPreset: nerd

modelRoles:
  default: zai/glm-5.3 # 行尾注释

composer:
  shape: box
`

const sampleMCP = `{
  "mcpServers": {
    "user-owned": { "type": "stdio", "command": "user-mcp", "args": ["--x"] }
  },
  "disabledServers": ["user-owned"]
}
`

func probeVersion(v string) kit.VersionProbe {
	return func(_ context.Context, _ string, _ ...string) (string, error) {
		return "omp/" + v + "\n", nil
	}
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
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
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

func desired(t *testing.T) adapter.AgentDesiredState {
	t.Helper()
	return adapter.AgentDesiredState{
		Family:  ID,
		Version: "17.4.0",
		Config:  json.RawMessage(`{"model":"glm-5.3","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_ZAI_KEY"}}`),
		MCP: map[string]adapter.MCPEntry{
			"local-tools": {Command: "/usr/local/bin/my-mcp", Args: []string{"--stdio"}},
		},
		Rules: map[string]adapter.RulesEntry{"global": {Content: "Follow repository instructions."}},
	}
}

func TestDetectDistinguishesMissingFromUnparseable(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	a := &Adapter{Probe: probeVersion("17.4.0")}
	d, err := a.Detect(ctx, home)
	if err != nil || !d.Installed || d.Version != "17.4.0" {
		t.Fatalf("detect = %+v err=%v", d, err)
	}
	if d.ConfigPath != filepath.Join(home, ".omp", "agent", "config.yml") {
		t.Fatalf("config path = %q", d.ConfigPath)
	}
	a = &Adapter{Probe: probeMissing()}
	if d, err = a.Detect(ctx, home); err != nil || d.Installed {
		t.Fatalf("missing binary → Installed=false, no error; got %+v err=%v", d, err)
	}
	a = &Adapter{Probe: func(_ context.Context, _ string, _ ...string) (string, error) {
		return "oh-my-pi 17.4.0\n", nil
	}}
	if _, err = a.Detect(ctx, home); err == nil {
		t.Fatal("unparseable output must be an explicit error")
	}
}

func TestMergeWritePreservesUnmanagedAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	write(t, filepath.Join(home, ".omp", "agent", "config.yml"), sampleConfig)
	write(t, filepath.Join(home, ".omp", "agent", "mcp.json"), sampleMCP)
	a := &Adapter{Probe: probeVersion("17.4.0")}
	d := desired(t)

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

	cfgText := read(t, filepath.Join(home, ".omp", "agent", "config.yml"))
	for _, keep := range []string{"# 用户注释必须保留", "symbolPreset: nerd", "shape: box", "# 行尾注释"} {
		if !strings.Contains(cfgText, keep) {
			t.Fatalf("unmanaged yaml content lost: %q\n%s", keep, cfgText)
		}
	}
	if !strings.Contains(cfgText, "default: fleet/glm-5.3") {
		t.Fatalf("model selector not written:\n%s", cfgText)
	}
	modelsText := read(t, filepath.Join(home, ".omp", "agent", "models.yml"))
	for _, want := range []string{"fleet:", "baseUrl: https://api.example.com/v1", "api: openai-completions", "apiKey: FLEET_ZAI_KEY", "id: glm-5.3"} {
		if !strings.Contains(modelsText, want) {
			t.Fatalf("models.yml missing %q:\n%s", want, modelsText)
		}
	}
	mcpText := read(t, filepath.Join(home, ".omp", "agent", "mcp.json"))
	if !strings.Contains(mcpText, "user-owned") || !strings.Contains(mcpText, "disabledServers") {
		t.Fatalf("unmanaged mcp content lost:\n%s", mcpText)
	}
	if !strings.Contains(mcpText, "local-tools") || !strings.Contains(mcpText, "/usr/local/bin/my-mcp") {
		t.Fatalf("managed mcp entry missing:\n%s", mcpText)
	}
	rulesText := read(t, filepath.Join(home, ".omp", "agent", "AGENTS.md"))
	if !strings.Contains(rulesText, "Follow repository instructions.") || !strings.Contains(rulesText, kit.BlockBegin) {
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
	path := filepath.Join(home, ".omp", "agent", "config.yml")
	write(t, path, sampleConfig)
	a := &Adapter{Probe: probeVersion("17.4.0")}
	d := adapter.AgentDesiredState{Family: ID, Version: "17.4.0", Config: json.RawMessage(`{"model":"glm-5.3","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_ZAI_KEY"}}`)}
	// 先纳管（写入 selector 与 models.yml）。
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

	// 未托管改动：不得触发 drift（在**纳管后**的文件上改，避免把受管键一并改回）。
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, strings.Replace(string(after), "symbolPreset: nerd", "symbolPreset: ascii", 1))
	unmanaged, err := a.Inventory(ctx, home, d)
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged.ObservedProjectionDigest != base.ObservedProjectionDigest {
		t.Fatal("unmanaged edit changed the managed projection")
	}

	// 受管改动：必须触发 drift。
	write(t, path, strings.Replace(sampleConfig, "default: zai/glm-5.3", "default: zai/other", 1))
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
	if len(cs) != 1 || cs[0].Key != "config.modelRoles.default" {
		t.Fatalf("expected single selector change, got %+v", cs)
	}
}

func TestValidateRejectsBeforeWrite(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	path := filepath.Join(home, ".omp", "agent", "config.yml")
	write(t, path, sampleConfig)
	before := read(t, path)
	a := &Adapter{Probe: probeVersion("17.4.0")}

	// envRefs 未验证：明确拒绝（不静默跳过）。
	withEnv := desired(t)
	withEnv.MCP = map[string]adapter.MCPEntry{
		"needs-env": {Command: "mcp-server", EnvRefs: map[string]string{"TOKEN": "SERVICE_TOKEN"}},
	}
	err := a.Validate(ctx, home, withEnv)
	var ve *adapter.ValidateError
	if !errors.As(err, &ve) || ve.Capability != adapter.CapabilityMCP {
		t.Fatalf("envRefs must be rejected for omp, got %v", err)
	}
	// provider 缺 model：选择器无法渲染，写入前拒绝。
	noModel := desired(t)
	noModel.Config = json.RawMessage(`{"provider":{"endpoint":"https://api.example.com/v1"}}`)
	if err := a.Validate(ctx, home, noModel); err == nil {
		t.Fatal("provider without model must be rejected for omp")
	}
	// 未验证 OS。
	other := &Adapter{Probe: probeVersion("17.4.0"), GOOS: "windows"}
	if err := other.Validate(ctx, home, desired(t)); err == nil {
		t.Fatal("unverified OS must be rejected")
	}
	if got := read(t, path); got != before {
		t.Fatal("validate must not write")
	}
}

func TestSkillLinkRequiresMaterializedArtifact(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	const digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	d := adapter.AgentDesiredState{Family: ID, Version: "17.4.0",
		Skills: map[string]domain.SkillDesired{"superpowers": {ContentDigest: digest}}}
	a := &Adapter{Probe: probeVersion("17.4.0")}
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
	if err := os.MkdirAll(kit.SkillCacheDir(home, "superpowers", digest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ctx, home, d, changes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := kit.ReadSkillDigest(filepath.Join(home, ".omp", "agent", "skills", "superpowers")); got != digest {
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
