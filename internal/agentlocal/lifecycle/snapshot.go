package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileRecord 描述快照中的一个条目（文件、目录或符号链接本身，不跟随链接）。
type FileRecord struct {
	Mode       fs.FileMode // 含权限位、setuid/setgid/sticky 与链接标记
	Size       int64
	SHA256     string // 常规文件的内容摘要；目录与符号链接为空
	LinkTarget string // 符号链接的目标串（原样，不做展开）；非链接为空
}

// Snapshot 是一棵目录树在某一时刻的完整形态：相对路径（/ 分隔）→ 条目。
type Snapshot map[string]FileRecord

// SnapshotDir 抓取 root 的快照。root 本身不入表；路径统一用 / 分隔，跨平台可比。
func SnapshotDir(root string) (Snapshot, error) {
	snap := Snapshot{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		info, err := d.Info() // Lstat 语义：符号链接按链接本身记录
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rec := FileRecord{Mode: info.Mode(), Size: info.Size()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			rec.LinkTarget = target
		case info.Mode().IsRegular():
			sum, err := fileSHA256(p)
			if err != nil {
				return err
			}
			rec.SHA256 = sum
		}
		snap[filepath.ToSlash(rel)] = rec
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("lifecycle: snapshot %s: %w", root, err)
	}
	return snap, nil
}

// DiffKind 枚举前后两次快照之间的差异类型。
type DiffKind string

const (
	DiffAdded   DiffKind = "added"
	DiffRemoved DiffKind = "removed"
	DiffContent DiffKind = "content-changed" // 常规文件内容/大小变化
	DiffMode    DiffKind = "mode-changed"    // 权限位或类型位变化
	DiffLink    DiffKind = "link-changed"    // 符号链接目标变化
)

// Difference 描述一处差异；From/To 为零值表示该侧不存在。
type Difference struct {
	Path     string
	Kind     DiffKind
	From, To FileRecord
}

// Diff 比较 before → after，返回全部差异（按路径排序，输出稳定）。
func (before Snapshot) Diff(after Snapshot) []Difference {
	var diffs []Difference
	paths := map[string]bool{}
	for p := range before {
		paths[p] = true
	}
	for p := range after {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	for _, p := range sorted {
		b, a := before[p], after[p]
		_, bOK := before[p]
		_, aOK := after[p]
		switch {
		case bOK && !aOK:
			diffs = append(diffs, Difference{Path: p, Kind: DiffRemoved, From: b})
		case aOK && !bOK:
			diffs = append(diffs, Difference{Path: p, Kind: DiffAdded, To: a})
		case b.Mode != a.Mode:
			diffs = append(diffs, Difference{Path: p, Kind: DiffMode, From: b, To: a})
		case b.LinkTarget != a.LinkTarget:
			diffs = append(diffs, Difference{Path: p, Kind: DiffLink, From: b, To: a})
		case b.SHA256 != a.SHA256 || b.Size != a.Size:
			diffs = append(diffs, Difference{Path: p, Kind: DiffContent, From: b, To: a})
		}
	}
	return diffs
}

// Report 渲染人类可读的差异清单（用于证据留存）。
func (before Snapshot) Report(after Snapshot) string {
	diffs := before.Diff(after)
	if len(diffs) == 0 {
		return "no differences"
	}
	var b strings.Builder
	for _, d := range diffs {
		fmt.Fprintf(&b, "%s %s", d.Kind, d.Path)
		switch d.Kind {
		case DiffLink:
			fmt.Fprintf(&b, ": %q -> %q", d.From.LinkTarget, d.To.LinkTarget)
		case DiffMode:
			fmt.Fprintf(&b, ": %s -> %s", d.From.Mode, d.To.Mode)
		case DiffAdded:
			fmt.Fprintf(&b, ": %+v", d.To)
		case DiffRemoved:
			fmt.Fprintf(&b, ": %+v", d.From)
		case DiffContent:
			fmt.Fprintf(&b, ": size %d -> %d, sha %s -> %s",
				d.From.Size, d.To.Size, shortSHA(d.From.SHA256), shortSHA(d.To.SHA256))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
