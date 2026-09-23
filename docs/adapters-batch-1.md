# 批次一适配器：接入前确认、逐家族验收与范围决策（KM-24）

本文件是第 4 片（批次一适配器：Codex / OMP / OpenCode / ZCode）的**接入前确认记录**、
**逐家族验收记录**与**范围决策**，依据 `docs/agent-support-matrix.md` 的统一适配契约与
逐家族验收 5 项编写。结论一律附证据（本机实测命令与输出，或官方文档 URL）；没有证据的
条目保留 **未验证**，不按同名模型/同名包推测。

接入实测环境：Linux x86_64（本机），Go 1.27.0。测试全部使用临时 HOME，
不触碰真实 `~/.codex`、`~/.omp`、`~/.config/opencode`（护栏 #12）。

---

## 0. 一览

| 家族 | 运行时来源 | 本机实测版本 | 适配器 | 结论 |
|---|---|---|---|---|
| Codex | OpenAI Codex CLI（standalone/npm 发行） | `codex-cli 0.154.0` | `internal/agentlocal/adapter/codex` | 已接入（Linux 实测） |
| Oh-My-Pi (OMP) | npm `@oh-my-pi/pi-coding-agent`（bin `omp`） | `omp/17.4.0` | `internal/agentlocal/adapter/omp` | 已接入（Linux 实测，配置契约取自官方文档） |
| OpenCode | npm `opencode-ai`（bin `opencode`） | 本机未安装 | `internal/agentlocal/adapter/opencode` | 已接入（全部结论取自官方文档，标注未验证） |
| ZCode | 上游为 Z.ai 官方 ZCode；**本机分发链是第三方非官方 npm `zcode-app-cli`** | `zcode-app-cli 3.11.2-24` / `zcode-runtime 0.16.5` | **未实现** | **范围决策**（见 §4） |

---

## 1. Codex

### 1.1 接入前确认

| 确认项 | 结论 | 证据 |
|---|---|---|
| 运行时来源 | OpenAI Codex CLI；本机为 standalone 发行（`~/.codex/packages/standalone/releases/<版本>-<triple>`，含 `0.154.0-x86_64-unknown-linux-musl`） | 本机 `ls ~/.codex/packages/standalone/releases` |
| 可执行程序与版本探测 | 可执行名 `codex`；`codex --version` → `codex-cli 0.154.0`（解析规则：前缀 `codex-cli ` + semver，形态不符即显式报错，不猜） | 本机实测 `codex --version` |
| 配置文件路径与格式 | `~/.codex/config.toml`，TOML | CLI 自带 help：`-c, --config <key=value> Override a configuration value that would otherwise be loaded from ~/.codex/config.toml`（本机 `codex --help` 输出） |
| 指令文件 | `~/.codex/AGENTS.md`（本机该路径为软链，指向用户全局规则文件 → 适配器只替换受管标记块，块外内容逐字节保留） | 本机 `ls -la ~/.codex/AGENTS.md`（symlink） |
| Skill 目的地 | `~/.codex/skills/<name>/SKILL.md`（非递归一层） | 本机 `~/.codex/skills/.system/{imagegen,openai-docs,plugin-creator}/SKILL.md` |
| MCP 表示 | `[mcp_servers.<name>]` 表，字段 `command` / `args` / `env` / `url` / `bearer_token_env_var`；适配器只写 `command`+`args`（stdio） | `codex mcp add --help`（本机）：`<NAME> (--url <URL> \| -- <COMMAND>...)`、`--env <KEY=VALUE>`；本机 config.toml 既有 `[mcp_servers.serena]`、`[mcp_servers.openaiDeveloperDocs]` |
| 模型/provider 表示 | 顶层 `model`；Fleet provider 命名空间 `[model_providers.fleet]`（`name`/`base_url`/`env_key`/`wire_api="responses"`） | 二进制内置 schema（本机 `strings ~/.local/bin/codex`）：`ModelProviderInfo` 字段表含 `base_url`/`env_key`/`wire_api`/`requires_openai_auth`，且含提示 `wire_api = "chat" is no longer supported. ... set wire_api = "responses"`；本机 config.toml 顶层 `model` 键 |
| 受管字段与所有权 | 受管：`model`、`model_provider`、`[model_providers.fleet]`、期望点名的 `[mcp_servers.<name>]`、`AGENTS.md` 受管块、`skills/<name>` 软链。其余键（`approval_policy`、`projects`、`tui`、`plugins`、未点名的 MCP 条目…）不读不写 | fixture 断言（`codex_test.go`：注释、`approval_policy`、`[projects.*]`、未托管 `[mcp_servers.serena]` 全部保留） |
| 健康检查方式 | 配置可解析 + 受管键到位 + 版本探测与期望一致；**不验证外部服务可用性**（FR-4.3） | `HealthCheck` + fixture 断言 |
| Linux / macOS / WSL 支持组合 | **Linux：本机实测**。macOS / Windows / WSL：**未验证**（本机无该平台证据；github.com 在本环境不可达，无法取官方文档） | 无 → 适配器 `Validate` 对 `GOOS != linux` 明确拒绝 |
| 能力声明 | version ✅ / modelProvider ✅ / MCP ✅（`command`+`args`）/ Skills ✅ / Rules ✅；**MCP `envRefs` 未验证**（没有证据表明 `${VAR}` 会在 `[mcp_servers.*].env` 展开，直接写入会把字面量交给服务进程） | `Capabilities()`；envRefs 请求在阶段 1 被拒绝（fixture 断言） |

### 1.2 逐家族验收（矩阵 5 项）

1. **身份与兼容性**：已装 → `Installed=true` + 版本；未装 → `Installed=false`（非错误）；已装但版本不可解析 → **显式错误**（不伪装成未安装）。fixture：`TestDetectDistinguishesMissingFromUnparseable`。非 Linux → 阶段 1 拒绝。
2. **配置与所有权**：`TestMergeWritePreservesUnmanagedAndIsIdempotent` 覆盖渲染、读取、未托管保留（含 JSON 注释级内容：TOML 采用**行级手术写**，注释与键序保留）、幂等；`envRefs` 在写入前拒绝（`TestValidateRejectsUnverifiedBeforeWrite`）。
3. **生命周期**：重复 reconcile 无实质变更（幂等断言）；受管改动触发 drift、未托管改动不触发（`TestDriftOnlyFromManagedFields`）。**安装/升级：未验证**——command installer（§20.2）不在本片范围，`Apply(version)` 在版本不匹配时**显式失败**，不谎报成功。
4. **恢复与一致性**：受管文件声明 `ManagedFiles`（config.toml、AGENTS.md）供 reconciler 备份/恢复使用；受管字段级回退（外部编辑场景）有 fixture（`TestRulesManagedBlockPreservesUserContent` 的 `MergeManaged` 断言 + config.toml 受管键回退路径）。daemon 与 one-shot 共用同一 `Executor`（AD-5，切片 3 已锁定）。
5. **验收记录**：本节；未验证项见 §5。

---

## 2. Oh-My-Pi (OMP)

### 2.1 接入前确认

| 确认项 | 结论 | 证据 |
|---|---|---|
| 运行时来源 | npm `@oh-my-pi/pi-coding-agent`（bin `omp`），仓库 `github.com/can1357/oh-my-pi`，官网 `omp.sh` | `curl -s https://registry.npmjs.org/@oh-my-pi%2fpi-coding-agent`（latest `18.2.11`；`bin {"omp":"dist/cli.js"}`；license MIT；engines `bun >=1.3.14`） |
| ⚠️ 同名包区分 | 无 scope 的 npm `oh-my-pi` 是**无关项目**（`github.com/acidsugarx/oh-my-pi`，bin `oh-my-pi`，配置 `.oh-my-pi.jsonc`）；npm `omp` 是占位包。适配器只认 `omp` 可执行程序与 `~/.omp/agent/` 布局 | `curl -s https://registry.npmjs.org/oh-my-pi`；`curl -s https://registry.npmjs.org/omp` |
| 可执行与版本探测 | `omp --version` → `omp/17.4.0`（本机实测）。`name/version` 输出格式的文字约定**未验证**（官方只说明"打印已安装版本"）→ 解析器要求 `omp/<semver>`，不符即显式报错 | 本机 `omp --version`；docs/cli-reference.md（`--version, -v  Print the installed version and exit.`） |
| 配置文件路径与格式 | `~/.omp/agent/config.yml`（YAML；官方称 canonical write target，已存在的 `config.yaml` 就地更新） | docs/settings.md「Where settings live」；本机 `~/.omp/agent/config.yml` 存在且键与文档吻合 |
| MCP 表示 | **独立文件** `~/.omp/agent/mcp.json`，顶层 `mcpServers.<name>.{type,command,args,env}`（MCP **不在** config.yml 内） | docs/mcp-config.md；docs/config-usage.md |
| 模型/provider 表示 | 模型角色 `modelRoles.default: <provider>/<model>`；自定义 provider 与凭据在 `~/.omp/agent/models.yml`（`baseUrl`/`api`/`apiKey`/`models`），`apiKey` 写**环境变量名**即可（OMP 自行按名字解析，值永不入期望状态） | docs/settings.md「Models」；docs/providers.md；本机 `modelRoles.default: zai/glm-5.3` 与选择器格式一致 |
| 指令文件 | `~/.omp/agent/AGENTS.md`（用户级上下文文件；另有 `RULES.md` 为 always-apply 规则） | docs/context-files.md |
| Skill 目的地 | `~/.omp/agent/skills/<name>/SKILL.md`（非递归一层） | docs/skills.md |
| 受管字段与所有权 | 受管：`modelRoles.default`、`providers.fleet`（models.yml）、`mcpServers.<name>`、`AGENTS.md` 受管块、`skills/<name>` 软链；其余键不读不写（YAML 走 Node 树合并，注释保留） | fixture 断言（`symbolPreset`、`composer.shape`、行尾注释、未托管 MCP server 全部保留） |
| 健康检查方式 | 受管键到位 + 文件可解析 + 版本一致 | `HealthCheck` + fixture 断言 |
| Linux / macOS / WSL 支持组合 | 官方平台行：macOS · Linux · Windows · bun ≥ 1.3.14（无需 WSL）。**本机仅 Linux 实测**；macOS/Windows 未实测 → 适配器只允许 linux | README「Install」；适配器 `Validate` |
| 能力声明 | version ✅ / modelProvider ✅ / MCP ✅（`command`+`args`）/ Skills ✅ / Rules ✅；**MCP `envRefs` 未验证**（文档只写字面 `env` 值） | `Capabilities()`；envRefs 请求阶段 1 拒绝 |
| 其他未验证 | `composer.shape` 未登记在文档 schema（权威以 `omp config list` 为准）——适配器不依赖它；Bun 安装的版本钉选写法与产物校验和**未验证** | docs/settings.md（"不是完整 schema"） |

### 2.2 逐家族验收（矩阵 5 项）

1. **身份与兼容性**：已装/未装/版本不可解析三态可区分（`TestDetectDistinguishesMissingFromUnparseable`）；非 Linux 与非 `omp/<semver>` 输出显式拒绝/报错。
2. **配置与所有权**：`TestMergeWritePreservesUnmanagedAndIsIdempotent` 覆盖 config.yml/models.yml/mcp.json/AGENTS.md 四处的渲染、读取、未托管保留与幂等；provider 缺 `model`（选择器无法渲染）与 envRefs 在写入前拒绝（`TestValidateRejectsBeforeWrite`）。
3. **生命周期**：幂等、受管 drift、未托管不误报（`TestDriftOnlyFromManagedFields`）。**安装/升级未验证**（同上，installer 不在本片）。
4. **恢复与一致性**：`ManagedFiles` 覆盖四个受管文件；受管字段级回退 fixture 覆盖 YAML 与 rules 块；MCP 条目级回退。
5. **验收记录**：本节；未验证项见 §5。

---

## 3. OpenCode

### 3.1 接入前确认（本机未安装该家族：全部结论来自官方文档）

| 确认项 | 结论 | 证据 |
|---|---|---|
| 运行时来源 | npm `opencode-ai`（bin `opencode`），latest `1.18.32`；12 个平台子包（linux/darwin/windows × x64/arm64，含 musl 与 baseline） | https://registry.npmjs.org/opencode-ai （`bin {"opencode":"bin/opencode.exe"}`、`os ["darwin","linux","win32"]`、`optionalDependencies` 平台包） |
| 可执行与版本探测 | 可执行名 `opencode`；`--version/-v` 语义为 "Print version number"；**输出格式未验证**（文档无示例）→ 解析器只接受裸 semver（可带 `v` 前缀），不符即显式报错 | https://opencode.ai/docs/cli/ |
| 配置文件路径与格式 | `~/.config/opencode/opencode.json`，JSON **或 JSONC**；schema `https://opencode.ai/config.json`；项目级 `opencode.json`；`$XDG_CONFIG_HOME` 是否被遵循**未验证**（文档一律写 `~/.config/opencode/`） | https://opencode.ai/docs/config/ ；https://opencode.ai/docs/troubleshooting/ |
| 模型/provider 表示 | 顶层 `model: "<provider>/<model-id>"`；自定义 OpenAI 兼容 provider：`provider.<id>.{npm:"@ai-sdk/openai-compatible", name, options.baseURL, options.apiKey, models.<id>.name}`；`apiKey` 支持 `{env:VAR}` 间接引用（不落明文） | https://opencode.ai/docs/providers/ ；https://opencode.ai/docs/config/ |
| MCP 表示 | 顶层 `mcp.<name>.{type:"local", command:[...], environment, enabled, timeout}`（remote 为 `type:"remote"` + `url`） | https://opencode.ai/docs/mcp-servers/ |
| 指令文件 | 项目根 `AGENTS.md`；全局 `~/.config/opencode/AGENTS.md` | https://opencode.ai/docs/rules/ |
| Skill 目的地 | 全局 `~/.config/opencode/skills/<name>/SKILL.md`；目录名必须匹配 `^[a-z0-9]+(-[a-z0-9]+)*$` 且与 frontmatter `name` 一致 | https://opencode.ai/docs/skills/ |
| 受管字段与所有权 | 受管：`model`、`provider.fleet`、`mcp.<期望点名条目>`、`AGENTS.md` 受管块、`skills/<name>` 软链 | fixture 断言（`$schema`、`theme`、未托管 `mcp.user-owned` 保留） |
| 健康检查方式 | 受管键到位 + 配置可解析 + 版本一致 | `HealthCheck` + fixture 断言 |
| Linux / macOS / WSL 支持组合 | npm 声明 `darwin/linux/win32` × `arm64/x64`；Windows 官方**建议 WSL**（"can run directly on Windows, we recommend using WSL"）。**本机未安装，任何平台都未实测** → 适配器只允许 linux | https://registry.npmjs.org/opencode-ai/1.18.32 ；https://opencode.ai/docs/windows-wsl/ |
| 能力声明 | version ✅（flag 有文档；输出格式未验证）/ modelProvider ✅ / MCP ✅（含 `environment` 的 `{env:VAR}` 间接引用）/ Skills ✅（拒绝不合规 skill 名）/ Rules ✅ | 见上表各 URL |

### 3.2 逐家族验收（矩阵 5 项）

1. **身份与兼容性**：`TestDetectDistinguishesMissingFromUnparseable`（三态可区分；格式未知时显式报错而非猜测）；非 Linux 阶段 1 拒绝。**真实二进制探测未实测**（本机未安装）。
2. **配置与所有权**：`TestMergeWritePreservesUnmanagedAndIsIdempotent` 覆盖 JSONC 读取、受管键渲染、未托管键保留、幂等；不合规 skill 名（`Bad_Name`）在写入前拒绝（`TestValidateRejectsBeforeWrite`）。
3. **生命周期**：幂等、受管 drift、未托管不误报（`TestDriftOnlyFromManagedFields`）。**安装/升级未验证**。
4. **恢复与一致性**：`ManagedFiles` 覆盖 opencode.json 与 AGENTS.md；受管字段级回退路径有 fixture。
5. **验收记录**：本节；未验证项见 §5。

### 3.3 已知限制（已在代码注释与本节声明）

- JSON 无注释语义：合并写整树重新序列化，**未托管键与取值全部保留，但用户注释不保留**（TOML/YAML 两个家族不受影响：分别走行级手术与 Node 树合并）。
- 全局配置只写 `opencode.json`；若用户既有 `opencode.jsonc`，适配器不会自动改用该文件（未纳入受管范围，不会破坏它）。
- OpenCode 官方**没有**定义 `AGENTS.md` 的标记块语义；适配器写入的 `# BEGIN/END agent-fleet managed` 对 OpenCode 只是纯文本，所有权靠"块外逐字节保留"保证。

---

## 4. ZCode：范围决策（未实现适配器）

### 4.1 已确认的事实（含硬证据）

| 事实 | 证据 |
|---|---|
| 本机 `zcode` 来自**第三方非官方** npm 包 `zcode-app-cli`（作者 kingsword09，MIT，自述 "Unofficial terminal client for the ZCode agent runtime"） | `curl -s https://registry.npmjs.org/zcode-app-cli`（HTTP 200，`dist-tags.latest 3.14.3-27`，本机版本 `3.11.2-24`，`repository git+https://github.com/kingsword09/zcode-cli.git`，`author Kingsword`） |
| 该包内嵌的运行时来自**官方** ZCode Desktop 安装包：`vendor/extraction.json` 记录 `source: https://cdn-zcode.z.ai/zcode/electron/releases/3.11.2/linux-x64/ZCode-3.11.2-linux-x64.deb`、`cliVersion 0.16.5`（=本机 `zcode --version` 第二行） | 包内 `zcode-runtime.lock.json` / `vendor/extraction.json`（本机读取）；`curl -I` 该 CDN URL → HTTP 200、`application/x-debian-package` |
| 上游项目公开：`github.com/zai-org/ZCode`（Apache-2.0）、文档站 `zcode.z.ai`（`/en/docs/install`、`/en/docs/configuration`、`/en/docs/skill`、`/en/docs/mcp-services` 均 HTTP 200） | `curl` 状态码与 `api.github.com/repos/zai-org/ZCode` |
| 官方 npm registry **没有** Z.ai 官方发布的 CLI 包（`@zcode/*`、`@zai/*`、`zcode-runtime` 全部 404） | 逐个 `curl -s -o /dev/null -w '%{http_code}' https://registry.npmjs.org/<name>` |
| 配置契约冲突：第三方包 README 只记载 `~/.zcode/cli/setting.json`，而本机实际生效的是 `~/.zcode/cli/config.json`（结构与包内 `config.example.json` 对应）；官方 docs 站点**没有** CLI 配置页 | README（npm `readme` 字段）；本机 `~/.zcode/cli/config.json`；官方 docs 侧栏清单 |
| `rules/instructions` 文件：npm README 与官方 docs 均**无记载** → 未验证 | README grep `AGENTS.md` 0 命中 |
| Skills：官方 docs 明确 `~/.zcode/skills/<name>/SKILL.md`（本机该目录为软链集合）；MCP：官方 docs 有 MCP 页（stdio/SSE/HTTP、`mcpServers` JSON） | https://zcode.z.ai/en/docs/skill ；https://zcode.z.ai/en/docs/mcp-services |

### 4.2 决策与理由

**不实现 `zcode` 适配器，报范围决策**（对应本片范围第 5 条："若运行时来源或能力无法确认，停下来报范围决策，不要静默跳过或写成功"）。理由：

1. **分发链不是官方的**：本机可执行程序来自第三方重新打包的 npm 包，我们把它的配置契约写进适配器，等于把 Fleet 的受管写入建立在一个非官方、可随时变更或下架的第三方包上；
2. **配置契约未文档化**：真正生效的 `~/.zcode/cli/config.json` 在官方文档中不存在（README 指向 `setting.json`），受管字段的权威性无法确认；写入用户配置属于不可轻率的动作（护栏 #3）；
3. **指令文件契约未验证**：rules/instructions 文件无任何文档证据，无法声明 Rules 能力。

**未注册 = 显式失败，不是静默跳过**：适配器未注册时，流水线阶段 1 直接以 `DesiredStateInvalid`（`adapter "zcode" not registered`）失败，且有测试锁定（`TestPipelineFailsOnUnregisteredFamily`）。

### 4.3 请裁决的选项（供贾维斯/用户选择，本片不自行扩大范围）

- **(a) 只认官方运行时来源**：以官方 ZCode Desktop 自带 CLI 为目标，先由范围决策确认其安装与配置契约（官方 docs 目前无 CLI 配置页），再决定是否接入；
- **(b) 接受第三方分发链**：明确接受 `zcode-app-cli` 作为运行时来源，并按本机实测的 `~/.zcode/cli/config.json` schema 接入，把"文档地位未验证"列入验收记录；
- **(c) 本批次移除 ZCode**，回到七家族范围（矩阵与架构文档的修改需单独授权，本片不改文档）。

---

## 5. 未验证清单（逐项，全部保留"未验证"）

**通用（三家族共有）**

1. **版本安装/升级**：command installer（§20.2）不在本片范围；`Apply(version)` 在版本不匹配时显式失败（`VersionVerificationFailed` 语义），绝不谎报成功。
2. **macOS / Windows / WSL**：三家族均只有 Linux 实测（OpenCode 连 Linux 也未安装实测）；`Validate` 对非 linux `GOOS` 明确拒绝。
3. **工件物化**：Skill 软链要求规范缓存 `~/.local/share/agent-fleet/skills/<name>/<digest>` 已存在；`FetchArtifact`/bundle 路径未实现（后续切片），缺失时**显式失败**而非静默跳过。
4. **MCP 环境变量间接引用**：Codex 与 OMP **未验证** → 请求 `envRefs` 时阶段 1 拒绝；OpenCode 有文档化的 `{env:VAR}` 语法，已按该语法渲染。
5. **`envRefs` 指向的变量值**：按 FR-3.3/§11 边界，从不读取、不写入、不入期望状态。

**Codex**：macOS/Windows 支持；MCP `envRefs`；`codex mcp add --env` 的字面量语义之外的间接引用。

**OMP**：`omp/<version>` 输出格式的文字约定；`composer.shape` 的 schema 语义；MCP `env` 的 `${VAR}` 展开；Bun 安装的版本钉选写法与产物校验和；macOS/Windows 实测；config.yml 之外的 `RULES.md`/`SYSTEM.md` 等指令来源未纳入受管范围（只管理 `AGENTS.md` 受管块）。

**OpenCode**：本机未安装 → 真实二进制探测、真实配置文件读写均未实测；`--version` 输出格式；`$XDG_CONFIG_HOME`；`npm install -g opencode-ai@<version>` 的文档化写法；`AGENTS.md` 标记块语义（官方无定义）；JSON 合并写不保留注释。

**ZCode**：见 §4（未实现）。

---

## 6. 本片同时闭环的切片 3 遗留项

| 遗留项 | 裁决与实现 | 断言 |
|---|---|---|
| 目录归属（§3.6） | **采纳 §3.6**：`internal/adapter` → `internal/agentlocal/adapter`、`internal/reconciler` → `internal/agentlocal/reconciler`（家族实现落 `internal/agentlocal/adapter/<family>`）；§3.6 的依赖护栏用可执行测试锁定 | `internal/agentlocal/adapter/layout_test.go`：controller/server 不得 import agentlocal、server 不得 import 家族包、agentlocal 不得 import controller/server、domain 不 import 运行时包、家族之间互不 import |
| 每操作绑定观测（§4.4 门禁条件 2） | 节点在流水线结束后、`OperationResult` **之前**上报与该 `operationId` 绑定的观测（携带双侧投影摘要 + `canonicalizationVersion` + `inventorySeq` + 适配器健康）；周期 inventory 也改为按"当前期望快照"计算双侧投影（不再是 `canonicalizationVersion="0"` 占位） | `cmd/agent-fleet-agentd/daemon_test.go`：绑定观测先于终态上线且与 verify 证据一致；`internal/agentlocal/inventory` 投影来自 `adapter.Inventory` |
| `observed_states` 单行 upsert 清空绑定 | **裁决：保留"每操作最近一份绑定观测"**——新增表 `operation_observations`（`PRIMARY KEY(machine_id, operation_id)`），周期报文（无 `operationId`）**结构性**无法触碰绑定行；门禁改读 `LatestBound` | `internal/store/sqlite/enrollment_store_test.go`：周期报文以更高 seq 更新主行后绑定仍在；同操作落后 seq 拒绝、较新 seq 替换；无绑定返回 `ErrNotFound`；`internal/controller/deployment`：周期报文不改变门禁结论 |
| `healthFromObservation` 恒为空 | 观测新增 `adapterHealth`（proto `ObservedState.adapter_health = 9` + domain + 服务端转换），门禁条件 4 可用同一份观测的健康结果；两侧冲突时取严 | `internal/controller/deployment/controller_test.go`：观测健康可独立满足条件 4；冲突（观测 failed / verify passed）不通过 |
| 跨家族 `canonicalizationVersion` 相等断言 | **裁决：多家族共用一个节点侧规则版本** `agentlocal-projection-v1`（`adapter.ProjectionCanonicalizationVersion`），任一家族投影规则变化即 bump 使旧结论整体失效（宁停不错）；家族级 schema 版本另由 `DetectedAgent.SchemaVer` 上报 | `adapter/projection.go` 注释记录决策；三家族与 fixture 共用该常量，聚合摘要可比 |
| 顺手项（不阻塞、可选） | **未做**：陈旧执行权锁恢复入口（`Reconciler.Recover()` 无调用者）与 `Superseded(Skipped)` 口径未在本片改动，避免夹带无关重构 | — |
| 明确不在本片 | FR-12.7 基线比对入口与 `AwaitingConfirmation` 生产入口 → 第 6 片 | — |

### 契约缺口（在评论中上报，按要求不改 spec 与架构文档）

1. **适配器输入形态未在 §5.4 明确**：`render.go` 把 provider 归一化进 `agents.<family>.config`，而 skills/mcp/rules 位于快照顶层。本片把 `adapter.AgentDesiredState` 扩展为同时携带 `Skills`/`MCP`/`Rules`（核心仍只依赖接口，I-1 不被破坏），但建议架构明确该输入契约（避免后续家族各自解释）。
2. **`fixture` 曾从 `config` 内读 skills**，与 render 输出不一致（切片 3 占位）；本片已统一为快照顶层 `skills`。
3. **MCP `envRefs` 在部分家族无法满足**：Codex/OMP 没有环境变量间接引用的证据，按矩阵"未验证即拒绝"处理；若要求 FR-4.1 的 `envRefs` 在所有家族可用，需要在范围上明确允许"该家族不支持 envRefs"的降级路径。
