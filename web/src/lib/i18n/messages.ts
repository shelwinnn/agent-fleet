/**
 * 集中管理的消息资源（zh-CN 为源语言，en 为目标语言）。
 *
 * 设计约束：
 * - 两个目录在类型层面强制同键集（en 声明为 Record<MessageKey, Message>），
 *   缺译在编译期报错，而不是运行时露出裸 key（spec User Story 20）。
 * - 消息值是 `string | (params) => string`：简单文案保持声明式；
 *   需要复数、条件或数字本地化的文案用函数（配合 Intl.PluralRules /
 *   Intl.NumberFormat），不引入运行时 ICU 解析器，也不自造 key 约定。
 * - 本模块是纯数据 + 纯函数：不依赖 Svelte runes、不做 IO、不读时钟；
 *   响应式语言状态在 locale.svelte.ts，时间/时长格式化在 state/format.ts。
 * - 品牌（agent-fleet）、版本、用户数据、地址与操作标识保持原值，
 *   不进入消息目录；后端 reason code 未识别时保留原码（见 state 模块）。
 */

export type Locale = 'zh-CN' | 'en';

export const LOCALES: Locale[] = ['zh-CN', 'en'];

/** 源语言：缺失翻译时回退到这里，绝不允许裸 key 上界面。 */
export const SOURCE_LOCALE: Locale = 'zh-CN';

export type MessageParams = Record<string, string | number>;
export type Message = string | ((params: MessageParams) => string);

/** en 复数取词（CLDR one/other；zh 无复数形态，直接用 other）。 */
export function selectPlural(locale: Locale, n: number, one: string, other: string): string {
  return new Intl.PluralRules(locale).select(n) === 'one' ? one : other;
}

/** 数字按 locale 格式化（分组符等）。 */
export function formatNumber(value: number, locale: Locale): string {
  return new Intl.NumberFormat(locale).format(value);
}

/**
 * 纯翻译解析：当前语言缺 key → 回退源语言；两端都没有 → 返回 key 本身
 * （类型化的调用方使该分支不可达，仅作运行时兜底，测试锁死该契约）。
 */
export function translate(locale: Locale, key: string, params?: MessageParams): string {
  const value = catalogs[locale][key] ?? catalogs[SOURCE_LOCALE][key];
  if (value === undefined) return key;
  return typeof value === 'function' ? value(params ?? {}) : value;
}

const zhCN = {
  // ---- 应用外壳 ----
  'app.title': 'agent-fleet 控制面',
  'app.subtitle': '单操作者机群管理',
  'app.stream.open': '实时事件流已连接',
  'app.stream.connecting': '实时事件流连接中',
  'app.stream.reconnecting': '实时事件流重连中',
  'app.stream.closed': '实时事件流已关闭',
  'app.lastEventAt': (p) => `最近事件 ${p.time} · 已处理 ${p.count} 条`,
  'app.eventsCoalesced': (p) => `（去重 ${p.count}）`,
  'app.noEvents': '尚未收到事件',
  'app.lastRefresh': (p) => `数据刷新于 ${p.time}`,
  'app.refresh': '全量刷新',
  'app.refreshing': '刷新中…',
  'app.manageToken': '管理令牌',
  'app.tokenLabel': '管理令牌（仅本次会话内存，不写入任何存储）',
  'app.tokenPlaceholder': '回环监听且服务端未配置时可留空',
  'app.applyToken': '应用并重连事件流',
  'app.limitNotice': '当前限制：无证书吊销（删除机器即拒绝其证书）。',
  'app.unknownRoute': '未知路由',
  'app.backToOverview': '返回总览',
  'app.language': '切换语言',
  'app.listSeparator': '；',

  // ---- 导航 ----
  'nav.overview': '总览',
  'nav.machines': '机器',
  'nav.profiles': '配置档',
  'nav.skills': '技能',
  'nav.deployments': '发布',
  'nav.sshInventory': 'SSH 清单',

  // ---- Overview 页 ----
  'overview.title': '总览',
  'overview.subtitle': '数据保持自动更新：变更事件只刷新受影响的表格。',
  'overview.card.totalMachines': '机器总数',
  'overview.card.daemonOnline': 'daemon 在线',
  'overview.card.sshReachable': 'SSH 可达',
  'overview.card.drifted': '已漂移',
  'overview.card.degraded': '降级',
  'overview.card.activeDeployments': '活跃发布',
  'overview.card.driftHintMore': (p) => `另有 ${p.n} 台未知或过期`,
  'overview.attention.title': '需要关注的机器',
  'overview.attention.empty': '没有需要关注的机器。',
  'overview.deployments.title': '最近发布',
  'overview.deployments.empty': '暂无发布。',
  'overview.ops.title': '最近失败/带修饰的操作',
  'overview.ops.subtitle': '操作按机器分别记录，此处汇总最近的失败与结果被改写的操作。',
  'overview.ops.empty': '没有失败或带修饰的操作。',
  'overview.table.name': '机器名',
  'overview.table.drift': '漂移',
  'overview.table.reconciled': '收敛',
  'overview.table.agentd': 'agentd',
  'overview.table.lastSeen': '最近上报',
  'overview.table.reason': '原因',
  'overview.table.phase': '阶段',
  'overview.table.progress': '进度',
  'overview.table.unusualTargets': '目标异常态',
  'overview.table.operation': '操作',
  'overview.table.machine': '机器',
  'overview.table.typeSource': '类型 · 来源',
  'overview.table.phaseOp': '相位',
  'overview.table.modifier': '修饰',
  'overview.table.time': '时间',
  'overview.progress': (p) => `${p.done}/${p.total} 成功（${p.pct}%）`,
  'overview.badge.rollback': '回滚型',
  'overview.unusualTarget': (p) =>
    p.reason ? `${p.machine}：${p.phase}（${p.reason}）` : `${p.machine}：${p.phase}`,

  // ---- 机器状态（machines.ts / drift.ts 共用） ----
  'machine.agentd.online': (p) => (p.version ? `在线 ${p.version}` : '在线'),
  'machine.agentd.offline': '离线',
  'machine.agentd.unknown': '未知',
  'machine.reconciled.ok': '已收敛',
  'machine.reconciled.no': '未收敛',
  'machine.reconciled.unknown': '未知',
  'machine.attention.drifted': '已漂移',
  'machine.attention.driftUnknown': (p) => `drift 未知：${p.code}`,
  'machine.attention.agentdOffline': 'agentd 未在线',
  'machine.attention.inventoryNotReady': 'inventory 未就绪',

  // ---- drift 三态 ----
  'drift.label.inSync': '已确认一致',
  'drift.label.unknown': '未知或过期',
  'drift.label.drifted': '已漂移',
  'drift.detail.sameDigest': '受管投影摘要相同但判定为漂移，请查看后端评测结果',
  'drift.detail.digestMismatch': '受管投影摘要与期望不一致（逐字段 diff 未由 API 暴露，可通过 Reconcile 收敛）',
  'drift.detail.lastConfirmedAt': (p) => `上次确认于 ${p.time}`,
  'drift.detail.lastConfirmedMissing': '上次确认时间未记录',
  'drift.detail.lastObservedAt': (p) => `最近观测于 ${p.time}`,
  'drift.detail.sshModeExpected': 'SSH-only 机器在两次操作之间不做后台轮询，因此本态属预期',
  'drift.reason.NeverInventoried': '从未采集受管投影（该机尚未完成 inventory）',
  'drift.reason.StaleObservation': '观测已过期（超过新鲜度窗口）',
  'drift.reason.ObservationPredatesDesired': '期望代已变化，尚未按新代采集',
  'drift.reason.ProjectionVersionMismatch': '投影规范化版本不一致，不沿用旧判定',
  'drift.reason.unreported': '原因未报告',

  // ---- Deployment 目标与相位 ----
  'target.phase.Pending': '待推进',
  'target.phase.Running': '执行中',
  'target.phase.Succeeded': '成功',
  'target.phase.Failed': '失败',
  'target.phase.Superseded': '已被取代',
  'target.phase.Skipped': '已跳过',
  'target.phase.Blocked': '被阻塞',
  'target.reason.SupersededByNewerGeneration':
    '该机已在更新代：本次发布的目标代已过期，不再重放。这不是失败。',
  'target.reason.BlockedByNodeLock': '节点执行权忙：排队等待，不计入失败也不推进批次。',
  'target.reason.unknown': (p) => `后端原因码：${p.code}`,
  'target.skipped.noReason': '操作者显式跳过：留审计、不计入成功，且不改变节点实际状态（节点可能仍在写）。',
  'target.superseded.noReason': '被更新代取代（未附原因码）',
  'deploy.phase.Pending': '待开始',
  'deploy.phase.Canary': '金丝雀',
  'deploy.phase.RollingOut': '滚动中',
  'deploy.phase.Paused': '已暂停',
  'deploy.phase.Succeeded': '成功',
  'deploy.phase.Failed': '失败',
  'deploy.phase.none': '未开始',

  // ---- 操作相位与类型 ----
  'ops.phase.Pending': '待派发',
  'ops.phase.Running': '执行中',
  'ops.phase.AwaitingConfirmation': '等待确认',
  'ops.phase.CancelRequested': '已请求取消',
  'ops.phase.Succeeded': '成功',
  'ops.phase.Failed': '失败',
  'ops.phase.Unknown': '结果未知',
  'ops.type.Reconcile': '收敛',
  'ops.type.Repair': '修复 agentd',
  'ops.type.Bootstrap': '纳管',
  'ops.type.Rollback': '回滚',
  'ops.type.AutoPlan': '自动 plan',
  'ops.source.automatic': '自动',
  'ops.source.manual': '手动',

  // ---- 时间与时长 ----
  'time.justNow': '刚刚',
  'time.minutesAgo': (p) => `${p.n} 分钟前`,
  'time.hoursAgo': (p) => `${p.n} 小时前`,
  'time.daysAgo': (p) => `${p.n} 天前`,
  'time.inFuture': (p) => `${p.duration}后`,
  'time.duration.expired': '已到期',
  'time.duration.seconds': (p) => `${p.n} 秒`,
  'time.duration.minutes': (p) => `${p.m} 分 ${p.s} 秒`,
  'time.duration.hours': (p) => `${p.h} 小时 ${p.m} 分`,
} as const satisfies Record<string, Message>;

export type MessageKey = keyof typeof zhCN & string;

/** en 目录：Record<MessageKey, Message> 使缺译/多译都在编译期暴露。 */
const en: Record<MessageKey, Message> = {
  // ---- App shell ----
  'app.title': 'agent-fleet console',
  'app.subtitle': 'Fleet management for a single operator',
  'app.stream.open': 'Realtime stream connected',
  'app.stream.connecting': 'Realtime stream connecting',
  'app.stream.reconnecting': 'Realtime stream reconnecting',
  'app.stream.closed': 'Realtime stream closed',
  'app.lastEventAt': (p) =>
    `Last event ${p.time} · ${p.count} ${selectPlural('en', Number(p.count), 'event', 'events')} processed`,
  'app.eventsCoalesced': (p) => ` (${p.count} coalesced)`,
  'app.noEvents': 'No events received yet',
  'app.lastRefresh': (p) => `Data refreshed ${p.time}`,
  'app.refresh': 'Refresh all',
  'app.refreshing': 'Refreshing…',
  'app.manageToken': 'Admin token',
  'app.tokenLabel': 'Admin token (kept in memory for this session only, never written to storage)',
  'app.tokenPlaceholder': 'Can be left empty on loopback when the server has none configured',
  'app.applyToken': 'Apply and reconnect',
  'app.limitNotice': 'Current limitation: no certificate revocation (deleting a machine rejects its certificate).',
  'app.unknownRoute': 'Unknown route',
  'app.backToOverview': 'Back to Overview',
  'app.language': 'Switch language',
  'app.listSeparator': '; ',

  // ---- Navigation ----
  'nav.overview': 'Overview',
  'nav.machines': 'Machines',
  'nav.profiles': 'Profiles',
  'nav.skills': 'Skills',
  'nav.deployments': 'Deployments',
  'nav.sshInventory': 'SSH Inventory',

  // ---- Overview page ----
  'overview.title': 'Overview',
  'overview.subtitle': 'Data stays current automatically: change events refresh only the tables they affect.',
  'overview.card.totalMachines': 'Total machines',
  'overview.card.daemonOnline': 'daemon online',
  'overview.card.sshReachable': 'SSH reachable',
  'overview.card.drifted': 'Drifted',
  'overview.card.degraded': 'Degraded',
  'overview.card.activeDeployments': 'Active rollouts',
  'overview.card.driftHintMore': (p) => `${p.n} more unknown or stale`,
  'overview.attention.title': 'Machines needing attention',
  'overview.attention.empty': 'No machines need attention.',
  'overview.deployments.title': 'Recent rollouts',
  'overview.deployments.empty': 'No rollouts yet.',
  'overview.ops.title': 'Recent failed/modified operations',
  'overview.ops.subtitle':
    'Operations are recorded per machine; this list aggregates recent failures and result-modified operations.',
  'overview.ops.empty': 'No failed or modified operations.',
  'overview.table.name': 'Machine',
  'overview.table.drift': 'Drift',
  'overview.table.reconciled': 'Reconciled',
  'overview.table.agentd': 'agentd',
  'overview.table.lastSeen': 'Last seen',
  'overview.table.reason': 'Reason',
  'overview.table.phase': 'Phase',
  'overview.table.progress': 'Progress',
  'overview.table.unusualTargets': 'Unusual targets',
  'overview.table.operation': 'Operation',
  'overview.table.machine': 'Machine',
  'overview.table.typeSource': 'Type · source',
  'overview.table.phaseOp': 'Phase',
  'overview.table.modifier': 'Modifier',
  'overview.table.time': 'Time',
  'overview.progress': (p) => `${p.done}/${p.total} succeeded (${p.pct}%)`,
  'overview.badge.rollback': 'Rollback',
  'overview.unusualTarget': (p) =>
    p.reason ? `${p.machine}: ${p.phase} (${p.reason})` : `${p.machine}: ${p.phase}`,

  // ---- Machine state (machines.ts / drift.ts) ----
  'machine.agentd.online': (p) => (p.version ? `online ${p.version}` : 'online'),
  'machine.agentd.offline': 'offline',
  'machine.agentd.unknown': 'Unknown',
  'machine.reconciled.ok': 'Reconciled',
  'machine.reconciled.no': 'Not reconciled',
  'machine.reconciled.unknown': 'Unknown',
  'machine.attention.drifted': 'Drifted',
  'machine.attention.driftUnknown': (p) => `drift unknown: ${p.code}`,
  'machine.attention.agentdOffline': 'agentd not connected',
  'machine.attention.inventoryNotReady': 'inventory not ready',

  // ---- Drift three states ----
  'drift.label.inSync': 'Confirmed in sync',
  'drift.label.unknown': 'Unknown or stale',
  'drift.label.drifted': 'Drifted',
  'drift.detail.sameDigest':
    'Managed projection digests match yet the condition evaluates as drifted; check the backend evaluation result',
  'drift.detail.digestMismatch':
    'Managed projection digest differs from the desired one (field-level diff is not exposed by the API; run Reconcile to converge)',
  'drift.detail.lastConfirmedAt': (p) => `Last confirmed ${p.time}`,
  'drift.detail.lastConfirmedMissing': 'Last confirmation time not recorded',
  'drift.detail.lastObservedAt': (p) => `Last observed ${p.time}`,
  'drift.detail.sshModeExpected':
    'SSH-only machines are not polled in the background between operations, so this state is expected',
  'drift.reason.NeverInventoried':
    'No managed projection has ever been collected (inventory has not completed on this machine)',
  'drift.reason.StaleObservation': 'The observation is stale (outside the freshness window)',
  'drift.reason.ObservationPredatesDesired':
    'The desired generation changed; the machine has not been observed at the new generation yet',
  'drift.reason.ProjectionVersionMismatch':
    'Projection canonicalization versions differ; the previous verdict is not reused',
  'drift.reason.unreported': 'Reason not reported',

  // ---- Deployment targets and phases ----
  'target.phase.Pending': 'Pending',
  'target.phase.Running': 'Running',
  'target.phase.Succeeded': 'Succeeded',
  'target.phase.Failed': 'Failed',
  'target.phase.Superseded': 'Superseded',
  'target.phase.Skipped': 'Skipped',
  'target.phase.Blocked': 'Blocked',
  'target.reason.SupersededByNewerGeneration':
    "This machine is already on a newer generation: this rollout's target generation is outdated and will not be replayed. This is not a failure.",
  'target.reason.BlockedByNodeLock':
    "The node's execution slot is busy: it queues and waits — not counted as failed, and the batch does not advance.",
  'target.reason.unknown': (p) => `Backend reason code: ${p.code}`,
  'target.skipped.noReason':
    "Explicitly skipped by the operator: kept in audit, not counted as success, and the node's actual state is unchanged (it may still be writing).",
  'target.superseded.noReason': 'Superseded by a newer generation (no reason code given)',
  'deploy.phase.Pending': 'Pending',
  'deploy.phase.Canary': 'Canary',
  'deploy.phase.RollingOut': 'Rolling out',
  'deploy.phase.Paused': 'Paused',
  'deploy.phase.Succeeded': 'Succeeded',
  'deploy.phase.Failed': 'Failed',
  'deploy.phase.none': 'Not started',

  // ---- Operation phases and types ----
  'ops.phase.Pending': 'Pending',
  'ops.phase.Running': 'Running',
  'ops.phase.AwaitingConfirmation': 'Awaiting confirmation',
  'ops.phase.CancelRequested': 'Cancel requested',
  'ops.phase.Succeeded': 'Succeeded',
  'ops.phase.Failed': 'Failed',
  'ops.phase.Unknown': 'Outcome unknown',
  'ops.type.Reconcile': 'Reconcile',
  'ops.type.Repair': 'Repair agentd',
  'ops.type.Bootstrap': 'Bootstrap',
  'ops.type.Rollback': 'Rollback',
  'ops.type.AutoPlan': 'Auto plan',
  'ops.source.automatic': 'Automatic',
  'ops.source.manual': 'Manual',

  // ---- Time and duration ----
  'time.justNow': 'just now',
  'time.minutesAgo': (p) => `${p.n} ${selectPlural('en', Number(p.n), 'minute', 'minutes')} ago`,
  'time.hoursAgo': (p) => `${p.n} ${selectPlural('en', Number(p.n), 'hour', 'hours')} ago`,
  'time.daysAgo': (p) => `${p.n} ${selectPlural('en', Number(p.n), 'day', 'days')} ago`,
  'time.inFuture': (p) => `in ${p.duration}`,
  'time.duration.expired': 'expired',
  'time.duration.seconds': (p) => `${p.n} ${selectPlural('en', Number(p.n), 'sec', 'sec')}`,
  'time.duration.minutes': (p) => `${p.m} min ${p.s} sec`,
  'time.duration.hours': (p) => `${p.h} hr ${p.m} min`,
};

export const catalogs: Record<Locale, Record<string, Message>> = {
  'zh-CN': zhCN,
  en,
};
