package bundle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// fixtureSnapshot 构造一份最小可用快照（1 个 Skill 引用 + 1 个家族）。
func fixtureSnapshot(t *testing.T, skills map[string]domain.SkillDesired) *domain.DesiredStateSnapshot {
	t.Helper()
	snap := &domain.DesiredStateSnapshot{
		Machine:                 "node-1",
		Generation:              7,
		Digest:                  "sha256:" + strings.Repeat("a", 64),
		Protocol:                domain.SnapshotProtocolVersion,
		CanonicalizationVersion: "1",
		CreatedAt:               time.Unix(0, 0).UTC(),
		Desired: domain.DesiredState{
			SchemaVersion: "1",
			Agents:        map[string]domain.AgentDesired{"codex": {Version: "1.2.3"}},
			Skills:        skills,
		},
	}
	return snap
}

// fixtureArtifact 在 root/<digest>/ 下写一个工件树（模拟控制面工件缓存，§4.7）。
func fixtureArtifact(t *testing.T, root, digest string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, strings.TrimPrefix(digest, "sha256:"))
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildAndVerifyRoundTrip(t *testing.T) {
	srcRoot := t.TempDir()
	digest := "sha256:" + strings.Repeat("b", 64)
	fixtureArtifact(t, srcRoot, digest, map[string]string{
		"SKILL.md":       "# skill\n",
		"scripts/run.sh": "#!/bin/sh\necho hi\n",
	})
	snap := fixtureSnapshot(t, map[string]domain.SkillDesired{
		"pdf-tools": {Revision: "abc", ContentDigest: digest},
	})
	dir := filepath.Join(t.TempDir(), "bundle")
	m, err := Build(dir, snap, LocalArtifactSource{Root: srcRoot}, DefaultLimits())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(m.Artifacts) != 1 {
		t.Fatalf("want 1 artifact, got %d", len(m.Artifacts))
	}
	if !strings.HasPrefix(m.BundleDigest, "sha256:") {
		t.Fatalf("bundle digest missing: %q", m.BundleDigest)
	}
	if got := m.Artifacts[0].Path; !strings.HasPrefix(got, SkillsDir+"/") {
		t.Fatalf("artifact path %q not under %s", got, SkillsDir)
	}
	// 布局必须与 §7.4 一致：manifest.json 与 artifacts/skills/<digest>/…
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(m.Artifacts[0].Path), "SKILL.md")); err != nil {
		t.Fatalf("artifact content missing: %v", err)
	}
	// 服务端构建的包必须能被节点侧校验通过。
	got, err := Verify(dir, DefaultLimits())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.BundleDigest != m.BundleDigest || got.Artifacts[0].TreeDigest != m.Artifacts[0].TreeDigest {
		t.Fatalf("verify round-trip mismatch: %+v vs %+v", got, m)
	}
	// 物化到节点规范缓存并幂等重放。
	cache := t.TempDir()
	installed, err := InstallArtifacts(dir, cache, got, DefaultLimits())
	if err != nil {
		t.Fatalf("InstallArtifacts: %v", err)
	}
	if len(installed) != 1 {
		t.Fatalf("want 1 installed artifact, got %d", len(installed))
	}
	if _, err := os.Stat(filepath.Join(installed[0], "scripts", "run.sh")); err != nil {
		t.Fatalf("installed content missing: %v", err)
	}
	// 物化路径必须与适配器契约一致（kit.SkillCacheDir: <root>/<name>/sha256-<hex>）。
	wantCache := filepath.Join(cache, "pdf-tools", strings.ReplaceAll(digest, ":", "-"))
	if installed[0] != wantCache {
		t.Fatalf("install path = %s, want %s", installed[0], wantCache)
	}
	if again, err := InstallArtifacts(dir, cache, got, DefaultLimits()); err != nil || len(again) != 1 {
		t.Fatalf("InstallArtifacts must be idempotent: %v %v", again, err)
	}
}

func TestBuildDeterministic(t *testing.T) {
	srcRoot := t.TempDir()
	d1 := "sha256:" + strings.Repeat("c", 64)
	d2 := "sha256:" + strings.Repeat("d", 64)
	fixtureArtifact(t, srcRoot, d1, map[string]string{"a.md": "a\n"})
	fixtureArtifact(t, srcRoot, d2, map[string]string{"b.md": "b\n"})
	snap := fixtureSnapshot(t, map[string]domain.SkillDesired{
		"zeta":  {Revision: "1", ContentDigest: d1},
		"alpha": {Revision: "1", ContentDigest: d2},
	})
	var digests []string
	for i := 0; i < 2; i++ {
		dir := filepath.Join(t.TempDir(), "bundle")
		m, err := Build(dir, snap, LocalArtifactSource{Root: srcRoot}, DefaultLimits())
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		digests = append(digests, m.BundleDigest)
		if len(m.Artifacts) != 2 || m.Artifacts[0].Name != "alpha" || m.Artifacts[1].Name != "zeta" {
			t.Fatalf("artifacts must be name-sorted: %+v", m.Artifacts)
		}
	}
	if digests[0] != digests[1] {
		t.Fatalf("bundle digest not deterministic: %s vs %s", digests[0], digests[1])
	}
}

// TestSymlinkEscapeRejected：工件内容中的符号链接默认拒绝（§4.7 第 3 条/A7）。
func TestSymlinkEscapeRejected(t *testing.T) {
	srcRoot := t.TempDir()
	digest := "sha256:" + strings.Repeat("e", 64)
	fixtureArtifact(t, srcRoot, digest, map[string]string{"SKILL.md": "# ok\n"})
	artDir := filepath.Join(srcRoot, strings.TrimPrefix(digest, "sha256:"))
	if err := os.Symlink("/etc/passwd", filepath.Join(artDir, "escape")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	snap := fixtureSnapshot(t, map[string]domain.SkillDesired{"evil": {Revision: "1", ContentDigest: digest}})
	_, err := Build(filepath.Join(t.TempDir(), "bundle"), snap, LocalArtifactSource{Root: srcRoot}, DefaultLimits())
	if err == nil {
		t.Fatal("symlink in artifact tree must be rejected")
	}
	if got := domain.ReasonOf(err); got != domain.ReasonSkillPathRejected {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonSkillPathRejected, got, err)
	}
}

// TestVerifyRejectsTamperedContent：篡改工件内容 → SkillDigestMismatch（§29.11）。
func TestVerifyRejectsTamperedContent(t *testing.T) {
	dir, m := buildFixture(t)
	target := filepath.Join(dir, filepath.FromSlash(m.Artifacts[0].Path), "SKILL.md")
	if err := os.WriteFile(target, []byte("# tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(dir, DefaultLimits())
	if err == nil {
		t.Fatal("tampered artifact must be rejected")
	}
	if got := domain.ReasonOf(err); got != domain.ReasonSkillDigestMismatch {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonSkillDigestMismatch, got, err)
	}
}

// TestVerifyRejectsTamperedManifest：manifest 被替换 → bundle 自身摘要校验失败
// （§7.4 安全点 5：防止传输层之外的替换）。
func TestVerifyRejectsTamperedManifest(t *testing.T) {
	dir, _ := buildFixture(t)
	path := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"generation": 7`, `"generation": 8`, 1)
	if tampered == string(raw) {
		t.Fatal("fixture manifest did not contain expected generation field")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Verify(dir, DefaultLimits())
	if err == nil {
		t.Fatal("tampered manifest must be rejected")
	}
	if got := domain.ReasonOf(err); got != domain.ReasonSkillDigestMismatch {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonSkillDigestMismatch, got, err)
	}
}

// TestVerifyRejectsPathEscape 使用 testdata/bundles/ 的恶意夹具（§3.6 布局）：
// 夹具里的 manifest 声明了越界路径，测试重新封装摘要以隔离"路径校验"这一条。
func TestVerifyRejectsPathEscape(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		wantCode string
	}{
		{"relative-escape", "malicious-path-relative", domain.ReasonSkillPathRejected},
		{"absolute-path", "malicious-path-absolute", domain.ReasonSkillPathRejected},
		{"bad-digest", "malicious-bad-digest", domain.ReasonSkillPathRejected},
		{"dot-element", "malicious-path-dot", domain.ReasonSkillPathRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw, err := os.ReadFile(filepath.Join("testdata", "bundles", tc.fixture, ManifestName))
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0o644); err != nil {
				t.Fatal(err)
			}
			// 重新封装 bundle 摘要：本用例要证明的是路径/digest 形态校验生效，
			// 而不是摘要校验（后者由 TestVerifyRejectsTamperedManifest 覆盖）。
			var m Manifest
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			if err := writeManifest(dir, &m); err != nil {
				t.Fatal(err)
			}
			_, err = Verify(dir, DefaultLimits())
			if err == nil {
				t.Fatal("malicious bundle must be rejected")
			}
			if got := domain.ReasonOf(err); got != tc.wantCode {
				t.Fatalf("want %s, got %s (%v)", tc.wantCode, got, err)
			}
		})
	}
}

// TestVerifyRequiresEveryReferencedArtifact：快照引用了工件但包里没有 →
// 拒绝整包（不做部分应用，§7.4 安全点 3）。
func TestVerifyRequiresEveryReferencedArtifact(t *testing.T) {
	dir, m := buildFixture(t)
	m.Artifacts = nil
	if err := writeManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(dir, DefaultLimits())
	if err == nil {
		t.Fatal("missing referenced artifact must be rejected")
	}
	if got := domain.ReasonOf(err); got != domain.ReasonSkillDownloadFailed {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonSkillDownloadFailed, got, err)
	}
}

// TestBudgets：§4.7 四项预算在构建期快速失败（不传输）。
func TestBudgets(t *testing.T) {
	srcRoot := t.TempDir()
	digest := "sha256:" + strings.Repeat("f", 64)
	fixtureArtifact(t, srcRoot, digest, map[string]string{"big.md": strings.Repeat("x", 4096)})
	snap := fixtureSnapshot(t, map[string]domain.SkillDesired{"big": {Revision: "1", ContentDigest: digest}})

	small := DefaultLimits()
	small.MaxArtifactBytes = 100
	_, err := Build(filepath.Join(t.TempDir(), "b"), snap, LocalArtifactSource{Root: srcRoot}, small)
	if got := domain.ReasonOf(err); got != domain.ReasonArtifactTooLarge {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonArtifactTooLarge, got, err)
	}

	fewFiles := DefaultLimits()
	fewFiles.MaxArtifactFiles = 0
	_, err = Build(filepath.Join(t.TempDir(), "b"), snap, LocalArtifactSource{Root: srcRoot}, fewFiles)
	if got := domain.ReasonOf(err); got != domain.ReasonArtifactTooLarge {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonArtifactTooLarge, got, err)
	}

	smallBundle := DefaultLimits()
	smallBundle.MaxBundleBytes = 10
	_, err = Build(filepath.Join(t.TempDir(), "b"), snap, LocalArtifactSource{Root: srcRoot}, smallBundle)
	if got := domain.ReasonOf(err); got != domain.ReasonBundleTooLarge {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonBundleTooLarge, got, err)
	}
}

// TestMissingArtifactRejected：来源里没有该 digest → SkillDownloadFailed，不派发。
func TestMissingArtifactRejected(t *testing.T) {
	snap := fixtureSnapshot(t, map[string]domain.SkillDesired{
		"ghost": {Revision: "1", ContentDigest: "sha256:" + strings.Repeat("9", 64)},
	})
	_, err := Build(filepath.Join(t.TempDir(), "b"), snap, LocalArtifactSource{Root: t.TempDir()}, DefaultLimits())
	if got := domain.ReasonOf(err); got != domain.ReasonSkillDownloadFailed {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonSkillDownloadFailed, got, err)
	}
}

// TestEmptySourceForSkillFreeSnapshot：无 Skill 引用的快照可以打包（空工件集）。
func TestEmptySourceForSkillFreeSnapshot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	m, err := Build(dir, fixtureSnapshot(t, nil), EmptySource{}, DefaultLimits())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(m.Artifacts) != 0 {
		t.Fatalf("want no artifacts, got %+v", m.Artifacts)
	}
	if _, err := Verify(dir, DefaultLimits()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestValidateNameDigestAndSafeJoin(t *testing.T) {
	for _, ok := range []string{"pdf-tools", "a.b_c-1", "A1"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("name %q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", "../x", "a b", "ünïcode"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
	if err := ValidateDigest("sha256:" + strings.Repeat("0", 64)); err != nil {
		t.Errorf("valid digest rejected: %v", err)
	}
	for _, bad := range []string{"", "sha256:zz", "sha256:" + strings.Repeat("0", 63), strings.Repeat("0", 64)} {
		if err := ValidateDigest(bad); err == nil {
			t.Errorf("digest %q should be rejected", bad)
		}
	}
	root := t.TempDir()
	if _, err := SafeJoin(root, "artifacts/skills/abc/SKILL.md"); err != nil {
		t.Errorf("legal relative path rejected: %v", err)
	}
	for _, bad := range []string{"/etc/passwd", "../x", "a/../../x", "./a", "a/./b", "a\\b", ""} {
		if _, err := SafeJoin(root, bad); err == nil {
			t.Errorf("path %q should be rejected", bad)
		}
	}
}

// buildFixture 返回一个已构建并通过校验的 bundle 目录与其 manifest。
func buildFixture(t *testing.T) (string, *Manifest) {
	t.Helper()
	srcRoot := t.TempDir()
	digest := "sha256:" + strings.Repeat("1", 64)
	fixtureArtifact(t, srcRoot, digest, map[string]string{"SKILL.md": "# skill\n", "nested/data.txt": "data\n"})
	snap := fixtureSnapshot(t, map[string]domain.SkillDesired{"tool": {Revision: "r1", ContentDigest: digest}})
	dir := filepath.Join(t.TempDir(), "bundle")
	m, err := Build(dir, snap, LocalArtifactSource{Root: srcRoot}, DefaultLimits())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, err := Verify(dir, DefaultLimits()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return dir, m
}

func TestCodedErrorsAreWrapped(t *testing.T) {
	err := errf(domain.ReasonSkillPathRejected, "bad %s", "path")
	var ce *domain.CodedError
	if !errors.As(err, &ce) {
		t.Fatalf("errf must produce *domain.CodedError, got %T", err)
	}
	if ce.Reason != domain.ReasonSkillPathRejected {
		t.Fatalf("reason = %q", ce.Reason)
	}
}
