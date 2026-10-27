/**
 * drift 三态呈现（FR-14.5 第 1 组、FR-1.9、§4.2 条件表、§7.1）。
 *
 * 铁律（M5）：**绝不允许把 Unknown 渲染成"一致"**。三态分别是
 * `已确认一致（上次确认于 T）` / `未知或过期（原因）` / `已漂移（附 diff 入口）`，
 * 且 Unknown 必须带原因——原因取自后端 Drifted 条件的 reason（域常量
 * 见 internal/domain/drift.go）。
 */
import type { Condition, ConditionStatus, ConditionView, Machine, MachineStatus } from '../api/types';
import type { Tone } from './tone.js';
import { formatRelativeTime } from './format';
import { translate, type Locale } from '../i18n/messages.js';

export type DriftState = 'in-sync' | 'unknown' | 'drifted';

export type { Tone };

export interface DriftInput {
  drifted?: ConditionView | null;
  reconciled?: ConditionView | null;
  desiredProjectionDigest?: string;
  observedProjectionDigest?: string;
  canonicalizationVersion?: string;
  lastInventoryAt?: string;
  managementMode?: 'agentd' | 'ssh';
}

export interface DriftView {
  state: DriftState;
  label: string;
  /** Unknown 的展示原因（中文），或漂移的补充说明。 */
  detail?: string;
  /** 后端 reason code 原文（不翻译丢弃：审计与排障要用原码）。 */
  reasonCode?: string;
  tone: Tone;
  /** 上次确认一致的时刻（Drifted/Reconciled 条件最近一次转为 False 的时间）。 */
  confirmedAt?: string;
  desiredDigest?: string;
  observedDigest?: string;
  /** 后端是否暴露逐字段 diff。当前 API 只给摘要对，故恒为 false（见 detail）。 */
  diffAvailable: boolean;
}

/** Unknown reason code → 展示文案（§6.2 三种情形必须 Unknown；已知码按 locale 取文案）。 */
const KNOWN_UNKNOWN_REASONS = [
  'NeverInventoried',
  'StaleObservation',
  'ObservationPredatesDesired',
  'ProjectionVersionMismatch',
] as const;

export function driftReasonText(
  reason?: string,
  message?: string,
  locale: Locale = 'zh-CN',
): string {
  if (reason && (KNOWN_UNKNOWN_REASONS as readonly string[]).includes(reason))
    return translate(locale, `drift.reason.${reason}`);
  if (message) return message;
  if (reason) return reason;
  return translate(locale, 'drift.reason.unreported');
}

/** 从 Machine 资源抽取 drift 判定输入。 */
export function driftInputFromMachine(machine: Machine): DriftInput {
  const status: MachineStatus = machine.status ?? {};
  return {
    drifted: conditionView(status.conditions, 'Drifted'),
    reconciled: conditionView(status.conditions, 'Reconciled'),
    desiredProjectionDigest: status.desiredProjectionDigest,
    observedProjectionDigest: status.observedProjectionDigest,
    canonicalizationVersion: status.canonicalizationVersion,
    lastInventoryAt: status.lastInventoryAt,
    managementMode: (machine.spec?.managementMode as 'agentd' | 'ssh') ?? 'agentd',
  };
}

/**
 * 三态判定。判据与后端一致：Drifted 条件的三态是权威（后端按
 * `期望摘要 == 观测摘要 且 canonicalizationVersion 一致` 求值，FR-8.6），
 * 前端不重算、只呈现；缺少条件记录时按 Unknown/NeverInventoried 处理
 * （与 GET /machines/{name}/drift 的 conditionView 一致）。
 */
export function driftView(input: DriftInput, now: Date = new Date(), locale: Locale = 'zh-CN'): DriftView {
  const drifted = input.drifted ?? { status: 'Unknown' as ConditionStatus, reason: 'NeverInventoried' };
  const status = drifted.status;
  const confirmedAt = driftConfirmedAt(input);
  const msg = (key: string, params?: Record<string, string | number>) => translate(locale, key, params);
  const base = {
    desiredDigest: input.desiredProjectionDigest,
    observedDigest: input.observedProjectionDigest,
    confirmedAt,
    // 后端当前只暴露摘要对与条件，不暴露逐字段 diff（契约缺口，已在 KM-25 评论登记）。
    diffAvailable: false,
  };

  if (status === 'True') {
    const sameDigest =
      input.desiredProjectionDigest &&
      input.observedProjectionDigest &&
      input.desiredProjectionDigest === input.observedProjectionDigest;
    return {
      ...base,
      state: 'drifted',
      tone: 'danger',
      label: msg('drift.label.drifted'),
      reasonCode: drifted.reason,
      detail:
        drifted.message ||
        (sameDigest
          ? msg('drift.detail.sameDigest')
          : msg('drift.detail.digestMismatch')),
    };
  }

  if (status === 'False') {
    return {
      ...base,
      state: 'in-sync',
      tone: 'ok',
      label: msg('drift.label.inSync'),
      detail: confirmedAt
        ? msg('drift.detail.lastConfirmedAt', { time: formatRelativeTime(confirmedAt, now, locale) })
        : msg('drift.detail.lastConfirmedMissing'),
    };
  }

  // Unknown：必须给出原因，且不得渲染为"一致"。
  const rows: string[] = [driftReasonText(drifted.reason, drifted.message, locale)];
  if (input.managementMode === 'ssh') {
    rows.push(msg('drift.detail.sshModeExpected'));
  }
  if (confirmedAt) rows.push(msg('drift.detail.lastConfirmedAt', { time: formatRelativeTime(confirmedAt, now, locale) }));
  if (input.lastInventoryAt)
    rows.push(msg('drift.detail.lastObservedAt', { time: formatRelativeTime(input.lastInventoryAt, now, locale) }));
  return {
    ...base,
    state: 'unknown',
    tone: 'warn',
    label: msg('drift.label.unknown'),
    reasonCode: drifted.reason ?? 'NeverInventoried',
    detail: rows.join(locale === 'zh-CN' ? '；' : '; '),
  };
}

/** 从 GET /machines/{name}/drift 的响应构造输入。 */
export function driftInputFromApi(drift: {
  drifted?: ConditionView;
  reconciled?: ConditionView;
  desiredProjectionDigest?: string;
  observedProjectionDigest?: string;
  desiredGeneration?: number;
  observedGeneration?: number;
  lastInventoryAt?: string;
  evaluation?: { CanonicalizationVersion?: string };
}): DriftInput {
  return {
    drifted: drift.drifted,
    reconciled: drift.reconciled,
    desiredProjectionDigest: drift.desiredProjectionDigest,
    observedProjectionDigest: drift.observedProjectionDigest,
    canonicalizationVersion: drift.evaluation?.CanonicalizationVersion,
    lastInventoryAt: drift.lastInventoryAt,
  };
}

export function conditionView(
  conditions: Condition[] | undefined,
  type: Condition['type'],
): ConditionView | null {
  const found = conditions?.find((c) => c.type === type);
  if (!found) return null;
  return {
    status: found.status,
    reason: found.reason,
    message: found.message,
    lastTransitionTime: found.lastTransitionTime,
  };
}

/**
 * 上次确认时刻：Drifted 与 Reconciled 都为 False 时取两者中较早的转变时间
 * （"上次确认于 T"要能同时代表两侧判定）。
 */
export function driftConfirmedAt(input: DriftInput): string | undefined {
  const drifted = input.drifted;
  const reconciled = input.reconciled;
  if (drifted?.status !== 'False') return undefined;
  const times = [drifted.lastTransitionTime, reconciled?.status === 'False' ? reconciled.lastTransitionTime : undefined]
    .filter((t): t is string => Boolean(t))
    .sort();
  return times[0];
}

/** 六条件的三态呈现（Machine Detail 的联通/状态区块与 Machines 列表列）。 */
export interface ConditionSummary {
  type: Condition['type'];
  status: ConditionStatus;
  tone: Tone;
  label: string;
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

export const CONDITION_LABELS: Record<Condition['type'], string> = {
  SSHReachable: 'SSH 可达',
  AgentConnected: 'agentd 在线',
  InventoryReady: 'inventory 就绪',
  Drifted: '漂移',
  Reconciled: '已收敛',
  Degraded: '降级',
};

export function summarizeCondition(c: ConditionView & { type: Condition['type'] }): ConditionSummary {
  const type = c.type;
  const status = (c.status ?? 'Unknown') as ConditionStatus;
  return {
    type,
    status,
    tone: status === 'True' ? 'ok' : status === 'False' ? 'muted' : 'warn',
    label: `${CONDITION_LABELS[type] ?? type}: ${status}`,
    reason: c.reason,
    message: c.message,
    lastTransitionTime: c.lastTransitionTime,
  };
}

/** 六条件的固定顺序（§6.2 归属表顺序），缺失的条件显式呈现为 Unknown。 */
export const CONDITION_ORDER: Condition['type'][] = [
  'SSHReachable',
  'AgentConnected',
  'InventoryReady',
  'Drifted',
  'Reconciled',
  'Degraded',
];

export function allConditionSummaries(conditions: Condition[] | undefined): ConditionSummary[] {
  return CONDITION_ORDER.map((type) => {
    const view = conditionView(conditions, type);
    // conditionView 只带四要素（与 GET /drift 的响应同形），这里补回条件类型。
    return summarizeCondition(
      view
        ? { ...view, type }
        : { type, status: 'Unknown' as ConditionStatus, reason: 'NeverReported', message: '该条件尚未记录' },
    );
  });
}
