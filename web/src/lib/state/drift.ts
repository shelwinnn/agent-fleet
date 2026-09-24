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

/** Unknown reason code → 展示文案（§6.2 三种情形必须 Unknown）。 */
export const DRIFT_UNKNOWN_REASONS: Record<string, string> = {
  NeverInventoried: '从未采集受管投影（该机尚未完成 inventory）',
  StaleObservation: '观测已过期（超过新鲜度窗口）',
  ObservationPredatesDesired: '期望代已变化，尚未按新代采集',
  ProjectionVersionMismatch: '投影规范化版本不一致，不沿用旧判定（§7.1 契约 2）',
};

export function driftReasonText(reason?: string, message?: string): string {
  if (reason && DRIFT_UNKNOWN_REASONS[reason]) return DRIFT_UNKNOWN_REASONS[reason];
  if (message) return message;
  if (reason) return reason;
  return '原因未报告';
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
export function driftView(input: DriftInput, now: Date = new Date()): DriftView {
  const drifted = input.drifted ?? { status: 'Unknown' as ConditionStatus, reason: 'NeverInventoried' };
  const status = drifted.status;
  const confirmedAt = driftConfirmedAt(input);
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
      label: '已漂移',
      reasonCode: drifted.reason,
      detail:
        drifted.message ||
        (sameDigest
          ? '受管投影摘要相同但判定为漂移，请查看后端评测结果'
          : '受管投影摘要与期望不一致（逐字段 diff 未由 API 暴露，可通过 Reconcile 收敛）'),
    };
  }

  if (status === 'False') {
    return {
      ...base,
      state: 'in-sync',
      tone: 'ok',
      label: '已确认一致',
      detail: confirmedAt ? `上次确认于 ${formatRelativeTime(confirmedAt, now)}` : '上次确认时间未记录',
    };
  }

  // Unknown：必须给出原因，且不得渲染为"一致"。
  const rows: string[] = [driftReasonText(drifted.reason, drifted.message)];
  if (input.managementMode === 'ssh') {
    rows.push('SSH-only 机器在两次操作之间不做后台轮询，因此本态属预期（§6.2）');
  }
  if (confirmedAt) rows.push(`上次确认于 ${formatRelativeTime(confirmedAt, now)}`);
  if (input.lastInventoryAt) rows.push(`最近观测于 ${formatRelativeTime(input.lastInventoryAt, now)}`);
  return {
    ...base,
    state: 'unknown',
    tone: 'warn',
    label: '未知或过期',
    reasonCode: drifted.reason ?? 'NeverInventoried',
    detail: rows.join('；'),
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
