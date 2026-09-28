package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/kit"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 本文件是 Hermes 家族的 fixture（矩阵逐家族验收 5 项：身份与兼容性 / 配置与所有权 /
// 生命周期 / 恢复与一致性 / 验收记录）。全部以 t.TempDir() 为 HOME 根，绝不触碰真实
// ~/.hermes；Probe/ConfigCheck 一律注入（护栏 #12）。证据来源见
// docs/adapters-batch-2.md §4 与 §10。

const (
	repoVersion  = "0.21.2"
	repoUpstream = "be2f7e9c"
	skillDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// ---- 桩与辅助 ----

// recordProbe 返回版本桩并记录每次调用的注入 home（默认探测必须钉 HOME/HERMES_HOME）。
func recordProbe(version string, calls *[]string) VersionProbe {
	return func(_ context.Context, home string) (string, error) {
		if calls != nil {
			*calls = append(*calls, home)
		}
		return "Hermes Agent v" + version + " (2026.9.11) · upstream " + repoUpstream + "\n" +
			"Install directory: /home/user/.hermes/hermes-agent\n" +
			"Install method: git\n", nil
	}
}

func probeMissing() VersionProbe {
	return func(_ context.Context, _ string) (string, error) {
		return "", &exec.Error{Name: binaryName, Err: exec.ErrNotFound}
	}
}

func probeGarbage() VersionProbe {
	return func(_ context.Context, _ string) (string, error) {
		return "not the hermes banner\n", nil
	}
}

// testConfigCheck 是 `hermes config check` 的桩：只报告配置版本。
func testConfigCheck(version int) ConfigCheckProbe {
	return func(_ context.Context, _ string) (string, error) {
		return "\n📋 Configuration Status\n\n  Config version: " + fmt.Sprintf("%d", version) + " ✓\n", nil
	}
}

func newTestAdapter() *Adapter {
	return &Adapter{Probe: recordProbe(repoVersion, nil), ConfigCheck: testConfigCheck(supportedConfigVersion)}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fixtureConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func baselineHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), fixtureConfig(t))
	return home
}

// materializeSkill 建出 Fleet 技能缓存目录（EnsureSkillLink 要求工件已物化）。
func materializeSkill(t *testing.T, home, name, digest string) {
	t.Helper()
	if err := os.MkdirAll(kit.SkillCacheDir(home, name, digest), 0o755); err != nil {
		t.Fatal(err)
	}
}

func fullDesired(t *testing.T, home string) adapter.AgentDesiredState {
	t.Helper()
	materializeSkill(t, home, "fleet-skill", skillDigest)
	return adapter.AgentDesiredState{
		Family:  ID,
		Version: repoVersion,
		Config:  json.RawMessage(`{"model":"fleet-model","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_API_KEY"}}`),
		MCP: map[string]adapter.MCPEntry{
			"fleet-tool": {Command: "npx", Args: []string{"-y", "@fleet/tool"}},
		},
		Skills: map[string]domain.SkillDesired{"fleet-skill": {ContentDigest: skillDigest}},
	}
}

// reconcile 跑一遍 Validate→Inventory→Plan→Apply，返回 apply 后的观测态。
func reconcile(t *testing.T, a *Adapter, home string, desired adapter.AgentDesiredState) adapter.AgentObservedState {
	t.Helper()
	ctx := context.Background()
	if err := a.Validate(ctx, home, desired); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := a.Apply(ctx, home, desired, changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	inv2, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatalf("Inventory(after): %v", err)
	}
	return inv2
}

func planAfter(t *testing.T, a *Adapter, home string, desired adapter.AgentDesiredState) []adapter.Change {
	t.Helper()
	ctx := context.Background()
	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return changes
}

func decodeYAML(t *testing.T, text string) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := yaml.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("yaml decode: %v", err)
	}
	return out
}

// ---- 1. 身份与兼容性 ----

func TestDetectDistinguishesMissingFromUnparseable(t *testing.T) {
	ctx := context.Background()

	t.Run("missing", func(t *testing.T) {
		a := &Adapter{Probe: probeMissing()}
		d, err := a.Detect(ctx, t.TempDir())
		if err != nil {
			t.Fatalf("missing executable must not be an error, got %v", err)
		}
		if d.Installed || d.Version != "" {
			t.Fatalf("missing executable: got installed=%v version=%q", d.Installed, d.Version)
		}
		if d.Family != ID || !strings.HasSuffix(d.ConfigPath, filepath.Join(".hermes", "config.yaml")) {
			t.Fatalf("unexpected detection metadata: %+v", d)
		}
	})

	t.Run("unparseable", func(t *testing.T) {
		a := &Adapter{Probe: probeGarbage()}
		d, err := a.Detect(ctx, t.TempDir())
		if err == nil {
			t.Fatalf("unparseable version must be an explicit error, got %+v", d)
		}
		if d.Installed {
			t.Fatalf("unparseable version must not report installed: %+v", d)
		}
	})

	t.Run("installed", func(t *testing.T) {
		var calls []string
		home := t.TempDir()
		a := &Adapter{Probe: recordProbe(repoVersion, &calls)}
		d, err := a.Detect(ctx, home)
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if !d.Installed || d.Version != repoVersion {
			t.Fatalf("got installed=%v version=%q", d.Installed, d.Version)
		}
		if len(calls) != 1 || calls[0] != home {
			t.Fatalf("probe must run against the injected home, got %v (want %s)", calls, home)
		}
	})

	t.Run("non-linux-is-rejected", func(t *testing.T) {
		a := newTestAdapter()
		a.GOOS = "darwin"
		if err := a.Validate(ctx, baselineHome(t), adapter.AgentDesiredState{Family: ID}); err == nil {
			t.Fatal("non-linux must be rejected")
		}
	})
}

func TestVersionParserRejectsAnomalies(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"ok", "Hermes Agent v0.21.2 (2026.9.11) · upstream be2f7e9c\nInstall method: git\n", "0.21.2"},
		{"ok-prerelease", "Hermes Agent v0.22.0-rc.1 (2026.10.01) · upstream deadbeef\n", "0.22.0-rc.1"},
		{"leading-blank", "\nHermes Agent v0.21.2 (2026.9.11) · upstream be2f7e9c\n", "0.21.2"},
		{"missing-upstream", "Hermes Agent v0.21.2 (2026.9.11)\n", ""},
		{"missing-date", "Hermes Agent v0.21.2 · upstream be2f7e9c\n", ""},
		{"wrong-prefix", "hermes 0.21.2 (2026.9.11) · upstream be2f7e9c\n", ""},
		{"no-triple", "Hermes Agent v0.21 (2026.9.11) · upstream be2f7e9c\n", ""},
		{"invalid-choice-stderr", "usage: hermes [-h] {config,doctor,status,...}\nhermes: error: argument command: invalid choice: 'version'\n", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseVersion(c.out); got != c.want {
				t.Fatalf("parseVersion(%q) = %q, want %q", c.out, got, c.want)
			}
		})
	}
}

func TestCapabilitiesDeclareSurfaceAndRecordUnverifiedItems(t *testing.T) {
	decls := newTestAdapter().Capabilities()
	byCap := map[adapter.Capability]adapter.CapabilityDecl{}
	for _, d := range decls {
		byCap[d.Capability] = d
	}
	for _, c := range []adapter.Capability{adapter.CapabilityVersion, adapter.CapabilityModelProvider, adapter.CapabilityMCP, adapter.CapabilitySkills} {
		if got := byCap[c].State; got != adapter.SupportSupported {
			t.Errorf("%s state = %q, want supported", c, got)
		}
	}
	if got := byCap[adapter.CapabilityRules].State; got != adapter.SupportUnsupported {
		t.Errorf("rules state = %q, want unsupported (SOUL.md out of managed scope, §4.2/§4.4)", got)
	}
	// §4.4 未验证项必须落进 Reason/Evidence，而不是被静默省略。
	checks := map[adapter.Capability][]string{
		adapter.CapabilityVersion:       {"0.21.2", "doctor", "status --deep", "macOS", "PyPI", "安装/升级"},
		adapter.CapabilityModelProvider: {"apiKeyEnv", "_config_version", ".env", "schema", "环境变量层"},
		adapter.CapabilityMCP:           {"envRefs", "mcp list", "连通性", "url/headers"},
		adapter.CapabilitySkills:        {"external_dirs", "契约缺口"},
		adapter.CapabilityRules:         {"SOUL.md", "§4.4 第 8 项"},
	}
	for cap, needles := range checks {
		decl := byCap[cap]
		hay := decl.Reason + " " + decl.Evidence
		for _, n := range needles {
			if !strings.Contains(hay, n) {
				t.Errorf("%s Reason/Evidence does not mention %q", cap, n)
			}
		}
	}
}

// ---- 2. 配置与所有权 ----

func TestMergeWritePreservesUnmanagedAndIsIdempotent(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)

	reconcile(t, a, home, desired)

	raw := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
	before := decodeYAML(t, fixtureConfig(t))
	after := decodeYAML(t, raw)

	if len(after) != 65 {
		t.Fatalf("top-level key count = %d, want 65 (unmanaged surface must be preserved)", len(after))
	}
	// 未托管顶层键逐键保留。
	for k, v := range before {
		if k == "model" || k == "mcp_servers" {
			continue
		}
		if !kit.ValuesEqual(v, after[k]) {
			t.Errorf("unmanaged top-level key %q changed: %v -> %v", k, v, after[k])
		}
	}
	// model 段内未托管兄弟键保留，受管键到位。
	modelBefore := before["model"].(map[string]any)
	modelAfter := after["model"].(map[string]any)
	if !kit.ValuesEqual(modelBefore["context_length"], modelAfter["context_length"]) {
		t.Errorf("model.context_length (unmanaged) changed: %v", modelAfter["context_length"])
	}
	for k, want := range map[string]any{
		"default": "fleet-model", "provider": "custom",
		"base_url": "https://api.example.com/v1", "api_key": "${FLEET_API_KEY}",
	} {
		if !kit.ValuesEqual(want, modelAfter[k]) {
			t.Errorf("model.%s = %v, want %v", k, modelAfter[k], want)
		}
	}
	// MCP：受管条目写入 command/args，条目内未托管键（env/enabled/timeout）逐字节保留。
	serversBefore := before["mcp_servers"].(map[string]any)
	serversAfter := after["mcp_servers"].(map[string]any)
	fleet := serversAfter["fleet-tool"].(map[string]any)
	if fleet["command"] != "npx" || !kit.ValuesEqual([]any{"-y", "@fleet/tool"}, fleet["args"]) {
		t.Errorf("fleet-tool not written: %v", fleet)
	}
	for _, name := range []string{"hermes-studio-api", "hermes-studio-browser", "hermes-studio-devices", "hermes-studio-use"} {
		entryBefore := serversBefore[name].(map[string]any)
		entryAfter := serversAfter[name].(map[string]any)
		for k, v := range entryBefore {
			if !kit.ValuesEqual(v, entryAfter[k]) {
				t.Errorf("mcp_servers.%s.%s (unmanaged) changed: %v -> %v", name, k, v, entryAfter[k])
			}
		}
	}

	// 密钥面在磁盘上逐字节保留（Fleet 只按叶子路径写，绝不整树 Decode）。
	for _, secret := range []string{
		`${DELEGATION_KEY}`, `${AUX_APPROVAL_KEY}`, `${AUX_VISION_KEY}`,
		`${DASHBOARD_PASSWORD}`, `${DASHBOARD_PASSWORD_HASH}`, `${DASHBOARD_SECRET}`,
		`BW_ACCESS_TOKEN`, `http://proxy.example:3128`,
	} {
		if !strings.Contains(raw, secret) {
			t.Errorf("secret-bearing value %s was not preserved", secret)
		}
	}
	// 注释保留（Node 树合并）。
	for _, comment := range []string{
		"# Hermes Agent configuration (fixture",
		"# Managed: default model id.",
		"# Secret face: delegation.api_key must never be read or written.",
		"# Multi-shape entries: one command+args only",
	} {
		if !strings.Contains(raw, comment) {
			t.Errorf("comment %q was not preserved", comment)
		}
	}

	// Skills 软链到位。
	if got := kit.ReadSkillDigest(filepath.Join(home, ".hermes", "skills", "fleet-skill")); got != skillDigest {
		t.Errorf("skill link digest = %q, want %q", got, skillDigest)
	}

	// 幂等：再跑一轮 Plan 为空、Apply 零写入。
	if changes := planAfter(t, a, home, desired); len(changes) != 0 {
		t.Fatalf("second reconcile must be idempotent, got %v", changes)
	}
	ctx := context.Background()
	if err := a.Apply(ctx, home, desired, nil); err != nil {
		t.Fatalf("empty Apply: %v", err)
	}
	if again := readText(t, filepath.Join(home, ".hermes", "config.yaml")); again != raw {
		t.Fatal("a no-change reconcile rewrote config.yaml")
	}
}

// TestInvalidMCPEntryIsNotTouched 锁定"未点名即不托管"：配置里已有的 [mcp_servers.*] 不被改写。
func TestInvalidMCPEntryIsNotTouched(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)

	raw := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
	if !strings.Contains(raw, `HERMES_MCP_SERVER_NAME: "hermes-studio-browser"`) {
		t.Fatal("un-named mcp entry was modified")
	}
}

func TestValidateRejectsUnverifiedBeforeWrite(t *testing.T) {
	home := baselineHome(t)
	configPath := filepath.Join(home, ".hermes", "config.yaml")
	before := readText(t, configPath)

	desired := fullDesired(t, home)
	entry := desired.MCP["fleet-tool"]
	entry.EnvRefs = map[string]string{"TOKEN": "FLEET_TOKEN"}
	desired.MCP["fleet-tool"] = entry

	err := newTestAdapter().Validate(context.Background(), home, desired)
	if err == nil {
		t.Fatal("envRefs must be rejected before any write")
	}
	var ve *adapter.ValidateError
	if !errors.As(err, &ve) || ve.State != adapter.SupportUnverified || ve.Capability != adapter.CapabilityMCP {
		t.Fatalf("want *ValidateError(envRefs/unverified), got %v", err)
	}
	if !strings.Contains(err.Error(), "envRefs") {
		t.Fatalf("rejection must name envRefs: %v", err)
	}
	if got := readText(t, configPath); got != before {
		t.Fatal("Validate wrote to config.yaml")
	}
	if _, statErr := os.Lstat(filepath.Join(home, ".hermes", "skills")); !os.IsNotExist(statErr) {
		t.Fatal("Validate created the skills dir")
	}
}

func TestValidateRejectsRulesRequest(t *testing.T) {
	home := baselineHome(t)
	desired := fullDesired(t, home)
	desired.Rules = map[string]adapter.RulesEntry{"global": {Content: "x"}}
	err := newTestAdapter().Validate(context.Background(), home, desired)
	if err == nil || !strings.Contains(err.Error(), "rules") || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("rules must be rejected as unsupported, got %v", err)
	}
}

func TestValidateRejectsMalformedRequests(t *testing.T) {
	cases := []struct {
		name    string
		desired adapter.AgentDesiredState
		want    string
	}{
		{"model-without-provider",
			adapter.AgentDesiredState{Config: json.RawMessage(`{"model":"m"}`)}, "provider"},
		{"provider-without-model",
			adapter.AgentDesiredState{Config: json.RawMessage(`{"provider":{"endpoint":"https://e","apiKeyEnv":"K"}}`)}, "config.model is required"},
		{"empty-endpoint",
			adapter.AgentDesiredState{Config: json.RawMessage(`{"model":"m","provider":{"apiKeyEnv":"K"}}`)}, "endpoint is required"},
		{"empty-apikeyenv",
			adapter.AgentDesiredState{Config: json.RawMessage(`{"model":"m","provider":{"endpoint":"https://e"}}`)}, "apiKeyEnv is required"},
		{"bad-apikeyenv",
			adapter.AgentDesiredState{Config: json.RawMessage(`{"model":"m","provider":{"endpoint":"https://e","apiKeyEnv":"1-BAD"}}`)}, "environment variable name"},
		{"bad-config-type",
			adapter.AgentDesiredState{Config: json.RawMessage(`["not","an","object"]`)}, "not a JSON object"},
		{"dotted-mcp-name",
			adapter.AgentDesiredState{MCP: map[string]adapter.MCPEntry{"a.b": {Command: "x"}}}, "mcp entry name"},
		{"mcp-without-command",
			adapter.AgentDesiredState{MCP: map[string]adapter.MCPEntry{"t": {}}}, "requires command"},
		{"traversal-skill-dot",
			adapter.AgentDesiredState{Skills: map[string]domain.SkillDesired{".": {ContentDigest: skillDigest}}}, "skill name"},
		{"traversal-skill-dotdot",
			adapter.AgentDesiredState{Skills: map[string]domain.SkillDesired{"..": {ContentDigest: skillDigest}}}, "skill name"},
		{"absolute-skill",
			adapter.AgentDesiredState{Skills: map[string]domain.SkillDesired{"/tmp/evil": {ContentDigest: skillDigest}}}, "skill name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			c.desired.Family = ID
			err := newTestAdapter().Validate(context.Background(), home, c.desired)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestTraversalSkillNamesAreRejectedBeforeWrite(t *testing.T) {
	for _, name := range []string{".", ".."} {
		t.Run(name, func(t *testing.T) {
			home := baselineHome(t)
			before := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
			desired := fullDesired(t, home)
			delete(desired.Skills, "fleet-skill")
			desired.Skills[name] = domain.SkillDesired{ContentDigest: skillDigest}

			if err := newTestAdapter().Validate(context.Background(), home, desired); err == nil {
				t.Fatal("traversal skill name must be rejected")
			}
			// Apply 纵深防御：绕过 Validate 的陈旧计划同样被挡。
			err := newTestAdapter().Apply(context.Background(), home, desired, []adapter.Change{
				{Family: ID, Step: "skills", Key: "skill:" + name, To: skillDigest},
			})
			if err == nil {
				t.Fatal("Apply must reject traversal skill names too")
			}
			if got := readText(t, filepath.Join(home, ".hermes", "config.yaml")); got != before {
				t.Fatal("config.yaml was written")
			}
			if _, statErr := os.Lstat(filepath.Join(home, ".hermes", "skills")); !os.IsNotExist(statErr) {
				t.Fatal("traversal skill name created a path under .hermes")
			}
		})
	}
}

func TestValidateRejectsUnparseableAndVersionDrift(t *testing.T) {
	ctx := context.Background()

	t.Run("unparseable", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), "model: [unclosed\n")
		err := newTestAdapter().Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Config: json.RawMessage(`{"model":"m","provider":{"endpoint":"https://e","apiKeyEnv":"K"}}`)})
		if err == nil || !strings.Contains(err.Error(), "unparseable") {
			t.Fatalf("want unparseable error, got %v", err)
		}
	})

	t.Run("missing-config", func(t *testing.T) {
		home := t.TempDir()
		err := newTestAdapter().Validate(ctx, home, adapter.AgentDesiredState{Family: ID})
		if err == nil || !strings.Contains(err.Error(), "not initialized") {
			t.Fatalf("want not-initialized error, got %v", err)
		}
	})

	t.Run("config-version-drift", func(t *testing.T) {
		home := t.TempDir()
		drifted := strings.Replace(fixtureConfig(t), "_config_version: 44", "_config_version: 43", 1)
		mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), drifted)
		before := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
		err := newTestAdapter().Validate(ctx, home, adapter.AgentDesiredState{Family: ID, Config: json.RawMessage(`{"model":"m","provider":{"endpoint":"https://e","apiKeyEnv":"K"}}`)})
		if err == nil || !strings.Contains(err.Error(), "_config_version=43") || !strings.Contains(err.Error(), "silently degrade") {
			t.Fatalf("want explicit version-drift error, got %v", err)
		}
		if got := readText(t, filepath.Join(home, ".hermes", "config.yaml")); got != before {
			t.Fatal("version drift must not write")
		}
	})

	t.Run("missing-version-key", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), "model:\n  default: x\n")
		err := newTestAdapter().Validate(ctx, home, adapter.AgentDesiredState{Family: ID})
		if err == nil || !strings.Contains(err.Error(), "_config_version") {
			t.Fatalf("want missing-version error, got %v", err)
		}
	})
}

// ---- 3. 生命周期 ----

func TestDriftOnlyFromManagedFields(t *testing.T) {
	ctx := context.Background()
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)
	configPath := filepath.Join(home, ".hermes", "config.yaml")

	t.Run("unmanaged-changes-do-not-drift", func(t *testing.T) {
		for _, edit := range []struct{ old, new string }{
			{`theme: "dark"`, `theme: "light"`},                                  // display
			{"context_length: 131072", "context_length: 65536"},                  // model 内未托管兄弟键
			{`HERMES_WEB_UI_MANAGED_MCP: "1"`, `HERMES_WEB_UI_MANAGED_MCP: "0"`}, // 受管 mcp 条目内的未托管 env
			{"hooks_auto_accept: false", "hooks_auto_accept: true"},
		} {
			original := readText(t, configPath)
			mustWrite(t, configPath, strings.Replace(original, edit.old, edit.new, 1))
			if changes := planAfter(t, a, home, desired); len(changes) != 0 {
				t.Errorf("edit %q must not drift, got %v", edit.old, changes)
			}
			mustWrite(t, configPath, original)
		}
	})

	t.Run("managed-changes-drift", func(t *testing.T) {
		cases := []struct{ old, new, wantKey string }{
			{"default: fleet-model", "default: someone-else", "config.model"},
			{"provider: custom", "provider: auto", "config.model"},
			{"base_url: https://api.example.com/v1", "base_url: https://other.example/v1", "config.model"},
			{`api_key: ${FLEET_API_KEY}`, `api_key: "sk-live-literal"`, "config.model"},
			{"command: npx", "command: other", "mcp.fleet-tool"},
			{"- -y", "- -x", "mcp.fleet-tool"},
		}
		for _, c := range cases {
			original := readText(t, configPath)
			edited := strings.Replace(original, c.old, c.new, 1)
			if edited == original {
				t.Fatalf("fixture edit %q did not apply", c.old)
			}
			mustWrite(t, configPath, edited)
			changes := planAfter(t, a, home, desired)
			found := false
			for _, ch := range changes {
				if ch.Key == c.wantKey {
					found = true
				}
			}
			if !found {
				t.Errorf("edit %q must drift %s, got %v", c.old, c.wantKey, changes)
			}
			// 应用后收敛。
			if err := a.Apply(ctx, home, desired, changes); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if again := planAfter(t, a, home, desired); len(again) != 0 {
				t.Errorf("edit %q did not converge: %v", c.old, again)
			}
			mustWrite(t, configPath, original)
		}
	})
}

// TestMCPEntryWithoutArgsConverges 是批次一复核缺陷 1 的家族回归：省略 args 与空 args 必须同投影。
func TestMCPEntryWithoutArgsConverges(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	desired := adapter.AgentDesiredState{
		Family: ID,
		MCP:    map[string]adapter.MCPEntry{"noargs": {Command: "uvx"}},
	}
	reconcile(t, a, home, desired)
	if changes := planAfter(t, a, home, desired); len(changes) != 0 {
		t.Fatalf("args-less entry must converge, got %v", changes)
	}
	raw := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
	if !strings.Contains(raw, "noargs:") || !strings.Contains(raw, "args: []") {
		t.Fatalf("expected args: [] for args-less entry, got:\n%s", raw)
	}
}

// TestProviderChangeConvergesOnSecondApply 锁定"整节点替换"修复：换 endpoint 必须真的改掉。
func TestProviderChangeConvergesOnSecondApply(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)

	desired.Config = json.RawMessage(`{"model":"fleet-model-2","provider":{"endpoint":"https://api2.example.com/v1","apiKeyEnv":"FLEET_KEY_2"}}`)
	reconcile(t, a, home, desired)

	raw := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
	doc := decodeYAML(t, raw)["model"].(map[string]any)
	for k, want := range map[string]any{
		"default": "fleet-model-2", "base_url": "https://api2.example.com/v1", "api_key": "${FLEET_KEY_2}",
	} {
		if !kit.ValuesEqual(want, doc[k]) {
			t.Errorf("model.%s = %v, want %v", k, doc[k], want)
		}
	}
}

func TestApplyVersionMismatchFailsExplicitly(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter() // 桩报告 0.21.2
	desired := fullDesired(t, home)
	desired.Version = "0.22.0"
	err := a.Apply(context.Background(), home, desired, []adapter.Change{
		{Family: ID, Step: "version", Key: "version", From: repoVersion, To: "0.22.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "command installer") {
		t.Fatalf("version mismatch must fail explicitly, got %v", err)
	}
}

// ---- 4. 恢复与一致性 ----

func TestManagedFilesExtractAndMergeRollback(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	configPath := filepath.Join(home, ".hermes", "config.yaml")

	files, err := a.ManagedFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != configRel {
		t.Fatalf("ManagedFiles = %v, want [%s]", files, configRel)
	}

	// ExtractManaged 只取受管 model 叶子键；未托管键与密钥面不进备份。
	before := readText(t, configPath)
	managed, err := a.ExtractManaged([]byte(before))
	if err != nil {
		t.Fatal(err)
	}
	wantManaged := map[string]any{"model.default": "deepseek-flash", "model.provider": "deepseek"}
	if !kit.ValuesEqual(wantManaged, managed) {
		t.Fatalf("ExtractManaged = %v, want %v", managed, wantManaged)
	}
	for k := range managed {
		if strings.Contains(k, "delegation") || strings.Contains(k, "auxiliary") || strings.Contains(k, "secrets") {
			t.Fatalf("secret-adjacent key %q went into the backup", k)
		}
	}

	// 应用受管改动，然后按受管键回退：受管键还原，未托管键与密钥面保留。
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)
	if err := a.MergeManaged(home, configRel, managed); err != nil {
		t.Fatalf("MergeManaged: %v", err)
	}
	restored := readText(t, configPath)
	doc := decodeYAML(t, restored)
	if got := doc["model"].(map[string]any)["default"]; got != "deepseek-flash" {
		t.Fatalf("managed rollback: model.default = %v, want deepseek-flash", got)
	}
	if !strings.Contains(restored, "${DELEGATION_KEY}") || !strings.Contains(restored, "context_length: 131072") {
		t.Fatal("managed rollback dropped unmanaged content")
	}
	if !strings.Contains(restored, "# Managed: default model id.") {
		t.Fatal("managed rollback dropped comments")
	}

	// 未知受管键显式失败（不静默跳过）。
	if err := a.MergeManaged(home, configRel, map[string]any{"bogus.key": 1}); err == nil {
		t.Fatal("unknown managed key must fail explicitly")
	}
	if err := a.MergeManaged(home, ".hermes/SOUL.md", managed); err == nil {
		t.Fatal("non-managed file must fail explicitly")
	}
}

// TestExtractManagedNeverPersistsLiteralSecret 锁定密钥边界：字面量 api_key 不进 manifest。
func TestExtractManagedNeverPersistsLiteralSecret(t *testing.T) {
	a := newTestAdapter()
	content := "model:\n  default: m\n  provider: custom\n  api_key: \"sk-live-deadbeef\"\n"
	managed, err := a.ExtractManaged([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := managed["model.api_key"]; ok {
		t.Fatalf("literal api_key leaked into the backup: %v", managed)
	}
	// `${VAR}` 间接引用则保留（回退能力不丢）。
	content = "model:\n  default: m\n  provider: custom\n  api_key: \"${SAFE_KEY}\"\n"
	managed, err = a.ExtractManaged([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if managed["model.api_key"] != "${SAFE_KEY}" {
		t.Fatalf("env ref api_key should be restored, got %v", managed)
	}
}

func TestSkillLinksAreSymlinkSetAndIdempotent(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)

	link := filepath.Join(home, ".hermes", "skills", "fleet-skill")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("skill link: %v", err)
	}
	if target != kit.SkillCacheDir(home, "fleet-skill", skillDigest) {
		t.Fatalf("skill link target = %s", target)
	}
	if changes := planAfter(t, a, home, desired); len(changes) != 0 {
		t.Fatalf("skill reconcile must be idempotent, got %v", changes)
	}
}

func TestSkillLinkRequiresMaterializedArtifact(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	desired := adapter.AgentDesiredState{
		Family: ID,
		Skills: map[string]domain.SkillDesired{"not-materialized": {ContentDigest: skillDigest}},
	}
	err := a.Apply(context.Background(), home, desired, []adapter.Change{
		{Family: ID, Step: "skills", Key: "skill:not-materialized", To: skillDigest},
	})
	if err == nil || !strings.Contains(err.Error(), "not materialized") {
		t.Fatalf("missing artifact must fail explicitly, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(home, ".hermes", "skills", "not-materialized")); !os.IsNotExist(statErr) {
		t.Fatal("a broken link was created")
	}
}

// TestConfigWritePreservesSymlink：config.yaml 是软链时必须写穿（护栏 #3）。
func TestConfigWritePreservesSymlink(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"), fixtureConfig(t))
	// 真实目标在 home 外，链接在 home 内。
	real := filepath.Join(home, "shared", "hermes-config.yaml")
	mustWrite(t, real, fixtureConfig(t))
	link := filepath.Join(home, ".hermes", "config.yaml")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	a := newTestAdapter()
	reconcile(t, a, home, fullDesired(t, home))

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.yaml symlink was replaced by a regular file")
	}
	if !strings.Contains(readText(t, real), "fleet-model") {
		t.Fatal("managed write did not go through the symlink")
	}
}

// ---- 5. 健康检查 ----

func TestHealthCheck(t *testing.T) {
	ctx := context.Background()
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)

	if err := a.HealthCheck(ctx, home, desired); err != nil {
		t.Fatalf("healthy config rejected: %v", err)
	}

	t.Run("config-check-error", func(t *testing.T) {
		bad := newTestAdapter()
		bad.ConfigCheck = func(_ context.Context, _ string) (string, error) {
			return "Required: missing DEEPSEEK_API_KEY", errors.New("exit status 1")
		}
		if err := bad.HealthCheck(ctx, home, desired); err == nil || !strings.Contains(err.Error(), "config check") {
			t.Fatalf("failing config check must fail health, got %v", err)
		}
	})

	t.Run("config-check-version-mismatch", func(t *testing.T) {
		bad := newTestAdapter()
		bad.ConfigCheck = testConfigCheck(43)
		if err := bad.HealthCheck(ctx, home, desired); err == nil || !strings.Contains(err.Error(), "config version 43") {
			t.Fatalf("version mismatch must fail health, got %v", err)
		}
	})

	t.Run("missing-managed-key", func(t *testing.T) {
		broken := t.TempDir()
		mustWrite(t, filepath.Join(broken, ".hermes", "config.yaml"), strings.Replace(fixtureConfig(t), `  provider: "deepseek"`, "", 1))
		if err := a.HealthCheck(ctx, broken, desired); err == nil || !strings.Contains(err.Error(), "managed model") {
			t.Fatalf("missing managed key must fail health, got %v", err)
		}
	})

	t.Run("version-mismatch", func(t *testing.T) {
		bad := newTestAdapter()
		bad.Probe = recordProbe("0.20.0", nil)
		if err := bad.HealthCheck(ctx, home, desired); err == nil || !strings.Contains(err.Error(), "installed version") {
			t.Fatalf("version mismatch must fail health, got %v", err)
		}
	})

	t.Run("config-version-drift", func(t *testing.T) {
		drifted := t.TempDir()
		mustWrite(t, filepath.Join(drifted, ".hermes", "config.yaml"),
			strings.Replace(fixtureConfig(t), "_config_version: 44", "_config_version: 45", 1))
		if err := a.HealthCheck(ctx, drifted, desired); err == nil || !strings.Contains(err.Error(), "_config_version=45") {
			t.Fatalf("config version drift must fail health, got %v", err)
		}
	})
}

// TestPatchGoldenPreservesCommentsAndOrder：YAML Node 合并的小黄金文件（注释/键序保留、
// 缩进 2、空行规整），锁定编码行为。
func TestPatchGoldenPreservesCommentsAndOrder(t *testing.T) {
	in := "# fixture top\nmodel:\n  # managed default\n  default: \"old-model\"  # inline\n  provider: \"auto\"\nunmanaged:\n  keep: true\nmcp_servers:\n  existing:\n    command: \"uvx\"\n    env:\n      TOKEN: \"${TOKEN}\"\n"
	want := "# fixture top\nmodel:\n  # managed default\n  default: new-model # inline\n  provider: custom\n  base_url: https://api.example.com/v1\nunmanaged:\n  keep: true\nmcp_servers:\n  existing:\n    command: node\n    env:\n      TOKEN: \"${TOKEN}\"\n    args:\n      - -y\n      - pkg\n"
	got, err := kit.PatchYAML(in, map[string]any{
		"model.default":                "new-model",
		"model.provider":               "custom",
		"model.base_url":               "https://api.example.com/v1",
		"mcp_servers.existing.command": "node",
		"mcp_servers.existing.args":    []string{"-y", "pkg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("patch golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRealHermesBinaryConfigCheck 是本机实测补证（默认跳过）：把受管合并写落到临时
// HERMES_HOME，再用真实 `hermes --version` + `hermes config check` 验证产物被 Hermes
// 接受。`HERMES_REAL=1` 且 PATH 上有 hermes 时执行；绝不触碰真实 ~/.hermes。
func TestRealHermesBinaryConfigCheck(t *testing.T) {
	if os.Getenv("HERMES_REAL") != "1" {
		t.Skip("set HERMES_REAL=1 to run against the installed hermes binary")
	}
	if _, err := exec.LookPath(binaryName); err != nil {
		t.Skipf("hermes binary not on PATH: %v", err)
	}
	home := baselineHome(t)
	materializeSkill(t, home, "fleet-skill", skillDigest)
	desired := fullDesired(t, home)
	reconcile(t, newTestAdapter(), home, desired)
	if err := (&Adapter{}).HealthCheck(context.Background(), home, desired); err != nil {
		t.Fatalf("real hermes rejected the merged config: %v", err)
	}
}

// ---- 复核轮（B1/B2/B3 + MINOR）回归 ----

// TestValidateRejectsMalformedDigest（B1）：digest 形状不符在任何写入之前被拒。
func TestValidateRejectsMalformedDigest(t *testing.T) {
	home := baselineHome(t)
	for _, digest := range []string{"../../../../../..", "sha256:not-a-digest/../../../..", "sha256:cc", ""} {
		desired := adapter.AgentDesiredState{Family: ID,
			Skills: map[string]domain.SkillDesired{"ok": {ContentDigest: digest}}}
		err := newTestAdapter().Validate(context.Background(), home, desired)
		if err == nil || !strings.Contains(err.Error(), "contentDigest") {
			t.Fatalf("digest %q must be rejected, got %v", digest, err)
		}
	}
}

// TestApplyPreflightRejectsBeforeAnyWrite（B1 + NIT）：绕过 Validate 的陈旧计划也不得发生
// 部分写入——坏 digest / envRefs / `_config_version` 漂移 / 点号 MCP 名都在写 config 之前被拒。
func TestApplyPreflightRejectsBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()

	t.Run("bad-skill-digest", func(t *testing.T) {
		home := baselineHome(t)
		a, desired := newTestAdapter(), fullDesired(t, home)
		before := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
		err := a.Apply(ctx, home, desired, []adapter.Change{
			{Family: ID, Step: "config", Key: keyModel},
			{Family: ID, Step: "skills", Key: "skill:ok", To: "../../../../.."},
		})
		if err == nil {
			t.Fatal("bad digest must be rejected")
		}
		if got := readText(t, filepath.Join(home, ".hermes", "config.yaml")); got != before {
			t.Fatal("config was written before the skill digest was checked")
		}
	})

	t.Run("envRefs", func(t *testing.T) {
		home := baselineHome(t)
		a, desired := newTestAdapter(), fullDesired(t, home)
		entry := desired.MCP["fleet-tool"]
		entry.EnvRefs = map[string]string{"TOKEN": "FLEET_TOKEN"}
		desired.MCP["fleet-tool"] = entry
		before := readText(t, filepath.Join(home, ".hermes", "config.yaml"))
		err := a.Apply(ctx, home, desired, []adapter.Change{
			{Family: ID, Step: "mcp", Key: "mcp.fleet-tool"},
		})
		var ve *adapter.ValidateError
		if !errors.As(err, &ve) {
			t.Fatalf("envRefs must be rejected in Apply pre-flight, got %v", err)
		}
		if got := readText(t, filepath.Join(home, ".hermes", "config.yaml")); got != before {
			t.Fatal("config was written before envRefs was checked")
		}
	})

	t.Run("config-version-drift", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, filepath.Join(home, ".hermes", "config.yaml"),
			strings.Replace(fixtureConfig(t), "_config_version: 44", "_config_version: 45", 1))
		a := newTestAdapter()
		err := a.Apply(ctx, home, adapter.AgentDesiredState{Family: ID}, []adapter.Change{
			{Family: ID, Step: "config", Key: keyModel},
		})
		if err == nil || !strings.Contains(err.Error(), "_config_version=45") {
			t.Fatalf("stale plan on drifted config must be rejected, got %v", err)
		}
	})

	t.Run("dotted-mcp-name", func(t *testing.T) {
		home := baselineHome(t)
		a := newTestAdapter()
		err := a.Apply(ctx, home, adapter.AgentDesiredState{Family: ID},
			[]adapter.Change{{Family: ID, Step: "mcp", Key: "mcp.a.b"}})
		if err == nil || !strings.Contains(err.Error(), "mcp entry name") {
			t.Fatalf("dotted mcp name must be rejected by Apply pre-flight, got %v", err)
		}
	})
}

// TestDefaultVersionProbePinsHome（B2）：默认探测必须把 HOME/HERMES_HOME 都钉到注入根，
// 否则 `hermes --version` 会读另一个 ~/.hermes 并在真实 home 里初始化骨架。
func TestDefaultVersionProbePinsHome(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "probe.env")
	script := filepath.Join(dir, binaryName)
	mustWrite(t, script, "#!/bin/sh\nprintf '%s\\n%s\\n' \"$HOME\" \"$HERMES_HOME\" > \"$PROBE_OUT\"\n"+
		"printf 'Hermes Agent v0.21.2 (2026.9.11) · upstream be2f7e9c\\n'\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PROBE_OUT", out)

	home := t.TempDir()
	d, err := (&Adapter{}).Detect(context.Background(), home)
	if err != nil {
		t.Fatalf("Detect with default probe: %v", err)
	}
	if !d.Installed || d.Version != repoVersion {
		t.Fatalf("got installed=%v version=%q", d.Installed, d.Version)
	}
	got := strings.Split(strings.TrimSpace(readText(t, out)), "\n")
	if len(got) != 2 || got[0] != home || got[1] != configRoot(home) {
		t.Fatalf("probe env = %v, want [%s %s]", got, home, configRoot(home))
	}
}

// managedScope 建一个临时 managed-scope 目录（注入 ManagedDir，绝不读真实 /etc/hermes）。
func managedScope(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		mustWrite(t, filepath.Join(dir, "config.yaml"), body)
	}
	return dir
}

// TestManagedScopeOverrideIsDistinguishable（B3）：managed scope 压过用户层时，
// 观测投影带 overriddenBy + 生效值、Plan 整片空、HealthCheck 返回可区分失败、Apply 拒写。
func TestManagedScopeOverrideIsDistinguishable(t *testing.T) {
	ctx := context.Background()
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)
	configPath := filepath.Join(home, ".hermes", "config.yaml")
	before := readText(t, configPath)

	a.ManagedDir = managedScope(t, "model:\n  default: managed-model\n  base_url: https://managed.example/v1\n")

	inv, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	markers, _ := inv.ManagedProjection[overrideMarkerKey].(map[string]any)
	if _, ok := markers[keyModel]; !ok {
		t.Fatalf("config.model must be marked overridden, got %v", inv.ManagedProjection)
	}
	model, _ := inv.ManagedProjection["model"].(map[string]any)
	if model["default"] != "managed-model" || model["base_url"] != "https://managed.example/v1" {
		t.Fatalf("observed model must carry the effective managed values, got %v", model)
	}

	changes, err := a.Plan(ctx, home, desired, inv)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("override must yield an empty plan, got %v", changes)
	}

	var oe *HigherLayerOverrideError
	if err := a.HealthCheck(ctx, home, desired); !errors.As(err, &oe) || oe.Key != keyModel {
		t.Fatalf("want *HigherLayerOverrideError(config.model), got %v", err)
	}
	if !strings.Contains(oe.Layer, "managed scope") {
		t.Fatalf("override layer must name managed scope, got %q", oe.Layer)
	}

	// 陈旧计划（含受管键变更）同样拒写。
	err = a.Apply(ctx, home, desired, []adapter.Change{{Family: ID, Step: "config", Key: keyModel}})
	if !errors.As(err, &oe) {
		t.Fatalf("stale plan under override must be refused, got %v", err)
	}
	if got := readText(t, configPath); got != before {
		t.Fatal("wrote to the user layer while a managed scope pins the key")
	}
}

// TestManagedScopeSameValueAndAbsenceAreNotOverrides（B3 反向对照）：
// 同值不算覆盖；空文件不算覆盖；无 managed 目录不算覆盖。
func TestManagedScopeSameValueAndAbsenceAreNotOverrides(t *testing.T) {
	ctx := context.Background()
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)

	t.Run("same-value", func(t *testing.T) {
		a.ManagedDir = managedScope(t, "model:\n  default: fleet-model\n  provider: custom\n"+
			"  base_url: https://api.example.com/v1\n  api_key: \"${FLEET_API_KEY}\"\n")
		inv, err := a.Inventory(ctx, home, desired)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := inv.ManagedProjection[overrideMarkerKey]; ok {
			t.Fatalf("same-value managed config must not count as an override: %v", inv.ManagedProjection)
		}
	})

	t.Run("empty-managed-file", func(t *testing.T) {
		a.ManagedDir = managedScope(t, "")
		inv, err := a.Inventory(ctx, home, desired)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := inv.ManagedProjection[overrideMarkerKey]; ok {
			t.Fatalf("absent managed config must not count as an override: %v", inv.ManagedProjection)
		}
	})

	t.Run("no-managed-dir", func(t *testing.T) {
		a.ManagedDir = filepath.Join(t.TempDir(), "does-not-exist")
		inv, err := a.Inventory(ctx, home, desired)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := inv.ManagedProjection[overrideMarkerKey]; ok {
			t.Fatal("missing managed dir must not count as an override")
		}
	})
}

// TestManagedScopeUnparseableFailsExplicitly（B3）：managed 文件不可解析时显式失败，不猜生效值。
func TestManagedScopeUnparseableFailsExplicitly(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	a.ManagedDir = managedScope(t, "model: [unclosed\n")
	if _, err := a.Inventory(context.Background(), home, fullDesired(t, home)); err == nil ||
		!strings.Contains(err.Error(), "managed scope") {
		t.Fatalf("unparseable managed scope must fail explicitly, got %v", err)
	}
}

// TestHealthCheckComparesMCPArgsAndRequiresVersionLine（MINOR）：
// MCP args 也算受管健康面；`config check` 输出没有 `Config version:` 行不再静默通过。
func TestHealthCheckComparesMCPArgsAndRequiresVersionLine(t *testing.T) {
	ctx := context.Background()
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)

	// 改掉受管 MCP 的 args → HealthCheck 必须失败。
	configPath := filepath.Join(home, ".hermes", "config.yaml")
	original := readText(t, configPath)
	mustWrite(t, configPath, strings.Replace(original, "- -y", "- -x", 1))
	if err := a.HealthCheck(ctx, home, desired); err == nil || !strings.Contains(err.Error(), "mcp server") {
		t.Fatalf("tampered mcp args must fail health, got %v", err)
	}
	mustWrite(t, configPath, original)

	// `config check` 输出缺少版本行 → 显式失败。
	a.ConfigCheck = func(_ context.Context, _ string) (string, error) { return "no version here\n", nil }
	if err := a.HealthCheck(ctx, home, desired); err == nil || !strings.Contains(err.Error(), "Config version") {
		t.Fatalf("unversioned config check output must fail health, got %v", err)
	}
}
