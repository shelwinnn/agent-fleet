# 部署实机演练记录（KM-37）

本文是 agent-fleet 在运行时机器上的一次**真实 loopback 全链路演练**留档：控制面启动 → 建机器 → enroll（mTLS）→ inventory 上报 → 期望状态下发 → 适配器物化 → 观测/drift → 删除机器（删除即拒证）。所有命令与输出均摘自演练现场记录，未验证的步骤在[§ 未验证项](#未验证项)如实登记。

- 演练日期：2026-09-29
- 代码基线：`main` `0e394b4`（批次一 + 批次二共 7 家族适配器合入后的 HEAD）
- 环境：Arch Linux（内核 7.1.5-arch1-2，x86_64）；go1.27.0；node v24.15.0；pnpm 12.3.4
- 演练拓扑：**单机 loopback**——控制面与节点 agentd 同机，gRPC 走 `127.0.0.1:7789`（TLS，证书 SAN 覆盖 localhost/loopback），HTTP 走 `127.0.0.1:7788`
- 适配器家族：`codex`（本机实装 codex-cli 0.154.0，与适配器 verifiedVersions 一致）；节点受管 home 为**注入的演练目录**，不触碰真实 `~/.codex`

结论先行：**全链路一次走通，无阻塞缺陷**。一个演练观察（删除后旧连接流的日志噪音）登记为产品改进候选，见[§ 演练发现](#演练中发现的产品问题另行开单)。

## 前置条件

- Go ≥ go.mod 声明版本（本机 go1.27.0）
- Web UI 需要 Node 22+ 与 pnpm 12（仅在需要控制面托管 SPA 时构建）
- 演练目录约定（下文所有命令以 `DRILL` 为根）：

```console
$ export DRILL=$HOME/agent-fleet-drill
$ mkdir -p $DRILL/server $DRILL/node $DRILL/logs
```

## 步骤 0：构建

```console
$ go build -o bin/agent-fleet-server ./cmd/agent-fleet-server
$ go build -o bin/agent-fleet-agentd ./cmd/agent-fleet-agentd

$ cd web && pnpm install --frozen-lockfile && pnpm build && cd ..
$ ls web/dist
assets  favicon.svg  icons.svg  index.html
```

`web/dist` 由控制面托管（`--spa-dir`，默认即 `web/dist`）；目录不存在时 server 只提供 API，不报错。

## 步骤 1：启动控制面

```console
$ ./bin/agent-fleet-server \
    --addr 127.0.0.1:7788 \
    --grpc-addr 127.0.0.1:7789 \
    --data-dir $DRILL/server \
    --db $DRILL/server/fleet.db \
    --spa-dir web/dist
```

server 日志（演练现场摘录）：

```json
{"msg":"fleet ca ready","pki_dir":"…/server/pki"}
{"msg":"migration applied","version":"0004_operation_observations"}
{"msg":"agent grpc listening","addr":"127.0.0.1:7789","tls":true,"heartbeat_interval":"15s","inventory_interval":"5m0s","offline_after":"45s"}
{"msg":"spa static serving enabled","dir":"…/web/dist"}
{"msg":"agent-fleet-server listening","addr":"127.0.0.1:7788","db":"…/server/fleet.db"}
```

观测：首次启动自动生成 Fleet CA（`pki/{ca.crt, ca.key, server.crt, server.key}`，私钥 0600）、跑完 migration、mTLS gRPC 与 HTTP 双监听就绪。

## 步骤 2：健康检查与 SPA 托管

```console
$ curl -s -w '\n%{http_code}\n' 127.0.0.1:7788/healthz
{"status":"ok"}
200
$ curl -s -w '\n%{http_code}\n' 127.0.0.1:7788/readyz
{"status":"ready"}
200
$ curl -s -o /dev/null -w '%{http_code}\n' 127.0.0.1:7788/
200
```

`GET /` 返回 200 即 SPA index.html 已由控制面托管；浏览器内的 UI 操作未在本次演练范围（见未验证项）。

## 步骤 3：创建第一台机器

```console
$ curl -s -w '\n%{http_code}\n' -X POST 127.0.0.1:7788/api/v1/machines \
    -H 'Content-Type: application/json' \
    -d '{"metadata":{"name":"drill-01"},"spec":{"managementMode":"agentd"}}'
{"metadata":{"name":"drill-01","uid":"f2189e67-…","resourceVersion":1,"creationTimestamp":"2026-09-29T02:54:00.08Z"},"spec":{"managementMode":"agentd"},"status":{}}
201
```

## 步骤 4：节点侧 enroll（无 token，先出指纹）

先从控制面数据目录复制 Fleet CA 证书给节点（校验服务端证书用，FR-11.4）：

```console
$ cp $DRILL/server/pki/ca.crt $DRILL/node/ca.crt
$ ./bin/agent-fleet-agentd enroll \
    --server 127.0.0.1:7789 --machine drill-01 \
    --ca $DRILL/node/ca.crt --data-dir $DRILL/node
machine_id: drill-01
csr: …/node/pki/client.csr
public key fingerprint: sha256:f24df5505ca18dd66933b0a5e6a251df7481c53ef9af06570a079e8c4c84c10e
next: POST /api/v1/machines/drill-01/enroll-token with body {"csrPubKeySha256": "sha256:f24df55…"}
then put the token into agentd.yaml (token:) or pass --token and re-run enroll.
```

观测：本地生成私钥（0600）与 CSR，打印公钥指纹后退出；此时还没有任何网络身份。

## 步骤 5：控制面签发一次性 enroll token

```console
$ curl -s -w '\n%{http_code}\n' -X POST 127.0.0.1:7788/api/v1/machines/drill-01/enroll-token \
    -H 'Content-Type: application/json' \
    -d '{"csrPubKeySha256": "sha256:f24df5505ca18dd66933b0a5e6a251df7481c53ef9af06570a079e8c4c84c10e"}'
{"machineId":"drill-01","token":"oAfX…aUou","expiresAt":"2026-09-29T03:04:09.47Z","note":"one-time token; shown only once and stored only as a hash"}
201
```

token 明文只在这次响应出现一次（服务端只存哈希），默认 10 分钟有效；下文以 `oAfX…aUou` 指代。

## 步骤 6：带 token 完成 enroll（mTLS 落证）

```console
$ ./bin/agent-fleet-agentd enroll \
    --server 127.0.0.1:7789 --machine drill-01 \
    --ca $DRILL/node/ca.crt --data-dir $DRILL/node \
    --token 'oAfX…aUou'
{"level":"INFO","msg":"enrolled; client certificate stored (0600)","machine_id":"drill-01","cert_path":"…/node/pki/client.crt","not_after_unix":1798426453}

$ ls -l $DRILL/node/pki/
-rw------- client.crt
-rw-r--r-- client.csr
-rw------- client.key
```

观测：客户端证书 90 天有效期，落盘 0600；token 即用即弃。

## 步骤 7：启动 daemon（mTLS 长连接 + 心跳 + inventory）

```console
$ ./bin/agent-fleet-agentd daemon \
    --server 127.0.0.1:7789 --machine drill-01 \
    --ca $DRILL/node/ca.crt --data-dir $DRILL/node \
    --home $DRILL/node/home
{"msg":"agentd daemon starting","server":"127.0.0.1:7789","machine_id":"drill-01","version":"0.2.0","home":"…/node/home"}
{"msg":"certificate renewal scheduled","not_after":"2026-12-28T02:54:13Z","renew_in":"2159h49m0s"}
```

约 1 分钟后控制面侧机器状态（节选）：

```json
"status": {
  "conditions": [
    {"type": "AgentConnected", "status": "True", "reason": "Connected", "message": "agentd connect stream established"},
    {"type": "InventoryReady", "status": "True", "reason": "InventoryReceived", "message": "first valid inventory received"}
  ],
  "os": "linux", "arch": "amd64", "hostname": "MindStone",
  "agentdVersion": "0.2.0",
  "lastHeartbeatAt": "2026-09-29T02:55:27.76Z",
  "lastInventoryAt": "2026-09-29T02:54:57.76Z",
  "inventorySeq": 1,
  "adapterCapabilities": [ … claude/codex/dsh/grok/hermes/omp/opencode 逐家族逐能力声明 … ]
}
```

观测：mTLS Connect 建立，能力协商回传 7 家族逐能力声明（supported / unsupported / unverified 如实上报），全量 inventory 入库。

## 步骤 8：定义最小期望状态（profile）并绑定机器

```console
$ curl -s -w '\n%{http_code}\n' -X POST 127.0.0.1:7788/api/v1/profiles \
    -H 'Content-Type: application/json' \
    -d '{"metadata":{"name":"drill-min"},"spec":{"agents":{"codex":{"enabled":true,"version":"0.154.0","config":{"model":"gpt-5.1"}}}}}'
{…"name":"drill-min"…}
201

$ curl -s -w '\n%{http_code}\n' -X PUT 127.0.0.1:7788/api/v1/machines/drill-01 \
    -H 'Content-Type: application/json' \
    -d '{"metadata":{"name":"drill-01"},"spec":{"managementMode":"agentd","profileRef":"drill-min"}}'
{…}
200

# 渲染预览（五要素 → 期望状态 + 确定性摘要）
$ curl -s '127.0.0.1:7788/api/v1/profiles/drill-min/render?machine=drill-01'
{"canonicalizationVersion":"snapshot-json-v1",
 "desired":{"schemaVersion":"fixture/v1","agents":{"codex":{"version":"0.154.0","config":{"model":"gpt-5.1"}}}},
 "digest":"sha256:43b0cb6fc696b5abdbb649f7a3d0481aafef5226031258e72dfab64910cdf6a1",
 "machine":"drill-01","profile":"drill-min"}
```

渲染要点：version 必须钉死（floating 引用被拒绝），schema 版本随 server `--adapter-schema-version`（默认 `fixture/v1`）。

## 步骤 9：触发收敛（reconcile → 派发 → apply → verify）

```console
$ curl -s -w '\n%{http_code}\n' -X POST 127.0.0.1:7788/api/v1/machines/drill-01/reconcile \
    -H 'Content-Type: application/json' -d '{}'
{"metadata":{"name":"82614a81-…"},"spec":{"machine":"drill-01","type":"Reconcile","transport":"agentd","desiredGeneration":1},"status":{"phase":"Pending"}}
202
```

数秒后查询操作记录（终态）：

```json
{
  "metadata": {"name": "82614a81-20ec-4d42-95ca-b1c7068eb97d"},
  "spec": {"machine": "drill-01", "type": "Reconcile", "transport": "agentd", "desiredGeneration": 1},
  "status": {
    "phase": "Succeeded",
    "finishedAt": "2026-09-29T02:55:38Z",
    "verify": {
      "desiredProjectionDigest": "sha256:736e6c16…",
      "observedProjectionDigest": "sha256:736e6c16…",
      "canonicalizationVersion": "agentlocal-projection-v1",
      "inventorySeq": 3,
      "adapterHealth": "passed"
    }
  }
}
```

server 日志（同一操作的下行与回灌，间隔约 80ms）：

```json
{"msg":"operation created","event":"operation_created","operation_id":"82614a81-…","machine_id":"drill-01","type":"Reconcile","desired_generation":1}
{"msg":"inventory received","machine_id":"drill-01","inventory_seq":3,"full":true,"os":"linux","arch":"amd64","operation_id":"82614a81-…"}
{"msg":"operation finished","event":"operation_result","operation_id":"82614a81-…","machine_id":"drill-01","phase":"Succeeded","desired_generation":1,"current_generation":1}
```

**行为说明（照做前需要知道的语义）**：`managementMode: agentd` 的通道是**一次派发直达终态**——控制面把快照经 gRPC `ExecuteOperation` 下发，节点串行执行 plan → apply → verify 并回灌观测与终态；`planDigest`/`AwaitingConfirmation` 的"人确认计划"流程属于 **SSH-only 通道**（§9.3，`POST /reconcile` 带 `{"confirmPlanDigest": …}`）。agentd 通道的安全表达是控制面互斥（409 MachineBusy）+ 节点执行权锁 + apply 前基线重算。

## 步骤 10：节点侧物化证据

```console
$ cat $DRILL/node/home/.codex/config.toml
model = "gpt-5.1"

$ cat $DRILL/node/home/.local/share/agent-fleet/backups/82614a81-…/manifest.json
{
  "operationId": "82614a81-20ec-4d42-95ca-b1c7068eb97d",
  "files": [
    {"path": ".codex/config.toml", "absent": true},
    {"path": ".codex/AGENTS.md", "absent": true}
  ]
}
```

观测：受管文件写入前节点先做回滚备份（记录"此前不存在"）；`config.toml` 只含受管键（本例 `model`）。演练用的 `--home` 是注入目录，真实 `~/.codex` 未被触碰。

（观察记录：空 home 下 inventory 的版本探测会在 `$DRILL/node/home` 产生厂商 CLI 的副作用文件，如 `.hermes/SOUL.md`——属探测对象自身的初始化行为，非 Fleet 写入，登记于未验证项一节的附注。）

## 步骤 11：drift 三态验证

收敛后的一致态：

```json
$ curl -s 127.0.0.1:7788/api/v1/machines/drill-01/drift
{"drifted": {"status": "False"}, "reconciled": {"status": "True",
  "message": "operation for current generation converged and observation proves consistency"},
 "desiredProjectionDigest": "sha256:736e6c16…", "observedProjectionDigest": "sha256:736e6c16…",
 "inventorySeq": 3}
```

人为篡改受管键（模拟漂移），重启 daemon 触发全量 inventory（周期 inventory 默认 5 分钟，演练不等）：

```console
$ echo 'model = "hand-edited-drift"' > $DRILL/node/home/.codex/config.toml
$ # 重启 daemon（Ctrl-C 后重跑步骤 7 命令）
```

漂移被观测到（两侧投影摘要分叉）：

```json
{"drifted": {"status": "True", "message": "observed managed projection differs from desired"},
 "reconciled": {"status": "False", "message": "managed state drifted"},
 "desiredProjectionDigest": "sha256:736e6c16…", "observedProjectionDigest": "sha256:b9a00dff…",
 "inventorySeq": 4}
```

再次 `POST /reconcile` 自愈：

```console
$ curl -s -X POST 127.0.0.1:7788/api/v1/machines/drill-01/reconcile -H 'Content-Type: application/json' -d '{}'
$ sleep 4 && curl -s 127.0.0.1:7788/api/v1/machines/drill-01/drift | …
drifted: False | reconciled: True | inventorySeq: 6
$ cat $DRILL/node/home/.codex/config.toml
model = "gpt-5.1"
```

## 步骤 12：删除机器 = 拒绝其证书

```console
$ curl -s -w '%{http_code}\n' -X DELETE 127.0.0.1:7788/api/v1/machines/drill-01 -o /dev/null
204
$ curl -s 127.0.0.1:7788/api/v1/machines
{"items":[]}
```

server 日志即刻记录证书退役：

```json
{"msg":"machine deleted; certificates retired","machine_id":"drill-01","event":"machine_deleted_certificates_retired"}
```

删除后、daemon 重连前的旧流上，心跳已被拒（资源不存在）：

```json
{"level":"ERROR","msg":"machine status update failed","op":"on_heartbeat","machine_id":"drill-01","err":"resource not found: machine \"drill-01\""}
```

重启 daemon 做全新 Connect，**已退役证书被明确拒绝**（节点侧只见流被对端关闭，按退避重试）：

```json
// server
{"level":"WARN","msg":"agent rpc rejected: machine unknown or deleted","method":"/fleet.v1.FleetAgentService/Connect","event":"agent_rpc_rejected","reason_code":"EnrollmentRejected","machine_id":"drill-01","cert_serial":"fc7a03fad0e85bc6a5dc6d211b94b920"}
// agentd
{"level":"WARN","msg":"connect stream ended; reconnecting","err":"EOF"}
```

结论：无证书吊销（既有裁决）下，"删除机器即拒证"成立——机器行删除即退役其证书，retired 证书无法再建立 Connect，日志携带 cert serial 可审计。

## 步骤 13：诊断与优雅停机

```console
$ ./bin/agent-fleet-agentd doctor --data-dir $DRILL/node --home $DRILL/node/home
agent-fleet-agentd 0.2.0
home        : …/node/home
data dir    : …/node
adapters    : [claude codex dsh grok hermes omp opencode]
exec-lock   : free (…/node/state/execution.lock)
hint        : stale lock recovery = `agent-fleet-agentd doctor --recover-lock` (confirm no change pipeline is running first)
```

两侧均响应 SIGTERM 优雅退出（daemon 日志 `agentd daemon stopped`；server 日志 `shutting down` / `offline scanner stopped`），端口释放。

## 常用 flag 速查

server（`agent-fleet-server`）：

| flag | 默认 | 说明 |
|---|---|---|
| `--addr` | `127.0.0.1:7788` | HTTP API + SPA 监听地址；**绑非回环地址必须配 `--admin-token-env`**（§29.14，否则拒绝启动） |
| `--grpc-addr` | `0.0.0.0:7789` | agent gRPC 监听（强制 TLS/mTLS） |
| `--data-dir` | `~/.local/share/agent-fleet` | 控制面数据目录（pki、generated、artifacts） |
| `--db` | `<data-dir>/fleet.db` | SQLite 库文件 |
| `--spa-dir` | `web/dist` | Web UI 静态产物目录；不存在则只提供 API |
| `--admin-token-env` | 空 | 存放 admin token 的**环境变量名**；非回环监听必配，`/api/v1` 全部要求 `Authorization: Bearer <token>` |
| `--ssh-binary` / `--scp-binary` | `ssh` / `scp` | SSH-only 通道用的系统 OpenSSH 客户端路径（FR-12.1） |
| `--ssh-client-config` | 空 | 传给 ssh/scp 的 `-F` 客户端配置 |
| `--token-ttl` | 10m | enroll token 有效期 |
| `--client-cert-validity` | 90d | 签发的客户端证书有效期 |
| `--heartbeat-interval` / `--inventory-interval` | 15s / 5m | 随 Welcome 下发的上报周期 |
| `--offline-after` | 45s | 心跳超时置离线阈值 |
| `--plan-timeout` | 30m | SSH 通道计划确认窗口 |
| `--freshness-window` | 15m | 观测新鲜度阈值（超窗 drift 转 Unknown） |
| `--adapter-schema-version` | `fixture/v1` | 适配器 schema 版本（渲染输入五要素之一） |

agentd（`agent-fleet-agentd`，daemon/enroll 共用）：

| flag | 默认 | 说明 |
|---|---|---|
| `--config` | `~/.config/agent-fleet/agentd.yaml` | 引导配置（server / machine_id / ca_cert / token 等，flag 覆盖文件） |
| `--server` | 配置文件 | 控制面 gRPC 地址 host:port |
| `--machine` | 配置文件 | Machine 名称 |
| `--ca` | 配置文件 | Fleet CA 证书路径（校验服务端证书） |
| `--data-dir` | `~/.local/share/agent-fleet` | 节点数据目录（pki、state） |
| `--token` | 空 | 仅 enroll 用；不写日志 |
| `--home`（daemon） | `$HOME` | 受管内容解析根目录（护栏 #12；演练/测试可注入） |

## 未验证项

以下内容**本次演练未覆盖**，照做前请自行评估，不得视为已验证：

1. **Web UI 浏览器内操作**：仅验证 server 托管 SPA（`GET /` 200）；路由/事件流/交互未做浏览器点选验证（自动化联调测试需 `FLEET_INTEGRATION=1`，见 `web/README.md`）。
2. **非回环部署与 admin token**：未演练 `--addr 0.0.0.0` + `--admin-token-env` 的真实外网/局域网部署与鉴权行为。
3. **SSH-only 通道全链路**：oneshot plan/apply、`confirmPlanDigest` 确认流、include 导出、agentd 临时二进制上传——需要真实 SSH 目标机，本演练用 agentd 通道覆盖等价收敛语义。
4. **Skills / MCP / rules 物化**：演练 profile 只含 codex `config.model`；技能软链、MCP 渲染、rules 受管块未在实机过链路。
5. **多机机群与 Deployment 编排**：单机 loopback；批量发布、跳过、回滚未演练。
6. **证书续期**：90 天证书的续期窗口（renew loop 已调度，见步骤 7 日志）未自然到期验证。
7. **CA 备份/恢复演练**：`docs/manual-ca-backup-recovery.md` 的 DR-1～DR-8 未执行。
8. **跨平台**：macOS / WSL 未测（本机 Linux amd64）。
9. **systemd 模板实机加载**：`deploy/*.service` 为随本次交付的模板，按模板字段与本演练命令一致编写，但未在本机 `systemctl` 实际加载运行（演练环境为用户会话）。

附注：空受管 home 下，inventory 的版本探测会触发部分厂商 CLI 的初始化副作用（如 `.hermes/SOUL.md`、`.hermes/.update_check` 出现在注入 home）——这是被探测对象自身的初始化行为，不是 Fleet 写入；真实工作站上这些文件早已存在，不影响受管键所有权边界。另外 server 日志里 `inventory_interval` 显示 5m0s，首连后约 38 秒即收到第二次全量 inventory（`inventorySeq` 1→3），与"连接即全量上报"的语义一致。

## 演练中发现的产品问题（另行开单）

- **删除机器后旧 Connect 流未强制断开**：DELETE 成功且证书退役后，先前已建立的 gRPC 流仍在，节点心跳每 15s 触发一对 `machine status update failed … resource not found` ERROR 日志，直到节点侧重连被拒为止。不影响"删除即拒证"的正确性，但产生持续错误日志噪音、且节点在旧流上误以为仍在线。建议：删除钩子里强制关闭该机的活跃流（CloseStream），或将该错误降级为一次性事件。KM-37 边界不改产品代码，登记为后续独立工单。
