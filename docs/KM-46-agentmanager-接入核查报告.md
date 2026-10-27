# KM-46 agentmanager 接入核查报告：固定版本、编译运行证据与能力矩阵

状态：KM-46（T1，阶段 1/5）交付记录。验证时间：2026-10-27。本报告回答一个问题：**agentmanager 能否作为 Codex 生命周期引擎引入**。结论按「OS × 架构 × 来源 × 安装方式」逐组合给出，附证据与复现方法。全部验证在隔离环境（临时 HOME、受控注册表替身、fake npm 可执行）完成，未在真实主机安装或卸载任何软件；真机行为一律标注「未验证」。

## 1. 结论（go/no-go）

| 组合 | 结论 | 一句话依据 |
|---|---|---|
| linux/amd64 + npm | **GO（有条件）** | 固定版本安装/升级/降级、卸载隔离、数据保护零变更全部通过隔离环境运行验证；条件是接入方使用本报告的窄适配层（不得直接用上游 Manager 裸 API），真机验收仍是后续门槛（未验证） |
| darwin/arm64（npm） | **UNVERIFIED（编译通过）** | `GOOS=darwin GOARCH=arm64` 交叉编译通过；行为零验证，无真机证据不得宣称支持 |
| darwin/arm64 + brew | UNVERIFIED | brew provider 源码存在，无任何运行证据 |
| windows/amd64 + npm | **BLOCKED-PENDING-WINDOWS** | 源码级证据：上游 Windows 平台层只认 `.exe`——`npm.cmd`/`codex.cmd` shim 不在视野内；标准 Node for Windows 不自带 `npm.exe`，`NPMProvider.IsAvailable()` 大概率恒 false。需要 Windows 真机确认后另行决策（升级上游、Fleet 自建平台实现、或放弃该组合），**不擅自替换引擎** |
| 任意平台 + binary（native） | **BLOCKED（上游缺陷）** | catalog 的 codex `binary` 条目没有安装 Command，上游 NativeProvider 遇空 Command 直接报错；该条目不可用 |
| 任意平台 + brew 之外的 pip 类 | 不适用 | Codex catalog 无此来源 |

总判定：**可以作为 Codex 生命周期引擎引入（npm 来源、linux/amd64 先行），窄适配层为强制件**。Windows 原生与 macOS 真机是后续阶段的独立验收，不由本单宣称。

## 2. 固定版本与依赖图

- go.mod 落定：`github.com/kevinelliott/agentmanager v1.5.0`（tag `v1.5.0`，修订 `e99ce7a2987791304ae6af2292d79bdf70159837`；`git ls-remote` 复核：tag 对象 `43f5c60e…`，peel 到该 commit；`go.sum` 固定模块哈希）。研究基线 `0a570e46`（2026-09-30）之后上游新增若干提交，本单全部源码核对以 v1.5.0 为准，研究笔记结论在 v1.5.0 全部复核成立（差异见 §4）。
- 实际进入验证二进制的上游包只有 10 个（`pkg/agent`、`pkg/platform`、`pkg/catalog`、`pkg/config`、`pkg/logging`、`pkg/storage`、`pkg/installer`、`pkg/installer/providers`、`pkg/detector`、`pkg/detector/strategies`），编译总包数 267。
- 传递依赖比研究快照更重：`pkg/catalog`（installer 与 detector 的公共依赖）引用 `pkg/config`（viper 全家桶：afero/cast/pflag/mapstructure/fsnotify/conc/locafero 等）与 `pkg/storage`（mattn/go-sqlite3）。影响评估：
  - 纯编译期负担，我们的代码路径不调用 storage/config；
  - `go-sqlite3` 在 `CGO_ENABLED=0` 下编译为空壳，交叉编译不受阻（已验证三平台）；**交叉编译必须显式 `CGO_ENABLED=0`**，Linux 原生 cgo 构建也已验证可过；
  - go.mod 新增 11 个间接 require，许可证全部为 MIT / Apache-2.0 / BSD 类（逐模块 LICENSE 已核对，无 copyleft）。
- 依赖图关键边（`go mod graph` 摘要）：`agent-fleet → agentmanager v1.5.0 → {viper v1.21.0, x/sys v0.45.0→0.48.0(MVS), sqlite3 v1.14.44, x/sync v0.20.0→0.23.0(MVS), yaml v3}`；上游 TUI/托盘相关依赖（bubbletea/systray/darwinkit 等）在其 go.mod 的 require 列表里，但**未进入**我们的构建（`go list -deps` 无一项）。

## 3. 交付物与复现方法

| 交付物 | 位置 |
|---|---|
| 窄适配层（接口草案 + npm 实现 + 严格探测 + 快照对比） | `internal/agentlocal/lifecycle/`（`engine.go` 接口、`npm.go` 实现、`snapshot.go` 数据快照、`evidence.go` 取证探针） |
| 隔离验证环境（fake npm 替身、受控注册表、`~/.codex` 数据替身） | `internal/agentlocal/lifecycle/testenv/` |
| 最小验证程序（C0–C7，退出码 0 = 全过） | `cmd/agentmanager-verify/main.go` |
| 单元/集成测试 | `internal/agentlocal/lifecycle/*_test.go`（12 个测试，`go test -race` 全绿） |

复现：

```sh
go test -race ./internal/agentlocal/lifecycle/...   # 验收项 3/4/5 的自动化断言
go run ./cmd/agentmanager-verify                    # 全场景运行证据，逐项 PASS/FAIL
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/agentmanager-verify
GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build -o /dev/null ./cmd/agentmanager-verify
```

**证据边界声明**：§4 的「运行证据」在隔离环境内、针对 fake npm 替身取得（替身只覆盖上游实际调用的 npm 子命令，语义对齐 npm 公开文档行为，源码见 `testenv/fakenpm.go`）。它证明的是**上游库在给定命令语义下的行为**（固定版本缝隙、零值陷阱、吞错、数据保护），上游源码级核对（§4 各条引用）与替身运行相互印证；真实 npm 与真机（Windows 原生/macOS/Linux 实装）行为不由此宣称，一律标注「未验证」，真机验收是后续阶段门槛。

最小验证程序本次运行完整输出（linux/amd64，退出码 0）：

```text
[KM-46] agentmanager 接入核查 · 最小验证程序
pinned dependency: github.com/kevinelliott/agentmanager v1.5.0
[PASS] C0 上游 DetectAll 吞错陷阱取证（npm 彻底失败时）
       DetectAll=(空 inventory, nil error)；引擎不使用 DetectAll 做判定；副作用：pip 检测策略执行了宿主 uv，$HOME/.cache/uv 新增 7 项（宿主相关，非生命周期操作）
[PASS] C1 未安装探测：空环境不得报已安装
       installed=false, version=""
[PASS] C2 固定版本安装 0.44.0 + 独立核验
       verified=0.44.0 exe="<isolated-prefix>/bin/codex" upstream-stdout=0B
[PASS] C2b 上游 Result.Version 零值陷阱（带版本 spec 安装后恒为零值）
       upstream Result.Version=0 (zero), strict detect=1.0.0 → 零值不得当成功，引擎独立核验成立
[PASS] C3 固定版本升级 0.44.0 → 0.45.0
       verified=0.45.0
[PASS] C3b 固定版本降级 0.45.0 → 0.44.0（latest=0.45.0 时仍落在 0.44.0）
       verified=0.44.0（registry latest=0.45.0）→ 固定版本语义成立
[PASS] C4 上游 Update 漂移取证：catalog 原生 update_cmd 无法固定目标
       pinned=0.44.0 在装，Update 后实测 0.45.0 → Update 不可用于固定版本升级（引擎已改走 Install 入口）
[PASS] C5 卸载目标实例；同前缀其他包与第二前缀同实例不受影响
       目标消失；other-agent 保留；第二前缀 codex@0.44.0 原样
[PASS] C6 探测失败必须报错且不得谎报已安装
       npm 彻底失败时 Detect 返回错误、不产出任何状态
[PASS] C7 数据保护：全程 ~/.codex（与整个 HOME）零变更
       全 HOME 快照 20 项，前后一致（文件集合/内容/权限/软链）

SUMMARY: 0 check(s) failed
RESULT: 隔离环境全部通过（GO 倾向）。真机（Windows 原生/macOS/Linux 实装）仍属未验证，范围见核查报告。
```

## 4. 关键上游行为核查（v1.5.0，源码级 + 运行级双重证据）

研究笔记（`docs/superpowers/specs/2026-09-30-upstream-reuse-research.md`）的六项已知缺口在 v1.5.0 逐条复核，结论不变并补充运行证据：

1. **Manager 没有目标版本参数**（`pkg/installer/installer.go`：`Install/Update/Uninstall/GetLatestVersion` 均不含版本）。运行证据：C4——catalog 原生 `update_cmd`（`npm update -g <pkg>`）驱动下，已装 0.44.0、意图固定 0.44.0，Update 后实测 **0.45.0**（漂到注册表 latest）。
2. **固定版本的窄适配可行**：上游 npm Install 原样执行 `method.Package`，把 `@openai/codex@0.44.0` 放入该字段即得精确安装；升级/降级都走同一入口（npm 带版本全局安装会替换既有版本）。运行证据：C2/C3/C3b——0.44.0 安装、0.44.0→0.45.0 升级、latest=0.45.0 时强制降回 0.44.0，独立核验全部命中请求版本。
3. **零值版本陷阱确认并规避**：带版本 spec 安装后，上游用 `npm list -g <pkg@ver>` 读版本，命中串 `pkg@ver@` 不会出现 → `Result.Version` 恒为零值且无错误（C2b 实证）。窄适配层**不采信上游 Result.Version**，每次操作后用 detector 重新发现并独立核验；`ParseVersion` 对垃圾输入返回 `{Raw: 输入}, nil`（非零、非错误），因此核验同时要求版本串满足精确 semver 形态，双闸（运行证据：空版本与 `not-a-version` 场景均被拒）。
4. **Detector.DetectAll 吞错确认**：策略错误被收集后丢弃，恒返回 `nil` error——npm 彻底失败时返回「空 inventory + 无错误」，无法与「未安装」区分（C0 实证）。窄适配层改用可传播错误的 `Detector.DetectByMethod`（仅 npm 策略），失败路径显式报错（C6 实证）。
5. **DetectAll 还有副作用**：它会执行宿主上存在的 pip/pipx/uv/brew 等其他包管理器（本机 uv 被调用并在 HOME 写出 `.cache/uv`，C0 INFO 记录）。这是研究笔记未覆盖的新发现，进一步支持「引擎只走 npm-only 的 DetectByMethod」；已按本单全局约束登记 §7 阻塞清单。T4 若要全量 inventory，必须为每种来源单独声明并隔离其副作用。
6. **Windows 平台层 `.exe` 硬编码确认**（`pkg/platform/windows.go`：`FindExecutable`/`FindExecutables`/`IsExecutableInPath` 一律追加 `.exe`）：npm 全局安装的 `codex.cmd`/`codex.ps1` shim 不可见；`npm` 本身在标准 Node for Windows 安装中是 `npm.cmd`，无 `npm.exe` → `NPMProvider.IsAvailable()` 大概率恒 false → Manager 对 npm 方法直接报「npm is not available」。真机验证前，该组合按 BLOCKED 处理。附带影响：即便探测修复，上游 NativeProvider 卸载回退是 `os.Remove(ExecutablePath)`，对 `.cmd` shim 同样失效。

**数据保护**（验收项 4）：`~/.codex` 替身含 config.toml（0644）、auth.json（0600 凭据替身）、sessions 历史、AGENTS.md、指向外部共享目录的相对软链与悬空软链；安装/升级/卸载全程后整 HOME 快照（20 项）逐项对比文件集合/内容/权限/链接**零变更**（C7）；卸载后目标实例消失，同前缀其他包与第二前缀同名实例原样保留（C5）。快照对比器自身用「内容/权限/删除三连篡改必须被抓到」自证非恒真（`dataprotection_test.go`）。上游 npm provider 构造的命令只涉及程序本体（npm 前缀内），未发现触碰用户数据的路径；唯一发现的 HOME 副作用来自 DetectAll 的 pip 策略间接调用宿主 uv（§4.5），已从引擎路径排除。

## 5. 能力矩阵（逐组合证据）

| OS | 架构 | 来源/方式 | 上游 Manager 路径 | 固定版本 | 数据保护 | 编译 | 运行 | 判定 |
|---|---|---|---|---|---|---|---|---|
| linux | amd64 | npm | `npm install -g <pkg@ver>`（窄适配） | ✅ 隔离环境验证（C2/C3/C3b） | ✅ 零变更（C7） | ✅ 原生 | ✅ 最小验证程序 | **GO（有条件）** |
| linux | arm64 | npm | 同上 | 理论同上 | 理论同上 | ✅ 交叉编译 | ❌ 未验证 | UNVERIFIED |
| darwin | arm64 | npm | 同上 | 理论同上 | 理论同上 | ✅ 交叉编译 | ❌ 未验证 | UNVERIFIED（真机验收另做） |
| darwin | arm64/amd64 | brew | brew provider | ❌ 无版本参数（brew 无窄适配缝） | 未审计 | ✅ 编译 | ❌ 未验证 | UNVERIFIED，不建议进入 T4 首批 |
| windows | amd64 | npm | `IsAvailable()` 依赖 `npm.exe` 探测 | 理论可行（Install 缝隙同源码） | 理论同上 | ✅ 交叉编译 | ❌ 源码级判定大概率不可用 | **BLOCKED-PENDING-WINDOWS** |
| windows | amd64 | native/scoop/chocolatey/winget | native provider | 取决于 catalog 命令 | ⚠️ native 卸载回退 `os.Remove` | ✅ 编译 | ❌ 未验证 | 不适用（catalog 无 codex 条目） |
| 任意 | — | binary | Native Install 空 Command 即失败 | ❌ | — | — | — | **BLOCKED（上游缺陷）** |

「理论同上」= 同一代码路径、无平台分支，但按规格未验证不标支持。

## 6. 窄适配层接口草案（供 T4 对齐）

`internal/agentlocal/lifecycle/engine.go`（完整定义以代码为准）：

```go
type Engine interface {
    Detect(ctx, Request) (Detected, error)          // 未安装≠错误；探测失败/零值版本=错误
    InstallExact(ctx, Request) (Outcome, error)     // 固定版本安装，操作后独立核验
    UpgradeToExact(ctx, Request) (Outcome, error)   // 不委托上游 Update（C4 证据）
    Uninstall(ctx, UninstallRequest) (Outcome, error) // 卸载后核验实例确已消失
}
```

T4 必须遵守的四条引擎纪律（均已内置于实现并有测试锁定）：

1. 版本只在创建操作时固定（`Request.Version` 必须是精确 `x.y.z`，range/latest/`v` 前缀一律拒绝，`engine_test.go::TestInstallExactRejectsRange`）。
2. 上游 `Result.Version` 不采信；每个操作后用 `DetectByMethod`（npm-only）重新发现并要求实测版本等于请求版本，不等即整体失败。
3. 探测失败（命令失败/空版本/不可解析版本）返回错误，绝不降级为「未安装」或零值成功。
4. 用户数据保护由调用方用 `lifecycle.SnapshotDir/Diff` 在操作前后对比 HOME；引擎自身不触碰 HOME 下任何路径（有测试锁定：探测不触发宿主其他包管理器）。

超时与取消：引擎对每个操作施加 `DefaultOpTimeout`（10 分钟）上限，沿 context 传播（上游无内建 deadline）。

## 7. 阻塞与未验证清单

| 项 | 类型 | 说明 / 建议去向 |
|---|---|---|
| windows/amd64 npm 探测（`npm.exe`） | 阻塞（上游） | 需 Windows 真机实测确认；若坐实，向上游提 issue/补丁或由 Fleet 自建 Windows 平台实现，属引擎替换级决策，另行评审 |
| `codex.cmd` shim 可见性 | 阻塞（上游，同源） | 与上条同根：`.exe` 硬编码 |
| binary 安装条目空 Command 失败 | 阻塞（上游） | catalog 条目不可用；如需 binary 通道须 Fleet 自行实现下载/校验（本单不授权） |
| brew 通道行为与数据保护 | 未验证 | macOS 真机验收时一并做；brew 无固定版本缝隙，需先解决版本固定策略 |
| linux/darwin 真机全流程（发现→安装 V1→固定升级 V2→停止管理→卸载） | 未验证 | 规格「Testing Decisions」明确容器与替身不能替代真机；T4/后续阶段执行 |
| catalog `update_cmd` 原生路径漂移 | 已证实，已规避 | 引擎不使用 Update；若上游未来给 Manager 加版本参数可重评 |
| DetectAll 间接执行宿主包管理器并触碰 HOME | **阻塞证据（探测侧）** | 上游 DetectAll 的 pip 策略在宿主装有 uv/pip/pipx 时调用它们，本机实测 uv 被执行并向 `$HOME` 写 `.cache/uv`（C0 运行记录）。按本单全局约束如实记录为触碰用户数据的上游行为证据：T4 不得用 DetectAll 做生命周期判定（引擎已用 npm-only DetectByMethod 规避）；若 T4/T2 需要全量 inventory，必须逐来源声明并隔离副作用，无法隔离的组合判阻塞 |
| repo 全仓 `GOOS=windows go build ./...` | 既有问题（非本单引入） | `internal/sshtransport`、`internal/agentlocal/reconciler` 使用 `syscall.Kill`/`Setpgid`（Linux 专属）；与本单交付物无关，生命周期包与验证程序三平台编译独立通过 |

## 8. 对父单 KM-42 的回报要点

- 阶段 1 的判定问题已有答案：**npm 来源 × linux/amd64 组合 GO（有条件）**，条件（窄适配层纪律）已代码化并有测试锁定；阶段 2–5 可以在此接缝上继续。
- Windows 原生与 macOS 的真机验收是后续阶段的硬门槛，能力矩阵已把「未验证」与「阻塞」分开标注，不把省略当支持。
- 新发现（DetectAll 副作用、ParseVersion 非严格、零值陷阱的运行级复现）已回写本报告 §4，供 T2/T3（版本发现/期望状态）避坑。
