# SSH-only 通道：操作包（bundle）与 `agentd oneshot`（KM-26）

本文是第 6 片的操作与验证记录，覆盖架构 v1.1.2 §4.5/§7.4/§7.7/§9.3、FR-9.4/FR-12.1–FR-12.8/FR-13.x。

## 1. 交付内容

| 组件 | 位置 | 说明 |
|---|---|---|
| SSH 受限执行器 | `internal/sshtransport/transport.go` | `ssh -G` 解析目标；`ssh -o BatchMode=yes -o ConnectTimeout=N <alias> -- <cmd>`；`scp`；结构化 argv + POSIX 引用（**本地不经 shell**）；§30.1 六类错误分类；**不提供任何放宽 host-key 校验的选项** |
| 远端 staging 与清理判定 | `internal/sshtransport/staging.go` | 固定路径 `<home>/.local/share/agent-fleet/staging/{bundle,bin}`；`DecideCleanup`/`PostRunCleanup` 落实 §4.5 清理规则 1–4 |
| OpenSSH include 导出 | `internal/sshtransport/openssh.go` | 渲染 `~/.ssh/agent-fleet.conf`（preview / export / 显式确认安装）；仅导出 Fleet 持有显式连接字段的机器；拒绝注入；不含私钥内容 |
| 操作包 | `internal/bundle/bundle.go` | 构建 / 校验 / 物化；名称与 digest 形态、路径边界、符号链接拒绝、四项预算；bundle 自身摘要 |
| 控制面编排 | `internal/controller/sshops/orchestrator.go` | 探测、bundle 上传、`oneshot plan/apply`、临时 agentd 二进制上传与 digest 校验、staging 清理、include 渲染 |
| 状态机接线 | `internal/controller/reconcile/ssh.go` | plan → `AwaitingConfirmation`（携 `planDigest`）→ 确认 → apply；失败三分法（Failed / Unknown / 内部错误） |
| 节点侧 oneshot | `cmd/agent-fleet-agentd/oneshot.go` | `oneshot inventory\|plan\|apply`；与 daemon 共用同一 reconciler 与同一把执行权锁（AD-5/§5.6）；结果与 `operationId` 持久化绑定；重复执行幂等 |
| HTTP 端点 | `internal/server/httpapi/ssh.go` | `POST /machines/{name}/ssh/probe`、`POST /machines/{name}/ssh/inventory`、`GET/POST /api/v1/ssh/include` |

## 2. 闭环怎么跑（真实命令）

控制面：

```bash
agent-fleet-server \
  --data-dir ~/.local/share/agent-fleet \
  --agentd-bin-dir ~/.local/share/agent-fleet/agentd \   # 预构建 agentd-<goos>-<goarch>（节点缺 agentd 时上传）
  --skill-artifact-dir ~/.local/share/agent-fleet/artifacts/skills \
  --ssh-client-config ~/.ssh/config                       # 可选：控制面专用 OpenSSH 客户端配置
```

机器（`POST /api/v1/machines`）：

```json
{"metadata":{"name":"node-1"},
 "spec":{"managementMode":"ssh","ssh":{"hostAlias":"node-1"},"profileRef":"default-dev"}}
```

操作序列（每一步都返回 Operation 资源，可轮询 `GET /machines/{id}/operations`）：

```bash
curl -XPOST $API/api/v1/machines/node-1/ssh/probe        # ssh -G + uname/printenv → SSHReachable/lastProbeAt/homeDir
curl -XPOST $API/api/v1/machines/node-1/ssh/inventory    # 只读 AutoPlan（validate→inventory→plan），202 + Operation
curl -XPOST $API/api/v1/machines/node-1/reconcile        # → AwaitingConfirmation，返回 Operation.spec.planDigest
curl -XPOST $API/api/v1/machines/node-1/reconcile \
     -d '{"confirmPlanDigest":"sha256:…"}'               # 确认 → apply（节点重取观测、重算 plan、比对基线）
```

远端实际发生的事（等价的手工命令，便于排障）：

```bash
ssh -G node-1                                            # 解析生效配置（HostName/User/Port/ProxyJump/IdentityFile）
scp -r ./bundle node-1:~/.local/share/agent-fleet/staging/
ssh node-1 -- agent-fleet-agentd oneshot plan  --bundle ~/.local/share/agent-fleet/staging/bundle \
     --operation-id op-… --staging ~/.local/share/agent-fleet/staging --home ~
ssh node-1 -- agent-fleet-agentd oneshot apply --bundle ~/.local/share/agent-fleet/staging/bundle \
     --operation-id op-… --plan-digest sha256:… --staging ~/.local/share/agent-fleet/staging --home ~
```

`oneshot` 的退出码：`0` 产出结果（成功或失败都算，结果 JSON 里有 `reason`）、`2` bundle 校验失败、`3` 执行权不可用（`NodeBusy`/`StaleExecutionLock`）、`1` 其它基础设施错误。stdout 恒为**恰好一份** JSON 文档。

节点侧排障：

```bash
agent-fleet-agentd doctor                  # 平台/适配器/执行权/暂存 报告
agent-fleet-agentd doctor --recover-lock   # 陈旧锁的唯一人工恢复入口（§5.6 规则 5）
```

陈旧锁不会被静默夺取：只有确认节点上没有变更流水线在跑，才由操作者执行 `--recover-lock`（错误消息里也带这条命令）。

## 3. 关键契约

- **bundle 布局**（§7.4）：`manifest.json`（快照 + 工件清单 + 自身 SHA-256）+ `artifacts/skills/<digest>/…`（仅被引用的工件）。
- **校验顺序**：bundle 自身摘要 → manifest/快照一致性 → 逐条名称与 digest 形态 → 路径必须落在 `artifacts/skills/` 内（拒绝绝对路径、`.`/`..` 元素）→ 逐条树摘要与声明一致 → 预算复核。任一条违规**拒绝整包**（不做部分应用）。
- **符号链接**：工件内容中的符号链接一律拒绝（`SkillPathRejected`）。
- **四项预算**（§4.7 设计默认值）：单工件 32 MiB / 解包后 64 MiB / 4096 文件 / 单 bundle 256 MiB；超限在构建期失败（`ArtifactTooLarge`/`BundleTooLarge`），不发生传输。
- **FR-12.7 基线**：`oneshot plan` 把 `(observedProjectionDigest, inventorySeq, planDigest)` 记在 staging；`apply` 重取观测、重算 plan，摘要不一致或 **seq 未前进**（说明没有真正重采）即零变更 → `Failed(ReplanRequired)`。
- **幂等**：`oneshot apply` 对同一 `operationId` 直接回放 `result-<opId>.json`，不启动第二条流水线；daemon 侧由 outbox 承担同一职责。
- **清理**（§4.5）：无未决操作才清理陈旧 staging；`Skipped`/`Unknown` 不清理（节点可能仍在读）；收到节点结果后才清理该次输入；清理失败只告警，不算操作失败。
- **取消**（FR-13.9）：SSH-only 没有 `CancelOperation` 通道，控制面经 SSH 落 `<staging>/cancel-<opId>.json`，节点在阶段边界观察；收到节点结果前服务端只保持 `CancelRequested`。
- **结果不可知**：远端命令已启动但连接中断/超时 → 操作置 `Unknown`（占用互斥、保留输入），**不**判 `Failed`。

## 4. 安全边界（本片明确不做的事）

- 绝不禁用 host-key 校验、绝不注入 `StrictHostKeyChecking=no`（§13.3/FR-12.3）；host-key 失败经 UI/API 以 `HostKeyVerificationFailed` 显式呈现。
- 不支持密码认证、不摄取私钥内容（§13.2/FR-12.2）；include 导出只写路径字符串，从不读私钥。
- 不做 Web SSH 终端、不代理交互式会话（§2.6 非目标）。
- 临时 agentd 二进制与 bundle 均须携带并**在节点侧**校验摘要（无 `sha256sum`/`shasum`/`openssl` 工具时拒绝执行未经校验的二进制）。

## 5. 验证证据

```bash
go build ./... && go vet ./... && go test -race ./...
go test -run 'TestSSH|TestOpenSSH' -v ./internal/controller/sshops/   # 真实本地 sshd 闭环
go test ./internal/bundle/ ./internal/sshtransport/                   # bundle 安全与传输分类
```

核查回归（KM-26 复核 4 项必改，均先用旧提交复现失败、修复后转绿）：
节点侧 agentd 摘要必须逐 token 全等（`digest_test.go`，含 `test -x` 复用分支与
fail-closed）；`Unknown(StaleObservation)` 在任何求值入口都不得被改写成确定判决
（`reconcile/freshness_test.go` + `httpapi` 的端点一致性用例）；节点锁失败映射为
`NodeBusy`/`StaleExecutionLock` 且 stdout 恒为单份 JSON（`oneshot_test.go`）；
陈旧锁经 `doctor --recover-lock` 显式恢复；另有 `inventorySeq` 未前进、
bundle 根/缓存祖先符号链接、scp 方向、连接前分类的远端输出守卫、
include 值含空白/引号等回归用例。

集成测试用真实 OpenSSH：测试内起本地 `sshd`（独立 host key + `known_hosts` 严格校验 + `environment="HOME=…,PATH=…"` 隔离），覆盖
inventory → plan → 确认 → apply 闭环、`MachineBusy`、错误 `planDigest` 被拒、
基线变化 `ReplanRequired`（零变更）、bundle 篡改与被路径逃逸包被拒、
host-key/DNS/连接三类传输错误分类、SSH-only 新鲜度三态（`NeverInventoried` /
`StaleObservation` / `ObservationPredatesDesired`）与 include 导出。

## 6. 已知遗留（不在本片范围）

1. `POST /machines/{name}/ssh/bootstrap` 与 `/ssh/repair-agentd` 未实现（路由未注册 → §6.4 JSON 404）：bootstrap 属 enrollment 编排，repair 需服务/日志诊断路径。
2. ~~`agentd doctor --recover-lock` 未实现~~ → 已实现（`cmd/agent-fleet-agentd/doctor.go`）：`doctor` 输出只读诊断报告（平台/适配器/执行权/暂存），`doctor --recover-lock` 是陈旧锁的显式恢复入口（持有者仍存活时拒绝清理）。剩余缺口是"节点侧自动判定 staging 已清理"，因此陈旧锁仍一律要求人工确认。
3. Deployment 驱动 SSH 机器时本片一律要求操作者确认（"按策略自动确认"的策略尚未定义，§9.3）。
4. 控制面尚无生产者在 gRPC 路径上填充 `ExecuteOperation.baseline/plan_digest`（proto 与节点侧已就绪，SSH 路径已完整实现 FR-12.7）。
5. 决议器 `internal/skills` 尚未落地：bundle 工件来源目前是 `<data-dir>/artifacts/skills/<contentDigest>/` 目录约定，工件树摘要由本片端到端校验，但"控制面解析器内容摘要算法"未参与复核。
6. Web UI（第 5 片 PR #7）本片基线尚未包含，`Machines` 页的 `ssh/probe`、`ssh/inventory` 入口需在 PR #7 合并后从"禁用 + 原因"改为可用。
