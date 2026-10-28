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
  'app.labelColon': '：',

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
  'overview.ops.title': '最近的失败与结果被改写的操作',
  'overview.ops.subtitle': '操作按机器分别记录，这里汇总最近的失败与完成时结果已不代表当前版本的操作。',
  'overview.ops.empty': '没有失败或结果被改写的操作。',
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
  'overview.table.modifier': '结果状态',
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

  // ---- 通用反馈与错误映射 ----
  'common.actionRejected': '操作被拒绝',
  'common.actionFailed': '操作未完成',
  'common.actionNotPerformed': '操作未执行',
  'common.cancel': '取消',
  'common.blocked': '被阻塞',
  'common.available': '可用',
  'common.unresolvedOpRef': (p) => `未决操作 ${p.id}（${p.phase}）`,
  'ok.reconcileAccepted': '收敛请求已受理',
  'ok.probeDone': '探测完成',
  'ok.inventoryAccepted': '观测采集已受理',
  'ok.planConfirmed': '计划已确认',
  'ok.cancelRequested': '取消请求已提交',
  'ok.skipped': '已跳过',
  'ok.rollbackAccepted': '回滚请求已受理',
  'ok.rolloutCreated': '回滚型发布已创建',
  'ok.exported': '已导出',
  'ok.installed': '已安装',
  'ok.refUpdated': '已更新 ref',
  'error.requestFailed': '请求失败',
  'error.unexpected': '发生意外错误，请稍后重试。',
  'error.loadRetryHint': '可稍后重试，或点击右上角「全量刷新」。',
  'machines.error.load': '机器列表加载失败',
  'profiles.error.load': '配置档加载失败',
  'skills.error.load': '技能加载失败',
  'deployments.error.load': '发布加载失败',
  'detail.error.load': '机器详情加载失败',
  'error.busy.title': '该机有未决操作（MachineBusy）',
  'error.busy.detail': (p) =>
    `${p.message}。该机的未决操作尚未结束，新的变更类操作会被拒绝；可对该操作执行确认、取消或跳过。`,
  'error.replan.title': '计划基线已失效（ReplanRequired）',
  'error.replan.detail': (p) =>
    `${p.message}。确认之后该机的受管内容又发生了变化，本次执行被拒绝且没有做任何变更。`,
  'error.replan.hint': '请重新生成计划并再次确认。',
  'error.rollbackUnsupported.title': '无法回滚（RollbackUnsupported）',
  'error.desiredInvalid.title': '期望状态无法渲染（DesiredStateInvalid）',
  'error.desiredInvalid.detail': (p) =>
    `${p.message}。请检查该机引用的配置档内容（如 agent 家族名称或引用是否有效）。`,
  'error.notFound.title': '对象不存在（NotFound）',
  'error.notFound.detail': (p) => `${p.message}。该对象可能已被删除，刷新后即可看到最新列表。`,
  'error.invalid.title': '请求被拒绝（Invalid）',
  'error.invalid.detail': (p) => `${p.message}。请检查输入内容后重试。`,
  'error.knownReason.title': (p) => `操作失败（${p.reason}）`,

  // ---- 条件（Machine Detail 六条件与列呈现） ----
  'condition.SSHReachable': 'SSH 可达',
  'condition.AgentConnected': 'agentd 在线',
  'condition.InventoryReady': 'inventory 就绪',
  'condition.Drifted': '漂移',
  'condition.Reconciled': '收敛',
  'condition.Degraded': '降级',
  'condition.notRecorded': '该条件尚未记录',

  // ---- 机器 SSH 三态（条件状态的展示词） ----
  'machines.ssh.reachable': '可达',
  'machines.ssh.unreachable': '不可达',
  'machines.ssh.unknown': '未知',

  // ---- Machines 页 ----
  'machines.subtitle': (p) => `共 ${p.n} 台机器。漂移与收敛列会明确区分「已确认一致」与「未知」。`,
  'machines.add': '添加机器',
  'machines.form.name': '机器名（DNS 风格，机群内唯一）',
  'machines.form.profile': '配置档（可留空）',
  'machines.form.mode': '管理方式',
  'machines.create': '创建',
  'machines.creating': '创建中…',
  'machines.createFailed': '创建失败',
  'machines.empty': '还没有机器。点击右上角「添加机器」创建第一台。',
  'machines.col.name': '机器名',
  'machines.col.osArch': '系统 · 架构',
  'machines.col.profile': '配置档',
  'machines.col.agentd': 'agentd',
  'machines.col.ssh': 'SSH',
  'machines.col.drift': '漂移',
  'machines.col.reconciled': '收敛',
  'machines.col.lastSeen': '最近上报',
  'machines.col.actions': '操作',
  'machines.unresolvedBadge': '未决操作',
  'machines.unresolvedTitle': (p) => `该机有未决操作（${p.phase}）：新的变更类操作会被拒绝。`,
  'machines.action.reconcile': '收敛',
  'machines.action.reconcileTitle': '对该机执行收敛；该机存在未决操作时会被拒绝。',
  'machines.action.probe': 'SSH 探测',
  'machines.probing': '探测中…',
  'machines.probeTitle': '解析该机的 SSH 配置并采集平台信息，结果写回该机的 SSH 可达状态。',
  'machines.action.inventory': '观测采集',
  'machines.collecting': '采集中…',
  'machines.inventoryTitle': '通过 SSH 只读采集观测；仅 SSH 管理的机器可用。',
  'machines.inventoryDisabled': '仅 SSH 管理的机器支持手动观测采集；该机由 agentd 管理。',
  'machines.bootstrapUnavailable': '引导与修复 agentd 功能尚未提供。',
  'machines.bootstrapRepair': '纳管 / 修复 agentd',
  'machines.reconcile.failed': '收敛请求失败',
  'machines.action.detail': '详情',
  'machines.reconcile.ok': (p) => `${p.name} 的收敛请求已受理：操作 ${p.id}。`,
  'machines.reconcile.hint': '未决操作会阻塞该机新的变更类操作；可在机器详情中取消或跳过。',
  'machines.probe.ok': (p) => `${p.name}：SSH 可达。`,
  'machines.probe.detail': (p) =>
    `平台 ${p.os}/${p.arch} · 家目录 ${p.homeDir}。探测只采集系统类型与家目录；完整观测由「观测采集」上报。`,
  'machines.probe.failed': 'SSH 探测失败',
  'machines.probe.failedHint': '失败原因已写入该机的 SSH 可达状态，可在机器详情查看。',
  'machines.inventory.ok': (p) => `${p.name} 的观测采集已受理：操作 ${p.id}。`,
  'machines.inventory.hint': '通过 SSH 只读执行，不改变机器；结果体现在该机的漂移状态与最近观测时间。',
  'machines.inventory.failed': '观测采集失败',

  // ---- Machine Detail 页 ----
  'detail.back': '返回机器列表',
  'detail.header.mode': '管理方式',
  'detail.header.profile': '配置档',
  'detail.header.created': '创建于',
  'detail.notFound': (p) => `机器 ${p.name} 不存在（可能已被删除）。`,
  'detail.section.connectivity': '联通与状态',
  'detail.section.agents': 'Agents 与版本',
  'detail.section.desiredObserved': '期望与观测',
  'detail.section.skills': '技能',
  'detail.section.providers': '模型与 Provider',
  'detail.section.mcp': 'MCP',
  'detail.section.drift': '漂移详情',
  'detail.section.operations': '操作历史与未决操作',
  'detail.section.actions': '回滚与其他动作',
  'detail.agents.subtitle': '左侧：按配置档渲染的期望版本；右侧：该机自行上报的能力。',
  'detail.desiredRenderDigest': '期望快照摘要',
  'detail.condition.transition': '最近转变',
  'detail.probe.caption': '采集该机的连接信息并更新 SSH 可达状态。',
  'detail.field.osArch': '系统 / 架构',
  'detail.field.hostname': '主机名',
  'detail.field.homeDir': '家目录',
  'detail.field.kernel': '内核版本',
  'detail.field.agentdVersion': 'agentd 版本',
  'detail.field.lastHeartbeat': '最近心跳',
  'detail.field.lastInventory': '最近观测',
  'detail.field.inventorySeq': '观测序号',
  'detail.field.desiredGeneration': '期望代',
  'detail.field.observedGeneration': '观测代',
  'detail.agents.desiredEmpty': '该机的期望配置中没有 agents 条目。',
  'detail.agents.col.family': '家族',
  'detail.agents.col.desiredVersion': '期望版本',
  'detail.agents.col.capability': '能力',
  'detail.agents.col.state': '状态',
  'detail.agents.col.verifiedVersions': '已验证版本',
  'detail.agents.col.note': '说明',
  'detail.agents.capabilitiesNote': '状态与说明来自节点上报；未验证的能力不会标为已验证。',
  'detail.agents.empty': '该机尚未上报能力信息（agentd 未连接或未上报）。',
  'detail.gen.mismatch': '代不一致',
  'detail.gen.mismatchTitle': '观测尚未按当前期望代采集；此时漂移状态会显示为未知。',
  'detail.gen.match': '代一致',
  'detail.genBasis': '两侧受管投影摘要一致即视为一致；字段级差异暂不可查看。',
  'detail.skills.empty': '该机的期望配置中没有已解析的技能。',
  'detail.skills.col.name': '名称',
  'detail.skills.col.revision': '修订',
  'detail.skills.col.contentDigest': '内容摘要',
  'detail.skills.col.artifactDigest': '工件摘要',
  'detail.providers.sshNote': 'SSH 管理的机器不做后台轮询；此处只在有操作时显示期望侧。',
  'detail.providers.col.family': '家族',
  'detail.providers.col.provider': 'Provider（配置档配置）',
  'detail.providers.col.config': '期望配置',
  'detail.providers.empty': '没有期望的 agents 条目。',
  'detail.providers.registered': (p) =>
    `已登记的 Provider：${p.names}（仅记录凭据的环境变量名，界面不显示凭据内容）。`,
  'detail.mcp.col.name': '名称',
  'detail.mcp.col.normalized': '归一化条目',
  'detail.mcp.note': '条目中的 envRefs 仅含环境变量名；未点名的条目不受本系统管理。',
  'detail.mcp.empty': '该机的期望配置未包含 MCP 条目。',
  'detail.drift.reconcile': '收敛',
  'detail.drift.desiredDigest': '期望受管投影摘要',
  'detail.drift.observedDigest': '观测受管投影摘要',
  'detail.drift.diffTitle': '字段级对比暂不可用',
  'detail.drift.diffUnavailable': '差异只能定位到摘要级别：字段级对比暂不可用。可执行收敛使机器与期望一致。',
  'detail.drift.unavailable': '漂移视图不可用（该机可能刚被删除或读取失败）。',
  'detail.blocked.title': (p) => `该机有未决操作（${p.phase}）`,
  'detail.blocked.detail': (p) =>
    `操作 ${p.id}。新的变更类操作会被拒绝，向该机派发发布也会保持等待；可对该操作执行确认、取消或跳过。`,
  'detail.awaiting.title': (p) => `等待确认（剩余 ${p.remaining}）· 操作 ${p.id}`,
  'detail.confirm.ok': (p) => `已确认计划并推进：操作 ${p.id}。`,
  'detail.confirm.hint': '确认只代表确认这份计划；执行时会重新校验基线，不一致会要求重新计划。',
  'detail.cancel.ok': (p) => `已请求取消：操作 ${p.id}。`,
  'detail.skip.ok': (p) => `已跳过：操作 ${p.id}。`,
  'detail.skip.reasonRequired': '跳过前必须填写原因（将记入审计）。',
  'detail.skip.placeholder': '跳过原因（必填）',
  'detail.skip.button': '跳过',
  'detail.rollback.genInvalid': '目标代必须是正整数。',
  'detail.rollback.ok': (p) => `已受理回滚：操作 ${p.id}，内容目标代 gen${p.gen}。`,
  'detail.rollback.hint': '回滚不把机器拉回旧代：内容取自历史代并物化为新代。',
  'detail.rollback.label': '回滚到内容代（正整数）',
  'detail.rollback.button': '回滚',
  'detail.bootstrap': '纳管（Bootstrap）',
  'detail.repairAgentd': '修复 agentd',
  'detail.probe.ok': (p) => `已探测：SSH 可达（平台 ${p.os}/${p.arch}，家目录 ${p.homeDir}）。`,
  'detail.inventory.ok': (p) => `已受理观测采集：操作 ${p.id}。`,
  'detail.inventory.hint': '通过 SSH 只读采集，不产生任何变更；采集在请求内执行，可能需要数十秒。',
  'detail.ops.col.id': '操作',
  'detail.ops.col.type': '类型 · 来源',
  'detail.ops.col.phase': '相位',
  'detail.ops.col.modifier': '修饰',
  'detail.ops.col.evidence': '门禁证据',
  'detail.ops.col.time': '时间',
  'detail.ops.col.disposition': '处置',
  'detail.ops.empty': '该机还没有操作记录。',
  'detail.ops.automatic': '自动（只读计划）',
  'detail.blockedTab': '未决期间的阻塞与例外',
  'detail.blocked.col.action': '动作',
  'detail.blocked.col.status': '状态',
  'detail.blocked.col.note': '说明',
  'detail.actionsNote':
    '跳过必须填写原因（记入审计），且不代表机器已停止；结果未知的操作仍占用机器锁，需等机器重新上报结果，或由你取消/跳过。',
  'detail.rollbackNote':
    '回滚会把该机内容切换为历史代的内容，并物化为新代（代计数不回退）。发放入职令牌（enrollment token）的入口不在此提供：令牌属凭据，界面不展示、不存储。',

  // ---- 操作相位与修饰的说明（Machine Detail 提示） ----
  'ops.modifierLabel.Superseded': '被取代',
  'ops.modifierLabel.Cancelled': '已取消',
  'ops.modifierLabel.Skipped': '已跳过',
  'ops.note.Pending': '操作已创建，尚未被机器取得执行权；期间新的变更类操作会被拒绝。',
  'ops.note.Running': '机器正在执行；此时取消需要机器确认停止。',
  'ops.note.AwaitingConfirmation': '计划已生成，等待操作者确认；确认前不会执行。',
  'ops.note.CancelRequested': '已发出取消请求，等待机器确认停止；机器不可达时会保持该状态。',
  'ops.note.Succeeded': '操作已成功完成。',
  'ops.note.Failed': '操作失败；失败原因见门禁证据列。',
  'ops.note.Unknown':
    '机器的执行结果未知（控制面重启或机器掉线）。机器锁仍被占用：需等机器重新上报结果，或由操作者显式取消/跳过。',
  'ops.modifier.Superseded': '迟到成功：完成时该机的期望代已前进，此结果不再代表当前代，不计为成功。',
  'ops.modifier.Cancelled': '机器确认停止后的取消终态。',
  'ops.modifier.Skipped':
    '操作者显式跳过：立即释放机器锁，但机器可能仍在写入——跳过不代表机器已停止，且不计入成功。',
  'ops.action.reconcile': '发起收敛',
  'ops.action.rollback': '回滚此机器',
  'ops.action.deployment': '向该机派发发布',
  'ops.action.autoApply': '自动应用',
  'ops.action.cancel': '取消该操作',
  'ops.action.skip': '跳过该操作（须填原因）',
  'ops.action.confirm': '确认计划',
  'ops.exception.confirmWhileAwaiting':
    '等待确认时可以确认该操作；确认不代表计划仍然有效（执行时会重新校验基线）。',
  'ops.exception.reconcileWhileAwaiting': '等待确认时，确认该操作的请求仍然可用。',
  'ops.exception.deploymentWhileBusy': '该机的发布目标保持等待：不计入失败，批次也不推进。',
  'ops.exception.autoApplyWhileBusy': '有未决操作的机器不参与自动应用。',
  'ops.exception.cancelWhileBusy': '未决期间允许取消该操作。',
  'ops.exception.skipWhileBusy': '未决期间允许跳过；会记入审计，不计入成功。',
  'ops.baselineNotice':
    '确认只表示「确认的是这份计划」：执行时仍会重新采集观测、重算计划并比对基线；基线不一致会拒绝执行、不做任何变更，并要求重新计划与确认。',
  'ops.timeoutNotice':
    '计划确认窗口超时后，该操作以失败结束并释放机器锁；超时的计划不会继续执行，需要重新计划。',
  'ops.skipConsequences':
    '跳过会立即释放机器锁，但不代表机器已停止：机器可能仍在写入，实际状态不变，且本次不计入成功。操作者、时间、目标与原因会记入审计。',
  'ops.cancelConsequences':
    '取消需要机器确认停止（机器可达时）才会离开未决状态；机器不可达时会保持「已请求取消」，系统不会单方面宣告已取消。等待确认中的计划被取消时直接结束（尚无可中断的执行）。',
  'ops.noDeadline': '未报告截止时间',

  // ---- Deployments 页 ----
  'deployments.subtitle': '批次推进与逐机结果。「已被取代」与「已跳过」会与「失败」分开显示，并附原因。',
  'deployments.all': (p) => `全部发布（${p.n}）`,
  'deployments.empty': '还没有发布。',
  'deployments.col.name': '名称',
  'deployments.col.phase': '阶段',
  'deployments.col.targetGen': '目标代',
  'deployments.col.strategy': '策略',
  'deployments.col.progress': '进度',
  'deployments.col.unusual': '异常目标',
  'deployments.col.actions': '操作',
  'deployments.badge.rollback': '回滚型',
  'deployments.progress': (p) =>
    `${p.done}/${p.total} 成功 · 失败 ${p.failed} · 取代 ${p.superseded} · 跳过 ${p.skipped} · 阻塞 ${p.blocked}`,
  'deployments.more': (p) => `…共 ${p.n} 项`,
  'deployments.pause.title': '暂停功能尚未提供。',
  'deployments.pause': '暂停',
  'deployments.resume.title': '恢复功能尚未提供。',
  'deployments.resume': '恢复',
  'deployments.detailTitle': '逐机结果',
  'deployments.stat.total': '目标机',
  'deployments.stat.succeeded': '成功',
  'deployments.stat.failed': '失败',
  'deployments.stat.modified': '取代 / 跳过',
  'deployments.levelReason': '发布级原因',
  'deployments.col.machine': '机器',
  'deployments.col.result': '结果',
  'deployments.col.reason': '原因说明',
  'deployments.col.effectiveGen': '有效代',
  'deployments.col.operation': '操作',
  'deployments.col.updated': '更新',
  'deployments.col.disposition': '处置',
  'deployments.notFailureTitle': '「已被取代」与「已跳过」都不是执行失败。',
  'deployments.skip.button': '跳过该目标',
  'deployments.skip.ok': (p) => `已跳过 ${p.machine}（记入审计；不计入成功，也不改变机器实际状态）。`,
  'deployments.skip.hint': '若该机仍在写入，请以机器实际状态为准；跳过只释放发布侧的推进。',
  'deployments.rollback.ok': (p) => `已创建回滚型发布 ${p.name}（内容目标代 gen${p.gen}）。`,
  'deployments.rollback.hint': '回滚为每台机器物化新代：机器不会回到旧代，代计数不回退。',
  'deployments.footer':
    '目标机存在未决操作时，发布目标保持等待：不计入失败，也不推进批次。跳过须填写原因并记入审计，不计入成功，也不改变机器的实际状态。',

  // ---- 发布策略与回滚说明（deployments.ts 视图） ----
  'deploy.strategy.canary': (p) => `金丝雀 ${p.n} 台`,
  'deploy.strategy.batchSize': (p) => `批大小 ${p.n}`,
  'deploy.strategy.maxUnavailable': (p) => `同批上限 ${p.n}`,
  'deploy.strategy.pauseOnFailure': '失败即暂停',
  'deploy.strategy.continueOnFailure': '失败不自动暂停',
  'deploy.rollback.label': (p) =>
    `回滚自 gen${p.from} 至 gen${p.to} 的内容（逐机物化为新代，代计数不回退）`,

  // ---- Profiles 页 ----
  'profiles.count': (p) => `配置档（${p.n}）`,
  'profiles.subtitle': '选择一项查看预览、使用它的机器与渲染结果。',
  'profiles.empty': '还没有配置档。',
  'profiles.col.name': '名称',
  'profiles.col.consumers': '使用机器',
  'profiles.col.updated': '更新时间',
  'profiles.detail.title': (p) => `${p.name} · 期望状态`,
  'profiles.spec.note': 'spec 可编辑并保存；status 由系统维护，不接受修改。',
  'profiles.tab.edit': '编辑 spec',
  'profiles.save': '保存 spec',
  'profiles.saving': '保存中…',
  'profiles.saveFailed': '保存失败',
  'profiles.delete': '删除配置档',
  'profiles.delete.confirm': (p) =>
    `删除配置档 ${p.name}？引用它的机器将无法渲染期望状态，相关操作会失败。`,
  'profiles.jsonInvalid': (p) => `JSON 解析失败：${p.message}`,
  'profiles.consumers': '使用该配置档的机器',
  'profiles.render.title': '渲染预览',
  'profiles.render.note': '预览不会保存任何内容，也不会产生新版本。',
  'profiles.render.machine': '目标机器',
  'profiles.render.button': '渲染',
  'profiles.rendering': '渲染中…',
  'profiles.render.failed': '渲染失败',
  'profiles.render.idle': '选择目标机器后点「渲染」，预览该机将收到的期望状态。',
  'profiles.selectPrompt': '从左侧选择一个配置档，查看 JSON/YAML 预览、使用它的机器与渲染结果。',

  // ---- Skills 页 ----
  'skills.subtitle': '技能需解析到明确的版本才能用于机器配置；未解析的技能会让期望状态渲染失败。',
  'skills.empty': '还没有技能资源。',
  'skills.col.name': '名称',
  'skills.col.source': '来源',
  'skills.col.ref': '请求 ref',
  'skills.col.revision': '已解析修订',
  'skills.col.digest': '内容摘要',
  'skills.col.machines': '机器',
  'skills.col.actions': '操作',
  'skills.unresolved': '未解析',
  'skills.resolve': '解析',
  'skills.resolve.title': '解析功能尚未提供。',
  'skills.metadata': '元数据',
  'skills.specRaw': 'spec（原样）',
  'skills.statusNote': 'status（由解析控制器维护）',
  'skills.ref.placeholder': '新的 ref',
  'skills.ref.button': '改 ref',
  'skills.ref.missingField': '该技能的 spec 中没有 ref 字段，无法在此修改；请直接编辑该技能的 spec。',
  'skills.ref.ok': (p) => `已更新 ${p.name} 的 ref；解析后才会影响机器配置。`,

  // ---- SSH Inventory 页 ----
  'sshinv.subtitle': '机器的 SSH 别名与 OpenSSH include 文件。',
  'sshinv.aliases.title': '机器别名',
  'sshinv.emptyAliases': '还没有机器。',
  'sshinv.col.alias': '别名',
  'sshinv.col.hostName': '主机名',
  'sshinv.col.user': '用户',
  'sshinv.col.port': '端口',
  'sshinv.col.identityFile': '身份文件',
  'sshinv.col.mode': '管理方式',
  'sshinv.col.ssh': 'SSH 状态',
  'sshinv.notSet': '（未配置）',
  'sshinv.machineRef': (p) => `机器 ${p.name}`,
  'sshinv.preview.title': 'OpenSSH include 预览',
  'sshinv.preview.note':
    '内容由控制面统一生成：只包含配置了连接信息的机器；连接值含空格或引号时会被拒绝。',
  'sshinv.preview.empty': '服务端渲染结果为空。',
  'sshinv.preview.counts': (p) => `包含 ${p.included} 台 · 跳过 ${p.skipped} 台`,
  'sshinv.export': '导出（下载 .conf）',
  'sshinv.exporting': '导出中…',
  'sshinv.export.ok': (p) => `已导出 include 文件（包含 ${p.included} 台，跳过 ${p.skipped} 台）。`,
  'sshinv.export.failed': '导出失败',
  'sshinv.install.confirm':
    '我确认安装/更新 ~/.ssh/agent-fleet.conf（主配置不会被改动，Include 行由我自己添加）',
  'sshinv.install.button': '安装 include 文件',
  'sshinv.install.title': '只写入 ~/.ssh/agent-fleet.conf，不会改动你的主 SSH 配置；需先勾选确认。',
  'sshinv.installing': '安装中…',
  'sshinv.install.ok': (p) => `已安装/更新 ${p.path}（内容摘要 ${p.digest}）。`,
  'sshinv.install.failed': '安装失败',
  'sshinv.lastInstall': '最近一次安装结果',
  'sshinv.boundary.title': '安装边界',
  'sshinv.boundary.detail':
    '安装只写 ~/.ssh/agent-fleet.conf；要让 ssh/scp 使用它，请在主配置中加入 Include 行（由你自己添加）。连接值含空格或引号会被拒绝渲染；host-key 校验保持严格。',

  // ---- 尚未提供的功能（禁用入口的统一口径） ----
  'ssh.featureUnavailable': '该功能尚未提供，当前版本不可用。',
  'ssh.showDiff.title': '查看字段级差异（暂不可用）',
  'ssh.showDiff.reason': '接口暂未提供字段级差异；可先用收敛操作让机器与期望一致。',

  // ---- SSH 通知（host-key / 版本协商） ----
  'ssh.hostKey.title': 'SSH host-key 校验失败',
  'ssh.hostKey.detail': (p) =>
    `${p.message}${p.reason}：请核对目标机的 host-key 指纹后重试；系统不会跳过该校验。`,
  'ssh.unreachable.title': 'SSH 不可达',
  'ssh.unreachable.detail': (p) => `${p.message}${p.reason}：请检查网络、端口与凭据配置。`,
  'ssh.agentVersion.title': '需升级 agentd',
  'ssh.agentVersion.detail': (p) =>
    `${p.message}（reason=${p.reason}）：服务端拒绝了不受支持的 agentd 版本，不会向该机下发期望状态；请升级该机的 agentd。`,

  // ---- 事件流重连原因（App 顶部条） ----
  'app.stream.reason.http': (p) => `连接失败（HTTP ${p.status}），正在自动重连`,
  'app.stream.reason.contentType': (p) => `响应类型异常（${p.type}），正在自动重连`,
  'app.stream.reason.serverClosed': '连接被服务端关闭，正在自动重连',
  'app.stream.reason.network': '网络连接失败，正在自动重连',
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
  'app.labelColon': ': ',

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
  'overview.ops.title': 'Recent failed or overridden operations',
  'overview.ops.subtitle':
    'Operations are recorded per machine; this list aggregates recent failures and results that no longer represent the current generation.',
  'overview.ops.empty': 'No failed or overridden operations.',
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
  'overview.table.modifier': 'Outcome',
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

  // ---- Common feedback and error mapping ----
  'common.actionRejected': 'Action rejected',
  'common.actionFailed': 'Operation not completed',
  'common.actionNotPerformed': 'Not performed',
  'common.cancel': 'Cancel',
  'common.blocked': 'Blocked',
  'common.available': 'Available',
  'common.unresolvedOpRef': (p) => `Unresolved operation ${p.id} (${p.phase})`,
  'ok.reconcileAccepted': 'Reconcile request accepted',
  'ok.probeDone': 'Probe finished',
  'ok.inventoryAccepted': 'Inventory collection accepted',
  'ok.planConfirmed': 'Plan confirmed',
  'ok.cancelRequested': 'Cancellation requested',
  'ok.skipped': 'Skipped',
  'ok.rollbackAccepted': 'Rollback request accepted',
  'ok.rolloutCreated': 'Rollback rollout created',
  'ok.exported': 'Exported',
  'ok.installed': 'Installed',
  'ok.refUpdated': 'Ref updated',
  'error.requestFailed': 'Request failed',
  'error.unexpected': 'An unexpected error occurred. Please try again later.',
  'error.loadRetryHint': 'You can retry, or use "Refresh all" in the header.',
  'machines.error.load': 'Failed to load the machine list',
  'profiles.error.load': 'Failed to load profiles',
  'skills.error.load': 'Failed to load skills',
  'deployments.error.load': 'Failed to load rollouts',
  'detail.error.load': 'Failed to load machine details',
  'error.busy.title': 'This machine has an unresolved operation (MachineBusy)',
  'error.busy.detail': (p) =>
    `${p.message}. The machine's unresolved operation is still open, so new change operations are rejected; you can confirm, cancel or skip that operation.`,
  'error.replan.title': 'The plan baseline is stale (ReplanRequired)',
  'error.replan.detail': (p) =>
    `${p.message}. The machine's managed content changed again after confirmation; execution was rejected and nothing was changed.`,
  'error.replan.hint': 'Generate a new plan and confirm it again.',
  'error.rollbackUnsupported.title': 'Rollback not possible (RollbackUnsupported)',
  'error.desiredInvalid.title': 'Desired state cannot be rendered (DesiredStateInvalid)',
  'error.desiredInvalid.detail': (p) =>
    `${p.message}. Check the profile this machine references (for example the agent family name or whether its references resolve).`,
  'error.notFound.title': 'Not found (NotFound)',
  'error.notFound.detail': (p) =>
    `${p.message}. The object may have been deleted; refresh to see the current list.`,
  'error.invalid.title': 'Request rejected (Invalid)',
  'error.invalid.detail': (p) => `${p.message}. Check your input and try again.`,
  'error.knownReason.title': (p) => `Operation failed (${p.reason})`,

  // ---- Conditions (machine detail & tables) ----
  'condition.SSHReachable': 'SSH reachable',
  'condition.AgentConnected': 'agentd connected',
  'condition.InventoryReady': 'inventory ready',
  'condition.Drifted': 'Drift',
  'condition.Reconciled': 'Reconciled',
  'condition.Degraded': 'Degraded',
  'condition.notRecorded': 'This condition has not been recorded',

  // ---- Machine SSH tri-state labels ----
  'machines.ssh.reachable': 'Reachable',
  'machines.ssh.unreachable': 'Unreachable',
  'machines.ssh.unknown': 'Unknown',

  // ---- Machines page ----
  'machines.subtitle': (p) =>
    `${p.n} ${selectPlural('en', Number(p.n), 'machine', 'machines')} in total. The drift and reconciled columns distinguish "confirmed in sync" from "unknown".`,
  'machines.add': 'Add machine',
  'machines.form.name': 'Machine name (DNS style, unique in the fleet)',
  'machines.form.profile': 'Profile (optional)',
  'machines.form.mode': 'Management mode',
  'machines.create': 'Create',
  'machines.creating': 'Creating…',
  'machines.createFailed': 'Creation failed',
  'machines.empty': 'No machines yet. Use "Add machine" in the top-right corner to create the first one.',
  'machines.col.name': 'Machine',
  'machines.col.osArch': 'OS · arch',
  'machines.col.profile': 'Profile',
  'machines.col.agentd': 'agentd',
  'machines.col.ssh': 'SSH',
  'machines.col.drift': 'Drift',
  'machines.col.reconciled': 'Reconciled',
  'machines.col.lastSeen': 'Last seen',
  'machines.col.actions': 'Actions',
  'machines.unresolvedBadge': 'Unresolved',
  'machines.unresolvedTitle': (p) =>
    `This machine has an unresolved operation (${p.phase}): new change operations are rejected.`,
  'machines.action.reconcile': 'Reconcile',
  'machines.action.reconcileTitle': 'Run reconcile on this machine; rejected while an operation is unresolved.',
  'machines.action.probe': 'Probe SSH',
  'machines.probing': 'Probing…',
  'machines.probeTitle':
    "Resolves the machine's SSH config and collects platform facts; the result is written back to its SSH reachability.",
  'machines.action.inventory': 'Inventory',
  'machines.collecting': 'Collecting…',
  'machines.inventoryTitle': 'Collects observations over SSH (read-only); only for SSH-managed machines.',
  'machines.inventoryDisabled':
    'Manual inventory collection is only available for SSH-managed machines; this one is agentd-managed.',
  'machines.bootstrapUnavailable': 'Bootstrap and agentd repair are not available yet.',
  'machines.bootstrapRepair': 'Bootstrap / repair agentd',
  'machines.reconcile.failed': 'Reconcile request failed',
  'machines.action.detail': 'Details',
  'machines.reconcile.ok': (p) => `${p.name}: reconcile request accepted (operation ${p.id}).`,
  'machines.reconcile.hint':
    'An unresolved operation blocks new change operations on this machine; you can cancel or skip it on the machine detail page.',
  'machines.probe.ok': (p) => `${p.name}: SSH reachable.`,
  'machines.probe.detail': (p) =>
    `Platform ${p.os}/${p.arch} · home ${p.homeDir}. Probing collects only OS type and home directory; the full inventory is reported via "Inventory".`,
  'machines.probe.failed': 'SSH probe failed',
  'machines.probe.failedHint':
    "The failure reason was written back to the machine's SSH reachability; see the machine detail page.",
  'machines.inventory.ok': (p) => `${p.name}: inventory collection accepted (operation ${p.id}).`,
  'machines.inventory.hint':
    "Runs read-only over SSH and changes nothing; results appear in the machine's drift state and last-observed time.",
  'machines.inventory.failed': 'Inventory collection failed',

  // ---- Machine Detail page ----
  'detail.back': 'Back to machines',
  'detail.header.mode': 'Mode',
  'detail.header.profile': 'Profile',
  'detail.header.created': 'Created',
  'detail.notFound': (p) => `Machine ${p.name} does not exist (it may have been deleted).`,
  'detail.section.connectivity': 'Connectivity & state',
  'detail.section.agents': 'Agents & versions',
  'detail.section.desiredObserved': 'Desired vs observed',
  'detail.section.skills': 'Skills',
  'detail.section.providers': 'Models & providers',
  'detail.section.mcp': 'MCP',
  'detail.section.drift': 'Drift details',
  'detail.section.operations': 'Operation history & unresolved operations',
  'detail.section.actions': 'Rollback & other actions',
  'detail.agents.subtitle': 'Left: desired versions rendered from the profile; right: capabilities reported by the machine itself.',
  'detail.desiredRenderDigest': 'Desired snapshot digest',
  'detail.condition.transition': 'Last transition',
  'detail.probe.caption': "Collects the machine's connection facts and updates its SSH reachability.",
  'detail.field.osArch': 'OS / arch',
  'detail.field.hostname': 'Hostname',
  'detail.field.homeDir': 'Home directory',
  'detail.field.kernel': 'Kernel',
  'detail.field.agentdVersion': 'agentd version',
  'detail.field.lastHeartbeat': 'Last heartbeat',
  'detail.field.lastInventory': 'Last inventory',
  'detail.field.inventorySeq': 'Inventory seq',
  'detail.field.desiredGeneration': 'Desired generation',
  'detail.field.observedGeneration': 'Observed generation',
  'detail.agents.desiredEmpty': "The machine's desired state has no agents entries.",
  'detail.agents.col.family': 'Family',
  'detail.agents.col.desiredVersion': 'Desired version',
  'detail.agents.col.capability': 'Capability',
  'detail.agents.col.state': 'State',
  'detail.agents.col.verifiedVersions': 'Verified versions',
  'detail.agents.col.note': 'Note',
  'detail.agents.capabilitiesNote':
    "State and notes come from the node's own report; unverified capabilities are never shown as verified.",
  'detail.agents.empty': 'This machine has not reported capability information yet (agentd not connected or nothing reported).',
  'detail.gen.mismatch': 'Generations differ',
  'detail.gen.mismatchTitle':
    'The machine has not been observed at the current desired generation yet; drift will show as unknown.',
  'detail.gen.match': 'Generations match',
  'detail.genBasis':
    'In sync means the digests of both managed projections match; a field-level diff is not available.',
  'detail.skills.empty': "The machine's desired state has no resolved skills.",
  'detail.skills.col.name': 'Name',
  'detail.skills.col.revision': 'Revision',
  'detail.skills.col.contentDigest': 'Content digest',
  'detail.skills.col.artifactDigest': 'Artifact digest',
  'detail.providers.sshNote':
    'SSH-managed machines are not polled in the background; the desired side only appears when operations exist.',
  'detail.providers.col.family': 'Family',
  'detail.providers.col.provider': 'Provider (from profile)',
  'detail.providers.col.config': 'Desired config',
  'detail.providers.empty': 'No desired agents entries.',
  'detail.providers.registered': (p) =>
    `Registered providers: ${p.names} (only the environment variable names of credentials are recorded; secrets are never shown).`,
  'detail.mcp.col.name': 'Name',
  'detail.mcp.col.normalized': 'Normalized entry',
  'detail.mcp.note':
    'envRefs in these entries only map environment variable names; entries not named here are not managed by this system.',
  'detail.mcp.empty': "The machine's desired state has no MCP entries.",
  'detail.drift.reconcile': 'Reconcile',
  'detail.drift.desiredDigest': 'Desired projection digest',
  'detail.drift.observedDigest': 'Observed projection digest',
  'detail.drift.diffTitle': 'Field-level diff is not available',
  'detail.drift.diffUnavailable':
    'Differences are only visible at digest level: a field-level diff is not available. Run Reconcile to bring the machine back in line.',
  'detail.drift.unavailable':
    'Drift view unavailable (the machine may have just been deleted, or reading it failed).',
  'detail.blocked.title': (p) => `This machine has an unresolved operation (${p.phase})`,
  'detail.blocked.detail': (p) =>
    `Operation ${p.id}. New change operations are rejected and rollouts to this machine keep waiting; you can confirm, cancel or skip that operation.`,
  'detail.awaiting.title': (p) => `Awaiting confirmation (${p.remaining} left) · operation ${p.id}`,
  'detail.confirm.ok': (p) => `Plan confirmed and advancing: operation ${p.id}.`,
  'detail.confirm.hint':
    'Confirming approves exactly this plan; the baseline is re-checked at execution and a mismatch requires a new plan.',
  'detail.cancel.ok': (p) => `Cancellation requested: operation ${p.id}.`,
  'detail.skip.ok': (p) => `Skipped: operation ${p.id}.`,
  'detail.skip.reasonRequired': 'A reason is required before skipping (it is recorded for audit).',
  'detail.skip.placeholder': 'Skip reason (required)',
  'detail.skip.button': 'Skip',
  'detail.rollback.genInvalid': 'The target generation must be a positive integer.',
  'detail.rollback.ok': (p) => `Rollback accepted: operation ${p.id}, content target generation gen${p.gen}.`,
  'detail.rollback.hint':
    'Rollback does not move the machine back to an old generation: content comes from a historical generation and materializes as a new one.',
  'detail.rollback.label': 'Roll back to content generation (positive integer)',
  'detail.rollback.button': 'Rollback',
  'detail.bootstrap': 'Bootstrap',
  'detail.repairAgentd': 'Repair agentd',
  'detail.probe.ok': (p) => `Probed: SSH reachable (platform ${p.os}/${p.arch}, home ${p.homeDir}).`,
  'detail.inventory.ok': (p) => `Inventory collection accepted: operation ${p.id}.`,
  'detail.inventory.hint':
    'Collected read-only over SSH with no changes; the request runs synchronously and can take tens of seconds.',
  'detail.ops.col.id': 'Operation',
  'detail.ops.col.type': 'Type · source',
  'detail.ops.col.phase': 'Phase',
  'detail.ops.col.modifier': 'Modifier',
  'detail.ops.col.evidence': 'Evidence',
  'detail.ops.col.time': 'Time',
  'detail.ops.col.disposition': 'Actions',
  'detail.ops.empty': 'No operations recorded for this machine yet.',
  'detail.ops.automatic': 'Automatic (read-only plan)',
  'detail.blockedTab': 'Blocks & exceptions while unresolved',
  'detail.blocked.col.action': 'Action',
  'detail.blocked.col.status': 'Status',
  'detail.blocked.col.note': 'Note',
  'detail.actionsNote':
    'Skipping requires a reason (recorded for audit) and does not mean the machine stopped; an operation with unknown outcome still holds the machine lock — wait for the machine to report again, or cancel/skip it.',
  'detail.rollbackNote':
    "Rollback switches this machine's content to a historical generation, materialized as a new generation (generation counters never decrease). Enrollment-token issuance is intentionally not offered here: tokens are credentials and are never shown or stored in this console.",

  // ---- Operation phase and modifier notes ----
  'ops.modifierLabel.Superseded': 'Superseded',
  'ops.modifierLabel.Cancelled': 'Cancelled',
  'ops.modifierLabel.Skipped': 'Skipped',
  'ops.note.Pending':
    'The operation was created but the machine has not picked it up yet; new change operations are rejected in the meantime.',
  'ops.note.Running': 'The machine is executing; cancelling now waits for the machine to confirm it stopped.',
  'ops.note.AwaitingConfirmation':
    'A plan was produced and waits for operator confirmation; nothing executes before it.',
  'ops.note.CancelRequested':
    'A cancellation was sent and waits for the machine to confirm it stopped; an unreachable machine stays in this state.',
  'ops.note.Succeeded': 'The operation completed successfully.',
  'ops.note.Failed': 'The operation failed; see the evidence column for the failure reason.',
  'ops.note.Unknown':
    "The machine's outcome is unknown (control plane restart or the machine went offline). The machine lock is still held: wait for the machine to report again, or explicitly cancel/skip.",
  'ops.modifier.Superseded':
    "Late success: the machine's desired generation had already moved on when it finished, so this result no longer represents the current generation and does not count as success.",
  'ops.modifier.Cancelled': 'Final state after the machine confirmed the cancellation.',
  'ops.modifier.Skipped':
    'Explicitly skipped by the operator: the machine lock is released immediately, but the machine may still be writing — skipping does not mean it stopped and does not count as success.',
  'ops.action.reconcile': 'Reconcile',
  'ops.action.rollback': 'Roll back this machine',
  'ops.action.deployment': 'Deploy to this machine',
  'ops.action.autoApply': 'Auto apply',
  'ops.action.cancel': 'Cancel this operation',
  'ops.action.skip': 'Skip this operation (reason required)',
  'ops.action.confirm': 'Confirm plan',
  'ops.exception.confirmWhileAwaiting':
    'While awaiting confirmation the plan can be confirmed; confirming does not guarantee it is still valid (the baseline is re-checked at execution).',
  'ops.exception.reconcileWhileAwaiting':
    'While awaiting confirmation, confirming this operation remains available.',
  'ops.exception.deploymentWhileBusy':
    "The rollout target for this machine keeps waiting: not counted as failed, and the batch does not advance.",
  'ops.exception.autoApplyWhileBusy': 'Machines with unresolved operations are excluded from auto apply.',
  'ops.exception.cancelWhileBusy': 'While unresolved, cancelling this operation is allowed.',
  'ops.exception.skipWhileBusy':
    'While unresolved, skipping is allowed; it is recorded for audit and does not count as success.',
  'ops.baselineNotice':
    'Confirming only approves this exact plan: at execution the observation is re-collected, the plan re-computed and compared against the baseline; a mismatch rejects execution with no changes and requires a new plan and confirmation.',
  'ops.timeoutNotice':
    'When the plan confirmation window times out, the operation ends as failed and the machine lock is released; a timed-out plan is never executed — create a new plan.',
  'ops.skipConsequences':
    'Skipping releases the machine lock immediately, but it does not mean the machine has stopped: it may still be writing, its actual state is unchanged, and this does not count as success. Operator, time, target and reason are recorded for audit.',
  'ops.cancelConsequences':
    'A cancellation takes effect only after the machine confirms it has stopped (when reachable); an unreachable machine stays in "cancel requested" and the server never declares it cancelled on its own. Cancelling an awaiting-confirmation plan ends it immediately (there is no execution to interrupt).',
  'ops.noDeadline': 'No deadline reported',

  // ---- Deployments page ----
  'deployments.subtitle':
    'Batch progress and per-machine results. "Superseded" and "Skipped" are shown separately from "Failed", each with its reason.',
  'deployments.all': (p) => `All rollouts (${p.n})`,
  'deployments.empty': 'No rollouts yet.',
  'deployments.col.name': 'Name',
  'deployments.col.phase': 'Phase',
  'deployments.col.targetGen': 'Target gen',
  'deployments.col.strategy': 'Strategy',
  'deployments.col.progress': 'Progress',
  'deployments.col.unusual': 'Unusual targets',
  'deployments.col.actions': 'Actions',
  'deployments.badge.rollback': 'Rollback',
  'deployments.progress': (p) =>
    `${p.done}/${p.total} succeeded · ${p.failed} failed · ${p.superseded} superseded · ${p.skipped} skipped · ${p.blocked} blocked`,
  'deployments.more': (p) => `…${p.n} in total`,
  'deployments.pause.title': 'Pause is not available yet.',
  'deployments.pause': 'Pause',
  'deployments.resume.title': 'Resume is not available yet.',
  'deployments.resume': 'Resume',
  'deployments.detailTitle': 'Per-machine results',
  'deployments.stat.total': 'Target machines',
  'deployments.stat.succeeded': 'Succeeded',
  'deployments.stat.failed': 'Failed',
  'deployments.stat.modified': 'Superseded / skipped',
  'deployments.levelReason': 'Rollout-level reason',
  'deployments.col.machine': 'Machine',
  'deployments.col.result': 'Result',
  'deployments.col.reason': 'Reason',
  'deployments.col.effectiveGen': 'Effective gen',
  'deployments.col.operation': 'Operation',
  'deployments.col.updated': 'Updated',
  'deployments.col.disposition': 'Actions',
  'deployments.notFailureTitle': 'Superseded and Skipped are not failures.',
  'deployments.skip.button': 'Skip this target',
  'deployments.skip.ok': (p) =>
    `Skipped ${p.machine} (recorded for audit; not counted as success and the machine's actual state is unchanged).`,
  'deployments.skip.hint':
    "If the machine is still writing, trust the machine's actual state; skipping only releases the rollout's progression.",
  'deployments.rollback.ok': (p) => `Created rollback rollout ${p.name} (content target generation gen${p.gen}).`,
  'deployments.rollback.hint':
    'A rollback materializes a new generation per machine: machines never go back to an old generation, and generation counters never decrease.',
  'deployments.footer':
    "While a target machine has an unresolved operation, its rollout target keeps waiting: not counted as failed and the batch does not advance. Skipping requires a reason, is recorded for audit, does not count as success and does not change the machine's actual state.",

  // ---- Rollout strategy and rollback wording (deployments.ts views) ----
  'deploy.strategy.canary': (p) => `canary ${p.n}`,
  'deploy.strategy.batchSize': (p) => `batch size ${p.n}`,
  'deploy.strategy.maxUnavailable': (p) => `max unavailable ${p.n}`,
  'deploy.strategy.pauseOnFailure': 'pause on failure',
  'deploy.strategy.continueOnFailure': 'continue on failure',
  'deploy.rollback.label': (p) =>
    `Rolls back content from gen${p.from} to gen${p.to} (materialized per machine as a new generation; generation counters never decrease)`,

  // ---- Profiles page ----
  'profiles.count': (p) => `Profiles (${p.n})`,
  'profiles.subtitle': 'Select one to see its preview, the machines using it, and render results.',
  'profiles.empty': 'No profiles yet.',
  'profiles.col.name': 'Name',
  'profiles.col.consumers': 'Machines used',
  'profiles.col.updated': 'Updated',
  'profiles.detail.title': (p) => `${p.name} · Desired state`,
  'profiles.spec.note': 'The spec can be edited and saved; status is maintained by the system and cannot be changed here.',
  'profiles.tab.edit': 'Edit spec',
  'profiles.save': 'Save spec',
  'profiles.saving': 'Saving…',
  'profiles.saveFailed': 'Save failed',
  'profiles.delete': 'Delete profile',
  'profiles.delete.confirm': (p) =>
    `Delete profile ${p.name}? Machines referencing it will fail to render their desired state, and related operations will fail.`,
  'profiles.jsonInvalid': (p) => `Invalid JSON: ${p.message}`,
  'profiles.consumers': 'Machines using this profile',
  'profiles.render.title': 'Render preview',
  'profiles.render.note': 'Previewing saves nothing and creates no new generation.',
  'profiles.render.machine': 'Target machine',
  'profiles.render.button': 'Render',
  'profiles.rendering': 'Rendering…',
  'profiles.render.failed': 'Render failed',
  'profiles.render.idle': 'Pick a target machine and press "Render" to preview the desired state it would receive.',
  'profiles.selectPrompt':
    'Select a profile on the left to see its JSON/YAML preview, the machines using it, and render results.',

  // ---- Skills page ----
  'skills.subtitle':
    'Skills must resolve to an exact version before machines can use them; an unresolved skill makes desired-state rendering fail.',
  'skills.empty': 'No skills yet.',
  'skills.col.name': 'Name',
  'skills.col.source': 'Source',
  'skills.col.ref': 'Requested ref',
  'skills.col.revision': 'Resolved revision',
  'skills.col.digest': 'Content digest',
  'skills.col.machines': 'Machines',
  'skills.col.actions': 'Actions',
  'skills.unresolved': 'Unresolved',
  'skills.resolve': 'Resolve',
  'skills.resolve.title': 'Resolve is not available yet.',
  'skills.metadata': 'Metadata',
  'skills.specRaw': 'spec (as stored)',
  'skills.statusNote': 'status (maintained by the resolve controller)',
  'skills.ref.placeholder': 'New ref',
  'skills.ref.button': 'Change ref',
  'skills.ref.missingField':
    "This skill's spec has no ref field, so it cannot be changed here; edit the skill's spec directly.",
  'skills.ref.ok': (p) => `Updated ${p.name}'s ref; it affects machine configuration only after resolving.`,

  // ---- SSH Inventory page ----
  'sshinv.subtitle': 'SSH aliases for your machines and the OpenSSH include file.',
  'sshinv.aliases.title': 'Machine aliases',
  'sshinv.emptyAliases': 'No machines yet.',
  'sshinv.col.alias': 'Alias',
  'sshinv.col.hostName': 'Hostname',
  'sshinv.col.user': 'User',
  'sshinv.col.port': 'Port',
  'sshinv.col.identityFile': 'Identity file',
  'sshinv.col.mode': 'Mode',
  'sshinv.col.ssh': 'SSH status',
  'sshinv.notSet': '(not set)',
  'sshinv.machineRef': (p) => `machine ${p.name}`,
  'sshinv.preview.title': 'OpenSSH include preview',
  'sshinv.preview.note':
    'The content is generated by the control plane: it only includes machines with connection fields configured; connection values containing whitespace or quotes are rejected.',
  'sshinv.preview.empty': 'The server-rendered content is empty.',
  'sshinv.preview.counts': (p) => `${p.included} included · ${p.skipped} skipped`,
  'sshinv.export': 'Export (download .conf)',
  'sshinv.exporting': 'Exporting…',
  'sshinv.export.ok': (p) => `Exported the include file (${p.included} included, ${p.skipped} skipped).`,
  'sshinv.export.failed': 'Export failed',
  'sshinv.install.confirm':
    'I confirm installing/updating ~/.ssh/agent-fleet.conf (your main SSH config is never touched; add the Include line yourself)',
  'sshinv.install.button': 'Install include file',
  'sshinv.install.title':
    "Writes only ~/.ssh/agent-fleet.conf; your main SSH config is never touched. Tick the confirmation first.",
  'sshinv.installing': 'Installing…',
  'sshinv.install.ok': (p) => `Installed/updated ${p.path} (content digest ${p.digest}).`,
  'sshinv.install.failed': 'Install failed',
  'sshinv.lastInstall': 'Latest install result',
  'sshinv.boundary.title': 'Install boundaries',
  'sshinv.boundary.detail':
    'Installing only writes ~/.ssh/agent-fleet.conf; for ssh/scp to use it, add the Include line to your main config yourself. Connection values with whitespace or quotes are rejected; host-key checking stays strict.',

  // ---- Features not shipped yet (uniform wording for disabled entries) ----
  'ssh.featureUnavailable': 'This feature is not available in the current version.',
  'ssh.showDiff.title': 'Show field-level diff (not available)',
  'ssh.showDiff.reason':
    'The API does not expose a field-level diff yet; run Reconcile to bring the machine back in line.',

  // ---- SSH notices (host-key / version negotiation) ----
  'ssh.hostKey.title': 'SSH host-key verification failed',
  'ssh.hostKey.detail': (p) =>
    `${p.message}${p.reason}: verify the target's host-key fingerprint and retry; this console never bypasses the check.`,
  'ssh.unreachable.title': 'SSH unreachable',
  'ssh.unreachable.detail': (p) => `${p.message}${p.reason}: check network, port and credential settings.`,
  'ssh.agentVersion.title': 'agentd upgrade required',
  'ssh.agentVersion.detail': (p) =>
    `${p.message} (reason=${p.reason}): the server rejected an unsupported agentd version and will not send desired state to it; upgrade agentd on this machine.`,

  // ---- Stream reconnect reasons (header bar) ----
  'app.stream.reason.http': (p) => `Connection failed (HTTP ${p.status}); reconnecting automatically`,
  'app.stream.reason.contentType': (p) => `Unexpected response type (${p.type}); reconnecting automatically`,
  'app.stream.reason.serverClosed': 'The server closed the connection; reconnecting automatically',
  'app.stream.reason.network': 'Network connection failed; reconnecting automatically',
};

export const catalogs: Record<Locale, Record<string, Message>> = {
  'zh-CN': zhCN,
  en,
};
