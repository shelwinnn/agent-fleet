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

export const PHASE_LABELS: Record<OperationPhase, string> = {
  Pending: '待派发',
  Running: '执行中',
  AwaitingConfirmation: '等待确认',
  CancelRequested: '已请求取消',
  Succeeded: '成功',
  Failed: '失败',
  Unknown: '结果未知',
};

/** 相位语义提示（UI 必须说明"它会阻塞什么/如何处置"）。 */
export const PHASE_NOTES: Record<OperationPhase, string> = {
  Pending: '已创建但尚未被节点取得执行权；控制面互斥已持有，新的变更类操作会被拒绝。',
  Running: '节点正在执行（13 阶段流水线）；此时取消需节点确认停止。',
  AwaitingConfirmation: '计划已产出、等待操作者确认；节点不持有执行权，但控制面互斥仍持有（§9.3）。',
  CancelRequested: '已投递取消请求，等待节点确认停止；节点不可达时保持本相位，服务端不单方面置 Cancelled。',
  Succeeded: '终态。',
  Failed: '终态；失败原因与门禁证据见证据列。',
  Unknown:
    '节点结果未知（控制面重启或节点掉线）。它不是终态：仍占用机器级互斥，且不得由系统自动夺锁，需节点重报最终结果，或由操作者显式取消/跳过（路径 C）。',
};

export const MODIFIER_NOTES: Record<string, string> = {
  Superseded: '迟到成功：执行完成时该机期望代已前进，本次结果不再代表当前代（FR-9.10）。不满足"操作成功"门禁。',
  Cancelled: '节点确认停止后的取消终态。',
  Skipped: '操作者显式跳过：立即释放控制面互斥，但节点可能仍在写——跳过不表示节点已停止，且不计入成功。',
};

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
  Reconcile: '收敛',
  Repair: '修复 agentd',
  Bootstrap: '纳管',
  Rollback: '回滚',
  AutoPlan: '自动 plan',
};

export function viewOperation(op: Operation, now: Date = new Date()): OperationView {
  const phase = op.status?.phase ?? 'Unknown';
  const modifier = op.status?.terminalModifier;
  const expires = op.status?.planExpiresAt ? Date.parse(op.status.planExpiresAt) : undefined;
  const remaining = expires === undefined ? undefined : expires - now.getTime();
  const isAuto = op.spec?.type === 'AutoPlan' || op.spec?.readOnly === true;

  let tone: OperationView['tone'] = 'info';
  if (phase === 'Succeeded') tone = modifier ? 'warn' : 'ok';
  else if (phase === 'Failed') tone = 'danger';
  else if (phase === 'Unknown' || phase === 'CancelRequested') tone = 'warn';
  else if (phase === 'AwaitingConfirmation') tone = 'warn';

  return {
    id: op.metadata?.name ?? '',
    machine: op.spec?.machine ?? '',
    type: op.spec?.type,
    typeLabel: TYPE_LABELS[op.spec?.type] ?? op.spec?.type,
    transport: op.spec?.transport,
    phase,
    phaseLabel: PHASE_LABELS[phase] ?? phase,
    phaseNote: PHASE_NOTES[phase] ?? '',
    terminalModifier: modifier,
    modifierNote: modifier ? MODIFIER_NOTES[modifier] : undefined,
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

export function viewOperations(ops: Operation[], now: Date = new Date()): OperationView[] {
  return ops.map((op) => viewOperation(op, now));
}

export function unresolvedOperations(ops: Operation[], now: Date = new Date()): OperationView[] {
  return viewOperations(ops, now).filter((v) => v.isUnresolved);
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
 * 未决期间哪些动作被阻塞（FR-1.10/§9.3 第 1 条）以及三个例外端点。
 * 返回固定清单，供 Machine Detail 直接渲染——不做"按相位猜测"的隐式逻辑。
 */
export function actionAvailability(
  ops: Operation[],
  now: Date = new Date(),
): { blockedBy: { id: string; phase: OperationPhase } | null; actions: BlockedAction[] } {
  const unresolved = unresolvedOperations(ops, now);
  const holder = unresolved[0];
  const busy = Boolean(holder);
  const awaiting = holder?.phase === 'AwaitingConfirmation';
  return {
    blockedBy: holder ? { id: holder.id, phase: holder.phase } : null,
    actions: [
      {
        action: 'reconcile',
        label: 'Reconcile（含回滚到历史代）',
        blocked: busy,
        exception: awaiting ? '携带 confirmPlanDigest 的确认请求可用（同一操作行的状态迁移）' : undefined,
      },
      {
        action: 'rollback',
        label: 'Rollback（机器级回滚）',
        blocked: busy,
      },
      { action: 'deployment', label: 'Deployment 派发到该机', blocked: busy, exception: busy ? '该目标保持阻塞，不计入失败也不推进批次（FR-10.7）' : undefined },
      { action: 'auto-apply', label: '自动 apply', blocked: busy, exception: busy ? '存在未决操作的机器不参与自动 apply（§7.8 第 4 条）' : undefined },
      { action: 'cancel', label: '取消该操作', blocked: !busy, exception: busy ? '互斥例外端点之一（§4.3 第 6 条）' : undefined },
      { action: 'skip', label: '跳过该操作（须填原因）', blocked: !busy, exception: busy ? '互斥例外端点之一；留审计、不计入成功' : undefined },
      {
        action: 'confirm',
        label: '确认计划',
        blocked: !awaiting,
        exception: awaiting ? '互斥例外端点之一；确认不等于计划仍有效（apply 会重算并比对基线）' : undefined,
      },
    ],
  };
}

/** 跳过的后果说明（§9.6 路径 C；UI 必须显式标出，不允许静默跳过）。 */
export const SKIP_CONSEQUENCES =
  '跳过会立即释放控制面互斥，但**不表示节点已停止**：节点可能仍在写，目标实际状态不变，且本次不计入成功。操作者、时间、目标与原因会写入审计。';

/** 取消的后果说明（§9.3 第 5 条）。 */
export const CANCEL_CONSEQUENCES =
  '取消需节点确认停止（节点可达时）才离开未决集合；节点不可达时保持"已请求取消"，服务端不会单方面宣告已取消。AwaitingConfirmation 的计划取消直接终态（节点无可中断流水线）。';
