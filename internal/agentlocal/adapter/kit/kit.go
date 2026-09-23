// Package kit 是家族适配器共享的工具集（架构 v1.1.2 §5.5/§16、FR-5.1、FR-6.3）：
// 原子写、受管标记块、Skill 软链、投影摘要与版本探测。家族适配器只在此处复用
// 机制，路径与格式仍各自封装（§5.4：控制面不得知道家族路径）。
package kit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
)

// 受管标记块（spec §16.2 / FR-5.1）。rules 文件只在此块内被改写，
// 块外内容逐字节保留（护栏 #3）。
const (
	BlockBegin = "# BEGIN agent-fleet managed"
	BlockEnd   = "# END agent-fleet managed"
)

// AtomicWrite 落实 §5.5/§16.1 的原子写：临时文件 + fsync + rename + 目录 fsync。
// 尽量保留原权限：目标已存在时沿用其权限位。
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create dir for %s: %w", path, err)
	}
	tmp := path + ".agent-fleet.tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open temp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	// 目录 fsync：确保 rename 本身持久（§16.1 原子写的完整语义）。
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ProjectionDigest 对归一化受管投影计算摘要（FR-8.2：绝不对整份文件哈希）。
// 规范化规则与快照摘要共用 desiredstate.CanonicalJSON（RD-4），不引入第二套。
func ProjectionDigest(projection map[string]any) string {
	canonical, err := desiredstate.CanonicalJSON(projection)
	if err != nil {
		return "sha256:error"
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", sum)
}

// RenderManagedBlock 生成受管块文本（含标记；结尾恰好一个换行）。
func RenderManagedBlock(content string) string {
	body := strings.TrimRight(content, "\n")
	if body == "" {
		return BlockBegin + "\n" + BlockEnd + "\n"
	}
	return BlockBegin + "\n" + body + "\n" + BlockEnd + "\n"
}

// ReadManagedBlock 读取文件中的受管块内容（不含标记）。文件不存在返回
// ("", false, nil)；标记不完整（只有开始没有结束）返回错误而不是猜测。
func ReadManagedBlock(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", path, err)
	}
	inner, ok, err := ManagedBlockIn(string(raw))
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", path, err)
	}
	return inner, ok, nil
}

// WriteManagedBlock 只替换受管块，块外内容逐字节保留（FR-5.1）。
// 文件不存在或没有块时追加块；有块时原位替换。
func WriteManagedBlock(path, content string) error {
	block := RenderManagedBlock(content)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return AtomicWrite(path, []byte(block), 0o644)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	text := string(raw)
	begin := strings.Index(text, BlockBegin)
	end := strings.Index(text, BlockEnd)
	if begin >= 0 && end > begin {
		// 块结束标记之后可能还有内容（含行尾换行）：保留全部尾部。
		tail := text[end+len(BlockEnd):]
		tail = strings.TrimPrefix(tail, "\n")
		out := text[:begin] + block
		if tail != "" {
			out += tail
		}
		return AtomicWrite(path, []byte(out), 0o644)
	}
	out := text
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += block
	return AtomicWrite(path, []byte(out), 0o644)
}

// RulesContent 把归一化 rules 条目渲染为块内容（确定性：按键排序）。
// 多条时每条前加 `<!-- rule: <name> -->` 以便人读区分。
func RulesContent(rules map[string]adapter.RulesEntry) string {
	if len(rules) == 0 {
		return ""
	}
	names := make([]string, 0, len(rules))
	for n := range rules {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		body := strings.TrimRight(rules[n].Content, "\n")
		if len(names) > 1 {
			parts = append(parts, "<!-- rule: "+n+" -->\n"+body)
			continue
		}
		parts = append(parts, body)
	}
	return strings.Join(parts, "\n\n")
}

// ---- Skill 目的地（FR-6.3）----

// SkillCacheDir 是节点规范缓存目录（FR-6.3）：<home>/.local/share/agent-fleet/skills/<name>/<digest>。
// 约定与 reconciler 的备份目录一致（同以 home 为根，护栏 #12）。
func SkillCacheDir(home, name, digest string) string {
	return filepath.Join(home, ".local", "share", "agent-fleet", "skills", SkillDirName(name), DigestDirName(digest))
}

// SkillDirName 把 digest 里的 "sha256:" 前缀转成可做目录名的形式。
func DigestDirName(digest string) string {
	return strings.ReplaceAll(digest, ":", "-")
}

// SkillDirName 做同一件事（名字保留给 skill 名称的清洗）。
func SkillDirName(name string) string { return name }

// ReadSkillDigest 读软链目标中的 digest（copy 模式读内容摘要文件）。
// 返回 "" 表示该 Skill 未被本适配器物化（漂移信号，FR-6.3）。
func ReadSkillDigest(linkPath string) string {
	target, err := os.Readlink(linkPath)
	if err == nil {
		return digestFromPath(target)
	}
	// copy 模式：内容摘要写在 <link>/.agent-fleet-digest。
	if b, err := os.ReadFile(filepath.Join(linkPath, ".agent-fleet-digest")); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func digestFromPath(target string) string {
	base := filepath.Base(strings.TrimRight(target, "/"))
	if strings.HasPrefix(base, "sha256-") {
		return "sha256:" + strings.TrimPrefix(base, "sha256-")
	}
	return ""
}

// EnsureSkillLink 把家族 Skill 目的地指向规范缓存（FR-6.3）。
// 缓存目录缺失时**显式失败**（不静默跳过、不写坏链）：工件物化由
// FetchArtifact/bundle 路径负责，本片未实现该路径，故不会伪装成功。
func EnsureSkillLink(linkPath, name, digest, cacheDir string) error {
	if _, err := os.Stat(cacheDir); err != nil {
		return fmt.Errorf("skill %q artifact %s is not materialized at %s (artifact fetch is not wired yet): %w",
			name, digest, cacheDir, err)
	}
	if cur, err := os.Readlink(linkPath); err == nil {
		if digestFromPath(cur) == digest {
			return nil
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("replace skill link %s: %w", linkPath, err)
		}
	} else if _, statErr := os.Stat(linkPath); statErr == nil {
		// 目标存在但不是软链（用户自建目录）：不动它，显式失败（护栏 #3）。
		return fmt.Errorf("skill path %s exists and is not an agent-fleet symlink; refusing to replace", linkPath)
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}
	if err := os.Symlink(cacheDir, linkPath); err != nil {
		return fmt.Errorf("link skill %s -> %s: %w", linkPath, cacheDir, err)
	}
	return nil
}

// ---- 版本探测 ----

// VersionProbe 探测可执行程序版本（FR-2.3：安装后必须用适配器探测验证版本）。
// 可注入：fixture 测试不依赖真实二进制。
type VersionProbe func(ctx context.Context, bin string, args ...string) (string, error)

// ExecVersionProbe 是默认探测：argv 直执行（不经过 sh -c，护栏 #1）。
func ExecVersionProbe(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ErrNotInstalled 表示可执行程序不存在（与"版本不可解析"区分，矩阵验收 1）。
var ErrNotInstalled = errors.New("executable not installed")

// ProbeVersion 执行探测并解析版本；程序缺失返回 ErrNotInstalled。
func ProbeVersion(ctx context.Context, probe VersionProbe, bin string, args []string, parse func(string) string) (string, error) {
	if probe == nil {
		probe = ExecVersionProbe
	}
	out, err := probe(ctx, bin, args...)
	if err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) || errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrNotInstalled, bin)
		}
		var le *os.PathError
		if errors.As(err, &le) && errors.Is(le.Err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrNotInstalled, bin)
		}
		return "", fmt.Errorf("probe %s: %w", bin, err)
	}
	v := parse(out)
	if v == "" {
		return "", fmt.Errorf("probe %s: cannot parse version from output %q", bin, strings.TrimSpace(out))
	}
	return v, nil
}

// FirstLine 是常见的版本输出解析：取首个非空行并去掉前缀。
func FirstLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// TrimVersionPrefix 去掉 "name " 前缀（如 "codex-cli 0.154.0" → "0.154.0"）。
func TrimVersionPrefix(prefix string) func(string) string {
	return func(out string) string {
		line := FirstLine(out)
		line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return ""
		}
		return fields[0]
	}
}

// ManagedProjection 只保留受管键（FR-8.2：未托管字段不参与摘要）。
func ManagedProjection(cfg map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := cfg[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ManagedBlockIn 从文本中提取受管块内容（ReadManagedBlock 的纯文本变体）。
func ManagedBlockIn(text string) (string, bool, error) {
	begin := strings.Index(text, BlockBegin)
	end := strings.Index(text, BlockEnd)
	switch {
	case begin < 0 && end < 0:
		return "", false, nil
	case begin < 0 || end < 0 || end < begin:
		return "", false, fmt.Errorf("managed block markers are incomplete or out of order")
	}
	return strings.Trim(text[begin+len(BlockBegin):end], "\n"), true, nil
}
