// Package dsh 是 DeepSeek Harness（官方 npm `@deepseek-ai/dsh`，可执行 `dsh`）家族适配器
// （批次二片 D，KM-36）。运行时来源与契约证据见 docs/adapters-batch-2.md §2（0/1 片
// KM-32 的接入前确认）与 §11（本片验收记录）；复核时点本机实测版本 0.1.7-rc.1。
//
// 本片按裁决 (c) 接入：只落 **version 探测 + rules + skills**，`mcp` 声明 `unsupported`，
// `modelProvider` 声明 `unverified`（§11 的两条前置复验结论）。两条拒绝都发生在
// **任何写入之前**（Validate 阶段 1 与 Apply 的写前 pre-flight）。
//
// 版本探测只用 `dsh --version` / `-V`：输出**裸 semver**（允许 prerelease 后缀），
// 形态不符即显式报错，不猜测。官方 README 明写 developer preview 且
// "THERE WILL BE COMPATIBILITY-BREAKING CHANGES"，故按复核时点**实际安装的版本**登记
// `VerifiedVersions`，未知版本显式失败。`--dump-config` / `--dump-default-config` /
// `--dump-config-schema` 会初始化缺失的 profile 文件（官方原文 "A dump initializes
// missing profile files"），**不得作为健康检查**；本适配器的健康检查只执行
// `dsh --version`。
//
// 受管面（§2.2/§11）：
//
//	$DSH_HOME/AGENTS.md     rules 受管标记块（块外逐字节保留）
//	$DSH_HOME/skills/<name>.md   Skill 软链（指向节点规范技能缓存；扁平 `*.md` 形态）
//
// 明确**不**受管：`$DSH_HOME/settings.yaml`（0.1.7-rc.1 起是**一次性 legacy 导入通道**，
// 见 §11.2）、`$DSH_HOME/cordis.patch.yml` 与 `profiles/*/cordis.patch.yml` 的 patch 行
// （batch-2 §6 缺口 1 的行级所有权，属 MCP/modelProvider 的写入面，本片不实现）、
// `$DSH_HOME/.credentials.yaml`、`$DSH_HOME/.env`、调用目录 `.env`（凭据链，绝不读值）、
// `profiles/*/package.json` 的 `dsh.profile.bundles`（属 `dsh plugin` 插件管理）。
//
// `~/.dsh/` 下的 `dsh-mcp-catalog*`、`dsh-mcp-manager` 设置段与 `@wingsky-1/` 是
// **第三方社区插件**产物，不是 core 契约，不写进本适配器（§2.3；批次一对 ZCode 的教训）。
package dsh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/kit"
)

const (
	// ID 是家族标识（矩阵统一名称）。
	ID = "dsh"
	// SchemaVersion 是本适配器受管 schema 版本。
	SchemaVersion = "dsh-home-v1"
	// binaryName 是可执行程序名。
	binaryName = "dsh"
	// keyRules 是 rules 受管单元的 plan 键。
	keyRules = "rules.global"
	// rulesRel 是 rules 受管文件相对 home 根的路径（reconciler 备份契约）。
	rulesRel = ".dsh/AGENTS.md"
)

// versionArgs 是版本探测参数（长/短旗标均输出裸 semver）。
var versionArgs = []string{"--version"}

var (
	// versionRe 严格匹配 `dsh --version` 的**整行**裸 semver（允许 prerelease 后缀）。
	// 形态不符（带前缀、非三段、多余内容）即显式报错，不猜测。
	versionRe = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+(?:[-.+][0-9A-Za-z.-]+)?)$`)
	// skillNameRe 是 Skill 文件名（软链末端 `<name>.md` 的 `<name>`）。
	skillNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// parseVersion 严格解析 `dsh --version` 输出；形态不符返回 ""（可区分"未装"）。
func parseVersion(out string) string {
	m := versionRe.FindStringSubmatch(kit.FirstLine(out))
	if m == nil {
		return ""
	}
	return m[1]
}

// VersionProbe 执行 `dsh --version`（home 为注入的 HOME 根；实现负责把 HOME 与
// DSH_HOME 都指到该根，绝不落到进程默认的 ~/.dsh，护栏 #12）。
type VersionProbe func(ctx context.Context, home string) (string, error)

// Adapter 是 DeepSeek Harness 适配器。Probe/GOOS 可注入以便测试。
type Adapter struct {
	Probe VersionProbe
	GOOS  string
}

func New() *Adapter { return &Adapter{} }

func (a *Adapter) ID() string { return ID }

func (a *Adapter) goos() string {
	if a.GOOS != "" {
		return a.GOOS
	}
	return runtime.GOOS
}

// probeVersion 探测已装版本：注入桩优先，默认实现把 HOME/DSH_HOME 钉到注入根。
func (a *Adapter) probeVersion(ctx context.Context, home string) (string, error) {
	probe := a.Probe
	if probe == nil {
		probe = defaultVersionProbe
	}
	out, err := probe(ctx, home)
	if err != nil {
		if kit.IsNotInstalled(err) {
			return "", fmt.Errorf("%w: %s", kit.ErrNotInstalled, binaryName)
		}
		return "", fmt.Errorf("probe %s: %w", binaryName, err)
	}
	v := parseVersion(out)
	if v == "" {
		return "", fmt.Errorf("probe %s: cannot parse version from output %q", binaryName, strings.TrimSpace(out))
	}
	return v, nil
}

// defaultVersionProbe 直执行 `dsh --version`（argv，不经过 sh -c，护栏 #1），并把
// HOME/DSH_HOME 都钉到注入根。`dsh --version` 零副作用（§11.2 P2a：临时 HOME 下不创建
// 任何文件），是唯一可直接使用的健康探测。
func defaultVersionProbe(ctx context.Context, home string) (string, error) {
	cmd := exec.CommandContext(ctx, binaryName, versionArgs...)
	cmd.Env = dshEnv(home)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// dshEnv 返回 os.Environ() 但把 HOME/DSH_HOME 钉到注入根。
func dshEnv(home string) []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "DSH_HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home, "DSH_HOME="+configRoot(home))
}

// Capabilities 声明逐能力支持状态与证据。§2.5 的未验证项逐条落进对应 Reason/Evidence。
func (a *Adapter) Capabilities() []adapter.CapabilityDecl {
	return adapter.SortDecls([]adapter.CapabilityDecl{
		{
			Capability: adapter.CapabilityVersion, State: adapter.SupportSupported,
			VerifiedVersions: "DeepSeek Harness 0.1.7-rc.1 (linux/amd64)",
			Reason: "版本**探测**已验证：`dsh --version` / `dsh -V` 输出**裸 semver**（允许 prerelease 后缀），" +
				"按整行严格匹配，形态不符显式报错；`--version` 零副作用（临时 HOME 下不创建任何文件）。" +
				"官方 README 明写 developer preview 且 \"THERE WILL BE COMPATIBILITY-BREAKING CHANGES\"，" +
				"故 `VerifiedVersions` 只登记复核时点**实际安装**的 0.1.7-rc.1，未知版本在 Apply(version) 显式失败。" +
				"安装/升级（`npx @deepseek-ai/dsh` / `pnpm add`）不在本片，**「按 latest 接入」不成立**" +
				"（复核时点 dist-tags latest=0.1.7-rc.2，仍是预发布）。" +
				"未验证：WSL 未被官方 support-matrix 提及；Windows 走 semaphore 实现而非 Landlock，" +
				"与 Linux 沙箱能力不等价；macOS 无本机实测；桌面端（Electron）`desktop` 保留 profile 的配置契约未验证",
			Evidence: "本机实测 `dsh --version` 与 `dsh -V` → `0.1.7-rc.1`（退出 0）；" +
				"npm dist-tags `{\"latest\":\"0.1.7-rc.2\",\"alpha\":\"0.1.7-alpha.2\",\"next\":\"0.1.7-rc.2\"}`；" +
				"官方 https://deepseek-harness.github.io/deepseek-harness/ 与仓库 README §Developer preview；" +
				"官方 `native/system/docs/support-matrix.md`（linux-x64/arm64、darwin-x64/arm64；Windows 无 Landlock）",
			Paths: []string{".dsh"},
		},
		{
			Capability: adapter.CapabilityModelProvider, State: adapter.SupportUnverified,
			VerifiedVersions: "",
			Reason: "**未验证**：本片按 0/1 片 §2.4 的裁决 (c) 把 modelProvider 交由前置复验决定，复核结论为不成立。" +
				"0.1.7-rc.1 起 `$DSH_HOME/settings.yaml` **不再是活的设置平面**：核心 `@deepseek-ai/dsh-settings` 只在" +
				"启动时做**一次性 legacy 导入**（读 `$DSH_HOME/settings.yaml`、先改名 `.imported`、再把各段经 " +
				"`@deepseek-ai/dsh-config-editor` 写入**当前 profile** 的 `cordis.patch.yml`），核心自身不创建该文件。" +
				"活的用户可写设置面是**当前 profile** 的 patch 列表数组，而 `$DSH_HOME/cordis.patch.yml`（home 级 patch）" +
				"与 `--patch` 覆盖又压过 profile patch（config-editor `edit()` 对被覆盖的写入直接拒绝）。" +
				"但 profile 名在 home 根不可知，且 patch 行插删/整 config 替换正是 MCP 同款的**新写入形态**（batch-2 §6 缺口 1），" +
				"不属本片；故本片**不写任何 modelProvider 键**，Validate 在写入前以 `ValidateError`(unverified) 拒绝该能力请求。" +
				"未验证：target profile 的发现与 patch 行级所有权、`llm-pi-ai` provider route 的 namespace 与注册条件" +
				"（本机 provider 走内置 `deepseek-official`，无 `llm-pi-ai` 段）、`${env:VAR}` 官方仍列为 **deferred**、" +
				"`settings.yaml` 并发写协议未做并发实测",
			Evidence: "§11.2 两条前置复验的**命令与输出原文**（临时 HOME，未触碰真实 `~/.dsh`）：" +
				"预置 `$DSH_HOME/settings.yaml` 后启动 web profile → 文件被改名为 `settings.yaml.imported`，" +
				"`agent-default-model` 段落到 `$DSH_HOME/profiles/web/cordis.patch.yml` 的 `- id: agent-default-model` 行；" +
				"未知名段未被导入。安装树源码 `@deepseek-ai/dsh-settings` `importLegacyDocument()` 与 " +
				"`@deepseek-ai/dsh-config-editor` `documentPath`/`edit()`（写 profile patch、home patch 覆盖即拒写）",
			Paths: []string{".dsh/profiles/<name>/cordis.patch.yml", ".dsh/cordis.patch.yml", ".dsh/settings.yaml"},
		},
		{
			Capability: adapter.CapabilityMCP, State: adapter.SupportUnsupported,
			VerifiedVersions: "",
			Reason: "本片明确不接：dsh 没有 MCP JSON 配置文件，MCP server 是组合平面 `@deepseek-ai/dsh-mcp-client` 的" +
				"**patch 行**（`insert` 到 `cordis.patch.yml` 的顶层数组），所有权是**行级**，且官方明说 patch " +
				"\"replaces the targeted row's complete `config` value rather than deep-merging keys\"。" +
				"该行级插删 + 行级回退 + 按行投影是批次一任何 kit 都不适用的新形态（batch-2 §6 缺口 1），" +
				"值得单独一片设计，故本片**不实现** patch 行插删，Validate 在写入前拒绝 MCP 请求。" +
				"另：`${env:VAR}` 值间接引用官方列为 **deferred**（记 `unsupported`，不是 `unverified`）。" +
				"再次评估触发条件见 §2.4/§5.5：稳定版发布、或 `$defs.patchList` 被固化为文档契约、或用户改判",
			Evidence: "官方 `docs/config-catalog.md` `@deepseek-ai/dsh-mcp-client` 段（transport/serverName/command/args/env/cwd/toolCallTimeoutMs）；" +
				"官方 CLI 参考 \"no MCP server is enabled by default\"；本机实测 `~/.dsh/` 下**无** `mcp.json`，" +
				"现有 MCP 形态来自第三方插件（见 §2.3）",
			Paths: []string{".dsh/cordis.patch.yml", ".dsh/profiles/<name>/cordis.patch.yml"},
		},
		{
			Capability: adapter.CapabilitySkills, State: adapter.SupportSupported,
			VerifiedVersions: "DeepSeek Harness 0.1.7-rc.1",
			Reason: "Fleet 自有条目写为 `$DSH_HOME/skills/<name>.md` **扁平文件软链**（指向节点规范技能缓存），" +
				"与批次一 `<name>/SKILL.md` 目录形态不同；官方 provider 明确支持 flat `<name>.md`。目录内既有条目" +
				"（本机为 `feishu-cli.md`/`workflow-one.md` 等真实文件，且 user DSH root 会跳过 `.system` 子目录）" +
				"**不点名即不触碰**；目标已存在但不是 agent-fleet 软链时显式失败（护栏 #3）。" +
				"缓存未物化或 digest 形状不符（非 `sha256:<64 hex>`）时在**写入前**显式失败，不写坏链、不静默跳过。" +
				"未验证：项目根（`<projectRoot>/.dsh/skills`、`<projectRoot>/.agents/skills`）与 `customSkillDirs`" +
				"不在本片受管范围（归一化 skills 契约不携带外部技能根 → 无可写来源）；`$DSH_AGENTS_HOME` 根未纳入；" +
				"macOS/Windows/WSL 未实测",
			Evidence: "本机实测 `ls ~/.dsh/skills/` → `feishu-cli.md`、`workflow-one.md`（扁平 `*.md`，带 `name`/`description` frontmatter，另带 `<!-- managed-by: … -->` 注释）；" +
				"官方 `@deepseek-ai/dsh-skill-filesystem` README §Skill format（`<name>/SKILL.md` **或** flat `<name>.md`；嵌套 `**/SKILL.md` 不发现）" +
				"与 §Roots and priority（rank 400 `user-dsh` = `<dshHome>/skills`，跳过 `.system`）",
			Paths: []string{".dsh/skills"},
		},
		{
			Capability: adapter.CapabilityRules, State: adapter.SupportSupported,
			VerifiedVersions: "DeepSeek Harness 0.1.7-rc.1",
			Reason: "`$DSH_HOME/AGENTS.md` 是 `@deepseek-ai/dsh-agent-instructions` 的**固定用户级全局指令文件**" +
				"（官方 catalog：\"Harness home containing the fixed user-global `AGENTS.md`\"）。Fleet 只写自有受管标记块，" +
				"块外内容逐字节保留（护栏 #3）；该路径为软链时写穿软链。文件不存在时按批次一惯例追加受管块。" +
				"未验证：本片未对 AGENTS.md 的运行时加载做端到端交互验证（只验证文件形态与块读写）；" +
				"项目级 `.dsh/AGENTS.md` / `.agents/` 等其它指令文件不在 home 受管面",
			Evidence: "本机实测 `ls -la ~/.dsh/AGENTS.md` → 1728 B 纯 Markdown 全局指令文件；" +
				"官方 `docs/config-catalog.md` `@deepseek-ai/dsh-agent-instructions` 段（默认 `$DSH_HOME` 或 `~/.dsh`）",
			Paths: []string{".dsh/AGENTS.md"},
		},
	})
}

// Validate 是阶段 1 的适配器侧前置校验（任何写入之前）。
func (a *Adapter) Validate(_ context.Context, home string, desired adapter.AgentDesiredState) error {
	if a.goos() != "linux" {
		return fmt.Errorf("agent %q: OS %q is unverified (only linux/amd64 verified on DeepSeek Harness 0.1.7-rc.1)", ID, a.goos())
	}
	// mcp 声明 unsupported、modelProvider 声明 unverified：请求即在此显式拒绝。
	if err := adapter.CheckCapabilities(ID, a.Capabilities(), desired); err != nil {
		return err
	}
	cfg := adapter.ConfigMap(desired)
	if len(desired.Config) != 0 && len(cfg) == 0 {
		return fmt.Errorf("agent %q: config is not a JSON object: %s", ID, string(desired.Config))
	}
	// 受管标记块不完整（只有开始没有结束）时先失败，避免写坏用户文件。
	if _, _, err := kit.ReadManagedBlock(a.rulesPath(home)); err != nil {
		return fmt.Errorf("agent %q: %w", ID, err)
	}
	for name, s := range desired.Skills {
		if !validSkillName(name) {
			return fmt.Errorf("agent %q: skill name %q must match %s and not be %q/%q", ID, name, skillNameRe, ".", "..")
		}
		// digest 是缓存目录分量：形状不符会在写入时把软链种到任意已存在目录（复核 B1 同款）。
		if !kit.ValidDigest(s.ContentDigest) {
			return fmt.Errorf("agent %q: skill %q contentDigest %q must match %s", ID, name, s.ContentDigest, kit.DigestRe)
		}
	}
	return nil
}

// validSkillName 防目录穿越：`filepath.Join(root, "..")` 会把目标 Clean 到 root 之外。
func validSkillName(name string) bool {
	return skillNameRe.MatchString(name) && name != "." && name != ".."
}

// Detect 探测已装/未装与版本（矩阵验收 1：程序缺失与版本不可解析必须可区分）。
func (a *Adapter) Detect(ctx context.Context, home string) (adapter.DetectedAgent, error) {
	d := adapter.DetectedAgent{Family: ID, ConfigPath: configRoot(home), SchemaVer: SchemaVersion}
	v, err := a.probeVersion(ctx, home)
	switch {
	case errors.Is(err, kit.ErrNotInstalled):
		return d, nil // 未安装：Installed=false，不是错误
	case err != nil:
		return d, err // 版本不可解析：显式错误（不伪装成"未安装"）
	}
	d.Installed, d.Version = true, v
	return d, nil
}

// Inventory 产出 ADR-1 双侧投影（只读受管面：rules 受管块 + skills 软链摘要）。
func (a *Adapter) Inventory(ctx context.Context, home string, desired adapter.AgentDesiredState) (adapter.AgentObservedState, error) {
	desiredProj := a.desiredProjection(desired)
	observedProj, err := a.observedProjection(home, desired)
	if err != nil {
		return adapter.AgentObservedState{}, err
	}
	return adapter.AgentObservedState{
		Family:                   ID,
		Version:                  a.probeVersionQuiet(ctx, home),
		ManagedProjection:        observedProj,
		DesiredProjectionDigest:  kit.ProjectionDigest(desiredProj),
		ObservedProjectionDigest: kit.ProjectionDigest(observedProj),
		CanonicalizationVersion:  adapter.ProjectionCanonicalizationVersion,
		ConfigPath:               configRoot(home),
	}, nil
}

func (a *Adapter) probeVersionQuiet(ctx context.Context, home string) string {
	v, err := a.probeVersion(ctx, home)
	if err != nil {
		return ""
	}
	return v
}

func (a *Adapter) desiredProjection(desired adapter.AgentDesiredState) map[string]any {
	proj := map[string]any{}
	if content := kit.RulesContent(desired.Rules); content != "" {
		proj["rules"] = content
	}
	if len(desired.Skills) != 0 {
		skills := map[string]any{}
		for name, s := range desired.Skills {
			skills[name] = s.ContentDigest
		}
		proj["skills"] = skills
	}
	return proj
}

func (a *Adapter) observedProjection(home string, desired adapter.AgentDesiredState) (map[string]any, error) {
	proj := map[string]any{}
	if kit.RulesContent(desired.Rules) != "" {
		content, _, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return nil, fmt.Errorf("dsh: %w", err)
		}
		proj["rules"] = content
	}
	if len(desired.Skills) != 0 {
		skills := map[string]any{}
		for name := range desired.Skills {
			skills[name] = kit.ReadSkillDigest(a.skillPath(home, name))
		}
		proj["skills"] = skills
	}
	return proj, nil
}

// Plan 比较两侧投影产出变更（阶段 3；空计划即幂等）。
func (a *Adapter) Plan(_ context.Context, _ string, desired adapter.AgentDesiredState, observed adapter.AgentObservedState) ([]adapter.Change, error) {
	desiredProj := a.desiredProjection(desired)
	observedProj := observed.ManagedProjection
	if observedProj == nil {
		observedProj = map[string]any{}
	}
	var changes []adapter.Change
	if desired.Version != "" && observed.Version != desired.Version {
		changes = append(changes, adapter.Change{Family: ID, Step: "version", Key: "version",
			From: observed.Version, To: desired.Version})
	}
	if !equal(desiredProj["rules"], observedProj["rules"]) {
		changes = append(changes, adapter.Change{Family: ID, Step: "rules", Key: keyRules,
			From: observedProj["rules"], To: desiredProj["rules"]})
	}
	changes = append(changes, kit.DiffMapStep(ID, "skills", "skill:", desiredProj["skills"], observedProj["skills"])...)
	return changes, nil
}

// Apply 执行变更（阶段 5–9）：rules 受管块、Skill 软链。空变更零写入（幂等）。
// 任何一步失败都零写入：pre-flight 先校验全部待写对象。
func (a *Adapter) Apply(ctx context.Context, home string, desired adapter.AgentDesiredState, changes []adapter.Change) error {
	if len(changes) == 0 {
		return nil // 空计划零写入（幂等）
	}
	byStep := map[string][]adapter.Change{}
	for _, c := range changes {
		byStep[c.Step] = append(byStep[c.Step], c)
	}
	// ---- 写前 pre-flight：任何一步失败都零写入 ----
	if err := a.preflight(home, desired, byStep); err != nil {
		return err
	}
	if len(byStep["version"]) != 0 {
		v := a.probeVersionQuiet(ctx, home)
		if v != desired.Version {
			return fmt.Errorf("dsh: version %s is required but %q is installed and the CLI installer "+
				"(npx/pnpm, batch-2 §20.2) is not wired in this slice", desired.Version, v)
		}
	}
	if len(byStep["rules"]) != 0 {
		if err := kit.WriteManagedBlock(a.rulesPath(home), kit.RulesContent(desired.Rules)); err != nil {
			return fmt.Errorf("dsh: rules: %w", err)
		}
	}
	for _, c := range byStep["skills"] {
		name := strings.TrimPrefix(c.Key, "skill:")
		digest := fmt.Sprintf("%v", c.To)
		if err := kit.EnsureSkillLink(a.skillPath(home, name), name, digest, kit.SkillCacheDir(home, name, digest)); err != nil {
			return fmt.Errorf("dsh: %w", err)
		}
	}
	return nil
}

// preflight 校验本次计划里每个待写对象，并再次拒绝未支持/未验证能力（陈旧计划纵深防御），
// 让"rules 先落盘、随后 skill 报穿越错"这类部分写入不可能发生。
func (a *Adapter) preflight(home string, desired adapter.AgentDesiredState, byStep map[string][]adapter.Change) error {
	// 绕过 Validate 的陈旧计划同样在写第一个字节之前被拒。
	if err := adapter.CheckCapabilities(ID, a.Capabilities(), desired); err != nil {
		return err
	}
	if _, _, err := kit.ReadManagedBlock(a.rulesPath(home)); err != nil {
		return fmt.Errorf("dsh: %w", err)
	}
	for _, c := range byStep["skills"] {
		name := strings.TrimPrefix(c.Key, "skill:")
		if !validSkillName(name) {
			return fmt.Errorf("dsh: refusing skill name %q (traversal guard)", name)
		}
		digest := fmt.Sprintf("%v", c.To)
		if !kit.ValidDigest(digest) {
			return fmt.Errorf("dsh: refusing skill %q digest %q (want %s)", name, digest, kit.DigestRe)
		}
	}
	return nil
}

// HealthCheck（阶段 10）：只用 `dsh --version` 做版本探测（dump 类命令会初始化 profile
// 文件，不得作只读健康检查），再校验 rules 受管块与 Skill 软链 digest。
func (a *Adapter) HealthCheck(ctx context.Context, home string, desired adapter.AgentDesiredState) error {
	if desired.Version != "" {
		v, err := a.probeVersion(ctx, home)
		if err != nil {
			return fmt.Errorf("dsh: health: version probe failed: %w", err)
		}
		if v != desired.Version {
			return fmt.Errorf("dsh: health: installed version %s != desired %s", v, desired.Version)
		}
	}
	if content := kit.RulesContent(desired.Rules); content != "" {
		got, ok, err := kit.ReadManagedBlock(a.rulesPath(home))
		if err != nil {
			return fmt.Errorf("dsh: health: %w", err)
		}
		if !ok || got != content {
			return fmt.Errorf("dsh: health: rules managed block in %s is missing or differs", a.rulesPath(home))
		}
	}
	for name, s := range desired.Skills {
		if got := kit.ReadSkillDigest(a.skillPath(home, name)); got != s.ContentDigest {
			return fmt.Errorf("dsh: health: skill %q link digest is %q, want %q", name, got, s.ContentDigest)
		}
	}
	return nil
}

// ---- 路径（相对注入的 home 根，护栏 #12）----

func configRoot(home string) string { return filepath.Join(home, ".dsh") }
func (a *Adapter) rulesPath(home string) string {
	return filepath.Join(configRoot(home), "AGENTS.md")
}

// skillPath 是扁平 `*.md` 形态的 Skill 目的地（§2.1/§11.3）。
func (a *Adapter) skillPath(home, name string) string {
	return filepath.Join(configRoot(home), "skills", name+".md")
}

// ---- 受管文件声明（reconciler 的备份/恢复契约，§5.3）----

// ManagedFiles 声明本适配器写入的文件（相对 home）。Skill 软链由路径体现，
// 不列入整文件备份（同批次一口径）。
func (a *Adapter) ManagedFiles(_ string) ([]string, error) {
	return []string{rulesRel}, nil
}

// ExtractManaged 提取受管块内容（其余内容是用户 AGENTS.md，绝不进备份）。
func (a *Adapter) ExtractManaged(content []byte) (map[string]any, error) {
	if block, ok, err := kit.ManagedBlockIn(string(content)); err == nil && ok {
		return map[string]any{"agentFleetRules": block}, nil
	}
	return map[string]any{}, nil
}

// MergeManaged 受管块级回退（§5.3 契约 3）：只还原受管标记块，保留块外（未托管）内容。
// 未知受管文件显式失败，不静默跳过。
func (a *Adapter) MergeManaged(home, relPath string, managed map[string]any) error {
	if relPath != rulesRel {
		return fmt.Errorf("dsh: %s is not a managed-key-restorable file", relPath)
	}
	content, _ := managed["agentFleetRules"].(string)
	return kit.WriteManagedBlock(a.rulesPath(home), content)
}

// ---- 小工具 ----

func equal(a, b any) bool { return kit.ValuesEqual(a, b) }
