package dsh

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/kit"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 本文件是 DeepSeek Harness 家族的 fixture（矩阵逐家族验收 5 项：身份与兼容性 /
// 配置与所有权 / 生命周期 / 恢复与一致性 / 验收记录）。全部以 t.TempDir() 为 HOME 根，
// 绝不触碰真实 ~/.dsh；Probe 一律注入（护栏 #12）。证据来源见
// docs/adapters-batch-2.md §2 与 §11。两条前置复验（settings.yaml 写入面、版本复验）
// 的结论落在 §11.2：modelProvider = unverified、mcp = unsupported。

const (
	repoVersion = "0.1.7-rc.1"
	skillDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rulesBody   = "Fleet managed rule content."
)

// ---- 桩与辅助 ----

// recordProbe 返回版本桩并记录每次调用的注入 home（默认探测必须钉 HOME/DSH_HOME）。
func recordProbe(version string, calls *[]string) VersionProbe {
	return func(_ context.Context, home string) (string, error) {
		if calls != nil {
			*calls = append(*calls, home)
		}
		return version + "\n", nil
	}
}

func probeMissing() VersionProbe {
	return func(_ context.Context, _ string) (string, error) {
		return "", &exec.Error{Name: binaryName, Err: exec.ErrNotFound}
	}
}

func probeGarbage() VersionProbe {
	return func(_ context.Context, _ string) (string, error) {
		return "dsh: some banner that is not a version\n", nil
	}
}

func newTestAdapter() *Adapter { return &Adapter{Probe: recordProbe(repoVersion, nil)} }

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

func fixtureText(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fixtureAGENTS(t *testing.T) string { return fixtureText(t, "AGENTS.md") }

func baselineHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".dsh", "AGENTS.md"), fixtureAGENTS(t))
	mustWrite(t, filepath.Join(home, ".dsh", "skills", "existing.md"), fixtureText(t, "skills/existing.md"))
	mustWrite(t, filepath.Join(home, ".dsh", "settings.yaml"), fixtureText(t, "settings.yaml"))
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
		Rules:   map[string]adapter.RulesEntry{"global": {Content: rulesBody}},
		Skills:  map[string]domain.SkillDesired{"fleet-skill": {ContentDigest: skillDigest}},
	}
}

func modelProviderDesired() adapter.AgentDesiredState {
	return adapter.AgentDesiredState{Family: ID,
		Config: json.RawMessage(`{"model":"deepseek-flash","provider":{"endpoint":"https://api.example.com/v1","apiKeyEnv":"FLEET_API_KEY"}}`)}
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

func agentsPath(home string) string { return filepath.Join(home, ".dsh", "AGENTS.md") }

func skillPath(home, name string) string {
	return filepath.Join(home, ".dsh", "skills", name+".md")
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
		if d.Family != ID || filepath.Base(d.ConfigPath) != ".dsh" {
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
		{"ok", "0.1.7-rc.1\n", "0.1.7-rc.1"},
		{"ok-other-prerelease", "0.1.5-rc.3\n", "0.1.5-rc.3"},
		{"ok-stable", "0.2.0\n", "0.2.0"},
		{"ok-build-metadata", "0.2.0+build.5\n", "0.2.0+build.5"},
		{"leading-blank", "\n0.1.7-rc.1\n", "0.1.7-rc.1"},
		{"prefixed-v", "v0.1.7-rc.1\n", ""},
		{"prefixed-name", "dsh/0.1.7-rc.1\n", ""},
		{"two-segments", "0.1\n", ""},
		{"trailing-text", "0.1.7-rc.1 extra\n", ""},
		{"npm-error", "npm error code EROFS\n", ""},
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
	byCap := map[adapter.Capability]adapter.CapabilityDecl{}
	for _, d := range newTestAdapter().Capabilities() {
		byCap[d.Capability] = d
	}
	for _, c := range []adapter.Capability{adapter.CapabilityVersion, adapter.CapabilitySkills, adapter.CapabilityRules} {
		if got := byCap[c].State; got != adapter.SupportSupported {
			t.Errorf("%s state = %q, want supported", c, got)
		}
	}
	if got := byCap[adapter.CapabilityModelProvider].State; got != adapter.SupportUnverified {
		t.Errorf("modelProvider state = %q, want unverified (settings.yaml is a legacy import channel; §11.2)", got)
	}
	if got := byCap[adapter.CapabilityMCP].State; got != adapter.SupportUnsupported {
		t.Errorf("mcp state = %q, want unsupported (slice (c): no patch-row insert; §2.2)", got)
	}
	if got := byCap[adapter.CapabilityVersion].VerifiedVersions; !strings.Contains(got, repoVersion) {
		t.Errorf("version VerifiedVersions = %q, want the installed %q", got, repoVersion)
	}
	// §2.5 未验证项必须落进 Reason/Evidence，而不是被静默省略。
	checks := map[adapter.Capability][]string{
		adapter.CapabilityVersion:       {"0.1.7-rc.1", "developer preview", "COMPATIBILITY-BREAKING", "WSL", "Windows", "安装/升级"},
		adapter.CapabilityModelProvider: {"settings.yaml", ".imported", "cordis.patch.yml", "llm-pi-ai", "deferred", "unverified"},
		adapter.CapabilityMCP:           {"patch", "行级", "env:VAR", "deferred"},
		adapter.CapabilitySkills:        {"扁平", "SKILL.md", "customSkillDirs"},
		adapter.CapabilityRules:         {"AGENTS.md", "dsh-agent-instructions"},
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
	fixture := fixtureAGENTS(t)

	reconcile(t, a, home, desired)

	raw := readText(t, agentsPath(home))
	// 受管块追加在文件末尾：块外（用户）内容逐字节保留。
	if !strings.HasPrefix(raw, fixture) {
		t.Fatalf("unmanaged AGENTS.md content was rewritten:\n--- raw ---\n%s", raw)
	}
	if !strings.Contains(raw, kit.BlockBegin) || !strings.Contains(raw, kit.BlockEnd) {
		t.Fatalf("managed block markers missing:\n%s", raw)
	}
	if !strings.Contains(raw, rulesBody) {
		t.Fatalf("managed rules body not written:\n%s", raw)
	}
	for _, kept := range []string{
		"<!-- unmanaged user note: this comment and everything outside the managed block must survive -->",
		"## House rules",
		"Unmanaged trailing paragraph",
	} {
		if !strings.Contains(raw, kept) {
			t.Errorf("unmanaged AGENTS.md content %q was lost", kept)
		}
	}

	// 用户既有的扁平 skill 文件不被触碰（未点名即不受管）。
	existing := readText(t, skillPath(home, "existing"))
	if existing != fixtureText(t, "skills/existing.md") {
		t.Fatal("un-named flat skill file was modified")
	}
	if info, err := os.Lstat(skillPath(home, "existing")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("un-named flat skill file must stay a regular file, got %v/%v", info, err)
	}

	// Fleet 自有 skill 软链到位。
	if got := kit.ReadSkillDigest(skillPath(home, "fleet-skill")); got != skillDigest {
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
	if again := readText(t, agentsPath(home)); again != raw {
		t.Fatal("a no-change reconcile rewrote AGENTS.md")
	}
}

// TestSettingsYamlIsNotAManagedSurface 把 §11.2 的前置复验结论编码成回归：
// `$DSH_HOME/settings.yaml` 是 legacy 一次性导入通道，绝不被本适配器读写成受管面。
func TestSettingsYamlIsNotAManagedSurface(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	settingsPath := filepath.Join(home, ".dsh", "settings.yaml")
	before := readText(t, settingsPath)

	files, err := a.ManagedFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != rulesRel {
		t.Fatalf("ManagedFiles = %v, want [%s] (settings.yaml must not be managed)", files, rulesRel)
	}

	reconcile(t, a, home, fullDesired(t, home))
	if got := readText(t, settingsPath); got != before {
		t.Fatalf("settings.yaml was modified by a reconcile:\n%s", got)
	}
}

func TestValidateRejectsUnverifiedBeforeWrite(t *testing.T) {
	home := baselineHome(t)
	agentsBefore := readText(t, agentsPath(home))

	t.Run("mcp-unsupported", func(t *testing.T) {
		desired := fullDesired(t, home)
		desired.MCP = map[string]adapter.MCPEntry{"fleet-tool": {Command: "npx", Args: []string{"-y", "@fleet/tool"}}}
		err := newTestAdapter().Validate(context.Background(), home, desired)
		if err == nil {
			t.Fatal("mcp must be rejected before any write")
		}
		if !strings.Contains(err.Error(), `capability "mcp" is unsupported`) {
			t.Fatalf("want an explicit mcp/unsupported rejection, got %v", err)
		}
	})

	t.Run("model-provider-unverified", func(t *testing.T) {
		err := newTestAdapter().Validate(context.Background(), home, modelProviderDesired())
		if err == nil {
			t.Fatal("modelProvider must be rejected before any write")
		}
		if !strings.Contains(err.Error(), `capability "modelProvider" is unverified`) {
			t.Fatalf("want an explicit modelProvider/unverified rejection, got %v", err)
		}
		// 拒绝原因点名 settings.yaml 的 legacy 结论，便于操作者理解。
		if !strings.Contains(err.Error(), "settings.yaml") {
			t.Fatalf("rejection must explain the settings.yaml finding: %v", err)
		}
	})

	if got := readText(t, agentsPath(home)); got != agentsBefore {
		t.Fatal("Validate wrote to AGENTS.md")
	}
	if _, statErr := os.Lstat(skillPath(home, "fleet-skill")); !os.IsNotExist(statErr) {
		t.Fatal("Validate created a skill link")
	}
}

func TestValidateRejectsMalformedRequests(t *testing.T) {
	cases := []struct {
		name    string
		desired adapter.AgentDesiredState
		want    string
	}{
		{"model-only", adapter.AgentDesiredState{Config: json.RawMessage(`{"model":"m"}`)}, "modelProvider"},
		{"provider-only", adapter.AgentDesiredState{Config: json.RawMessage(`{"provider":{"endpoint":"https://e","apiKeyEnv":"K"}}`)}, "modelProvider"},
		{"bad-config-type", adapter.AgentDesiredState{Config: json.RawMessage(`["not","an","object"]`)}, "not a JSON object"},
		{"traversal-skill-dot", adapter.AgentDesiredState{Skills: map[string]domain.SkillDesired{".": {ContentDigest: skillDigest}}}, "skill name"},
		{"traversal-skill-dotdot", adapter.AgentDesiredState{Skills: map[string]domain.SkillDesired{"..": {ContentDigest: skillDigest}}}, "skill name"},
		{"absolute-skill", adapter.AgentDesiredState{Skills: map[string]domain.SkillDesired{"/tmp/evil": {ContentDigest: skillDigest}}}, "skill name"},
		{"bad-digest", adapter.AgentDesiredState{Skills: map[string]domain.SkillDesired{"ok": {ContentDigest: "../../../.."}}}, "contentDigest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.desired.Family = ID
			err := newTestAdapter().Validate(context.Background(), t.TempDir(), c.desired)
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
			before := readText(t, agentsPath(home))
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
			if got := readText(t, agentsPath(home)); got != before {
				t.Fatal("AGENTS.md was written")
			}
		})
	}
}

// ---- 3. 生命周期 ----

func TestDriftOnlyFromManagedFields(t *testing.T) {
	ctx := context.Background()
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)

	t.Run("unmanaged-changes-do-not-drift", func(t *testing.T) {
		original := readText(t, agentsPath(home))
		for _, edit := range []struct{ old, new string }{
			{"Never print secrets.", "Always print secrets."},
			{"<!-- unmanaged user note: this comment and everything outside the managed block must survive -->",
				"<!-- edited unmanaged note -->"},
			{"## House rules", "## House rules (edited by the user)"},
		} {
			mustWrite(t, agentsPath(home), strings.Replace(original, edit.old, edit.new, 1))
			if changes := planAfter(t, a, home, desired); len(changes) != 0 {
				t.Errorf("edit %q must not drift, got %v", edit.old, changes)
			}
			mustWrite(t, agentsPath(home), original)
		}
		// 未被期望点名的扁平 skill 文件改动也不漂移。
		mustWrite(t, skillPath(home, "existing"), "rewritten by the user\n")
		if changes := planAfter(t, a, home, desired); len(changes) != 0 {
			t.Errorf("un-named skill edit must not drift, got %v", changes)
		}
	})

	t.Run("managed-rules-change-drifts", func(t *testing.T) {
		original := readText(t, agentsPath(home))
		mustWrite(t, agentsPath(home), strings.Replace(original, rulesBody, "tampered rules", 1))
		changes := planAfter(t, a, home, desired)
		found := false
		for _, ch := range changes {
			if ch.Key == keyRules {
				found = true
			}
		}
		if !found {
			t.Fatalf("tampered managed rules must drift %s, got %v", keyRules, changes)
		}
		if err := a.Apply(ctx, home, desired, changes); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if again := planAfter(t, a, home, desired); len(again) != 0 {
			t.Errorf("managed rules did not converge: %v", again)
		}
	})

	t.Run("managed-skill-change-drifts", func(t *testing.T) {
		link := skillPath(home, "fleet-skill")
		other := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		materializeSkill(t, home, "fleet-skill", other)
		if err := os.Symlink(kit.SkillCacheDir(home, "fleet-skill", other), link); err != nil {
			t.Fatal(err)
		}
		changes := planAfter(t, a, home, desired)
		if len(changes) == 0 || changes[0].Key != "skill:fleet-skill" {
			t.Fatalf("skill digest drift must be planned, got %v", changes)
		}
		if err := a.Apply(ctx, home, desired, changes); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if again := planAfter(t, a, home, desired); len(again) != 0 {
			t.Errorf("skill did not converge: %v", again)
		}
	})
}

func TestApplyVersionMismatchFailsExplicitly(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter() // 桩报告 0.1.7-rc.1
	desired := fullDesired(t, home)
	desired.Version = "0.2.0"
	err := a.Apply(context.Background(), home, desired, []adapter.Change{
		{Family: ID, Step: "version", Key: "version", From: repoVersion, To: "0.2.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "CLI installer") {
		t.Fatalf("version mismatch must fail explicitly, got %v", err)
	}
}

// ---- 4. 恢复与一致性 ----

func TestManagedFilesExtractAndMergeRollback(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()

	files, err := a.ManagedFiles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != rulesRel {
		t.Fatalf("ManagedFiles = %v, want [%s]", files, rulesRel)
	}

	// 无受管块时提取为空（用户文件不进备份）。
	empty, err := a.ExtractManaged([]byte(fixtureAGENTS(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("AGENTS.md without a managed block must extract nothing, got %v", empty)
	}

	// 应用受管改动后提取受管块，再按块回退：块外内容保留。
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)
	managed, err := a.ExtractManaged([]byte(readText(t, agentsPath(home))))
	if err != nil {
		t.Fatal(err)
	}
	if managed["agentFleetRules"] != rulesBody {
		t.Fatalf("ExtractManaged = %v, want agentFleetRules=%q", managed, rulesBody)
	}
	// 人为改动受管块，再回退到备份值。
	tampered := strings.Replace(readText(t, agentsPath(home)), rulesBody, "tampered rules", 1)
	mustWrite(t, agentsPath(home), tampered)
	if err := a.MergeManaged(home, rulesRel, managed); err != nil {
		t.Fatalf("MergeManaged: %v", err)
	}
	restored := readText(t, agentsPath(home))
	if !strings.Contains(restored, rulesBody) || strings.Contains(restored, "tampered rules") {
		t.Fatalf("managed rollback did not restore the block:\n%s", restored)
	}
	if !strings.HasPrefix(restored, fixtureAGENTS(t)) {
		t.Fatal("managed rollback dropped unmanaged AGENTS.md content")
	}

	// 非受管文件显式失败（不静默跳过）。
	if err := a.MergeManaged(home, ".dsh/settings.yaml", managed); err == nil {
		t.Fatal("non-managed file must fail explicitly")
	}
}

func TestSkillLinksAreSymlinkSetAndIdempotent(t *testing.T) {
	home := baselineHome(t)
	a := newTestAdapter()
	desired := fullDesired(t, home)
	reconcile(t, a, home, desired)

	link := skillPath(home, "fleet-skill")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("skill link: %v", err)
	}
	if target != kit.SkillCacheDir(home, "fleet-skill", skillDigest) {
		t.Fatalf("skill link target = %s", target)
	}
	if !strings.HasSuffix(link, "fleet-skill.md") {
		t.Fatalf("dsh skills are flat `*.md`: link path = %s", link)
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
	if _, statErr := os.Lstat(skillPath(home, "not-materialized")); !os.IsNotExist(statErr) {
		t.Fatal("a broken link was created")
	}
}

// TestManagedBlockWritePreservesSymlink：AGENTS.md 是软链时必须写穿（护栏 #3）。
func TestManagedBlockWritePreservesSymlink(t *testing.T) {
	home := t.TempDir()
	link := agentsPath(home)
	real := filepath.Join(home, "shared", "agents-global.md")
	mustWrite(t, real, fixtureAGENTS(t))
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
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
		t.Fatal("AGENTS.md symlink was replaced by a regular file")
	}
	if !strings.Contains(readText(t, real), rulesBody) {
		t.Fatal("managed block write did not go through the symlink")
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
		t.Fatalf("healthy home rejected: %v", err)
	}

	t.Run("version-mismatch", func(t *testing.T) {
		bad := newTestAdapter()
		bad.Probe = recordProbe("0.1.5-rc.3", nil)
		if err := bad.HealthCheck(ctx, home, desired); err == nil || !strings.Contains(err.Error(), "installed version") {
			t.Fatalf("version mismatch must fail health, got %v", err)
		}
	})

	t.Run("rules-block-missing", func(t *testing.T) {
		broken := t.TempDir()
		mustWrite(t, agentsPath(broken), fixtureAGENTS(t))
		materializeSkill(t, broken, "fleet-skill", skillDigest)
		if err := a.HealthCheck(ctx, broken, desired); err == nil || !strings.Contains(err.Error(), "rules managed block") {
			t.Fatalf("missing managed block must fail health, got %v", err)
		}
	})

	t.Run("rules-block-tampered", func(t *testing.T) {
		broken := t.TempDir()
		mustWrite(t, agentsPath(broken), kit.RenderManagedBlock("someone else's rules"))
		materializeSkill(t, broken, "fleet-skill", skillDigest)
		if err := a.HealthCheck(ctx, broken, desired); err == nil || !strings.Contains(err.Error(), "rules managed block") {
			t.Fatalf("tampered managed block must fail health, got %v", err)
		}
	})

	t.Run("skill-drift", func(t *testing.T) {
		broken := t.TempDir()
		mustWrite(t, agentsPath(broken), kit.RenderManagedBlock(rulesBody))
		if err := a.HealthCheck(ctx, broken, desired); err == nil || !strings.Contains(err.Error(), "skill") {
			t.Fatalf("missing skill link must fail health, got %v", err)
		}
	})

	t.Run("health-does-not-run-dump-commands", func(t *testing.T) {
		// 健康检查只执行 `dsh --version`：dump 类命令会初始化 profile 文件（§2.1）。
		var calls []string
		spy := &Adapter{Probe: func(_ context.Context, home string) (string, error) {
			calls = append(calls, home)
			return repoVersion + "\n", nil
		}}
		if err := spy.HealthCheck(ctx, home, desired); err != nil {
			t.Fatalf("HealthCheck: %v", err)
		}
		if len(calls) != 1 {
			t.Fatalf("health check must probe exactly once, got %d", len(calls))
		}
	})
}

// ---- 复核口径：写前 pre-flight ----

// TestApplyPreflightRejectsBeforeAnyWrite：绕过 Validate 的陈旧计划也不得发生部分写入。
func TestApplyPreflightRejectsBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()

	t.Run("bad-skill-digest", func(t *testing.T) {
		home := baselineHome(t)
		a, desired := newTestAdapter(), fullDesired(t, home)
		before := readText(t, agentsPath(home))
		err := a.Apply(ctx, home, desired, []adapter.Change{
			{Family: ID, Step: "rules", Key: keyRules},
			{Family: ID, Step: "skills", Key: "skill:ok", To: "../../../../.."},
		})
		if err == nil {
			t.Fatal("bad digest must be rejected")
		}
		if got := readText(t, agentsPath(home)); got != before {
			t.Fatal("AGENTS.md was written before the skill digest was checked")
		}
	})

	t.Run("stale-mcp-plan", func(t *testing.T) {
		home := baselineHome(t)
		a := newTestAdapter()
		desired := fullDesired(t, home)
		desired.MCP = map[string]adapter.MCPEntry{"fleet-tool": {Command: "npx"}}
		before := readText(t, agentsPath(home))
		err := a.Apply(ctx, home, desired, []adapter.Change{
			{Family: ID, Step: "rules", Key: keyRules},
		})
		if err == nil || !strings.Contains(err.Error(), `capability "mcp" is unsupported`) {
			t.Fatalf("mcp must be rejected in Apply pre-flight, got %v", err)
		}
		if got := readText(t, agentsPath(home)); got != before {
			t.Fatal("AGENTS.md was written before the mcp capability was checked")
		}
	})

	t.Run("stale-model-provider-plan", func(t *testing.T) {
		home := baselineHome(t)
		a := newTestAdapter()
		desired := fullDesired(t, home)
		desired.Config = modelProviderDesired().Config
		before := readText(t, agentsPath(home))
		err := a.Apply(ctx, home, desired, []adapter.Change{
			{Family: ID, Step: "rules", Key: keyRules},
		})
		if err == nil || !strings.Contains(err.Error(), `capability "modelProvider" is unverified`) {
			t.Fatalf("modelProvider must be rejected in Apply pre-flight, got %v", err)
		}
		if got := readText(t, agentsPath(home)); got != before {
			t.Fatal("AGENTS.md was written before modelProvider was checked")
		}
	})

	t.Run("incomplete-managed-block", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, agentsPath(home), "# user file\n"+kit.BlockBegin+"\nno end marker\n")
		before := readText(t, agentsPath(home))
		a, desired := newTestAdapter(), fullDesired(t, home)
		if err := a.Validate(ctx, home, desired); err == nil {
			t.Fatal("incomplete managed block must be rejected by Validate")
		}
		if err := a.Apply(ctx, home, desired, []adapter.Change{
			{Family: ID, Step: "rules", Key: keyRules},
		}); err == nil {
			t.Fatal("incomplete managed block must be rejected by Apply")
		}
		if got := readText(t, agentsPath(home)); got != before {
			t.Fatal("AGENTS.md was written from an incomplete managed block")
		}
	})
}

// TestDefaultVersionProbePinsHome：默认探测必须把 HOME/DSH_HOME 都钉到注入根，
// 否则 `dsh --version` 会读另一个 ~/.dsh（护栏 #12）。
func TestDefaultVersionProbePinsHome(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "probe.env")
	script := filepath.Join(dir, binaryName)
	mustWrite(t, script, "#!/bin/sh\nprintf '%s\\n%s\\n' \"$HOME\" \"$DSH_HOME\" > \"$PROBE_OUT\"\n"+
		"printf '0.1.7-rc.1\\n'\n")
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

// TestRealDshBinaryVersionProbe 是本机实测补证（默认跳过）：用真实 `dsh --version`
// 验证探测与形态解析。只在 `DSH_REAL=1` 且 PATH 上有 dsh 时执行，绝不触碰真实 ~/.dsh。
func TestRealDshBinaryVersionProbe(t *testing.T) {
	if os.Getenv("DSH_REAL") != "1" {
		t.Skip("set DSH_REAL=1 to run against the installed dsh binary")
	}
	if _, err := exec.LookPath(binaryName); err != nil {
		t.Skipf("dsh binary not on PATH: %v", err)
	}
	var calls []string
	d, err := (&Adapter{Probe: func(ctx context.Context, home string) (string, error) {
		calls = append(calls, home)
		return defaultVersionProbe(ctx, home)
	}}).Detect(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !d.Installed || d.Version == "" {
		t.Fatalf("real dsh must report a version, got %+v (calls=%v)", d, calls)
	}
}
