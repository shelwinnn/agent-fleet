// Package testenv 构建 KM-46 隔离验证环境：临时 HOME、受控 npm 注册表替身、
// fake npm 可执行与两个互不干扰的全局前缀。仅供 lifecycle 的单元测试与
// cmd/agentmanager-verify 验证程序使用，不得在生产路径引用。
//
// 替身语义只覆盖上游 agentmanager 实际调用的 npm 子命令（install/update/
// uninstall/list/view），并支持注入 list/view/install 失败；包内 package.json
// 的 "bins"（逗号分隔）与 "versionOutput" 是本替身的私有约定，不是 npm 真实
// schema——上游从不读 package.json，只有替身自己读。
package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Env 是一个自包含的隔离环境。Root 之下所有状态都可整体删除。
type Env struct {
	Root     string // 环境根目录
	Home     string // 临时 HOME（$HOME 替换后 os.UserHomeDir 指向这里）
	Prefix   string // 主全局前缀：引擎操作的目标环境
	Prefix2  string // 第二全局前缀：验证“其他实例不受影响”
	BinDir   string // fake npm 所在目录（加入 PATH 首位）
	Registry string // 受控注册表根（FAKE_NPM_REGISTRY）
}

// New 在 root 下创建目录结构与 fake npm 脚本。
func New(root string) (*Env, error) {
	e := &Env{
		Root:     root,
		Home:     filepath.Join(root, "home"),
		Prefix:   filepath.Join(root, "npm-prefix"),
		Prefix2:  filepath.Join(root, "npm-prefix-2"),
		BinDir:   filepath.Join(root, "bin"),
		Registry: filepath.Join(root, "registry"),
	}
	for _, d := range []string{e.Home, e.Prefix, e.Prefix2, e.BinDir, e.Registry} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	// 两个前缀的 bin 目录进 PATH：真实 npm 全局 bin 本就在用户 PATH 上，
	// 替身环境保持同构，FindExecutable 才会找到替身 shim 而不是宿主程序。
	for _, bin := range []string{filepath.Join(e.Prefix, "bin"), filepath.Join(e.Prefix2, "bin")} {
		if err := os.MkdirAll(bin, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(e.BinDir, "npm"), []byte(fakeNpmScript), 0o755); err != nil {
		return nil, err
	}
	return e, nil
}

// Environ 返回隔离环境所需的进程环境变量。顺序：fake npm 目录 → 主前缀
// bin → 第二前缀 bin → 原 PATH。
func (e *Env) Environ() (home, npmPrefix, registry, path string) {
	return e.Home, e.Prefix, e.Registry, strings.Join([]string{
		e.BinDir,
		filepath.Join(e.Prefix, "bin"),
		filepath.Join(e.Prefix2, "bin"),
		os.Getenv("PATH"),
	}, string(os.PathListSeparator))
}

// Publish 向注册表发布一个版本。bins 是该包提供的命令名列表（逗号分隔），
// versionOutput 是这些命令响应 --version 时输出的完整一行。
func (e *Env) Publish(name, version, bins, versionOutput string) error {
	dir := filepath.Join(e.Registry, name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pkg := fmt.Sprintf(`{"name":%q,"version":%q,"bins":%q,"versionOutput":%q}`+"\n",
		name, version, bins, versionOutput)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		return err
	}
	return e.SetLatest(name, version)
}

// SetLatest 改写注册表的 latest 标签（不新增版本）。
func (e *Env) SetLatest(name, version string) error {
	dir := filepath.Join(e.Registry, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(fmt.Sprintf(`{"latest":%q}`+"\n", version)), 0o644)
}

// SetFlag 注入故障：.fail-list / .fail-view / .fail-install / .fail-uninstall。
// list 失败复刻“npm 彻底失败”：非零退出且 stdout 为空。
func (e *Env) SetFlag(flag string, on bool) error {
	p := filepath.Join(e.Registry, flag)
	if !on {
		return os.RemoveAll(p)
	}
	return os.WriteFile(p, nil, 0o644)
}

// InstallDirect 用指定前缀直接调用 fake npm 安装（不经引擎）。用于布置
// 既有安装与第二实例，或安装引擎会拒绝的非法版本（如空版本）。
func (e *Env) InstallDirect(prefix, spec string) error {
	cmd := exec.Command(filepath.Join(e.BinDir, "npm"), "install", "-g", spec)
	cmd.Env = append(os.Environ(),
		"NPM_PREFIX="+prefix,
		"FAKE_NPM_REGISTRY="+e.Registry,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fake npm install %q: %w\n%s", spec, err, out)
	}
	return nil
}

// SeedCodexData 在 HOME 下铺设 ~/.codex 数据替身：配置、凭据、会话历史、
// rules、skill 目录、一条指向外部共享目录的相对软链和一条悬空软链。
// 权限与链接形态是数据保护对比（DirSnapshot）的输入。
func (e *Env) SeedCodexData() error {
	codex := filepath.Join(e.Home, ".codex")
	files := []struct {
		rel  string
		perm os.FileMode
		body string
	}{
		{filepath.Join(".codex", "config.toml"), 0o644,
			"# 用户配置替身\nmodel = \"gpt-5.5\"\napproval_policy = \"on-request\"\n"},
		{filepath.Join(".codex", "auth.json"), 0o600,
			"{\"OPENAI_API_KEY\":\"sk-standin-not-a-secret\"}\n"},
		{filepath.Join(".codex", "AGENTS.md"), 0o644,
			"# 全局规则替身\n"},
		{filepath.Join(".codex", "sessions", "2026-10-27.jsonl"), 0o644,
			"{\"session\":\"standin-history\"}\n"},
		{filepath.Join("shared", "demo", "SKILL.md"), 0o644,
			"---\nname: demo\n---\n共享 skill 替身（.codex 之外）\n"},
	}
	for _, f := range files {
		p := filepath.Join(e.Home, f.rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f.body), f.perm); err != nil {
			return err
		}
	}
	// 相对软链：~/.codex/skills/linked → ../../shared/demo（.codex 之外的真实目标）
	if err := os.MkdirAll(filepath.Join(codex, "skills"), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(filepath.Join("..", "..", "shared", "demo"),
		filepath.Join(codex, "skills", "linked")); err != nil {
		return err
	}
	// 悬空软链：读取目录时必须存活，不允许被生命周期操作清理
	return os.Symlink(filepath.Join("does-not-exist"),
		filepath.Join(codex, "stale-link"))
}
