/**
 * Deployment 呈现（FR-14.5 第 3 组、FR-10.x、§4.4、§7.3）。
 *
 * 关键区分：`Superseded`（机器已在更新代）与 `Skipped`（操作者显式跳过）必须
 * 与 `Failed` 分开显示，且各自带原因；跳过的目标不计入成功（§9.6 路径 C）。
 */
import type { Deployment, DeploymentTarget, DeploymentTargetPhase } from '../api/types';
import { formatRelativeTime } from './format';
import { translate, type Locale } from '../i18n/messages.js';

import type { Tone } from './tone.js';

export type { Tone };

/** 已知语义原因码（其余原样保留原码展示，不翻译丢弃）。 */
const KNOWN_TARGET_REASONS = ['SupersededByNewerGeneration', 'BlockedByNodeLock'] as const;

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

export function viewTarget(
  target: DeploymentTarget,
  now: Date = new Date(),
  locale: Locale = 'zh-CN',
): TargetView {
  const phase = target.phase;
  const tone: Tone =
    phase === 'Succeeded'
      ? 'ok'
      : phase === 'Failed'
        ? 'danger'
        : phase === 'Superseded' || phase === 'Skipped' || phase === 'Blocked'
          ? 'warn'
          : 'info';
  const msg = (key: string, params?: Record<string, string | number>) => translate(locale, key, params);
  const knownReason =
    target.reason && (KNOWN_TARGET_REASONS as readonly string[]).includes(target.reason)
      ? (target.reason as (typeof KNOWN_TARGET_REASONS)[number])
      : undefined;
  return {
    machine: target.machine,
    phase,
    phaseLabel: msg(`target.phase.${phase}`),
    tone,
    reason: target.reason,
    reasonText: target.reason
      ? knownReason
        ? msg(`target.reason.${knownReason}`)
        : msg('target.reason.unknown', { code: target.reason })
      : phase === 'Skipped'
        ? msg('target.skipped.noReason')
        : phase === 'Superseded'
          ? msg('target.superseded.noReason')
          : undefined,
    operationId: target.operationId,
    effectiveGeneration: target.effectiveGeneration,
    updatedAt: target.updatedAt,
    updatedAtText: target.updatedAt ? formatRelativeTime(target.updatedAt, now, locale) : undefined,
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

const KNOWN_DEPLOY_PHASES = ['Pending', 'Canary', 'RollingOut', 'Paused', 'Succeeded', 'Failed'] as const;

export function deploymentPhaseLabel(deployment: Deployment, locale: Locale = 'zh-CN'): string {
  const phase = deployment.status?.phase;
  if (!phase) return translate(locale, 'deploy.phase.none');
  // 后端新出现的 phase 原样展示，绝不把消息 key 露到界面上。
  return (KNOWN_DEPLOY_PHASES as readonly string[]).includes(phase)
    ? translate(locale, `deploy.phase.${phase}`)
    : phase;
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
