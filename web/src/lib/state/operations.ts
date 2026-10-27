/**
 * 未决操作与阻塞原因（FR-1.10、FR-14.5 第 2 组、§4.3、§9.3、§9.6）。
 *
 * 语义边界（逐条对应架构原文，不得在 UI 里改写）：
 *  - 未决集合 = Pending / Running / AwaitingConfirmation / CancelRequested / Unknown
 *    （§4.3 唯一索引谓词；AwaitingConfirmation 自 v1.1.2 起属于未决集合）；
 *  - 未决期间新的变更类操作默认被拒（409 MachineBusy），Deployment 目标阻塞；
 *  - 三个合法例外只对同一操作行做状态迁移：确认 / 取消 / 跳过（§4.3 第 6 条）；
 *  - 取消需节点确认停止（节点可达时）才离开未决集合；跳过立即释放控制面互斥，
 *    但**不表示节点已停止**、不改变节点实际状态、不计入成功（§9.3 第 5 条）；
 *  - Unknown 不是终态，不得自动夺锁（§5.6：StaleExecutionLock 交操作者处理）。
 */
import type { Operation, OperationPhase, UnresolvedOperationRef } from '../api/types';
import { translate, type Locale } from '../i18n/messages.js';

export const UNRESOLVED_PHASES: OperationPhase[] = [
  'Pending',
  'Running',
  'AwaitingConfirmation',
  'CancelRequested',
  'Unknown',
];

export function isUnresolvedPhase(phase: OperationPhase): boolean {
  return UNRESOLVED_PHASES.includes(phase);
}

export function isTerminalPhase(phase: OperationPhase): boolean {
  return phase === 'Succeeded' || phase === 'Failed';
}

/** 相位说明（UI 必须说明"它会阻塞什么/如何处置"）。Machine Detail 提示用。 */
export function phaseNote(phase: OperationPhase, locale: Locale = 'zh-CN'): string {
  return (UNRESOLVED_PHASES.includes(phase) || phase === 'Succeeded' || phase === 'Failed')
    ? translate(locale, `ops.note.${phase}`)
    : '';
}

/** 终态修饰的说明（Superseded / Cancelled / Skipped）。 */
export function modifierNote(modifier: string, locale: Locale = 'zh-CN'): string | undefined {
  return modifier === 'Superseded' || modifier === 'Cancelled' || modifier === 'Skipped'
    ? translate(locale, `ops.modifier.${modifier}`)
    : undefined;
}

/** 终态修饰的展示名（已知修饰取本地化文案，未知修饰原样保留）。 */
export function modifierLabel(modifier: string, locale: Locale = 'zh-CN'): string {
  return modifier === 'Superseded' || modifier === 'Cancelled' || modifier === 'Skipped'
    ? translate(locale, `ops.modifierLabel.${modifier}`)
    : modifier;
}

export type OperationSource = 'manual' | 'automatic';

export interface OperationView {
  id: string;
  machine: string;
  type: Operation['spec']['type'];
  typeLabel: string;
  transport?: string;
  phase: OperationPhase;
  phaseLabel: string;
  phaseNote: string;
  terminalModifier?: string;
  modifierLabel?: string;
  modifierNote?: string;
  /** 自动来源（AutoPlan，readOnly）与手动来源必须可区分（§7.8 第 3 条）。 */
  source: OperationSource;
  readOnly: boolean;
  isUnresolved: boolean;
  /** 是否计入"成功"：终态 Succeeded 且无 Superseded/Skipped 修饰。 */
  countsAsSuccess: boolean;
  desiredGeneration?: number;
  planDigest?: string;
  planExpiresAt?: string;
  /** 确认窗口剩余毫秒（AwaitingConfirmation）；未到期为正。 */
  planRemainingMs?: number;
  planExpired: boolean;
  startedAt?: string;
  finishedAt?: string;
  createdAt?: string;
  adapterHealth?: string;
  verifyInventorySeq?: number;
  tone: 'ok' | 'warn' | 'danger' | 'muted' | 'info';
}

const TYPE_LABELS: Record<string, string> = {
  Reconcile: 'ops.type.Reconcile',
  Repair: 'ops.type.Repair',
  Bootstrap: 'ops.type.Bootstrap',
  Rollback: 'ops.type.Rollback',
  AutoPlan: 'ops.type.AutoPlan',
};

export function viewOperation(op: Operation, now: Date = new Date(), locale: Locale = 'zh-CN'): OperationView {
  const phase = op.status?.phase ?? 'Unknown';
  const modifier = op.status?.terminalModifier;
  const expires = op.status?.planExpiresAt ? Date.parse(op.status.planExpiresAt) : undefined;
  const remaining = expires === undefined ? undefined : expires - now.getTime();
  const isAuto = op.spec?.type === 'AutoPlan' || op.spec?.readOnly === true;
  const opType = op.spec?.type;
  const msg = (key: string, params?: Record<string, string | number>) => translate(locale, key, params);

  let tone: OperationView['tone'] = 'info';
  if (phase === 'Succeeded') tone = modifier ? 'warn' : 'ok';
  else if (phase === 'Failed') tone = 'danger';
  else if (phase === 'Unknown' || phase === 'CancelRequested') tone = 'warn';
  else if (phase === 'AwaitingConfirmation') tone = 'warn';

  return {
    id: op.metadata?.name ?? '',
    machine: op.spec?.machine ?? '',
    type: opType,
    // 后端新出现的 type/phase 原样展示，绝不把消息 key 露到界面上。
    typeLabel: opType && TYPE_LABELS[opType] ? msg(TYPE_LABELS[opType]) : (opType ?? ''),
    transport: op.spec?.transport,
    phase,
    phaseLabel: operationPhaseLabel(phase, locale),
    phaseNote: phaseNote(phase, locale),
    terminalModifier: modifier,
    modifierLabel: modifier ? modifierLabel(modifier, locale) : undefined,
    modifierNote: modifier ? modifierNote(modifier, locale) : undefined,
    source: isAuto ? 'automatic' : 'manual',
    readOnly: op.spec?.readOnly === true,
    isUnresolved: isUnresolvedPhase(phase),
    countsAsSuccess: phase === 'Succeeded' && !modifier,
    desiredGeneration: op.spec?.desiredGeneration,
    planDigest: op.spec?.planDigest,
    planExpiresAt: op.status?.planExpiresAt,
    planRemainingMs: remaining,
    planExpired: remaining !== undefined && remaining <= 0,
    startedAt: op.status?.startedAt,
    finishedAt: op.status?.finishedAt,
    createdAt: op.metadata?.creationTimestamp,
    adapterHealth: op.status?.verify?.adapterHealth,
    verifyInventorySeq: op.status?.verify?.inventorySeq,
    tone,
  };
}

/** 相位展示名：已知相位按 locale 取文案，后端新出现的相位原样展示（不露消息 key）。 */
export function operationPhaseLabel(phase: OperationPhase, locale: Locale = 'zh-CN'): string {
  return (UNRESOLVED_PHASES.includes(phase) || phase === 'Succeeded' || phase === 'Failed')
    ? translate(locale, `ops.phase.${phase}`)
    : phase;
}

export function viewOperations(ops: Operation[], now: Date = new Date(), locale: Locale = 'zh-CN'): OperationView[] {
  return ops.map((op) => viewOperation(op, now, locale));
}

export function unresolvedOperations(ops: Operation[], now: Date = new Date(), locale: Locale = 'zh-CN'): OperationView[] {
  return viewOperations(ops, now, locale).filter((v) => v.isUnresolved);
}

/** 机器级互斥的持有者（优先用 status.unresolvedOperation，回退到操作列表）。 */
export function unresolvedRef(
  ops: Operation[],
  statusRef?: UnresolvedOperationRef,
  now: Date = new Date(),
): { id: string; phase: OperationPhase; type?: string } | null {
  const fromList = unresolvedOperations(ops, now)[0];
  if (fromList) return { id: fromList.id, phase: fromList.phase, type: fromList.type };
  if (statusRef) return { id: statusRef.id, phase: statusRef.phase, type: statusRef.type };
  return null;
}

export interface BlockedAction {
  action: string;
  label: string;
  blocked: boolean;
  /** 未被阻塞时的例外说明（确认/取消/跳过）。 */
  exception?: string;
}

/**
 * 未决期间哪些动作被阻塞以及三个例外端点。
 * 返回固定清单，供 Machine Detail 直接渲染——不做"按相位猜测"的隐式逻辑。
 */
export function actionAvailability(
  ops: Operation[],
  now: Date = new Date(),
  locale: Locale = 'zh-CN',
): { blockedBy: { id: string; phase: OperationPhase } | null; actions: BlockedAction[] } {
  const unresolved = unresolvedOperations(ops, now);
  const holder = unresolved[0];
  const busy = Boolean(holder);
  const awaiting = holder?.phase === 'AwaitingConfirmation';
  const msg = (key: string, params?: Record<string, string | number>) => translate(locale, key, params);
  return {
    blockedBy: holder ? { id: holder.id, phase: holder.phase } : null,
    actions: [
      {
        action: 'reconcile',
        label: msg('ops.action.reconcile'),
        blocked: busy,
        exception: awaiting ? msg('ops.exception.reconcileWhileAwaiting') : undefined,
      },
      {
        action: 'rollback',
        label: msg('ops.action.rollback'),
        blocked: busy,
      },
      { action: 'deployment', label: msg('ops.action.deployment'), blocked: busy, exception: busy ? msg('ops.exception.deploymentWhileBusy') : undefined },
      { action: 'auto-apply', label: msg('ops.action.autoApply'), blocked: busy, exception: busy ? msg('ops.exception.autoApplyWhileBusy') : undefined },
      { action: 'cancel', label: msg('ops.action.cancel'), blocked: !busy, exception: busy ? msg('ops.exception.cancelWhileBusy') : undefined },
      { action: 'skip', label: msg('ops.action.skip'), blocked: !busy, exception: busy ? msg('ops.exception.skipWhileBusy') : undefined },
      {
        action: 'confirm',
        label: msg('ops.action.confirm'),
        blocked: !awaiting,
        exception: awaiting ? msg('ops.exception.confirmWhileAwaiting') : undefined,
      },
    ],
  };
}

/** 跳过的后果说明（UI 必须显式标出，不允许静默跳过）。 */
export function skipConsequences(locale: Locale = 'zh-CN'): string {
  return translate(locale, 'ops.skipConsequences');
}

/** 取消的后果说明。 */
export function cancelConsequences(locale: Locale = 'zh-CN'): string {
  return translate(locale, 'ops.cancelConsequences');
}
