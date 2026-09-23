// Package bundle 实现 SSH-only 操作包（架构 v1.1.2 §7.4、FR-12.4、FR-12.8、§4.7）。
//
// 布局（§7.4）：
//
//	<staging>/bundle/
//	  manifest.json                # DesiredStateSnapshot + 依赖工件 digest 清单 + bundle 自身 SHA-256
//	  artifacts/skills/<digest>/…  # 仅被引用的 Skill 工件
//
// 本包同时被控制面（构建）与节点（校验/物化）链接，因此放在中立位置：它只依赖
// domain（纯类型）；控制面不因此依赖 agentlocal，节点也不因此依赖 controller。
//
// 安全点（§7.4 安全点 2–6、FR-6.7/A7）：
//  1. 条目 name 必须匹配 ^[A-Za-z0-9._-]+$，digest 必须匹配 ^sha256:[0-9a-f]{64}$；
//  2. 解包路径归一化后必须仍在 artifacts/skills/ 根内，拒绝绝对路径与 "."/".." 元素；
//  3. 内容中的符号链接一律拒绝（SkillPathRejected），不做部分应用；
//  4. bundle 自身摘要：校验 manifest 声明的摘要与实收内容一致；
//  5. 四项预算（§4.7）在构建期与消费期各校验一次，超限即拒绝、不传输。
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 固定名与固定子目录（§7.4：staging 使用与操作无关的固定路径）。
const (
	ManifestName = "manifest.json"
	// DirName 是 bundle 在本地构建与远端 staging 中使用的固定目录名（§4.5/§7.4：
	// 与操作无关的固定路径，避免 bundle-<op-id> 垃圾累积）。
	DirName = "bundle"
	// SkillsDir 是工件根（相对 bundle 根），§7.4 安全点 2 的前缀校验基准。
	SkillsDir = "artifacts/skills"
	// SchemaVersion 是 manifest 结构版本。
	SchemaVersion = "1"
)

// Limits 是 §4.7 的四项工件预算（默认值见 DefaultLimits）。
type Limits struct {
	// MaxArtifactBytes 是单 Skill 工件（归档/上传态）大小上限。
	MaxArtifactBytes int64
	// MaxUnpackedBytes 是单 Skill 工件解包后总大小上限。
	MaxUnpackedBytes int64
	// MaxArtifactFiles 是单 Skill 工件文件数上限。
	MaxArtifactFiles int
	// MaxBundleBytes 是单次操作 bundle 总量上限（含所有被引用工件）。
	MaxBundleBytes int64
}

// DefaultLimits 返回 §4.7 的设计默认值（标注为"待负载验证的设计默认值"，非实测结论）。
func DefaultLimits() Limits {
	return Limits{
		MaxArtifactBytes: 32 << 20,
		MaxUnpackedBytes: 64 << 20,
		MaxArtifactFiles: 4096,
		MaxBundleBytes:   256 << 20,
	}
}

// Artifact 是 manifest 中的一条工件条目（仅被引用的 Skill 工件会进 bundle，§7.4）。
type Artifact struct {
	// Name 是 Skill 资源名（^[A-Za-z0-9._-]+$）。
	Name string `json:"name"`
	// ContentDigest 是快照声明的解析结果内容摘要（domain.SkillDesired.ContentDigest）。
	// 它是"bundle 内的工件确实是该代期望引用的那一份"的绑定依据。
	ContentDigest string `json:"contentDigest"`
	// TreeDigest 是本包对工件目录树计算的规范化摘要（端到端完整性判据，
	// 节点据此校验实收内容未被替换）。
	TreeDigest string `json:"treeDigest"`
	// Path 是工件在 bundle 内的相对路径（artifacts/skills/<treeDigest>）。
	Path string `json:"path"`
	// Size / Files 是构建期实测值，消费期复核。
	Size  int64 `json:"size"`
	Files int   `json:"files"`
}

// Manifest 是 bundle 的清单（§7.4：DesiredStateSnapshot + 依赖工件 digest 清单 +
// bundle 自身 SHA-256）。
type Manifest struct {
	SchemaVersion string `json:"schemaVersion"`
	Machine       string `json:"machine"`
	Generation    int64  `json:"generation"`
	// SnapshotDigest 是控制面快照摘要（domain.DesiredStateSnapshot.Digest）。
	SnapshotDigest string `json:"snapshotDigest"`
	// Snapshot 是自包含完整快照（FR-13.6 同一原则：bundle 路径不依赖控制面网络）。
	Snapshot json.RawMessage `json:"snapshot"`
	// Artifacts 按 Name 排序（确定性，FR-7.2 同一精神）。
	Artifacts []Artifact `json:"artifacts,omitempty"`
	// BundleDigest 是对"除本字段外的规范化 manifest"计算的 SHA-256；
	// 它连同每条 TreeDigest 一起，构成 §7.4 安全点 5 的"实收内容一致"校验。
	BundleDigest string `json:"bundleDigest"`
}

// ArtifactSource 提供被引用 Skill 工件的本地目录（控制面工件缓存，§4.7）。
// 解析器（internal/skills）落地前，本接口就是 bundle 与工件来源之间的接缝。
type ArtifactSource interface {
	// Dir 返回 (name, contentDigest) 对应工件目录的本地绝对路径。
	Dir(name, contentDigest string) (string, error)
}

// ErrArtifactNotFound 表示被引用的工件在来源中不存在（构建期拒绝，不派发）。
var ErrArtifactNotFound = errors.New("bundle: artifact not found in source")

// LocalArtifactSource 以 <root>/<contentDigest>/ 布局读取工件缓存（§4.7 的服务端
// 路径 artifacts/skills/<digest>/）。
type LocalArtifactSource struct{ Root string }

func (s LocalArtifactSource) Dir(name, contentDigest string) (string, error) {
	if err := ValidateDigest(contentDigest); err != nil {
		return "", err
	}
	if err := ValidateName(name); err != nil {
		return "", err
	}
	dir := filepath.Join(s.Root, strings.TrimPrefix(contentDigest, "sha256:"))
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("%w: skill %q digest %s: %v", ErrArtifactNotFound, name, short(contentDigest), err)
	}
	return dir, nil
}

// EmptySource 供不引用任何 Skill 的快照使用（无工件可打包）。
type EmptySource struct{}

func (EmptySource) Dir(name, contentDigest string) (string, error) {
	return "", fmt.Errorf("%w: skill %q", ErrArtifactNotFound, name)
}

// errf 构造带 §30 reason code 的错误（domain.CodedError；节点侧据此产出 reason code）。
func errf(reason, format string, args ...any) error {
	return domain.Coded(reason, format, args...)
}

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ValidateName 校验 Skill 名称（§4.7 第 1 条：不是 "." / ".."，仅 [A-Za-z0-9._-]）。
func ValidateName(name string) error {
	if !nameRe.MatchString(name) || name == "." || name == ".." {
		return errf(domain.ReasonSkillPathRejected, "invalid skill name %q (want ^[A-Za-z0-9._-]+$)", name)
	}
	return nil
}

// ValidateDigest 校验 digest 形态（§4.7 第 1 条）。
func ValidateDigest(d string) error {
	if !digestRe.MatchString(d) {
		return errf(domain.ReasonSkillPathRejected, "invalid digest %q (want sha256:<64 hex>)", d)
	}
	return nil
}

// SafeJoin 归一化后做前缀校验（§4.7 第 2 条：先 Clean 再校验，拒绝绝对路径与
// "."/".." 元素）。root 必须是绝对路径，rel 是 bundle 内的相对路径。
func SafeJoin(root, rel string) (string, error) {
	if rel == "" {
		return "", errf(domain.ReasonSkillPathRejected, "empty bundle-relative path")
	}
	if filepath.IsAbs(rel) || path.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", errf(domain.ReasonSkillPathRejected, "absolute path rejected: %q", rel)
	}
	// Windows 风格分隔符与盘符一律拒绝（bundle 由 POSIX 侧构建）。
	if strings.ContainsRune(rel, '\\') {
		return "", errf(domain.ReasonSkillPathRejected, "backslash in path rejected: %q", rel)
	}
	for _, elem := range strings.Split(rel, "/") {
		if elem == "." || elem == ".." {
			return "", errf(domain.ReasonSkillPathRejected, "path element %q rejected in %q", elem, rel)
		}
	}
	clean := path.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, "../") {
		return "", errf(domain.ReasonSkillPathRejected, "path escapes bundle root: %q", rel)
	}
	if filepath.IsAbs(root) {
		joined := filepath.Join(root, filepath.FromSlash(clean))
		rootClean := filepath.Clean(root) + string(filepath.Separator)
		if !strings.HasPrefix(joined, rootClean) {
			return "", errf(domain.ReasonSkillPathRejected, "path %q escapes root %q", rel, root)
		}
		return joined, nil
	}
	return filepath.Join(root, filepath.FromSlash(clean)), nil
}

// Build 在 dir 下构建 bundle（控制面，§7.4 执行序第 1 步）：
// 快照校验 → 逐条工件取源、量预算、算树摘要、复制 → 写 manifest（含 bundle 摘要）。
// 任一条目违规即整体拒绝（不做部分构建）。
func Build(dir string, snap *domain.DesiredStateSnapshot, src ArtifactSource, limits Limits) (*Manifest, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if snap == nil {
		return nil, errf(domain.ReasonDesiredStateInvalid, "nil snapshot")
	}
	if snap.Machine == "" {
		return nil, errf(domain.ReasonDesiredStateInvalid, "snapshot has no machine")
	}
	if err := ValidateDigest(snap.Digest); err != nil {
		return nil, errf(domain.ReasonDesiredStateInvalid, "snapshot digest %q", snap.Digest)
	}
	// 重新序列化快照：保证 bundle 内快照字节形态确定（同内容同摘要）。
	raw, err := snap.JSON()
	if err != nil {
		return nil, err
	}
	m := &Manifest{
		SchemaVersion:  SchemaVersion,
		Machine:        snap.Machine,
		Generation:     snap.Generation,
		SnapshotDigest: snap.Digest,
		Snapshot:       raw,
	}
	names := make([]string, 0, len(snap.Desired.Skills))
	for name := range snap.Desired.Skills {
		names = append(names, name)
	}
	sort.Strings(names)

	var total int64
	for _, name := range names {
		ref := snap.Desired.Skills[name]
		if err := ValidateName(name); err != nil {
			return nil, err
		}
		if err := ValidateDigest(ref.ContentDigest); err != nil {
			return nil, errf(domain.ReasonSkillPathRejected, "skill %q contentDigest: %s", name, err)
		}
		srcDir, err := src.Dir(name, ref.ContentDigest)
		if err != nil {
			if errors.Is(err, ErrArtifactNotFound) {
				return nil, errf(domain.ReasonSkillDownloadFailed, "skill %q: %v", name, err)
			}
			return nil, err
		}
		stat, err := measureTree(srcDir, limits)
		if err != nil {
			return nil, err
		}
		treeDigest, err := TreeDigest(srcDir)
		if err != nil {
			return nil, err
		}
		artifactPath := path.Join(SkillsDir, strings.TrimPrefix(treeDigest, "sha256:"))
		dst, err := SafeJoin(dir, artifactPath)
		if err != nil {
			return nil, err
		}
		if err := copyTree(srcDir, dst, limits); err != nil {
			return nil, err
		}
		total += stat.bytes
		if total > limits.MaxBundleBytes {
			return nil, errf(domain.ReasonBundleTooLarge,
				"bundle exceeds %d bytes (skill %q pushed it to %d)", limits.MaxBundleBytes, name, total)
		}
		m.Artifacts = append(m.Artifacts, Artifact{
			Name: name, ContentDigest: ref.ContentDigest, TreeDigest: treeDigest,
			Path: artifactPath, Size: stat.bytes, Files: stat.files,
		})
	}
	if err := writeManifest(dir, m); err != nil {
		return nil, err
	}
	return m, nil
}

// Verify 校验一个已落地的 bundle 目录（节点侧，§7.4：先做路径/名称/digest 校验，
// 再校验全部工件 digest）：
//  1. manifest 可解析、schema 版本受支持、必填字段齐备；
//  2. 名称与 digest 形态合规（§4.7 第 1 条，服务端校验过的在消费侧再校验一次）；
//  3. bundle 自身摘要与 manifest 实收内容一致（安全点 5）；
//  4. 每条工件的路径在 artifacts/skills/ 根内、无符号链接逃逸、实测树摘要与声明一致；
//  5. 四项预算复核。
//
// 任一条违规 → 返回带 §30 reason 的错误，调用方必须拒绝整包（不做部分应用）。
func Verify(dir string, limits Limits) (*Manifest, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, errf(domain.ReasonDesiredStateInvalid, "manifest unreadable: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errf(domain.ReasonDesiredStateInvalid, "manifest unparseable: %v", err)
	}
	if m.SchemaVersion != SchemaVersion {
		return nil, errf(domain.ReasonDesiredStateInvalid, "unsupported bundle schemaVersion %q", m.SchemaVersion)
	}
	if m.Machine == "" {
		return nil, errf(domain.ReasonDesiredStateInvalid, "manifest has no machine")
	}
	// 摘要先于语义：先证明"这份 manifest 就是被构建并声明过的那一份"（安全点 5），
	// 再解释其内容。否则被篡改的 manifest 可能被更早的语义错误掩盖成别的原因。
	declared := m.BundleDigest
	recomputed, err := manifestDigest(&m)
	if err != nil {
		return nil, err
	}
	if declared == "" || declared != recomputed {
		return nil, errf(domain.ReasonSkillDigestMismatch,
			"bundle digest mismatch: declared=%s recomputed=%s", short(declared), short(recomputed))
	}
	if err := ValidateDigest(m.SnapshotDigest); err != nil {
		return nil, errf(domain.ReasonDesiredStateInvalid, "manifest snapshotDigest: %v", err)
	}
	if len(m.Snapshot) == 0 {
		return nil, errf(domain.ReasonDesiredStateInvalid, "manifest carries no snapshot")
	}
	snap, err := domain.ParseSnapshot(m.Snapshot)
	if err != nil {
		return nil, errf(domain.ReasonDesiredStateInvalid, "%v", err)
	}
	if snap.Machine != m.Machine || snap.Generation != m.Generation || snap.Digest != m.SnapshotDigest {
		return nil, errf(domain.ReasonDesiredStateInvalid,
			"manifest/snapshot mismatch (manifest machine=%s gen=%d digest=%s)",
			m.Machine, m.Generation, short(m.SnapshotDigest))
	}
	var total int64
	seen := map[string]bool{}
	for _, a := range m.Artifacts {
		if err := ValidateName(a.Name); err != nil {
			return nil, err
		}
		if seen[a.Name] {
			return nil, errf(domain.ReasonSkillPathRejected, "duplicate artifact entry %q", a.Name)
		}
		seen[a.Name] = true
		if err := ValidateDigest(a.ContentDigest); err != nil {
			return nil, err
		}
		if err := ValidateDigest(a.TreeDigest); err != nil {
			return nil, err
		}
		wantPath := path.Join(SkillsDir, strings.TrimPrefix(a.TreeDigest, "sha256:"))
		if a.Path != wantPath {
			return nil, errf(domain.ReasonSkillPathRejected,
				"artifact %q path %q is not %q", a.Name, a.Path, wantPath)
		}
		abs, err := SafeJoin(dir, a.Path)
		if err != nil {
			return nil, err
		}
		stat, err := measureTree(abs, limits)
		if err != nil {
			return nil, err
		}
		if stat.files != a.Files || stat.bytes != a.Size {
			return nil, errf(domain.ReasonSkillDigestMismatch,
				"artifact %q size/files mismatch: manifest=%d/%d actual=%d/%d",
				a.Name, a.Size, a.Files, stat.bytes, stat.files)
		}
		got, err := TreeDigest(abs)
		if err != nil {
			return nil, err
		}
		if got != a.TreeDigest {
			return nil, errf(domain.ReasonSkillDigestMismatch,
				"artifact %q tree digest mismatch: declared=%s actual=%s", a.Name, short(a.TreeDigest), short(got))
		}
		total += stat.bytes
		if total > limits.MaxBundleBytes {
			return nil, errf(domain.ReasonBundleTooLarge, "bundle exceeds %d bytes", limits.MaxBundleBytes)
		}
	}
	// 快照里被引用的 Skill 必须逐条在 bundle 内（否则节点会缺工件而"部分应用"）。
	for name := range snap.Desired.Skills {
		if !seen[name] {
			return nil, errf(domain.ReasonSkillDownloadFailed,
				"snapshot references skill %q but the bundle carries no artifact for it", name)
		}
	}
	return &m, nil
}

// InstallArtifacts 把校验过的工件物化到节点规范缓存。
//
// 目标路径必须是**适配器契约**所要求的位置（agentlocal/adapter/kit.SkillCacheDir）：
// <cacheRoot>/<name>/<contentDigest 的冒号转横线形式>，因为适配器物化 Skill 软链时
// 从软链目标的目录名反读 digest 与期望状态比对（kit.ReadSkillDigest）。用 treeDigest
// 命名会让"节点物化的 Skill"与"期望里的 Skill digest"对不上，从而永远漂移。
//
// 完整性不受影响：bundle 校验阶段已按 treeDigest 逐条核对实收内容（§7.4 安全点 5），
// 物化后再复核一次。幂等：目标已存在且树摘要一致则跳过，不做任何写入。
func InstallArtifacts(bundleDir, cacheRoot string, m *Manifest, limits Limits) ([]string, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	var installed []string
	for _, a := range m.Artifacts {
		src, err := SafeJoin(bundleDir, a.Path)
		if err != nil {
			return installed, err
		}
		dst, err := skillCachePath(cacheRoot, a.Name, a.ContentDigest)
		if err != nil {
			return installed, err
		}
		if got, err := TreeDigest(dst); err == nil && got == a.TreeDigest {
			installed = append(installed, dst)
			continue
		}
		if err := os.RemoveAll(dst); err != nil {
			return installed, errf(domain.ReasonConfigWriteFailed, "clear skill cache %s: %v", dst, err)
		}
		if err := copyTree(src, dst, limits); err != nil {
			return installed, err
		}
		got, err := TreeDigest(dst)
		if err != nil {
			return installed, err
		}
		if got != a.TreeDigest {
			return installed, errf(domain.ReasonSkillDigestMismatch,
				"materialized artifact %q digest mismatch: %s != %s", a.Name, short(got), short(a.TreeDigest))
		}
		installed = append(installed, dst)
	}
	return installed, nil
}

// skillCachePath 计算节点侧 Skill 工件缓存路径（与适配器 kit.SkillCacheDir 一致）。
func skillCachePath(cacheRoot, name, contentDigest string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	if err := ValidateDigest(contentDigest); err != nil {
		return "", err
	}
	dst := filepath.Join(cacheRoot, name, strings.ReplaceAll(contentDigest, ":", "-"))
	root := filepath.Clean(cacheRoot) + string(filepath.Separator)
	if !strings.HasPrefix(dst, root) {
		return "", errf(domain.ReasonSkillPathRejected, "cache path %q escapes %q", dst, cacheRoot)
	}
	return dst, nil
}

// TreeDigest 计算目录树的规范化 SHA-256（§4.7：固定文件排序、规范化权限位与换行，
// 保证同内容同 digest）。条目 = 排序后的 "mode:relpath:sha256:<hex>\n"，目录本身不计入。
func TreeDigest(root string) (string, error) {
	h := sha256.New()
	err := walkTree(root, func(rel string, info fs.FileInfo, abs string) error {
		sum, err := fileDigest(abs)
		if err != nil {
			return err
		}
		// 权限位只保留 0644/0755 两档的规范化结果（可执行/不可执行），
		// 避免因 umask 差异产生不同摘要。
		mode := "100644"
		if info.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
		fmt.Fprintf(h, "%s:%s:%s\n", mode, rel, sum)
		return nil
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// walkTree 以确定顺序遍历目录树，**不跟随符号链接**（遇到即拒绝）。
func walkTree(root string, fn func(rel string, info fs.FileInfo, abs string) error) error {
	entries := []string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return errf(domain.ReasonSkillPathRejected, "symlink rejected in artifact tree: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return errf(domain.ReasonSkillPathRejected, "non-regular file rejected in artifact tree: %s", p)
		}
		entries = append(entries, p)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, p := range entries {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if err := fn(filepath.ToSlash(rel), info, p); err != nil {
			return err
		}
	}
	return nil
}

type treeStat struct {
	files int
	bytes int64
}

// measureTree 量取工件树的大小与文件数并施加 §4.7 的逐工件预算。
func measureTree(root string, limits Limits) (treeStat, error) {
	var st treeStat
	err := walkTree(root, func(rel string, info fs.FileInfo, abs string) error {
		st.files++
		st.bytes += info.Size()
		if st.files > limits.MaxArtifactFiles {
			return errf(domain.ReasonArtifactTooLarge, "artifact %s exceeds %d files", root, limits.MaxArtifactFiles)
		}
		if st.bytes > limits.MaxArtifactBytes {
			return errf(domain.ReasonArtifactTooLarge,
				"artifact %s exceeds %d bytes (upload-state budget)", root, limits.MaxArtifactBytes)
		}
		if st.bytes > limits.MaxUnpackedBytes {
			return errf(domain.ReasonArtifactTooLarge,
				"artifact %s exceeds %d bytes (unpacked budget)", root, limits.MaxUnpackedBytes)
		}
		return nil
	})
	return st, err
}

// copyTree 逐文件复制并保留可执行位；目标已存在时先删除（构建幂等）。
func copyTree(src, dst string, limits Limits) error {
	if _, err := os.Stat(src); err != nil {
		return errf(domain.ReasonSkillDownloadFailed, "artifact source %s: %v", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return errf(domain.ReasonConfigWriteFailed, "create %s: %v", dst, err)
	}
	return walkTree(src, func(rel string, info fs.FileInfo, abs string) error {
		target, err := SafeJoin(dst, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		in, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func fileDigest(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// manifestDigest 计算"除 BundleDigest 外"的规范化 manifest 摘要（bundle 自身摘要）。
func manifestDigest(m *Manifest) (string, error) {
	cp := *m
	cp.BundleDigest = ""
	b, err := desiredstate.CanonicalJSON(&cp)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func writeManifest(dir string, m *Manifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	d, err := manifestDigest(m)
	if err != nil {
		return err
	}
	m.BundleDigest = d
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ManifestName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ManifestName))
}

func short(d string) string {
	if len(d) > 19 {
		return d[:19] + "…"
	}
	return d
}
