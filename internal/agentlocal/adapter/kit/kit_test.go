package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureSkillLinkRejectsMalformedDigest 是复核 B1 的收口回归：digest 是缓存目录分量，
// 形状不符（含穿越）时必须在任何写入之前显式失败，绝不把软链种到任意已存在目录。
// 「去掉 EnsureSkillLink 里的 ValidDigest 守卫即失败」。
func TestEnsureSkillLinkRejectsMalformedDigest(t *testing.T) {
	bad := []string{
		"../../../../../..",
		"sha256:not-a-digest/../../../..",
		"sha256:../../../..",
		"sha256:",
		"sha256:cc",
		"sha256:" + strings.Repeat("Z", 64), // 非小写 hex
		"",
		"not-a-digest",
	}
	for _, digest := range bad {
		t.Run(digest, func(t *testing.T) {
			home := t.TempDir()
			// 预置一个“任意已存在目录”，证明坏 digest 不会借它落链。
			existing := filepath.Join(home, "existing-target")
			if err := os.MkdirAll(existing, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(home, ".hermes", "skills", "pwn")
			if err := EnsureSkillLink(link, "pwn", digest, SkillCacheDir(home, "pwn", digest)); err == nil {
				t.Fatalf("digest %q must be rejected", digest)
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				t.Fatalf("digest %q created a link (err=%v)", digest, err)
			}
		})
	}

	t.Run("valid-digest-still-works", func(t *testing.T) {
		home := t.TempDir()
		digest := "sha256:" + strings.Repeat("a", 64)
		cache := SkillCacheDir(home, "ok", digest)
		if err := os.MkdirAll(cache, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, ".hermes", "skills", "ok")
		if err := EnsureSkillLink(link, "ok", digest, cache); err != nil {
			t.Fatalf("valid digest rejected: %v", err)
		}
		if got := ReadSkillDigest(link); got != digest {
			t.Fatalf("link digest = %q, want %q", got, digest)
		}
	})
}
