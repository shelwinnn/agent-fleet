# 批次二适配器：四家族接入前确认与切片规划（KM-32）

本文件是批次二（Claude / DeepSeek Harness / Grok / Hermes）的**接入前确认记录**与**切片结构建议**，
依据 `docs/agent-support-matrix.md` 的统一适配契约与逐家族验收 5 项、并对照批次一
`docs/adapters-batch-1.md` 的结构编写。

**本片不改产品代码、不改批次一契约、不改 spec/架构。** 结论一律附证据：本机实测命令与输出，或官方来源 URL
（标注取回时的 HTTP 状态）。没有证据的条目保留 **未验证**，不按同名模型、产品名或截图推测。

**证据分级**（本文件每处结论都标级别）：

| 级别 | 含义 |
|---|---|
| **本机实测** | 在本机（MindStone，Linux x86_64，Arch）实际执行命令并观察到输出 |
| **官方来源** | 厂商官方文档 / 官方 npm registry 元数据 / 官方仓库原件，附 URL 与 HTTP 状态 |
| **未验证** | 无上述两类证据；适配器遇到此类能力必须显式拒绝，不得静默跳过 |

接入实测环境：Linux x86_64（本机），使用临时 HOME 读写，不触碰真实 `~/.claude`、`~/.claude.json`、
`~/.dsh`、`~/.grok`、`~/.hermes`（护栏 #12）。**本片全程只读**：唯一一次写入尝试被沙箱拒绝，
且该拒绝本身即 §2.1「副作用」结论的证据（见 §2.1 第 5 项）。

> **关于 `multica runtime list` 的定位**：本机 Multica 中 `Claude`、`Hermes`、`Grok`、`dsh`（以及
> `DeepSeek Harness` 的第二条注册）均为 `online`。这只证明「Multica 侧存在可用的 agent 运行时」，
> **不构成任何一家的 CLI 安装契约或配置契约证据**——它既不说可执行程序是什么、也不说配置文件在哪。
> 本文件四家的契约结论全部另行取证，`multica runtime list` 仅作为「本机确有该家族在跑」的旁证，
> 在 §0 表中单列。为便于核对，本机 `multica runtime list` 的 `launch_header` 分别是：
> `claude (stream-json)`、`dsh --profile multica (stdio)`、`grok agent stdio`、`hermes acp`。

---

## 0. 一览

| 家族 | 运行时来源（官方渠道） | 本机实测版本 | 契约证据强度 | 接入可行性结论 |
|---|---|---|---|---|
| **Claude** | npm `@anthropic-ai/claude-code`（Anthropic 官方）；另有原生安装脚本与桌面应用 | `2.1.270 (Claude Code)` | 官方 docs 覆盖到**每一个设置键**，含 `env` 逐键语义 | **可接入**；唯 **user 作用域 MCP 需用户裁决**（§1.4） |
| **DeepSeek Harness** | npm `@deepseek-ai/dsh`（DeepSeek 官方）；仓库 `deepseek-ai/deepseek-harness` | `0.1.5-rc.3` | 官方**生成式** config catalog（143 个包逐字段）+ 字段级 provider 契约（`baseURL`/`apiKeyEnv` 直配） | **需用户裁决**（§2.4）：能力可确认，但上游声明 developer preview 破坏性变更 |
| **Grok** | `https://x.ai/cli/install.sh`（原生自更新二进制）；**官方 npm 包不存在** | `1.0.30 (04b7ffed98c6)` | CLI 随附**与自身版本匹配**的字段全表 + 机器可读健康检查 + 原生 env 间接引用 | **可接入**（本批次契约最完整） |
| **Hermes** | `NousResearch/hermes-agent`（Nous Research 官方，MIT），`install.sh` 内部即 git clone | `Hermes Agent v0.21.2 (2026.9.11) · upstream be2f7e9c` | 单一 YAML 真源 + 官方 CLI 参考 + 配置版本号可校验；**无机器可读 schema** | **可接入** |

四家在 Multica 侧均 `online`（旁证，非契约证据）。**本批次无一家按批次一 ZCode 的「非官方分发链」被排除**：
四家的可执行程序均可回溯到厂商官方渠道（Grok 的**本机安装记录**另有一处不一致，见 §3.1、§3.5）。

---

## 1. Claude（Claude Code，Anthropic）

### 1.1 接入前确认

| 确认项 | 结论 | 证据 |
|---|---|---|
| 运行时来源 | 官方 npm `@anthropic-ai/claude-code`，bin `claude`；`engines.node >=22`；`optionalDependencies` 含 8 个平台子包（linux/darwin/win32 × x64/arm64，外加 linux x64/arm64 musl）；`homepage` = `github.com/anthropics/claude-code`；524 个已发布版本 | **官方来源**：`curl -s https://registry.npmjs.org/@anthropic-ai%2fclaude-code` → HTTP 200（`dist-tags` = `latest 2.1.282` / `stable 2.1.274`） |
| 本机形态 | npm-global 安装（fnm node v24.15.0），**不是**桌面应用、不是容器 | **本机实测**：`claude doctor` → `Running: npm-global (2.1.270)`、`Commit: 97ecbf7abeb4`、`Platform: linux-x64`；`readlink -f $(command -v claude)` → `.../node_modules/@anthropic-ai/claude-code/bin/claude.exe` |
| 可执行程序与版本探测 | 可执行名 `claude`；`claude --version` → `2.1.270 (Claude Code)`。解析规则：`<semver> (Claude Code)`，形态不符即**显式报错**，不猜。备用探测：`claude doctor` 首行 `Running: <install-method> (<version>)`（同时给出 install method 与 commit） | **本机实测**：`claude --version`；`claude doctor` |
| 配置路径与格式 | 用户作用域 `~/.claude/settings.json`（JSON，无注释语义）；项目 `.claude/settings.json`；本地覆盖 `.claude/settings.local.json`；管理策略 `managed-settings.json`（系统目录 / MDM / 服务端下发）。**另有第五个文件 `~/.claude.json`，由 Claude Code 自己写**，承载登录态、MCP server 配置与 per-project 状态 | **官方来源**：`https://code.claude.com/docs/en/settings`（HTTP 200）；`.../claude-directory`（HTTP 200）原文："Claude Code also keeps a fifth file, `~/.claude.json`, that it writes for itself; **you don't need to edit it**. It holds your sign-in session, MCP server configurations, per-project state such as trust…" |
| 指令文件 | `~/.claude/CLAUDE.md`（用户级；亦识别 `AGENTS.md`）。**本机该路径是软链**，指向用户全局规则文件 → 适配器只替换受管标记块，块外逐字节保留，且必须**写穿软链** | **本机实测**：`ls -la ~/.claude/CLAUDE.md` → `-> /home/shelwin/.config/plexus/personal/rules/global.md`（同批次一 Codex `AGENTS.md` 的形态） |
| Skill 目的地 | `~/.claude/skills/<name>/SKILL.md`（非递归一层）。**本机该目录是软链集合**（指向 `~/.agents/skills/*`） | **本机实测**：`ls -la ~/.claude/skills/` 全部条目为 `-> ../../.agents/skills/<name>` |
| MCP 表示 | 三个作用域：**Local**（当前项目，不共享）→ `~/.claude.json`；**Project**（随版本控制共享）→ 仓库根 `.mcp.json`；**User**（所有项目）→ `~/.claude.json`。条目形状 `{type, command, args, env}` 或 `{type, url, headers}`；`type` 接受 `streamable-http` 作为 `http` 的别名 | **官方来源**：`https://code.claude.com/docs/en/mcp`（HTTP 200）作用域表（三列：Loads in / Shared with team / Stored in）；**本机实测**：`~/.claude.json` 顶层 `mcpServers` 有 4 条，形状与文档一致；`claude mcp add -s/--scope <local\|user\|project>` |
| MCP 环境变量间接引用 | **官方支持** `${VAR}` 与 `${VAR:-default}`；官方列出展开位置为 `command`、`args`、`env`、`url`、`headers`。文档示例即用 `${API_KEY}` 作 `headers.Authorization`，并明确"so the token never lands in the file" | **官方来源**：`https://code.claude.com/docs/en/mcp` §"Environment variable expansion in .mcp.json"（含 Supported syntax / Expansion locations） |
| 模型/provider 表示 | `~/.claude/settings.json` 顶层 `model`（别名或完整 ID）；端点走同一文件的 `env` 块 `ANTHROPIC_BASE_URL`（官方示例即用该键演示 `env`）。**凭据不进配置文件**：官方指明 `env` 的值是"plain text in the settings file"，API 凭据应改用 `apiKeyHelper`（执行命令取凭据） | **官方来源**：`https://code.claude.com/docs/en/settings-reference`（HTTP 200）`### env` / `### model` / `### apiKeyHelper` 三节 |
| 受管字段与所有权 | 见 §1.2 | fixture 断言（待实现） |
| 健康检查方式 | `claude --version`（版本一致）；`claude doctor`（官方子命令："Check the health of your Claude Code installation"，本机实跑产出真实报告，**含设置文件解析诊断**——本机就报出 `~/.claude.json › mcpServers.zread: Skipped — MCP server "zread" has a "url" but no "type"`）。注意 `claude mcp list` 会**连接** MCP server，不适合做纯离线健康检查 | **本机实测**：`claude doctor`（只读）；`claude --help` 子命令表 |
| Linux / macOS / WSL 支持组合 | 官方：macOS 13.0+、Windows 10 1809+ 或 Server 2019+、Ubuntu 20.04+、Debian 10+、Alpine Linux 3.19+；硬件 x64 或 ARM64，4 GB+ RAM。Windows **可原生运行**，Git for Windows 可选；**WSL 2 支持、WSL 1 不支持**。npm 平台子包覆盖 linux/darwin/win32 × x64/arm64（含 musl） | **官方来源**：`https://code.claude.com/docs/en/setup`（HTTP 200）§System requirements 与 §Set up on Windows 表；npm `optionalDependencies`。**本机仅 Linux x64 实测** |
| 能力声明 | version ✅ / modelProvider ✅ / MCP ⚠️（**写入面待裁决**，见 §1.4）/ Skills ✅ / Rules ✅ | 见上各行 |

### 1.2 受管字段与所有权边界

**Fleet 可管（`~/.claude/settings.json`，JSON 整树合并写、保留未托管键）**

- `model`（string，模型别名或完整 ID）
- `env.ANTHROPIC_BASE_URL`（**仅端点**，非密钥；对应归一化 provider 的 `endpoint`）

**必须保留未托管（逐字节/逐键保留，不读不写）**

- `~/.claude/settings.json`：**`env.ANTHROPIC_AUTH_TOKEN` 及 `env` 下其余全部变量**（本机该键实际存在且持有真实凭据，
  **值不记录**）、`statusLine`、`enabledPlugins`、`permissions`、`hooks`、`apiKeyHelper`、`availableModels` 等
- `~/.claude.json`：**全部键**（`projects`、`numStartups`、`skillUsage`、`mcpServers` 中未点名的条目……）
- `~/.claude/CLAUDE.md`：受管标记块**之外**的全部内容（且写穿软链）

**这里出现一个批次一没有的所有权形态**：受管键 `env.ANTHROPIC_BASE_URL` 与必须未托管的
`env.ANTHROPIC_AUTH_TOKEN` **位于同一个嵌套对象 `env` 内**，因此所有权必须做到
**嵌套对象内的键级**，而不是"顶层键级 + 整个文件保留"。批次一的 codex（TOML 行级手术）、
omp（YAML Node 树合并）、opencode（JSON 整树重序列化但保留未托管键）三种写法**都能**做到键级，
但 §6 缺口 1 建议在契约里写明这一要求，避免后续家族各自解释。

**官方对凭据的指引与 Fleet 边界一致**（可直接引用为"不托管 secret"的家族级依据）：
`env` 一节原文 "Values here are plain text in the settings file and reach every subprocess Claude Code
starts. … **for API credentials, use `apiKeyHelper`**"。即：端点可由 Fleet 管，凭据由用户用
`apiKeyHelper`（命令）或自身登录态解决，Fleet 既不读也不写。

### 1.3 接入可行性结论

**可接入**，但 MCP 一项需用户裁决。逐能力：

| 能力 | 结论 | 说明 |
|---|---|---|
| version | ✅ 可接入 | `claude --version` 形态明确、本机实测；**安装/升级不在本片范围**（同批次一口径，探测 ✅ / 安装未实现） |
| modelProvider | ✅ 可接入 | `model` + `env.ANTHROPIC_BASE_URL`；`endpoint` 直配，`apiKeyEnv` 语义由 `apiKeyHelper` 承接（Fleet 不写凭据） |
| rules | ✅ 可接入 | `~/.claude/CLAUDE.md` 受管标记块；**必须复用批次一的软链写穿修复**（`kit.resolveWritePath`，批次一复核轮修复 #3） |
| skills | ✅ 可接入 | `~/.claude/skills/<name>` 软链；本机目录已是软链集合，幂等性需 fixture 锁定 |
| mcp | ⚠️ **需用户裁决** | user 作用域唯一落点是 `~/.claude.json`——官方明说该文件"Claude Code writes for itself; you don't need to edit it"，且它是**活状态文件**（本机 mtime `2026-09-23 14:49`，晚于 `settings.json` 的 `2026-09-19 22:31`，证明应用在持续重写）。见 §1.4 |

### 1.4 Claude 的 MCP 裁决项（**本片唯一阻塞维度**）

**事实**：Claude Code 的 MCP 有三个作用域，其中 Local 与 User **都**存在 `~/.claude.json`；
只有 Project 作用域存在独立、可安全外部写入的 `.mcp.json`（仓库根，随版本控制共享）。
而 `~/.claude.json` 是应用自持的活状态文件，官方明确"你不需要编辑它"。

**风险具体是什么**：Fleet 若直接改写 `~/.claude.json`，就是与 Claude Code 自身**并发写同一个文件**。
批次一三个家族的受管写入面（`config.toml`/`config.yml`/`mcp.json`/`opencode.json`/`AGENTS.md`）
都是"用户配置"，没有一个是"应用高频重写的状态文件"。该文件同时承载登录态与 per-project 状态，
一旦写坏影响面超出 MCP 本身。

**请裁决的选项（本片不自行扩大范围）**：

- **(a) MCP 走官方 CLI 写入路径**：adapter 的 MCP 能力实现为调用 `claude mcp add/remove -s user`，
  由 Claude Code 自己完成对 `~/.claude.json` 的写入。优点：只使用厂商支持的写路径，规避并发写；
  代价：适配器从"纯文件投影"变成"调用家族 CLI"，是**批次一契约之外的新形态**，且会与文件投影的
  双侧摘要/drift 判定方式产生分歧（需要单独的观测手段）。
- **(b) 只支持 Project 作用域 MCP**：Fleet 只写 `.mcp.json`（明确文档化、可安全外部写入），
  user 作用域声明为 `unsupported`，用户自管。优点：零新增形态、零风险；代价：能力覆盖不完整。
- **(c) 直接写 `~/.claude.json` 并接受风险**：需要额外设计（写前重读 + 原子替换 + 丢失检测），
  且必须承认厂商不支持该用法。**不推荐**。
- **(d) 本批次 Claude 不接 MCP**，先交付其余四项能力。

**倾向**：**(b)** 或 **(d)** —— 两者都只使用文档化的写入面，不引入新形态、不承担厂商未背书的写入风险。
最终由用户裁决；裁决前 Claude 适配器的 MCP 能力必须声明 `unverified`，并在 `Validate` 阶段
**在任何写入之前显式拒绝**（矩阵要求，禁止静默跳过）。

### 1.5 未验证清单（Claude）

1. **macOS / Windows / WSL 实测**：OS 结论全部来自官方文档；本机仅 Linux x64。
2. **安装/升级**：仅探测已验证；`claude install <target>`（stable/latest/版本号）存在但未纳入本片。
3. **`~/.claude.json` 的并发写行为**：未做并发写实验，也未确认 Claude Code 是否使用文件锁
   （官方文档未见相关说明）→ §1.4 的风险判断基于 mtime 与官方措辞，**未做压力验证**。
4. **`env` 块中 `ANTHROPIC_BASE_URL` 与用户 shell 导出的优先级**：文档已述（settings 覆盖 shell 导出），
   但未在本机做交互验证。
5. **`apiKeyHelper` 作为 `apiKeyEnv` 语义承接方**：命令形态已确认，但"Fleet 期望的 apiKeyEnv 名字
   如何映射到 helper 命令"未设计，属接入实现期的设计项。
6. **Skills 的软链幂等性**：本机 `~/.claude/skills/` 全为软链，但"重复 reconcile 无实质变更"
   需 fixture 锁定后才能声明 ✅。
7. **`availableModels` / `enforceAvailableModels` 等管理键**：本片确认其存在但**不纳入受管范围**，
   与 `managed-settings.json` 的关系留待范围决策。

---

## 2. DeepSeek Harness（`dsh`，DeepSeek AI）

### 2.1 接入前确认

| 确认项 | 结论 | 证据 |
|---|---|---|
| 运行时来源 | 官方 npm `@deepseek-ai/dsh`，bin `dsh` → `lib/bin.js`；MIT；`repository` = `git+https://github.com/deepseek-ai/deepseek-harness.git`（directory `apps/cli`）；**maintainers 含 `tianyicui-deepseek <tianyi@deepseek.com>`**。上游仓库 `deepseek-ai/deepseek-harness`：MIT、235,500 stars、默认分支 `master`、创建于 2026-08-13、`homepage` = `https://deepseek.com/harness`、描述 "DeepSeek Harness: Everything is a Plugin."。仓库 README 自述 "an open-source agent harness developed by **DeepSeek AI**"。官方文档站 `https://deepseek-harness.github.io/deepseek-harness/`。另有 Python runtime wheel 打包同一命令 | **官方来源**：`curl -s https://registry.npmjs.org/@deepseek-ai%2fdsh` → HTTP 200（27 个版本；`dist-tags` = `latest 0.1.5-rc.3` / `next 0.1.7-rc.2` / `alpha 0.1.7-alpha.2`）；`https://api.github.com/repos/deepseek-ai/deepseek-harness` → HTTP 200；仓库 `README.md` HTTP 200 |
| ⚠️ 版本状态 | **`latest` 是预发布版 `0.1.5-rc.3`，不是稳定版**。仓库 README 原文："DeepSeek Harness is in _developer preview_ and iterating rapidly. **THERE WILL BE COMPATIBILITY-BREAKING CHANGES.**" | **官方来源**：npm `dist-tags`；仓库 README §Developer preview |
| 本机形态 | npm-global 安装（fnm node v24.15.0），版本 `0.1.5-rc.3`。本机 Multica 注册有**两条** dsh runtime（`0.1.5-rc.1` 与 `0.1.5-rc.3`，provider 均为 `dsh`），实际安装为 `0.1.5-rc.3` | **本机实测**：`dsh --version` → `0.1.5-rc.3`；`readlink -f $(command -v dsh)` → `.../node_modules/@deepseek-ai/dsh/lib/bin.js`；`multica runtime list` |
| 可执行程序与版本探测 | 可执行名 `dsh`；`dsh --version` 与 `dsh -V` 均输出**裸 semver** `0.1.5-rc.3`（本机实测，退出码 0）。解析规则：裸 semver（允许 prerelease 后缀），不符即**显式报错** | **本机实测**：`dsh --version`、`dsh -V`；`dsh --help`（`-V, --version  output the version number`） |
| 配置根与路径 | `$DSH_HOME`（默认 `~/.dsh`）。**配置不是单文件，而是两个平面**（官方架构决策原文）：<br>① **组合平面** `cordis.yml` + patch 层 —— "which plugins exist, wiring, **deployment config**, owned by the **orchestrator** and upgraded with the product"；<br>② **用户设置平面** `$DSH_HOME/settings.yaml` —— "only the **user-editable** subset; the test is 'should the personal config page edit it?'" | **官方来源**：仓库 `.agents/notes/archived/architecture/2026-07-28-user-settings-seam.md`（HTTP 200）§Decision "Two planes with a litmus test"；**本机实测**：`~/.dsh/settings.yaml` 顶层为「插件 id → 配置段」映射（本机 10 个顶层键：`ui-onboarding`、`agent-default-model`、`pet`、`dsh-web-ui-market`、`skin-custom-theme`、`pae-ping`、`ui-theme`、`agency-agents`、`dsh-mcp-manager`、`dsh-better-sidebar`） |
| 配置格式 | 两个平面均为 **YAML**。patch 层是"a top-level YAML array of loader patch entries (id-targeted config overrides, disables, and insert lists; `!!js` expressions allowed)"。patch 层的**权威顺序**：各 bundle patch → profile 的 `cordis.patch.yml` → **home 级 `$DSH_HOME/cordis.patch.yml`**（"machine-local preferences shared by every profile, so it outranks the per-profile layer"）→ 各 `--patch` 覆盖。**"Later layers win per row; a patch replaces the targeted row's complete `config` value rather than deep-merging keys, and may insert new rows."** | **官方来源**：`apps/cli/reference/README.md`（HTTP 200）§第 9 段（层序与替换语义）；profile 目录本机实测含 `package.json`（`dsh.profile.bundles`）、`cordis.patch.yml`、`cordis.yml` |
| 指令文件 | `$DSH_HOME/AGENTS.md`（**固定的用户级全局指令文件**）。插件 `@deepseek-ai/dsh-agent-instructions` 的官方 catalog 原文："Harness home containing the fixed user-global `AGENTS.md`; defaults to `$DSH_HOME` or `~/.dsh`" | **官方来源**：`docs/config-catalog.md`（HTTP 200）`@deepseek-ai/dsh-agent-instructions` 段；**本机实测**：`~/.dsh/AGENTS.md` 存在（1728 B） |
| Skill 目的地 | 插件 `@deepseek-ai/dsh-skill-filesystem`，官方 catalog 给出根序：项目根 → `$DSH_HOME`（默认 `~/.dsh`）→ `$DSH_AGENTS_HOME`（默认 `~/.agents`）→ `customSkillDirs`。**本机 `~/.dsh/skills/` 是扁平 `*.md`，而 `~/.agents/skills/` 是 `<name>/SKILL.md` 目录** | **官方来源**：`docs/config-catalog.md` `@deepseek-ai/dsh-skill-filesystem` 段；**本机实测**：`ls ~/.dsh/skills/` = `feishu-cli.md`、`workflow-one.md`（含 `name`/`description` frontmatter，且带 `<!-- managed-by: … -->` 注释）；`ls ~/.agents/skills/` = 64 个 `<name>/` 目录，内含 `SKILL.md` |
| MCP 表示 | **没有 MCP JSON 配置文件**。MCP server 是插件 `@deepseek-ai/dsh-mcp-client` 的**组合层行**，官方 catalog 给出完整 config 类型：`transport: 'stdio' \| 'streamable-http'`、`serverName`（`[A-Za-z0-9_-]{1,32}`，决定工具名前缀 `mcp__<serverName>__<rawName>`）、`command`、`args`、`env: Record<string,string>`、`cwd`、`toolCallTimeoutMs`、`failOnStartupError`，或 HTTP 侧的 `url`/`headers`。官方 CLI 参考原文："The CLI also ships `@deepseek-ai/dsh-mcp-client` as a dependency for patch layers, but **no MCP server is enabled by default**" | **官方来源**：`docs/config-catalog.md` `@deepseek-ai/dsh-mcp-client` 段；`apps/cli/reference/README.md` 末段。**本机实测**：`~/.dsh/` 下**无** `mcp.json`；本机 MCP 由第三方插件 `@wingsky-1/dsh-mcp-manager` + `@yilinxiao/dsh-mcp-lazy` 提供（**非 core**，见 §2.3 说明） |
| 模型/provider 表示 | 默认模型：设置平面 `agent-default-model` → `{provider, model, reasoningEffort}`（官方 catalog `@deepseek-ai/dsh-agent-default-model`，字段一一对应）。**自定义端点：插件 `@deepseek-ai/dsh-llm-pi-ai`** 的 `providers: Record<string, PiAiProviderProfile>`，官方 catalog 逐字段原文：`baseURL?` = "Endpoint for this route's models"、**`apiKeyEnv?` = "Credential reference (environment-variable name) resolved per request through `ctx.credentials`"**、`api?` = 线协议。且"An empty (or omitted) dict is the dormant **settings-driven** posture: the adapter mounts with no routes and registers them the moment **a settings section** supplies profiles" | **官方来源**：`docs/config-catalog.md` `@deepseek-ai/dsh-agent-default-model` 与 `@deepseek-ai/dsh-llm-pi-ai` 两段；**本机实测**：`~/.dsh/settings.yaml` 的 `agent-default-model` = `{provider: deepseek-official, model: deepseek-flash, reasoningEffort: high}`，键名与 catalog 完全一致 |
| 受管字段与所有权 | 见 §2.2 | fixture 断言（待实现） |
| 健康检查方式 | `dsh --version`（版本一致）。⚠️ **`--dump-config` / `--dump-default-config` / `--dump-config-schema` 有写入副作用**：官方 CLI 参考明写 "**A dump initializes missing profile files**"；本机实测 `dsh --profile multica --dump-default-config` 触发 `prepareProfile` 写入 `~/.dsh/profiles/multica/cordis.yml`，被只读沙箱以 `EROFS: read-only file system, open '/home/shelwin/.dsh/profiles/multica/cordis.yml'` 拒绝。→ **不可作为只读 HealthCheck**。<br>正面的健康检查素材：`--dump-config-schema` 输出 **JSON Schema 2020-12** 文档（其 `$defs.patchList` 描述 patch 覆盖层），可用于**离线校验** Fleet 写出的 patch 层；此外 `dsh --profile <name> --dump-config` 会打印"哪一层提供了每一行"，可用于 drift 观测（但仍需接受其初始化副作用） | **官方来源**：`apps/cli/reference/README.md` §48/§56 段；**本机实测**：见上（EROFS 拒绝） |
| Linux / macOS / WSL 支持组合 | 官方 `native/system/docs/support-matrix.md`：平台包 `linux-x64`、`linux-arm64`、`darwin-x64`、`darwin-arm64`；macOS 构建目标 11.0+；Linux 绑定按运行时 Node 进程的 libc 选择（glibc/musl 均有）。原文："**Windows has neither a Landlock launcher nor this POSIX addon.** The Harness retains its existing Windows semaphore implementation." → Windows 走另一实现（沙箱能力不同）。npm 包**未声明 `os`/`cpu`/`engines`**。**WSL 未被提及 → 未验证** | **官方来源**：`native/system/docs/support-matrix.md`（HTTP 200）；npm 元数据（无 `os`/`cpu`/`engines`）。**本机仅 Linux x64 实测** |
| 能力声明 | version ✅ / modelProvider ✅ / MCP ⚠️（契约明确但写入形态是新形态，见 §2.2）/ Skills ✅ / Rules ✅ / **MCP `envRefs` ❌ 不支持** | 见 §2.2、§2.3 |

### 2.2 受管字段与所有权边界

dsh 与批次一三家族的关键差异：**厂商自己就把"组合平面"划给了 orchestrator**，把"用户设置平面"划给了用户。
Fleet 的受管写入应当**分别对齐这两个平面**，而不是只写一个 settings 文件。

**Fleet 可管（组合平面）**

- **`$DSH_HOME/cordis.patch.yml`**（home 级 patch 层，官方定位为"machine-local preferences shared by
  every profile"）——这是 dsh 的**首选受管写入面**：
  - **MCP server**：以 `insert` 行插入 `@deepseek-ai/dsh-mcp-client` 行，`config` 为该 server 的
    `{transport, serverName, command, args, env, cwd, toolCallTimeoutMs}`
  - 所有权粒度是**行级**（一个 MCP server 一行 → 逐 server 可管、可回退）；**注意**：官方明说 patch
    "replaces the targeted row's complete `config` value rather than deep-merging keys"，因此
    **对同一个既有行不能只改一个键**——Fleet 必须只操作自己 insert 的行，或整体替换它自己的行。

**Fleet 可管（用户设置平面，`$DSH_HOME/settings.yaml`）**

- `agent-default-model`：`provider` / `model` / `reasoningEffort`
- `llm-pi-ai` 的 provider route（`baseURL` + `apiKeyEnv`，若该 namespace 由设置平面供给）
- **该文件是设计上支持外部写入的**：官方 `dsh-settings-file` provider 原文提供
  "read-modify-write persists under a **cross-process writer lock** with atomic `0600` tmp+rename commits,
  **leaf-level diff patching of the written namespace (comments survive untouched nodes)**, and
  content-equality self-write suppression"。→ 与 Claude 的 `~/.claude.json` 形成鲜明对比：
  **dsh 的 settings.yaml 有明确的外部写入并发协议，Claude 的没有。**

**必须保留未托管**

- `$DSH_HOME/settings.yaml` 中**其余全部 namespace**（本机 10 个顶层键中除上述外均为 UI/pet/skin/第三方插件配置）
- `$DSH_HOME/cordis.patch.yml` 中**用户既有的 patch 行**（Fleet 只增删自己 insert 的行）
- `$DSH_HOME/.credentials.yaml`、`$DSH_HOME/.env`、`invoking dir/.env`（**凭据解析链，绝不读值**）
- `profiles/*/package.json` 的 `dsh.profile.bundles` 与依赖（属插件管理，`dsh plugin` 的职责）
- 设置平面中标注 `role('secret')` 的字段（官方 seam 文明确要求这些字段在对外暴露前必须脱敏）

### 2.3 关于本机 MCP 来源的重要区分（避免误判）

本机 `~/.dsh/` 下可见 `dsh-mcp-catalog.json`、`dsh-mcp-catalog/`、`dsh-mcp-manager` 设置段与
`@wingsky-1/` 目录，容易误判为"dsh 的 MCP 配置文件契约"。**实际不是**：

- 这些是**第三方社区插件**（`@wingsky-1/dsh-mcp-manager`、`@yilinxiao/dsh-mcp-lazy`）的产物；
- **core 的 MCP 契约**是 §2.1 所述的插件行 config（`@deepseek-ai/dsh-mcp-client`）；
- `~/.dsh/dsh-mcp.json` 在本机**不存在**（另有 `dsh-mcp.json.migrated.bak` 迁移残留）。

**适配器只能按 core 契约实现**（对应批次一对 ZCode 的教训：不得把第三方分发链的形态写进受管契约）。

### 2.4 接入可行性结论：**需用户裁决**

**这不是 ZCode 式的来源问题**。dsh 的运行时来源与配置契约都**可以确认**，且质量是本批次最高的之一：

- 来源官方（npm scope + deepseek.com 维护者邮箱 + 官方仓库 + 官方文档站）；
- `docs/config-catalog.md` 是**生成式**文档（`scripts/gen-config-catalog.ts` 生成、CI `verify-config-catalog`
  校验，且"cross-checks the runtime schemastery schema against the pasted declaration"），
  143 个包逐字段列出——这是比批次一任何一家都更硬的字段级契约证据；
- provider 契约（`baseURL` / `apiKeyEnv`）与 Fleet 归一化 `{endpoint, apiKeyEnv}` **字段级直接对齐**；
- `envRefs` 的缺席也有**明确官方记载**（seam 文把 "${env:VAR} value indirection for secrets" 列为
  **deferred**），而不是"查不到"——可以如实声明为 `unsupported` 而不是 `unverified`。

**但有两项范围问题需要用户拍板**：

1. **目标版本是预发布**：`latest` = `0.1.5-rc.3`；仓库 README 明写 developer preview 且
   "THERE WILL BE COMPATIBILITY-BREAKING CHANGES"。适配器一旦按当前字段落地，上游一个 rc 就可能失效
   （catalog 里的包名/字段名都可能变）。**这是范围决策，不是能力缺失。**
2. **MCP 的受管写入形态是批次一没有的新形态**：写入面是**组合平面的 patch 列表数组**
   （`$DSH_HOME/cordis.patch.yml`），不是"配置文件里的受管键"。现有 kit（TOML 行级手术 / YAML Node
   树合并 / JSON 整树）都不直接适用，需要新增"patch 列表数组的受管行插删 + 行级回退"逻辑，
   并让双侧投影按**行**而非按**文件键**计算。这需要在契约层面明确，而不是各家族自行解释（见 §6 缺口 1）。

**请裁决的选项**：

- **(a) 按 rc 接入，接受后续返工**：以 `0.1.5-rc.3` 为 `VerifiedVersions` 落地，
  并在能力声明的 `Reason` 与验收记录中写明 pre-release 风险；上游破坏性变更时按新版本重新确认。
- **(b) 等稳定版再接入**：本批次先做其余三家，dsh 记为「待稳定版」，
  触发条件见 §5.5。
- **(c) 只接 modelProvider + rules + skills（不接 MCP）**：避开新写入形态，
  只写已确认的 `settings.yaml` namespace 与 `AGENTS.md`/skills 目录；MCP 声明 `unsupported`。

**倾向**：**(c) 或 (b)**。MCP 的新形态值得单独一片设计，不宜夹在家族接入片里；
而 pre-release 目标版本让"一次做满"的性价比偏低。

### 2.5 未验证清单（DeepSeek Harness）

1. **patch 行的完整 schema 细节**：官方给出层序、替换语义与 `--dump-config-schema`（JSON Schema 2020-12，
   `$defs.patchList`），但本片**未导出实际 schema 内容**（dump 有写入副作用，见 §2.1）→
   `insert` 行的确切字段集属 **未验证**。
2. **`env` 值的间接引用**：官方 seam 文列为 **deferred** → 记为 `unsupported`；
   但"deferred"是否会在近期版本实现，未验证。
3. **WSL**：官方 support-matrix 未提及 → **未验证**；Windows 走 semaphore 实现，
   与 Linux 的 Landlock 沙箱能力不等价 → **行为差异未验证**。
4. **`settings.yaml` 的写入并发协议**：官方文档描述了 writer lock / leaf-level diff / self-write
   suppression，但**未在本机做并发写验证**。
5. **设置平面里 `llm-pi-ai` 的 namespace 名与注册条件**：catalog 说 routes 可由"a settings section"
   供给，但本机 `settings.yaml` 中**没有** `llm-pi-ai` 段（本机 provider 走 `deepseek-official`
   内置路由）→ namespace 名与 schema 属 **未验证**。
6. **`dsh --dump-config` 输出格式的稳定性**：可用于 drift 观测，但输出含注释与来源标注，
   作为机器判据的稳定性未验证。
7. **install/upgrade**：`npx @deepseek-ai/dsh` 与 `pnpm add` 两种路径均已文档化，但未纳入本片。
8. **桌面端（Electron）profile**：CLI 参考提到 `desktop` 是保留 profile 名，CLI 拒绝其
   boot/config-dump/plugin 管理请求 → 桌面形态的配置契约**未验证**（本机无桌面端）。

---

## 3. Grok（SpaceXAI/xAI 的 Grok Build）

### 3.1 接入前确认

| 确认项 | 结论 | 证据 |
|---|---|---|
| 运行时来源 | 官方渠道是**原生自更新二进制**：`curl -fsSL https://x.ai/cli/install.sh \| bash`（macOS / Linux / Git Bash，可 `bash -s <版本>` 钉版本）、Windows `irm https://x.ai/cli/install.ps1 \| iex`，企业侧另有 `https://x.ai/cli/enterprise-install.sh`。二进制自身内嵌上述三个 URL 与产物主机 `https://storage.googleapis.com/grok-build-public-artifacts/cli`。**官方 npm 包不存在**。上游仓库 `xai-org/grok-build`（Apache-2.0，27,084 stars，默认分支 `main`，创建 2026-07-14；无 GitHub Releases） | **官方来源**：本机随 CLI 发布的 `~/.grok/docs/user-guide/01-getting-started.md` 与 `~/.grok/README.md`（§Installation）；`strings ~/.grok/bin/grok-1.0.30 \| grep -aoE 'https://…'` 得到上述 URL；`https://api.github.com/repos/xai-org/grok-build` → HTTP 200。<br>⚠️ **本沙箱内 `x.ai` 与 `docs.x.ai` 不可达（curl HTTP 000）**，故安装脚本 URL 取自厂商随包文档与二进制内嵌字符串，**未直接取回** |
| ⚠️ 同名包区分 | npm 上 `grok-cli`（作者 whitesmith，自述 "A CLI tool that starts anthropic-proxy with Grok model and runs claude-code"，bin 也叫 `grok`）与 `grok` 均为**无关第三方**；`@xai/grok-cli`、`@xai/grok`、`@xai/cli`、`grok-build`、`@xai/grok-build`、`@spacexai/grok` 全部 **404** | **官方来源**：逐个 `curl -o /dev/null -w '%{http_code}' https://registry.npmjs.org/<name>`（404/200 见上）；`/tmp` 搜索接口结果 |
| ⚠️ 本机分发链不一致 | 本机 `~/.grok/config.toml` 记 `[cli] installer = "npm"`；官方对该键的定义是"**Which installer last set up this CLI, used to pick the update path**"，但**不存在任何官方 npm 包**。本机实际形态是原生二进制（`~/.local/bin/grok` → `~/.grok/bin/grok` → `grok-1.0.30`，161 MB），`~/.grok/downloads/` 留有 `grok-1.0.25-linux-x86_64` 与 `grok-linux-x86_64` 两个下载产物 → 与"下载原生二进制"路径一致 | **本机实测**：`cat ~/.grok/config.toml`；`ls -la ~/.grok/bin/ ~/.grok/downloads/`；**官方来源**：`26-config-reference.md` `cli.installer` 行。**该不一致不影响配置契约确认，记入 §3.5** |
| 可执行程序与版本探测 | 可执行名 `grok`；`grok --version` → `grok 1.0.30 (04b7ffed98c6)`。**另有官方 JSON 形态**：`grok version --json` → `{"currentVersion":"1.0.30 (04b7ffed98c6)","channel":"unknown"}`。解析规则：优先用 `version --json` 的 `currentVersion`，正则 `^grok <semver>( \(<hash>\))?$` 作为回退；不符即**显式报错**。<br>⚠️ **`~/.grok/version.json` 内容为 `{"version":"1.0.25",…}`，落后于实际二进制 `1.0.30`** → **不可作为版本探测来源** | **本机实测**：`grok --version`、`grok version --json`、`cat ~/.grok/version.json` |
| 配置路径与格式 | `$GROK_HOME`（默认 `~/.grok`）。**用户层** `~/.grok/config.toml`（TOML；`/settings` 写这里）；**项目层** `.grok/config.toml`（官方限定只能贡献 `[mcp_servers]`/`[plugins]`/`[permission]`/`[mcp] max_output_bytes`）；**部署层** `$GROK_HOME/managed_config.toml` 与 `/etc/grok/managed_config.toml`；**策略层** `$GROK_HOME/requirements.toml` 与 `/etc/grok/requirements.toml`（签名、`pin` 键用户不可覆盖）；另有 `$GROK_HOME/settings.json`，但官方文档把它限定得很窄（marketplace sources + Claude 兼容 permissions/hooks）。<br>**`settings_cache.json` / `models_cache.json` 在随包 vendored 文档中 0 命中 → 内部缓存，不是写入目标** | **官方来源**：随 CLI 发布的 `~/.grok/docs/user-guide/26-config-reference.md`（"This file ships with the CLI and is extracted to `~/.grok/docs/user-guide/26-config-reference.md` on launch. It is the **complete field list** for `config.toml`, `managed_config.toml`, and `requirements.toml`"）与 `05-configuration.md`（层序 8 级）；**本机实测**：`~/.grok/config.toml` 实际内容（`[cli]`/`[marketplace]`/`[models]`/`[ui]`，与文档键名一致） |
| 指令文件（rules） | **用户级规则目录 `$GROK_HOME/rules/*.md`**（官方："`$GROK_HOME/rules/` (default `~/.grok/rules/`) — Always scanned; applies to all projects"，且"Every `*.md` directly inside a listed directory is loaded as a rule … in every project and regardless of folder trust"）。项目级是 `AGENTS.md`（兼容 `Claude.md`/`CLAUDE.md`/`CLAUDE.local.md`/`AGENT.md` 等，目录内全部命中文件都加载） | **官方来源**：`~/.grok/docs/user-guide/12-project-rules.md`（Rules Directories 表 + 支持文件名清单）。⚠️ 本机 `~/.grok/rules/` **不存在** → 属新建路径 |
| Skill 目的地 | `~/.grok/skills/<name>/SKILL.md`（用户级，优先级最低）；另有 `.grok/skills`、`.agents/skills`、`~/.claude/skills`、`.claude/skills`、`~/.cursor/skills` 等兼容根。可用 `[skills] paths/ignore/disabled` 增删与禁用 | **官方来源**：`~/.grok/docs/user-guide/08-skills.md` §Skill Locations 表；**本机实测**：`~/.grok/skills/` 有 40+ 条目（本机与 `~/.agents/skills` 同名集合） |
| MCP 表示 | `~/.grok/config.toml` 下的 `[mcp_servers.<name>]`。stdio：`command`/`args`/`env`/`enabled`/`startup_timeout_sec`/`tool_timeout_sec`；HTTP：`url` + `headers`/`bearer_token_env_var`。管理命令 `grok mcp add \| list \| remove \| enable \| disable \| doctor`（`--scope user\|project`，user 写 `~/.grok/config.toml`） | **官方来源**：`~/.grok/docs/user-guide/07-mcp-servers.md`；`26-config-reference.md` `mcp_servers.<name>.*` 行 |
| MCP 环境变量间接引用 | **官方原生支持**：`${VAR}` 与 `${VAR:-default}`，展开位置为 `[mcp_servers.*]` 的 `url`、`command`、`args`、`env` 的值、`headers`（"at load time"）。另有专用键 `mcp_servers.<name>.bearer_token_env_var`（HTTP bearer 取环境变量名）。文档建议"reference secrets as `${VAR}` instead of pasting them" | **官方来源**：`07-mcp-servers.md`（"Grok expands string fields in `[mcp_servers.*]` … at load time" 与 `grok mcp add` 段落）；`26-config-reference.md` `bearer_token_env_var` 行 |
| 模型/provider 表示 | 顶层 `[models] default`（默认模型，`pin` 级）；自定义/BYOK 端点在 `[model.<id>]`：`model`、`base_url`、**`env_key`（"Environment variable name(s) holding the provider API key"）**、`api_key`（内联，官方明确"**Prefer `env_key`**"）、`api_backend`（`chat_completions`/`responses`/`messages`）、`context_window`、`extra_headers`、**`env_http_headers`（"HTTP headers populated from environment variables"）**、`query_params`。凭据解析顺序：`api_key` → `env_key` 命名的变量 → 登录态 → `XAI_API_KEY` | **官方来源**：`11-custom-models.md`（含 Credential Resolution / Environment-Variable Headers 两节）；`26-config-reference.md` `model.<id>.env_key` / `model.<id>.env_http_headers` 行 |
| 受管字段与所有权 | 见 §3.2 | fixture 断言（待实现） |
| 健康检查方式 | **机器可读、且不止一个**：`grok doctor --json`（本机实跑，输出 `{"schemaVersion":"1","facts":{…}}`，覆盖终端/多路复用器/颜色/剪贴板等；`grok doctor` 亦可 `--fix`）；**`grok inspect` / `grok inspect --json`——官方定位为"see which files and values won"，即配置来源与生效值一览**（层序冲突排查）；`grok mcp doctor --json`；`grok version --json` | **官方来源**：`21-terminal-support.md`（doctor）、`26-config-reference.md` §"Run `grok inspect` or `grok inspect --json` to see which files and values won"、`07-mcp-servers.md`（`grok mcp doctor --json`）；**本机实测**：`grok doctor --json`（只读，退出 0） |
| Linux / macOS / WSL 支持组合 | 官方：macOS、Linux、Windows（原生 PowerShell 安装器或 Git Bash/MSYS2）；**"WSL users get the Linux binary automatically" → WSL 支持**。Windows 侧对 `npx`/`npm`/`pnpm`/`yarn` 的 `.cmd` shim 有专门解析（按 `PATHEXT` 解析真实启动器路径） | **官方来源**：`01-getting-started.md` §Installation；`07-mcp-servers.md` §Windows 段。**本机仅 Linux x64 实测** |
| 能力声明 | version ✅ / modelProvider ✅（`env_key` 直配 `apiKeyEnv`）/ MCP ✅（**envRefs 官方支持**）/ Skills ✅ / Rules ✅ | 见上各行 |

### 3.2 受管字段与所有权边界

**Fleet 可管（`$GROK_HOME/config.toml`，TOML；建议复用批次一 codex 的行级手术写，注释与键序保留）**

- `[models] default`（+ 可选 `[models] default_reasoning_effort`）
- `[model.<fleet 命名空间>]`：`model`、`base_url`、`env_key`、`api_backend`、`name`
  （`env_key` 即归一化 provider 的 `apiKeyEnv`，**只写变量名，不写值**）
- `[mcp_servers.<name>]`（仅期望点名的条目）：`command`/`args`/`env`（值可用 `"${VAR}"`）/`url`/`headers`/
  `bearer_token_env_var`/`enabled`
- `$GROK_HOME/rules/*.md`（用户级规则目录；Fleet 可写自己的受管文件或受管块）
- `$GROK_HOME/skills/<name>/SKILL.md`（软链/目录，同批次一口径）

**必须保留未托管**

- `config.toml` 中其余全部键：`[ui]`、`[features]`、`[session]`、`[tools]`、`[plugins]`、`[compat]`、
  `[auth]`、`[marketplace]`、`[skills]` 中未被 Fleet 点名的项、未点名的 `[model.*]` 与 `[mcp_servers.*]`
- `~/.grok/auth.json`（**凭据，绝不读值**）、`~/.grok/settings_cache.json`、`~/.grok/models_cache.json`
  （内部缓存，不作为写入面）
- `$GROK_HOME/requirements.toml` 与 `/etc/grok/requirements.toml`（**组织策略层**，`pin` 键用户层不可覆盖；
  Fleet 不应试图绕过或改写策略层）
- `$GROK_HOME/managed_config.toml`（**部署方层**，语义上属于比 Fleet 用户层更高的层；
  本片**不纳入**受管范围，见 §6 缺口 3）

**本机实测的 `~/.grok/config.toml` 恰好是一份理想的"未托管保留"回归样本**（四组既有键均不在受管范围）：

```toml
[cli]
installer = "npm"
[marketplace]
default_skills_installs_purged = true
official_marketplace_auto_installed = true
  [[marketplace.sources]]
  name = "xAI Official"
  git = "https://github.com/xai-org/plugin-marketplace.git"
[models]
default = "grok-4.6"
default_reasoning_effort = "xhigh"
[ui]
max_thoughts_width = 120
fork_secondary_model = "grok-4.6"
yolo = false
compact_mode = false
```

注意 `[models] default` **在受管范围内**（Fleet 会改它），而 `[ui]`/`[marketplace]`/`[cli]` 必须原样保留——
fixture 应直接以此形状作为输入。

### 3.3 接入可行性结论：**可接入**

逐能力：version ✅（探测已验证，安装/升级未纳入本片）/ modelProvider ✅ / MCP ✅（**含官方 `envRefs`**）/
Skills ✅ / Rules ✅。

**这是本批次契约最完整的一家**，理由：

1. **厂商随 CLI 发布与自身版本匹配的字段全表**（`26-config-reference.md` 是**生成物**，
   且逐键标注 `Requirements`（`pin`/`yes`/`—`）与 `Managed`（`fleet`/`user`）两列——
   **厂商自己就给出了"哪些键可被 fleet 管理"的列**，这在批次一四家里没有先例；
2. **机器可读健康检查有两个独立来源**（`doctor --json` 与 `inspect --json`）；
3. **`env_key` / `env_http_headers` / `bearer_token_env_var` / `${VAR}` 与 Fleet 的
   `apiKeyEnv` / `envRefs` 语义直接对齐**——批次一遗留的"envRefs 在部分家族无法满足"缺口，
   在 Grok 上原生成立；
4. TOML 行级手术 kit 可从批次一 codex **复用**（`internal/agentlocal/adapter/codex/tomlpatch.go`）。

### 3.4 已知限制 / 实现注意

- **`requirements.toml` 优先于用户层**：若目标机器部署了组织策略，Fleet 写入的用户层键可能被
  `pin` 覆盖。适配器的观测侧必须**读实际生效值**（`grok inspect --json`），否则会出现
  "Fleet 写成功但 drift 永不收敛"。建议在 `Inventory` 里对 `pin` 覆盖情形给出明确的健康告警，
  而不是伪装成 Reconciled。
- **`version.json` 不可用于版本探测**（本机即落后 5 个补丁版本）。
- **项目层 `.grok/config.toml` 只允许 4 个表**：Fleet 若在项目目录下工作，写别的键会被忽略/报错。
- **Windows 的 `command` 解析特殊**（`.cmd` shim 按 `PATHEXT` 解析），MCP 条目跨平台移植需注意。

### 3.5 未验证清单（Grok）

1. **本机安装路径与 `cli.installer = "npm"` 不一致**：官方无 npm 包，本机却记录 npm 安装器。
   本片**未能确定**该值的来源（可能是历史遗留或早期渠道）。**不影响配置契约**，但记入未验证；
   §5.5 给出再次评估触发条件。
2. **`x.ai` / `docs.x.ai` 本沙箱不可达**：安装脚本与在线文档**未直接取回**；
   安装渠道结论来自厂商随包文档（`~/.grok/README.md`、`user-guide/01-getting-started.md`）
   与二进制内嵌 URL 字符串。
3. **macOS / Windows / WSL 实测**：全部来自官方文档；本机仅 Linux x64。
4. **`grok inspect --json` 的 schema 稳定性**：命令与用途已文档化，但 JSON 结构未在文档中给出字段表 →
   作为 drift 观测判据前需先锁定结构。
5. **`settings.json` 的完整字段集**：官方提到其存在且很窄，但本片未取到完整字段表。
6. **`managed_config.toml` / `requirements.toml` 是否纳入 Fleet 受管范围**：未决（§6 缺口 3）。
7. **install/upgrade**：`grok update [--check|--version <v>|--alpha|--stable]` 已文档化，未纳入本片。

---

## 4. Hermes（Nous Research）

### 4.1 接入前确认

| 确认项 | 结论 | 证据 |
|---|---|---|
| ⚠️ 同名消歧 | 本机 `hermes` = **Nous Research 官方开源项目 `NousResearch/hermes-agent`**（Python），**不是** Nous 的 Hermes LLM 权重家族。易混：npm `hermes-agent` 是第三方桥接（自述 "Unofficial npm bridge"）；PyPI `hermes-agent` 虽元数据署名 Nous Research，但官方 `platform-support` 把 pypi/AUR/brew/macOS-Intel 一并列为 **Unsupported** | **本机实测**：`git -C ~/.hermes/hermes-agent remote -v` → `https://github.com/NousResearch/hermes-agent.git`；`pyproject.toml` 署名 Nous Research、MIT；**官方来源**：`https://api.github.com/repos/NousResearch/hermes-agent` → HTTP 200（MIT、248,793 stars、默认分支 `main`、创建 2025-07-22）；`https://hermes-agent.nousresearch.com/docs/getting-started/platform-support`（HTTP 200）§Unsupported |
| 运行时来源 | 官方两条渠道：`curl -fsSL https://hermes-agent.nousresearch.com/install.sh \| bash`（Linux/macOS/WSL2/Termux）与 `iex (irm https://hermes-agent.nousresearch.com/install.ps1)`（Windows 原生）。install.sh 内部即 `git clone https://github.com/NousResearch/hermes-agent.git` → `$HERMES_HOME/hermes-agent`，**故本机 `hermes --version` 报的 `Install method: git` 正是官方文档路径**，不是非官方安装 | **官方来源**：`https://hermes-agent.nousresearch.com/docs/getting-started/installation`（HTTP 200）；install.sh（HTTP 200，内含 REPO_URL 与 git clone）。**本机实测**：`hermes --version` 的 `Install directory` / `Install method` 两行 |
| `upstream be2f7e9c` 是什么 | 是**本机 checkout 的 upstream 提交 short SHA**（`origin/main`），不是产品版本号；banner 由 `git rev-parse --short=8` 取得。该 SHA 在官方仓库真实存在（`api.github.com` 提交可查）。**`v0.21.2` 是包版本，二者不可互推** | **本机实测**：`git -C ~/.hermes/hermes-agent log -1 --format='%H %ad %s'` → `be2f7e9c3616bf0f915c384d48bf9ec197da6863 Sat Sep 12 05:48:46 2026 -0700 feat: curator prunes unused skills at 30 days…`（提交信息与版本串前缀一致） |
| 本机形态 | git checkout + Python venv（`~/.local/bin/hermes` → `~/.hermes/hermes-agent/venv/bin/hermes`），Python 3.11.15 | **本机实测**：`ls -la ~/.local/bin/hermes`；`hermes --version` |
| 可执行程序与版本探测 | 可执行名 `hermes`；`hermes --version` / `-V` 输出**多行**（本机实测 6 行），首行 `Hermes Agent v<semver> (<date>) · upstream <sha>`；其后 `Install directory:` / `Install method:` / `Python:` / `OpenAI SDK:` / `Update available: …`（更新检查有 6h 缓存，失败则该行不打印）。<br>⚠️ **`hermes version` 不是子命令**（不在 choices 中，报 invalid choice，退出码 **2**）→ 健康探测**必须**用 `--version`。该多行格式**官方文档未记载**（CLI 参考只写 "Show version and exit"）→ 解析器按行取首行并按 `Hermes Agent v<semver>` 严格匹配，不符即**显式报错** | **本机实测**：`hermes --version`（退出 0）；`hermes --help`（`--version, -V  Show version and exit`，且 choices 中无 `version`）；**官方来源**：`https://hermes-agent.nousresearch.com/docs/reference/cli-commands`（HTTP 200） |
| 配置根与路径 | `$HERMES_HOME`（默认 `~/.hermes`）。**配置只有一个真源**：`~/.hermes/config.yaml`；密钥在 `~/.hermes/.env`（冲突时 `config.yaml` 胜）。另有 `SOUL.md`（用户级人格）、`skills/`、`profiles/`、`memories/`、`hooks/`、`gateway/` 等。**MCP 在 `config.yaml` 内，无独立 `mcp.json`** | **本机实测**：`hermes config path` → `~/.hermes/config.yaml`；`ls -la ~/.hermes/`；**官方来源**：`https://hermes-agent.nousresearch.com/docs/user-guide/configuration`（HTTP 200） |
| 配置格式 | **YAML 是唯一规范配置**，且带**配置版本号**：本机 `_config_version: 44`，`hermes config check` 通过（退出 0）。用 `hermes config migrate` 升级。**无机器可读 JSON Schema**（规范 = 仓库根 `cli-config.yaml.example` 注释样例 + 文档） | **本机实测**：`~/.hermes/config.yaml` 第 517 行 `_config_version: 44`；`hermes config check` → 配置版本 44 ✓（退出 0）；`venv/bin/python` + `yaml.safe_load` 导出**仅键树**（零值输出，65 个顶层键） |
| 指令文件 | ① **`HERMES_HOME/SOUL.md`** = 用户级人格/身份，**只从该路径加载**（不在 CWD 查找），占 system prompt 第 1 槽；② 项目级上下文 `AGENTS.md`，另有 `AGENTS.override.md`/`.hermes.md`/`CLAUDE.md`/`.cursorrules`，每会话只取首个命中；③ 记忆在 `memories/` | **官方来源**：`.../docs/user-guide/features/personality` 与 `.../features/context-files`（均 HTTP 200）。**本机实测**：`~/.hermes/SOUL.md` 存在（667 B，纯 Markdown，无 frontmatter） |
| Skill 目的地 | `~/.hermes/skills/`（bundled skills 安装时复制到此处）；支持 `<category>/<skill>/SKILL.md` 或 `<skill>/SKILL.md` 两种层级，并支持 `skills.external_dirs` 与项目级 `<repo>/.hermes/skills`、`<repo>/.agents/skills`（优先级 project > local > external）。**SKILL.md = YAML frontmatter（必需 `name`/`description`）+ Markdown，兼容 agentskills.io 开放标准**（形状即 Claude Code 的 SKILL.md 约定） | **本机实测**：`ls ~/.hermes/skills/`（38 个条目 + `.bundled_manifest`/`.hub/`/`.archive/`，含指向 `~/.agents/skills/*` 的软链），`ls ~/.hermes/skills/<name>/` → `SKILL.md`；**官方来源**：`.../docs/user-guide/features/skills`（HTTP 200）、`https://agentskills.io/specification`（HTTP 200） |
| MCP 表示 | **`config.yaml` 顶层 `mcp_servers:` 映射**，每项 `command`/`args`/`env`/`url`/`headers`/`transport`/`timeout`/`enabled`/`tools.{include,exclude}`。管理命令 `hermes mcp {add,list,test,configure,login,catalog,install,serve}`。**本机无独立 `mcp.json`**（`mcp.json` 只出现在 profile 分发包场景，由 `hermes profile install/update` 合并进 config.yaml） | **官方来源**：`.../docs/reference/mcp-config-reference`（HTTP 200）；**本机实测**：`~/.hermes/config.yaml` 顶层 `mcp_servers` 有 5 项（`code-review-graph` 用 `command`+`args`；4 个 `hermes-studio-*` 另有 `enabled`/`env`），形状与文档一致 |
| 模型/provider 表示 | 顶层 `model.default` + `model.provider`；**支持任意 OpenAI 兼容端点**：`model.base_url` + `model.api_key`（缺省回落 `OPENAI_API_KEY`），自托管用 `provider: custom`。另有 `fallback_providers:` 链、`auxiliary.<task>.{provider,model,base_url}`、`delegation.{provider,model,base_url}`。provider/model 目录来自 models.dev（`model_catalog.url`，本机缓存 `models_dev_cache.json` 4.9 MB / 223 个 provider） | **本机实测**：`~/.hermes/config.yaml` 的 `model:` / `model_catalog:` / `delegation:` 段（仅键名）；**官方来源**：`.../docs/integrations/providers`（HTTP 200，§Custom endpoints） |
| 受管字段与所有权 | 见 §4.2 | fixture 断言（待实现） |
| 健康检查方式 | 三者齐备且已文档化：**`hermes doctor [--fix] [--live] [--ack <id>]`**（配置/依赖诊断；`--live` 会做真实网络探测 → **不适合**作为常规只读健康检查）；**`hermes status [--all] [--deep]`**（组件状态，`--all` 已脱敏）；**`hermes config check`**（配置版本 + 缺失环境变量，本机退出 0）。另 `hermes --version` | **本机实测**：`hermes doctor --help`、`hermes status --help`、`hermes config check`（退出 0）；**官方来源**：`.../docs/reference/cli-commands`（HTTP 200） |
| Linux / macOS / WSL 支持组合 | 支持：**Linux x86_64/aarch64**（测试基线最新 Ubuntu + **WSL2**，需 glibc/systemd/FHS）、**macOS Apple Silicon**、**WSL2**、**Windows 10/11 原生**（x86_64/aarch64，PowerShell 安装，部分功能缺失）、Android/Termux（受限）、Nix、Docker。**明确不支持**：macOS Intel (x86)、AUR、PyPI、brew | **官方来源**：`.../docs/getting-started/platform-support`（HTTP 200）§Unsupported 四项。**本机仅 Linux 实测** |
| 能力声明 | version ✅ / modelProvider ✅ / MCP ✅（**envRefs 未验证**）/ Skills ✅ / Rules ✅（用户级 `SOUL.md` + 项目级 `AGENTS.md`） | 见下 |

### 4.2 受管字段与所有权边界

**Fleet 可管（`~/.hermes/config.yaml`，YAML；建议复用批次一 omp 的 Node 树合并写，注释保留）**

- `model.default`、`model.provider`、`model.base_url`（对应归一化 provider 的 `endpoint`）
- `mcp_servers.<期望点名的条目>`（`command`/`args`/`env`/`enabled`/`tools.*`）
- `skills.external_dirs`（若要纳入外部技能根）

**必须保留未托管（本机 65 个顶层键中的绝大多数）**

- `agent`(17 子键)、`terminal`(22 子键，含 docker/modal/daytona/vercel 后端)、`auxiliary`(14 子任务)、
  `display`(37 子键)、`dashboard`、`gateway`、`hooks_auto_accept`、`command_allowlist`、`security`、
  `approvals`、`compression`、`memory`、`curator`、`kanban`、`cron`、`sessions`、`checkpoints`、
  `logging`、`lsp`、`updates`、`openrouter`、`bedrock`、`web`/`browser`/`x_search`、
  `tts`/`stt`/`voice`、`slack`/`discord`/`telegram`/`matrix`/`mattermost`、
  `privacy`/`network`/`timezone`/`streaming`/`tool_output`/`tool_loop_guardrails`/
  `code_execution`/`context`/`goals`/`human_delay`/`onboarding`/`prefill_messages_file`/
  `file_read_max_chars`/`paste_collapse*`/`_config_version`
- **密钥面（绝不读值）**：`~/.hermes/.env`、`~/.hermes/auth.json` 完全不读；
  `config.yaml` 内 `delegation.api_key`、`auxiliary.*.api_key`、`secrets.bitwarden.*`、
  `dashboard.basic_auth.*`、以及顶层 **`HTTP_PROXY`/`HTTPS_PROXY`**（可能内嵌代理凭据）——
  这些同样要求**嵌套键级所有权**（与 §1.2 Claude 的 `env` 情形同类）
- `SOUL.md`：属**用户身份/人格**，建议**不纳入受管**（若要管，只能做受管块，且默认关闭）

### 4.3 接入可行性结论：**可接入**

逐能力：version ✅（探测已验证；`--version` 多行解析已明确，`hermes version` 不可用）/
modelProvider ✅（`model.base_url` + `provider: custom`，任意 OpenAI 兼容端点）/
MCP ✅（`config.yaml` 顶层 `mcp_servers`；**envRefs 未验证** → 需在 `Validate` 阶段拒绝）/
Skills ✅ / Rules ✅。

**风险点（相对 Grok 更高）**：

1. **无机器可读 schema**：`config.yaml` 的规范是 2138 行注释样例 + 文档，适配器必须自建容错，
   且上游 `_config_version` 迁移（当前 44）可能改变键语义；
2. **未托管面最大**：65 个顶层键里 Fleet 只碰 3 个组，fixture 的"未托管保留"回归面最宽；
3. **密钥面最广**（见 §4.2），嵌套键级所有权是硬要求。

### 4.4 未验证清单（Hermes）

1. **线上文档与本机 0.21.2 的逐页差异未 diff**：本机 checkout 为 2026-09-12 的 `be2f7e9c`，
   线上文档为最新；本片同时引用两者，但**未逐页比对**。
2. **`hermes doctor` / `hermes status --deep` 完整实跑输出未采集**（避免 `--live` 的真实网络探测），
   仅验证 `--help` 与 `config check`。
3. **`hermes mcp list` / `hermes mcp test` 未执行**（会连接 MCP server）→ MCP 结论来自
   `config.yaml` 结构 + 官方 mcp-config-reference，**未做连通性验证**。
4. **MCP `env` 是否支持 `${VAR}` 间接引用未验证** → 适配器对 `envRefs` 请求必须**在写入前拒绝**
   （同批次一 Codex/OMP 口径）。
5. **无机器可读 config schema**；`_config_version: 44` 的逐版本迁移清单未展开。
6. **PyPI `hermes-agent` 的官方性未定论**（元数据署名 Nous Research，但官方列为 Unsupported）。
7. **Windows / macOS / Termux 未实测**；OS 结论全部来自官方文档。
8. **`SOUL.md` 是否纳入受管范围未决**（倾向不纳入，见 §4.2）。
9. **`.env` / `auth.json` 的运行时优先级未做运行验证**（按边界要求未读取）。

---

## 5. 切片结构建议

**总原则**：每个**可接入**家族一片（Grok / Claude / Hermes），每片自带 fixture 与逐家族验收 5 项；
`dsh` 待裁决后再建片（§2.4）。片内沿用批次一范式：新增 `internal/agentlocal/adapter/<family>` 包 +
注册，**控制面零改动**。

### 5.1 每片的固定内容

| 项 | 具体产物 |
|---|---|
| 适配器包 | `internal/agentlocal/adapter/<family>/<family>.go`（实现 `Adapter` 接口的 8 个方法：`ID`/`Capabilities`/`Validate`/`Detect`/`Inventory`/`Plan`/`Apply`/`HealthCheck`） |
| 注册（**3 处**） | `cmd/agent-fleet-agentd/daemon.go`、`cmd/agent-fleet-agentd/oneshot.go`、`cmd/agent-fleet-agentd/doctor.go` 各加一行 `reg.Register(<family>.New())`。⚠️ 批次一文档说的"控制面零改动"成立，但**agent 侧有 3 个注册点**——建议本批次顺手收敛为单一注册函数（属小重构，需在片内声明，不夹带） |
| 写 kit | 复用批次一：TOML 行级手术（Grok，源自 `codex/tomlpatch.go`）、YAML Node 树合并（Hermes，源自 omp `yamlpatch`）、JSON 整树重序列化保留未托管键（Claude，源自 opencode）。**若 kit 需下沉共享，按批次一复核轮先例下沉到 `kit`**（避免三份实现） |
| fixture | 放在 `internal/agentlocal/adapter/<family>/testdata/`（或复用 `internal/agentlocal/adapter/fixture/`），**必须以本机真实文件形状为基线**（Grok 用 §3.2 的实际 `config.toml`；Claude 用 `settings.json` 的 4 个顶层键形状 + `CLAUDE.md` 软链；Hermes 用 65 顶层键的截断样本 + 软链技能） |
| 逐家族验收 5 项 | ① 身份与兼容性：`TestDetectDistinguishesMissingFromUnparseable`（已装/未装/版本不可解析三态可区分，格式不明即显式报错）；② 配置与所有权：`TestMergeWritePreservesUnmanagedAndIsIdempotent` + `TestValidateRejectsUnverifiedBeforeWrite`；③ 生命周期：`TestDriftOnlyFromManagedFields`（幂等、受管 drift、未托管不误报；**安装/升级不在片内**，`Apply(version)` 版本不匹配显式失败）；④ 恢复与一致性：`ManagedFiles` + `ExtractManaged`/`MergeManaged` 两条回退分支 + 软链写穿（`TestManagedBlockWritePreservesSymlink`）；⑤ 验收记录：把 §1.5 / §3.5 / §4.4 的未验证项逐条落进 `Capabilities()` 的 `Reason` 与文档章节 |
| 文档 | 每片在 `docs/adapters-batch-2.md` 追加该家族的"逐家族验收记录"，或另建片级记录（跟随批次一惯例） |

### 5.2 工作量与风险差异

| 片 | 写入面 | 可复用 kit | 主要风险 | 相对工作量 |
|---|---|---|---|---|
| **Grok** | `~/.grok/config.toml`（TOML）、`~/.grok/rules/*.md`、`~/.grok/skills/<name>` | TOML 行级手术（**高复用**） | `requirements.toml` 组织策略 `pin` 会压过用户层 → 观测侧需读实际生效值；`cli.installer` 不一致（不影响契约） | **中低**（4 家中最低） |
| **Claude** | `~/.claude/settings.json`（JSON）、`~/.claude/CLAUDE.md`（**软链**）、`~/.claude/skills/<name>`（**软链**）；MCP 待裁决 | JSON 整树重序列化（高复用）+ 软链写穿（**必须**，批次一已有修复） | ① MCP 写入面待裁决（§1.4）；② `env` 内嵌套键级所有权（密钥与端点同对象）；③ 本机 `CLAUDE.md` 与整个 `skills/` 都是软链 → 软链回归必须覆盖 | **中** |
| **Hermes** | `~/.hermes/config.yaml`（YAML，单文件 65 顶层键）、`~/.hermes/skills/<name>` | YAML Node 树合并（高复用） | ① 无 schema，容错自建；② 未托管面最宽；③ 密钥面最广（嵌套键级所有权）；④ `--version` 多行解析（官方未记载格式） | **中高** |
| **dsh**（待裁决） | `$DSH_HOME/cordis.patch.yml`（**patch 列表数组**，新形态）+ `$DSH_HOME/settings.yaml` | **无可直接复用的 kit**（需新增 patch 行插删 + 行级回退 + 按行投影） | ① 目标版本是 pre-release，上游声明破坏性变更；② 行级（非键级）所有权与现有投影模型的适配需要在契约层明确；③ dump 类命令有写入副作用 | **高**（且含设计工作） |

### 5.3 建议接入顺序

1. **Grok** —— 契约最完整（厂商随包字段全表 + `Managed: fleet/user` 列 + 两个 JSON 健康检查）、
   kit 复用度最高、**且原生支持 `envRefs`**，可顺带闭环批次一遗留的 envRefs 缺口。
2. **Claude** —— 契约文档最广（每个键都有官方页），JSON kit 可复用；
   先交付 version/modelProvider/rules/skills 四项（低风险），**MCP 待 §1.4 裁决**后补。
3. **Hermes** —— 单 YAML 真源、`config check` 可校验版本，但无 schema 且未托管面最宽，
   排在 Claude 之后以积累 YAML 大文件的 fixture 经验。
4. **dsh** —— 待 §2.4 裁决；若选 (c) 只接非 MCP 能力，可插在 Hermes 之前或并行。

**排序理由**：按"契约确定性 × kit 复用度 ÷ 未托管回归面"降序，先把不确定性最低、能立刻验证流程的
家族做掉；`envRefs` 原生支持的两家（Grok、Claude）靠前，可直接检验批次一 §6 缺口 3 的降级路径是否正确。

### 5.4 逐家族切片与验收 5 项（可直接建片）

- **片 A：Grok 适配器**（含 §3.5 未验证项登记）
- **片 B：Claude 适配器**（version/modelProvider/rules/skills；MCP 视 §1.4 裁决再定，若裁决为 (b)/(d)
  则该能力声明 `unsupported` 并在 `Validate` 阶段拒绝）
- **片 C：Hermes 适配器**（含 §4.4 未验证项登记）
- **片 D（条件片）：dsh 适配器** —— 仅在 §2.4 裁决为 (a) 或 (c) 时建立

### 5.5 「不可接入」与再次评估触发条件

**本批次没有"不可接入"的家族**（对照批次一 ZCode：那是由**非官方分发链**导致的排除）。
四家的可执行程序均可回溯到厂商官方渠道，契约均可确认。但以下三项需保留再次评估的触发条件：

| 家族 | 待决项 | 再次评估的触发条件 |
|---|---|---|
| Claude | MCP user 作用域写入路径（§1.4） | 用户给出裁决；**或**官方文档明确 `~/.claude.json` 的外部写入协议（如文件锁 / 官方配置文件化 API）；**或** Claude Code 新增独立可写的 user 作用域 MCP 文件 |
| DeepSeek Harness | 目标版本（pre-release）与 MCP 写入形态（§2.4） | 用户给出裁决；**或** npm `dist-tags.latest` 指向非 `-rc`/`-alpha` 版本且 README 撤下 developer preview 声明；**或** 官方 `--dump-config-schema` 的 `$defs.patchList` 被固化为文档化契约 |
| Grok | 本机 `cli.installer = "npm"` 与官方无 npm 包的不一致（§3.5） | 在能访问 `x.ai` 的环境重取官方安装文档；**或** 官方发布 npm 包；**或** 本机重装后该值改变。**不阻塞接入** |

---

## 6. 契约缺口（在评论中上报，按要求不改 spec 与架构文档）

1. **所有权粒度需要从"顶层键"明确到"嵌套键"与"行"**。批次一三家族的受管键都在配置文件顶层，
   现有描述可以含糊过去；批次二出现三种新粒度：
   - **嵌套对象内的键级**：Claude 的 `settings.json` 中受管 `env.ANTHROPIC_BASE_URL` 与必须未托管的
     `env.ANTHROPIC_AUTH_TOKEN` 同处一个 `env` 对象；Hermes 的 `config.yaml` 同类（`delegation.api_key` 等）。
   - **数组行级**：dsh 的 `cordis.patch.yml` 是 patch 列表数组，所有权单位是"行"，且官方明说 patch
     **整体替换**目标行的 `config`（非深合并）。
   - **目录条目级**：已有的 skills 软链（批次一已覆盖）。
   建议在 §5.4 / 矩阵的"受管字段与所有权"里显式写明这三档粒度与各自的回退语义，
   避免后续家族各自解释（批次一 §6 缺口 1 的同类问题，这次粒度更细）。

2. **MCP `envRefs` 应按家族声明，而不是全局要求**。批次一 §6 缺口 3 提出"若要求 FR-4.1 的 envRefs
   在所有家族可用，需要明确降级路径"。批次二的证据给出了答案：
   - **原生支持**：Grok（`${VAR}`/`${VAR:-default}`，展开位置含 `url`/`command`/`args`/`env` 值/`headers`，
     另有 `bearer_token_env_var`）、Claude（`${VAR}`/`${VAR:-default}`，展开位置含 `command`/`args`/`env`/`url`/`headers`）
   - **明确不支持**：dsh（官方 seam 文把 `${env:VAR}` 列为 **deferred**）
   - **未验证**：Hermes（`mcp_servers.<name>.env` 的展开语义无证据）
   建议把 envRefs 从"统一必需"改为**逐家族能力声明**（`supported`/`unsupported`/`unverified`），
   与现有 `CapabilityDecl` 机制天然契合，无需新增机制。

3. **"Fleet 之外的更高配置层"没有归属**。Grok 有 `requirements.toml`（签名策略，`pin` 键压过用户层）与
   `managed_config.toml`（部署方默认值）；Claude 有 `managed-settings.json`（MDM / 服务端下发）。
   当目标机器部署了这些层时，"Fleet 写入成功但生效值不同"是**正常但不收敛**的状态。
   建议在契约里明确：Fleet 的受管层定位为**用户层**，观测侧必须读**实际生效值**，
   且对"被更高层覆盖"给出可区分的健康态（而不是 drift 永不收敛或伪装 Reconciled）。

4. **`version` 能力声明需要区分"探测"与"安装/升级"**（批次一已提，本批次再次出现）。
   四家均只做了探测确认；Grok 有 `grok update`、Claude 有 `claude install/update`、
   Hermes 有 `hermes update`、dsh 有 npm/pnpm 路径——都未纳入本片。建议声明文本统一为
   `version(probe)` 与 `version(install)` 两档，避免矩阵读者误读。

---

## 7. 未验证清单汇总（按家族，全部保留"未验证"）

**通用**

1. **安装/升级**：四家均仅"探测已验证"，安装/升级不在本片范围；`Apply(version)` 在版本不匹配时
   显式失败（`VersionVerificationFailed` 语义），绝不谎报成功。
2. **macOS / Windows / WSL 实测**：四家均只有 Linux x64 实测（dsh 连 WSL 都未被官方提及）；
   `Validate` 对非 linux `GOOS` 明确拒绝，直到取得对应平台证据。
3. **工件物化**：Skill 软链要求规范缓存 `~/.local/share/agent-fleet/skills/<name>/<digest>` 已存在；
   `FetchArtifact`/bundle 路径未实现（同批次一）。
4. **`envRefs` 指向的变量值**：按 FR-3.3/§11 边界，从不读取、不写入、不入期望状态。

**Claude**：`~/.claude.json` 并发写行为与锁；`apiKeyHelper` ↔ `apiKeyEnv` 的映射设计；
Skills 软链幂等性；macOS/Windows/WSL；`managed-settings.json` 的归属；`availableModels` 族键的范围。

**DeepSeek Harness**：patch 行完整 schema（`$defs.patchList` 未导出）；`env` 值间接引用（官方 deferred）；
WSL 与 Windows 行为差异；`settings.yaml` 并发写协议未实测；设置平面 `llm-pi-ai` namespace 名与 schema；
`--dump-config` 输出作为机器判据的稳定性；桌面端 profile。

**Grok**：本机 `cli.installer = "npm"` 与官方无 npm 包的矛盾；`x.ai` / `docs.x.ai` 本沙箱不可达
（安装脚本未直接取回）；macOS/Windows/WSL；`grok inspect --json` 的结构稳定性；
`settings.json` 完整字段集；`managed_config.toml`/`requirements.toml` 是否纳入受管范围。

**Hermes**：线上文档与本机 0.21.2 未逐页 diff；`hermes doctor`/`status --deep` 完整输出未采集；
`hermes mcp list`/`test` 未执行；MCP `env` 的 `${VAR}` 语义；无机器可读 schema；
PyPI 包的官方性；Windows/macOS/Termux；`SOUL.md` 是否受管；`.env`/`auth.json` 优先级。

---

## 8. 片 A：Grok 适配器逐家族验收记录（KM-33）

本片交付批次二第 1 片：接入 Grok（可执行 `grok`）适配器，沿用批次一范式
（新增 `internal/agentlocal/adapter/grok` + 注册，**控制面零改动**）。基线为 `main` = `059a955d`
（PR #12 已合并，§3 已在 `main`），因此按 §5.1 的第二种情形把验收记录追加在本文件 §3 之后。

### 8.1 交付物

| 项 | 产物 |
|---|---|
| 适配器包 | `internal/agentlocal/adapter/grok/`：`Adapter` 8 方法（`ID`/`Capabilities`/`Validate`/`Detect`/`Inventory`/`Plan`/`Apply`/`HealthCheck`）+ `ManagedFiles`/`ExtractManaged`/`MergeManaged` |
| 注册 | `cmd/agent-fleet-agentd/daemon.go:82`、`oneshot.go:342`、`doctor.go:130` 各一处 `reg.Register(grok.New())`。**未**收敛为单一注册函数：该重构会同时触碰三个入口，为控制回归面留给后续片（片内允许） |
| 写 kit | TOML 行级手术按批次一先例**下沉共享**：新增 `internal/agentlocal/adapter/kit/tomlpatch.go`（`ParseTOML`/`PatchTOML`），codex 改为复用同一实现，`codex/tomlpatch.go` 已删除（不写第二份）。为 Grok 的表内所有权新增三档粒度：`Keys`（表内指定键写入，保留表内未托管键）、`Remove`（清理不再受管的子表表头）、`RemoveKeys`（清理表内点号键，如 `env.TOKEN = "..."`）；并保留原文尾换行。另下沉 `kit.IsNotInstalled`（多步版本探测需要区分"没装"与"这一步失败"） |
| fixture | `internal/agentlocal/adapter/grok/testdata/config.toml`：**= §3.2 记录的代码块形状**（`[cli]`/`[marketplace]`/`[models]`/`[ui]` + `[[marketplace.sources]]`；非 live 文件的逐字节复刻）。另含黄金文件 `testdata/config.after.toml`，供逐字节比对（含尾换行） |
| 测试 | `internal/agentlocal/adapter/grok/grok_test.go`（矩阵 5 项 + 回归断言，全部临时 HOME，绝不触碰真实 `~/.grok`） |

### 8.2 受管面与所有权（实现口径）

- `~/.grok/config.toml`
  - `[models] default`（**只写该键**，表内 `default_reasoning_effort` 等未托管键保留）
  - `[model.fleet]`：`model`/`base_url`/`env_key`/`api_backend`/`name`（表内其它未托管键保留；
    这五个键都进观测投影与 `HealthCheck`，用户改掉任一即触发 drift —— 复核 MINOR 2 的修正）
  - 期望点名的 `[mcp_servers.<name>]`：`command`/`args`；其 `env` 由 Fleet 整表所有
    （`envRefs` → `${VAR}`），两种落盘形态——子表 `[mcp_servers.<name>.env]` 与表内点号键
    `env.TOKEN = "..."`——在期望不再点名时都会被清理（复核 B2）
- `~/.grok/rules/agent-fleet.md`：rules 受管标记块（目录内其它 `*.md` 不读不写）
- `~/.grok/skills/<name>`：Skill 软链（缓存未物化时显式失败）
- 受管键来源按裁决口径取自 `26-config-reference.md` 中 `Managed: user` 的行；`Managed: fleet` 的行
  （如 `features.remote_fetch`）Fleet 不写。

### 8.3 本机实测补证（临时 `GROK_HOME`，未触碰真实 `~/.grok`）

在临时 `GROK_HOME` 写入 `[models] default = "fleet"` + `[model.fleet]`（`model = "grok-4.6"`、
`base_url`、`env_key`）后：

```
$ grok models
You are not authenticated.

Default model: fleet

Available models:
  - grok-4.6
  - grok-4.5
  * fleet (default)
```

即"默认选择器指向 Fleet 命名空间、`[model.fleet]` 作为可选中自定义模型"的写法成立（§3.2 的
`[model.<fleet 命名空间>]` 口径）。补充证据：`grok inspect --json`（1.0.30）的顶层键为
`grokVersion/channel/cwd/projectRoot/projectInstructions/permissions/loginPolicy/hooks/skills/agents/
plugins/marketplaces/mcpServers/lspServers/configSources/externalCompat`，**不暴露生效模型值**；
`configSources.layers` 按 `managed` → `user` → `requirements` 给出实际加载层（本片用它判定是否存在
更高层，失败则回落到 home 内文件）。

### 8.4 逐家族验收 5 项（矩阵口径）

1. **身份与兼容性**：`TestDetectDistinguishesMissingFromUnparseable`——已装/未装/版本不可解析三态可区分
   （程序缺失 `Installed=false` 且非错误；形态不符显式报错）。`TestVersionSourcesAreCLIOnly`——`~/.grok/version.json`
   写入 `1.0.25` 诱饵仍报 CLI 的 `1.0.30`；`version --json` 不可用时回退 `grok --version`；回退形态不符显式报错。
   非 linux → 阶段 1 拒绝。
2. **配置与所有权**：`TestMergeWritePreservesUnmanagedAndIsIdempotent`——以 §3.2 真实 `config.toml` 为输入，
   注释/键序/未托管表（`[cli]`/`[marketplace]`/`[[marketplace.sources]]`/`[ui]`）与表内未托管键
   （`default_reasoning_effort`）逐字节保留，受管键到位，重复 reconcile 无变更。
   `TestUnmanagedMCPEntriesArePreserved`——未点名的 `[mcp_servers.*]` 原样保留。
   `TestMergeWriteMatchesGoldenBytes`——整份 `config.toml` 与 `testdata/config.after.toml` **逐字节**比对
   （覆盖注释/键序/空行/尾换行）。`TestDottedKeyEnvIsRemovedAndConverges`——env 写成表内点号键
   （`env.TOKEN = "..."`）时也能清理并收敛（复核 B2）。
   `TestValidateRejectsUnverifiedBeforeWrite` + `TestValidateRejectsUnparseableConfig`——envRefs 变量名非法、
   provider 缺 model、非法 MCP 名、缺 command、非 linux、既有配置不可解析，全部在**任何写入之前**拒绝且零写入产物。
3. **生命周期**：`TestDriftOnlyFromManagedFields`——幂等、受管改动触发 drift、表外未托管改动不误报、
   **表内未托管键改动也不误报**。安装/升级不在片内：`TestApplyVersionMismatchFailsExplicitly` 在版本不匹配时
   显式失败，不谎报成功。
4. **恢复与一致性**：`TestManagedFilesAndMergeManagedRollback`——`ManagedFiles` 两条（`config.toml`、
   `rules/agent-fleet.md`）、`ExtractManaged` 只提取受管键（未托管的 `default_reasoning_effort` 不进备份）、
   配置受管键级回退与 rules 受管块回退两条分支。`TestRulesManagedBlockPreservesUserContentAndRollback`、
   `TestManagedBlockWritePreservesSymlink`（软链写穿，护栏 #3）、`TestSkillLinkRequiresMaterializedArtifact`。
   另含批次一复核缺陷 1 的家族回归 `TestMCPEntryWithoutArgsConverges` 与逐字节黄金文件比对
   `TestMergeWriteMatchesGoldenBytes`。
5. **验收记录**：本节；§3.5 未验证项逐条落点见 8.7。

### 8.5 `envRefs` 降级路径验证（批次一 §6 缺口 3）

Grok 是原生支持家族。`TestEnvRefsRenderNativelyAndConverge`：`envRefs` 渲染为
`[mcp_servers.<name>.env]` 下的 `${VAR}`（只写变量名，断言不出现字面量回填），且收敛；期望去掉 envRefs 后
陈旧的受管 `env` 子表被清理并继续收敛。能力声明 `MCP = supported`，`Evidence` 引 `07-mcp-servers.md`
（"Grok expands string fields in `[mcp_servers.*]` … at load time"）。

### 8.6 更高配置层：可区分的健康态（§3.4 / §6 缺口 3）

层序（`26-config-reference.md`「How to configure」）为 2 `managed_config.toml` → 3 用户 `config.toml` →
6 `requirements.toml`。因此 **`requirements.toml` 是唯一能压过 Fleet 用户层的文件层**；`managed_config.toml`
在用户层之前，且其值只在 `Managed: fleet` 的键上胜出（厂商原文："Their value applies, except
`features.remote_fetch`. Pin the key instead if it must hold."），而 Fleet 只写 `Managed: user` 的键。

**判据（复核 B1 修正）**：只有"更高层**确实设了该键**、且生效值 ≠ 期望"才算覆盖。
"文件存在但为空/仅注释"（真实 `grok inspect --json` 会报 `role=requirements`、`path` 在 `$GROK_HOME` 内、
`note=empty` 的层）**一律不算覆盖**；`$GROK_HOME` 内文件存在时也绝不报"outside-home 覆盖"——
旧实现按 `len(req)==0` 落判定，会让部署工具留下的一个空占位文件永久冻结本家族的 model/provider/MCP。

**行为（复核 B3 裁决）**：只要**任一**受管键被更高层覆盖，`Plan` 一律返回**空计划**（整片零写入），
`HealthCheck` 返回 `*HigherLayerOverrideError`（点名键、层、生效值与期望值）。这样流水线是"一次干净、
零写入、可区分的失败"，不产生备份/回滚，也不会每轮写+回滚。**代价（明示）**：被 pin 期间，同机本家族的
rules/skills 也一并停写——宁可可见地停下，也不要每轮写入后回滚。

- `TestHigherLayerRequirementPinIsDistinguishable`：真实 pin 时观测投影带 `overriddenBy` 标记（**不报
  Reconciled**）、`Plan` **整片空**、`HealthCheck` 返回 `*HigherLayerOverrideError`。
- `TestPinnedFamilyStopsAllWrites`：pin + 同机 rules drift → `Plan` 仍为空，`Apply` 零写入（rules 文件
  未被触碰），`HealthCheck` 可区分失败（覆盖 B3 的循环场景）。
- `TestEmptyRequirementsLayerIsNotAnOverride`（两个子用例 `empty-file` / `comment-only`）：空文件与仅注释
  文件都不算覆盖、不冻结写入；反向对照证明真正的用户层 drift 仍排变更。
- `TestHigherLayerOutsideHomeIsReported`：`inspect` 报出的 requirements 层路径**不等于** `$GROK_HOME`
  内的 `requirements.toml`（`/etc/grok/requirements.toml` 或 macOS MDM）→ 如实报"被覆盖、生效值不可读"。
- `TestManagedConfigLayerIsNotAnOverride`：`managed_config.toml` 设同键时用户层胜出，不误报覆盖。
- 本机真实二进制复跑（临时 `HOME`/`GROK_HOME`，`Inspect` 不注入）：空 `requirements.toml` → 无覆盖标记、
  正常排 config drift；写入 `[models] default = "pinned-model"` → `overriddenBy = config.model: requirements.toml`
  且 `Plan` 为空。

**契约缺口（已在评论上报）**：现有投影/门禁只有"收敛 / drift"两态，没有"被更高层覆盖"。本片把它表达为
"观测投影带标记 + `Plan` 整片空 + `HealthCheck` 可区分失败"；只读 reconcile 仍会把它呈现为 drift
（无法既不报 drift 又不报 Reconciled）。建议在 §6 缺口 3 / §7.1 明确该三态与归属。

### 8.7 §3.5 未验证项逐条落点

| # | §3.5 项 | 落点 |
|---|---|---|
| 1 | `cli.installer = "npm"` 与官方无 npm 包的矛盾 | `Capabilities(version).Reason`；本文件 §8.3 |
| 2 | `x.ai` / `docs.x.ai` 本沙箱不可达 | `Capabilities(version).Evidence`（结论取自随包文档与二进制内嵌 URL） |
| 3 | macOS / Windows / WSL 无实测 | `Capabilities(version).Reason` + `Validate` 对非 linux 明确拒绝 |
| 4 | `grok inspect --json` 结构未锁定 | `Capabilities(modelProvider).Reason`；§8.3（1.0.30 不暴露生效模型值，本片只用其 layer 列表，失败回落 home 文件） |
| 5 | `settings.json` 完整字段集未取全 | `Capabilities(rules).Reason`（本片不使用该文件） |
| 6 | `managed_config.toml` / `requirements.toml` 归属未决 | §8.6 + `Capabilities(modelProvider).Reason`（Fleet 受管层定位=用户层；`managed_config` 只在 `Managed: fleet` 键上胜出，Fleet 不写该类键） |
| 7 | install / upgrade | `Capabilities(version).Reason`（探测 supported；`Apply(version)` 不匹配显式失败） |
| 8 | **环境变量层**（复核 MINOR 5，超出 §3.5） | `Capabilities(modelProvider).Reason` 的未验证清单：本机实测 `GROK_DEFAULT_MODEL=grok-4.5` 会压过用户层（`grok models` → `Default model: grok-4.5`），本片不判定该层 → 记入未验证，留后续片 |

### 8.8 本片上报的契约缺口（不改 spec/架构文档，只在评论里报告）

1. **归一化 MCP 契约只携带 `command`/`args`/`envRefs`**，因此 §3.2 列出的
   `url`/`headers`/`bearer_token_env_var` 受管面在本片不可达（`Validate` 对无 `command` 的条目显式拒绝）。
   建议扩展 `adapter.MCPEntry`，否则"Grok 的 HTTP MCP 受管"只是纸面能力。
2. **"更高配置层"三态未定义**（见 8.6）。
3. **§3.1/§3.2 把 `managed_config.toml` 描述为"比 Fleet 用户层更高的层"与厂商文档不符**：
   它是层序第 2 层，位于用户 `config.toml`（第 3 层）**之前**，只对 `Managed: fleet` 的键胜出。
   本片按厂商文档处理（只有 `requirements.toml` 构成覆盖），建议修正 §3.2 表述。
4. **环境变量层（`GROK_*`）未在契约中表达**：`GROK_DEFAULT_MODEL` 等会压过用户层（本机实测），
   属"更高层覆盖"的同一族；本片只处理了两个文件层，环境变量层记为未验证（§8.7 第 8 项）。

### 8.9 核查退回（B1/B2/B3 + MINOR）的修复记录（2026-09-26，复核轮）

核查在 `fb7acc7` 上给了 3 项阻断与 7 项 MINOR。逐条修复与新增回归如下（每条都用"去掉修复即失败"验证过）：

| # | 问题 | 修复 | 回归断言 |
|---|---|---|---|
| B1 | 空的 `$GROK_HOME/requirements.toml` 触发**假**全量覆盖（真实二进制可复现） | `readRequirements` 返回"文件是否存在"；文件在 home 内时只按它实际设置的键判定；`outsideRequirementsLayer` 用 inspect 的 `path` 判定"home 外"，路径等于 home 时不报 | `TestEmptyRequirementsLayerIsNotAnOverride`（空/仅注释两子用例）+ 真实二进制复跑 |
| B2 | 点号键写法的 `env.TOKEN = "..."` 永不被 `Remove` 清掉 → 每轮重排变更 | `kit.TOMLPatch` 新增 `RemoveKeys`（表内键前缀删除）；`applyConfig` 对点名 MCP 条目清 `env` 两类形态，并**写后断言**受管 env 已收敛，否则显式失败 | `TestDottedKeyEnvIsRemovedAndConverges`（无 envRefs 清理 / envRefs 覆盖点号键两场景） |
| B3 | "被覆盖 + 其它受管项 drift" 进入写→失败→回滚循环 | 只要任一受管键被覆盖，`Plan` 返回**空计划**（整片停写）；`HealthCheck` 照旧给可区分失败；代价写进 §8.6 与 `Capabilities(modelProvider).Reason` | `TestPinnedFamilyStopsAllWrites`；`TestHigherLayerRequirementPinIsDistinguishable` 改为断言空计划 |
| 1 | `PatchTOML` 丢尾换行 | `PatchTOML` 记录原文是否以 `\n` 结尾并在返回前恢复 | `TestMergeWriteMatchesGoldenBytes`（逐字节 + 尾换行断言） |
| 2 | `api_backend`/`name` 写而不可观测 | 期望/观测投影与 `HealthCheck` 纳入这两个键；requirements 覆盖判定同步 | 既有收敛测试即可暴露（缺省即 drift） |
| 3 | §8.7 第 6 项落点缺 `managed_config` 归属 | `Capabilities(modelProvider).Reason` 补"归属未决"与 `Managed: fleet` 说明 | 本表与 §8.7 |
| 4 | `MergeManaged` 对非 map 受管值静默跳过 | 改为显式报错（与 default 分支口径一致） | 代码路径 + 既有 `TestManagedFilesAndMergeManagedRollback` |
| 5 | `GROK_*` 环境变量层会压过用户层 | 记入 `Capabilities(modelProvider).Reason` 未验证清单与 §8.7 第 8 项 | 文档 |
| 6 | 表内新键插在尾部空行之后；fixture 措辞 | 新键插入让过尾部空行；§8.1 措辞改为"= §3.2 记录的代码块形状" | 黄金文件（含空行形态） |
| 7 | 重复小工具已是第 4 份 | 按批次一 §7 已登记的"下沉未做"留给后续片，本片不夹带 | — |

---

## 9. 片 B：Claude 适配器逐家族验收记录（KM-34）

本片交付批次二第 2 片：接入 Claude（可执行 `claude`，Anthropic Claude Code）适配器。
沿用批次一范式（新增 `internal/agentlocal/adapter/claude` + 注册，**控制面零改动**）。
基线为 `main` = `e76a1d1`（PR #13 已合并，§8 已在 `main`），因此按 KM-34 的开工须知把
验收记录追加为 §9 及之后（不另建片级文档）。**本片全程不触碰真实 `~/.claude` 与
`~/.claude.json`**：所有测试用 `t.TempDir()` 作为 HOME 根，Probe/Doctor/ManagedSettingsDir
一律注入桩，绝不执行真实二进制、绝不读真实 `/etc/claude-code`（护栏 #12）。

### 9.1 交付物

| 项 | 产物 |
|---|---|
| 适配器包 | `internal/agentlocal/adapter/claude/`：`Adapter` 8 方法（`ID`/`Capabilities`/`Validate`/`Detect`/`Inventory`/`Plan`/`Apply`/`HealthCheck`）+ `ManagedFiles`/`ExtractManaged`/`MergeManaged` |
| 注册（3 处，跟随现状） | `cmd/agent-fleet-agentd/daemon.go:82`、`oneshot.go:342`、`doctor.go:130` 各一处 `reg.Register(claude.New())`。片 A 未收敛，本片不夹带该重构 |
| 写 kit | JSON 整树重序列化（读→只改受管键→`MarshalIndent`→写后重解析→`kit.AtomicWrite`）落在 claude 包内；软链写穿直接复用批次一 `kit.AtomicWrite` 的 `resolveWritePath`（批次一复核轮修复 #3），rules/skills 复用 `kit.WriteManagedBlock`/`kit.EnsureSkillLink`。**未新增共享 kit**（重复项已在评论上报，见 §9.8） |
| fixture | `internal/agentlocal/adapter/claude/testdata/settings.json`：**= §1.2 记录的本机真实形状**（顶层 `env`/`model`/`statusLine`/`enabledPlugins` 四键；`env` 内含必须未托管的凭据键位，值全部为占位符）。黄金文件 `testdata/settings.after.json` 供逐字节比对（含尾换行）。`CLAUDE.md` 软链与 `skills/` 软链集合由 fixture 测试构造并断言（软链不宜提交为仓库对象） |
| 测试 | `internal/agentlocal/adapter/claude/claude_test.go`（矩阵 5 项 + 更高层覆盖 + 回归，全部临时 HOME） |
| 注册实测 | `go run ./cmd/agent-fleet-agentd doctor --home <临时> --json` → `"adapters": ["claude","codex","grok","omp","opencode"]` |

### 9.2 受管面与所有权（实现口径）

- `~/.claude/settings.json`（JSON 整树重序列化）
  - `model`（string；官方 `### model`，优先级 `--model` > `ANTHROPIC_MODEL` > settings）
  - `env.ANTHROPIC_BASE_URL`（**嵌套键级所有权**：`env` 下其余键逐键保留）
  - **绝不读、绝不写、绝不入期望状态/投影/fixture 的键**：`env.ANTHROPIC_AUTH_TOKEN`
    及其他 `env` 键、`statusLine`、`enabledPlugins`、`permissions`、`hooks`、`apiKeyHelper`、
    `availableModels` 等。本机 `env` 实测有 4 键（`ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` /
    `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` / `API_TIMEOUT_MS`）——**只记录键名，凭据值全程未读取**。
- `~/.claude/CLAUDE.md`：rules 受管标记块（FR-5.1）；**本机该路径是软链**
  （`-> ~/.config/plexus/personal/rules/global.md`），原子写必须写穿，块外逐字节保留。
- `~/.claude/skills/<name>`：Skill 软链（FR-6.3）；**本机该目录 40+ 条目全部是软链**
  （`-> ../../.agents/skills/<name>`），Fleet 只管理自己点名的条目，缓存未物化时显式失败。
- **完全不写**：`~/.claude.json`（应用自持活状态文件）；`managed-settings.json` 只读（见 §9.6）。
- 凭据边界：归一化 `provider.apiKeyEnv` 非空时 `Validate` 显式拒绝（见 §9.4 第 2 项）。

### 9.3 本机实测补证（未读凭据值）

```
$ claude --version
2.1.270 (Claude Code)

$ claude doctor            # 只读
Claude Code doctor

Running: npm-global (2.1.270)
Commit: 97ecbf7abeb4
Platform: linux-x64
Managed settings (remote): not fetched — not available with a custom ANTHROPIC_BASE_URL
Organization policy: not fetched with a custom ANTHROPIC_BASE_URL
...

$ ls -la ~/.claude/CLAUDE.md
lrwxrwxrwx ... /home/shelwin/.claude/CLAUDE.md -> /home/shelwin/.config/plexus/personal/rules/global.md

$ ls -la ~/.claude/skills/          # 全部为软链
ask-matt -> ../../.agents/skills/ask-matt
codebase-design -> ../../.agents/skills/codebase-design
...

$ python3 -c '<只打印键与类型>'      # ~/.claude/settings.json
top keys: ['env', 'model', 'statusLine', 'enabledPlugins']
env keys: ['ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_BASE_URL', 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC', 'API_TIMEOUT_MS']
```

- **`claude doctor` 的 `Running:` 行不是第 1 行**（实测第 1 行是 `Claude Code doctor`，空行后才是
  `Running: ...`）：解析器逐行扫描该形态，而非取首行；这一点写入 §9.7 第 1 项。
- 两行托管设置/组织策略均为 `not fetched ...` → 本机**无**远程层（远程信号的反向对照 fixture 即此形态）。
- 本机 `/etc/claude-code/` **不存在**（文件层更高配置未部署），故文件层覆盖由 fixture 构造验证。

### 9.4 逐家族验收 5 项（矩阵口径）

1. **身份与兼容性**：`TestDetectDistinguishesMissingFromUnparseable`——已装/未装/版本不可解析
   三态可区分（程序缺失 `Installed=false` 且非错误；形态不符显式报错）。
   `TestVersionProbeFallsBackToDoctor`——`--version` 形态不符时回退 `claude doctor` 的
   `Running: ...` 行；两条路径都不符时显式报错。非 linux → 阶段 1 拒绝。
2. **配置与所有权**：`TestMergeWritePreservesUnmanagedAndIsIdempotent`——以本机真实
   `settings.json` 形状为输入，未托管顶层键（`statusLine`/`enabledPlugins`）与 **`env` 内未托管键**
   逐键保留，受管键到位，重复 reconcile 无变更。`TestMergeWriteMatchesGoldenBytes`——整份
   `settings.json` 与 `testdata/settings.after.json` **逐字节**比对（含尾换行）。
   `TestDriftOnlyFromManagedFields`——受管 `model`/受管端点各自触发 drift；未托管顶层改动与
   **同一 `env` 对象内的未托管键改动**都不误报。`TestValidateRejectsUnverifiedBeforeWrite` +
   `TestValidateRejectsUnparseableSettings`——MCP 请求、非空 `apiKeyEnv`、缺 endpoint、非法
   skill 名、非 linux、既有配置不可解析、`env` 非对象、更高层文件不可解析，全部在**任何写入之前**
   拒绝且零写入产物。
3. **生命周期**：`TestDesiredScopedProjection`（未点名即不管理、绝不 drift；点名即 drift）、
   `TestSkillLinksAreSymlinkSetAndIdempotent`。安装/升级不在片内：
   `TestApplyVersionMismatchFailsExplicitly` 版本不匹配显式失败，不谎报成功。
4. **恢复与一致性**：`TestManagedFilesAndMergeManagedRollback`——`ManagedFiles` 两条
   （`settings.json`、`CLAUDE.md`）；`ExtractManaged` 只提取 `model` 与 `env.ANTHROPIC_BASE_URL`
   （**`ANTHROPIC_AUTH_TOKEN` 绝不进备份**），未知受管键让 `MergeManaged` 显式失败；
   受管键级回退保留未托管的 `env` 凭据键与其余顶层键。`TestRulesManagedBlockPreservesUserContentAndRollback`、
   `TestManagedBlockWritePreservesSymlink`（**软链写穿**，护栏 #3）、`TestSkillLinkRequiresMaterializedArtifact`。
5. **验收记录**：本节；§1.5 未验证项逐条落点见 §9.7。

### 9.5 MCP 裁决 (d) 的落地

- `Capabilities(mcp) = unsupported`，`Reason` 记录 KM-32 §1.4 的四选项结论与依据
  （`~/.claude.json` 为应用自持活状态文件、无外部写入协议；`.mcp.json` 需要契约里不存在的
  project root 入参且未信任目录停在 `Pending approval`；`claude mcp add -s user` 属新形态）。
- `Validate` 经 `adapter.CheckCapabilities` 在**任何写入之前**拒绝 MCP 请求；
  `TestValidateRejectsUnverifiedBeforeWrite` 断言消息为 `capability "mcp" is unsupported`
  且 settings/skills 零写入产物。`TestMCPDeclarationIsUnsupported` 锁定声明。
- `claude mcp list` 会连接 MCP server，本片不把它用作离线健康检查；健康检查只用
  `claude --version` + `claude doctor`（均只读）。

### 9.6 更高配置层：可区分的健康态（§1.4 / §6 缺口 3）

**文件层**：`managed-settings.json`（Linux 为 `/etc/claude-code/`，macOS
`/Library/Application Support/ClaudeCode/`，Windows `C:\Program Files\ClaudeCode\`）在层序上
压过用户 `settings.json` 且不可覆盖；官方另支持 `managed-settings.d/*.json` drop-in 片段，
按字母序合并（后者覆盖前者）。适配器只读该层，**不纳入受管写入范围**。

**判据**：只有"该层确实设了 `model` 或 `env.ANTHROPIC_BASE_URL`、且值不同于期望"才算覆盖；
同值不算；空文件/占位文件不算（`TestEmptyManagedSettingsIsNotAnOverride`、
`TestManagedSettingsSameValueIsNotAnOverride`）。片段合并由
`TestManagedSettingsFragmentsMergeAlphabetically` 锁定。

**行为（沿用 §8.6 跨片口径）**：只要**任一**受管键被覆盖，`Plan` 一律返回**空计划**（整片零写入）、
`HealthCheck` 返回 `*HigherLayerOverrideError`（点名键、层、生效值与期望值）。流水线因此是
"一次干净、零写入、可区分的失败"，不产生备份/回滚，也不会每轮写+回滚。

**远程/服务端层**：admin console / MDM plist / Windows registry 的生效值适配器读不到。
本片用 `claude doctor` 的 `Managed settings (remote):` / `Organization policy:` 两个固定前缀行做
**保守信号**：只要该行的措辞不是"未获取/不可用"之类否定形态，就报"被覆盖、值不可读"
（`TestRemoteManagedSettingsAreReported`）；本机实测的 `not fetched ...` 形态不误判
（`TestRemoteNotFetchedIsNotAnOverride`）。误判方向是**停写**而非假装收敛（未验证项见 §9.7 第 6 项）。

- `TestHigherLayerManagedSettingsOverrideIsDistinguishable`：真实 pin 时观测投影带 `overriddenBy`、
  生效值替换为 pin 值（**不报 Reconciled**）、`Plan` 整片空、`HealthCheck` 可区分失败。
- `TestManagedSettingsBaseURLOverrideIsReported`：`env.ANTHROPIC_BASE_URL` 被覆盖同样可区分。
- `TestHigherLayerManagedSettingsOverrideIsDistinguishable` 同时断言 **pin 期间 rules 停写**
  （空计划 Apply 零写入），覆盖 §8.6 的循环场景。

### 9.7 §1.5 未验证项逐条落点

| # | §1.5 项 | 落点 |
|---|---|---|
| 1 | macOS / Windows / WSL 无实测 | `Capabilities(version).Reason` + 各能力 `Reason` 的未验证清单；`Validate` 对非 linux 明确拒绝 |
| 2 | 安装/升级未纳入 | `Capabilities(version).Reason`（探测 supported；`Apply(version)` 不匹配显式失败） |
| 3 | `~/.claude.json` 并发写行为/锁未验证 | `Capabilities(mcp).Reason`（不作为写入面的理由 + 未验证声明）；`~/.claude.json` 全程不读不写 |
| 4 | `env.ANTHROPIC_BASE_URL` 与 shell 导出优先级无本机交互验证 | `Capabilities(modelProvider).Reason` 未验证清单 |
| 5 | `apiKeyHelper` ↔ `apiKeyEnv` 映射未设计 | `Capabilities(modelProvider).Reason` + `Validate` 对非空 `apiKeyEnv` 显式拒绝（unverified）；§9.8 第 1 项作为契约缺口上报 |
| 6 | Skills 软链幂等性 | `Capabilities(skills).Reason` + `TestSkillLinksAreSymlinkSetAndIdempotent`（重复 reconcile 零变更）后声明 supported |
| 7 | `availableModels` / `enforceAvailableModels` 族键范围、`managed-settings.json` 归属 | `Capabilities(modelProvider).Reason`（存在但不纳入受管范围；归属留待范围决策）+ §9.6 |
| 8 | **超出 §1.5**：`ANTHROPIC_MODEL` 环境变量层 | 官方 precedence 明确 `--model` > `ANTHROPIC_MODEL` > settings.model；本片不判定该层 → 记入 `Capabilities(modelProvider).Reason` 未验证清单 |
| 9 | **超出 §1.5**：`claude doctor` 文案解析 | §9.3（`Running:` 非首行 → 逐行扫描）+ §9.6（远程行按固定前缀与否定词解析，未验证，误判方向为停写） |

### 9.8 本片上报的契约缺口（不改 spec/架构文档，只在评论里报告）

1. **归一化 MCP 契约只携带 `command`/`args`/`envRefs`**，与 §8.8 第 1 项同源；本片因裁决 (d)
   不实现 MCP，故不受影响，但缺口仍在。
2. **归一化 provider 契约携带 `apiKeyEnv`，而 Claude 没有承载"环境变量名"的设置键**：
   凭据由用户登录态或 `apiKeyHelper` 解决，`apiKeyEnv` → `apiKeyHelper` 的映射未设计。
   本片按矩阵"未验证即拒绝"显式失败；需要后续设计片给出映射，或扩展 provider 契约表达
   "凭据由用户自理"。
3. **"更高配置层"三态未定义**（同 §8.8 第 2 项）：本片把 Claude 表达为
   "观测投影带 `overriddenBy` + `Plan` 整片空 + `HealthCheck` 可区分失败"；只读 reconcile 仍会把它
   呈现为 drift（无法既不报 drift 又不报 Reconciled）。
4. **`HigherLayerOverrideError` / `overriddenBy` 标记与片 A（grok）是第二份实现**：
   建议按批次一复核轮先例下沉 `kit`；本片按批次一 §7"下沉未做"登记，不夹带跨片重构。
5. **JSON 读/写工具是第二份**（opencode 私有 JSONC 实现 vs claude 纯 JSON 实现）：
   同样登记为"下沉未做"；opencode 支持 JSONC 注释、Claude 官方为无注释 JSON，语义并不完全重合。
6. **远程/服务端托管设置行的文案解析未验证**：只按两个固定前缀行与几个否定词判定，
   多版本文案变化可能误判（误判方向为保守停写）。
7. **`managed-settings.d/*.json` 的合并顺序**按官方文档实现为"字母序后者覆盖"，
   但未在本机实测（本机 `/etc/claude-code/` 不存在）。

### 9.9 本片自身验证

- `go build ./...`、`go vet ./internal/agentlocal/... ./cmd/...` 通过。
- `go test ./...` 全绿（含 claude 包 22 个测试函数与既有五家族回归）。
- `go run ./cmd/agent-fleet-agentd doctor --home <临时> --json` → 注册表含 `claude`。
