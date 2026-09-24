/**
 * Deployment 呈现（FR-14.5 第 3 组、FR-10.x、§4.4、§7.3）。
 *
 * 关键区分：`Superseded`（机器已在更新代）与 `Skipped`（操作者显式跳过）必须
 * 与 `Failed` 分开显示，且各自带原因；跳过的目标不计入成功（§9.6 路径 C）。
 */
import type { Deployment, DeploymentTarget, DeploymentTargetPhase } from '../api/types';
import { formatRelativeTime } from './format';

import type { Tone } from './tone.js';

export type { Tone };

export const TARGET_PHASE_LABELS: Record<DeploymentTargetPhase, string> = {
  Pending: '待推进',
  Running: '执行中',
  Succeeded: '成功',
  Failed: '失败',
  Superseded: '已被取代',
  Skipped: '已跳过',
  Blocked: '被阻塞',
};

/** 目标 phase 的语义原因（后端 reason code 优先，未识别时原样显示）。 */
export const TARGET_REASONS: Record<string, string> = {
  SupersededByNewerGeneration:
    '该机已在更新代：本次发布的目标代已过期，不再重放（FR-9.10/FR-10.6）。这不是失败。',
  BlockedByNodeLock: '节点执行权忙（§5.6 规则 3）：排队等待，不计入失败也不推进批次。',
};

export interface TargetView {
  machine: string;
  phase: DeploymentTargetPhase;
  phaseLabel: string;
  tone: Tone;
  reason?: string;
  reasonText?: string;
  operationId?: string;
  effectiveGeneration?: number;
  updatedAt?: string;
  updatedAtText?: string;
  /** 是否计入"成功"：只有无修饰的 Succeeded 计入（Superseded/Skipped 都不计）。 */
  countsAsSuccess: boolean;
  /** 是否阻塞批次推进（FR-10.7：未决目标不推进）。 */
  blocksBatch: boolean;
}

export function viewTarget(target: DeploymentTarget, now: Date = new Date()): TargetView {
  const phase = target.phase;
  const tone: Tone =
    phase === 'Succeeded'
      ? 'ok'
      : phase === 'Failed'
        ? 'danger'
        : phase === 'Superseded' || phase === 'Skipped' || phase === 'Blocked'
          ? 'warn'
          : 'info';
  return {
    machine: target.machine,
    phase,
    phaseLabel: TARGET_PHASE_LABELS[phase] ?? phase,
    tone,
    reason: target.reason,
    reasonText: target.reason
      ? (TARGET_REASONS[target.reason] ?? `后端原因码：${target.reason}`)
      : phase === 'Skipped'
        ? '操作者显式跳过：留审计、不计入成功，且不改变节点实际状态（节点可能仍在写）。'
        : phase === 'Superseded'
          ? '被更新代取代（未附原因码）'
          : undefined,
    operationId: target.operationId,
    effectiveGeneration: target.effectiveGeneration,
    updatedAt: target.updatedAt,
    updatedAtText: target.updatedAt ? formatRelativeTime(target.updatedAt, now) : undefined,
    countsAsSuccess: phase === 'Succeeded',
    blocksBatch: phase === 'Blocked' || phase === 'Running' || phase === 'Pending',
  };
}

export interface DeploymentProgress {
  total: number;
  succeeded: number;
  failed: number;
  superseded: number;
  skipped: number;
  blocked: number;
  running: number;
  pending: number;
  /** 已终结且计入成功的比例（Superseded/Skipped 不计入分子）。 */
  succeededPercent: number;
}

export function deploymentProgress(deployment: Deployment): DeploymentProgress {
  const targets = deployment.status?.targets ?? [];
  const count = (phase: DeploymentTargetPhase) => targets.filter((t) => t.phase === phase).length;
  const succeeded = count('Succeeded');
  return {
    total: targets.length,
    succeeded,
    failed: count('Failed'),
    superseded: count('Superseded'),
    skipped: count('Skipped'),
    blocked: count('Blocked'),
    running: count('Running'),
    pending: count('Pending'),
    succeededPercent: targets.length === 0 ? 0 : Math.round((succeeded / targets.length) * 100),
  };
}

export function deploymentPhaseLabel(deployment: Deployment): string {
  const phase = deployment.status?.phase;
  const labels: Record<string, string> = {
    Pending: '待开始',
    Canary: '金丝雀',
    RollingOut: '滚动中',
    Paused: '已暂停',
    Succeeded: '成功',
    Failed: '失败',
  };
  return phase ? (labels[phase] ?? phase) : '未开始';
}

/**
 * 回滚型 Deployment 的展示文案（§7.2：回滚不把机器拉回旧代）。
 * 形如：回滚自 gen14 至 gen12 的内容（逐机物化为新代）。
 */
export function rollbackLabel(deployment: Deployment): string | null {
  const rollbackOf = deployment.spec?.rollbackOf;
  if (rollbackOf === undefined || rollbackOf === null) return null;
  const target = deployment.spec?.targetGeneration;
  return `回滚自 gen${rollbackOf} 至 gen${target ?? '?'} 的内容（逐机物化为新代，代计数不回退）`;
}

/** 自动/手动来源与批次策略摘要（§6.1 策略字段）。 */
export function strategyLabel(deployment: Deployment): string {
  const s = deployment.spec?.strategy ?? {};
  const parts: string[] = [];
  if (s.canary) parts.push(`金丝雀 ${s.canary} 台`);
  if (s.batchSize) parts.push(`批大小 ${s.batchSize}`);
  if (s.maxUnavailable) parts.push(`同批上限 ${s.maxUnavailable}`);
  parts.push(s.pauseOnFailure ? '失败即暂停' : '失败不自动暂停');
  return parts.join(' · ');
}
