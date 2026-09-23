/**
 * Deployment 目标异常态（FR-14.5 第 3 组）：Superseded/Skipped 与 Failed 区分显示。
 */
import { describe, expect, it } from 'vitest';
import {
  deploymentPhaseLabel,
  deploymentProgress,
  rollbackLabel,
  strategyLabel,
  viewTarget,
} from '../src/lib/state/deployments.js';
import type { Deployment, DeploymentTarget } from '../src/lib/api/types.js';

const target = (phase: DeploymentTarget['phase'], extra: Partial<DeploymentTarget> = {}): DeploymentTarget => ({
  machine: 'ws-1',
  phase,
  ...extra,
});

describe('Deployment 目标呈现', () => {
  it('Superseded 与 Skipped 各有独立标签与原因，且不计入成功', () => {
    const superseded = viewTarget(
      target('Superseded', { reason: 'SupersededByNewerGeneration' }),
      new Date('2026-09-23T12:00:00Z'),
    );
    expect(superseded.phaseLabel).toBe('已被取代');
    expect(superseded.countsAsSuccess).toBe(false);
    expect(superseded.reasonText).toContain('已在更新代');
    expect(superseded.reasonText).toContain('不是失败');
    expect(superseded.tone).not.toBe('danger');

    const skipped = viewTarget(target('Skipped'), new Date('2026-09-23T12:00:00Z'));
    expect(skipped.phaseLabel).toBe('已跳过');
    expect(skipped.countsAsSuccess).toBe(false);
    expect(skipped.reasonText).toContain('不计入成功');
    expect(skipped.tone).not.toBe('danger');
  });

  it('Failed 是 danger，且不等于被取代/跳过', () => {
    const failed = viewTarget(target('Failed', { reason: 'VerifyFailed' }), new Date());
    expect(failed.tone).toBe('danger');
    expect(failed.phaseLabel).toBe('失败');
    expect(failed.reasonText).toBe('后端原因码：VerifyFailed');
  });

  it('Blocked 目标阻塞批次推进且不计入失败', () => {
    const blocked = viewTarget(target('Blocked', { reason: 'BlockedByNodeLock' }), new Date());
    expect(blocked.blocksBatch).toBe(true);
    expect(blocked.reasonText).toContain('不计入失败');
  });

  it('进度统计把取代/跳过单列，且不计入成功分子', () => {
    const dep: Deployment = {
      metadata: { name: 'rollout-1' },
      status: {
        phase: 'RollingOut',
        targets: [
          target('Succeeded', { machine: 'a' }),
          target('Superseded', { machine: 'b' }),
          target('Skipped', { machine: 'c' }),
          target('Failed', { machine: 'd' }),
        ],
      },
    };
    const progress = deploymentProgress(dep);
    expect(progress.total).toBe(4);
    expect(progress.succeeded).toBe(1);
    expect(progress.superseded).toBe(1);
    expect(progress.skipped).toBe(1);
    expect(progress.failed).toBe(1);
    expect(progress.succeededPercent).toBe(25);
    expect(deploymentPhaseLabel(dep)).toBe('滚动中');
  });

  it('回滚型发布显示"自 genX 至 genY 的内容（新代）"', () => {
    const dep: Deployment = {
      metadata: { name: 'rollout-2' },
      spec: { rollbackOf: 14, targetGeneration: 12, strategy: { canary: 1, batchSize: 2, pauseOnFailure: true } },
    };
    expect(rollbackLabel(dep)).toContain('回滚自 gen14 至 gen12 的内容');
    expect(rollbackLabel(dep)).toContain('代计数不回退');
    expect(strategyLabel(dep)).toBe('金丝雀 1 台 · 批大小 2 · 失败即暂停');
    expect(rollbackLabel({ metadata: { name: 'x' } })).toBeNull();
  });
});
