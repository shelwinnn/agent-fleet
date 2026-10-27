package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 本文件验证验收项 4：隔离环境安装/升级/卸载各一次，前后对比测试用户数据
// 目录（~/.codex 替身）的文件集合、内容、权限、符号链接——零变更才通过。

// TestLifecycleOpsLeaveUserDataUntouched：完整走一遍 install → upgrade →
// uninstall，期间 ~/.codex 替身必须逐字节不变（含 0600 凭据、软链目标、
// 悬空软链）；同目录快照本身能察觉人为篡改，证明对比器不是恒真。
func TestLifecycleOpsLeaveUserDataUntouched(t *testing.T) {
	e := newEnv(t)
	if err := e.Publish("@openai/codex", "0.44.0", "codex", "codex-cli 0.44.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish("@openai/codex", "0.45.0", "codex", "codex-cli 0.45.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.SeedCodexData(); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(e.Home, ".codex")
	sharedDir := filepath.Join(e.Home, "shared")

	before, err := SnapshotDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	beforeShared, err := SnapshotDir(sharedDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 || len(beforeShared) == 0 {
		t.Fatalf("seed data missing: .codex=%d entries, shared=%d entries", len(before), len(beforeShared))
	}

	eng := newEngine()
	req := withVersion(codexReq, "0.44.0")
	if _, err := eng.InstallExact(context.Background(), req); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := eng.UpgradeToExact(context.Background(), withVersion(codexReq, "0.45.0")); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if _, err := eng.Uninstall(context.Background(), codexUnreq); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	after, err := SnapshotDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	afterShared, err := SnapshotDir(sharedDir)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := before.Diff(after); len(diffs) != 0 {
		t.Fatalf("~/.codex changed during lifecycle ops:\n%s", before.Report(after))
	}
	if diffs := beforeShared.Diff(afterShared); len(diffs) != 0 {
		t.Fatalf("shared dir changed during lifecycle ops:\n%s", beforeShared.Report(afterShared))
	}
	// 引擎探测走 npm-only 的 DetectByMethod，绝不执行宿主上的其他包管理器
	// （上游 DetectAll 的 pip 策略会调用 uv，uv 会在 HOME 下写 .cache/uv）。
	if _, err := os.Stat(filepath.Join(e.Home, ".cache")); !os.IsNotExist(err) {
		t.Fatalf("detection must not cause foreign package managers to write into HOME: %v", err)
	}

	// 对比器自证：人为篡改必须被抓到（内容 / 权限 / 链接 / 集合各一）。
	touch := filepath.Join(dataDir, "config.toml")
	if err := os.WriteFile(touch, []byte("# tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if diffs := before.Diff(mustSnapshot(t, dataDir)); len(diffs) == 0 || diffs[0].Kind != DiffContent {
		t.Fatalf("comparator missed content tampering: %v", diffs)
	}
	if err := os.Chmod(touch, 0o666); err != nil {
		t.Fatal(err)
	}
	if diffs := before.Diff(mustSnapshot(t, dataDir)); len(diffs) == 0 || diffs[0].Kind != DiffMode {
		t.Fatalf("comparator missed mode tampering: %v", diffs)
	}
	if err := os.Remove(filepath.Join(dataDir, "stale-link")); err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, d := range before.Diff(mustSnapshot(t, dataDir)) {
		if d.Path == "stale-link" && d.Kind == DiffRemoved {
			removed = true
		}
	}
	if !removed {
		t.Fatal("comparator missed removed dangling symlink")
	}
}

func mustSnapshot(t *testing.T, dir string) Snapshot {
	t.Helper()
	s, err := SnapshotDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
