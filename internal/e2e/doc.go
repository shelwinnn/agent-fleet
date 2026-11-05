// Package e2e 是 Codex 生命周期纵向闭环的端到端链路测试（KM-51，T6）。
//
// 测试边界（docs/superpowers/specs/2026-10-27-codex-lifecycle-spec.md，Testing
// Decisions 第一条）：**浏览器发起操作 → 各目标实际结果**的完整链路。断言用户
// 可观察的状态与结果（REST 响应即 Lifecycle.svelte 页面渲染的数据来源），不绑定
// 内部实现细节；配受控版本源（testenv）与故障注入。
//
// 链路每一环都是真实实现，只有两处按仓库既定隔离纪律使用替身：
//
//   - 控制面：真实 SQLite（迁移后）+ 真实 gRPC mTLS 端点 + 真实 lifecycle
//     编排控制器 + 真实 T3 上游发现服务（CommandQuerier 经 PATH 命中 fake npm）
//   - 真实 httpapi REST（与 cmd/agent-fleet-server 同形装配）；
//   - 节点：真实 enroll/mTLS Connect 流 + 真实 NodeExecutor + 真实 NpmEngine
//   - fake npm 替身与临时 HOME（KM-46 C0–C7 同款隔离环境）；
//   - CapabilityPolicy 注入 allowPolicy 替身（隔离验证配置）——生产装配固定
//     NewRegistryPolicy，T8 真机登记 supported 之前一律拒绝执行；本包证明的是
//     "准入放行后"的链路语义，与 KM-49 执行引擎测试同一纪律；
//   - 节点侧队列/确认语义按 cmd/agent-fleet-agentd/lifecycle.go 的 RunWorker
//     同构实现为最小泵（Recv → Execute → Send → Ack）；outbox 重发等可靠上行
//     细节由该包自身测试覆盖，不在链路级重复。
//
// 全部用例只在 linux 上运行（fake npm 是 POSIX sh 替身；Windows 原生场景
// 保持未验证，见 KM-46 报告）；真机验收属 T8（KM-53），本包不冒充。
//
// 与 T5 页面的保真度说明：链路按 T2/T4 落地的 REST 契约驱动（创建 → 确认 →
// 轮询），confirm 是预览确认对话框的后端契约端点；已交付的 Lifecycle.svelte
// 页面目前尚未接线 confirm（页面创建的操作将停在未确认、不会被派发）。该
// 集成缝隙不在 T6 修复范围，已在 KM-51 回报登记，T5/T7 侧跟进。
package e2e
