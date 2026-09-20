# agent-fleet MVP 架构设计文档

| 项目 | 内容 |
|---|---|
| 文档版本 | v1.0（评审稿） |
| 日期 | 2026-09-14 |
| 需求基准 | 《agent-fleet MVP Specification》v0.1（2026-09-10），本文件唯一需求来源 |
| 文档范围 | 需求拆解与需求分析、系统架构设计 |
| 目标读者 | 项目所有者、实现工程师（含 AI 编码代理）、测试工程师 |
| 关联验收 | spec 第 34 节 12 项验收标准（A–L）、第 39 节 Definition of Done |

---

## 1. 文档概述

### 1.1 目的

本文档基于 spec v0.1 完成三项工作：

1. **需求拆解与需求分析**：将 spec 的叙述性需求分解为可编号、可验证、可追踪的功能需求（FR）与非功能需求（NFR），并明确系统边界与非目标；
2. **架构设计**：给出满足上述需求的总体架构、组件划分、领域模型、关键机制设计、接口契约、数据设计、安全设计与测试策略；
3. **决策固化**：将 spec 第 36 节的 8 项架构决策（AD-1～AD-8）整理为决策记录，并补充少量实现级决策建议。

实现团队（含 AI 编码代理）应以本文档 + spec 为实现依据；两者冲突时以 spec 为准，并回写修订本文档。

### 1.2 系统一句话定义

`agent-fleet` 是面向 Linux / macOS / WSL 开发机的**单操作者 Agent 工作站机群管理器**：以期望状态（desired state）为中心，通过 Web UI 集中管理多台开发机上的 Agent CLI 版本、模型/Provider 配置、MCP 配置、Skills 与 rules，持续检测 drift；以 `agentd` 守护进程作为持续管理通道，SSH 作为一等公民的 bootstrap / 兜底 / SSH-only 管理通道。

它管理的是 **Agent 开发环境的状态**，不是 Agent 工作负载（spec §1、§4.2）。

### 1.3 术语表

| 术语 | 定义 |
|---|---|
| 控制面 / Server | 单进程 `agent-fleet-server`：Web UI + REST API + SSE + 各控制器 + SQLite |
| 节点 / agentd | 单二进制 `agent-fleet-agentd`，运行于受管开发机；`daemon` 与 `oneshot` 两种执行模式 |
| 期望状态（Desired State） | 操作者声明的机群应达到的 Agent 环境状态（profile + overrides 的归一化结果） |
| Effective Desired State | Machine × AgentProfile × Skill 修订 × ModelProvider × overrides × adapter schema 版本解析后的最终期望状态 |
| DesiredStateSnapshot | 有效期望状态的不可变快照，附确定性 SHA-256 摘要；generation 仅在其内容变化时递增 |
| 受管投影（Managed Projection） | 适配器声明自己拥有的配置字段的集合；drift 只在受管投影上计算 |
| drift | 观测到的受管状态 ≠ 有效期望受管状态 |
| Reconcile | 将节点实际状态向期望状态收敛的一次受控执行（plan → backup → apply → verify） |
| Operation | 每次变更产生的不可变操作记录（含步骤、结果、脱敏输出） |
| Deployment | 跨多台机器的 canary/批量发布编排资源 |
| Enrollment | 新节点以一次性短时效 token 换取 Fleet CA 签发的 mTLS 客户端证书的过程 |
| Operation Bundle | SSH-only 模式下，服务端打包的不可变操作包（manifest + 引用的 Skill 工件），自带 SHA-256 摘要 |
| 受管 include 文件 | 由 Fleet 生成、可显式安装到 `~/.ssh/` 的 OpenSSH include 片段 `agent-fleet.conf` |

### 1.4 阅读指引

- 只关心"做什么"：读第 2 章（需求分析）与第 13.3 节（验收映射）；
- 只关心"怎么做"：读第 3～12 章；
- 关键权衡：读第 14 章（架构决策记录）与第 17 章（风险）。

---

## 2. 需求拆解与需求分析

### 2.1 问题域与产品定位

**问题**（spec §2）：使用多台开发机的开发者需要在每台机器上重复：安装/升级 Agent CLI、配置模型端点、更新 MCP、同步 Skills、维护 rules、发现版本/配置 drift、升级失败后恢复机器、维护远程开发机 SSH 条目。同一概念配置在不同 Agent（Codex / OMP / OpenCode）中路径、格式、Skill 目录、MCP 表示各不相同。

**产品主张**：一个控制面把**归一化的期望状态**映射为各 Agent 的原生表示，并持续报告每台机器是否与期望状态一致。核心价值排序：

1. 一处声明、多机一致（期望状态 + reconcile）；
2. drift 可见、可解释、可一键收敛（受管投影 diff）；
3. 变更安全（备份、回滚、canary、健康门禁）;
4. 双通道可达（daemon 持续管理 + SSH 兜底/SSH-only）。

### 2.2 干系人与运行假设

| 干系人 | 关注点 |
|---|---|
| 单操作者（唯一人类角色） | 通过 Web UI 声明期望、审批变更、处理 drift 与故障 |
| 控制面宿主机 | 承载 `agent-fleet-server`（默认 loopback HTTP + 0.0.0.0 agent gRPC） |
| 受管开发机 | Linux amd64/arm64、macOS amd64/arm64、WSL（视同 Linux）；能被操作者本机 OpenSSH 客户端非交互访问 |
| Agent 生态 | Codex、OMP（oh-my-pi）、OpenCode 三个 MVP 家族；后续 Claude Code、Gemini CLI 等 |

**运行假设**（来自 spec，违反则功能退化）：

- A1 操作者的本机 OpenSSH 客户端已配置好可用凭据（密钥/agent），不支持密码认证（§13.2）；
- A2 daemon 模式要求节点能主动出站访问控制面 `advertiseURL`（§7.2）；不可达的节点以 SSH-only 模式仍可管理；
- A3 引用的环境变量（如 `OPENAI_API_KEY`）的**值**由节点环境自行提供，Fleet 全程不读取（§3.6、§18）；
- A4 安装器命令（如 `npm install -g ...`）及其运行时由操作者负责，Fleet 只保证 argv 直执行与安装后验证（§8.3、§20）。

### 2.3 核心用户旅程（验收视角）

| # | 旅程 | 关键步骤（spec §5） | 对应验收 |
|---|---|---|---|
| U1 | 添加机器 | Web UI 输入 SSH alias 与管理模式 → 服务端解析 SSH 配置 → 非交互连接性测试 → 探测 OS/arch/home/shell/服务管理器/已装 Agents → 展示结果 → 一键 Bootstrap agentd → 装用户服务 → 一次性 enrollment → 出站 mTLS 流建立 → `AgentConnected=True` | A |
| U2 | 应用 profile | 为多台机器指派同一 profile（版本/模型端点/Skills/MCP/rules）→ 控制面计算有效期望状态并递增 generation → 在线节点即时收到新 generation；SSH-only 节点在操作者发起 reconcile 或 Deployment 命中时经一次性 SSH 操作收敛（MVP 不做后台轮询） | B、C、D、E、F |
| U3 | 发现并收敛 drift | 用户手工改了受管字段 → `agentd` 上报不同受管摘要 → UI 显示 `Drifted: True` 与逐字段 diff → 操作者点击 `Reconcile` → 成功后 drift 清除 | G |
| U4 | 修复 agentd | 流断开但 SSH 可达（`AgentConnected=False, SSHReachable=True`）→ UI 暴露 `Repair agentd` → 服务端经 SSH 诊断、替换/重启二进制或服务 → 等待重连 | I |
| U5 | 金丝雀发布 | 创建 Deployment（canary=1、batchSize=1、maxUnavailable=1、pauseOnFailure=true）→ 金丝雀健康门禁通过后才继续下一批 → 注入失败立即暂停 | H |
| U6 | 回滚 | 对失败变更回滚到上一次成功受管状态；版本回滚 = 用同一安装器装回先前观测版本；不可能时明确报 `RollbackUnsupported` | J |

### 2.4 功能需求拆解

优先级定义：**P0** = MVP 验收必需；**P1** = MVP 范围内但可在验收演示中弱化（spec §4.1 in-scope 且未进入 §34）；本表不引入 P2。每条注明 spec 出处章节。

#### FR-1 机器与机群管理

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-1.1 | 以既有 SSH host alias 或显式 HostName/User/Port/ProxyJump/IdentityFile 路径注册机器；资源名在类型内唯一 | P0 | §5.1、§8.2 |
| FR-1.2 | `managementMode` 取值 `agentd`（daemon 为首选收敛路径，SSH 仍保留用于 bootstrap/repair）或 `ssh`（永远经 SSH oneshot 收敛）；UI 文案 "agentd + ssh" 对应领域取值 `agentd` | P0 | §8.2、§5.1 |
| FR-1.3 | SSH 探测：非交互连通性测试，采集 OS、架构、home 目录、shell、服务管理器、当前已装支持 Agents | P0 | §5.1、§34.A |
| FR-1.4 | Bootstrap：按探测结果选择 `agentd` 工件（GOOS/GOARCH），上传安装，受支持时安装用户级服务，完成 enrollment，等待出站 mTLS | P0 | §5.1、§7.3、§12.1 |
| FR-1.5 | Repair agentd：SSH 诊断 → 必要时替换二进制/重启服务 → 等待重连；对不兼容/过期 agentd 可经 SSH 替换 | P0 | §5.4、§7.3 |
| FR-1.6 | Machine 状态条件（conditions）维护：`SSHReachable`、`AgentConnected`、`InventoryReady`、`Drifted`、`Reconciled`、`Degraded`；每个 condition 含 status/reason/message/lastTransitionTime；条件由控制器显式写入，禁止从另一条件推导 | P0 | §22 |
| FR-1.7 | 在线判定：心跳 15s、离线阈值 45s（可配置）；离线但 SSH 可达的机器保留为可修复对象 | P0 | §10.4 |
| FR-1.8 | Machine 删除/编辑（PATCH）；profile 重指派触发有效期望状态重算 | P0 | §23.1 |

#### FR-2 Agent 版本管理

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-2.1 | Profile 中每个 Agent 家族可指定 enabled + 精确版本；解析后的快照禁止 `latest` 等浮动版本 | P0 | §8.3、§20.1 |
| FR-2.2 | command installer：argv 数组直执行（不经过 `sh -c`），`${VERSION}` 按参数逐个替换；捕获退出码与 stdout/stderr（限额），脱敏已知敏感环境变量名 | P0 | §8.3、§20.2、§29.9 |
| FR-2.3 | 安装后必须用适配器探测验证版本；失败报 `VersionVerificationFailed` | P0 | §20.2、§30.3 |
| FR-2.4 | 版本回滚 = 用同一安装器安装先前观测版本；不可行时报 `RollbackUnsupported`，不得谎报成功 | P0 | §20.3 |
| FR-2.5 | 适配器可内置经验证的默认安装命令，但一切默认值必须可被 profile 覆盖（如 OMP 安装方式因环境而异） | P0 | §15.2、§20.2 |
| FR-2.6 | 未来 installer-driver 层（npm/brew/mise/签名二进制）不在 MVP，但安装路径须为 driver 抽象留位 | P1 | §8.3、§37 |

#### FR-3 模型/Provider 配置管理

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-3.1 | 归一化 ModelProvider 资源：`name`、`type`（MVP：openai-compatible）、`endpoint`、`apiKeyEnv`（仅环境变量名）；不存储任何凭据值 | P0 | §8.4、§18 |
| FR-3.2 | 适配器把归一化值翻译为各 Agent 原生配置字段（受管字段合并进现有文件） | P0 | §16、§18 |
| FR-3.3 | 服务端与 agentd 不读取、不上传、不同步、不持久化 `apiKeyEnv` 指向的值；UI 仅可做不接触秘密值的连通性检查，带鉴权的模型 API 校验延期 | P0 | §3.6、§18 |
| FR-3.4 | Provider CRUD（REST + UI） | P0 | §23.4 |

#### FR-4 MCP 管理

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-4.1 | 归一化 MCP 条目：`command`、`args`、`envRefs`（配置键 → 环境变量名映射，值永不出现在期望状态中） | P0 | §19 |
| FR-4.2 | 适配器翻译为 Codex/OMP/OpenCode 原生 MCP 配置；同一归一化条目至少渲染进 Codex 与 OpenCode（验收 F） | P0 | §34.F |
| FR-4.3 | 保留未托管的既有 MCP 条目（在原生格式允许的部分所有权范围内）；适配器健康检查只验证语法/注册，不验证外部服务可用性 | P0 | §19、§34.F |

#### FR-5 Rules / 指令文件管理

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-5.1 | Profile 携带全局 rules 内容；适配器以受管标记块（`# BEGIN/END agent-fleet managed`）或 `ownership=whole-file`（须在 UI 可见）方式管理指令文件 | P0 | §16.2、§8.3 |
| FR-5.2 | whole-file 所有权之外不得覆盖用户未知内容（护栏 #3） | P0 | §38 |

#### FR-6 Skill 管理

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-6.1 | Skill 来源：Git（repo + ref + path）或控制面本地目录；有效性判据 = 解析目录存在且含 `SKILL.md` | P0 | §8.5、§17.1 |
| FR-6.2 | 解析：可变引用 → 精确 commit/revision + 内容 digest + 不可变工件（服务端缓存 `<data-dir>/artifacts/skills/<sha256>/...`） | P0 | §17.1–17.2 |
| FR-6.3 | 节点物化：规范缓存 `~/.local/share/agent-fleet/skills/<skill-name>/<digest>/`，适配器 symlink 到各 Agent Skill 目录；不支持 symlink 时用复制模式，以内容 digest 为 drift 信号 | P0 | §17.3 |
| FR-6.4 | Skill 更新只在控制面解析出新 revision/digest 且操作者指派后改变期望状态；禁止 `latest` 浮动部署；自动更新策略非 MVP | P0 | §17.4 |
| FR-6.5 | REST：Skill CRUD + `POST /skills/{id}/resolve` + `GET /skills/{id}/revisions`；UI 列表含 Name/Source/Requested Ref/Resolved Revision/Digest/Machines | P0 | §23.3、§24.5 |
| FR-6.6 | 工件节点侧 digest 校验（gRPC `FetchArtifact` 与 SSH bundle 两条路径都必须） | P0 | §29.11、§13.4、§17.2 |

#### FR-7 期望状态解析与快照

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-7.1 | 服务端把每台机器解析为一个不可变 `DesiredStateSnapshot`；输入 = AgentProfile + 引用 Skill 的精确修订/digest + 引用 ModelProvider + Machine overrides + adapter schema 版本 | P0 | §9 |
| FR-7.2 | 快照含确定性 SHA-256 摘要；**generation 仅在有效期望状态变化时递增**（内容寻址，而非每次保存都递增） | P0 | §9 |
| FR-7.3 | 快照只含归一化期望状态与不可变 Skill 工件引用；不含秘密值 | P0 | §9、§3.6 |
| FR-7.4 | `GET /profiles/{id}/render?machine=<id>` 支持渲染预览 | P0 | §23.2 |

#### FR-8 观测状态与 drift

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-8.1 | agentd 采集：已装 Agent 版本、适配器受管配置投影、Skill 目标修订/内容 digest、MCP 受管投影、rules 受管投影、适配器健康、服务健康 | P0 | §10.1 |
| FR-8.2 | 观测摘要按归一化受管状态计算；适配器不得对整份配置文件哈希（只哈希自有字段投影） | P0 | §10.2 |
| FR-8.3 | drift 判定：`observed 受管状态 != effective desired 受管状态` → `Drifted=True`；未托管本地配置不得触发 drift | P0 | §10.3 |
| FR-8.4 | 上报节奏（默认，均可配置）：心跳 15s；离线阈值 45s；全量 inventory 在连接建立时、每次操作后、每 5 分钟；本地 inventory 发现受管摘要变化后立即上报 drift | P0 | §10.4 |
| FR-8.5 | UI 提供 `Show Diff`：受管字段的 desired vs observed 逐项对比（如 `codex.config.model: gpt-5.6 → other-model`、`skill/superpowers: 91c7f3 → 3a82d1`） | P0 | §5.3、§34.G |

#### FR-9 Reconcile（收敛执行）

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-9.1 | 本地 reconciler 13 阶段固定流水线（validate → inventory → plan → backup → 应用版本/配置/Skills/MCP/rules → 健康检查 → 再 inventory → 校验 desired==observed → 提交成功）；daemon 与 oneshot 模式复用同一实现（AD-5） | P0 | §14.1、§3.4 |
| FR-9.2 | 可变步骤失败：停止后续步骤 → 保留诊断 → 用本次操作备份自动恢复 → 恢复后再 inventory → 同时上报原始失败与恢复结果；恢复也失败 → `Degraded=True` 并停止向该机继续自动 rollout | P0 | §14.2 |
| FR-9.3 | 幂等：同一期望快照执行两次，第二次不得产生实质变更 | P0 | §14.3 |
| FR-9.4 | 手动触发（UI/API `POST /machines/{id}/reconcile`）与 Deployment 驱动两条入口；SSH-only 节点经 `agentd oneshot plan/apply --bundle` 收敛 | P0 | §23.1、§13.4 |
| FR-9.5 | 性能：无 drift 的在线节点 reconcile plan 通常 <1s（不含网络/包管理操作） | P0 | §35.3 |
| FR-9.6 | 变更前备份至 `~/.local/share/agent-fleet/backups/<operation-id>/`，保留每机最近 10 次（可配置）；备份元数据足以立即回滚 | P0 | §16.3 |

#### FR-10 Deployment（机群发布编排）

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-10.1 | Deployment 资源：显式机器列表、`targetGeneration`、策略（canary 数、整数 batchSize、maxUnavailable、pauseOnFailure） | P0 | §8.6、§21.1 |
| FR-10.2 | 发布算法：解析目标 → 过滤不可用/降级机器 → 金丝雀批 → 要求 reconcile 成功 + 健康 → 逐批继续 → 失败策略触发立即暂停；无需时间型渐进发布 | P0 | §21.2 |
| FR-10.3 | 健康门禁（缺一不可）：操作成功 + apply 后 inventory 完成 + 观测受管摘要 == 期望摘要 + 适配器健康检查通过 | P0 | §21.3 |
| FR-10.4 | 回滚 = 创建指向既往 recorded generation 的新 Deployment；历史不可变 | P0 | §21.4 |
| FR-10.5 | Deployment 暂停/恢复/回滚 API 与 UI 进度（含逐机结果） | P0 | §23.5、§24.6 |

#### FR-11 Enrollment、证书与传输安全

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-11.1 | Bootstrap 流程 10 步（建 Machine → SSH 探测 → 一次性短时效 token → SSH 下发二进制+引导配置 → agentd 生成本地私钥 → 携 Machine ID+token+CSR 连 enrollment 端点 → 校验 token → Fleet CA 签发客户端证书 → token 失效 → 私钥/证书以仅用户可读权限落盘 → 开启常规 mTLS Connect 流） | P0 | §12.1 |
| FR-11.2 | Fleet CA 服务端首次启动本地创建；Agent 证书有限期，agentd 在到期前经已认证 mTLS 通道续期；CA 私钥留在控制面主机且权限 0600；外部 PKI/HSM 非 MVP | P0 | §12.2 |
| FR-11.3 | 节点无入站管理端口；连接方向恒为 agentd → 控制面（AD-3） | P0 | §11.1 |

#### FR-12 SSH 传输与集成

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-12.1 | 使用系统 OpenSSH 客户端（`ssh -G`、`ssh -o BatchMode=yes <alias> -- <cmd>`、`scp`），经受限执行器调用；本地命令构造不用 shell，远程参数经专用工具安全引用；保留 Host/Include/ProxyJump/identity 选择/known_hosts/ssh-agent 等既有行为（AD-4） | P0 | §13.1 |
| FR-12.2 | 认证沿用操作者本地 OpenSSH 已支持的非交互方式；不支持密码提示；服务端不摄取 SSH 私钥内容入库 | P0 | §13.2、§29.2 |
| FR-12.3 | host-key 策略：保留标准校验；UI 必须呈现 host-key 失败，禁止静默 `StrictHostKeyChecking=no` | P0 | §13.3 |
| FR-12.4 | SSH-only reconcile：服务端构建不可变操作 bundle（`manifest.json` = DesiredStateSnapshot + digest；`artifacts/skills/<digest>/...` 仅含被引用工件；bundle 自带 SHA-256 摘要）→ scp 上传到远端临时目录 → `oneshot plan` → `oneshot apply` → 删除临时目录；agentd 在 plan/apply 前校验全部工件 digest；agentd 缺失时先上传临时兼容二进制 | P0 | §13.4 |
| FR-12.5 | SSH 错误分类不坍缩：DNSResolveFailed / HostKeyVerificationFailed / AuthenticationFailed / ConnectionTimeout / RemoteCommandFailed / UnsupportedPlatform | P0 | §30.1 |
| FR-12.6 | OpenSSH include 导出：渲染 `~/.ssh/agent-fleet.conf`（preview / 导出 / 显式确认后安装更新）；仅为 Fleet 持有显式连接字段的机器导出；经既有 hostAlias 导入的机器不重复导出；绝不静默改写用户主 SSH 配置；动因 = Codex Desktop Remote SSH 的 OpenSSH 配置发现集成（§40） | P0 | §13.5、§34.K |

#### FR-13 agentd 协议

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-13.1 | protobuf + 双向流 gRPC over TLS；服务 `FleetAgentService.Connect / FetchArtifact` 与 `FleetEnrollmentService.Enroll / RenewCertificate` | P0 | §11 |
| FR-13.2 | 消息集：Agent→Server：Hello / Heartbeat / ObservedState / OperationStarted / OperationProgress / OperationResult / LogEvent；Server→Agent：Welcome / DesiredStateChanged / ExecuteOperation / CancelOperation / RequestInventory；LogEvent 仅运维日志，禁止 Agent 会话内容 | P0 | §11.3–11.4 |
| FR-13.3 | 重连：指数退避 + 抖动；重连必以 Hello + 全量观测快照开始；服务端对重复的操作结果投递幂等 | P0 | §11.5 |
| FR-13.4 | 版本协商：快照与协议消息均带 `protocolVersion`/`schemaVersion`；服务端拒绝不支持的 daemon 主版本；agentd 在 Hello 中上报支持的 adapters/schema 版本；服务端不下发 agentd 看不懂的期望状态，改由 UI 提示升级 | P0 | §31 |
| FR-13.5 | daemon 模式 Skill 工件经认证的 `FetchArtifact` gRPC 流按 digest 下载并本地校验 | P0 | §13.4、§17.2 |

#### FR-14 Web UI 与 API

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-14.1 | REST `/api/v1`：machines / profiles / skills / providers / deployments 五类资源 CRUD + 动作端点（probe/bootstrap/repair/inventory/reconcile/rollback、resolve、pause/resume/rollback、render）+ `GET /events`（SSE） | P0 | §23 |
| FR-14.2 | SSE 事件含资源类型/ID + revision，供 UI 选择性重取 | P0 | §23.6 |
| FR-14.3 | 页面：Overview（卡片 + 三张表）、Machines（列与动作）、Machine Detail（9 个区块，无交互 shell）、Profiles（JSON/YAML 预览 + 消费机器）、Skills、Deployments、SSH Inventory | P0 | §24 |
| FR-14.4 | SPA 技术栈推荐 React + TypeScript + Vite；API 契约不得依赖前端框架 | P0 | §24 |

#### FR-15 操作审计与错误模型

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-15.1 | 每次变更产生不可变 Operation 记录：machine/type/transport/desiredGeneration/phase/steps（每步 phase）；输出脱敏环境值，永不捕获秘密 | P0 | §8.7 |
| FR-15.2 | 错误模型三类：SSH 错误（6 类）、Agent 错误（5 类）、Reconcile 错误（9 类）；每条用户可见错误含 reason code、人读消息、operation ID、时间戳、安全诊断 | P0 | §30 |
| FR-15.3 | 服务端重启后，in-flight 操作置为 `Unknown`，除非后续 agent 报告给出最终结果 | P0 | §35.2 |

### 2.5 非功能需求

| 编号 | 类别 | 需求 | spec 出处 |
|---|---|---|---|
| NFR-1 | 规模 | ≤100 机器；每机 ≤10 个受支持 Agent 实例；目录 ≤200 Skills；默认 ≤20 并发变更操作。为验证目标而非硬上限 | §35.1 |
| NFR-2 | 性能 | 无 drift 在线节点 reconcile plan 通常 <1s（不含网络/包管理操作） | §35.3 |
| NFR-3 | 可靠性 | 服务重启保留全部资源状态；agentd 自动重连；重启后 in-flight 操作置 `Unknown`；reconcile 幂等 | §35.2 |
| NFR-4 | 安全 | §29 全部 14 条为**非可选**（详见第 11 章逐条落点） | §29 |
| NFR-5 | 可升级 | SQLite schema 变更走显式迁移；协议与期望状态 schema 自首个 MVP 版本起即版本化 | §35.4 |
| NFR-6 | 可测试 | 每条变更路径可用临时 HOME 测试，不触碰开发者真实 Agent 配置 | §38.12 |
| NFR-7 | 可观测 | 结构化日志（resource_type/resource_id/machine_id/operation_id/deployment_id/reason）；`/metrics` 至少含 8 个指定指标；指标标签不含凭据/命令输出/模型秘密 | §33 |
| NFR-8 | 平台 | Linux amd64/arm64、macOS amd64/arm64；WSL 视同 Linux；macOS 路径可沿用 XDG 兼容路径 | §4.1、§7.1 |
| NFR-9 | 契约稳定 | REST JSON API 契约不依赖前端框架；proto 定义集中于 `api/proto/fleet/v1/` | §24、§27 |

### 2.6 系统边界与非目标

**明确的非目标**（spec §4.2，共 14 条——凡此清单内的请求一律拒绝并引用本节）：

1. 不从控制面运行用户的 Agent 任务；
2. 不代理 Codex/OMP/OpenCode 交互会话；
3. 不提供内嵌 Web SSH 终端；
4. 不做完整 SSH 密钥生命周期 / SSH CA；
5. 不做 secret 同步；
6. 不支持密码 SSH 认证；
7. 不做多用户/多租户 RBAC；
8. 不依赖 Kubernetes CRDs 或把 K8s 作为运行时；
9. 不支持 WSL 之外的 Windows 原生节点；
10. 不做 Skill 市场/公共注册发现；
11. 不管理 Agent 相关状态之外的任意机器配置；
12. 不集中收集 Agent 对话日志；
13. 不同步 Agent 会话/历史；
14. 不经显式 opt-in 自动修改用户主 `~/.ssh/config`。

**MVP 有意不做但架构不得阻塞**（spec §37）：原生 installer drivers、原生 Windows、secret-manager 集成、SSO/RBAC/审计、PostgreSQL、GitOps 源、Skill 注册表、自动 Skill 更新策略、SSH CA 集成、签名工件、策略继承、发布审批门、多操作者远程控制面、第三方适配器插件 SDK、Webhook、Grafana 仪表盘、OTel traces、Helm 打包、对外自动化 API 兼容层。第 16 章给出各项的预留扩展点。

### 2.7 实现护栏（对实现的强制约束，spec §38）

1. SSH 与 daemon 不得分裂为两套 Agent 管理实现——都必须调用同一本地 reconciler；
2. Agent 专属路径/配置逻辑不得进入 controller/API 包——留在适配器内；
3. 除适配器显式声明 whole-file 外，不得覆盖用户未知配置字段；
4. 不得为图方便实现 secret 存储；
5. 不得经 `sh -c` 执行安装器字符串；
6. 不得禁用 SSH host-key 校验；
7. MVP 不得引入 Kubernetes、Redis、PostgreSQL、消息队列、服务网格；
8. 不得实现内嵌 SSH 终端；
9. `latest` 不得作为已解析期望版本/修订——apply 前必须解析为不可变版本/digest；
10. apply 后 inventory 与适配器健康检查证明 desired==observed 之前，不得报告 reconcile 成功；
11. 偏好显式接口的小包；server 控制器与文件系统/Agent 专属细节解耦；
12. 每条变更路径必须可用临时 HOME 测试。

### 2.8 需求汇总统计

- 功能需求 15 组（FR-1～FR-15），共 70 条子项，其中 P0 = 69 条、P1 = 1 条；
- 非功能需求 9 组（NFR-1～NFR-9）；
- 安全强制项 14 条（NFR-4 引用）；
- 验收标准 12 项（A–L）与护栏 12 条，全部可在 FR 表中找到承载条目（映射见第 13.3 节）。

---
## 3. 总体架构

### 3.1 架构风格与总原则

系统采用**单进程声明式控制面 + 双通道节点代理**架构（spec §6）：

- 控制面为单进程单体（AD-1），内部按包/模块划界，不拆服务；
- 一切管理操作以期望状态为源点（声明式优先，§3.1）；
- 节点侧只有二进制 `agent-fleet-agentd`，`daemon` 与 `oneshot` 复用同一本地 reconciler（AD-5）；
- 两条管理通道：mTLS 出站 gRPC（持续）与系统 OpenSSH（bootstrap/兜底/SSH-only）；
- K8s 式 `metadata/spec/status` + generation + conditions + controller 循环，但不依赖 K8s（AD-7）。

架构必须满足的不变量（从需求推导，实现与评审时逐条对照）：

| # | 不变量 | 来源 |
|---|---|---|
| I-1 | 控制面核心逻辑不知道任何 Agent 专属路径/格式；一切 Agent 差异封装在适配器内 | FR 护栏 #2 |
| I-2 | daemon 与 SSH 两条通道执行的是同一个本地 reconciler 二进制代码 | AD-5、护栏 #1 |
| I-3 | 任何秘密值（API key 值、SSH 私钥内容）不进入 SQLite、日志、操作记录、观测负载 | §29.1/29.2、§34.L |
| I-4 | apply 前必须解析出不可变快照（无 `latest`）；快照摘要确定性 | FR-7.2、护栏 #9 |
| I-5 | 任何变更先备份，失败自动恢复，恢复失败置 `Degraded` | FR-9.2/9.6 |
| I-6 | 未托管字段永不因 Fleet 操作而丢失（结构化文件合并 + 标记块） | §16、护栏 #3 |
| I-7 | reconcile 成功报告前必须完成 apply 后 inventory + 健康检查且 desired==observed | 护栏 #10、§21.3 |

### 3.2 系统上下文（C1 层）

```text
                    +--------------------------+
                    |   单操作者（浏览器）      |
                    +-----------+--------------+
                                | HTTP (默认 127.0.0.1:7788)
                                v
+--------------+    SSH(22, 出站自控制面)   +---------------------------+
| 开发机 C      |<--------------------------|   agent-fleet-server      |
| sshd         |   scp / ssh exec          |   （单进程，操作者宿主机）  |
| agentd oneshot|                          +----+-----------------+----+
+--------------+                                |                  |
                                                | mTLS gRPC        | Git / 本地目录
                                                | (默认 0.0.0.0:7789)| (Skill 源)
+--------------+      出站 mTLS gRPC            v                  v
| 开发机 A      |---------------------------->+----------+   +-----------+
| agentd daemon |      agentd 发起连接        | gRPC 端点 |   | Skill 解析器|
+--------------+                              +----------+   +-----------+
```

要点：

- 连接方向：**永远是 agentd → 控制面**（AD-3）；控制面经 SSH 主动连接受管机是唯一反向流量，且只用于 bootstrap / repair / SSH-only oneshot；
- SSH-only 机器可以完全无法访问控制面 agent 端点（§7.2），此时一切状态同步由操作者触发的 SSH 操作完成；
- Skill 源（Git 仓库 / 控制面本地目录）是唯一的外部数据依赖；MVP 不依赖任何云服务。

### 3.3 容器视图（C2 层）

```text
+-----------------------------------------------------------------------------+
| agent-fleet-server（Go 单进程）                                              |
|                                                                             |
|  +--------------------+   +---------------------+   +--------------------+  |
|  | HTTP/API 服务器     |   | SSE 事件枢纽         |   | Web UI 静态资源     |  |
|  | /api/v1 REST       |-->| (资源类型/id+revision)|   | (SPA, 由 server 托管)|  |
|  +--------+-----------+   +----------+----------+   +--------------------+  |
|           |                          ^                                       |
|           v                          |                                       |
|  +--------------------+   +----------+----------+   +--------------------+  |
|  | Machine 控制器      |   | Reconcile 控制器     |   | Deployment 控制器   |  |
|  | 探测/条件/生命周期  |   | 期望渲染/触发/跟踪   |<--| canary/批次/门禁    |  |
|  +--------+-----------+   +----------+----------+   +---------+----------+  |
|           |                          ^                        |              |
|           v                          |                        v              |
|  +--------------------+   +----------+----------+   +--------------------+  |
|  | SSH 控制器          |   | 期望状态渲染器       |   | Enrollment/CA 服务  |  |
|  | 受限执行器          |   | (DesiredStateSnapshot)|  | token/签发/续期    |  |
|  +--------+-----------+   +----------+----------+   +---------+----------+  |
|           |                          ^                        |              |
|           v                          |                        v              |
|  +--------------------+   +----------+----------+   +--------------------+  |
|  | Skill 解析器/缓存    |   | SQLite 存储          |   | agent gRPC 端点     |  |
|  | git/local→digest    |   | (fleet.db + 迁移)    |   | Connect/FetchArtifact| |
|  +--------------------+   +---------------------+   +--------------------+  |
+-----------------------------------------------------------------------------+
        | SSH                                        ^ mTLS gRPC
        v                                            | (出站)
+-------------------------------+        +-------------------------------+
| 开发机（daemon 模式）          |        | 开发机（SSH-only 模式）        |
| agent-fleet-agentd daemon     |        | sshd + agent-fleet-agentd     |
|  + 本地 reconciler（共享）      |        |   oneshot inventory/plan/apply|
|  + 适配器 codex/omp/opencode  |        |  + 同一本地 reconciler + 适配器 |
+-------------------------------+        +-------------------------------+
```

### 3.4 请求与数据流（C3 关键流）

**流 1：期望状态传播（daemon 模式）**

```text
操作者保存 Profile 变更
→ 期望状态渲染器为每台引用机器重算 DesiredStateSnapshot
→ 摘要与上一代相同 → generation 不变，流程结束
→ 摘要变化 → generation+1，落库 desired_snapshots
→ agent gRPC 端点向在线节点的 Connect 流推送 DesiredStateChanged{generation}
→ agentd 拉取/接收新快照 → 本地 reconciler plan（可自动 plan，apply 按策略/操作者触发）
→ 上报 ObservedState → 服务端更新 conditions（Drifted/Reconciled）→ SSE 推送 UI
```

**流 2：SSH-only reconcile（操作者触发）**

```text
POST /machines/{id}/reconcile
→ Reconcile 控制器取该机最新快照
→ SSH 控制器构建 operation bundle（manifest + 工件，含 bundle digest）
→ scp 上传至远端临时目录 → ssh 'agentd oneshot plan --bundle ...'（无 agentd 则先上传临时二进制）
→ 展示 plan → ssh 'agentd oneshot apply --bundle ...'
→ OperationResult 流回 → 服务端记录 Operation/conditions → 清理远端临时目录
```

**流 3：Canary Deployment**

```text
POST /deployments（selector + targetGeneration + 策略）
→ Deployment 控制器解析目标机、过滤 unavailable/degraded
→ 金丝雀批（canary=N 台）：逐台触发 reconcile
→ 每台健康门禁（操作成功 + apply 后 inventory + digest 一致 + 适配器健康）全部通过
→ 继续 batchSize 批次；任一台失败且 pauseOnFailure → Deployment 置 Paused
→ 操作者修复后 resume 或创建回滚 Deployment（指向旧 generation）
```

**流 4：Enrollment（新节点入队）**

见第 9.3 节。

### 3.5 技术栈

| 层 | 选型 | 说明 |
|---|---|---|
| 语言 | Go（服务端 + agentd 单仓双二进制） | spec §27 推荐布局；跨平台交叉编译 GOOS/GOARCH 天然覆盖 4 平台 |
| 节点通信 | protobuf + gRPC over TLS（mTLS） | `api/proto/fleet/v1/` |
| 传输（SSH） | 系统 OpenSSH 客户端（`ssh`/`scp`/`ssh -G`），受限执行器调用 | AD-4，不引入 Go SSH 库 |
| 存储 | SQLite（单文件 + 显式迁移），工件/备份落盘 | AD-2；大对象不入库（§25） |
| API | REST `/api/v1` JSON + SSE | OpenAPI 描述于 `api/openapi/` |
| 前端 | React + TypeScript + Vite SPA（推荐项，契约不绑定框架） | §24 |
| 可观测 | 结构化日志 + `/metrics`（Prometheus 兼容） | §33 |

### 3.6 仓库与代码组织

沿用 spec §27 布局，本文档补充每包职责约束：

```text
agent-fleet/
  cmd/agent-fleet-server/     # 入口：装配 HTTP/gRPC/控制器，禁止业务逻辑
  cmd/agent-fleet-agentd/     # 入口：daemon | oneshot inventory|plan|apply | doctor | version
  api/proto/fleet/v1/         # agent.proto（连接）+ state.proto（状态消息）
  api/openapi/                # REST 契约
  internal/
    domain/                   # 领域类型：资源、conditions、digest、错误码（纯类型，无 IO）
    store/sqlite/             # 仓储实现 + 迁移装配（接口定义在 domain 或 store 包）
    server/httpapi/           # REST 处理器：参数校验→调用控制器→映射错误码
    server/sse/               # 事件枢纽（类型/ID/revision）
    server/grpcagent/         # agent gRPC 端点：连接注册表、流推送、工件流
    controller/machine/       # 条件维护、探测编排、生命周期
    controller/reconcile/     # 期望渲染触发、操作派发（daemon 推送 / SSH 走 sshtransport）
    controller/deployment/    # canary/批次/门禁/暂停/恢复
    sshtransport/             # 受限执行器：ssh -G / BatchMode exec / scp / 安全引用工具
    enrollment/               # token 生命周期、CSR 校验、CA（Fleet CA 本地生成）
    skills/                   # git/local 解析、SKILL.md 校验、digest、工件缓存
    desiredstate/             # 有效期望解析、确定性摘要、快照存储
    operations/               # Operation/步骤记录、超时与 Unknown 置位、重试语义
    agentlocal/               # —— 仅 agentd 二进制链接本组包 ——
      reconciler/             # 13 阶段共享流水线（daemon/oneshot 唯一实现）
      inventory/              # 观测采集（调用各适配器）
      backup/                 # 备份/恢复
      installer/              # command installer（argv 直执行、${VERSION} 替换）
      adapter/                # 适配器注册表与公共契约
        codex/ omp/ opencode/ # 家族实现（路径/格式/合并策略封装于此）
  web/                        # SPA
  migrations/                 # SQLite 迁移脚本
  testdata/adapters/          # 各适配器 fixture home
  testdata/ssh/               # SSH 集成测试容器夹具
  docs/architecture.md        # 本文档（仓库内版本）
```

依赖方向约束（编译期可验证）：

- `controller/*`、`server/*` 不得 import `agentlocal/adapter/{codex,omp,opencode}`（护栏 #2、§27）；
- `agentlocal/*` 不得 import `controller/*`、`server/*`；
- `domain` 不 import 任何运行时包；
- 适配器之间互不 import；新适配器 = 新增 `adapter/<family>` 包 + 注册表注册，控制面零改动（§1 扩展性要求）。

---

## 4. 控制面设计

### 4.1 HTTP/API 服务器

- 默认监听 `127.0.0.1:7788`（Web + REST + SSE + 静态 SPA）；绑定非 loopback 时**必须**配置 admin token（`security.adminTokenEnv`），校验失败 401（§28、§29.14）；
- agent gRPC 端点默认 `0.0.0.0:7789`，对外的 `advertiseURL` 写入 agentd 引导配置（§7）；
- REST 契约以 `api/openapi/` 为准；资源统一 `metadata/spec/status` JSON 形态；错误响应统一 `{reason, message, operationId?, timestamp, diagnostics?}`（§30.3）。

### 4.2 Machine 控制器

职责：

1. 维护 conditions（唯一写入方），每次写完整记录（status/reason/message/lastTransitionTime），条件间不互相推导（§22）；
2. 心跳超时扫描：`lastHeartbeatAt + offlineAfter(45s)` 过期 → `AgentConnected=False`（原因 `AgentDisconnected`）；
3. SSH 探测编排：调 `sshtransport` 执行 `ssh -G <alias>`（解析生效配置）→ BatchMode 探测命令（采集 OS/arch/home/shell/服务管理器/已装 Agents）→ 更新 `SSHReachable` 与 `status.os/arch/hostname/homeDir`；
4. Bootstrap/Repair 编排：见第 9.3/9.4 节。

条件写入规则示例：

| 事件 | 写入条件 |
|---|---|
| SSH 探测成功/失败 | `SSHReachable=True/False`（失败带 §30.1 的 reason） |
| Connect 流建立/断开 + 45s 无心跳 | `AgentConnected=True/False` |
| 收到首个有效 inventory | `InventoryReady=True` |
| 观测摘要 ≠ 期望摘要 / 相等 | `Drifted=True/False`、`Reconciled=False/True` |
| 恢复失败 | `Degraded=True`（后续 rollout 跳过该机，§14.2） |

### 4.3 Reconcile 控制器

- 输入：手动 API 触发、Deployment 派发、（daemon 模式）agentd 请求收敛；
- 路由：`managementMode=agentd` 且在线 → 经 gRPC `ExecuteOperation` 推送；否则若配置了 SSH → 走 SSH bundle 路径（§13.4）；两者都不可用 → 报错并写 `Operation.Failed`（reason 明确：`AgentDisconnected` 或 SSH 细分原因）；
- 每次触发先确认目标快照 generation；操作完成后核对 apply 后 inventory 的摘要以决定 `Reconciled/Drifted`；
- 并发上限：默认 ≤20 个并发变更操作（NFR-1），排队或在 UI 呈现等待；同一机器同时只允许一个变更操作（由 Machine 级互斥保证，避免备份/恢复交错）。

### 4.4 Deployment 控制器

- 状态机：`Pending → Canary → RollingOut → (Paused | Succeeded | Failed)`（phase 命名遵循 §8.6 的 `RollingOut` 风格）；
- 目标解析：`spec.selector.machineNames` 显式列表；解析后过滤 `Degraded=True`、`AgentConnected=False` 且无 SSH 配置的机器（不可达目标保持 pending 并呈现原因）；
- 批次执行：先 canary 批（`strategy.canary` 台），全部通过健康门禁后才继续 `batchSize` 批；`maxUnavailable` 限制同批同时变更数；失败策略 `pauseOnFailure=true` 时立即暂停（§21.2）；
- 健康门禁 = FR-10.3 四条件（缺一不可）；门禁评估依赖 apply 后 inventory 上报，daemon 模式由 agentd 自动触发（操作后全量 inventory，§10.4），SSH 模式由 oneshot apply 返回的 OperationResult 内含；
- 回滚：`POST /deployments/{id}/rollback` 创建新 Deployment 指向历史 `targetGeneration`；历史记录不可变（§21.4）；
- 幂等与恢复：控制面重启后，`RollingOut` 的 Deployment 从持久化的批次进度继续；单机操作状态 `Unknown` 的按 §35.2 处理（等待 agent 重报或操作者重试）。

### 4.5 SSH 控制器（sshtransport）

受限执行器规格：

- 只接受**结构化参数**（alias、argv 数组、文件对），不接受拼接命令字符串；
- 本地进程：直接 `exec` OpenSSH 二进制（不经 shell）；远程命令由专用引用工具按 POSIX shell 引用规则构造单个字符串后交 `ssh <alias> -- <cmd>`；
- 探测：`ssh -G <alias>` 解析生效配置（HostName/User/Port/ProxyJump/IdentityFile），用于探测与 include 导出；
- 执行：`ssh -o BatchMode=yes -o ConnectTimeout=<cfg> <alias> -- <cmd>`；文件传输用 `scp`；
- 超时：`connectTimeout=10s`、`commandTimeout=60s`（可配置，§28）；
- 错误映射：stderr/退出码 + OpenSSH 输出模式 → §30.1 六类 reason（如 `Host key verification failed.` → HostKeyVerificationFailed；`Permission denied` → AuthenticationFailed）；不得坍缩为 unreachable；
- 绝不禁用 host-key 校验、绝不注入 `StrictHostKeyChecking=no`（§13.3）；不摄取私钥内容（§13.2）。

### 4.6 Enrollment / CA 服务

- 首启本地生成 Fleet CA（自签），`pki/ca.key` 权限 0600（§12.2、§26）；
- Enrollment token：一次性、短时效（建议默认 10 分钟，MVP 可配置），绑定 Machine ID；使用即作废（§12.1）；
- `Enroll(machineId, token, CSR)`：校验 token 有效性与 Machine 匹配 → 校验 CSR 公钥 → 签发有限期客户端证书（建议 24h 量级，可配置）→ token 失效；
- `RenewCertificate`：仅接受既有有效 mTLS 通道上的续期请求（§12.2）；
- 服务端证书：`pki/server.crt/key`，advertiseURL 的 TLS 终结点使用；agentd 引导配置需携带 CA 证书以校验服务端。

### 4.7 Skill 解析器 / 缓存

- 输入：`source.type=git`（repo+ref+path）或 `local`（控制面路径）；
- Git 解析：fetch（缓存于 `<data-dir>/git-cache/`）→ ref 解析为精确 commit → 校验 path 下存在 `SKILL.md` → 计算内容 digest（对目录规范化内容树计算 SHA-256）→ 产出不可变工件至 `artifacts/skills/<sha256>/...`；
- 本地解析：同上（无 fetch 步骤）；
- 同一 digest 幂等复用；`skill_revisions` 表记录解析历史（§25）；
- 摘要算法必须规范化（固定文件排序、规范化换行/权限位），保证同内容同 digest（支撑 FR-7.2 确定性）。

### 4.8 期望状态渲染器（desiredstate）

- 输入组装：Machine（含 overrides）→ AgentProfile → 各 `skillRef` 的已解析 revision/digest → `providerRef` 的 ModelProvider → 适配器 schema 版本（§9）；
- 输出：`DesiredStateSnapshot`（纯数据：归一化 agents 版本与配置、skills 工件引用、MCP、rules），含 `protocolVersion/schemaVersion`；
- 摘要：对快照规范化 JSON（键排序、无易变字段）计算 SHA-256；比较上一代快照摘要——**相同则不递增 generation、不产生新行**（内容寻址语义，FR-7.2）；
- 渲染必须纯函数化（同输入必同输出），便于单测（§32.1 前两项覆盖点）。

### 4.9 SQLite 存储

- 单文件 `~/.local/share/agent-fleet/fleet.db`，显式迁移（`migrations/`，启动时按序应用）；
- 表集合按 §25：machines、profiles、providers、skills、skill_revisions、desired_snapshots、observed_states、deployments、deployment_targets、operations、operation_steps、events、agent_certificates；
- 访问原则：
  - 仓储接口定义于领域层，SQLite 实现可替换（AD-2 允许未来 PostgreSQL，§37）；
  - 大内容（Skill 工件、bundle）一律落盘，库中只存 digest 与路径；
  - observed_states 仅保留最新 + 近期操作引用的状态，防止无限增长（§25）；
  - 事务边界：快照落库、操作记录落库、deployment 目标进度更新各自原子提交；
  - WAL 模式启用（单写多读，支撑 SSE 读路径与控制循环并发）。

---

## 5. 节点侧设计（agentd）

### 5.1 执行模式

```text
agent-fleet-agentd daemon                    # 常驻：连接、心跳、操作执行、上报
agent-fleet-agentd oneshot inventory         # 输出观测状态 JSON
agent-fleet-agentd oneshot plan --bundle P   # 对 bundle 内快照计算变更计划
agent-fleet-agentd oneshot apply --bundle P  # 备份→应用→验证，输出 OperationResult JSON
agent-fleet-agentd doctor                    # 本地诊断（平台/服务/权限/连通性）
agent-fleet-agentd version
```

`daemon` 与 `oneshot` 链接同一组 `agentlocal/*` 包（reconciler/inventory/backup/installer/adapter）——这是 AD-5 的物理保证（护栏 #1）。

### 5.2 daemon 运行时

- 启动：读取 `~/.config/agent-fleet/agentd.yaml`（控制面地址、CA 证书路径、间隔配置）；
- 连接：出站 mTLS gRPC `Connect` 双向流；指数退避 + 抖动重连；每次（重）连接先 `Hello`（身份、版本、支持的 adapters/schema 版本）+ 全量 `ObservedState`（§11.5、§31）；
- 周期任务：心跳 15s；全量 inventory 于连接建立时、每次操作后、每 5 分钟；本地 inventory 发现受管摘要变化 → 立即上报 drift（§10.4，全部可配置）；
- 操作执行：收到 `ExecuteOperation` → `OperationStarted` → 按共享 reconciler 执行（含备份/恢复）→ `OperationProgress` → `OperationResult`；服务端须对重复结果幂等（§11.5）；
- 工件获取：`FetchArtifact(digest)` 认证流式下载 → 本地 SHA-256 校验后进入规范缓存（§17.2）；
- 证书续期：到期前经既有通道调 `RenewCertificate`（§12.2）；
- 本地状态：`~/.local/share/agent-fleet/state/last-observed.json` 记录最近观测摘要，用于加速 drift 判定与断线恢复。

### 5.3 本地 reconciler（共享核心）

13 阶段流水线（§14.1）：

```text
 1 validate desired state      # schema/版本协商校验，失败→DesiredStateInvalid
 2 inventory current state     # 调用适配器采集观测
 3 calculate plan              # desired vs observed → []Change；空计划→直接跳到 12
 4 backup managed files /      # backups/<operation-id>/
   Skill links / version metadata
 5 apply Agent versions        # installer（argv 直执行）+ 版本验证
 6 apply normalized config     # 适配器合并写（原子写）
 7 materialize Skills          # 规范缓存 + symlink/copy
 8 apply MCP                   # 适配器合并写
 9 apply rules                 # 标记块/whole-file
10 run adapter health checks
11 inventory again
12 verify desired == observed  # 摘要比对；不等→HealthCheckFailed/漂移未消
13 commit operation success
```

失败行为（§14.2）：可变步骤（5–10）失败 → 停止后续 → 保留诊断 → 从本次备份自动恢复 → 恢复后再 inventory → 上报「原始失败 + 恢复结果」；恢复亦失败 → 置 `Degraded=True`，Deployment 控制器跳过该机。幂等（§14.3）：第二次执行同一快照零实质变更（由 plan 阶段空计划保证）。

### 5.4 适配器契约

Go 接口（§15）：

```go
type Adapter interface {
    ID() string
    Detect(ctx context.Context) (DetectedAgent, error)
    Inventory(ctx context.Context, desired AgentDesiredState) (AgentObservedState, error)
    Plan(ctx context.Context, desired AgentDesiredState, observed AgentObservedState) ([]Change, error)
    Apply(ctx context.Context, desired AgentDesiredState, changes []Change) error
    HealthCheck(ctx context.Context, desired AgentDesiredState) error
}
```

每个适配器自包含声明（§15）：二进制探测、版本解析、安装器默认值（可覆盖）、配置路径、受管配置 schema、合并策略、Skill 目的地、MCP 表示、rules 表示、健康检查。**核心包（控制面、reconciler 骨架）只依赖接口，不 import 任何家族实现包**（I-1）。

新家族（Claude Code、Gemini CLI…）接入 = 新增 `adapter/<family>` 包并注册：实现上述接口 + 提供 fixture home 测试，不改控制面核心（§1 的扩展性要求由此满足）。

### 5.5 配置合并与所有权（§16）

- 结构化文件（TOML/YAML/JSON）：解析 → 仅补写适配器自有键 → 保留未知键 → 临时文件 + fsync + rename 原子写 → 写后解析验证 → 尽量保留原权限；
- 文本 rules：`# BEGIN/END agent-fleet managed` 标记块替换；不可安全部分拥有的文件必须声明 `ownership=whole-file` 且 UI 可见（§16.2）；
- 投影哈希：只对受管字段投影计算观测摘要，绝不对整文件哈希（§10.2）——这同时是"未托管字段变更不触发 drift"（§10.3）的实现基础。

---
## 6. 领域模型设计

### 6.1 资源总览

六类资源，统一 K8s 风格 `metadata/spec/status`（AD-7），JSON over REST，名称在类型内唯一：

| 资源 | 生命周期 | 关键 spec 字段 | 关键 status 字段 |
|---|---|---|---|
| Machine | 操作者 CRUD + 控制器写 status | managementMode（agentd\|ssh）、ssh（hostAlias 或显式连接字段）、profileRef、overrides | observedGeneration、os/arch/hostname/homeDir、agentdVersion、desiredDigest/observedDigest、lastHeartbeatAt/lastInventoryAt、conditions×6 |
| AgentProfile | 操作者 CRUD | agents（codex/omp/opencode：enabled/version/installer/config）、skills[]（skillRef）、mcp{}、rules | —（无控制器） |
| ModelProvider | 操作者 CRUD | type（openai-compatible）、endpoint、apiKeyEnv | —（解析结果进入快照） |
| Skill | 操作者 CRUD + resolve 控制器写 status | source（git: repo/ref/path 或 local: path） | resolvedRevision、contentDigest、resolvedAt |
| Deployment | 操作者创建 + 控制器写 status | selector.machineNames、targetGeneration、strategy（canary/batchSize/maxUnavailable/pauseOnFailure） | phase、succeeded/failed/pending |
| Operation | 系统创建（不可变） | machine、type（Reconcile/Repair/Bootstrap/Rollback…）、transport（agentd\|ssh）、desiredGeneration | phase、startedAt/finishedAt、steps[]（name/phase） |

### 6.2 Machine 与 conditions

Machine 状态条件（§22，六条件全为必填维护项）：

| Condition | 置位方 | True 含义 | False 含义与典型 reason |
|---|---|---|---|
| SSHReachable | Machine 控制器 | 最近一次 SSH 探测成功 | 探测失败，reason ∈ §30.1 六类 |
| AgentConnected | Machine 控制器（由 gRPC 连接注册表驱动） | daemon 流活跃 | AgentDisconnected（心跳超 45s） |
| InventoryReady | Machine 控制器 | 已收到至少一份有效 inventory | 尚未收到 / InventoryFailed |
| Drifted | Reconcile 控制器 | observedDigest ≠ desiredDigest（受管投影） | 受管状态一致 |
| Reconciled | Reconcile 控制器 | observed generation/digest == desired | 最近收敛未确认 |
| Degraded | Reconcile 控制器（备份恢复失败时） | 处于不健康/不确定状态 | 自动 rollout 跳过该机 |

规则重申（§22）：每个 condition 记录含 status/reason/message/lastTransitionTime；**禁止从另一条件推导**（例如不得由 `AgentConnected=False` 推导 `Degraded=True`），一切由控制器显式决策写入。

### 6.3 DesiredStateSnapshot 与 generation 语义

```text
DesiredStateSnapshot {
  machine, generation, protocolVersion, schemaVersion,
  agents: { <family>: { version, config{model, provider{endpoint, apiKeyEnv}}} },
  skills: { <name>: { revision, digest, artifactDigest } },
  mcp: {...}, rules: {...},
  digest: "sha256:..."   # 规范化 JSON 的确定性 SHA-256
}
```

- 输入五要素（§9）：AgentProfile、Skill 精确修订/digest、ModelProvider、Machine overrides、adapter schema 版本；
- **generation 仅在快照摘要变化时递增**：保存 profile 但有效结果不变 → generation 不动（这是 drift 判定与 Deployment targetGeneration 语义的基石）；
- 快照不可变、可重放：Deployment 回滚 = 将机器重指向前一快照 generation；
- 快照中不含秘密值，`apiKeyEnv` 只携带环境变量名。

### 6.4 Operation 与错误模型

Operation（§8.7）为不可变审计单元：

```yaml
kind: Operation
spec: { machine, type, transport, desiredGeneration }
status:
  phase: Succeeded | Failed | Unknown
  startedAt / finishedAt
  steps: [ {name: backup|codex-version|codex-config|skills|mcp|rules|verify, phase} ]
```

- 输出脱敏：环境值、敏感环境变量名列表命中的内容、SSH 私钥路径内容一律不得进入 step 输出（§29.10、§34.L）；
- 错误码三组（§30）：SSH 六类 / Agent 五类（AgentDisconnected、ProtocolVersionMismatch、EnrollmentRejected、CertificateExpired、InventoryFailed）/ Reconcile 九类（DesiredStateInvalid、InstallerFailed、VersionVerificationFailed、ConfigParseFailed、ConfigWriteFailed、SkillDownloadFailed、SkillDigestMismatch、HealthCheckFailed、RollbackFailed）；
- 用户可见错误五要素：reason code、人读消息、operation ID、时间戳、安全诊断（§30.3）。

---

## 7. 关键机制设计

### 7.1 受管投影与 drift 检测

```text
期望侧：snapshot.agents.codex.config.model = "gpt-5.6"           （归一化）
观测侧：adapter.Inventory() → managedConfig{"model": ..., "model_provider": ...}
比较：  normalize(观测投影) vs normalize(期望投影映射到该适配器受管 schema)
→ 不等 ⇒ Drifted=True，并产出逐字段 diff（UI Show Diff）
```

关键约束：

1. 投影范围由适配器声明，未托管字段既不比较也不备份范围之外的内容（§10.2、§16）；
2. 摘要对"归一化后的受管状态"计算，而非原始文件字节（否则任何无关编辑都会误报 drift）；
3. Skill 的 drift 信号 = 目标 revision/内容 digest（symlink 模式）或复制内容 digest（copy 模式，§17.3）；
4. 未托管本地配置变更**不得**触发 drift（§10.3）——此为验收 G 的隐含前提。

### 7.2 备份与回滚

备份（§16.3）：每次变更前把受管工件复制到 `~/.local/share/agent-fleet/backups/<operation-id>/`，附恢复所需元数据（原文件路径、权限、内容哈希、Skill link 目标、先前版本号）；保留每机最近 10 次操作（可配置）。

回滚的两条路径：

| 场景 | 机制 | 失败语义 |
|---|---|---|
| reconcile 中途失败 | 同操作内自动从备份恢复（§14.2），恢复失败置 `Degraded` | 如实上报恢复结果 |
| 事后回滚（API/UI） | 配置/文件类：恢复上一成功操作的备份；版本类：用同一 installer 安装先前观测版本（§20.3） | 版本装不回 → `RollbackUnsupported`，绝不虚报成功（验收 J） |

回滚 API：`POST /machines/{id}/rollback`（机器级）；`POST /deployments/{id}/rollback`（机群级，生成新 Deployment 指向旧 generation，§21.4）。

### 7.3 Canary / 批量 Deployment

状态机与门禁见 4.4 节。补充设计细节：

- **批次并发**：同批内机器并发执行（上限受全局 ≤20 并发约束），批间严格串行等待门禁；
- **canary 选择**：按目标列表顺序取前 `canary` 台；操作者可在 UI 调整顺序（简单确定性优先于智能调度）；
- **不可用目标处理**：`AgentConnected=False` 且 SSH 不可达的目标保持 pending 并显示原因，不计入失败（§21.2 的"filter unavailable"）；
- **暂停语义**：`Paused` 冻结批次推进，已完成批次不回退；resume 从冻结点继续；
- **失败注入友好**：每台机器的批内结果独立记录（deployment_targets），验收 H 的"注入一台失败 → 暂停"由此可演示。

### 7.4 SSH-only 操作 bundle

结构（§13.4）：

```text
<tmpdir>/bundle-<op-id>/
  manifest.json                # DesiredStateSnapshot + 依赖工件 digest 清单 + bundle 自身 SHA-256
  artifacts/skills/<digest>/…  # 仅被引用的 Skill 工件
```

执行序：build（服务端，含摘要）→ scp 上传 → `oneshot plan --bundle`（校验全部工件 digest 后计算计划）→ 操作者确认（或 Deployment 自动）→ `oneshot apply --bundle` → 返回 OperationResult JSON → 远端临时目录删除。agentd 缺失时先上传临时兼容二进制（按探测 GOOS/GOARCH 选择，§7.3）。**该路径不需要节点能访问控制面网络**——Skill 工件随 bundle 走（§13.4 的存在理由）。

安全点：bundle 上传至用户 home 下临时目录并按 0700 权限创建；apply 校验 digest 失败 → `SkillDigestMismatch`，拒绝应用（§29.11）。

### 7.5 Enrollment 与证书生命周期

时序（§12.1 十步）：

```text
Server: 建 Machine → SSH 探测（平台识别）
Server: 签发一次性 token（绑定 machineId，短时效）
Server→Node(SSH): 上传 agentd 二进制 + agentd.yaml（含 advertiseURL、CA 证书）
Node: agentd 生成本地私钥（0600）
Node→Server(HTTPS/enroll 端点): {machineId, token, CSR}
Server: 校验 token（存在/未过期/未用/machineId 匹配）→ CA 签发 client.crt
Server: token 立即作废
Node: client.crt/client.key/ca.crt 落盘 0600
Node→Server(mTLS): Connect 流建立 → Hello + 全量 ObservedState
```

证书生命周期（§12.2）：有限期客户端证书；到期前经已认证通道续期（`RenewCertificate`）；CA 私钥永不离开控制面主机（0600）。续期失败/过期 → `CertificateExpired` 错误，UI 提示重新 bootstrap（经 SSH）。

### 7.6 版本协商与兼容（§31）

- 快照与 gRPC 消息均携带 `protocolVersion` + `schemaVersion`；
- 服务端拒绝不支持的 daemon 主版本 → 该机条件 `AgentConnected` 保持 False、UI 呈现"需升级 agentd"，绝不向其下发期望状态；
- agentd 在 `Hello` 中上报支持的 adapters 与 schema 版本；服务端据此过滤可下发内容；
- 适配器 desired-state schema 独立版本化——新增字段向后兼容（加字段不改语义），破坏性变更升主版本并要求 agentd 升级。

### 7.7 OpenSSH include 导出（§13.5、§40）

- 渲染来源：仅"Fleet 持有显式 SSH 连接字段"的机器（HostName/User/Port/ProxyJump/IdentityFile）；经既有 `hostAlias` 导入的机器不重复导出（它们已在操作者配置里）；
- 产物：`~/.ssh/agent-fleet.conf`（生成路径 `generated/ssh/agent-fleet.conf`），用户主配置需 `Include ~/.ssh/agent-fleet.conf` 才生效——MVP 提供 preview、导出下载、**显式确认后**安装/更新 include 文件三种动作，绝不静默改写主配置（非目标 #14）；
- 动因：Codex Desktop Remote SSH 从 OpenSSH 配置发现主机——Fleet 以"配置文件"为集成面，不代理会话（AD-8、§40）。

---

## 8. 接口契约

### 8.1 REST API（`/api/v1`，§23）

资源端点（标准 CRUD）：

```text
/machines  /profiles  /skills  /providers  /deployments
```

动作端点：

```text
POST /machines/{id}/ssh/probe | /bootstrap | /repair-agentd | /inventory
POST /machines/{id}/reconcile | /rollback
GET  /machines/{id}/operations | /machines/{id}/drift
GET  /profiles/{id}/render?machine=<id>
POST /skills/{id}/resolve      GET /skills/{id}/revisions
POST /deployments/{id}/pause | /resume | /rollback
GET  /events        # SSE：Accept: text/event-stream
```

约定：

- 资源 JSON 形态 = `metadata/spec/status`；列表端点支持按 name/status 过滤（MVP 可最小化）；
- 动作端点返回 `Operation` 资源（202 语义），客户端凭 operation id 追踪进度（轮询或 SSE）；
- 错误体统一 `{reason, message, operationId?, timestamp, diagnostics?}`，reason 取 §30 三组错误码；
- 非回环部署时全部端点要求 `Authorization: Bearer <adminToken>`（§29.14）；
- SSE 事件格式：`event: <resource-type>`、`data: {id, revision}`——UI 据此选择性重取（§23.6）。

### 8.2 gRPC 协议（`api/proto/fleet/v1`，§11）

```proto
service FleetAgentService {
  rpc Connect(stream AgentToServer) returns (stream ServerToAgent);
  rpc FetchArtifact(ArtifactRequest) returns (stream ArtifactChunk);
}
service FleetEnrollmentService {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);          // 无 mTLS（token 鉴权）
  rpc RenewCertificate(RenewCertificateRequest) returns (...); // 须既有 mTLS 通道
}
```

消息 union：

| AgentToServer | ServerToAgent |
|---|---|
| Hello（身份/版本/adapters/schema 版本） | Welcome（连接确认、服务端配置回传） |
| Heartbeat | DesiredStateChanged{generation} |
| ObservedState（全量/增量标志） | ExecuteOperation{operationId, snapshot 或摘要引用} |
| OperationStarted / OperationProgress / OperationResult | CancelOperation{operationId} |
| LogEvent（仅运维日志，禁会话内容） | RequestInventory |

语义要点：重连必以 Hello + 全量 ObservedState 开始；服务端幂等处理重复 OperationResult（按 operationId 去重，§11.5）；`ExecuteOperation` 携带完整快照或 digest 引用（daemon 可经 `FetchArtifact` 取 Skill 工件）。

### 8.3 主要页面与 API 对应（§24）

| 页面 | 数据来源 | 关键动作 |
|---|---|---|
| Overview | GET /machines + /deployments 聚合 | — |
| Machines | GET /machines（列：Name/OS-Arch/Profile/agentd/SSH/Drift/Reconciled/Last Seen） | Add/Probe/Bootstrap/Reconcile/Repair |
| Machine Detail | GET /machines/{id} + operations + drift | Bootstrap/Repair/Reconcile/Rollback、diff 查看 |
| Profiles | /profiles + /profiles/{id}/render | 编辑 + JSON/YAML 预览 + 消费机器 |
| Skills | /skills（Name/Source/Requested Ref/Resolved Revision/Digest/Machines） | Resolve/查看元数据/改 ref |
| Deployments | /deployments | Pause/Resume/Rollback、批次进度 |
| SSH Inventory | /machines（显式字段机器）→ 渲染 | 预览/导出/确认后安装 include |

无交互 shell、无会话代理（非目标 #2/#3）。

---

## 9. 关键流程时序

### 9.1 Bootstrap（新机入队，daemon 模式）

```text
操作者: Web UI Add Machine(alias=gpu-home, mode=agentd, profile=default-dev)
Server: 建 Machine 记录 → ssh -G 解析 → BatchMode 探测（OS/arch/home/shell/svc/agents）
Server: 生成一次性 token（TTL 10m，绑定 machineId）
Server→Node(SSH): scp agentd 二进制(GOOS/GOARCH) + agentd.yaml
Server→Node(SSH): 安装用户服务（launchd/systemd user，受支持时）并启动
Node: agentd 启动 → 生成私钥 → POST /enroll{machineId, token, CSR}
Server: 校验 → CA 签发 client.crt → token 作废
Node: 证书落盘 0600 → 出站 mTLS Connect → Hello + 全量 ObservedState
Server: AgentConnected=True, InventoryReady=True → SSE → UI 状态更新
```

### 9.2 daemon 模式 drift 与 reconcile

```text
[背景] 操作者手工改了受管 codex.model
Node: 周期/触发 inventory → 受管摘要变化 → 立即上报 ObservedState
Server: observedDigest != desiredDigest → Drifted=True → SSE → UI 显示 drift + Show Diff
操作者: 点击 Reconcile → POST /machines/{id}/reconcile
Server: 建 Operation(transport=agentd) → Connect 流推 ExecuteOperation
Node: 13 阶段流水线（含备份）→ OperationStarted/Progress/Result
Server: apply 后 inventory 摘要 == 期望 → Drifted=False, Reconciled=True → SSE
```

### 9.3 SSH-only reconcile

见 3.4 流 2。补充：plan 输出在人发起的路径上可回显给操作者确认；Deployment 驱动路径上按策略自动 apply；`maxUnavailable` 与并发上限同样适用；临时二进制上传发生在"agentd 不存在或版本不兼容"时（§13.4）。

### 9.4 Repair agentd

```text
触发: AgentConnected=False 持续 + SSHReachable=True（或操作者主动探测后触发）
Server: SSH 诊断（doctor / 服务状态 / 日志尾部，限额）
分支: 二进制缺失/损坏 → 重传兼容工件；服务未启 → 启动；版本不兼容 → 替换
Server: 重装/重启用户服务 → 等待出站流重连（超时上限可配置）
结果: 重连成功 → AgentConnected=True；失败 → Operation.Failed(reason)
```

### 9.5 Canary Deployment

见 3.4 流 3；批内机器结果独立记录于 deployment_targets；门禁四条件评估所需数据全部来自 apply 后 inventory 与适配器健康检查（§21.3）。

### 9.6 服务重启恢复（§35.2）

```text
Server 重启: SQLite 恢复全部资源状态
  - daemon 节点: agentd 指数退避重连 → Hello + 全量快照 → 状态自愈
  - in-flight 操作: 置 Unknown，直到 agent 重报最终结果或操作者重试
  - Deployment: 从持久化批进度继续（已 Unknown 的机器等待重报）
```

---

## 10. 数据设计

### 10.1 表清单与要点（§25）

| 表 | 要点 |
|---|---|
| machines | spec/status JSON 列 + 查询列（name 唯一、managementMode、profileRef）；ssh 凭据只有引用（alias/路径），无私钥内容 |
| profiles / providers | spec JSON；providers 不含秘密值 |
| skills | source spec；status 指向最新解析 |
| skill_revisions | (skill_id, revision, digest, artifact_path, resolved_at)——解析历史不可变 |
| desired_snapshots | (machine_id, generation, digest UNIQUE, snapshot JSON, created_at)；同 machine+digest 唯一（内容寻址不重复存） |
| observed_states | machine 最新 + 近期操作引用的历史，滚动清理 |
| deployments / deployment_targets | 策略与批进度；targets 记录每机 phase/result/operation_id |
| operations / operation_steps | 不可变审计；steps 含脱敏输出（限长） |
| events | SSE 重放/审计辅助（保留窗口可配置） |
| agent_certificates | machine ↔ 证书序列号/有效期（不含私钥） |

迁移：`migrations/NNNN_*.sql` 顺序应用，禁止修改已应用脚本（NFR-5）。

### 10.2 文件系统布局（§26）

控制面：

```text
~/.config/agent-fleet/config.yaml
~/.local/share/agent-fleet/
  fleet.db
  pki/{ca.crt, ca.key(0600), server.crt, server.key(0600)}
  artifacts/skills/<sha256>/…
  git-cache/
  generated/ssh/agent-fleet.conf
```

节点：

```text
~/.config/agent-fleet/agentd.yaml
~/.local/share/agent-fleet/
  pki/{client.crt, client.key(0600), ca.crt}
  skills/<skill-name>/<digest>/
  backups/<operation-id>/
  state/last-observed.json
```

### 10.3 数据保留

- 备份：每机 10 次操作（默认，可配置，§16.3）；
- observed_states：最新 + 近期操作引用（§25）；
- Skill 工件：按 digest 引用计数，未被任何快照引用且超出保留窗后可清理（MVP 可仅做手动清理）；
- events/operations：MVP 全量保留（单操作者规模下量可控），清理策略留待 Post-MVP。

---

## 11. 安全设计（§29 十四条逐条落点）

| # | 要求 | 架构落点 |
|---|---|---|
| 1 | 不持久化 API key 值 | 领域模型仅 `apiKeyEnv` 名称（FR-3.1）；快照/DB/日志/操作记录全链路无值；存储层无秘密列 |
| 2 | 不持久化 SSH 私钥内容 | Machine.spec.ssh 仅存 alias/路径引用（FR-12.2）；SSH 执行交给系统 OpenSSH 与 ssh-agent |
| 3 | 用本地 OpenSSH 与既有凭据机制 | sshtransport 受限执行器（AD-4） |
| 4 | 不自动禁 host-key 校验 | 执行器禁止注入该选项；失败显式呈现 HostKeyVerificationFailed（FR-12.3） |
| 5 | agentd 传输 mTLS | gRPC over TLS 双向认证（FR-11）；enroll 端点是唯一例外且以一次性 token 鉴权 |
| 6 | token 一次性且短时效 | enrollment 服务（FR-11.1）；用后作废 + TTL |
| 7 | CA/客户端私钥 0600 | 文件布局固定权限；写入时显式 chmod；服务端启动自检 |
| 8 | 变更操作全部留痕 | Operation 不可变记录（FR-15.1） |
| 9 | installer argv 直执行 | installer 包 exec argv，无 shell；`${VERSION}` 逐参数替换（FR-2.2） |
| 10 | 日志限长 + 脱敏 | 统一日志中间件：输出限额 + 敏感环境变量名清单匹配脱敏；agentd LogEvent 同规 |
| 11 | Skill 工件节点侧校验 | gRPC 下载与 SSH bundle 两路径 apply 前强制 SHA-256 校验（FR-6.6） |
| 12 | 配置原子写 | 临时文件 + fsync + rename + 写后解析验证（§16.1） |
| 13 | 变更前备份 | reconciler 阶段 4 强制（FR-9.6） |
| 14 | 非回环暴露强制 admin token | HTTP 服务器启动校验：绑定非 loopback 且未配置 token → 拒绝启动（§28） |

威胁模型要点（MVP 单操作者）：主要威胁 = 凭据意外落盘/落日志、传输窃听与伪造节点、供应链（工件篡改）。不防御：恶意操作者本人、节点 root compromise 后的本地秘密（本就不经手秘密）、网络层 DoS。

---

## 12. 可观测性与运维（§33）

- 结构化日志字段：`resource_type / resource_id / machine_id / operation_id / deployment_id / reason`；统一限长与脱敏（§29.10）；
- `/metrics` 最小指标集（Prometheus 兼容）：

```text
agent_fleet_machines_total
agent_fleet_agent_connected
agent_fleet_ssh_reachable
agent_fleet_machine_drifted
agent_fleet_operations_total{type,result}
agent_fleet_operation_duration_seconds{type}
agent_fleet_deployments_total{result}
agent_fleet_reconcile_total{result}
```

- 标签禁含凭据、命令输出、模型秘密（§33）；
- 运维操作面：`agentd doctor` 节点自诊断；服务端配置全走 `config.yaml`（§28）；升级 = 替换二进制 + 迁移自动应用；agentd 升级 = SSH 替换（repair 通道复用），daemon 自更新非 MVP（§7.3）。

### 12.1 容量估算（NFR-1/NFR-2 佐证）

100 台机器、心跳 15s ≈ **6.7 msg/s**（约 24k 条/小时）心跳消息——单进程 gRPC 轻松承载；全量 inventory 每 5 分钟一轮 ≈ 0.33 台/s——SQLite WAL 写入充裕；20 并发变更操作与 1000（=100×10）Agent 实例的 inventory 采集在节点本地完成，控制面只收结果。结论：单进程 + SQLite 在目标规模下无容量风险，与 AD-1/AD-2 自洽。

---

## 13. 测试策略（§32）

### 13.1 分层测试矩阵

| 层 | 对象 | 关键断言 | spec |
|---|---|---|---|
| 单元 | 期望解析/确定性摘要/投影与 drift 比较/Deployment 批次/版本比较/installer argv 渲染/合并保留未托管字段/digest 校验/版本解析/条件迁移 | 同输入同摘要；未托管字段无损；门禁逻辑 | §32.1 |
| 适配器 fixture | 每家族 8 类 fixture home（缺席/旧版本/目标版本/合法未托管配置/畸形配置/既有 Skills/被篡改 Skills/含未托管 MCP） | 受管编辑不擦除未托管设置；畸形配置不崩 | §32.2 |
| gRPC 集成 | in-process transport | enrollment/连接/通知/结果/重连/重复结果幂等/协议不匹配 | §32.3 |
| SSH 集成 | Linux 容器 + 真实 sshd + 真 OpenSSH 客户端 | probe/bootstrap/oneshot/repair/host-key 失败/auth 失败 | §32.4 |
| 故障注入 | 每个可变阶段后注入失败 | 后续阶段不执行；回滚被尝试；最终观测被上报；仅恢复失败才置 Degraded | §32.5 |
| E2E | ≥3 台 SSH 目标自动化场景（§32.6 十步） | 完整控制闭环 | §32.6、§39 |

### 13.2 隔离原则

一切变更路径测试用临时 `$HOME`（NFR-6、护栏 #12）：适配器路径解析必须可注入 HOME 根，禁止硬编码 `/home/user`；SSH 测试容器化，杜绝触碰真实开发机。

### 13.3 验收标准 → 需求/机制映射

| 验收 | 承载需求 | 关键机制 |
|---|---|---|
| A 机群 onboarding | FR-1.1–1.4 | §9.1 时序；enrollment |
| B SSH-only | FR-1.2、FR-9.4、FR-12.4 | bundle 机制 §7.4 |
| C 版本管理 | FR-2.1–2.4 | installer + 安装后验证 |
| D 配置管理 | FR-3.2、FR-5.1 | 合并/标记块 §5.5 |
| E Skills | FR-6.1–6.3 | 解析/物化/drift 信号 |
| F MCP | FR-4.1–4.3 | 适配器渲染 + 未托管保留 |
| G drift/reconcile | FR-8.3–8.5、FR-9.1 | 投影哈希 §7.1 |
| H Deployment | FR-10.1–10.3 | 状态机 + 门禁 §7.3 |
| I 修复 agentd | FR-1.5、FR-1.7 | §9.4 时序 |
| J 回滚 | FR-2.4、FR-9.6、§7.2 | 备份/回滚两路径 |
| K SSH 导出 | FR-12.6 | include 渲染 §7.7 |
| L 安全 | NFR-4 全部 | 第 11 章十四点落点 |

DoD（§39）= 完整控制闭环演示：Desired State → 不可变快照 → agentd/SSH 派发 → 本地 plan/apply → 健康验证 → 观测状态 → drift 决策 → Web UI 状态。该闭环由上述机制串联，缺一环即不达标。

---

## 14. 架构决策记录（整理自 §36 + 实现级补充）

| # | 决策 | 理由 | 备选与否决理由 |
|---|---|---|---|
| AD-1 | MVP 单进程控制面 | 需要强逻辑边界而非分布式复杂度；拆服务无 MVP 价值 | 微服务：运维成本与单操作者规模不匹配 |
| AD-2 | SQLite 唯一数据库 | 单操作者 + ≤100 节点不 justify 外部 DB | PostgreSQL：多一个有状态依赖，Post-MVP（§37） |
| AD-3 | agentd 出站 gRPC | 节点无入站管理端口；天然穿越常规防火墙 | 服务端入站连节点：需端口开放/隧道，MVP 明确不做反向隧道 |
| AD-4 | 系统 OpenSSH 而非 Go SSH 库 | 复用 ~/.ssh/config、ProxyJump、Include、known_hosts、ssh-agent、企业 SSH 行为的价值 > 全 Go 传输 | golang.org/x/crypto/ssh：需重实现上述语义，行为漂移风险高 |
| AD-5 | daemon 与 SSH-only 共用 reconciler | 防止双实现行为漂移，大幅缩测试面 | 两套实现：正是护栏 #1 禁止项 |
| AD-6 | MVP 不做 secret 分发 | secret 生命周期显著扩大安全范围，偏离核心问题 | 内置 secret 存储：非目标 #5 |
| AD-7 | K8s 式资源语义但不依赖 K8s | 保留成熟的调和模型，产品保持工作站易运行 | 直接上 K8s CRD：违背非目标 #8 |
| AD-8 | 不做任务执行代理 | SSH/Codex Remote 与原生 Agent 运行时已拥有执行；Fleet 只管环境状态 | 内嵌终端/会话代理：非目标 #2/#3 |

**实现级补充决策**（不改变 spec 语义，属落地选型建议，可评审调整）：

| # | 决策 | 理由 |
|---|---|---|
| RD-1 | gRPC 端点直接以 TLS 终结（不经反代） | 单进程约束下少一个组件；advertiseURL 直指服务端 |
| RD-2 | enrollment 复用同一 gRPC 监听端口的 HTTP/2 明文路径（token 鉴权）或独立 HTTPS 处理器均可；推荐前者以简化部署 | 减少 MVP 端口面；token 一次性 + 短时效兜底 |
| RD-3 | SQLite 驱动选 modernc.org/sqlite（纯 Go，免 CGO 交叉编译负担） | 4 平台交叉编译简单性优先；性能对目标规模富余 |
| RD-4 | 期望快照规范化 JSON：键序字典序、无符号数、UTC 时间戳、剔除易变字段 | 支撑 FR-7.2 确定性摘要 |
| RD-5 | agentd 用户服务模板按探测到的服务管理器选择（systemd user / launchd）；不支持时降级为提示 + 手动启动 | §5.1 的"where supported" |
| RD-6 | 20 并发变更用带权信号量实现（daemon 与 SSH 路径共享计数） | 全局上限语义统一（NFR-1） |

---

## 15. 与 spec 的可追溯性总表

| spec 章节 | 本文承载 |
|---|---|
| §1–2 摘要/问题 | 1.2、2.1 |
| §3 设计原则 | 3.1 不变量 I-1～I-7 |
| §4 MVP 范围/非目标 | 2.4（FR 全表）、2.6 |
| §5 用户体验 | 2.3（U1–U6）、9.1/9.2/9.4 |
| §6 高层架构 | 3.2–3.4 |
| §7 部署模型 | 3.5、4.1、7.4、12 |
| §8 领域模型 | 6 |
| §9 有效期望状态 | 4.8、6.3、7.1 |
| §10 观测与 drift | 5.2、7.1 |
| §11 agentd 协议 | 8.2 |
| §12 enrollment/mTLS | 4.6、7.5、9.1 |
| §13 SSH 传输 | 4.5、7.4、7.7 |
| §14 本地 reconciler | 5.3 |
| §15 适配器契约 | 5.4 |
| §16 合并与所有权 | 5.5、7.2 |
| §17 Skills | 4.7、7.4 |
| §18–20 Provider/MCP/版本 | 4.7（FR-3/4）、FR-2 |
| §21 Deployment | 4.4、7.3 |
| §22 conditions | 4.2、6.2 |
| §23–24 API/UI | 8 |
| §25–26 持久化/文件 | 10 |
| §27 仓库布局 | 3.6 |
| §28 服务器配置 | 4.1、4.5 |
| §29 安全 14 条 | 11 |
| §30 错误处理 | 6.4 |
| §31 版本协商 | 7.6 |
| §32 测试 | 13 |
| §33 可观测 | 12 |
| §34 验收 A–L | 13.3 |
| §35 NFR | 2.5、12.1 |
| §36 AD-1–8 | 14 |
| §37 延期项 | 2.6、16 |
| §38 护栏 12 条 | 2.7 |
| §39 DoD | 13.3 |
| §40 Codex Remote SSH | 7.7 |

---

## 16. 演进路线（Post-MVP 扩展点，对应 §37）

架构为以下方向预留了位置，但 MVP 一律不实现：

1. **原生 installer drivers**：`installer` 包以 driver 接口封装，`command` 只是首个实现；
2. **原生 Windows**：适配器/路径解析以 GOOS 分派，reconciler 流水线本身平台无关；
3. **secret-manager 集成**：`apiKeyEnv` 引用模型天然兼容"值由外部注入"——只需新增 provider 端的引用类型；
4. **PostgreSQL**：仓储接口在领域层，`store/sqlite` 可平行增加实现；
5. **GitOps 期望源**：期望渲染器输入是资源集合，可增加"从 Git 读取资源"的前置加载器；
6. **第三方适配器插件 SDK**：适配器接口已是边界，后续可加进程外插件（go-plugin）而不改核心；
7. **SSO/RBAC、审批门、多操作者**：admin token 中间件位置即认证/鉴权中间件位置；Deployment 状态机可插入 approval 阶段；
8. **Webhook/OTel/Grafana**：SSE 枢纽旁挂事件订阅器即可；指标已 Prometheus 格式。

## 17. 风险与开放问题

| # | 风险/开放问题 | 影响 | 缓解 |
|---|---|---|---|
| R1 | 三家 Agent 的配置 schema/路径随版本漂移，适配器维护成本 | 适配器失效 → 误报/漏报 drift | fixture 测试锁定行为（§32.2）；schema 版本协商挡住不兼容 agentd（§7.6） |
| R2 | SSH 探测/安装命令在不同发行版/服务管理器差异大 | bootstrap 失败率 | 探测先行 + UnsupportedPlatform 显式分类；E2E 覆盖 Linux 容器矩阵 |
| R3 | npm 等 installer 全局安装在用户环境可能涉及 sudo/权限差异 | 安装失败 | 安装命令完全可覆盖（FR-2.5）；错误如实上报 InstallerFailed |
| R4 | macOS launchd 用户服务行为（登录会话/休眠） | daemon 稳定性 | MVP 以单元/fixture + 手动 smoke 覆盖（§32.6 末段）；文档明示限制 |
| R5 | SQLite 单写并发（长 inventory 写 + SSE 读） | 写延迟 | WAL 模式 + 短事务；观测历史滚动清理 |
| R6 | enrollment 端口与 advertiseURL 暴露面 | 未授权 enroll 尝试 | 一次性短时效 token；失败限速（建议实现）；admin token 覆盖 HTTP 面 |
| O1 | 开放问题：agentd 升级窗口与协议主版本不兼容节点的人工处理流程细节 | 少量运维摩擦 | MVP：UI 提示 + SSH repair 通道替换（已覆盖验收路径）；细节留实现阶段确认 |
| O2 | 开放问题：Skill 内容 digest 的规范化细节（权限位/符号链接是否入哈希） | 影响 drift 稳定性 | RD-4 原则延伸：MVP 建议"固定排序 + 内容 + 常规权限位"入哈希，实现阶段以 fixture 固化 |

---

## 18. 结论

本文档完成了 spec v0.1 的需求拆解（15 组功能需求 55 条子项、9 组非功能需求、14 条非目标、12 条护栏）与架构设计（总体架构、控制面/节点组件、领域模型、七项关键机制、REST/gRPC 契约、数据设计、安全十四点落点、测试矩阵、AD/RD 决策记录与全量追溯表）。设计核心是一条不变的闭环：

```text
期望状态 → 不可变快照 → agentd / SSH 双通道派发 → 本地 plan/apply（备份护航）
→ 健康验证 → 观测状态 → drift 决策 → Web UI 呈现
```

所有架构选择（单进程、SQLite、出站 gRPC、系统 OpenSSH、单一 reconciler、无 secret、无 K8s、无任务代理）都服务于同一目标：让单操作者以最小运维成本获得对多机 Agent 开发环境的**声明式、可审计、可回滚**的控制。该闭环即 MVP 产品本身（§39）。
