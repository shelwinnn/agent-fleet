> **修订与来源说明（v1.1）**：v1.1 在 v1.0 原稿（完整保留于 KM-11/KM-13 附件）基础上完成整体修订，不是补丁。修订由资深架构专家起草（KM-13 第一次执行因供应商额度中断，其五段草稿经首席调度官从运行记录恢复拼接为未验收草稿 `recovered-architecture-v1.1-draft.md`）；资深后端工程师接续完成（KM-13 第二次执行）：对恢复稿做了全篇一致性校验与必要修订（需求统计校正、错误码分组校正、apply 后验证失败路径补全、悬挂引用修复），并产出《architecture-revision-resolution.md》（问题处置表 + 文档验证记录）。草稿原稿保留备查，来源与分工如实记录。

> **v1.1.1 增量说明（KM-14，2026-09-20）**：本版**不是**一次新的架构评审或重写，只落实一项已确认的用户决策：**MVP 不新增内置 CA 备份导出产品功能；但必须提供人工安全备份、恢复与演练方案**（来源：KM-11 评论 `01a0bceb-5547-7531-b625-9957ad171b81`，用户在 KM-14 任务中再次确认为已定决定）。因此本版同步修正 v1.1 中把"不备份 CA""CA 丢失＝全机群重建"写成**已接受风险**的残留表述，改为"人工备份/恢复为必须运维动作 + 内置导出为明确非目标"，并区分**CA 丢失（有可用备份）**、**CA 丢失且无可用备份**、**CA 泄露（必须重建信任，恢复旧备份不能消除）**三个分支。除与本决策直接相关的位置外，v1.1 的全部技术契约、FR 编号与统计口径**一律不改**。人工操作细节见配套文档 `manual-ca-backup-recovery.md`。本版为文档修订版（文档修订号 v1.1.1），**不是软件版本**。

# agent-fleet MVP 架构设计文档

| 项目 | 内容 |
|---|---|
| 文档版本 | **v1.1.1（文档修订版，非软件版本）** |
| 日期 | 2026-09-20 |
| 需求基准 | 《agent-fleet MVP Specification》v0.1（2026-09-10），本文件唯一需求来源 |
| 前序版本 | v1.1（2026-09-19，修订稿）；v1.0（2026-09-14，评审稿）——**v1.0 原文完整保留，v1.1 为在其上的完整修订，不是补丁或建议清单；v1.1.1 只在 v1.1 上落实 CA 备份决策** |
| 修订依据 | v1.1 依据 KM-3 第一性原理评审（M1–M5、S1–S6）、KM-12 对抗式评审报告（A1–A11、F6、小项 a–d）、KM-11 最终汇总的采纳边界（§4、§6）；v1.1.1 依据 KM-11 评论 `01a0bceb-5547-7531-b625-9957ad171b81` 的用户确认（KM-14） |
| 修订执行 | 起草：资深架构专家（KM-13 第一次执行，未完成）；接续修订与定稿：资深后端工程师（KM-13 第二次执行）；v1.1.1 CA 备份决策同步：文档专员（KM-14） |
| 文档范围 | 需求拆解与需求分析、系统架构设计；v1.1.1 增量范围仅限 CA 备份/恢复决策及其关联表述 |
| 目标读者 | 项目所有者、实现工程师（含 AI 编码代理）、测试工程师、负责执行 CA 备份与恢复的操作者 |
| 关联验收 | spec 第 34 节 12 项验收标准（A–L）、第 39 节 Definition of Done |
| 修订状态 | **待复核**：本版是修订交付物，不构成评审通过，也不构成任何软件测试结论；CA 备份/恢复**演练尚未执行**，相关验证项均为待验证 |

---

## 1. 文档概述

### 1.1 目的

本文档基于 spec v0.1 完成三项工作：

1. **需求拆解与需求分析**：将 spec 的叙述性需求分解为可编号、可验证、可追踪的功能需求（FR）与非功能需求（NFR），并明确系统边界与非目标；
2. **架构设计**：给出满足上述需求的总体架构、组件划分、领域模型、关键机制设计、接口契约、数据设计、安全设计与测试策略；
3. **决策固化**：将 spec 第 36 节的 8 项架构决策（AD-1～AD-8）整理为决策记录，并补充实现级决策建议与本轮新增的架构决策记录（ADR-1～ADR-3）。

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
| 快照摘要（snapshotDigest） | 对整个快照规范化 JSON 计算的 SHA-256，**仅用于内容寻址与代变更判定**，永不作为 drift 判据（v1.1 明确，见 §7.1） |
| 受管投影（Managed Projection） | 适配器声明自己拥有的配置字段的集合；drift 只在受管投影上计算 |
| 期望受管投影摘要（desiredProjectionDigest） | 对"期望状态在该适配器受管字段集上投影"的规范化结果计算的 SHA-256（v1.1 新增为显式产物） |
| 观测受管投影摘要（observedProjectionDigest） | 对"节点观测状态在同一受管字段集上投影"的规范化结果计算的 SHA-256 |
| 规范化版本（canonicalizationVersion） | 投影摘要与逐字段比较所依赖的规范化规则版本；**影响可比性，不只是诊断信息**（v1.1 明确，见 §7.1） |
| 执行权（Execution Right） | 节点上一次只允许一个变更流水线运行的排他权；由节点本地跨进程锁表达（v1.1 新增，见 §5.6） |
| 未决操作（Unresolved Operation） | phase ∈ {Pending, Running, CancelRequested, Unknown} 的操作；占用该机器的服务端互斥（v1.1 新增） |
| drift | 观测到的受管状态 ≠ 有效期望受管状态 |
| Reconcile | 将节点实际状态向期望状态收敛的一次受控执行（plan → backup → apply → verify） |
| Operation | 每次变更产生的不可变操作记录（含步骤、结果、脱敏输出） |
| Superseded | 操作/目标在完成时其目标代已落后于机器当前代的终态标记；保留为历史事实，不写 `Reconciled`（v1.1 新增） |
| AwaitingConfirmation | SSH 人发起路径中，plan 已产出、等待操作者确认的执行中间态；确认对象是**具体 plan**（v1.1 新增，见 §9.3） |
| Deployment | 跨多台机器的 canary/批量发布编排资源 |
| Enrollment | 新节点以一次性短时效 token 换取 Fleet CA 签发的 mTLS 客户端证书的过程 |
| Operation Bundle | SSH-only 模式下，服务端打包的不可变操作包（manifest + 引用的 Skill 工件），自带 SHA-256 摘要 |
| 受管 include 文件 | 由 Fleet 生成、可显式安装到 `~/.ssh/` 的 OpenSSH include 片段 `agent-fleet.conf` |

### 1.4 阅读指引

- 只关心"做什么"：读第 2 章（需求分析）与第 13.3 节（验收映射）；
- 只关心"怎么做"：读第 3～12 章；
- 关键权衡：读第 14 章（架构决策记录，含 v1.1 新增 ADR-1～ADR-3）与第 17 章（风险）。
- **要执行/复核 CA 备份与恢复**：读 §1.6（v1.1.1 变更清单）、§4.6（契约）、§13.5（演练验收口径）、§17 R6/O4，然后按配套操作手册 `manual-ca-backup-recovery.md` 执行；决策补充与演练记录模板见 `architecture-revision-resolution-v1.1.1-supplement.md`。

### 1.5 v1.0 → v1.1 修订摘要

本版把两轮评审的发现落到**可实现的契约文本**上。修订性质分三类，全文一律标注：

| 标记 | 含义 |
|---|---|
| **【契约】** | 原文缺失或自相矛盾的语义，本版给出唯一权威定义（实现前必须遵守） |
| **【澄清】** | 原文已有方向但不精确，本版收紧表述（不改变设计方向） |
| **【新增】** | v1.0 未出现的设计元素 |

主要修订点（逐条对应关系与验收场景见 `architecture-revision-resolution.md`）：

| 位置 | 修订 |
|---|---|
| §2.4 FR-9.7/9.8 | 【新增】automatic reconcile 的 FR 承载：自动 plan/上报与自动 apply 分离，apply 默认关闭 |
| §3.1 | 【契约】不变量表扩为 I-1～I-11，新增执行权、代绑定、恢复保留、路径边界四条 |
| §4.3 | 【契约】服务端机器级互斥以持久层表达；Unknown 不释放互斥；派发点获取、终态释放 |
| §4.4 | 【契约】门禁证据与 operationId/machineId/generation 因果绑定；禁止用时间比较与缓存 status 判门禁 |
| §5.2 | 【新增】节点结果 outbox；【契约】daemon 变更一律入单 worker 队列 |
| §5.6 | 【新增】节点本地跨进程执行权锁（daemon/oneshot 共享） |
| §6.2 | 【契约】条件 status 允许 Unknown；SSH-only 机器 `Drifted=Unknown` + 新鲜度字段 |
| §6.3 | 【契约】generation 与内容身份分离；A→B→A 可引用；单表方案 + ADR-2 |
| §7.1 | 【契约】三类摘要职责边界；权威判据唯一；规范化版本影响可比性；ADR-1 选型 |
| §7.2 | 【契约】面向 generation 的回滚 = 重放目标快照；备份还原仅限操作内；外部编辑冲突检测 |
| §7.4 | 【契约】bundle 路径/名称/digest 强校验；工件预算；staging 固定名 |
| §7.5 | 【契约】enroll 强制服务端 TLS 校验；token 原子消费并绑定机器与 CSR；重复连接与删除机器策略 |
| §7.8 | 【新增】automatic reconcile 触发、默认策略、暂停、审计、串行化 |
| §8.2 | 【契约】`ExecuteOperation` 一律携带完整快照；`CancelOperation` 语义；outbox 重报 |
| §9.2/9.6/9.7 | 【新增】迟到操作、Unknown 恢复、取消确认的完整时序 |
| §10.1/10.3/10.4 | 【契约】表结构按代唯一；保护引用集合；清理不得删未决操作的输入 |
| §12.1 | 【澄清】容量为待验证假设；串行点、事务大小、`SQLITE_BUSY` 指标；<1s 指标不归因于数据库 |
| §14 | 【新增】ADR-1 比较位置、ADR-2 代与内容身份、ADR-3 自动收敛边界；RD-2 重写 |
| §18 | 【契约】需求条数按表机械统计，结论段不得与表格脱钩 |

### 1.6 v1.1 → v1.1.1 修订摘要（仅 CA 备份决策）【用户已决】

本小节是本版相对 v1.1 的**全部**实质变更清单，其余章节仅为使下列位置自洽而作的措辞同步。

| # | 位置 | v1.1 原表述 | v1.1.1 修正 |
|---|---|---|---|
| 1 | §2.2 已接受风险 | 未涉及 CA 材料备份 | 新增一条：CA 材料丢失的**残余**风险是"操作者未执行或未校验备份"，不再以"不备份"作为已接受风险；泄露与丢失分开处理 |
| 2 | §2.6 v1.1 明确不引入 | "不把 CA 离线备份…写成未经验证的既定需求" | 改为：**不新增内置 CA 备份导出产品功能**（MVP 非目标）；**人工备份/恢复/演练是必须运维动作**，不得写成已接受风险 |
| 3 | §4.6 Enrollment / CA 服务 | "MVP 不做根密钥离线备份（取舍记录于 RD-7 与 R6）" | 重写为完整的**【契约】CA 材料人工备份、恢复与演练**：备份内容与边界、一致性流程、保护要求、恢复前检查与停止条件、丢失/无备份/泄露三分支、演练状态=待验证 |
| 4 | §7.5 证书生命周期 | "CA 私钥永不离开控制面主机（0600）" | 收紧为"产品不将 CA 私钥外发/上传"；**唯一例外是操作者按 §4.6 在控制面之外执行的人工加密备份**（不经过产品 API/日志/任务系统） |
| 5 | §10.2 文件系统布局 | 仅列产品目录 | 增加说明：人工备份副本不属于产品布局，由操作者存放在受控加密介质，产品不提供内置导出路径 |
| 6 | §13.5（新增小节） | 无 | 新增"CA 备份/恢复演练"测试与验收承载：演练项 DR-1～DR-8 定义于 `manual-ca-backup-recovery.md`，当前状态**计划已定义、未执行** |
| 7 | §14.3 RD-7 | "CA 根密钥不做离线备份…已接受风险" | 改写为：**MVP 不做内置导出（用户已决）；人工备份/恢复/演练为必须运维契约**；实现与演练状态=待验证 |
| 8 | §14.4 不采纳清单 | "不备份 CA"列入"未获授权的评审建议" | 改为记录用户已决的范围边界：**不新增内置产品导出动作**；人工备份方案属于已授权交付，不再列为"已接受风险" |
| 9 | §17 R6 | "MVP 不做离线备份，由操作者自行决定是否手工备份" | 改写为三分支恢复契约；人工备份为**必须**而非"自行决定" |
| 10 | §17 O4 | "待决点：CA 根密钥是否需要离线备份…需要用户决策" | 标记**用户已决**：MVP 不做内置导出；人工备份/恢复/演练落地；实现与演练**待验证** |
| 11 | §17 待决点处理原则 | "O4 需要用户决策…默认按不做离线备份推进" | O4 已决，移出待决集合；剩余待决点仅 O3 |
| 12 | §18 本版状态 | 修订稿待复核 | 补充 v1.1.1 增量范围与"演练未执行"声明 |

**范围边界**：本版**不新增、不改写任何 FR**（尤其不因"人工备份"新增产品导出功能需求，见 §2.8 统计口径与 §18）；不改动 spec；不改变 v1.1 的任何其它契约。CA 备份的**操作步骤**写在 `manual-ca-backup-recovery.md`，架构层只固定"边界、契约与验收口径"。

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
- A4 安装器命令（如 `npm install -g ...`）及其运行时由操作者负责，Fleet 只保证 argv 直执行与安装后验证（§8.3、§20）；
- **A5【新增】节点上可能存在由操作者手工启动的 `agentd oneshot` 进程，与常驻 daemon 进程并存**；两者都能执行变更，因此节点必须提供跨进程执行权（§5.6），控制面的内存互斥不足以覆盖；
- **A6【新增】控制面进程可能在任意时刻重启/崩溃**，节点上的变更可能继续执行；因此"操作是否终结"与"节点是否仍在写"是两件必须分别处理的事（§9.6、§4.3）。

**已接受的风险（明确记录，不视为缺陷）**：

- 操作者本人是可信的，不做恶意操作者防御（spec §3.7）；
- 节点 root 沦陷后本机秘密的暴露不在防御范围（Fleet 本就不经手秘密值）；
- 网络层 DoS 不做专门防御；
- **【新增】MVP 不实现证书吊销（无 CRL/OCSP）**：机器被删除后其已签发证书在自然到期前仍然有效。此风险在单操作者威胁模型下接受，审计事实记录于 `agent_certificates`（第 11 章、§4.6）。
- **【v1.1.1 修正】CA 材料的丢失不等同于"已接受风险"**：v1.1 曾把"不做离线备份、CA 丢失＝全机群重新 bootstrap"写成已接受风险；用户已确认（KM-14）该表述不成立——**人工安全备份、恢复与演练是必须的运维动作**（§4.6、`manual-ca-backup-recovery.md`）。本版接受的**残余**风险只有两条，且均可由运维动作压低：(a) 操作者未按期执行或未校验备份，导致备份不可用；(b) 操作者未妥善保管备份介质，导致备份本身成为新的泄露面。**CA 泄露是另一类事件**：它不因恢复旧备份而消除，必须重建信任（§4.6 分支三），不在"已接受风险"范围内。

### 2.3 核心用户旅程（验收视角）

| # | 旅程 | 关键步骤（spec §5） | 对应验收 |
|---|---|---|---|
| U1 | 添加机器 | Web UI 输入 SSH alias 与管理模式 → 服务端解析 SSH 配置 → 非交互连接性测试 → 探测 OS/arch/home/shell/服务管理器/已装 Agents → 展示结果 → 一键 Bootstrap agentd → 装用户服务 → 一次性 enrollment → 出站 mTLS 流建立 → `AgentConnected=True` | A |
| U2 | 应用 profile | 为多台机器指派同一 profile（版本/模型端点/Skills/MCP/rules）→ 控制面计算有效期望状态并递增 generation → 在线节点即时收到新 generation；SSH-only 节点在操作者发起 reconcile 或 Deployment 命中时经一次性 SSH 操作收敛（MVP 不做后台轮询） | B、C、D、E、F |
| U3 | 发现并收敛 drift | 用户手工改了受管字段 → `agentd` 上报不同受管摘要 → UI 显示 `Drifted: True` 与逐字段 diff → 操作者点击 `Reconcile` → 成功后 drift 清除 | G |
| U4 | 修复 agentd | 流断开但 SSH 可达（`AgentConnected=False, SSHReachable=True`）→ UI 暴露 `Repair agentd` → 服务端经 SSH 诊断、替换/重启二进制或服务 → 等待重连 | I |
| U5 | 金丝雀发布 | 创建 Deployment（canary=1、batchSize=1、maxUnavailable=1、pauseOnFailure=true）→ 金丝雀健康门禁通过后才继续下一批 → 注入失败立即暂停 | H |
| U6 | 回滚 | 对失败变更回滚到上一次成功受管状态；版本回滚 = 用同一安装器装回先前观测版本；不可能时明确报 `RollbackUnsupported` | J |
| **U7【新增】** | **中断后的未知态处理** | 控制面重启或节点掉线导致操作停在 `Unknown` → UI 显示"该机有未决操作"并**禁止**新的变更操作 → 节点重连后经 outbox 重报最终结果 → 操作终态化、互斥释放 → 或操作者显式取消/跳过 | §35.2 |

### 2.4 功能需求拆解

优先级定义：**P0** = MVP 验收必需；**P1** = MVP 范围内但可在验收演示中弱化（spec §4.1 in-scope 且未进入 §34）；本表不引入 P2。每条注明 spec 出处章节。

> **编号稳定性约定【新增】**：v1.1 在 FR-9 组末尾**追加** FR-9.7、FR-9.8，不重排任何既有编号，以保证两轮评审中的引用（如"FR-9.6 备份保留"）继续有效。

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
| **FR-1.9【新增】** | **条件三态与新鲜度**：condition status 允许 `True`/`False`/`Unknown`；`Drifted`/`Reconciled` 在"从未采集""采集已过期""期望代已变化且尚未重新采集"三种情形下必须为 `Unknown`，不得以 `False` 冒充"已确认一致"；status 暴露 `lastProbeAt`/`lastInventoryAt`/`observedGeneration` 供 UI 呈现新鲜度 | P0 | §10.4、§22、§35.2 |
| **FR-1.10【新增】** | **未决操作可见与阻塞**：机器存在未决操作（Pending/Running/CancelRequested/Unknown）时，UI 必须显示原因，且新的变更类操作默认被拒绝（返回 `MachineBusy`），除非操作者显式取消/跳过；此约束对 daemon 与 SSH 两条通道一致生效 | P0 | §35.2 |

#### FR-2 Agent 版本管理

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-2.1 | Profile 中每个 Agent 家族可指定 enabled + 精确版本；解析后的快照禁止 `latest` 等浮动版本 | P0 | §8.3、§20.1 |
| FR-2.2 | command installer：argv 数组直执行（不经过 `sh -c`），`${VERSION}` 按参数逐个替换；捕获退出码与 stdout/stderr（限额），脱敏已知敏感环境变量名 | P0 | §8.3、§20.2、§29.9 |
| FR-2.3 | 安装后必须用适配器探测验证版本；失败报 `VersionVerificationFailed` | P0 | §20.2、§30.3 |
| FR-2.4 | 版本回滚 = 用同一安装器安装先前观测版本；不可行时报 `RollbackUnsupported`，不得谎报成功 | P0 | §20.3 |
| FR-2.5 | 适配器可内置经验证的默认安装命令，但一切默认值必须可被 profile 覆盖（如 OMP 安装方式因环境而异） | P0 | §15.2、§20.2 |
| FR-2.6 | 未来 installer-driver 层（npm/brew/mise/签名二进制）不在 MVP，但安装路径须为 driver 抽象留位。**【澄清】本条是"未来扩展约束"，不是 MVP 必须实现的功能；MVP 只要求 `command` driver 存在且抽象点不被写死** | P1 | §8.3、§37 |

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
| **FR-6.7【新增】** | **工件路径与名称安全**：skill 名称必须匹配 `^[A-Za-z0-9._-]+$` 且不等于 `.`/`..`；digest 必须匹配 `^sha256:[0-9a-f]{64}$`；解包/物化路径拼接后必须仍在既定根目录内（先归一化再前缀校验），拒绝绝对路径与含 `..` 的路径元素；**Skill 内容内的符号链接默认拒绝解析**（`SkillPathRejected`），除非严格限定在工件根内且逐条记录 | P0 | §17.3、§29.11 |
| **FR-6.8【新增】** | **工件与 bundle 资源预算**：单 Skill 工件大小、解包后总大小、文件数上限、单 bundle 总量上限均须有配置项与显式超限失败码（`ArtifactTooLarge`），避免传输/解包在无上界的情况下运行；默认值见 §7.4 | P0 | §35.1、§13.4 |

#### FR-7 期望状态解析与快照

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-7.1 | 服务端把每台机器解析为一个不可变 `DesiredStateSnapshot`；输入 = AgentProfile + 引用 Skill 的精确修订/digest + 引用 ModelProvider + Machine overrides + adapter schema 版本 | P0 | §9 |
| FR-7.2 | 快照含确定性 SHA-256 摘要；**generation 仅在有效期望状态变化时递增**（内容寻址，而非每次保存都递增） | P0 | §9 |
| FR-7.3 | 快照只含归一化期望状态与不可变 Skill 工件引用；不含秘密值 | P0 | §9、§3.6 |
| FR-7.4 | `GET /profiles/{id}/render?machine=<id>` 支持渲染预览 | P0 | §23.2 |
| **FR-7.5【新增】** | **代与内容身份分离**：`(machine_id, generation)` 唯一标识一次期望；同一内容可以出现在多个 generation（A→B→A）；任何"回滚到第 N 代"的引用必须能解析到具体内容，不得因内容去重而失联。方案见 §6.3 与 ADR-2 | P0 | §9、§21.4 |

#### FR-8 观测状态与 drift

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-8.1 | agentd 采集：已装 Agent 版本、适配器受管配置投影、Skill 目标修订/内容 digest、MCP 受管投影、rules 受管投影、适配器健康、服务健康 | P0 | §10.1 |
| FR-8.2 | 观测摘要按归一化受管状态计算；适配器不得对整份配置文件哈希（只哈希自有字段投影） | P0 | §10.2 |
| FR-8.3 | drift 判定：`observed 受管状态 != effective desired 受管状态` → `Drifted=True`；未托管本地配置不得触发 drift | P0 | §10.3 |
| FR-8.4 | 上报节奏（默认，均可配置）：心跳 15s；离线阈值 45s；全量 inventory 在连接建立时、每次操作后、每 5 分钟；本地 inventory 发现受管摘要变化后立即上报 drift | P0 | §10.4 |
| FR-8.5 | UI 提供 `Show Diff`：受管字段的 desired vs observed 逐项对比（如 `codex.config.model: gpt-5.6 → other-model`、`skill/superpowers: 91c7f3 → 3a82d1`） | P0 | §5.3、§34.G |
| **FR-8.6【新增】** | **摘要契约唯一化**：drift 与门禁的**唯一权威判据**是"期望受管投影摘要 vs 观测受管投影摘要"；快照摘要只用于内容寻址与代变更判定；逐字段 diff 只用于 UI 展示，不得作为判据。三类摘要均须携带 `canonicalizationVersion`，同一比较双方版本不一致时不得沿用旧判定，必须重算并显式记录 | P0 | §10.2、§10.3 |
| **FR-8.7【新增】** | **观测有序性**：每次 inventory 上报携带节点本地单调递增的 `inventorySeq`；服务端必须拒绝用较旧 `inventorySeq` 的观测改写由较新观测得出的条件（防止迟到报文抖动条件） | P0 | §10.4、§22 |

#### FR-9 Reconcile（收敛执行）

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-9.1 | 本地 reconciler 13 阶段固定流水线（validate → inventory → plan → backup → 应用版本/配置/Skills/MCP/rules → 健康检查 → 再 inventory → 校验 desired==observed → 提交成功）；daemon 与 oneshot 模式复用同一实现（AD-5） | P0 | §14.1、§3.4 |
| FR-9.2 | 可变步骤失败：停止后续步骤 → 保留诊断 → 用本次操作备份自动恢复 → 恢复后再 inventory → 同时上报原始失败与恢复结果；恢复也失败 → `Degraded=True` 并停止向该机继续自动 rollout | P0 | §14.2 |
| FR-9.3 | 幂等：同一期望快照执行两次，第二次不得产生实质变更 | P0 | §14.3 |
| FR-9.4 | 手动触发（UI/API `POST /machines/{id}/reconcile`）与 Deployment 驱动两条入口；SSH-only 节点经 `agentd oneshot plan/apply --bundle` 收敛 | P0 | §23.1、§13.4 |
| FR-9.5 | 性能：无 drift 的在线节点 reconcile plan 通常 <1s（不含网络/包管理操作）。**【澄清】该指标的适用前提是 daemon 模式、热缓存、inventory 已完成；不适用于 SSH-only 路径（含 scp 上传），也不构成对控制面数据库延迟的承诺** | P0 | §35.3 |
| FR-9.6 | 变更前备份至 `~/.local/share/agent-fleet/backups/<operation-id>/`，保留每机最近 10 次（可配置）；备份元数据足以立即回滚 | P0 | §16.3 |
| **FR-9.7【新增】** | **automatic reconcile —— 自动 plan 与自动上报（默认开启）**：daemon 模式节点在收到新 generation 或本地 inventory 检出受管摘要变化时，自动执行 reconcile 流水线的**只读部分**（validate → inventory → plan），产出计划并向控制面上报（作为 Operation 记录，`type=AutoPlan`，`readOnly=true`）；该路径**不得**执行任何变更阶段。暂停开关与审计见 §7.8 | P0 | §4.1、§10.4 |
| **FR-9.8【新增】** | **automatic reconcile —— 自动 apply（默认关闭，须显式开启）**：操作者可在机器或全局级别显式开启"drift 自动收敛"。开启后，自动 apply 必须是控制面下发的普通 Operation（有 operationId、受机器级互斥约束、受单 worker 队列串行化、产生完整审计记录），**不是**节点自行发起的无记录变更。自动 plan/上报在任何配置下都**不**等同于自动 apply | P0 | §4.1、§8.7 |
| **FR-9.9【新增】** | **执行权与串行化**：任一时刻某台机器上至多一条变更流水线在执行；该约束由节点本地跨进程执行权（§5.6）与服务端持久化互斥（§4.3）**双重表达**，缺一不可 | P0 | §14.3、§35.2 |
| **FR-9.10【新增】** | **操作终态与代绑定**：操作完成时若其 `desiredGeneration` 小于机器当前 generation，终态记为 `Succeeded(Superseded)`，保留为历史事实，**不**写 `Reconciled=True`，也**不**把机器拉回旧代；`Drifted`/`Reconciled` 永远对机器**当前**期望求值 | P0 | §22、§21.3 |

#### FR-10 Deployment（机群发布编排）

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-10.1 | Deployment 资源：显式机器列表、`targetGeneration`、策略（canary 数、整数 batchSize、maxUnavailable、pauseOnFailure） | P0 | §8.6、§21.1 |
| FR-10.2 | 发布算法：解析目标 → 过滤不可用/降级机器 → 金丝雀批 → 要求 reconcile 成功 + 健康 → 逐批继续 → 失败策略触发立即暂停；无需时间型渐进发布 | P0 | §21.2 |
| FR-10.3 | 健康门禁（缺一不可）：操作成功 + apply 后 inventory 完成 + 观测受管摘要 == 期望摘要 + 适配器健康检查通过。**【契约】四条证据必须与同一 `machineId`/`operationId`/目标代快照因果绑定，见 §4.4** | P0 | §21.3 |
| FR-10.4 | 回滚 = 创建指向既往 recorded generation 的新 Deployment；历史不可变 | P0 | §21.4 |
| FR-10.5 | Deployment 暂停/恢复/回滚 API 与 UI 进度（含逐机结果） | P0 | §23.5、§24.6 |
| **FR-10.6【新增】** | **目标代一致性检查**：Deployment 推进某目标前必须校验 `targetGeneration` 仍等于该机当前 generation；若机器当前代已高于目标代，该目标置 `Superseded` 并**不**执行（防止把机器"拉回旧代"造成回退震荡；回退只能由显式 rollback 表达）；若低于目标代，等待其收敛 | P0 | §21.2、§21.4 |
| **FR-10.7【新增】** | **未决目标不推进**：目标机存在未决操作（含 `Unknown`）时，Deployment 该目标保持阻塞，不计入失败也不推进批次；操作者可在 UI 显式跳过（记为 `Skipped` 并留审计），符合"宁停不错" | P0 | §35.2、§21.2 |

#### FR-11 Enrollment、证书与传输安全

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-11.1 | Bootstrap 流程 10 步（建 Machine → SSH 探测 → 一次性短时效 token → SSH 下发二进制+引导配置 → agentd 生成本地私钥 → 携 Machine ID+token+CSR 连 enrollment 端点 → 校验 token → Fleet CA 签发客户端证书 → token 失效 → 私钥/证书以仅用户可读权限落盘 → 开启常规 mTLS Connect 流） | P0 | §12.1 |
| FR-11.2 | Fleet CA 服务端首次启动本地创建；Agent 证书有限期，agentd 在到期前经已认证 mTLS 通道续期；CA 私钥留在控制面主机且权限 0600；外部 PKI/HSM 非 MVP | P0 | §12.2 |
| FR-11.3 | 节点无入站管理端口；连接方向恒为 agentd → 控制面（AD-3） | P0 | §11.1 |
| **FR-11.4【新增】** | **enrollment 传输强制 TLS**：`Enroll` 必须经 TLS 且 agentd **校验服务端证书**（使用 bootstrap 经 SSH 下发的 CA 证书），不接受明文 HTTP/2 路径；无客户端证书不等于可以明文（spec §11 的 "protobuf + bidirectional gRPC over TLS" 与 §29.5 均为非可选） | P0 | §11、§29.5 |
| **FR-11.5【新增】** | **token 原子消费与绑定**：token 校验与作废在**单个数据库事务内原子完成**（CAS 语义），并发请求至多一个成功；token 熵 ≥128-bit（熵下界为本版新增设计决策，spec 未规定具体数值，见 §14.4）；token 绑定 `machineId` **与** CSR 公钥指纹，二者任一不匹配即拒绝 | P0 | §12.1、§29.6 |
| **FR-11.6【新增】** | **重复连接与删除机器策略**：同一 `machineId` 的并发第二条 `Connect` 流默认**拒绝并产生告警事件**（不做"新连接挤掉旧流"）；机器被删除后其证书自然到期失效（无吊销），`agent_certificates` 保留序列号作为审计事实 | P0 | §11.1、§22 |

#### FR-12 SSH 传输与集成

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-12.1 | 使用系统 OpenSSH 客户端（`ssh -G`、`ssh -o BatchMode=yes <alias> -- <cmd>`、`scp`），经受限执行器调用；本地命令构造不用 shell，远程参数经专用工具安全引用；保留 Host/Include/ProxyJump/identity 选择/known_hosts/ssh-agent 等既有行为（AD-4） | P0 | §13.1 |
| FR-12.2 | 认证沿用操作者本地 OpenSSH 已支持的非交互方式；不支持密码提示；服务端不摄取 SSH 私钥内容入库 | P0 | §13.2、§29.2 |
| FR-12.3 | host-key 策略：保留标准校验；UI 必须呈现 host-key 失败，禁止静默 `StrictHostKeyChecking=no` | P0 | §13.3 |
| FR-12.4 | SSH-only reconcile：服务端构建不可变操作 bundle（`manifest.json` = DesiredStateSnapshot + digest；`artifacts/skills/<digest>/...` 仅含被引用工件；bundle 自带 SHA-256 摘要）→ scp 上传到远端临时目录 → `oneshot plan` → `oneshot apply` → 删除临时目录；agentd 在 plan/apply 前校验全部工件 digest；agentd 缺失时先上传临时兼容二进制 | P0 | §13.4 |
| FR-12.5 | SSH 错误分类不坍缩：DNSResolveFailed / HostKeyVerificationFailed / AuthenticationFailed / ConnectionTimeout / RemoteCommandFailed / UnsupportedPlatform | P0 | §30.1 |
| FR-12.6 | OpenSSH include 导出：渲染 `~/.ssh/agent-fleet.conf`（preview / 导出 / 显式确认后安装更新）；仅为 Fleet 持有显式连接字段的机器导出；经既有 hostAlias 导入的机器不重复导出；绝不静默改写用户主 SSH 配置；动因 = Codex Desktop Remote SSH 的 OpenSSH 配置发现集成（§40） | P0 | §13.5、§34.K |
| **FR-12.7【新增】** | **确认对象是 plan 而非仅目标代**：人发起的 SSH 路径中，操作者确认的是**具体计划**（由 `planDigest` 标识）；`apply` 前必须重新采集观测并重算 plan，若重算基线（观测受管摘要 + `inventorySeq`）与 plan 时不一致，则拒绝执行并报 `ReplanRequired`，要求重新 plan 与重新确认 | P0 | §13.4、§14.1 |
| **FR-12.8【新增】** | **SSH 上传工件的完整性**：经 SSH 上传的临时 agentd 二进制、bundle 及其全部条目均须携带并校验摘要（与 Skill 工件同一标准），不得以"SSH 已加密"为由省略校验 | P0 | §29.11、§13.4 |

#### FR-13 agentd 协议

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-13.1 | protobuf + 双向流 gRPC over TLS；服务 `FleetAgentService.Connect / FetchArtifact` 与 `FleetEnrollmentService.Enroll / RenewCertificate` | P0 | §11 |
| FR-13.2 | 消息集：Agent→Server：Hello / Heartbeat / ObservedState / OperationStarted / OperationProgress / OperationResult / LogEvent；Server→Agent：Welcome / DesiredStateChanged / ExecuteOperation / CancelOperation / RequestInventory；LogEvent 仅运维日志，禁止 Agent 会话内容 | P0 | §11.3–11.4 |
| FR-13.3 | 重连：指数退避 + 抖动；重连必以 Hello + 全量观测快照开始；服务端对重复的操作结果投递幂等 | P0 | §11.5 |
| FR-13.4 | 版本协商：快照与协议消息均带 `protocolVersion`/`schemaVersion`；服务端拒绝不支持的 daemon 主版本；agentd 在 Hello 中上报支持的 adapters/schema 版本；服务端不下发 agentd 看不懂的期望状态，改由 UI 提示升级 | P0 | §31 |
| FR-13.5 | daemon 模式 Skill 工件经认证的 `FetchArtifact` gRPC 流按 digest 下载并本地校验 | P0 | §13.4、§17.2 |
| **FR-13.6【新增】** | **快照下发必须自包含**：`ExecuteOperation` 一律携带**完整快照**；MVP 不定义"仅摘要引用"的形态（冷启动节点没有取回快照的 RPC 路径，引用形态会造成必然失败）。摘要引用形态与配套的 `GetSnapshot` RPC 一并留待 Post-MVP | P0 | §11.4、§31 |
| **FR-13.7【新增】** | **节点结果 outbox 与重报**：agentd 在本地持久化最近 N 条已终结的 `OperationResult`（含 operationId、generation、终态、finishedAt、verify 证据摘要），重连后按序重发；服务端按 operationId 幂等去重。这是 spec §35.2 "unless a later agent report proves their final result" 中"agent 报告"的具体落地 | P0 | §35.2、§11.5 |
| **FR-13.8【新增】** | **重复派发幂等**：节点对同一 `operationId` 的重复 `ExecuteOperation` 必须幂等处理——执行中则忽略并回报已知进度；已终结则直接重发该操作的终态结果（取自 outbox），不得启动第二条流水线 | P0 | §11.5 |
| **FR-13.9【新增】** | **取消语义**：`CancelOperation` 仅在阶段边界生效；处于可变阶段（5–10）时取消 = 走标准备份恢复路径后终态 `Failed(Cancelled)`；重复取消幂等；节点不可达时取消**不生效**，服务端不得单方面把操作置为 `Cancelled`——在收到节点结果前只能保持 `CancelRequested` | P0 | §11.4、§14.2 |

#### FR-14 Web UI 与 API

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-14.1 | REST `/api/v1`：machines / profiles / skills / providers / deployments 五类资源 CRUD + 动作端点（probe/bootstrap/repair/inventory/reconcile/rollback、resolve、pause/resume/rollback、render）+ `GET /events`（SSE） | P0 | §23 |
| FR-14.2 | SSE 事件含资源类型/ID + revision，供 UI 选择性重取 | P0 | §23.6 |
| FR-14.3 | 页面：Overview（卡片 + 三张表）、Machines（列与动作）、Machine Detail（9 个区块，无交互 shell）、Profiles（JSON/YAML 预览 + 消费机器）、Skills、Deployments、SSH Inventory | P0 | §24 |
| FR-14.4 | SPA 技术栈推荐 React + TypeScript + Vite；API 契约不得依赖前端框架 | P0 | §24 |
| **FR-14.5【新增】** | **UI 必须呈现的状态区分**：drift 三态（一致 / 未知或过期 / 漂移）、未决操作及其阻塞原因、Deployment 目标的 `Superseded`/`Skipped` 原因、SSH 路径的 `AwaitingConfirmation` 与 plan 基线失效提示 | P0 | §24.2、§24.3 |

#### FR-15 操作审计与错误模型

| 编号 | 需求 | 优先级 | spec 出处 |
|---|---|---|---|
| FR-15.1 | 每次变更产生不可变 Operation 记录：machine/type/transport/desiredGeneration/phase/steps（每步 phase）；输出脱敏环境值，永不捕获秘密 | P0 | §8.7 |
| FR-15.2 | 错误模型四组（v1.1）：SSH 错误（6 类）、Agent 错误（6 类）、Reconcile 错误（14 类）、操作协调错误（4 类）；每条用户可见错误含 reason code、人读消息、operation ID、时间戳、安全诊断 | P0 | §30 |
| FR-15.3 | 服务端重启后，in-flight 操作置为 `Unknown`，除非后续 agent 报告给出最终结果 | P0 | §35.2 |
| **FR-15.4【新增】** | **Unknown 不是终态**：`Unknown` 占用机器级互斥、阻止自动 rollout、且在 Deployment 中阻塞其目标；只有在收到节点重报（outbox）、操作者显式取消（且节点确认停止）或操作者显式跳过之后才离开。**禁止**用"重新 inventory 匹配"把 Unknown 推断为成功——inventory 只能证明采集时的状态，不能证明历史上没有触发恢复路径 | P0 | §35.2、§14.2 |
| **FR-15.5【新增】** | **审计充分性**：Operation 记录须含 `readOnly` 标记、`planDigest`（如适用）、`verify` 证据（受管摘要对、`inventorySeq`、健康检查结果）、以及终态修饰（`Superseded`/`Cancelled`/`Skipped`），使事后可区分"真成功""被取代的成功""被跳过" | P0 | §8.7、§29.8 |

### 2.5 非功能需求

| 编号 | 类别 | 需求 | spec 出处 |
|---|---|---|---|
| NFR-1 | 规模 | ≤100 机器；每机 ≤10 个受支持 Agent 实例；目录 ≤200 Skills；默认 ≤20 并发变更操作。为验证目标而非硬上限 | §35.1 |
| NFR-2 | 性能 | 无 drift 在线节点 reconcile plan 通常 <1s（不含网络/包管理操作）。**【澄清】适用前提 = daemon 模式 + 热缓存 + inventory 已完成；该指标在节点侧计算，与控制面 SQLite 写延迟无关** | §35.3 |
| NFR-3 | 可靠性 | 服务重启保留全部资源状态；agentd 自动重连；重启后 in-flight 操作置 `Unknown`；reconcile 幂等 | §35.2 |
| NFR-4 | 安全 | §29 全部 14 条为**非可选**（详见第 11 章逐条落点） | §29 |
| NFR-5 | 可升级 | SQLite schema 变更走显式迁移；协议与期望状态 schema 自首个 MVP 版本起即版本化 | §35.4 |
| NFR-6 | 可测试 | 每条变更路径可用临时 HOME 测试，不触碰开发者真实 Agent 配置 | §38.12 |
| NFR-7 | 可观测 | 结构化日志（resource_type/resource_id/machine_id/operation_id/deployment_id/reason）；`/metrics` 含指定指标（v1.1 增补，见 §12）；指标标签不含凭据/命令输出/模型秘密 | §33 |
| NFR-8 | 平台 | Linux amd64/arm64、macOS amd64/arm64；WSL 视同 Linux；macOS 路径可沿用 XDG 兼容路径 | §4.1、§7.1 |
| NFR-9 | 契约稳定 | REST JSON API 契约不依赖前端框架；proto 定义集中于 `api/proto/fleet/v1/` | §24、§27 |
| **NFR-10【新增】** | **容量假设可验证** | 控制面容量、数据库写入串行点的表现均作为**待验证假设**记录（§12.1），并给出验证方法与阈值；不得在缺少实测的情况下声称"无容量风险" | §35.1 |

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

**v1.1 明确不引入的东西**（对应两轮评审中"不得由评审直接宣布为需求"的项）：

- 不引入企业级 PKI/HSM、CRL/OCSP、证书吊销列表；
- 不引入无限期保留所有历史快照的承诺（保留契约见 §10.4）；
- 不引入"永久保留所有工件"的承诺（改为引用保护 + 有界清理）；
- 不以新增服务（消息队列、缓存、外部 DB）替代已给出的最小方案；
- **【v1.1.1 修正】不新增内置的 CA 备份导出产品功能**：这是用户已决的 MVP 范围边界（KM-14；§14.3 RD-7），它排除的是"产品里多一个导出动作及其密钥保护问题"，**不排除人工备份**。人工安全备份、恢复与演练是必须的运维动作，写在 `manual-ca-backup-recovery.md`，不得被表述为"不备份"或"已接受风险"（§4.6）。同样地，不把 token 熵数值等运维细节写成未经验证的既定需求（改为显式决策项，见 §14、§17）。

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

**v1.1 追加的护栏**（与上表同等强制）：

13. **【新增】不得以"重新 inventory 匹配"推断历史操作成功**——不得把 `Unknown` 直接改写为 `Succeeded`；
14. **【新增】不得用"服务端接收时间晚于操作结束时间"代替证据因果绑定**（理由见 §4.4）；
15. **【新增】不得依赖控制面内存中的互斥**表达机器级串行——互斥必须在持久层可恢复；
16. **【新增】不得让节点在未持有本地执行权的情况下开始任何变更阶段**；
17. **【新增】回滚不得以"整文件还原备份"实现**（会静默毁掉未托管编辑）——面向 generation 的回滚必须是对目标快照的重放（§7.2）；
18. **【新增】不得在未校验工件路径边界与摘要格式的前提下解包或物化任何外部内容**。

### 2.8 需求汇总统计

> **统计口径【契约】**：以下数字由脚本对 §2.4 的 FR 表格逐行机械统计（正则匹配表格行 + 编号去重），不是目测。修订后必须重新统计，**不得沿用旧数字**——v1.0 正文 §18 写"55 条"而表格实为 70 条，正是统计文字未随表格同步造成的。

| 组 | FR-1 | FR-2 | FR-3 | FR-4 | FR-5 | FR-6 | FR-7 | FR-8 | FR-9 | FR-10 | FR-11 | FR-12 | FR-13 | FR-14 | FR-15 | 合计 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| v1.0 条数 | 8 | 6 | 4 | 3 | 2 | 6 | 4 | 5 | 6 | 5 | 3 | 6 | 5 | 4 | 3 | **70** |
| v1.1 新增 | +2 | 0 | 0 | 0 | 0 | +2 | +1 | +2 | +4 | +2 | +2 | +2 | +4 | +1 | +2 | **+25** |
| **v1.1 条数** | **10** | **6** | **4** | **3** | **2** | **8** | **5** | **7** | **10** | **7** | **6** | **8** | **9** | **5** | **5** | **95** |

- 功能需求 15 组（FR-1～FR-15），共 **95 条**子项，其中 **P0 = 94 条、P1 = 1 条**（唯一 P1 仍为 FR-2.6）；
- 非功能需求 **10 组**（NFR-1～NFR-10）；
- 安全强制项 14 条（NFR-4 引用），另加 v1.1 落点补充（第 11 章逐条）；
- 验收标准 12 项（A–L）与护栏 **18 条**（原 12 + v1.1 追加 6），全部可在 FR 表中找到承载条目（映射见第 13.3 节）；
- **FR-2.6 的性质【澄清】**:它是"未来扩展约束"（spec §37 明确把原生 installer drivers 放在 Post-MVP），不表示 MVP 必须实现 96 项功能。

**统计校核记录**：本节数字与 §18 结论段数字必须一致，且二者都由机械统计产生（验证记录见 `architecture-revision-resolution.md` 的文档验证小节）。

---


## 3. 总体架构

### 3.1 架构风格与总原则

系统采用**单进程声明式控制面 + 双通道节点代理**架构（spec §6）：

- 控制面为单进程单体（AD-1），内部按包/模块划界，不拆服务；
- 一切管理操作以期望状态为源点（声明式优先，§3.1）；
- 节点侧只有二进制 `agent-fleet-agentd`，`daemon` 与 `oneshot` 复用同一本地 reconciler（AD-5）；
- 两条管理通道：mTLS 出站 gRPC（持续）与系统 OpenSSH（bootstrap/兜底/SSH-only）；
- K8s 式 `metadata/spec/status` + generation + conditions + controller 循环，但不依赖 K8s（AD-7）；

**v1.1 的架构性补充【契约】**：v1.0 把"同一机器同时只允许一个变更操作"完全交给控制面的 Machine 级互斥。这在 daemon 与 SSH 两条通道并存、且控制面可能重启的前提下**不成立**——控制面内存中的互斥随进程消失，而节点上的旧流水线仍在写文件；控制面也无法阻止操作者手工启动的 `oneshot` 进程。因此 v1.1 明确：**串行化必须同时在节点侧与持久层表达**（不变量 I-8/I-9，机制见 §5.6 与 §4.3）。

架构必须满足的不变量（从需求推导，实现与评审时逐条对照）：

| # | 不变量 | 来源 |
|---|---|---|
| I-1 | 控制面核心逻辑不知道任何 Agent 专属路径/格式；一切 Agent 差异封装在适配器内 | FR 护栏 #2 |
| I-2 | daemon 与 SSH 两条通道执行的是同一个本地 reconciler 二进制代码 | AD-5、护栏 #1 |
| I-3 | 任何秘密值（API key 值、SSH 私钥内容）不进入 SQLite、日志、操作记录、观测负载 | §29.1/29.2、§34.L |
| I-4 | apply 前必须解析出不可变快照（无 `latest`）；快照摘要确定性 | FR-7.2、护栏 #9 |
| I-5 | 任何变更先备份，失败自动恢复，恢复失败置 `Degraded` | FR-9.2/9.6 |
| I-6 | 未托管字段永不因 Fleet 操作而丢失（结构化文件合并 + 标记块）——**包括回滚** | §16、护栏 #3 |
| I-7 | reconcile 成功报告前必须完成 apply 后 inventory + 健康检查且 desired==observed | 护栏 #10、§21.3 |
| **I-8【新增】** | **任一时刻每台机器至多一条变更流水线在执行**；该性质由节点本地执行权与服务端持久化互斥共同保证，任一单独存在都不充分 | FR-9.9、§35.2 |
| **I-9【新增】** | **操作的终态必须由事实产生**：节点报告、显式取消确认、或显式跳过；不得由服务端从"当前状态看起来一致"推断历史成功 | FR-15.4、§35.2 |
| **I-10【新增】** | **回滚与恢复都必须保留未托管字段**：面向 generation 的回滚是对目标快照的重放；操作内恢复若检测到外部编辑冲突则拒绝静默覆盖，不得以"已接受限制"豁免 | FR-9.2、§16、A5 |
| **I-11【新增】** | **drift 与门禁使用同一的可比摘要，且比较双方规范化版本一致**；快照摘要不得充当 drift 判据 | FR-8.6、§10.2 |

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
- Skill 源（Git 仓库 / 控制面本地目录）是唯一的外部数据依赖；MVP 不依赖任何云服务；
- **【新增】同一台机器上可能同时存在 `agentd daemon` 进程与操作者手工启动的 `agentd oneshot` 进程**；两者都是变更执行者，因此节点必须自持执行权（§5.6），这一事实也决定了运行假设 A5。

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
|  | + 未决操作阻塞      |   | + 持久化互斥(§4.3)   |   | + 代一致性(§4.4)    |  |
|  +--------+-----------+   +----------+----------+   +---------+----------+  |
|           |                          ^                        |              |
|           v                          |                        v              |
|  +--------------------+   +----------+----------+   +--------------------+  |
|  | SSH 控制器          |   | 期望状态渲染器       |   | Enrollment/CA 服务  |  |
|  | 受限执行器          |   | (DesiredStateSnapshot)|  | token 原子消费/TLS  |  |
|  | + staging 管理      |   | + 投影契约(§7.1)     |   | 重复连接策略(§7.5)  |  |
|  +--------+-----------+   +----------+----------+   +---------+----------+  |
|           |                          ^                        |              |
|           v                          |                        v              |
|  +--------------------+   +----------+----------+   +--------------------+  |
|  | Skill 解析器/缓存    |   | SQLite 存储          |   | agent gRPC 端点     |  |
|  | git/local→digest    |   | (fleet.db + 迁移)    |   | Connect/FetchArtifact| |
|  | + 路径/预算校验(§7.4)|  | + 持久化互斥约束     |   | + 结果幂等/去重     |  |
|  +--------------------+   +---------------------+   +--------------------+  |
+-----------------------------------------------------------------------------+
        | SSH                                        ^ mTLS gRPC
        v                                            | (出站)
+-------------------------------+        +-------------------------------+
| 开发机（daemon 模式）          |        | 开发机（SSH-only 模式）        |
| agent-fleet-agentd daemon     |        | sshd + agent-fleet-agentd     |
|  + 本地 reconciler（共享）      |        |   oneshot inventory/plan/apply|
|  + 执行权锁 (§5.6)             |        |  + 同一本地 reconciler + 适配器 |
|  + 结果 outbox (§5.2)          |        |  + 同一执行权锁 (§5.6)         |
+-------------------------------+        +-------------------------------+
```

### 3.4 请求与数据流（C3 关键流）

**流 1：期望状态传播（daemon 模式）**

```text
操作者保存 Profile 变更
→ 期望状态渲染器为每台引用机器重算 DesiredStateSnapshot
→ 摘要与上一代相同 → 不新增代，流程结束
→ 摘要变化 → generation+1，落库（按代唯一，内容可复用）
→ agent gRPC 端点向在线节点的 Connect 流推送 DesiredStateChanged{generation}
→ agentd 收到新代 → 自动执行只读流水线（validate → inventory → plan）→ 上报 AutoPlan Operation
→ 操作者/Deployment 触发 apply（§7.8；自动 apply 默认关闭）
→ 派发 ExecuteOperation{operationId, 完整快照}（受机器级互斥与节点执行权双重约束）
→ 上报 ObservedState → 服务端按"当前代"重算 conditions → SSE 推送 UI
```

**【契约】与 v1.0 的差异**：v1.0 写"本地 reconciler plan（可自动 plan，apply 按策略/操作者触发）"，但"策略"从未定义。v1.1 把这条路径拆成两件互不混淆的事：**自动 plan/上报**（只读，默认开启，见 FR-9.7）与**自动 apply**（写操作，默认关闭，且必须是控制面下发的普通 Operation，见 FR-9.8、§7.8）。

**流 2：SSH-only reconcile（操作者触发）**

```text
POST /machines/{id}/reconcile
→ Reconcile 控制器校验：该机无未决操作（否则 409 MachineBusy）
→ 服务端在持久层取得机器级互斥（写 operation 行，phase=Pending）
→ SSH 控制器构建 operation bundle（manifest + 工件，含 bundle digest）→ 写入节点固定 staging 路径
→ scp 上传 → ssh 'agentd oneshot plan --bundle <staging>'（无 agentd 或版本不兼容则先上传临时二进制并校验其 digest）
→ 节点取得执行权（§5.6）→ 产出 plan 与基线（观测受管摘要 + inventorySeq + 快照摘要）→ 服务端记录 planDigest → Operation 进入 AwaitingConfirmation
→ 操作者确认该 plan（Deployment 驱动路径按策略自动确认）
→ ssh 'agentd oneshot apply --bundle <staging>' → 节点重取观测、重算 plan：基线与 plan 时不一致 → 拒绝（ReplanRequired，零变更）
→ 一致则执行变更 → 返回 OperationResult JSON → 释放执行权
→ 服务端记录 Operation/conditions → 清理 staging 与临时二进制
```

**流 3：Canary Deployment**

```text
POST /deployments（selector + targetGeneration + 策略）
→ Deployment 控制器解析目标机、过滤 unavailable/degraded
→ 目标代一致性检查：targetGeneration < 机器当前 generation → 该目标置 Superseded（FR-10.6）
→ 金丝雀批（canary=N 台）：逐台触发 reconcile（受机器级互斥；有未决操作的目标阻塞，不推进）
→ 每台健康门禁（操作成功 + 与该操作绑定（同 operationId、按 inventorySeq 有序）的 apply 后 inventory + 投影摘要一致 + 适配器健康）全部通过
→ 继续 batchSize 批次；任一台失败且 pauseOnFailure → Deployment 置 Paused
→ 操作者修复后 resume 或创建回滚 Deployment（指向旧 generation）
```

**流 4：Enrollment（新节点入队）**

见第 9.1 与 7.5 节。

**流 5【新增】：Unknown 操作的收敛**

```text
控制面重启 / 节点掉线 → 该机存在 phase=Unknown 的操作，互斥仍被占用
→ UI 显示"该机有未决操作"，新变更操作被拒绝（MachineBusy），Deployment 目标阻塞
分支 A：节点重连 → Hello → outbox 重发该 operationId 的终态结果 → 服务端幂等接收 → 操作终态化 → 释放互斥
分支 B：操作者显式取消 → CancelOperation（节点可达时）→ 节点确认停止并回报 Failed(Cancelled) → 释放互斥
分支 C：操作者显式跳过 → 需在 UI 写明"节点可能仍在写"，服务端把操作置 Superseded(Skipped) 并**保留 Degraded/Unknown 提示**
```
分支 C 不释放"节点是否仍在写"这一事实：跳过只解除控制面的推进阻塞，不宣称节点已停止。节点再次上报时结果仍被接收（幂等），其是否成功只作为历史事实记录，不写 `Reconciled`。

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

沿用 spec §27 布局，本文档补充每包职责约束（**加粗为 v1.1 新增/修订**）：

```text
agent-fleet/
  cmd/agent-fleet-server/     # 入口：装配 HTTP/gRPC/控制器，禁止业务逻辑
  cmd/agent-fleet-agentd/     # 入口：daemon | oneshot inventory|plan|apply | doctor | version
  api/proto/fleet/v1/         # agent.proto（连接）+ state.proto（状态消息）
  api/openapi/                # REST 契约
  internal/
    domain/                   # 领域类型：资源、conditions、digest、错误码（纯类型，无 IO）
    store/sqlite/             # 仓储实现 + 迁移装配（含机器级未决操作唯一约束）
    server/httpapi/           # REST 处理器：参数校验→调用控制器→映射错误码
    server/sse/               # 事件枢纽（类型/ID/revision）
    server/grpcagent/         # agent gRPC 端点：连接注册表、流推送、工件流、结果幂等去重
    controller/machine/       # 条件维护、探测编排、生命周期、新鲜度与三态判定
    controller/reconcile/     # 期望渲染触发、操作派发、持久化互斥、代绑定与 Superseded
    controller/deployment/    # canary/批次/门禁因果校验/暂停/恢复/目标代一致性
    sshtransport/             # 受限执行器：ssh -G / BatchMode exec / scp / 安全引用 / staging 管理
    enrollment/               # token 生命周期（原子消费）、CSR 校验与绑定、CA（Fleet CA 本地生成）
    skills/                   # git/local 解析、SKILL.md 校验、digest、工件缓存、路径与预算校验
    desiredstate/             # 有效期望解析、确定性摘要、快照存储、投影规范化契约
    operations/               # Operation/步骤记录、终态修饰、Unknown 处置、重试与取消语义
    agentlocal/               # —— 仅 agentd 二进制链接本组包 ——
      reconciler/             # 13 阶段共享流水线（daemon/oneshot 唯一实现）
      execrights/             # 【新增】节点本地跨进程执行权（daemon/oneshot 共享锁）
      outbox/                 # 【新增】已终结操作结果与最后观测的持久化与重报
      inventory/              # 观测采集（调用各适配器）、inventorySeq、投影摘要计算
      backup/                 # 备份/恢复、外部编辑冲突检测
      installer/              # command installer（argv 直执行、${VERSION} 替换）
      adapter/                # 适配器注册表与公共契约
        codex/ omp/ opencode/ # 家族实现（路径/格式/合并策略封装于此）
  web/                        # SPA
  migrations/                 # SQLite 迁移脚本
  testdata/adapters/          # 各适配器 fixture home
  testdata/ssh/               # SSH 集成测试容器夹具
  testdata/bundles/           # 【新增】恶意/畸形 bundle 夹具（路径逃逸、坏 digest、symlink 逃逸）
  docs/architecture.md        # 本文档（仓库内版本）
```

依赖方向约束（编译期可验证）：

- `controller/*`、`server/*` 不得 import `agentlocal/adapter/{codex,omp,opencode}`（护栏 #2、§27）；
- `agentlocal/*` 不得 import `controller/*`、`server/*`；
- `domain` 不 import 任何运行时包；
- 适配器之间互不 import；新适配器 = 新增 `adapter/<family>` 包 + 注册表注册，控制面零改动（§1 扩展性要求）；
- **【新增】`desiredstate` 与 `controller/reconcile` 不得依赖任何适配器实现来计算投影摘要**——投影由节点侧适配器产出并上报（ADR-1）。

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

条件写入规则示例（**v1.1 已按三态与新鲜度修订**）：

| 事件 | 写入条件 |
|---|---|
| SSH 探测成功/失败 | `SSHReachable=True/False`（失败带 §30.1 的 reason）；同时写 `lastProbeAt` |
| Connect 流建立/断开 + 45s 无心跳 | `AgentConnected=True/False` |
| 收到首个有效 inventory | `InventoryReady=True`（同时写 `lastInventoryAt`、`inventorySeq`、`observedGeneration`） |
| 观测受管投影摘要 ≠ 期望受管投影摘要（同一 canonicalizationVersion）/ 相等 | `Drifted=True/False`、`Reconciled=False/True` |
| **【新增】从未采集过 inventory** | **`Drifted=Unknown`、`Reconciled=Unknown`（reason=`NeverInventoried`）——不得写 False** |
| **【新增】观测新鲜度超过配置阈值** | **`Drifted=Unknown`（reason=`StaleObservation`），保留 `lastInventoryAt` 供 UI 展示"上次确认于 T"** |
| **【新增】期望代已变但尚未据新代采集** | **`Drifted=Unknown`（reason=`ObservationPredatesDesired`），直到该代操作完成并上报新观测** |
| 恢复失败 | `Degraded=True`（后续 rollout 跳过该机，§14.2） |
| **【新增】操作终态为 CancelRequested/Unknown** | **不写 `Drifted`/`Reconciled`；该机由"未决操作"状态单独呈现** |

**【契约】条件写入的两个强制前提**：

1. **评价基准永远是机器当前期望快照**：任何操作结果、任何 inventory 都只触发"按当前代重新求值"，不得按操作携带的旧代写入 `Drifted`/`Reconciled`（FR-9.10）；
2. **观测有序性**：若到达的 `ObservedState.inventorySeq` 小于已处理过的值，则该报文不得改写 conditions（FR-8.7），只可写入历史表用于诊断。

### 4.3 Reconcile 控制器

- 输入：手动 API 触发、Deployment 派发、自动 apply（仅在显式开启时，FR-9.8）；
- 路由：`managementMode=agentd` 且在线 → 经 gRPC `ExecuteOperation` 推送；否则若配置了 SSH → 走 SSH bundle 路径（§13.4）；两者都不可用 → 报错并写 `Operation.Failed`（reason 明确：`AgentDisconnected` 或 SSH 细分原因）；
- 每次触发先确认目标快照 generation；操作完成后核对 apply 后 inventory 的摘要以决定 `Reconciled/Drifted`（**对当前代求值**，见 4.2 前提 1）。

**【契约】机器级互斥：以持久层表达，不以内存表达（v1.1 关键修订）**

v1.0 只写"同一机器同时只允许一个变更操作（由 Machine 级互斥保证）"，未说明该互斥存在哪里。若实现为进程内信号量，则控制面重启即丢失，而节点上的旧流水线仍在执行（A2 反例）。v1.1 规定：

1. 互斥的载体是 `operations` 表上的**部分唯一索引**：

   ```sql
   -- 每台机器至多一个"未决"操作（含 Unknown）
   CREATE UNIQUE INDEX ux_operations_machine_unresolved
     ON operations(machine_id)
     WHERE phase IN ('Pending','Running','CancelRequested','Unknown');
   ```

2. **派发点获取**：Reconcile 控制器在派发前以一次事务创建 `phase=Pending` 的操作行；唯一约束冲突即表示该机已有未决操作 → 请求返回 409 `MachineBusy`（FR-1.10），不排队（MVP 用"拒绝 + 显式取消/跳过"替代复杂队列，宁停不错）；
3. **终态释放**：只有在收到节点结果、或操作者显式取消并获节点确认、或显式跳过之后，方可将 phase 迁出上述集合，从而释放互斥；
4. **重启自愈**：进程重启时把 `Running` → `Unknown`，但**不**移出未决集合——互斥随数据库恢复而恢复，这正是本条的目的；
5. **跨通道一致**：daemon 与 SSH 两条派发路径都必须经过同一张表的同一条约束，禁止任何"绕过互斥直接执行"的路径。

**【契约】并发上限与互斥的关系**：全局 ≤20 并发变更操作（NFR-1）是**资源上限**，机器级互斥是**正确性约束**。两者独立：20 是"能同时推进多少台不同机器"，机器级互斥是"同一台机器不能有两个变更"。

### 4.4 Deployment 控制器

- 状态机：`Pending → Canary → RollingOut → (Paused | Succeeded | Failed)`（phase 命名遵循 §8.6 的 `RollingOut` 风格）；
- 目标解析：`spec.selector.machineNames` 显式列表；解析后过滤 `Degraded=True`、`AgentConnected=False` 且无 SSH 配置的机器（不可达目标保持 pending 并呈现原因）；
- 批次执行：先 canary 批（`strategy.canary` 台），全部通过健康门禁后才继续 `batchSize` 批；`maxUnavailable` 限制同批同时变更数；失败策略 `pauseOnFailure=true` 时立即暂停（§21.2）；
- 回滚：`POST /deployments/{id}/rollback` 创建新 Deployment 指向历史 `targetGeneration`；历史记录不可变（§21.4）；
- 幂等与恢复：控制面重启后，`RollingOut` 的 Deployment 从持久化的批次进度继续。

**【契约】健康门禁的因果绑定（v1.1 关键修订）**

v1.0 只说"门禁评估依赖 apply 后 inventory 上报"，没有定义"哪一份 inventory 才算数"。这意味着**缓存 status 可以冒充证据**：机器 A 曾与 gen12 一致，之后用户手改受管字段而该上报因断线丢失；操作者基于 UI 现状 resume gen12 的 Deployment，门禁读到缓存 `observedDigest == desiredDigest` 加上历史健康记录，判为健康并推进批次——而 A 实际处于 drift（A4 反例）。

v1.1 规定门禁必须由**与本次操作同源、且因果靠后**的证据满足，四条件全部绑定 `machineId` 与 `operationId`：

| 门禁条件 | 必须的证据 | 明确不被接受为证据 |
|---|---|---|
| 1. 操作成功 | 该 `operationId` 的 `OperationResult{phase=Succeeded}`，且其 `desiredGeneration == targetGeneration`；若为 `Succeeded(Superseded)` 则**不**满足 | 机器 status 中的历史 phase |
| 2. apply 后 inventory 完成 | 该操作结果中携带（或紧接着该结果之后由同一 `operationId` 上报）的 inventory，含 `inventorySeq` 与 `operationId` | 任何未标注 operationId 的周期 inventory |
| 3. 投影摘要一致 | 以门禁 2 的那份观测计算的 `observedProjectionDigest`，与 `targetGeneration` 快照的 `desiredProjectionDigest` 相等，且二者 `canonicalizationVersion` 一致 | 机器 status 中缓存的 `observedDigest` |
| 4. 适配器健康通过 | 同一份观测中的适配器健康结果（或该操作步骤 10 的健康检查结果，绑定 `operationId`） | 上一次操作的健康结果 |

**【契约】不得用时间比较代替因果绑定**。评审第二轮曾建议以 `reportedAt > op.finishedAt`（服务端接收序）作为新鲜度判据，本版**不采纳**该形式，理由有二：

1. **方向错误**：spec 的健康检查、apply 后重新 inventory、verify 都发生在**操作成功提交之前**（§14.1 阶段 10/11/12 在阶段 13 之前）。要求这些证据的时间晚于 `finishedAt` 会拒绝掉正常执行；
2. **强度不足**：服务端接收时间不能证明采集时间，迟到的旧报文同样"晚到"，仍然会满足该不等式。

正确做法是 v1.1 采用的**身份绑定 + 序绑定**：证据必须携带产生它的 `operationId`（身份）与 `inventorySeq`（节点本地单调序，用于丢弃乱序旧报文）。由于节点时钟不被信任，`inventorySeq` 只在单机内比较，不跨机比较。

**【契约】目标代一致性（FR-10.6）**：Deployment 推进任一目标前必须校验 `targetGeneration` 与机器当前 generation 的关系：

- 相等 → 正常推进；
- 机器当前代 **高于** 目标代 → 该目标置 `Superseded`，**不执行**，不把机器拉回旧代（回退只能由显式 rollback 表达）；若所有目标都被 Superseded，Deployment 终态 `Failed(SupersededByNewerGeneration)` 并提示操作者；
- 机器当前代 **低于** 目标代 → 等待其收敛到目标代（这是正常落后，不是错误）。

### 4.5 SSH 控制器（sshtransport）

受限执行器规格：

- 只接受**结构化参数**（alias、argv 数组、文件对），不接受拼接命令字符串；
- 本地进程：直接 `exec` OpenSSH 二进制（不经 shell）；远程命令由专用引用工具按 POSIX shell 引用规则构造单个字符串后交 `ssh <alias> -- <cmd>`；
- 探测：`ssh -G <alias>` 解析生效配置（HostName/User/Port/ProxyJump/IdentityFile），用于探测与 include 导出；
- 执行：`ssh -o BatchMode=yes -o ConnectTimeout=<cfg> <alias> -- <cmd>`；文件传输用 `scp`；
- 超时：`connectTimeout=10s`、`commandTimeout=60s`（可配置，§28）；
- 错误映射：stderr/退出码 + OpenSSH 输出模式 → §30.1 六类 reason（如 `Host key verification failed.` → HostKeyVerificationFailed；`Permission denied` → AuthenticationFailed）；不得坍缩为 unreachable；
- 绝不禁用 host-key 校验、绝不注入 `StrictHostKeyChecking=no`（§13.3）；不摄取私钥内容（§13.2）。

**【契约】staging 目录与清理（A10）**：

- 远端 staging 使用**与操作无关的固定路径**：`~/.local/share/agent-fleet/staging/`（0700 创建），bundle 落于其下固定子目录 `bundle/`，临时 agentd 二进制落于 `bin/`；
- 每次操作开始前，在**持有该机互斥**的前提下清空 staging 中属于自己的陈旧内容（固定路径使反复崩溃不再累积 `bundle-<op-id>` 垃圾）；
- **清理规则（与 A10 的汇总裁决一致）**：
  1. 只清理可判定为"无主"的内容——即当前不存在引用该路径的未决操作；
  2. **存在未决操作（含 `Unknown`）时，不得删除其输入**（bundle 与临时二进制都可能是节点尚在读取的工厂材料）；
  3. 操作成功后清理该次操作的输入；操作失败/取消后，在**确认节点已停止**（收到节点结果或下一轮探测确认无进程持有）后清理；
  4. 清理失败不视为操作失败，但须产生告警事件与指标（避免"删不掉"静默累积）。
- 临时 agentd 二进制必须携带 digest 并在节点侧校验后方可执行（FR-12.8、小项 b）。

### 4.6 Enrollment / CA 服务

- 首启本地生成 Fleet CA（自签），`pki/ca.key` 权限 0600（§12.2、§26）；
- Enrollment token：一次性、短时效（默认 10 分钟，可配置），绑定 Machine ID，**并绑定 CSR 公钥指纹**；熵 ≥128-bit；
- **【契约】token 的原子消费**：`Enroll` 中"校验 token 有效性与绑定 → 签发"必须处于**同一数据库事务**，且 token 作废使用条件更新（`UPDATE ... WHERE used_at IS NULL AND ...` 并检查受影响行数）；受影响行数为 0 即拒绝。并发抢注在数据库层被序列化，至多一个请求成功（FR-11.5）；
- **【契约】enroll 必须经 TLS 且校验服务端证书**（FR-11.4）：agentd 在 bootstrap 时已获 CA 证书（经 SSH 下发），据此校验 enroll 端点；无客户端证书（token 才是身份证明）不等于允许明文；
- **【契约】重复连接策略**（FR-11.6）：同一 `machineId` 已有活跃 Connect 流时，第二条流默认拒绝并产生告警事件，不做"新连接顶掉旧流"。理由：顶替语义会把"账号被抢注/私钥泄露"这一安全事件伪装成正常的网络抖动；拒绝 + 告警会把它变成可见事件；
- `RenewCertificate`：仅接受既有有效 mTLS 通道上的续期请求（§12.2），**续期窗口加随机抖动**（避免全机群续期对齐到同一时刻，S4）；
- 服务端证书：`pki/server.crt/key`，advertiseURL 的 TLS 终结点使用；agentd 引导配置需携带 CA 证书以校验服务端；
- **【新增】机器删除后的证书**：MVP 无吊销机制（无 CRL/OCSP，见 2.2 已接受风险）。删除机器时在 `agent_certificates` 写入删除时间与"该序列号至自然到期前仍有效"的审计事实；被删除机器的证书在到期前仍能建立 Connect 流，因此**删除机器不是安全措施**——这一点必须在 UI 上与"已接受风险"一并呈现；
- **【v1.1.1 重写】CA 材料的人工备份、恢复与演练契约【用户已决，KM-14】**：用户已确认（KM-11 评论 `01a0bceb-5547-7531-b625-9957ad171b81`）**MVP 不新增内置 CA 备份导出产品功能**，同时**必须提供人工安全备份、恢复与演练方案**。因此 v1.1 的"不做离线备份＝已接受风险"表述作废，替换为下列契约。完整可执行步骤、前置条件与停止点见 `manual-ca-backup-recovery.md`；本节只固定边界、责任与验收口径。

  1. **责任人**：单操作者（唯一人类角色）本人，或经其明确授权的运维执行者。责任不可转移给产品——产品不发起备份，也不判断备份是否已做。
  2. **执行时机**（人工触发，无产品提醒机制）：
     - CA 首次生成后**立即**（此时尚无客户端证书，未备份即处于"零可用备份"状态）；
     - CA 材料、服务端证书或控制面身份元数据发生任何变更后（如重新生成 CA、轮换服务端证书）；
     - 按固定周期（建议不超过 90 天）复核性重做一次，或每次机群结构发生较大变化后；
     - **证书续期本身不改变 CA 根密钥**，因此不要求每次续期都重做 CA 备份；但若续期伴随服务端证书更换，则按上一条执行。
  3. **备份内容及边界**（缺一不可，但粒度按恢复目标选择）：
     - **CA 根材料**：`pki/ca.key`（0600）与 `pki/ca.crt`——决定"能否继续签发/校验本机群证书"；
     - **服务端证书材料**：`pki/server.crt`、`pki/server.key`（0600）——决定 `advertiseURL` 的 TLS 终结点能否原样恢复；
     - **控制面配置**：`~/.config/agent-fleet/config.yaml`（含 `advertiseURL`、admin token 等的引用/配置）；
     - **身份与控制面元数据**：`fleet.db` 中与恢复直接相关的部分——`machines`（machineId ↔ SSH alias 等身份）、`agent_certificates`（已签发证书序列号/有效期/删除事实）、`desired_snapshots` 与 `operations`（期望代与操作审计）；
     - **工件**：`artifacts/skills/`、`artifacts/agentd/`、`git-cache/`（若恢复目标包含"恢复后可直接 reconcile"，而非仅"恢复控制面能起"）。
     - **边界【契约】**：**只备份 CA ≠ 控制面完整恢复**。CA 材料只解决信任根的连续性；控制面要在原状下启动并保持与既有节点的身份对应，还必须同时具备与 CA 同一时间点的 `fleet.db`、配置与（必要时）工件。反过来，只恢复 `fleet.db` 而不恢复 CA，也无法通过既有客户端的 mTLS 校验。三类材料（CA / 配置 / 身份元数据与工件）必须**成套、同源、时间一致**，这一点必须在恢复前检查中显式确认（本节第 6 条）。
     - **不在备份范围**：任何 API key 值、SSH 私钥内容（产品本就不持久化，§11.1/11.2）；节点侧 `client.key`（属于节点本地，丢失时按本节第 7 条分支二重新 enroll）。
  4. **快照一致性流程【契约】**：默认采用**离线一致性方案**——在操作者控制的维护窗口内停止 `agent-fleet-server`（或至少停止一切会写 `fleet.db`/`pki`/`artifacts` 的产品动作），确认无并发写后再复制，复制后校验，最后恢复运行。**不声称产品支持在线热备**：MVP 不提供备份 API、不提供 SQLite 在线备份命令、不保证"暂停写"由产品自动完成。若操作者自行选择 `sqlite3 .backup` 等在线方案，须自行提供其一致性与可用性的**真实依据**并自行承担验证责任；本版不为其背书。推荐顺序与校验清单见手册。
  5. **敏感材料保护【契约】**：
     - 备份必须落在**操作者控制的加密介质或加密存储**内（如加密卷/加密归档），介质与解密口令**分开保管**；
     - 至少一份**离机副本**（防控制面宿主机的物理/磁盘故障）；
     - 副本访问权限限操作者本人，介质在不使用时离线存放；
     - **保留与可恢复性检查**：保留策略由操作者自定，但必须做**定期可恢复性抽查**（用副本在隔离环境实际走一次恢复检查，见本节第 8 条演练）；
     - **不得**把备份内容、CA 私钥、口令或任何备份文件本体**上传任务系统、日志、工单、代码仓库或任何第三方服务**——包括本项目的任务平台。备份过程中产生的命令行输出不得包含私钥内容。
  6. **恢复前检查与失败停止条件【契约】**：恢复前必须逐项确认，任一项不满足即**停止恢复**并保留原现场：
     - 目标恢复位置为**隔离位置**（先用新目录/新主机还原检查，不直接覆盖线上）；
     - 备份来源可信（介质、时间、校验值可追溯），密文可解密、归档可展开、关键文件权限可还原为 0600；
     - **版本/配置兼容**：恢复的控制面版本与备份生成的版本兼容（迁移脚本可顺序应用，不跳版本、不倒序）；
     - **CA 与证书配对**：`ca.key` 与 `ca.crt` 配对（公钥一致），既有 `client.crt` 能由该 CA 校验通过；
     - **数据一致性**：`fleet.db` 完整可打开、迁移可应用、`machines`/`agent_certificates` 与恢复目标一致；
     - **节点身份/连接验证**：至少一台已知可信节点能在恢复后建立 mTLS `Connect` 流，且其 machineId 与库中记录一致；
     - **退回路径**：开始覆盖线上之前，必须先保有当前（失败）现场的可回退副本；恢复失败时回退到该现场并如实报告，不得"边猜边改"。
  7. **三个分支【契约】**（详见手册，此处只给决策口径）：
     - **分支一：CA 丢失，但有可用备份** → 优先恢复。按本节第 6 条检查清单恢复 CA 材料与控制面元数据；恢复后必须验证既有节点仍能通过 mTLS 连接（身份连续性）。**恢复旧备份只恢复可用性，不消除任何泄露。**
     - **分支二：CA 丢失，且无可用备份** → 无法延续既有信任：必须**重新生成 CA 并重新 bootstrap 全机群**（逐台经 SSH repair 重新 enroll），节点侧旧 `client.crt/key` 作废。这一路径是**最后手段**，不是默认期望；人工备份的存在正是为把它变成小概率事件。
     - **分支三：CA 私钥泄露** → **恢复旧备份不能消除已经泄露的信任**。泄露发生时，攻击者持有的私钥/证书在有效期内仍可被用于伪造节点或冒充控制面。因此唯一的安全恢复是**重新生成 CA、重签/重新 enroll 全部节点，并视泄露范围轮换可能受影响的凭据**；**不得复用已泄露的私钥**作为"恢复"。是否同时删除/替换受影响机器记录，由操作者按泄露时间窗判断，并在审计事件中记录。
  8. **演练【契约，当前状态＝待验证】**：备份存在**不等于**可恢复。操作者必须按手册的演练计划，在**隔离测试环境**中定期执行完整演练并留存记录（模板见 `manual-ca-backup-recovery.md` 附录 A 与 `architecture-revision-resolution-v1.1.1-supplement.md` §5）。演练至少覆盖：归档完整性/权限检查、用备份 CA 与既有可信节点建立连接、用**错误/过期证书**验证被拒绝、关键资源（配置、身份元数据、工件）恢复、以及至少一条**异常恢复路径**（如备份损坏、配对失败、迁移失败时的停止与回退）。**在本版交付时，演练计划已定义但尚未执行**——任何"已可恢复"的结论都必须等真实演练记录产出后才能作出；架构与交付文档不得把计划写成已验证。
  9. **与实现的关系**：上述全部为**运维动作与文档契约**。MVP 不新增产品 API、CLI 子命令或 UI 导出入口来执行它们（§2.6、§14.3 RD-7）。手册中凡涉及产品动作之处，只能写成"步骤/待实现验证"，不得虚构命令。

### 4.7 Skill 解析器 / 缓存

- 输入：`source.type=git`（repo+ref+path）或 `local`（控制面路径）；
- Git 解析：fetch（缓存于 `<data-dir>/git-cache/`）→ ref 解析为精确 commit → 校验 path 下存在 `SKILL.md` → 计算内容 digest（对目录规范化内容树计算 SHA-256）→ 产出不可变工件至 `artifacts/skills/<sha256>/...`；
- 本地解析：同上（无 fetch 步骤）；
- 同一 digest 幂等复用；`skill_revisions` 表记录解析历史（§25）；
- 摘要算法必须规范化（固定文件排序、规范化换行/权限位），保证同内容同 digest（支撑 FR-7.2 确定性）。

**【契约】路径与内容安全（FR-6.7、A7）**：

1. Skill 名称校验 `^[A-Za-z0-9._-]+$` 且不等于 `.`/`..`；digest 校验 `^sha256:[0-9a-f]{64}$`；二者在**入队时（服务端）与消费时（节点侧）各校验一次**；
2. 解包与物化的路径必须"先归一化（`filepath.Clean`）再前缀校验"，结果必须落在既定根目录（服务端 `artifacts/skills/`、节点 `skills/`）之内；出现绝对路径或含 `..` 的路径元素一律拒绝（`SkillPathRejected`）；
3. Skill **内容中的符号链接**：MVP 默认**拒绝解析**并报明确错误码。理由：内容来自外部 Git 仓库（系统唯一外部数据依赖），内容内 symlink 可以在物化阶段把写入引向根外；仅靠"名称正则"与"digest 相等"都无法约束这一点——digest 证明的是"内容未被篡改"，不是"路径安全"（A7）。
4. **摘要匹配不能替代路径安全**：如果实现选择允许根内 symlink，必须逐条记录并在 UI 可见，且仍不得逃出工件根。

**【澄清】工件预算（FR-6.8、S1）**：spec 未给工件大小上限，而 ≤200 Skills 的目录可包含大工件。默认预算（可配置）：

| 项 | 默认值 | 超限行为 |
|---|---|---|
| 单 Skill 工件解包后总大小 | 64 MiB | 解析期拒绝，`ArtifactTooLarge` |
| 单 Skill 工件文件数 | 4096 | 解析期拒绝，`ArtifactTooLarge` |
| 单次操作 bundle 总量（含所有被引用工件） | 256 MiB | 构建期拒绝，操作不派发，`BundleTooLarge` |

超限必须在**构建/解析期**快速失败，不得"传了十分钟才失败"。

### 4.8 期望状态渲染器（desiredstate）

- 输入组装：Machine（含 overrides）→ AgentProfile → 各 `skillRef` 的已解析 revision/digest → `providerRef` 的 ModelProvider → 适配器 schema 版本（§9）；
- 输出：`DesiredStateSnapshot`（纯数据：归一化 agents 版本与配置、skills 工件引用、MCP、rules），含 `protocolVersion/schemaVersion`；
- 摘要：对快照规范化 JSON（键排序、无易变字段）计算 SHA-256，得到 `snapshotDigest`——**仅用于内容寻址与"代是否变化"的判定**，不参与 drift 比较（FR-8.6）；
- **【契约】代与内容身份的落库**：见 §6.3 与 ADR-2。摘要与当前代内容相同时**不新增代**（FR-7.2 语义保持）；摘要变化时新增代，但**允许内容与某个更早的代相同**（A→B→A）；
- 渲染必须纯函数化（同输入必同输出），便于单测（§32.1 前两项覆盖点）。

### 4.9 SQLite 存储

- 单文件 `~/.local/share/agent-fleet/fleet.db`，显式迁移（`migrations/`，启动时按序应用）；
- 表集合按 §25：machines、profiles、providers、skills、skill_revisions、desired_snapshots、observed_states、deployments、deployment_targets、operations、operation_steps、events、agent_certificates；
- 访问原则：
  - 仓储接口定义于领域层，SQLite 实现可替换（AD-2 允许未来 PostgreSQL，§37）；
  - 大内容（Skill 工件、bundle）一律落盘，库中只存 digest 与路径；
  - observed_states 仅保留最新 + 近期操作引用的状态，防止无限增长（§25）；**【澄清】"近期操作引用"是 §10.4 保护引用集合的一个子集，清理必须按该集合判定**；
  - 事务边界：快照落库、操作记录落库、deployment 目标进度更新各自原子提交；**token 消费与签发在同一事务（§4.6）**；
  - WAL 模式启用（单写多读，支撑 SSE 读路径与控制循环并发）；
- **【契约】关键约束与索引**（v1.1 修订，见 §10.1 与 ADR-2）：

  ```sql
  -- 代身份：每机每代一行；digest 只做普通索引（内容可跨代复用）
  CREATE UNIQUE INDEX ux_desired_snapshots_machine_gen ON desired_snapshots(machine_id, generation);
  CREATE INDEX ix_desired_snapshots_machine_digest ON desired_snapshots(machine_id, digest);

  -- 机器级互斥（§4.3）
  CREATE UNIQUE INDEX ux_operations_machine_unresolved
    ON operations(machine_id)
    WHERE phase IN ('Pending','Running','CancelRequested','Unknown');
  ```

- **【澄清】写竞争**：SQLite 的写是单写，WAL 只放开读并发。因此长事务（尤其全量 inventory 落库）必须避免；`busy_timeout` 必须显式设置；`SQLITE_BUSY`/重试次数须计入指标（§12）。

---


## 5. 节点侧设计（agentd）

### 5.1 执行模式

```text
agent-fleet-agentd daemon                    # 常驻：连接、心跳、操作执行、上报
agent-fleet-agentd oneshot inventory         # 输出观测状态 JSON
agent-fleet-agentd oneshot plan --bundle P   # 对 bundle 内快照计算变更计划
agent-fleet-agentd oneshot apply --bundle P  # 备份→应用→验证，输出 OperationResult JSON
agent-fleet-agentd oneshot cancel            # 【新增】对本地 in-flight 操作发出取消请求（供 SSH 路径使用）
agent-fleet-agentd doctor                    # 本地诊断（平台/服务/权限/连通性）
agent-fleet-agentd version
```

`daemon` 与 `oneshot` 链接同一组 `agentlocal/*` 包（reconciler/inventory/backup/installer/adapter）——这是 AD-5 的物理保证（护栏 #1）。

**【新增】`oneshot cancel` 的存在理由**：`CancelOperation` 是服务端→节点的 gRPC 消息，而 SSH-only 机器没有 gRPC 通道。当操作者要取消一台 SSH-only 机器上的在途 oneshot 时，服务端只能经 SSH 唤起一个本地取消。该子命令读取本地执行权锁中记录的属主信息（PID/启动时间），校验其确为本进程族的变更进程后投递取消信号。（MVP 也可先不提供该子命令，此时 SSH 路径的取消只能在下一个阶段边界由节点自行观察到响应文件后退出；但**不得**因此让服务端单方面把操作置为 `Cancelled`——FR-13.9。）

### 5.2 daemon 运行时

- 启动：读取 `~/.config/agent-fleet/agentd.yaml`（控制面地址、CA 证书路径、间隔配置）；
- 连接：出站 mTLS gRPC `Connect` 双向流；指数退避 + 抖动重连；每次（重）连接先 `Hello`（身份、版本、支持的 adapters/schema 版本）+ 全量 `ObservedState`（§11.5、§31）；
- **【契约】重连后立即重发 outbox**：在发出 Hello 与全量观测**之后、接收任何新操作之前**，按顺序重发本机 outbox 中尚未被服务端确认的已终结操作结果（FR-13.7）。顺序要求是为了让服务端先看到"上一操作的真实终态"，再看到新派发；
- 周期任务：心跳 15s；全量 inventory 于连接建立时、每次操作后、每 5 分钟；本地 inventory 发现受管摘要变化 → 立即上报 drift（§10.4，全部可配置）；
- **【契约】变更执行一律入单 worker 队列**：daemon 内所有变更（`ExecuteOperation`、自动 apply 的第一阶段之内的写操作等）进入**同一个**串行队列，同一时刻只有一条流水线运行（FR-9.9 的进程内一半）；
- 操作执行：收到 `ExecuteOperation` → **先查 outbox 中是否已有该 operationId 的终态**（有则直接重发，不执行）→ **若在执行中则忽略并回报已知进度**（FR-13.8）→ 否则 `OperationStarted` → 取得本地执行权（§5.6）→ 按共享 reconciler 执行（含备份/恢复）→ `OperationProgress` → `OperationResult` 落 outbox → 释放执行权；
- 工件获取：`FetchArtifact(digest)` 认证流式下载 → 本地 SHA-256 校验 → 路径边界校验 → 进入规范缓存（§17.2、FR-6.7）；
- 证书续期：到期前经既有通道调 `RenewCertificate`（§12.2），窗口带随机抖动；
- 本地状态：`~/.local/share/agent-fleet/state/last-observed.json` 记录最近观测摘要与 `inventorySeq`，用于加速 drift 判定与断线恢复。**【澄清】该文件用于加速判定，不是权威证据**：它不能替代服务端要求的那一份"与 operationId 绑定"的观测。

**【新增】结果 outbox（FR-13.7）**

- 位置：`~/.local/share/agent-fleet/state/outbox/`，每操作一个 JSON 文件（原子写），加一个单调递增的序号索引；
- 内容：`operationId`、`desiredGeneration`、`phase`（含终态修饰）、`startedAt/finishedAt`、`steps[]`、`verify` 证据（`observedProjectionDigest`、`desiredProjectionDigest`、`canonicalizationVersion`、`inventorySeq`）、脱敏后的诊断片段；
- 容量：默认保留最近 50 条（可配置），超出后删最旧的**已被服务端确认**项；未被确认的项不得删除（宁占空间不丢事实）；
- 确认机制：服务端在收到结果并幂等处理后回 `OperationResultAck{operationId}`；节点收到 ack 才可淘汰该项；
- **【契约】outbox 是 `Unknown` 收敛的唯一正常路径**：服务端不得用其他推断方式终结 Unknown（FR-15.4）。

### 5.3 本地 reconciler（共享核心）

13 阶段流水线（§14.1）：

```text
 1 validate desired state      # schema/版本协商校验，失败→DesiredStateInvalid
 2 inventory current state     # 调用适配器采集观测；记录 inventorySeq 与基线摘要
 3 calculate plan              # desired vs observed → []Change；产出 planDigest
                               #   空计划：仍必须走阶段 10（健康检查）与 12（验证），不得直接跳过
 4 backup managed files /      # backups/<operation-id>/；记录备份基线与文件级内容摘要
   Skill links / version metadata
 5 apply Agent versions        # installer（argv 直执行）+ 版本验证
 6 apply normalized config     # 适配器合并写（原子写）
 7 materialize Skills          # 规范缓存 + symlink/copy（含路径边界校验）
 8 apply MCP                   # 适配器合并写
 9 apply rules                 # 标记块/whole-file
10 run adapter health checks
11 inventory again
12 verify desired == observed  # 投影摘要比对（同一 canonicalizationVersion）；不等→VerifyFailed
13 commit operation success
```

**【契约】阶段 3 的空计划语义（v1.1 修订）**：v1.0 写"空计划→直接跳到 12"。这会让"机器看起来已经一致"跳过必要的健康验证。v1.1 规定：空计划跳过阶段 5–9（无变更可做），但**必须**执行阶段 10（适配器健康检查）与阶段 12（验证）。否则"操作成功 + 健康"这一门禁条件会建立在没有健康证据的操作上。

**【契约】阶段 12 的错误码（小项 a）**：v1.0 复用 `HealthCheckFailed` 表达"验证 desired==observed 不相等"，语义错位——健康检查是适配器的独立检查，验证不等是另一回事。v1.1 新增 `VerifyFailed`（§30.3），并保留 `HealthCheckFailed` 给阶段 10。

失败行为（§14.2）：**可变步骤 = 阶段 5–12**（一切可能改变节点状态、或据其判定操作成功的步骤）。任一可变步骤失败——含阶段 10 健康检查失败（`HealthCheckFailed`）与**阶段 11/12 失败（`VerifyFailed`）**——→ 停止后续 → 保留诊断 → 从本次操作备份自动恢复（恢复路径含外部编辑冲突检测，见下）→ 恢复后再 inventory → 上报「原始失败 + 恢复结果」；恢复亦失败 → 置 `Degraded=True`，Deployment 控制器跳过该机。

**【契约】apply 后验证失败（阶段 12）不是"部分成功"**：apply 已发生而验证不等，说明变更未按预期生效——按与可变步骤失败相同的路径处理（恢复 + 如实上报 `VerifyFailed` 与恢复结果），不得在验证失败时报告操作成功，也不得跳过恢复把机器留在中间态（对应验收 J 与护栏 #10 的反面路径；断言由 §13.1 故障注入层与 T7 承载）。

**失败与取消的边界**：取消是外部请求，为避免制造半状态在阶段 11–13 不中断（§9.7）；失败是内部事实，阶段 11–12 失败仍触发恢复。二者不矛盾。

幂等（§14.3）：第二次执行同一快照零实质变更（由 plan 阶段空计划保证）。

**【契约】操作内恢复与外部编辑冲突（A5 / M3 / I-10）**

v1.0（与 KM-12 报告 A5）建议把"操作窗口内丢失用户未托管编辑"写成**已接受限制**。本版**不采纳该豁免**——它直接违反 I-6 与 spec §3.5/护栏 #3 的意图（回滚同样是 Fleet 对用户文件的变更）。v1.1 规定：

1. **备份粒度记录**：阶段 4 备份时，除整文件副本外，记录**受管字段的键集与值**、以及每个备份文件的**全文件内容摘要**；
2. **恢复时先检测外部编辑**：恢复前重新读取当前文件，比较"当前全文件摘要"与"备份时记录的全文件摘要"；
3. **冲突处理**：
   - 若当前文件与备份时逐字节相同 → 无外部编辑，按整文件还原（与操作前完全一致）；
   - 若不同 → **存在外部编辑**。此时不得整文件还原。执行**受管字段级回退**：只把受管字段恢复为备份中的值，保留所有未托管字段的当前值（含操作窗口内新增的编辑）；写后重新解析验证；
   - 若受管字段级回退也不可行（如文件已不可解析）→ **显式失败**：操作终态 `Failed(RestoreConflict)`，机器置 `Degraded=True`，并在 UI 明确列出冲突文件与差异。**绝不静默覆盖**；
4. **不可恢复错误**：备份不可读/损坏、受管字段回退后仍无法解析、或回退反而扩大冲突范围，均属不可恢复错误，一律走上述显式失败路径，不得尝试"再猜一次"。

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

**【契约】适配器在 v1.1 多出的两项义务**（由 ADR-1 决定，见 §14）：

1. **`Inventory` 必须同时产出期望侧投影与观测侧投影**：给定随操作下发的期望状态，适配器负责
   - 把归一化期望映射为该适配器受管字段集上的投影，并算出 `desiredProjectionDigest`；
   - 从本机原生配置中提取受管字段投影，并算出 `observedProjectionDigest`；
   - 二者使用**同一个** `canonicalizationVersion`，并在 `AgentObservedState` 中一并返回；
2. **投影必须可复现**：同一份 `canonicalizationVersion` + 同一输入，必须产出同一摘要（由 fixture 测试锁定，§13.1）。

新家族（Claude Code、Gemini CLI…）接入 = 新增 `adapter/<family>` 包并注册：实现上述接口 + 提供 fixture home 测试，不改控制面核心（§1 的扩展性要求由此满足）。

### 5.5 配置合并与所有权（§16）

- 结构化文件（TOML/YAML/JSON）：解析 → 仅补写适配器自有键 → 保留未知键 → 临时文件 + fsync + rename 原子写 → 写后解析验证 → 尽量保留原权限；
- 文本 rules：`# BEGIN/END agent-fleet managed` 标记块替换；不可安全部分拥有的文件必须声明 `ownership=whole-file` 且 UI 可见（§16.2）；
- 投影哈希：只对受管字段投影计算观测摘要，绝不对整文件哈希（§10.2）——这同时是"未托管字段变更不触发 drift"（§10.3）的实现基础；
- **【澄清】合并写是"未托管字段保留"的机制基础，因此面向 generation 的回滚必须复用合并写（§7.2），而不是整文件还原**；
- **【新增】写后验证必须包含**：可解析 + 受管键已到位 + 未托管键总数不减少（计数检查用于尽早发现合并逻辑缺陷）。

### 5.6 节点本地执行权【新增：本节是 v1.1 针对 A1/A2 的核心新增】

**问题**：v1.0 假设"控制面 Machine 级互斥"足以防止同机并发变更。该假设在两个场景下不成立：

1. 服务端重启（A2）：内存互斥消失，而节点上的旧流水线还在写文件；操作者此时经 SSH 触发 oneshot，两条流水线并发；
2. 操作者手工执行 `agentd oneshot apply`（运行假设 A5）：该进程完全不经过控制面，控制面互斥对它没有任何约束。

**契约**：节点本地以文件锁表达执行权，daemon 与 oneshot **共享同一把锁**。

```text
锁文件：~/.local/share/agent-fleet/state/execution.lock
取得方式：O_CREAT|O_EXCL 原子创建，内容为 JSON：
  { ownerPid, ownerStartTime, channel, operationId, acquiredAt, hostBootId }
```

规则：

1. **取得时机**：任何变更流水线在阶段 1 之前必须取得执行权；未取得则不得开始任何阶段（护栏 #16）；
2. **释放时机**：流水线终结（成功/失败/取消）后，且阶段 12/13 的结果已写入 outbox 之后释放。**释放的依据是"进程已完成并记录了结果"，不是"操作者点了某个按钮"**；
3. **daemon 的行为**：取得失败时**排队**（自身单 worker 队列已保证进程内串行，此处排队主要覆盖锁被外部 oneshot 持有），排队的操作在 UI 上呈现为 `Pending(BlockedByNodeLock)`；
4. **oneshot 的行为**：取得失败时**立即以退出码/错误码 `NodeBusy` 失败**，不排队、不等待。理由：oneshot 是同步的 SSH 调用，长阻塞会同时占用全局并发槽位与 SSH commandTimeout；
5. **陈旧锁的判定**：锁文件中的 `ownerPid` 不再存在、且 `hostBootId` 与当前一致时，判定为陈旧锁。**但陈旧锁不等于可以立即夺取**：
   - 若锁同时记录"本次操作的 staging 已清理、无暂存产物"→ 可安全夺锁，记录一条告警事件；
   - 若存在未清理的暂存产物或无法确认节点上是否仍有该操作写入的残留 → **不得自动夺锁**，报 `StaleExecutionLock`，要求操作者显式处理（提供 `agentd doctor --recover-lock` 引导界面并说明风险）；
6. **与退出的关系**：进程被 `SIGKILL` 不会释放文件锁内容（文件仍在），这正是第 5 条存在的原因；不得用"文件存在即视为持有中"的简化判断；
7. **不得用人工标记替代**：控制面把操作标为终态、UI 上点"已解决"，都不构成释放执行权的依据——节点上的实际执行权只能由持锁进程的退出或显式夺锁流程改变（对应汇总裁决"人工终态不能代替确认节点停止"）。

**验收场景**（§13.1 对应项）：在同一临时 HOME 下并发启动 `daemon` 与 `oneshot apply`，断言恰好一条流水线运行、另一条按渠道语义排队或失败；注入 `SIGKILL` 后再启动，断言陈旧锁被正确判定且不静默夺锁。

---

## 6. 领域模型设计

### 6.1 资源总览

六类资源，统一 K8s 风格 `metadata/spec/status`（AD-7），JSON over REST，名称在类型内唯一：

| 资源 | 生命周期 | 关键 spec 字段 | 关键 status 字段 |
|---|---|---|---|
| Machine | 操作者 CRUD + 控制器写 status | managementMode（agentd\|ssh）、ssh（hostAlias 或显式连接字段）、profileRef、overrides | observedGeneration、os/arch/hostname/homeDir、agentdVersion、desiredDigest/observedDigest、**desiredProjectionDigest/observedProjectionDigest【新增】**、lastProbeAt/lastInventoryAt、**inventorySeq【新增】**、**unresolvedOperation【新增】**、conditions×6 |
| AgentProfile | 操作者 CRUD | agents（codex/omp/opencode：enabled/version/installer/config）、skills[]（skillRef）、mcp{}、rules | —（无控制器） |
| ModelProvider | 操作者 CRUD | type（openai-compatible）、endpoint、apiKeyEnv | —（解析结果进入快照） |
| Skill | 操作者 CRUD + resolve 控制器写 status | source（git: repo/ref/path 或 local: path） | resolvedRevision、contentDigest、resolvedAt |
| Deployment | 操作者创建 + 控制器写 status | selector.machineNames、targetGeneration、strategy（canary/batchSize/maxUnavailable/pauseOnFailure） | phase、succeeded/failed/pending/**superseded/skipped【新增】** |
| Operation | 系统创建（不可变） | machine、type（Reconcile/Repair/Bootstrap/Rollback/**AutoPlan【新增】**）、transport（agentd\|ssh）、desiredGeneration、**readOnly【新增】**、**planDigest【新增】** | phase、startedAt/finishedAt、steps[]（name/phase）、**terminalModifier（Superseded/Cancelled/Skipped）【新增】**、**verify 证据【新增】** |

### 6.2 Machine 与 conditions

Machine 状态条件（§22，六条件全为必填维护项）。**v1.1 修订：status 允许三态，SSH-only 与无新鲜观测的机器必须显式 Unknown。**

| Condition | 置位方 | True 含义 | False 含义与典型 reason | **Unknown 的含义（v1.1）** |
|---|---|---|---|---|
| SSHReachable | Machine 控制器 | 最近一次 SSH 探测成功 | 探测失败，reason ∈ §30.1 六类 | 从未探测过 |
| AgentConnected | Machine 控制器（由 gRPC 连接注册表驱动） | daemon 流活跃 | AgentDisconnected（心跳超 45s） | 该机为 `managementMode=ssh`，从不建立 daemon 流（不使用 Unknown 表达"离线"——那是 False） |
| InventoryReady | Machine 控制器 | 已收到至少一份有效 inventory | 已尝试采集但失败（InventoryFailed） | 从未采集过 |
| Drifted | Reconcile 控制器 | observedProjectionDigest ≠ desiredProjectionDigest（同一规范化版本） | 受管状态一致（**且证据新鲜、对应当前代**） | **`NeverInventoried` / `StaleObservation` / `ObservationPredatesDesired`** |
| Reconciled | Reconcile 控制器 | 一次针对**当前代**的操作成功且其 apply 后观测证明一致 | 最近一次针对当前代的操作未收敛 | 同上三态原因 |
| Degraded | Reconcile 控制器（恢复失败/恢复冲突时） | 处于不健康/不确定状态 | 自动 rollout 跳过该机 | — |

规则重申（§22）：每个 condition 记录含 status/reason/message/lastTransitionTime；**禁止从另一条件推导**（例如不得由 `AgentConnected=False` 推导 `Degraded=True`），一切由控制器显式决策写入。

**【契约】新鲜度阈值**：`driftFreshnessWindow`（默认 3 × inventory 间隔 = 15 分钟，可配置）。超出该窗口且当前无未决操作时，`Drifted`/`Reconciled` 置 `Unknown(StaleObservation)`。这与 spec §35.2 不冲突：spec §8.2 的示例只用 True/False，未禁止第三态，且 spec §35.2 本身已使用 `Unknown` 作为操作状态先例（M5）。

**【契约】SSH-only 机器的语义**：spec §5.2 明确 MVP 不对 SSH-only 机器做后台轮询。因此这类机器在两次操作之间**必须**是 `Drifted=Unknown`，UI 显示"上次确认于 T"。把"从未检查"呈现为"已确认一致"是明确的实现缺陷。

### 6.3 DesiredStateSnapshot 与 generation 语义

```text
DesiredStateSnapshot {
  machine, generation, protocolVersion, schemaVersion,
  agents: { <family>: { version, config{model, provider{endpoint, apiKeyEnv}}} },
  skills: { <name>: { revision, digest, artifactDigest } },
  mcp: {...}, rules: {...},
  digest: "sha256:..."   # 规范化 JSON 的确定性 SHA-256（snapshotDigest）
}
```

- 输入五要素（§9）：AgentProfile、Skill 精确修订/digest、ModelProvider、Machine overrides、adapter schema 版本；
- **generation 仅在快照摘要变化时递增**：保存 profile 但有效结果不变 → 不新增代（这是 drift 判定与 Deployment targetGeneration 语义的基石）；
- 快照不可变、可重放：Deployment 回滚 = 将机器重指向某一具体 generation；
- 快照中不含秘密值，`apiKeyEnv` 只携带环境变量名。

**【契约】generation 身份与内容身份必须分离（FR-7.5、F6、ADR-2）**

v1.0 写 `desired_snapshots` 上"同 machine+digest 唯一"，同时又写"摘要相同则不产生新行"。这两条在 A→B→A 的回退序列下自相矛盾（F6 反例）：

```text
gen12 = 内容 A（行已存在，digest=A）
gen13 = 内容 B
profile 改回 A → 摘要相对上一代（B）已变化 → generation 必须递增到 14
             → 但 (machine, digestA) 行已存在，其 generation 列 = 12
→ machine.generation = 14，却没有任何 generation=14 的行
→ Deployment.targetGeneration = 14 无行可指；"回滚到 gen12 / gen13"语义含混
```

**v1.1 决策（单表方案，理由见 ADR-2）**：`desired_snapshots` **按 (machine_id, generation) 唯一**，`digest` 建**普通索引**（不唯一）：

- `generation` 是**身份**（"第几代期望"），`digest` 是**内容指纹**（"这一代的内容是不是和某一代相同"）；
- 同一 `(machine_id, digest)` 可以在多行出现（A→B→A 时 gen12 与 gen14 同 digest，两行内容相同）；
- "摘要与上一代相同则不递增 generation"的语义**不变**（FR-7.2）：只有"与紧邻的上一代"相同才不增代；与更早的某代相同仍须增代；
- 不强制引入 `snapshot_contents` 额外表：MVP 规模下（100 机 × 数千代 × KB 级 JSON）整块重复存储的总量可忽略，而单表让"回滚到第 N 代"是一次主键查找，没有跨表一致性问题、没有内容引用计数、也没有"内容行被回收而代行悬空"的失败模式。
- 若未来存储成为问题，可无损迁移到双表（内容表 + 代表），迁移路径记录于 §16。

### 6.4 Operation 与错误模型

Operation（§8.7）为不可变审计单元：

```yaml
kind: Operation
spec: { machine, type, transport, desiredGeneration, readOnly, planDigest? }
status:
  phase: Pending | Running | AwaitingConfirmation | CancelRequested | Succeeded | Failed | Unknown
  terminalModifier: Superseded | Cancelled | Skipped     # 仅终态，可空
  startedAt / finishedAt
  verify:                                               # 门禁与审计的证据（绑定 operationId）
    desiredProjectionDigest / observedProjectionDigest
    canonicalizationVersion
    inventorySeq
    adapterHealth: passed | failed | skipped
  steps: [ {name: backup|codex-version|codex-config|skills|mcp|rules|verify, phase} ]
```

- 输出脱敏：环境值、敏感环境变量名列表命中的内容、SSH 私钥路径内容一律不得进入 step 输出（§29.10、§34.L）；
- 错误码四组（§30，**v1.1 新增第四组**）：SSH 六类（DNSResolveFailed、HostKeyVerificationFailed、AuthenticationFailed、ConnectionTimeout、RemoteCommandFailed、UnsupportedPlatform）/ Agent **六类**（AgentDisconnected、ProtocolVersionMismatch、EnrollmentRejected、CertificateExpired、InventoryFailed，+ **v1.1 增补 `ProjectionVersionMismatch`**，见 §7.1）/ Reconcile **十四类**（v1.0 九类 DesiredStateInvalid、InstallerFailed、VersionVerificationFailed、ConfigParseFailed、ConfigWriteFailed、SkillDownloadFailed、SkillDigestMismatch、HealthCheckFailed、RollbackFailed，+ **v1.1 增补** `ReplanRequired`、`VerifyFailed`、`RestoreConflict`、`ArtifactTooLarge`、`SkillPathRejected`）/ 操作协调**四类**（`MachineBusy`、`NodeBusy`、`StaleExecutionLock`，+ **v1.1 增补 `BundleTooLarge`**，见 §7.4）；
- 用户可见错误五要素：reason code、人读消息、operation ID、时间戳、安全诊断（§30.3）；
- **【契约】`Unknown` 不是终态**（FR-15.4）：它占用机器级互斥、阻塞自动 rollout、阻止新变更；只有 outbox 重报、节点确认的取消、或操作者显式跳过才能使其离开。

**【契约】pending 阶段与执行权的关系**：`AwaitingConfirmation` 表示 plan 已产出、等待确认，此时节点**不持有执行权**（已释放），控制面互斥**仍持有**。这是两条独立机制的自然分工：执行权管"文件正在被写"，互斥管"这台机器不要开新操作"。

---

## 7. 关键机制设计

### 7.1 受管投影与 drift 检测

```text
期望侧：snapshot.agents.codex.config.model = "gpt-5.6"           （归一化）
观测侧：adapter.Inventory() → managedConfig{"model": ..., "model_provider": ...}
比较：  normalize(观测受管投影) vs normalize(期望受管投影映射到该适配器受管 schema)
        → 两侧同 canonicalizationVersion
→ 不等 ⇒ Drifted=True，并产出逐字段 diff（UI Show Diff）
```

关键约束：

1. 投影范围由适配器声明，未托管字段既不比较也不备份范围之外的内容（§10.2、§16）；
2. 摘要对"归一化后的受管状态"计算，而非原始文件字节（否则任何无关编辑都会误报 drift）；
3. Skill 的 drift 信号 = 目标 revision/内容 digest（symlink 模式）或复制内容 digest（copy 模式，§17.3）；
4. 未托管本地配置变更**不得**触发 drift（§10.3）——此为验收 G 的隐含前提。

**【契约】三类摘要的职责边界（v1.1 核心修订，M1/M2）**

v1.0 在四处使用了三种语义不同的摘要，却当成一种：

| 摘要 | v1.0 出现位置 | v1.1 定义与唯一用途 |
|---|---|---|
| `snapshotDigest`（全量快照摘要） | §4.8 | **仅**用于内容寻址快照与判定"代是否变化"。**禁止**用于 drift 比较或门禁摘要比较 |
| `desiredProjectionDigest`（期望侧受管投影摘要） | v1.0 未定义（FR-10.3/§4.4 隐含使用） | drift 判据的左侧。由节点侧适配器基于随操作下发的期望快照计算（ADR-1） |
| `observedProjectionDigest`（观测侧受管投影摘要） | §5.5、§7.1 | drift 判据的右侧。由节点侧适配器从本机原生配置提取后计算 |

**权威判据只有一条**：`desiredProjectionDigest == observedProjectionDigest`（且二者 `canonicalizationVersion` 相同）。
逐字段 diff **只用于 UI 展示**，不得作为判据——v1.0 §4.2/§4.4 按摘要判定、§7.1 按逐字段判定、FR-8.3 又回到摘要式，三种表述在规范化或字段集不一致时结论会不同（M2）。

**已明确不存在的东西**（v1.0 未承诺，本版也不引入）：不存在"回滚到第 N 代"所需的第五种摘要；也不存在"节点上报全文件哈希"的路径——v1.0 已正确禁止整文件哈希（FR-8.2），本版保持。

**【契约】规范化版本必须影响可比性，不只是诊断信息（M1 修正 5）**

`canonicalizationVersion` 随每次投影摘要一起携带，并且：

1. **同一操作的比较双方必须版本一致**。若期望快照携带的版本与节点适配器产出的版本不一致 → **不产生 drift 判定**，而是报 `ProjectionVersionMismatch`（归入 Agent 错误组），把该机 `Drifted`/`Reconciled` 置 `Unknown`，并在 UI 提示需要 agentd 升级（与 §7.6 的版本协商一致）；
2. **跨版本重放不得静默沿用旧判定**。服务端存储 `observed_states` 时必须一并存版本；比较记录中版本不同即视为不可比，不得复用历史比较结论；
3. **快照身份必须与产出它的规则版本关联**：`desiredProjectionDigest` 不是快照的固有属性（它依赖于该代内容 + 规范化版本），因此快照落库时同时记录 `canonicalizationVersion`，便于事后审计"当时是用哪版规则比较的"。

**【契约】比较位置的选型（ADR-1）**：v1.1 采用**节点侧双侧投影**方案：节点在每次 inventory 时对**当前快照**同时计算期望侧与观测侧投影摘要并随 `ObservedState` 上报，服务端只存储与比较两个数、写条件。理由是控制面完全不需要投影 schema 与规范化实现（I-1 平凡成立、护栏 #2 无例外），且消除了"服务端按自己的规范化规则重算"与"节点按适配器规则计算"两条实现分叉的可能。完整 ADR（背景、候选、决策、代价、验证）见 §14。

### 7.2 备份与回滚

备份（§16.3）：每次变更前把受管工件复制到 `~/.local/share/agent-fleet/backups/<operation-id>/`，附恢复所需元数据（原文件路径、权限、内容哈希、**受管键集合与值**、Skill link 目标、先前版本号）；保留每机最近 10 次操作（可配置）。

**【契约】两条恢复路径必须分开，且都不得毁未托管字段（A5、I-10）**

| 场景 | 机制 | 失败语义 |
|---|---|---|
| reconcile 中途失败（操作内恢复） | 从本次操作备份恢复。**恢复前先做外部编辑冲突检测**（§5.3）：无冲突 → 整文件还原；有冲突 → 受管字段级回退并保留未托管编辑；两者都不可行 → `Failed(RestoreConflict)` + `Degraded=True` | 如实上报恢复结果，绝不静默覆盖 |
| 事后回滚（API/UI，**面向 generation**） | **对目标 generation 的快照重放一次 reconcile**：走适配器合并写（天然保留未托管字段）→ 阶段 11–12 验证 → 成功则机器指向该代 | 目标快照不可用 / 安装器不支持 → `RollbackUnsupported`（版本类）或 `RollbackFailed`（配置类），绝不虚报成功（验收 J） |
| 版本类回滚 | 用同一 installer 安装先前观测版本（§20.3） | 不可行 → `RollbackUnsupported` |

**v1.1 明确删除的表述**：v1.0 §7.2 的"事后回滚｜配置/文件类：恢复上一成功操作的备份"。该表述与 §6.3"回滚 = 将机器重指向前一快照 generation"并存且互相矛盾（一个说还原文件、一个说重指代），而还原备份会静默毁掉备份之后的用户未托管编辑（A5 反例：gen12 备份里没有用户 11:00 手工加的 `customSetting=bar`，14:00 回滚到 gen12 会把 `bar` 抹掉）。**备份还原的适用范围收缩为"仅操作内失败恢复"**。

回滚 API：`POST /machines/{id}/rollback`（机器级，参数为目标 generation）；`POST /deployments/{id}/rollback`（机群级，生成新 Deployment 指向旧 generation，§21.4）。

**【契约】回滚与 generations 的交互**：
- 回滚**产生新的 generation**（重放的是旧内容，但期望状态相对上一代发生了变化），而不是"把 machine.generation 改回旧值"——历史不可变（§21.4）；
- 因此回滚后机器实际指向的是一个**新代**，其内容等于目标代；`Reconciled` 对新代求值；
- 若要"回退"到内容 A，且 A 曾是 gen12，那么回滚到 gen12 会生成 gen15（内容 A）。UI 必须显示"回滚自 gen14 至 gen12 的内容（新代 gen15）"，避免操作者误以为代计数在回退。

### 7.3 Canary / 批量 Deployment

状态机与门禁见 4.4 节。补充设计细节：

- **批次并发**：同批内机器并发执行（上限受全局 ≤20 并发约束），批间严格串行等待门禁；
- **canary 选择**：按目标列表顺序取前 `canary` 台；操作者可在 UI 调整顺序（简单确定性优先于智能调度）；
- **不可用目标处理**：`AgentConnected=False` 且 SSH 不可达的目标保持 pending 并显示原因，不计入失败（§21.2 的"filter unavailable"）；
- **暂停语义**：`Paused` 冻结批次推进，已完成批次不回退；resume 从冻结点继续；
- **失败注入友好**：每台机器的批内结果独立记录（deployment_targets），验收 H 的"注入一台失败 → 暂停"由此可演示；
- **【新增】未决目标**：目标机有未决操作（含 `Unknown`）时该目标阻塞、批次不推进（FR-10.7）；操作者可在 UI 显式跳过（记 `Skipped` 并留审计），跳过不宣称节点已停止；
- **【新增】门禁证据来源**：门禁四条件全部取自 §4.4 的绑定证据（该 `operationId` 的结果与观测），**不得读取机器 status 的缓存摘要**。

### 7.4 SSH-only 操作 bundle

结构（§13.4）：

```text
<staging>/bundle/
  manifest.json                # DesiredStateSnapshot + 依赖工件 digest 清单 + bundle 自身 SHA-256
  artifacts/skills/<digest>/…  # 仅被引用的 Skill 工件
```

执行序：build（服务端，含摘要）→ 校验预算（§4.7）→ scp 上传至固定 staging → `oneshot plan --bundle`（先做路径/名称/digest 校验，再校验全部工件 digest，然后计算计划并记录基线）→ 操作者确认（或 Deployment 自动）→ `oneshot apply --bundle`（**重取观测、重算 plan、比对基线**）→ 返回 OperationResult JSON → 清理 staging 与临时二进制。agentd 缺失时先上传临时兼容二进制（按探测 GOOS/GOARCH 选择，§7.3，**须校验 digest**）。**该路径不需要节点能访问控制面网络**——Skill 工件随 bundle 走（§13.4 的存在理由）。

安全点：

1. bundle 上传至用户 home 下 staging 目录并按 0700 权限创建；
2. apply 校验 digest 失败 → `SkillDigestMismatch`，拒绝应用（§29.11）；
3. **【新增】名称与路径校验（A7）**：`manifest.json` 中每个条目的 `name` 必须匹配 `^[A-Za-z0-9._-]+$`、`digest` 必须匹配 `^sha256:[0-9a-f]{64}$`；解包路径归一化后必须仍在 `artifacts/skills/` 根内，拒绝绝对路径与 `..` 元素；违规即 `SkillPathRejected` 并拒绝整包（不做部分应用）；
4. **【新增】内容内符号链接**：默认拒绝（§4.7 第 3 条）；
5. **【新增】bundle 自身摘要**：解包前校验 manifest 声明的 bundle 摘要与实收内容一致，防止传输层之外的替换；
6. **【新增】预算**：超过 §4.7 的 bundle 预算时不构建、不派发，报 `BundleTooLarge`。

### 7.5 Enrollment 与证书生命周期

时序（§12.1 十步，**v1.1 按 A8 修订**）：

```text
Server: 建 Machine → SSH 探测（平台识别）
Server: 签发一次性 token（绑定 machineId + CSR 公钥指纹，短时效，熵≥128bit）
Server→Node(SSH): 上传 agentd 二进制（附 digest）+ agentd.yaml（含 advertiseURL、CA 证书）
Node: 校验 agentd 二进制 digest → agentd 生成本地私钥（0600）
Node→Server(TLS + 服务端证书校验): Enroll{machineId, token, CSR}
Server: 【单事务】校验 token（存在/未过期/未用/machineId 匹配/CSR 指纹匹配）→ CA 签发 client.crt → token 作废
Server: 并发争抢时数据库层序列化，至多一个成功
Node: client.crt/client.key/ca.crt 落盘 0600
Node→Server(mTLS): Connect 流建立 → Hello + 全量 ObservedState + outbox 重发
Server: 同 machineId 已有活跃流 → 拒绝新流 + 告警（除非旧流已被判定断开）
```

证书生命周期（§12.2）：有限期客户端证书；到期前经已认证通道续期（`RenewCertificate`，**窗口加随机抖动**）；CA 私钥**由产品全程留在控制面主机**（0600），产品不将其外发、不写入日志/工件/快照，也不提供导出端点。续期失败/过期 → `CertificateExpired` 错误，UI 提示重新 bootstrap（经 SSH）。

**【v1.1.1 补充】"留在控制面主机"的唯一例外**：操作者本人为灾难恢复执行的**人工加密备份**（§4.6）会把 CA 材料复制到控制面主机之外。该动作由操作者发起、在操作者控制的加密介质上完成，**不经过产品的 API、CLI 导出、日志或任务系统**；它不改变"产品不导出 CA"这一范围边界，只是承认"操作者可以手工复制自己主机上的文件"。因此上一段的表述应读作**产品行为约束**，而不是"CA 材料在物理上永不可能出现在第二处"。

**【契约】A8 的三处修订理由**：

1. **明文 enroll 与 spec 冲突**：v1.0 的 RD-2 推荐"复用同一 gRPC 监听端口的 HTTP/2 明文路径"，而 spec §11 明写 "protobuf + bidirectional gRPC **over TLS**"、§29.5 要求 "Agentd transport uses mTLS"。明文路径下 `{machineId, token, CSR}` 全程可被同网段窃听，攻击者先到即赢得一次性 token 并获得该 machineId 的合法客户端证书，真实节点反而 enroll 失败（A8 攻击链）。agentd 在 bootstrap 时已通过 SSH 获得 CA 证书，**做服务端证书校验零额外成本**；
2. **token 必须原子消费并绑定 CSR**：非原子的"先查后写"在并发下可被抢注；不绑定 CSR 指纹则截获的 token 可配任意公钥；
3. **重复连接必须拒绝并告警**：若实现为"新连接顶替旧流"，攻击者持合法证书即可顶掉真实节点，且该安全事件在监控上表现为一次普通重连。

### 7.6 版本协商与兼容（§31）

- 快照与 gRPC 消息均携带 `protocolVersion` + `schemaVersion`；
- 服务端拒绝不支持的 daemon 主版本 → 该机条件 `AgentConnected` 保持 False、UI 呈现"需升级 agentd"，绝不向其下发期望状态；
- agentd 在 `Hello` 中上报支持的 adapters 与 schema 版本；服务端据此过滤可下发内容；
- 适配器 desired-state schema 独立版本化——新增字段向后兼容（加字段不改语义），破坏性变更升主版本并要求 agentd 升级；
- **【新增】`canonicalizationVersion` 是版本协商的一部分**：它是"drift 是否可比"的前置条件，不是可选诊断字段（§7.1）。服务端在 `Hello` 中一并记录该版本；不匹配时的行为见 §7.1 第 1 条。

### 7.7 OpenSSH include 导出（§13.5、§40）

- 渲染来源：仅"Fleet 持有显式 SSH 连接字段"的机器（HostName/User/Port/ProxyJump/IdentityFile）；经既有 `hostAlias` 导入的机器不重复导出（它们已在操作者配置里）；
- 产物：`~/.ssh/agent-fleet.conf`（生成路径 `generated/ssh/agent-fleet.conf`），用户主配置需 `Include ~/.ssh/agent-fleet.conf` 才生效——MVP 提供 preview、导出下载、**显式确认后**安装/更新 include 文件三种动作，绝不静默改写主配置（非目标 #14）；
- 动因：Codex Desktop Remote SSH 从 OpenSSH 配置发现主机——Fleet 以"配置文件"为集成面，不代理会话（AD-8、§40）。

### 7.8 automatic reconcile 的触发、策略与审计【新增：本节回应 A11】

spec §4.1 明确把 "Manual and automatic reconcile" 列为 in-scope，而 v1.0 的 FR 表没有任何条目承载它，§3.4 流 1 的"apply 按策略/操作者触发"里"策略"也从未定义。这造成两份实现依据相互矛盾（A11），并且如果实现者据此做了自动 apply、又没有触发策略定义，会直接放大 A1 的并发窗口。

**【契约】v1.1 保留 spec 的 automatic reconcile 要求，不删除、不延期**，并把它拆成两个明确分离的能力：

| 能力 | 默认 | 触发条件 | 是否写数据 | 是否有 operationId |
|---|---|---|---|---|
| **自动 plan 与上报**（FR-9.7） | **开启** | ① 收到 `DesiredStateChanged` 且机器在线；② 本地 inventory 检出 `observedProjectionDigest` 变化（drift 疑似）；③ 操作者手动请求 inventory 后 | 否（只读流水线：validate → inventory → plan） | 是（`type=AutoPlan`、`readOnly=true`） |
| **自动 apply**（FR-9.8） | **关闭** | 仅当操作者在该机器或全局显式开启，且满足漂移条件 | 是 | 是（由控制面下发的普通 Operation） |

**审计与可见性要求**：

1. 自动 plan 也产生 Operation 记录（`readOnly=true`），使"系统何时看过这台机器"可追溯；
2. 自动 apply 产生的 Operation 与手动 reconcile **完全同形**：同一互斥、同一节点执行权、同一门禁证据要求；不得有"快捷路径"；
3. UI 必须能区分自动与手动来源（`type` 字段），并展示自动 apply 的开关状态（Machine 级与全局级）；
4. **自动 apply 不得由节点自行发起**：节点只上报"检出 drift"，由控制面决定是否派发（否则又回到 A1 的并发窗口）。

**暂停与限流**：

1. 全局暂停开关（`automaticReconcile.paused`）：置位后不再产生任何自动 plan 或自动 apply，进行中的操作不受影响；
2. 每机暂停：Machine 级开关；
3. 限流：自动操作受全局 ≤20 并发约束，且**不与手动操作抢占**——为手动/Deployment 操作保留至少 1 个槽位（避免自动操作把 20 个槽位占满而使操作者的手动请求长时间排队）；
4. **降级机器**（`Degraded=True`）或**存在未决操作**的机器不参与自动 apply；
5. 连续失败熔断：同一机器连续 N 次（默认 3）自动 apply 失败后，自动 apply 对该机暂停并产生告警事件，需操作者显式恢复。

**串行化**：自动 apply 与手动 reconcile 走**同一条服务端互斥**与**同一节点单 worker 队列**，因此二者在节点上不会并发；若某机已有未决操作，自动 apply 的派发被同一唯一约束拒绝（视作"稍后重试"而非错误，记录为跳过并计入指标）。

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
POST /machines/{id}/operations/{opId}/cancel      # 【新增】取消未决操作
POST /machines/{id}/operations/{opId}/skip        # 【新增】显式跳过 Unknown（不宣称节点已停止）
GET  /machines/{id}/operations | /machines/{id}/drift
GET  /profiles/{id}/render?machine=<id>
POST /skills/{id}/resolve      GET /skills/{id}/revisions
POST /deployments/{id}/pause | /resume | /rollback
GET  /events        # SSE：Accept: text/event-stream
```

约定：

- 资源 JSON 形态 = `metadata/spec/status`；列表端点支持按 name/status 过滤（MVP 可最小化）；
- 动作端点返回 `Operation` 资源（202 语义），客户端凭 operation id 追踪进度（轮询或 SSE）；
- 错误体统一 `{reason, message, operationId?, timestamp, diagnostics?}`，reason 取 §30 四组错误码；
- 非回环部署时全部端点要求 `Authorization: Bearer <adminToken>`（§29.14）；
- SSE 事件格式：`event: <resource-type>`、`data: {id, revision}`——UI 据此选择性重取（§23.6）；
- **【新增】变更类动作在机器存在未决操作时返回 409 `MachineBusy`**，`diagnostics` 中带未决操作的 id 与 phase，供 UI 直接呈现处置入口（取消/跳过）；
- **【新增】SSH 路径的确认端点**：`plan` 产出的计划以 `planDigest` 标识，操作者确认的对象是它。确认动作沿用 `POST /machines/{id}/reconcile`（携带 `confirmPlanDigest`），以便服务端在确认时校验"确认的还是当初那一份计划"。

### 8.2 gRPC 协议（`api/proto/fleet/v1`，§11）

```proto
service FleetAgentService {
  rpc Connect(stream AgentToServer) returns (stream ServerToAgent);
  rpc FetchArtifact(ArtifactRequest) returns (stream ArtifactChunk);
}
service FleetEnrollmentService {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);          // TLS + 服务端证书校验（token 鉴权）
  rpc RenewCertificate(RenewCertificateRequest) returns (...); // 须既有 mTLS 通道
}
```

消息 union（**v1.1 增补**）：

| AgentToServer | ServerToAgent |
|---|---|
| Hello（身份/版本/adapters/schema 版本/**canonicalizationVersion**） | Welcome（连接确认、服务端配置回传） |
| Heartbeat | DesiredStateChanged{generation} |
| ObservedState（全量/增量标志、**inventorySeq**、**双侧投影摘要**、**operationId（如属某操作）**） | ExecuteOperation{operationId, **完整快照**、desiredGeneration} |
| OperationStarted / OperationProgress / OperationResult（**含 verify 证据**） | CancelOperation{operationId} |
| LogEvent（仅运维日志，禁会话内容） | RequestInventory |
| — | **OperationResultAck{operationId}【新增】**（outbox 淘汰依据） |

语义要点（**加粗为 v1.1 契约**）：

- 重连必以 Hello + 全量 ObservedState 开始；**紧随其后是 outbox 中的未确认结果重发**（FR-13.7）；
- 服务端幂等处理重复 OperationResult（按 operationId 去重，§11.5）；**Ack 之后节点方可淘汰 outbox 项**；
- **`ExecuteOperation` 一律携带完整快照**（FR-13.6）：v1.0 写"完整快照或摘要引用"，但协议中没有让节点取回快照的 RPC（`FetchArtifact` 只覆盖 Skill 工件），冷启动节点遇到引用形态必然失败且没有对应错误码。快照在目标规模下是 KB 量级，省这点流量不值得引入一条不存在的协议路径。摘要引用形态与配套 `GetSnapshot` RPC 一并留待 Post-MVP；
- **重复 `ExecuteOperation` 幂等**（FR-13.8）：执行中忽略并回报进度；已终结则从 outbox 重发终态（小项 c）；
- **`CancelOperation` 语义**（FR-13.9）：见 §9.7；
- 服务端对同一 `machineId` 的并发第二条流**拒绝并告警**（FR-11.6）。

### 8.3 主要页面与 API 对应（§24）

| 页面 | 数据来源 | 关键动作 |
|---|---|---|
| Overview | GET /machines + /deployments 聚合 | — |
| Machines | GET /machines（列：Name/OS-Arch/Profile/agentd/SSH/Drift/Reconciled/Last Seen） | Add/Probe/Bootstrap/Reconcile/Repair |
| Machine Detail | GET /machines/{id} + operations + drift | Bootstrap/Repair/Reconcile/Rollback、diff 查看、**取消/跳过未决操作【新增】** |
| Profiles | /profiles + /profiles/{id}/render | 编辑 + JSON/YAML 预览 + 消费机器 |
| Skills | /skills（Name/Source/Requested Ref/Resolved Revision/Digest/Machines） | Resolve/查看元数据/改 ref |
| Deployments | /deployments | Pause/Resume/Rollback、批次进度 |
| SSH Inventory | /machines（显式字段机器）→ 渲染 | 预览/导出/确认后安装 include |

无交互 shell、无会话代理（非目标 #2/#3）。

**【契约】UI 必须区分显示的三组状态（FR-14.5）**：

1. **drift 三态**：`已确认一致（上次确认于 T）` / `未知或过期（原因）` / `已漂移（附 diff）`。绝不允许把 Unknown 渲染成"一致"（M5）；
2. **未决操作**：显示是否存在未决操作、其 phase、以及它将阻塞哪些动作；提供取消/跳过入口及其后果说明（跳过**不**表示节点已停止）；
3. **Deployment 目标异常态**：`Superseded`（机器已在更新代）与 `Skipped`（操作者显式跳过）必须与 `Failed` 区分显示，且各自带原因。

---

## 9. 关键流程时序

### 9.1 Bootstrap（新机入队，daemon 模式）

```text
操作者: Web UI Add Machine(alias=gpu-home, mode=agentd, profile=default-dev)
Server: 建 Machine 记录 → ssh -G 解析 → BatchMode 探测（OS/arch/home/shell/svc/agents）
Server: 生成一次性 token（TTL 10m，绑定 machineId + CSR 公钥指纹）
Server→Node(SSH): scp agentd 二进制（附 digest）+ agentd.yaml（含 advertiseURL、CA 证书）
Server→Node(SSH): 校验并安装用户服务（launchd/systemd user，受支持时）并启动
Node: 校验二进制 digest → agentd 启动 → 生成私钥 → Enroll（TLS + 服务端证书校验）{machineId, token, CSR}
Server: 【单事务】校验（含 CSR 指纹）→ CA 签发 client.crt → token 作废
Node: 证书落盘 0600 → 出站 mTLS Connect → Hello + 全量 ObservedState + outbox 重发
Server: AgentConnected=True, InventoryReady=True → SSE → UI 状态更新
```

### 9.2 daemon 模式 drift 与 reconcile

```text
[背景] 操作者手工改了受管 codex.model
Node: 周期/触发 inventory → observedProjectionDigest 变化 → 立即上报 ObservedState（带 inventorySeq）
Server: 对当前代求值 → 摘要不等 → Drifted=True → SSE → UI 显示 drift + Show Diff
操作者: 点击 Reconcile → POST /machines/{id}/reconcile
Server: 互斥检查（无未决操作）→ 写 operation(Pending) → Connect 流推 ExecuteOperation{opId, 完整快照}
Node: 单 worker 队列 → 取得执行权 → 13 阶段流水线（含备份）→ OperationStarted/Progress/Result（落 outbox）
Server: 幂等接收结果 → 按结果中的 verify 证据对当前代求值 → Drifted=False, Reconciled=True → 回 Ack → SSE
```

### 9.3 SSH-only reconcile

见 3.4 流 2。补充（**v1.1 契约**）：

- plan 输出在人发起的路径上回显给操作者确认，确认对象是**具体计划**（`planDigest`）；
- Deployment 驱动路径按策略自动确认，但仍受 §4.4 的门禁证据要求；
- `maxUnavailable` 与并发上限同样适用；
- 临时二进制上传发生在"agentd 不存在或版本不兼容"时（§13.4），并须校验 digest；
- **apply 必须重算 plan 并比对基线**：plan 与 apply 之间本地受管文件被用户或其他进程修改时，`apply` 拒绝执行、零变更、报 `ReplanRequired`，要求重新 plan 与重新确认（FR-12.7、A6）。
  v1.0 把 `oneshot apply` 描述为"备份→应用→验证"，未规定它是否重算计划：若直接执行 plan 阶段的计划，会把基于旧观测的计划套在已变化的现状上；若内部重算，则操作者确认的计划与实际执行的计划不是同一个——"确认"失去意义。v1.1 选择**重算 + 基线比对 + 拒绝**，使确认语义重新成立。
- 未确认的计划超时后失效（默认 30 分钟，可配置），失效后需重新 plan（避免操作者隔夜点击确认一份早已过期的计划）。

### 9.4 Repair agentd

```text
触发: AgentConnected=False 持续 + SSHReachable=True（或操作者主动探测后触发）
Server: SSH 诊断（doctor / 服务状态 / 日志尾部，限额）
分支: 二进制缺失/损坏 → 重传兼容工件（附 digest）；服务未启 → 启动；版本不兼容 → 替换
Server: 重装/重启用户服务 → 等待出站流重连（超时上限可配置）
结果: 重连成功 → AgentConnected=True；失败 → Operation.Failed(reason)
```

**【新增】repair 与执行权的关系**：repair 可能重启 daemon 进程。若此时该机存在未决操作，repair 必须先经操作者确认"这可能中断正在执行的操作"，并记录在审计中（repair 不自动夺锁、不自动删除 staging）。

### 9.5 Canary Deployment

见 3.4 流 3；批内机器结果独立记录于 deployment_targets；门禁四条件评估所需数据全部来自**与本次操作绑定**的 apply 后 inventory 与适配器健康检查（§4.4、§21.3）。

### 9.6 服务重启恢复（§35.2）【v1.1 重写】

```text
Server 重启: SQLite 恢复全部资源状态，持久化互斥随库恢复
  - daemon 节点: agentd 指数退避重连 → Hello + 全量快照 + outbox 重发 → 状态自愈
  - in-flight 操作: 置 Unknown。Unknown 不是终态：
      · 占用该机互斥（唯一索引仍命中），阻止新变更与自动 rollout
      · 在 Deployment 中阻塞其目标（FR-10.7）
      · 收敛路径见下
  - Deployment: 从持久化批进度继续（已 Unknown 的机器阻塞等待）
```

**Unknown 的三条收敛路径【新增】**：

| 路径 | 触发 | 结果 | 前提 |
|---|---|---|---|
| A. 节点重报 | agentd 重连 → outbox 重发该 operationId 的终态 | 按重报的真实终态终结操作，释放互斥 | 节点能重连 |
| B. 显式取消 | 操作者 POST cancel | `CancelOperation` 送达且节点确认停止 → `Failed(Cancelled)`，释放互斥 | 节点可达（daemon 通道或 SSH `oneshot cancel`） |
| C. 显式跳过 | 操作者 POST skip | `Superseded(Skipped)`，释放控制面互斥，**但保留"节点可能仍在写"的提示与 Degraded/Unknown 标记** | 无（但风险由操作者承担，UI 必须写明） |

**明确禁止的收敛方式**：以"重新 inventory 与当前期望匹配"把 Unknown 推断为成功。inventory 只能证明**采集时**的状态，不能证明历史步骤和健康门禁曾被执行、也不能排除当时触发过恢复路径。把它记为成功等于**虚造审计事实**（FR-15.4、护栏 #13）。

### 9.7 取消与迟到操作【新增】

**取消（FR-13.9、A3 的 (d) 项）**：

```text
操作者/系统发起取消
→ 服务端把操作置 CancelRequested（仍属未决集合，互斥不释放）
→ 节点在阶段边界检查取消标志：
   · 阶段 1-4 之间 → 直接终止，终态 Failed(Cancelled)，无需恢复（尚无变更）
   · 阶段 5-10 之间 → 停止后续 → 走标准备份恢复路径（含外部编辑冲突检测）→ 终态 Failed(Cancelled)
   · 阶段 11-13 之间 → 视为不可中断，继续到终结（避免在验证/提交阶段制造半状态）
→ 节点回报 → 服务端终结操作 → 释放互斥
重复取消：幂等（CancelRequested 状态下再次取消返回 200 且不重复投递）
节点不可达：服务端不得单方面置 Cancelled，只能保持 CancelRequested 并在 UI 说明"等待节点确认"
```

**迟到操作（FR-9.10、A3 的 (a)(b)(c) 项）**：

```text
op1(gen12) 执行中，操作者编辑 profile → gen13 生成并推送
（profile 保存不占用机器级变更互斥，与 op1 并行是常态）
op1 完成: 节点按随操作下发的 gen12 快照 verify 通过，上报 Succeeded
Server: 发现 op1.desiredGeneration(12) < machine.generation(13)
  → 终态记 Succeeded(Superseded)：保留"该操作确实成功执行过"这一历史事实
  → 不写 Reconciled=True（那会把落后一代说成已收敛）
  → 不在操作结果上直接写 Drifted/Reconciled；触发"按当前代(13)重新求值"
  → 当前代(13)尚无对应观测 → Drifted=Unknown(ObservationPredatesDesired)
  → gen13 的派发逻辑决定是否发起后续操作（人工触发或 Deployment/自动 apply）
```

**条件抖动防护（FR-8.7）**：apply 后 inventory 尚在途时，一个排队的**操作前**周期 inventory 可能迟到到达，若允许其改写条件，Deployment 门禁恰好采样即误判（A3 反向时序、A4 叠加）。因此服务端维护每机已处理的 `inventorySeq` 高水位，低于高水位的报文只入历史表、不改条件。

---

## 10. 数据设计

### 10.1 表清单与要点（§25）【v1.1 修订加粗】

| 表 | 要点 |
|---|---|
| machines | spec/status JSON 列 + 查询列（name 唯一、managementMode、profileRef）；ssh 凭据只有引用（alias/路径），无私钥内容 |
| profiles / providers | spec JSON；providers 不含秘密值 |
| skills | source spec；status 指向最新解析 |
| skill_revisions | (skill_id, revision, digest, artifact_path, resolved_at)——解析历史不可变 |
| desired_snapshots | **(machine_id, generation) UNIQUE【修订】；digest 普通索引【修订】；snapshot JSON；canonicalizationVersion【新增】；created_at**。A→B→A 时同一 digest 可出现在多代（ADR-2） |
| observed_states | machine 最新 + 受保护引用集合（§10.4）；**含 inventorySeq、双侧投影摘要、canonicalizationVersion、关联 operationId【新增】**，滚动清理 |
| deployments / deployment_targets | 策略与批进度；targets 记录每机 phase/result/operation_id/**superseded/skipped 原因【新增】** |
| operations / operation_steps | 不可变审计；steps 含脱敏输出（限长）；**phase 部分唯一索引实现机器级互斥（§4.3）；含 readOnly/planDigest/terminalModifier/verify 证据【新增】** |
| events | SSE 重放/审计辅助（保留窗口可配置） |
| agent_certificates | machine ↔ 证书序列号/有效期（不含私钥）；**含删除时间与"到期前仍有效"审计事实【新增】** |

迁移：`migrations/NNNN_*.sql` 顺序应用，禁止修改已应用脚本（NFR-5）。

### 10.2 文件系统布局（§26）【v1.1 增补加粗】

控制面：

```text
~/.config/agent-fleet/config.yaml
~/.local/share/agent-fleet/
  fleet.db
  pki/{ca.crt, ca.key(0600), server.crt, server.key(0600)}
  artifacts/skills/<sha256>/…
  artifacts/agentd/<version>/<goos>/<goarch>/agent-fleet-agentd   # 【澄清】§7.3 的工件目录
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
  state/execution.lock            # 【新增】本地执行权（§5.6）
  state/outbox/                   # 【新增】未确认结果 outbox（§5.2）
  staging/bundle/                 # 【新增】固定 staging 路径（§4.5）
  staging/bin/                    # 【新增】临时 agentd 二进制
```

**【v1.1.1 补充】人工 CA 备份副本不在上述产品布局内【契约】**：人工备份是操作者对上述文件的**外部复制**，存放在操作者控制的加密介质/加密存储中（§4.6），产品**不定义**其路径、不感知其是否存在、也不提供内置导出端点。因此本节的布局图描述的是"产品运行所需的目录"，**不是"需要备份的内容清单"**——需要备份的是 §4.6 第 3 条列出的 CA/服务端证书材料、控制面配置、身份元数据（`fleet.db` 相关表）与（按恢复目标）工件。

### 10.3 数据保留

- 备份：每机 10 次操作（默认，可配置，§16.3）；
- observed_states：最新 + 保护引用集合（§10.4）；
- Skill 工件：按**保护引用集合**判定，未被引用且超出保留窗后可清理（MVP 可仅做手动清理）；
- events/operations：MVP 全量保留（单操作者规模下量可控），清理策略留待 Post-MVP；
- **snapshot（v1.1 明确）**：不承诺"永久保留所有历史快照"，但**必须保证"承诺回滚所需的那一代"可恢复**。具体保留契约见 §10.4。

### 10.4 保护引用集合与清理规则【新增：本节回应 M3 与汇总裁决第 7 条】

v1.0 只在 §10.3 提到"按 digest 引用计数清理 Skill 工件"，而对快照只字未提，且未说明"未被当前快照引用的工件可以清理"这一直觉规则在恢复语义下的漏洞。第二轮报告的清理建议（"节点清理未被当前快照引用的工件"）**只按当前引用判断**，会删掉回滚与恢复所需的材料（A5 相关的汇总裁决第 7 条）。

**【契约】任何清理（服务端工件缓存、节点规范缓存、staging、备份）都必须按保护引用集合判定**。保护集合 = 以下四类引用的并集：

| # | 保护对象 | 理由 |
|---|---|---|
| 1 | **当前期望快照**引用的全部工件 digest | 正在进行与即将进行的 reconcile 需要 |
| 2 | **未决操作**（含 Unknown）引用的快照与其全部工件 | 该操作可能仍在节点上执行，删除其输入会制造不可解释的失败（A10 汇总裁决） |
| 3 | **上一成功状态的快照与工件** | "回滚到上一次成功受管状态"（spec §34.J）的直接依赖 |
| 4 | **已承诺的回滚目标**：任何 Deployment/API 回滚请求所指向的 generation 及其工件 | 否则"回滚到第 N 代"会在若干代之后静默失效（M3） |

清理算法：

```text
候选集 = 全部工件/快照
保护集 = 上述四类引用的并集
可清理 = 候选集 - 保护集 - 仍在保留窗内的最近 K 代（默认 K=10，可配置）
```

**【契约】不可恢复错误**：若一个**承诺回滚目标**的快照或工件已因外部原因（手工删除、磁盘损坏）不可用，系统必须**显式拒绝**该回滚（`RollbackUnsupported`，附原因"目标 generation 内容不可用"），而不是尝试用一个近似的代替代。UI 上标明哪些回滚目标仍然可用。

**【澄清】observed_states 与保护引用**：spec §25 允许"仅保留最新 + 近期操作引用的状态"。本版明确"近期操作引用"必须至少覆盖未决操作与其 apply 后验证证据——门禁与审计都需要它（§4.4 反过来依赖这份证据）。

---

## 11. 安全设计（§29 十四条逐条落点）

| # | 要求 | 架构落点 |
|---|---|---|
| 1 | 不持久化 API key 值 | 领域模型仅 `apiKeyEnv` 名称（FR-3.1）；快照/DB/日志/操作记录全链路无值；存储层无秘密列 |
| 2 | 不持久化 SSH 私钥内容 | Machine.spec.ssh 仅存 alias/路径引用（FR-12.2）；SSH 执行交给系统 OpenSSH 与 ssh-agent |
| 3 | 用本地 OpenSSH 与既有凭据机制 | sshtransport 受限执行器（AD-4） |
| 4 | 不自动禁 host-key 校验 | 执行器禁止注入该选项；失败显式呈现 HostKeyVerificationFailed（FR-12.3） |
| 5 | agentd 传输 mTLS | gRPC over TLS 双向认证（FR-11）；**enroll 端点不是"明文的例外"，而是"TLS + token 鉴权 + 服务端证书校验"（FR-11.4 修订，A8）** |
| 6 | token 一次性且短时效 | enrollment 服务（FR-11.1）；**单事务原子消费 + 绑定 machineId 与 CSR 指纹 + 熵≥128bit（FR-11.5）** |
| 7 | CA/客户端私钥 0600 | 文件布局固定权限；写入时显式 chmod；服务端启动自检；**【v1.1.1】人工备份副本同样要求加密介质 + 权限受限 + 口令分开保管，且不得上传任务系统/日志/仓库（§4.6 第 5 条）** |
| 8 | 变更操作全部留痕 | Operation 不可变记录（FR-15.1）；**含 readOnly/planDigest/verify 证据/终态修饰（FR-15.5）** |
| 9 | installer argv 直执行 | installer 包 exec argv，无 shell；`${VERSION}` 逐参数替换（FR-2.2） |
| 10 | 日志限长 + 脱敏 | 统一日志中间件：输出限额 + 敏感环境变量名清单匹配脱敏；agentd LogEvent 同规 |
| 11 | Skill 工件节点侧校验 | gRPC 下载与 SSH bundle 两路径 apply 前强制 SHA-256 校验（FR-6.6）；**并叠加路径/名称边界校验与预算（FR-6.7/6.8）**；**经 SSH 上传的二进制同标准（FR-12.8）** |
| 12 | 配置原子写 | 临时文件 + fsync + rename + 写后解析验证（§16.1） |
| 13 | 变更前备份 | reconciler 阶段 4 强制（FR-9.6）；**恢复路径含外部编辑冲突检测（§5.3）** |
| 14 | 非回环暴露强制 admin token | HTTP 服务器启动校验：绑定非 loopback 且未配置 token → 拒绝启动（§28） |

威胁模型要点（MVP 单操作者）：主要威胁 = 凭据意外落盘/落日志、传输窃听与伪造节点、供应链（工件篡改）。不防御：恶意操作者本人、节点 root compromise 后的本地秘密（本就不经手秘密）、网络层 DoS。

**【新增】威胁模型的两处明确化**：

1. **伪造节点（A8 攻击链）**：enroll 走 TLS 后，窃听者无法截获 token 与 CSR；token 原子消费使并发抢注至多一个成功；重复连接拒绝 + 告警使"顶替真实节点"从静默事件变成可见事件。三处合起来关闭这条链；
2. **机器删除不是安全措施**：MVP 无吊销，被删除机器的证书在到期前仍可建立 Connect 流。因此**删除机器后不应假定该证书立即失效**；若有安全需求，正确做法是显式重新生成 CA 并重新 bootstrap 全机群（§4.6 的恢复契约），这是运维决策而非自动行为。

**【新增】enroll 端点的限速与审计（S3 升为强制）**：enroll 是系统唯一的未认证写入口。v1.0 把它列为"建议实现"（R6）。v1.1 升为强制：失败尝试限速（默认每源 IP 每分钟 ≤10 次，可配置）、每次失败产生审计事件、按 `machineId` 做相关性检测（连续失败触发告警）。理由：一个未认证入口的防护强度不应取决于"建议"。

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

**【新增】v1.1 追加的指标（NFR-7 修订）**：

```text
agent_fleet_machine_unresolved_operations        # 未决操作数（含 Unknown）——互斥阻塞的可观测面
agent_fleet_operation_unknown_age_seconds         # Unknown 持续时长（Unknown 不是终态，需要看它悬了多久）
agent_fleet_drift_unknown_machines{reason}        # 三态语义的可观测面（NeverInventoried/StaleObservation/ObservationPredatesDesired）
agent_fleet_node_busy_total{channel}              # 执行权冲突（NodeBusy）——节点侧并发压力的直接证据
agent_fleet_sqlite_busy_total                     # SQLITE_BUSY 次数（S6）
agent_fleet_sqlite_write_duration_seconds         # 写事务耗时直方图（S6）
agent_fleet_ssh_queue_depth                       # SSH 变更请求排队深度（S2 降级后的真实问题）
agent_fleet_artifact_rejected_total{reason}       # 工件被拒（SkillPathRejected/ArtifactTooLarge/BundleTooLarge）
agent_fleet_stale_execution_lock_total            # 陈旧执行权锁（§5.6）
```

### 12.1 容量估算与待验证假设（NFR-1/NFR-2）

**v1.0 的表述**"结论：单进程 + SQLite 在目标规模下无容量风险，与 AD-1/AD-2 自洽"**已被修订**。该结论只算了消息速率，漏掉了真正的串行点，且把"算术推演"表述成了"无风险"。

**保留的算术推演（复核无误）**：

| 项 | 计算 | 结果 |
|---|---|---|
| 心跳消息速率 | 100 台 / 15s | ≈ 6.7 msg/s（约 24k 条/小时） |
| 全量 inventory 频率 | 100 台 / 5 分钟 | ≈ 0.33 台/s |

**v1.0 漏掉的串行点【新增】**：

1. 全量 inventory 响应（每机每 5 分钟一次，负载可达数百 KB）与 SSE 读、控制循环写**共享同一个 SQLite 单写锁**排队；WAL 只放开读并发，**写仍然是单写**；
2. `events` / `operations` 表在全量保留策略下持续增长（§10.3 明确 MVP 全量保留），其索引维护进入同一写路径；
3. 条件更新、deployment_targets 进度更新、token 消费（事务）都在写路径上；
4. **未知量的真实分布**：单次 inventory 响应的实际大小、写入频率的突发性（重连风暴：控制面重启后 100 台同时重连并各发一份全量观测）——这些在文档阶段无法确定，必须实测。

**因此 v1.1 给出的是待验证假设与验证方法，不是结论**：

| # | 假设 | 验证方法 | 通过阈值 |
|---|---|---|---|
| H1 | 单进程 + SQLite 在 100 机目标规模下写路径不成为瓶颈 | 控制面压测：100 台模拟节点（心跳 15s + 每 5 分钟全量 inventory，响应大小取实测分布）+ 20 并发变更操作 | p99 写事务耗时 < 50ms；`agent_fleet_sqlite_busy_total` 在 1 小时稳态运行中为 0（或仅在重连风暴窗口内出现且自动恢复） |
| H2 | 重连风暴（100 台同时重连并全量上报）不导致持续写阻塞 | 故障注入：重启控制面，令全部节点同时重连 | 恢复时间 < 60s；期间无操作因 `SQLITE_BUSY` 失败 |
| H3 | 20 并发变更 + 周期写入不互相饿死 | 并发压测 | 变更请求排队延迟 p99 < 5s（这是 S2 降级后真正要观察的量） |

**必须显式配置的项**：

- `busy_timeout`（建议 5s，可配置）——必须显式设置，不能依赖驱动默认；
- 写连接数（SQLite 单写特性的前提下，写连接应串行化在应用层，避免多连接互相制造 `SQLITE_BUSY`）；
- 长事务禁止：inventory 落库必须拆分为多个短事务（单条 inventory 的写入不应持有写锁超过阈值）。

**【契约】指标的适用范围（S6 的归因纠正）**：spec §35.3 的 "<1s plan" 指标针对的是**在线 agentd 的本地 plan**——plan 计算发生在**节点侧**（reconciler 阶段 3，§5.3），控制面 SQLite 的写竞争**不进入该路径**。因此不能说 `SQLITE_BUSY` "直接影响 NFR-2"。SQLite 争用影响的是控制循环写、条件更新、SSE 推送与 UI 延迟，以及变更请求的排队时间（H3）。v1.0 把二者混为一谈的表述已删除。

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

**【新增】v1.1 必须补充的测试项**（对应本轮修订的契约，逐条可追溯；对照关系见 `architecture-revision-resolution.md`）：

| # | 测试项 | 断言 | 对应修订 |
|---|---|---|---|
| T1 | **节点跨进程执行权** | 同一临时 HOME 下并发运行 daemon 与 `oneshot apply`：恰好一条流水线执行；oneshot 渠道语义为 `NodeBusy` 失败，daemon 渠道排入队列；注入 `SIGKILL` 后陈旧锁被识别且不静默夺取 | A1/A2、§5.6 |
| T2 | **服务端持久化互斥** | 派发 op1 后重启控制面进程，立即经 SSH 触发 reconcile：op2 被拒（409 MachineBusy）；节点上任意时刻只有一条流水线 | A2、§4.3 |
| T3 | **迟到操作与代绑定** | reconcile@gen12 进行中升级 gen13：op1 终态 `Succeeded(Superseded)`；`Reconciled` 不为 True；对 gen13 呈 `Drifted=Unknown(ObservationPredatesDesired)`；随后 reconcile@gen13 正常收敛 | A3、FR-9.10 |
| T4 | **观测有序性** | 构造 `inventorySeq` 较旧的迟到报文（内容为操作前状态）：断言条件不被改写 | A3 反向时序、FR-8.7 |
| T5 | **门禁因果绑定** | 构造"status 缓存匹配 + 最新真实上报 drift"的机器：Deployment 不放行该目标，呈现"证据过期"；正常路径不受影响 | A4、§4.4 |
| T6 | **回滚保留未托管字段** | gen12 → 手工加未托管键 → gen13 → 回滚到 gen12（重放路径）：断言未托管键保留、受管字段等于 gen12 内容、验证通过 | A5、§7.2 |
| T7 | **操作内恢复的冲突检测** | 注入阶段 6 失败且期间存在外部编辑：断言走受管字段级回退并保留未托管编辑；不可行时终态 `Failed(RestoreConflict)` + `Degraded=True`，**绝不**静默整文件覆盖；无外部编辑时与操作前逐字节一致 | A5、§5.3 |
| T8 | **SSH plan/apply 基线** | plan 与 apply 之间修改受管文件：断言 apply 拒绝、零变更、`ReplanRequired`；基线未变时执行与展示计划一致 | A6、FR-12.7 |
| T9 | **工件路径安全** | 恶意 fixture bundle（`..` 条目、坏 digest 格式、名称含 `/`、内容内 symlink 逃逸）：断言全部拒绝、错误码明确、**临时 HOME 下无任何根外写入** | A7、FR-6.7 |
| T10 | **工件与 bundle 预算** | 超限 fixture：解析期/构建期即失败，错误码 `ArtifactTooLarge`/`BundleTooLarge`，不产生长时间传输 | S1、FR-6.8 |
| T11 | **enroll TLS 与 token 原子性** | 明文监听断言无 token 可见；并发两个相同 token 的 enroll 请求：至多一个成功；CSR 指纹不匹配即拒绝；token 重放失败 | A8、FR-11.4/11.5 |
| T12 | **重复连接策略** | 同 machineId 并发第二条 Connect 流：被拒并产生告警事件 | A8、FR-11.6 |
| T13 | **快照自包含** | 节点冷启动（无本地缓存）收到 `ExecuteOperation`：正常执行（必带完整快照） | A9、FR-13.6 |
| T14 | **重复派发与 outbox** | 同 operationId 重复 `ExecuteOperation`：执行中忽略；已终结则从 outbox 重发终态；重启服务端后 outbox 重报使 Unknown 终态化并释放互斥 | 小项 c、M4、FR-13.7/13.8 |
| T15 | **staging 清理与未决保护** | apply 前注入取消 → 再次触发 reconcile：远端仅一份 staging 内容；且构造未决操作时断言其输入**未**被删除 | A10、§4.5/§10.4 |
| T16 | **A→B→A 序列** | 单测：A→B→A 后 generation 递增、按代唯一行存在、回滚到中间代的引用可解析 | F6、ADR-2 |
| T17 | **投影契约** | 同一 fixture 下：双侧投影摘要用于判据、逐字段 diff 仅展示；构造规范化版本不匹配：断言不产生 drift 判定而是 `ProjectionVersionMismatch` + `Drifted=Unknown` | M1/M2、§7.1 |
| T18 | **空计划健康验证** | 无 drift 的机器执行 reconcile：断言仍执行阶段 10 与 12，不因空计划跳过健康证据 | 小项 a、§5.3 |
| T19 | **SSH-only 新鲜度三态** | 从未采集/过期/期望代变化三种情形分别断言 `Drifted=Unknown` 且 UI 呈现对应原因 | M5、FR-1.9 |
| T20 | **自动收敛边界** | 默认配置下 drift 检出：断言只上报不自动 apply；开启自动 apply 后断言产生与手动同形的 Operation（有 operationId、受互斥约束） | A11、§7.8 |
| T21 | **取消语义** | 各阶段注入取消：断言阶段边界生效、可变阶段走恢复路径后 `Failed(Cancelled)`、重复取消幂等、节点不可达时服务端不置 Cancelled | A3、FR-13.9 |
| T22 | **容量假设验证** | 按 §12.1 的 H1/H2/H3 执行压测与故障注入，产出实测数据 | S6、NFR-10 |

### 13.2 隔离原则

一切变更路径测试用临时 `$HOME`（NFR-6、护栏 #12）：适配器路径解析必须可注入 HOME 根，禁止硬编码 `/home/user`；SSH 测试容器化，杜绝触碰真实开发机。**【新增】跨进程测试（T1）必须在同一临时 HOME 下真实启动两个进程**，不得用同一进程内的两个 goroutine 模拟——该测试的目的正是验证跨进程语义。

### 13.3 验收标准 → 需求/机制映射

| 验收 | 承载需求 | 关键机制 |
|---|---|---|
| A 机群 onboarding | FR-1.1–1.4 | §9.1 时序；enrollment（TLS + 原子 token） |
| B SSH-only | FR-1.2、FR-9.4、FR-12.4、**FR-12.7** | bundle 机制 §7.4；基线比对 |
| C 版本管理 | FR-2.1–2.4 | installer + 安装后验证 |
| D 配置管理 | FR-3.2、FR-5.1 | 合并/标记块 §5.5 |
| E Skills | FR-6.1–6.3、**FR-6.7/6.8** | 解析/物化/drift 信号/路径与预算校验 |
| F MCP | FR-4.1–4.3 | 适配器渲染 + 未托管保留 |
| G drift/reconcile | FR-8.3–8.5、**FR-8.6/8.7**、FR-9.1 | 投影摘要契约 §7.1 |
| H Deployment | FR-10.1–10.3、**FR-10.6/10.7** | 状态机 + 因果门禁 §4.4/§7.3 |
| I 修复 agentd | FR-1.5、FR-1.7 | §9.4 时序 |
| J 回滚 | FR-2.4、FR-9.6、**FR-7.5**、§7.2 | 重放式回滚 + 保护引用集合 §10.4 |
| K SSH 导出 | FR-12.6 | include 渲染 §7.7 |
| L 安全 | NFR-4 全部、**FR-11.4–11.6** | 第 11 章十四点落点 |

DoD（§39）= 完整控制闭环演示：Desired State → 不可变快照 → agentd/SSH 派发 → 本地 plan/apply → 健康验证 → 观测状态 → drift 决策 → Web UI 状态。该闭环由上述机制串联，缺一环即不达标。

**【契约】关于本节的性质**：§13.3 是**需求到验收的映射**，不是"已经通过验收"的记录。本轮修订没有运行任何测试。

### 13.4 spec A–L 之外必须新增的故障反例映射【新增】

两轮评审发现的失败场景在 spec §34 的 A–L 中没有对应验收项。v1.1 为每个反例指定了承载测试项，使"设计承诺"变成"可验证断言"：

| 故障反例 | spec A–L 中的位置 | v1.1 承载测试 |
|---|---|---|
| daemon 与 SSH 双通道并发变更同一节点 | 无 | T1、T2 |
| 控制面重启后 Unknown 操作与新操作交错 | 无 | T2、T14 |
| 迟到操作覆盖新代语义 | 无 | T3、T4 |
| 门禁缓存状态放行漂移机器 | 部分（H 只说"健康"） | T5 |
| 回滚毁掉未托管编辑 | 无（J 只验证"能恢复"） | T6、T7 |
| SSH plan/apply 之间状态变化 | 无 | T8 |
| 恶意/畸形 bundle | 无 | T9、T10 |
| enroll 明文抢注与假节点 | 部分（L 只验证"不泄露秘密"） | T11、T12 |
| 冷启动节点取不到快照 | 无 | T13 |
| 重复派发产生第二条变更链 | 无 | T14 |
| staging 泄漏与误删未决输入 | 无 | T15 |
| A→B→A 代引用失效 | 无 | T16 |
| 投影不可比导致误判 | 部分（G 只说"drift 出现并收敛"） | T17 |
| 空计划跳过健康验证 | 无 | T18 |
| apply 后验证失败被虚报为成功 | 无（J 只验证"能恢复"） | T7 扩展断言（注入阶段 12 失败：断言恢复执行、上报 `VerifyFailed` 与恢复结果、不写成功） |
| 未知/过期被呈现为一致 | 无 | T19 |
| 自动 apply 绕过审计与互斥 | 无 | T20 |
| 取消语义缺失 | 无 | T21 |
| 容量假设未验证 | 部分（NFR 只有目标值） | T22 |

### 13.5 CA 备份/恢复演练（v1.1.1 新增，当前状态：计划已定义、未执行）

CA 备份/恢复能力**没有产品的自动化测试承载**（它不是产品功能，§2.6、§14.3 RD-7），因此 v1.1 的 T1–T22 不覆盖它；它的验证方式是**操作者在隔离环境执行的演练**。演练项 DR-1～DR-8、前置条件、步骤、成功判据与记录模板定义于 `manual-ca-backup-recovery.md`，本架构只声明验收口径：

| # | 演练项 | 通过判据（状态：待验证） |
|---|---|---|
| DR-1 | 归档完整性/权限检查 | 备份归档可解密/可展开；还原出的 `ca.key`、`server.key` 权限为 0600；`ca.key` 与 `ca.crt` 公钥配对 |
| DR-2 | 隔离环境控制面启动 | 用备份在隔离位置启动控制面，迁移顺序应用成功，`fleet.db` 可打开 |
| DR-3 | 可信节点连接 | 至少一台既有可信节点经恢复后的控制面建立 mTLS `Connect` 流，machineId 与库中记录一致 |
| DR-4 | 错误/过期证书被拒绝 | 用不属于该 CA、或已过期的客户端证书连接 → 被拒绝，且失败可观测 |
| DR-5 | 关键资源恢复 | 配置、身份元数据（`machines`/`agent_certificates`）、按恢复目标的工件可在恢复后被读取并保持语义 |
| DR-6 | CA 丢失（有备份）路径 | 按 §4.6 分支一恢复后，既有节点无需重新 enroll 即可连接 |
| DR-7 | CA 丢失（无可用备份）路径 | 按 §4.6 分支二走重新生成 CA + SSH repair 重新 bootstrap，节点身份重建成功 |
| DR-8 | 异常恢复路径 | 注入"备份损坏/配对失败/迁移失败"之一时，流程在预定停止点**停止并保留原现场**、按退回路径回退，不出现静默覆盖 |

**性质声明【契约】**：本表是**待执行的验证计划**，不是已通过的测试。**KM-14 交付时未执行任何演练**（无可运行产品、无实现仓库；且恢复演练涉及真实密钥材料，不在本任务授权范围内）。任何"CA 已可恢复"的对外结论，必须引用一份已填写的演练记录（模板见 `manual-ca-backup-recovery.md` 附录 A），在此之前只能表述为"方案已定义、待验证"。

---

## 14. 架构决策记录（整理自 §36 + 实现级补充 + v1.1 新增 ADR）

### 14.1 spec §36 的架构决策

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

### 14.2 v1.1 新增的架构决策记录（ADR）

#### ADR-1：drift 比较位置——节点侧双侧投影 vs 服务端投影 schema

**背景**：drift 判定与门禁都依赖"期望受管投影摘要 == 观测受管投影摘要"（§7.1）。该契约在 v1.0 缺失：期望侧投影摘要 spec 未定义、文档也未补（M1）。实现它必须决定**谁来计算这两个摘要**，而该决定直接影响 I-1（控制面零适配器知识）与"规范化规则一致性"。

**候选方案**：

| 方案 | 做法 | 优点 | 代价 |
|---|---|---|---|
| **A. 节点双侧投影**（本版采纳） | 节点适配器在每次 inventory 时，对随操作下发的期望快照计算期望侧投影摘要，对本机原生配置计算观测侧投影摘要，二者一起上报；服务端只存两数与比较 | 控制面**完全不需要**投影 schema 与规范化实现，I-1 平凡成立、护栏 #2 无例外；两侧必然出自同一份代码与同一版本，不存在实现分叉；逐字段 diff 天然同源 | 服务端失去独立复核能力（节点说一致即一致）；快照不自包含投影描述，跨工具审计变弱；漂移判定依赖节点实现正确性 |
| B. 服务端投影 schema | 快照携带 `projectionSchema`（适配器投影字段描述符）+ `canonicalizationVersion`；服务端按描述符计算期望侧投影摘要 | 服务端可独立计算与复核；快照自包含、便于第三方审计 | 控制面必须理解并忠实实现一套投影语义——这与 I-1/护栏 #2 直接冲突，需要开例外；规范化规则要在服务端与节点两侧各实现一次，存在分叉风险；快照结构、bundle、gRPC 消息都要随之扩展 |

**决策**：采用方案 A。

**理由**：I-1 与护栏 #2 是 spec §38.2 的强制约束，方案 B 需要为它开例外，而例外的收益（服务端独立复核）在**单操作者 + 同一二进制发布**的假设下价值有限——节点与服务端来自同一仓库的同一次发布，方案 B 所谓的"独立"复核并不独立于同一份代码库。方案 A 用"两侧同源"直接消除了 M1 想解决的"比较双方规则不一致"问题。

**代价与接受的理由**：

1. 服务端无法在节点说谎时发现矛盾。接受的依据：MVP 的信任模型是"操作者是可信的、节点是操作者自己的机器"（spec §3.7），且不存在恶意节点的场景——若节点不可信，整个 apply 结果都不可信，比较位置救不了；
2. 快照不自包含投影描述。接受：跨工具审计在 MVP 不是需求（非目标范围），且 `canonicalizationVersion` 随快照与观测落库，事后审计仍能知道"当时用哪版规则比较的"。

**验证方式**：T17（投影契约测试）——同一 fixture 下双侧投影摘要用于判据、逐字段 diff 仅展示；构造规范化版本不匹配，断言系统不产生 drift 判定而是报 `ProjectionVersionMismatch` 并把 `Drifted` 置 `Unknown`。

**重新评估条件**（出现以下任一证据时应重开此 ADR）：需要跨工具/第三方审计 drift 判定；出现多实现节点（非同一仓库发布）；需要服务端在不信任节点的前提下判定一致性。

#### ADR-2：generation 身份与内容身份——单表按代唯一 vs 双表拆分

**背景**：v1.0 同时主张 `desired_snapshots` 上"(machine_id, digest) 唯一"与"摘要相同则不产生新行"。A→B→A 回退序列下二者冲突（F6）：内容 A 在 gen12 已有一行，回到 A 时代计数需递增到 14，但没有 generation=14 的行，`targetGeneration=14` 无行可指。

**候选方案**：

| 方案 | 做法 | 优点 | 代价 |
|---|---|---|---|
| **A. 单表按代唯一**（本版采纳） | `desired_snapshots` 按 `(machine_id, generation)` 唯一；`digest` 普通索引；同一内容可在多代重复存储 | 回滚是主键查找；无跨表一致性；无引用计数；无"内容行被回收而代行悬空"的失败模式；迁移最小（只改约束） | 内容重复存储（MVP 规模下可忽略：100 机 × 数千代 × KB 级 JSON） |
| B. 双表拆分 | `snapshot_contents(machine_id, digest UNIQUE, json)` + `generation_history(machine_id, generation UNIQUE → digest)` | 存储不重复；内容与代概念清晰分离 | 每次解析要多一次写与一次 join；需要内容引用计数或"永不回收"策略才能保证回滚可达；多一张表的迁移与一致性维护 |

**决策**：采用方案 A。

**理由**：方案 B 的唯一优势（去重）在 MVP 规模下没有实际收益，而它引入的一致性问题（内容回收与代引用）恰好会破坏 M3 要保护的"回滚可达性"——用复杂性换取一个不需要的优化，且新复杂度的失败模式直接落在产品的核心承诺上。

**代价**：存储重复。量化：单快照 KB 级，100 机 × 假设每机 5000 代 ≈ 500 MB 量级 JSON；即便按更悲观的估计也在可接受范围，且可用"同一 digest 只存一份 JSON、行内指针复用"作为后续无损优化（不影响外部语义）。

**验证方式**：T16（A→B→A 序列单测）。

**重新评估条件**：实测快照表体积成为运维问题（例如单机代数量级突破 10^5）。

#### ADR-3：automatic reconcile 的边界——自动 plan 与自动 apply 分离

**背景**：spec §4.1 把 "Manual and automatic reconcile" 列为 in-scope，但未定义"自动"到什么程度；v1.0 的 FR 表未承载该需求，§3.4 流 1 的"apply 按策略"中策略未定义（A11）。若直接把"自动 apply"设为默认，会与 A1 的并发窗口叠加；若直接删除该需求，则是对 spec 的偏离，需要 owner 批准。

**候选方案**：

| 方案 | 做法 | 优点 | 代价 |
|---|---|---|---|
| **A. 分离为两能力，自动 plan 默认开、自动 apply 默认关**（本版采纳） | 自动 plan/上报（只读）默认开启；自动 apply 仅当显式开启，且必须是控制面下发的普通 Operation | 保留 spec 的 automatic reconcile 需求（不删不延期）；默认行为不引入写并发；审计完整；操作者随时可开启 | 需要两个开关与相应 UI；"自动"在产品语义上被默认弱化了一点 |
| B. 自动 apply 默认开启 | 节点检出 drift 即自动收敛 | 最贴合"期望状态"理念 | 默认引入无人监督的写操作；与 A1 并发窗口叠加；审计与门禁依赖节点自发行为 |
| C. 从 MVP 移除 automatic reconcile | 只保留手动 + Deployment | 最小实现面 | 是对 spec in-scope 项的偏离，需 owner 批准（v1.1 无此授权） |

**决策**：采用方案 A。

**理由**：方案 C 需要范围变更授权（本版没有）；方案 B 把"默认安全"与"默认自动"混为一谈。方案 A 同时满足 spec 的需求承载与 v1.0 设计意图，并把"是否让系统自动写入用户的开发机"变成一个**显式、可审计、可撤销**的开关——这正是这类能力的合理默认。

**代价**：多一个产品开关与其 UI；自动收敛在默认配置下不生效，可能让期望"开箱即自动"的使用者意外。

**验证方式**：T20（自动收敛边界测试）。

**重新评估条件**：操作者反馈默认关闭造成实际负担，且已具备足够的门禁与审计信心（例如 T1–T5 全部通过并在真实机群验证过）。

### 14.3 实现级补充决策（保留项 + v1.1 修订）

| # | 决策 | 理由 |
|---|---|---|
| RD-1 | gRPC 端点直接以 TLS 终结（不经反代） | 单进程约束下少一个组件；advertiseURL 直指服务端 |
| **RD-2（重写）** | **enrollment 端点是独立 HTTPS 服务**，与 agent gRPC 端口分离；两者**均强制 TLS**。agentd 在 enroll 时**必须校验服务端证书**（用 bootstrap 经 SSH 下发的 CA 证书）；enroll 仍以一次性 token 鉴权，不要求客户端证书 | v1.0 的"复用同一监听端口的 HTTP/2 明文路径"与 spec §11/§29.5 直接冲突，且使 token 与 CSR 明文暴露（A8）。分离端口使"enroll 端点的限速、审计与暴露面控制"可以与 agent 通道独立配置；TLS + token 已足够，不需要为 MVP 引入客户端证书引导 |
| RD-3 | SQLite 驱动选 modernc.org/sqlite（纯 Go，免 CGO 交叉编译负担） | 4 平台交叉编译简单性优先；性能对目标规模富余（**但需满足 §12.1 的 H1–H3 验证**，不得以"富余"代替测量） |
| RD-4 | 期望快照规范化 JSON：键序字典序、无符号数、UTC 时间戳、剔除易变字段 | 支撑 FR-7.2 确定性摘要 |
| RD-5 | agentd 用户服务模板按探测到的服务管理器选择（systemd user / launchd）；不支持时降级为提示 + 手动启动 | §5.1 的"where supported" |
| RD-6 | 20 并发变更用带权信号量实现（daemon 与 SSH 路径共享计数）；**【澄清】为手动与 Deployment 操作保留至少 1 个槽位，自动操作不得占满** | 全局上限语义统一（NFR-1）；避免自动操作饿死人工请求（§7.8、S2 降级后的真实问题） |
| **RD-7（v1.1.1 改写）** | **MVP 不新增内置的 CA 备份导出产品功能（用户已决，KM-14）；同时必须提供人工安全备份、恢复与演练方案。不引入 HSM/外部 PKI。** CA 丢失（有可用备份）按恢复路径处理；CA 丢失且无可用备份才落到"重新生成 CA + 全机群重新 bootstrap"；**CA 泄露必须重建信任、不复用已泄露私钥**（三分支见 §4.6） | MVP 不做企业 PKI/HSM 是 spec §12.2 的取向。"不做内置导出"仍是范围边界（避免新增一个产品动作与密钥保护问题），但**用户已明确"不导出"不等于"不备份"**（来源：KM-11 评论 `01a0bceb-5547-7531-b625-9957ad171b81`）。因此 v1.1 的"不备份＝已接受风险"表述作废；人工备份/恢复/演练是**必须运维契约**，操作步骤见 `manual-ca-backup-recovery.md`。**本决策只改文档与运维要求，不新增 FR、不新增产品 API/CLI/UI**；实现与演练状态均为**待验证** |

### 14.4 明确不采纳的评审建议

以下建议在评审中出现过，但本版**不采纳**，并给出理由（对应汇总裁决的采纳边界）：

| 建议来源 | 建议内容 | 不采纳的理由 |
|---|---|---|
| KM-12 A4 修改建议 | 用 `reportedAt > op.finishedAt`（服务端接收序）作为门禁证据新鲜度判据 | 方向错误（健康检查/inventory/verify 都在操作提交**之前**执行，会拒绝正常路径）且强度不足（接收时间不能证明采集时间，迟到旧报文照样"晚到"）。改用身份绑定（operationId）+ 序绑定（inventorySeq），见 §4.4 |
| KM-12 A1 修改建议 | 只用节点单 worker 队列即可表达互斥 | 单 worker 只覆盖**同一进程内**的串行；无法阻止 SSH 启动的另一个 oneshot 进程，也无法在服务端重启后恢复互斥。必须以节点**跨进程**执行权 + 服务端持久化互斥双重表达，见 §5.6/§4.3 |
| KM-12 A5 修改建议 | 把"操作窗口内丢失未托管编辑"写成已接受限制 | 与 spec §3.5、护栏 #3 及 I-6 的意图冲突；该豁免不能由评审自行批准。改为冲突检测 + 受管字段级回退 + 显式拒绝，见 §5.3 |
| KM-12 V-M3 论证 | "不同 generation 必然对应不同 digest"（据此认为按代唯一是空操作） | 该全称判断与其自身的 F6 反例矛盾（A→B→A 时两代内容相同）。保留 A→B→A 的问题，舍弃该论证。见 ADR-2 |
| KM-12 V-M1 处方 | 期望侧投影"只能"由快照携带 projectionSchema 算出来（唯一解） | 存在同样自洽且更贴合 I-1 的候选（节点双侧投影）。作为 ADR 二选一处理，见 ADR-1 |
| KM-12 V-M1 补充 | 规范化版本仅作诊断信息、不影响可比性 | 若不要求比较双方版本一致，跨版本会静默沿用旧判定，产生错误 drift 结论。改为版本必须影响可比性，见 §7.1 |
| KM-12 安全建议若干 | token 熵取特定数值、永久保留所有快照、不备份 CA、删除机器后仍允许旧证书连接、Unknown 目标允许人工跳过 | 这些均未获得原 spec 的明确授权或验收依据，不能由评审直接宣布"已接受"。其中"删除后证书仍有效"与"跳过 Unknown"作为**已接受风险**记录（§2.2/§9.6）而非需求；token 熵给出 ≥128-bit 的下界（安全属性而非具体取值）。**【v1.1.1 更正】"不备份 CA"这一项的处置已由用户决定取代**：仍**不新增内置产品导出动作**（范围边界不变），但人工备份/恢复/演练是**必须运维契约**，不再记为"已接受风险"（§2.2、§4.6、RD-7） |
| KM-12 清理建议 | 节点清理"未被当前快照引用"的工件 | 只按当前引用判断会删掉上一成功状态、未决操作输入与已承诺回滚目标所需的材料。改为按保护引用集合判定，见 §10.4 |
| KM-12 超范围建议 | enroll 复用 SSH 通道回传 CSR（彻底消除网络明文面） | 改变协议边界，不建议 MVP 实施；记录于 §16 作为 Post-MVP 可评估方向 |

---

## 15. 与 spec 的可追溯性总表

| spec 章节 | 本文承载 |
|---|---|
| §1–2 摘要/问题 | 1.2、2.1 |
| §3 设计原则 | 3.1 不变量 I-1～I-11 |
| §4 MVP 范围/非目标 | 2.4（FR 全表）、2.6 |
| §5 用户体验 | 2.3（U1–U7）、9.1/9.2/9.4 |
| §6 高层架构 | 3.2–3.4 |
| §7 部署模型 | 3.5、4.1、7.4、12 |
| §8 领域模型 | 6 |
| §9 有效期望状态 | 4.8、6.3、7.1、ADR-2 |
| §10 观测与 drift | 5.2、7.1、4.2（新鲜度与三态） |
| §11 agentd 协议 | 8.2、13（结果 outbox 与幂等） |
| §12 enrollment/mTLS | 4.6、7.5、9.1（TLS + 原子 token + 重复连接策略） |
| §13 SSH 传输 | 4.5、7.4、7.7（staging 与基线比对） |
| §14 本地 reconciler | 5.3、5.6（执行权与恢复冲突检测） |
| §15 适配器契约 | 5.4（含投影义务） |
| §16 合并与所有权 | 5.5、7.2 |
| §17 Skills | 4.7、7.4（路径与预算约束） |
| §18–20 Provider/MCP/版本 | 2.4（FR-2、FR-3、FR-4）、6.1 |
| §21 Deployment | 4.4、7.3（因果门禁与代一致性） |
| §22 conditions | 4.2、6.2（三态与新鲜度） |
| §23–24 API/UI | 8 |
| §25–26 持久化/文件 | 10（保护引用集合与清理规则） |
| §27 仓库布局 | 3.6 |
| §28 服务器配置 | 4.1、4.5 |
| §29 安全 14 条 | 11 |
| §30 错误处理 | 6.4（四组错误码） |
| §31 版本协商 | 7.6（含 canonicalizationVersion） |
| §32 测试 | 13（含 T1–T22 与故障反例映射） |
| §33 可观测 | 12（含追加指标） |
| §34 验收 A–L | 13.3、13.4 |
| §35 NFR | 2.5、12.1 |
| §36 AD-1–8 | 14.1 |
| §37 延期项 | 2.6、16 |
| §38 护栏 12 条 | 2.7（含 v1.1 追加 6 条） |
| §39 DoD | 13.3 |
| §40 Codex Remote SSH | 7.7 |

---

## 16. 演进路线（Post-MVP 扩展点，对应 §37）

架构为以下方向预留了位置，但 MVP 一律不实现：

1. **原生 installer drivers**：`installer` 包以 driver 接口封装，`command` 只是首个实现（FR-2.6 是这一点的显式承载：仅要求抽象点存在，不要求 driver 落地）；
2. **原生 Windows**：适配器/路径解析以 GOOS 分派，reconciler 流水线本身平台无关；
3. **secret-manager 集成**：`apiKeyEnv` 引用模型天然兼容"值由外部注入"——只需新增 provider 端的引用类型；
4. **PostgreSQL**：仓储接口在领域层，`store/sqlite` 可平行增加实现；
5. **GitOps 期望源**：期望渲染器输入是资源集合，可增加"从 Git 读取资源"的前置加载器；
6. **第三方适配器插件 SDK**：适配器接口已是边界，后续可加进程外插件（go-plugin）而不改核心；
7. **SSO/RBAC、审批门、多操作者**：admin token 中间件位置即认证/鉴权中间件位置；Deployment 状态机可插入 approval 阶段；
8. **Webhook/OTel/Grafana**：SSE 枢纽旁挂事件订阅器即可；指标已 Prometheus 格式；
9. **【新增】`GetSnapshot` RPC 与摘要引用形态**：当快照体积或节点数量使"每操作携带完整快照"成为负担时，可增加配套 RPC 与引用形态（FR-13.6 的延期项）；
10. **【新增】快照表双表化**：若单表重复存储成为运维问题，可按 ADR-2 记录的无损迁移路径拆分为内容表 + 代表；
11. **【新增】证书吊销机制**：CRL/OCSP 或短证书 + 频繁续期；MVP 明确不做（§2.2 已接受风险）；
12. **【新增】enroll 经 SSH 回传 CSR**：彻底消除网络明文面（记录为可评估方向，不改变 MVP 协议边界）；
13. **【新增】自动收敛的策略化**：按机器分组、按时间窗、按 drift 类型的细粒度自动 apply 策略（MVP 只有全局/单机开关）。

---

## 17. 风险与开放问题

| # | 风险/开放问题 | 影响 | 缓解 |
|---|---|---|---|
| R1 | 三家 Agent 的配置 schema/路径随版本漂移，适配器维护成本 | 适配器失效 → 误报/漏报 drift | fixture 测试锁定行为（§32.2）；schema 版本协商挡住不兼容 agentd（§7.6）；**投影契约由适配器单点实现（ADR-1），改动面收在一个包内** |
| R2 | SSH 探测/安装命令在不同发行版/服务管理器差异大 | bootstrap 失败率 | 探测先行 + UnsupportedPlatform 显式分类；E2E 覆盖 Linux 容器矩阵 |
| R3 | npm 等 installer 全局安装在用户环境可能涉及 sudo/权限差异 | 安装失败 | 安装命令完全可覆盖（FR-2.5）；错误如实上报 InstallerFailed |
| R4 | macOS launchd 用户服务行为（登录会话/休眠） | daemon 稳定性 | MVP 以单元/fixture + 手动 smoke 覆盖（§32.6 末段）；文档明示限制 |
| R5 | SQLite 单写并发（长 inventory 写 + SSE 读 + 控制循环写） | 写延迟与变更排队 | WAL + 短事务 + 显式 `busy_timeout` + 观测历史滚动清理；**容量按 §12.1 的 H1–H3 实测验证，不假设** |
| R6 | **CA 根密钥丢失或泄露**（v1.1.1 改写，用户已决） | 丢失（有可用备份）= 可恢复，不要求全机群重建；丢失且**无可用备份** = 必须重新生成 CA 并全机群重新 bootstrap；**泄露** = 必须重建信任（重新生成 CA、重签/重新 enroll），恢复旧备份**不能**消除已泄露的信任 | **人工备份、恢复与演练为必须运维契约**（§4.6、RD-7、`manual-ca-backup-recovery.md`），不再是"已接受风险"。残余风险仅为"操作者未备份/备份不可用"与"备份介质本身泄露"，由一致性流程、加密保管与定期演练压低（§2.2）。**实现与演练状态：待验证** |
| R7 | **机器删除后证书至到期前仍有效（无吊销）** | 被删除机器仍能建立 Connect 流 | 单操作者威胁模型下接受；UI 明示这是已接受风险而非安全措施（§2.2、§4.6） |
| R8 | **节点上存在不受控制面约束的 oneshot 进程** | 与控制面派发的操作并发 | 节点跨进程执行权（§5.6）为唯一防线；陈旧锁不静默夺取 |
| R9 | **Unknown 操作悬置** | 该机无法推进新变更、Deployment 目标阻塞 | 三条显式收敛路径 + `agent_fleet_operation_unknown_age_seconds` 指标 + UI 醒目提示（§9.6） |
| R10 | **自动 apply 若被开启且门禁/审计不完善，可能自动扩大影响面** | 未经人工确认的批量变更 | 默认关闭；开启后仍与手动同形（同一互斥、同一证据要求、连续失败熔断、为手动保留槽位）（§7.8） |
| R11 | 操作者设置过于宽松的工件预算或过长的 staging 保留 | 节点磁盘占用 | 预算有默认值且超限快速失败；清理按保护集合判定并有告警指标（§4.5、§10.4） |
| O1 | 开放问题：agentd 升级窗口与协议主版本不兼容节点的人工处理流程细节 | 少量运维摩擦 | MVP：UI 提示 + SSH repair 通道替换（已覆盖验收路径）；细节留实现阶段确认 |
| O2 | 开放问题：Skill 内容 digest 的规范化细节（权限位/符号链接是否入哈希） | 影响 drift 稳定性 | RD-4 原则延伸：MVP 建议"固定排序 + 内容 + 常规权限位"入哈希，实现阶段以 fixture 固化；**内容内 symlink 按 §4.7 默认拒绝，因而"symlink 是否入哈希"的争议范围收窄** |
| O3 | **【新增】待决点：`canonicalizationVersion` 的兼容策略** | 规则升级时可能造成全机群短暂不可比 | MVP：版本不匹配即不比较（置 Unknown）并在 UI 提示升级 agentd；是否需要"双版本并行比较"的过渡期留待实现阶段按升级体验决定（**若需要并行期，属于用户体验决策，不改变本版契约**） |
| O4 | **【v1.1.1 已决，不再是待决点】CA 根密钥的备份方式** | 影响灾难恢复能力 | **用户已决（KM-14，来源 KM-11 评论 `01a0bceb-5547-7531-b625-9957ad171b81`）**：MVP **不新增内置"导出加密 CA 备份"产品机制**（范围边界），**同时**必须提供人工安全备份、恢复与演练方案（§4.6、RD-7、`manual-ca-backup-recovery.md`）。**范围选择=已决；实现与演练=待验证**（无实现仓库，演练未执行） |

**【契约】待决点的处理原则**（v1.1.1 更新）：**仅 O3 仍是待决点**，且不阻塞实现。O4 已由用户决定（见上），移出待决集合；其结论按 RD-7 与 §4.6 落地，实现与演练部分标记为待验证，不因"已决"而被当作已验证。

---

## 18. 结论

本文档完成了 spec v0.1 的需求拆解（**15 组功能需求 95 条子项**、**10 组非功能需求**、14 条非目标、**18 条护栏**）与架构设计（总体架构、控制面/节点组件、领域模型、八项关键机制、REST/gRPC 契约、数据设计、安全十四点落点、测试矩阵、AD/RD 决策记录与全量追溯表）。

> **统计口径说明【契约】**：上述数字由脚本对 §2.4 表格逐行机械统计得到（v1.1 新增 25 条，v1.0 为 70 条），并与 §2.8 完全一致。v1.0 此处写"55 条"而表格实为 70 条，属于正文统计文字未随表格同步；本版以表格为准，并在 `architecture-revision-resolution.md` 中保留统计验证记录。**该段数字不得手写，必须由统计脚本生成后核对。**

设计核心仍是一条不变的闭环：

```text
期望状态 → 不可变快照 → agentd / SSH 双通道派发 → 本地 plan/apply（备份护航）
→ 健康验证 → 观测状态 → drift 决策 → Web UI 呈现
```

v1.1 相对 v1.0 的实质变化是：把这条闭环上**每一处"看起来成立"的边界**——双通道并发、控制面重启、迟到结果、外部编辑、工件路径、证书引导、容量承诺——都换成了**可实现的契约与可验证的断言**。具体而言：

1. **执行不再依赖单一防线**：节点跨进程执行权 + 服务端持久化互斥双重表达同机串行（I-8），两者缺一即不可实现；
2. **状态不再被推断**：操作终态只能由节点报告、取消确认或显式跳过产生（I-9）；Unknown 不因"看起来一致"而消失；
3. **摘要不再是三种混用的一个概念**：快照摘要只做身份、投影摘要只做判据、diff 只做展示，且规范化版本影响可比性（I-11）；
4. **回滚与恢复不再以牺牲用户数据为代价**：回滚是重放，操作内恢复先检测冲突（I-10）；
5. **清理不再只看当前引用**：上一成功状态、未决操作的输入、已承诺的回滚目标同受保护（§10.4）；
6. **"无容量风险"被换成待验证假设与验证方法**（NFR-10、§12.1）。

v1.1.1（KM-14）只追加一条同等的边界修正：**信任根的可恢复性不再靠"已接受风险"打发**——CA 丢失、无备份与泄露三条分支给出各自的恢复口径，人工备份/恢复/演练成为必须运维契约，而"内置导出"仍明确留在产品范围之外（§2.6、§4.6、RD-7、R6、O4）。**其范围选择为"用户已决"，其实现与演练为"待验证"**——两者不得混为一谈。

所有架构选择（单进程、SQLite、出站 gRPC、系统 OpenSSH、单一 reconciler、无 secret、无 K8s、无任务代理）都服务于同一目标：让单操作者以最小运维成本获得对多机 Agent 开发环境的**声明式、可审计、可回滚**的控制。该闭环即 MVP 产品本身（§39）。

**本版状态**：v1.1.1 文档修订版，待复核。本文档不构成评审通过，也不构成任何软件测试结论——本版未运行产品代码、未执行攻击、未做性能实测；文中所有规模与性能数字均为转引或算术推演，容量结论已按 §12.1 标注为待验证假设。v1.1.1 的增量仅为 CA 备份决策的落地：**决策范围已由用户确认，但 CA 备份/恢复演练（§13.5 DR-1～DR-8）尚未执行**，不得据此宣称 CA 已可恢复。

---

## 附录 A：v1.1.1 触及位置索引（供复核逐条核对）

| # | 位置 | 类型 | 摘要 |
|---|---|---|---|
| 1 | 文首来源说明 + 文档头表 | 新增 | v1.1.1 增量说明、版本/日期/依据/执行人/状态 |
| 2 | §1.6（新增） | 新增 | v1.1→v1.1.1 全部实质变更清单（12 处）与范围边界 |
| 3 | §2.2 已接受风险 | 修改 | "不备份＝已接受风险"作废；残余风险重新定义；泄露单列 |
| 4 | §2.6 v1.1 明确不引入 | 修改 | 内置导出为明确非目标；人工备份为必须运维动作 |
| 5 | §4.6 | 重写 | CA 材料人工备份/恢复/演练契约（9 条） |
| 6 | §7.5 | 修改 | "CA 私钥永不离开控制面主机"→产品不外发 + 人工备份例外 |
| 7 | §10.2 | 新增说明 | 人工备份副本不属于产品布局 |
| 8 | §13.5（新增） | 新增 | 演练项 DR-1～DR-8 与"未执行"性质声明 |
| 9 | §14.3 RD-7 | 改写 | 内置导出非目标 + 人工备份契约 + 待验证 |
| 10 | §14.4 | 修改 | "不备份 CA"处置由用户决定取代（更正说明） |
| 11 | §17 R6 | 改写 | 三分支恢复契约；人工备份为必须 |
| 12 | §17 O4 / 待决点处理原则 | 改写 | 标记"用户已决"；仅 O3 仍待决 |
| 13 | §18 结论与本版状态 | 修改 | 追加 v1.1.1 边界修正与"演练未执行"声明 |

**未改动项声明**：§2.4 全部 FR（含编号与计数）、§2.8 统计、§3–§12 的其它技术契约、§13.1–13.4 的 T1–T22、§14.1/14.2/14.3 其它 RD、§15 追溯表、§16 演进路线，均与 v1.1 逐字一致（除为指向新增小节而必需的极少数措辞外，无此类改动）。
