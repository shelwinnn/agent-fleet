# 上游能力复用研究笔记

研究时间：2026-09-30T16:18:04+08:00，由 `date -Iseconds` 获取。

状态：源码与官方文档研究；不是已批准的集成设计，也不是三平台实测报告。

## 用户已确认的约束

- agent 安装、升级、卸载只操作程序本体，保留配置、凭据、会话、历史记录等用户数据。例如更新 Codex 不动 `~/.codex`。
- skill 生命周期优先复用 `runkids/skillshare`，优先 import。
- agent 生命周期优先复用 `kevinelliott/agentmanager`，优先 import。
- 离线处理沿用纠偏文档建议。

agent 的数据保护约束适用于 agent 生命周期操作；skill 安装和卸载本身需要改变明确选定的 skill 文件或链接，必须单独限定操作范围，不能把整个 agent 数据目录交给清理操作。

## 固定研究基线

通过 GitHub 官方 API 读取当次仓库 HEAD 与完整文件树，后续源码读取使用固定 commit 的 raw URL。每次 HTTP 请求设置 20 秒超时，本次未重试。

| 项目 | 本次研究 commit | Go module | 最低声明 Go 版本 | 许可证 |
|---|---|---|---|---|
| skillshare | `c6f96f98658ef881c0830e161f791a581de4825c` | `skillshare` | `1.25.5` | MIT |
| agentmanager | `0a570e464afc627d4f14725fe4e0b070b1003fd9` | `github.com/kevinelliott/agentmanager` | `1.25.0` | MIT |

依据：[skillshare go.mod](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/go.mod)、[skillshare LICENSE](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/LICENSE)、[agentmanager go.mod](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/go.mod)、[agentmanager LICENSE](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/LICENSE)。源码复用和分发时保留相应版权与许可声明；没有在本轮检查全部传递依赖的许可证。

这些 commit 是可复核的研究快照，不代表选定生产依赖版本，也不证明后续上游版本仍有相同行为。

## skillshare：生命周期能力存在，直接 import 有结构障碍

### 已核查事实

1. 核心安装逻辑及类型位于 `internal/install`，包括 `Install`、`InstallFromDiscovery`、`InstallTrackedRepo`、`InstallOptions`；CLI 入口位于 `cmd/skillshare`。完整文件树未发现 `pkg/` 公共库。[源码](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/internal/install/install.go)
2. `go.mod` 使用 `module skillshare`；不能按常规 GitHub 模块路径直接获得一个可供外部项目 import 的生命周期 SDK。即使使用 `replace` 定位源码，`internal` 可见性规则仍是独立障碍。[go.mod](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/go.mod)、[Go 官方 internal 规则](https://pkg.go.dev/cmd/go#hdr-Internal_Directories)
3. 官方 `check` 文档描述更新检查及 JSON 输出：区分远程更新、本地来源、上游已删除的 skill；更新依据主要是 Git revision，不应一律展示为软件发行版号。[check 文档](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/website/docs/reference/commands/check.md)
4. `update` 与目标分发是两步操作，更新源内容后需要 `sync`；项目模式有锁文件支持。[update 文档](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/website/docs/reference/commands/update.md)
5. 卸载逻辑把选定的源内容移入 trash，然后更新元数据；目标清理依赖后续 sync。源码允许部分失败时退出成功，接入方必须读取逐项结果，不能只看 exit code。[卸载源码](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/cmd/skillshare/uninstall_handlers.go)、[卸载文档](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/website/docs/reference/commands/uninstall.md)
6. 发布配置列出 `linux`、`darwin`、`windows`，架构 `amd64`、`arm64`；Windows 发布 zip。[发布配置](https://github.com/runkids/skillshare/blob/c6f96f98658ef881c0830e161f791a581de4825c/.goreleaser.yaml)

### 接入建议与限制

- **优先 import 是选型偏好，但当前上游核心不具备外部直接 import 条件。** 推荐先确认是否能由上游提取公共包；本轮没有联系维护者。
- 如果需要立即集成，可比较“小型维护分支提取公共 facade”和“固定版本 CLI + JSON”两种路径。前者增加维护成本，后者偏离进程内 import 偏好。二者都是待评审候选，本轮不替用户决定。
- 不建议修改 agent-fleet 的 module 身份来绕过 `internal` 规则，也不建议重写完整 skill 管理器。
- 控制面负责选主机、确定 revision 和任务状态，节点侧复用 skillshare 负责本地来源与目标操作。跨主机固定同一 commit 的流程、只影响选定 agent 的 target 配置、与现有 skill 缓存/链接机制的职责边界，均需设计和实测。
- 文档中的 `check` 描述不能直接等同“绝无本地写入”：例如 Git fetch 会改变 Git 元数据。上线前需要核查并约束实际写入范围。

## agentmanager：公开 import 路径存在，需要能力筛选

### 已核查事实

1. 有公开 `pkg/installer`、`pkg/installer/providers`、`pkg/detector`、`pkg/catalog`、`pkg/agent`、`pkg/platform` 包。`installer.NewManager` 接受 `platform.Platform`，提供 `Install`、`Update`、`Uninstall`、`GetLatestVersion`；操作使用 `context.Context`。[Manager 源码](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/installer.go)
2. Manager API 没有独立目标版本参数。NPM 安装取 `method.Package`，升级固定执行 `npm update -g <package>`，不能直接认为支持已经解析的精确目标版本升级。[NPM provider](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/providers/npm.go)
3. NPM 卸载执行 `npm uninstall -g <package>`；该 provider 源码没有删除 `~/.codex` 的逻辑。这只证明 provider 构造的命令，不证明所有软件包生命周期脚本或外部安装器都不碰用户数据。[NPM provider](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/providers/npm.go)
4. Native provider 卸载优先执行 catalog 的 `UninstallCmd`；没有命令时删除 `ExecutablePath`。它没有内建“保护 agent 数据目录”的策略，不能无条件把整个 catalog 开放给用户。[Native provider](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/providers/native.go)
5. Manager 最新版本查询支持 npm、pip 类和 brew；对 native、curl、binary 等明确返回不支持版本检查。这不证明上游其他高层流程没有更新信息来源，但该公开入口无法完整覆盖所有方法。[Manager 源码](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/installer.go)
6. Codex catalog 标记 npm 支持 macOS、Linux、Windows，brew 支持 macOS；binary 方法仅有下载和平台工件 metadata，没有安装 Command，而 Native Install 在空 Command 时失败。因此“catalog 有 binary 条目”不能证明公开 Manager 已能安装该条目。[catalog](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/catalog/catalog.json)、[Native provider](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/providers/native.go)
7. Windows 有原生平台实现，使用 PowerShell/cmd；其 `FindExecutable`、`IsExecutableInPath` 会为无后缀名称追加 `.exe`，npm 和 Codex 的 `.cmd` shim 是否识别正确是待实测风险。[Windows 平台源码](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/platform/windows.go)
8. NPM 与 Native 的安装后版本读取错误会转成零值；`Detector.DetectAll` 收集后丢弃策略错误并返回 nil error。接入方必须额外验证或使用能传播错误的入口，不能将空 inventory 或零版本直接视为成功。[NPM provider](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/providers/npm.go)、[Native provider](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/installer/providers/native.go)、[Detector](https://github.com/kevinelliott/agentmanager/blob/0a570e464afc627d4f14725fe4e0b070b1003fd9/pkg/detector/detector.go)

### 接入建议与限制

- 优先从公开包 import，保留 agent-fleet 原有控制面、节点连接、任务审计和离线机制，只增加本地操作的窄适配层。
- 精确版本控制需要补充验证：考虑借用安装入口部署指定包版本，但目前没有编译/运行证明此组合可用，也不能把 `Update` 原样当成精确版本接口。
- 每种 agent × 安装方法 × 操作 × 平台分别声明能力。未经验证的组合显示不支持或具体阻塞原因，不因 catalog 条目存在而启用。
- 数据保护必须核查实际命令、安装方式和路径；不能只依靠上游 Uninstall 返回 nil。安装、升级和卸载均应在隔离测试账户下验证用户数据目录保持不变。
- 父 context 应设 deadline；对外部命令的超时后状态、幂等重试和安装后精确版本检测由 agent-fleet 负责。包中没有独立 deadline，不应传入无限期后台 context。
- 只 import 生命周期相关包不等同启动上游 TUI、托盘或后台服务；但平台包及传递依赖的三平台编译情况仍需实际检查。

## 进入实现前应解决的验证事项

1. skillshare 复用方式：公共库提取、维护分支、CLI 候选的明确决定。优先 import 不能被静默替换成 CLI。
2. 固定依赖版本，检查 Go 工具链、依赖图和三平台编译；本轮未执行 `go get` 或构建。
3. 在 Windows 原生验证 npm/Codex shim 查找、进程执行与取消；在 macOS/Linux 验证实际安装方法。
4. 用同一精确版本进行多机安装与升级，检查结果与请求版本完全一致。
5. 在数据目录放置配置、凭据测试替身和历史文件，记录操作前后文件集合、内容、权限及链接；agent 生命周期不能改变这些数据。
6. skill 验证 install → sync、check、固定 revision 更新 → sync、uninstall → sync，保留非受管 skill 和其他配置。
7. 故障验证：版本检测失败、部分 skill 失败、离线、进程超时和重复任务，不得把零值或部分成功汇总为全部成功。

## 结论

已验证：两者有可复用能力；agentmanager 提供公开生命周期包，skillshare 核心受 Go internal 规则限制。两者的三平台声明都不足以替代真实设备验收。

建议：需求文档将“复用指定项目、优先 import”写为明确约束，将上述阻塞与验证事项放在第一阶段；完成复用决策和小范围验证后再写完整实现计划。继续独立开发通用安装器或 skill 管理器不符合本次用户确认的方向。
