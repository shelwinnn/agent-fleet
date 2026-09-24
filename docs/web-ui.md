# Web UI（第 5 片，KM-25）

面向单操作者的 agent-fleet 控制面 Web UI。技术栈是**用户已定案并登记为显式 FR 例外**的
Svelte + TypeScript + Vite + shadcn-svelte（架构 v1.1.2 FR-14.4、§14.5；组件按页面需要引入）。
API 契约不依赖前端框架（NFR-9）：UI 只消费 `api/openapi/fleet-v1.yaml`。

## 运行

```bash
cd web
pnpm install
pnpm dev            # http://localhost:5173，经 vite proxy 把 /api、/healthz、/readyz 转发到控制面
pnpm test           # 单元测试（drift 三态、未决操作、Deployment 目标、SSE 解析、客户端）
pnpm check          # svelte-check（类型）
pnpm build          # 生产构建（dist/）
pnpm integration    # 与真实控制面的联调检查（需控制面在 127.0.0.1:7788 或 FLEET_API）
```

- 控制面地址：`FLEET_API`（默认 `http://127.0.0.1:7788`）。dev/preview 走同源 proxy，
  因此浏览器侧不需要 CORS，也不把 admin token 放进 URL。
- admin token（§29.14）：界面上「管理令牌」输入，只保存在内存并作为 `Authorization: Bearer` 发送。
  服务端未配置 token（回环默认）时留空即可。

## 路由

| 路由 | 页面 | spec 出处 |
|---|---|---|
| `#/` | Overview（6 张卡片 + 需关注机器 / 最近发布 / 最近失败操作） | §24.1 |
| `#/machines` | Machines（8 列 + Add/Probe/Bootstrap/Reconcile/Repair） | §24.2 |
| `#/machines/:name` | Machine Detail（9 个区块，无交互 shell） | §24.3 |
| `#/profiles`、`#/profiles/:name` | Profiles（JSON/YAML 预览、消费机器、渲染预览） | §24.4 |
| `#/skills` | Skills（Name/Source/Requested Ref/Resolved Revision/Digest/Machines） | §24.5 |
| `#/deployments`、`#/deployments/:name` | Deployments（批次进度与逐机结果） | §24.6 |
| `#/ssh-inventory` | SSH Inventory（别名 + OpenSSH include 预览与导出） | §24.7 |

hash 路由：静态产物可被任意静态服务器托管，不需要服务端 rewrite 规则。

## 状态呈现（FR-14.5 四组）

| 组 | 实现位置 | 要点 |
|---|---|---|
| drift 三态 | `src/lib/state/drift.ts` | `已确认一致（上次确认于 T）` / `未知或过期（原因）` / `已漂移`。**Unknown 绝不渲染成"一致"**（M5）；Unknown 必带后端 reason code 原文（NeverInventoried / StaleObservation / ObservationPredatesDesired / ProjectionVersionMismatch）。 |
| 未决操作与阻塞 | `src/lib/state/operations.ts` | 未决相位集合与 §4.3 唯一索引谓词一致（含 AwaitingConfirmation/Unknown）；列出被阻塞的动作与三个例外端点（确认/取消/跳过）；跳过必须填原因且明示"不表示节点已停止、不计入成功"。 |
| Deployment 目标异常态 | `src/lib/state/deployments.ts` | `Superseded`（机器已在更新代，附原因码）与 `Skipped`（显式跳过）与 `Failed` 分开显示，且都不计入成功；`Blocked` 明示"不计入失败也不推进批次"。 |
| SSH 路径 AwaitingConfirmation 与 plan 基线失效 | `src/lib/state/ssh.ts` | "等待确认（剩余 T）"、确认入口旁的基线提示（apply 会重算 plan 并比对基线，不一致报 `ReplanRequired`）、计划超时语义、host-key 失败与"需升级 agentd"提示。 |

实时更新（FR-14.2）：`GET /api/v1/events` 的 SSE 事件 = `event: <resource-type>` +
`data: {id, revision}`；客户端 `src/lib/state/refetch.ts` 按 (类型, id) + revision 记账并折叠成
一轮选择性重取（`src/lib/state/store.svelte.ts`）。SSE 不承载资源正文，REST 重取仍是唯一权威数据路径。

> 浏览器 `EventSource` 不能设置请求头，而 SSE 与其余端点同一鉴权模型（§29.14），
> 因此 `src/lib/api/sse.ts` 用 `fetch` + `ReadableStream` 手工解析事件流，从而携带同一个 admin token。

## 契约缺口（本片发现，未自行改契约）

1. **`GET /events` 的路径前缀**：§8.1 全部路径都写在 `/api/v1` 之下，故 §23.6 的 `GET /events`
   实现为 `GET /api/v1/events`。
2. **删除事件与重放**：§23.6 只规定事件含"资源类型/ID + revision"。实现取最小自洽语义：
   删除事件携带"删除前版本 +1"（随后 REST 读取 404）；无 `Last-Event-ID` 重放，客户端重连后全量重取；
   订阅端缓冲溢出时服务端关闭连接（不静默丢事件）。
3. **逐字段 drift diff 未暴露**：REST 只提供判定与两侧投影摘要，没有字段级 diff 端点。
   §9 流 2 的 "Show Diff" 一环因此以**禁用按钮 + 原因标注**呈现（与其余未实现端点同一处理），
   界面不推断"哪些字段变了"。
4. **无全局 operations 端点**：Overview 的"最近失败操作"由逐机 `GET /machines/{name}/operations`
   聚合（并发 6、10 秒内不重复取），规模是 MVP 数量级；若新增全局端点应替换该实现。
5. **Deployment 逐机结果（已闭环）**：`deployment_targets` 是权威存储（§10.1），而 §6.1 规定它以
   `status.targets` 出现在 Deployment 资源里。服务端在读取/写入响应中并入该字段（不改存储模型、
   不开放 status 写入）；`POST /api/v1/deployments` 改走**控制器创建路径**（校验 `machineNames` 与
   可解析的 `targetGeneration`、物化目标行、写初始 status），入口校验失败以 400 `Invalid` /
   409 `RollbackUnsupported` 报出而不落库。
6. **未注册端点**（属第 6 片/后续切片，UI 一律禁用并标注原因，不给假入口）：
   `POST /machines/{name}/ssh/probe|bootstrap|repair-agentd|inventory`、
   `POST /skills/{name}/resolve`、`GET /skills/{name}/revisions`、
   `POST /deployments/{name}/pause|resume`、`GET /machines/{name}/drift` 的字段级 diff，
   以及 Skill 的 spec 形态。
7. **静态资源托管（已闭环）**：控制面进程以 `--spa-dir`（默认 `web/dist`）托管 Web UI
   （§3.6/§4.1）。目录不存在时不注册静态托管、只保留 API（并打日志）——刻意**不用 `go:embed`**，
   否则没有前端产物的干净树上 `go build ./...` 会直接失败。UI 用 hash 路由，深链接不需要 rewrite 回退；
   `/api` 命名空间未匹配时仍是 §6.4 JSON 404（KM-21 既有行为，有测试锁定）。
8. **凭据边界**：UI 不展示也不存储任何 secret（含 `POST /machines/{name}/enroll-token` 的一次性明文 token），
   因此不提供签发入口。

## 核查必改与建议的闭环（KM-25 复核后）

| 项 | 处置 | 锁定方式 |
|---|---|---|
| **M1** 事件版本必须等于落库 `resourceVersion` | `ReplaceSteps` 在同一事务内 bump `operations.resource_version` 并发新版本（此前发的是"当前 rv+1"而库里没变） | `internal/store/sqlite/change_invariant_test.go` 逐路径断言"事件 rv == 落库 rv"（机器/状态/spec/profile/操作 Create·ReplaceSteps·Transition/发布/目标行）；联调第 2 节断言真实后端上事件 rv == REST 读到的 rv |
| **M2** REST 创建发布必须物化目标行 | `POST /api/v1/deployments` 经 `DeploymentAPI.Create` 走控制器创建路径（`resourceAPI.onCreate` 钩子）；入口校验失败 400 `Invalid` / 409 `RollbackUnsupported` | `internal/server/httpapi/stack_test.go`（全栈：真实控制器 + SQLite）：创建 → `status.targets` 非空 → 控制器推进 → 目标 Failed 且发布 reason 不为 `no target succeeded`；联调第 6 节用**真实创建**的发布复现同一链路 |
| **M3** 控制面托管 SPA | `--spa-dir`（默认 `web/dist`）+ catch-all 拆分：`/api` → §6.4 JSON 404，其余 → 静态文件 | `TestSPAServing`（`/` 返回 index.html、`/assets/*.js` 200、`/api/v1/nope` 仍是 JSON 404、目录缺失时不托管） |
| S1 阻塞目标每轮重写同一状态 | 目标行内容无变化时不写库、不 bump、不发事件（`deployment_target_store.Update` 先比后写） | `TestTargetUpdateWithoutChangeIsNoop`（重复 3 次无事件、版本不变；真变化时仍发事件且版本一致） |
| S2 targets 的 N+1 与 `omitempty` | 登记在案：list 逐条并入（MVP 数量级可接受，新增全局端点时应替换）。`DeploymentStatus.Targets` 保留 `omitempty`——**空即缺省**，UI 侧 `?? []` 兜底 | 代码注释 + 本节 |
| S3 联调证据强度 | 第 3/4/5 节改成对可达分支的**显式断言**；不可达分支用 `it.skip` 并在名称里写明原因（in-sync/drifted、未决操作、409 MachineBusy、Superseded/Skipped）；第 6 节改为真实创建发布并断言目标行与推进结果 | `web/tests/integration.test.ts`（7 passed / 4 explicitly skipped） |
| 裁定 3 "Show Diff" | §7 增加禁用入口 + 原因标注（缺字段级 diff 端点） | `web/tests/render.test.ts` + 截图 |

## 未验证清单（不得当作已验证）

- 真实浏览器中的交互路径只做了无头 Chromium 截图核对（页面渲染、SSE 连接状态、三态标签）；
  未做跨浏览器/小屏适配验证。
- `AwaitingConfirmation` / `ReplanRequired` / `MachineBusy` 的**真实后端**触发需要 SSH 路径或在线 agentd
  （第 6 片），本片以单元测试 + 渲染测试覆盖其呈现；联调中这四条分支以 `it.skip` **显式标注为未覆盖**
  （不是"通过"），联调实际覆盖的是 Failed / Unknown / 无未决 分支。
- 发布的 `Superseded` / `Skipped` 目标行同样需要真实推进历史（节点在场 + 出现更新代），联调中以
  `it.skip` 标注；其呈现由渲染测试（`web/tests/render.test.ts`）与 Go 侧 `deployment_targets_test.go` 覆盖。
- 无头浏览器截图中的 `Superseded` / `Skipped` / `Blocked` 目标行为**演示数据**（直接写入本地演示库），
  用于核对呈现层；后端真实产生该状态需要节点在场。
