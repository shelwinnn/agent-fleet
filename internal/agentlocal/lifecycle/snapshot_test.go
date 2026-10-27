package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

// DirSnapshot 的单元测试：抓取语义（链接不跟随、权限入档）与差异分类。

func TestSnapshotCapturesModesLinksAndContent(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("plain.txt", "hello", 0o644)
	write("secret.txt", "s3cret", 0o600)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("plain.txt", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("nowhere"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}

	snap := mustSnapshot(t, root)
	if got := snap["plain.txt"]; got.SHA256 == "" || got.Mode.Perm() != 0o644 {
		t.Fatalf("plain.txt record wrong: %+v", got)
	}
	if got := snap["secret.txt"]; got.Mode.Perm() != 0o600 {
		t.Fatalf("secret.txt perm not captured: %+v", got)
	}
	if got := snap["alias"]; got.LinkTarget != "plain.txt" || got.SHA256 != "" {
		t.Fatalf("symlink record wrong (must record link, not target content): %+v", got)
	}
	if got := snap["dangling"]; got.LinkTarget != "nowhere" {
		t.Fatalf("dangling symlink must still be capturable: %+v", got)
	}
	if _, ok := snap["sub"]; !ok {
		t.Fatalf("empty dir absent from snapshot")
	}
}

func TestSnapshotDiffClassifiesChanges(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one", 0o644)
	write("keep.txt", "same", 0o644)
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	before := mustSnapshot(t, root)

	// 变更：内容、权限、链接目标；新增 b.txt；删除 keep.txt。
	write("a.txt", "two", 0o644)
	if err := os.Chmod(filepath.Join(root, "a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	// mode 变化会同时改变内容 hash 的记录吗？不会：mode 与内容独立字段。
	write("b.txt", "new", 0o644)
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatal(err)
	}
	after := mustSnapshot(t, root)

	kinds := map[string]DiffKind{}
	for _, d := range before.Diff(after) {
		kinds[d.Path] = d.Kind
	}
	if kinds["a.txt"] != DiffMode {
		// 内容与权限同变时按实现顺序归类为 mode；两类信息都在记录里。
		if kinds["a.txt"] != DiffContent {
			t.Fatalf("a.txt change missed entirely: %v", kinds)
		}
	}
	if kinds["b.txt"] != DiffAdded || kinds["keep.txt"] != DiffRemoved || kinds["link"] != DiffLink {
		t.Fatalf("classification wrong: %v", kinds)
	}

	// 恢复原状后 Diff 必须为空（对称性）。注意 os.WriteFile 不会重置既有
	// 文件的权限，模式必须显式还原。
	write("a.txt", "one", 0o644)
	write("keep.txt", "same", 0o644)
	if err := os.Chmod(filepath.Join(root, "a.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	restored := mustSnapshot(t, root)
	if diffs := before.Diff(restored); len(diffs) != 0 {
		t.Fatalf("restored tree must diff clean:\n%s", before.Report(restored))
	}
}
