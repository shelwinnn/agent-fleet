# agent-fleet

面向 Linux、macOS 和 WSL 的单操作者 Agent 工作站机群管理器：一个控制面（机器台账、期望状态、reconcile、观测与 drift、Web UI）加每台机器一个节点代理（agentd，mTLS 长连接），把 Codex / Claude / Grok / DeepSeek Harness / Hermes / OMP / OpenCode 七个 Agent CLI 家族的配置纳入受管收敛。SSH-only 通道（oneshot plan/apply、OpenSSH include 导出）作为无 daemon 场景的补充路径。

当前状态：MVP 已实现并完成一次实机部署演练——[批次一]（Codex / OMP / OpenCode）与[批次二]（Grok / Claude / Hermes / dsh）适配器全部合入 `main`，发布 tag `v0.1.0`。架构基准是[中文架构 v1.1.2](docs/agent-fleet-architecture-v1.1.2.md)，从[文档索引](docs/README.md)进入；最近一次实机全链路演练（含真实命令与输出、未验证清单）见[部署演练记录](docs/deployment-drill.md)。

## 快速上手

以下步骤与[部署演练记录](docs/deployment-drill.md)逐条对应，照做能跑通（演练日期 2026-09-29，单机 loopback）。前置：Go ≥ go.mod 声明版本；Web UI 需要 Node 22+ 与 pnpm 12。

### 1. 构建

```console
$ go build -o bin/agent-fleet-server ./cmd/agent-fleet-server
$ go build -o bin/agent-fleet-agentd ./cmd/agent-fleet-agentd
```

### 2. 构建 Web UI（可选，控制面托管）

```console
$ cd web && pnpm install --frozen-lockfile && pnpm build && cd ..
```

产出 `web/dist`。server 默认按 `--spa-dir web/dist` 托管它；目录不存在时只提供 API。

### 3. 启动控制面

```console
$ ./bin/agent-fleet-server \
    --addr 127.0.0.1:7788 \
    --grpc-addr 127.0.0.1:7789 \
    --data-dir ./fleet-data
```

首次启动自动生成 Fleet CA（`<data-dir>/pki/`，私钥 0600）、跑 migration，然后监听 HTTP `127.0.0.1:7788`（默认仅回环）与 agent gRPC `0.0.0.0:7789`（强制 TLS/mTLS）。验证：

```console
$ curl -s 127.0.0.1:7788/healthz
{"status":"ok"}
```

**绑非回环地址必须配 admin token**，否则拒绝启动：`--admin-token-env FLEET_ADMIN_TOKEN`（值是环境变量**名**），此后 `/api/v1` 全部要求 `Authorization: Bearer <token>`。

### 4. 注册第一台机器

控制面建台账 → 节点出指纹 → 控制面签发一次性 token → 节点落证：

```console
# ① 建机器
$ curl -s -X POST 127.0.0.1:7788/api/v1/machines \
    -H 'Content-Type: application/json' \
    -d '{"metadata":{"name":"ws-1"},"spec":{"managementMode":"agentd"}}'

# ② 节点首次 enroll：本地生成私钥(0600)+CSR，打印公钥指纹后退出
$ cp ./fleet-data/pki/ca.crt ~/ws-1-ca.crt        # 节点要用它校验服务端证书
$ ./bin/agent-fleet-agentd enroll \
    --server 127.0.0.1:7789 --machine ws-1 \
    --ca ~/ws-1-ca.crt --data-dir ~/ws-1-agent
machine_id: ws-1
csr: …/pki/client.csr
public key fingerprint: sha256:…
next: POST /api/v1/machines/ws-1/enroll-token with body {"csrPubKeySha256": "sha256:…"}

# ③ 用上一步打印的指纹签发一次性 token（明文只出现一次，默认 10 分钟有效）
$ curl -s -X POST 127.0.0.1:7788/api/v1/machines/ws-1/enroll-token \
    -H 'Content-Type: application/json' \
    -d '{"csrPubKeySha256": "sha256:<上一步的指纹>"}'
{"machineId":"ws-1","token":"…","expiresAt":"…","note":"one-time token; shown only once and stored only as a hash"}

# ④ 带 token 完成 enroll：客户端证书落盘 0600
$ ./bin/agent-fleet-agentd enroll \
    --server 127.0.0.1:7789 --machine ws-1 \
    --ca ~/ws-1-ca.crt --data-dir ~/ws-1-agent \
    --token '<③返回的token>'
```

### 5. 启动节点代理

```console
$ ./bin/agent-fleet-agentd daemon \
    --server 127.0.0.1:7789 --machine ws-1 \
    --ca ~/ws-1-ca.crt --data-dir ~/ws-1-agent
```

连接建立后，控制面侧 `GET /api/v1/machines/ws-1` 的 status 出现 `AgentConnected=True` 与 `InventoryReady=True`，并携带 7 家族逐能力声明（supported / unsupported / unverified 如实上报）。节点诊断：`./bin/agent-fleet-agentd doctor`。

### 6. 下发期望状态并收敛

profile 定义期望的 Agent 形态（version 必须钉死），绑到机器后触发 reconcile：

```console
# ① 定义 profile（本例管 codex 的 model 键；家族按需换）
$ curl -s -X POST 127.0.0.1:7788/api/v1/profiles \
    -H 'Content-Type: application/json' \
    -d '{"metadata":{"name":"min"},"spec":{"agents":{"codex":{"enabled":true,"version":"0.154.0","config":{"model":"gpt-5.1"}}}}}'

# ② 绑定到机器
$ curl -s -X PUT 127.0.0.1:7788/api/v1/machines/ws-1 \
    -H 'Content-Type: application/json' \
    -d '{"metadata":{"name":"ws-1"},"spec":{"managementMode":"agentd","profileRef":"min"}}'

# ③ 触发收敛；agentd 通道一次派发直达终态（节点 plan→apply→verify 并回灌证据）
$ curl -s -X POST 127.0.0.1:7788/api/v1/machines/ws-1/reconcile \
    -H 'Content-Type: application/json' -d '{}'

# ④ 查看终态与验证证据
$ curl -s 127.0.0.1:7788/api/v1/machines/ws-1/operations
```

节点受管文件在写入前自动备份到 `<data-dir>/state`（回滚用），之后可随时比对：`GET /api/v1/machines/ws-1/drift` 返回 drift 三态（一致 / 未知或过期 / 漂移）与两侧投影摘要。SSH-only 通道的语义不同：`POST /reconcile` 先产出计划并停在 `AwaitingConfirmation`，必须再 `POST /reconcile` 带 `{"confirmPlanDigest": "<planDigest>"}` 才会 apply（§9.3）。

### 7. 注销机器（删除即拒证）

```console
$ curl -s -X DELETE 127.0.0.1:7788/api/v1/machines/ws-1 -o /dev/null -w '%{http_code}\n'
204
```

MVP 不做证书吊销（既有裁决）：**删除机器即退役其证书**——该机的已签发证书无法再建立 mTLS 连接，server 日志记录 `machine deleted; certificates retired` 与后续拒绝事件（带 cert serial）。UI 与 API 标注「当前限制：无证书吊销」。

### 常用 flag 速查

server：

| flag | 默认 | 说明 |
|---|---|---|
| `--addr` | `127.0.0.1:7788` | HTTP + SPA 监听；非回环必须配 `--admin-token-env` |
| `--grpc-addr` | `0.0.0.0:7789` | agent gRPC（强制 TLS/mTLS） |
| `--data-dir` / `--db` | `~/.local/share/agent-fleet` / `<data-dir>/fleet.db` | 数据目录 / SQLite |
| `--spa-dir` | `web/dist` | Web UI 静态产物；不存在则只提供 API |
| `--admin-token-env` | 空 | admin token 的环境变量**名**；`/api/v1` 要求 Bearer |
| `--ssh-binary` / `--scp-binary` / `--ssh-client-config` | `ssh` / `scp` / 空 | SSH-only 通道的系统 OpenSSH（FR-12.1） |
| `--token-ttl` / `--client-cert-validity` | 10m / 90d | enroll token 与客户端证书有效期 |
| `--heartbeat-interval` / `--inventory-interval` / `--offline-after` | 15s / 5m / 45s | 上报与离线判定 |
| `--freshness-window` | 15m | 观测超窗则 drift 转 Unknown |
| `--adapter-schema-version` | `fixture/v1` | 渲染五要素之一 |

agentd：

| flag | 默认 | 说明 |
|---|---|---|
| `--config` | `~/.config/agent-fleet/agentd.yaml` | 引导配置；命令行 flag 覆盖文件值 |
| `--server` / `--machine` / `--ca` / `--data-dir` | 配置文件 | gRPC 地址 / 机器名 / CA 证书路径 / 节点数据目录 |
| `--token` | 空 | 仅 enroll；不写日志 |
| `--home`（daemon） | `$HOME` | 受管内容解析根目录（护栏 #12） |

### 进程守护（systemd 模板）

`deploy/agent-fleet-server.service` 与 `deploy/agent-fleet-agentd.service` 是按上述默认端口与路径编写的模板，安装位置与启动前检查见文件内注释。模板未经 systemctl 实机加载验证（见演练记录未验证项 9）。

## 文档

- [文档索引](docs/README.md)——需求、架构（当前 v1.1.2）、评审与决策记录
- [部署演练记录](docs/deployment-drill.md)——实机全链路演练：真实命令与输出、drift 与拒证验证、未验证清单
- [Agent 支持矩阵](docs/agent-support-matrix.md)、[批次一](docs/adapters-batch-1.md)/[批次二](docs/adapters-batch-2.md)适配器交付记录
- [Web UI](docs/web-ui.md)、[SSH-only oneshot](docs/ssh-only-oneshot.md)、[人工 CA 备份恢复](docs/manual-ca-backup-recovery.md)

## 已知限制

- **无证书吊销**（既有裁决）：删除机器即拒证，无 CRL/OCSP。
- **KM-30**：已知间歇性测试失败，裁决「暂不修」，不影响主链路。
- 版本探测只探测不安装/升级：各家族 `Apply(version)` 在版本不匹配时显式失败（安装/升级不在 MVP 范围）。
- 工件物化（FetchArtifact/bundle）未实现：引用技能的 profile 在节点缓存缺失时显式失败，不静默跳过。

## 开发

```console
$ go test -race ./...        # Go 全量（CI 门禁含 gofmt / vet / build）
$ cd web && pnpm test        # Web UI 类型检查 + 单测 + 构建同为 CI 门禁
```

License: 见 [LICENSE](LICENSE)。
