/**
 * 未决操作与阻塞语义（三态呈现、未决集合、动作可用性与后果说明）。
 */
import { describe, expect, it } from 'vitest';
import {
  actionAvailability,
  isUnresolvedPhase,
  modifierNote,
  operationPhaseLabel,
  phaseNote,
  skipConsequences,
  unresolvedOperations,
  unresolvedRef,
  viewOperation,
} from '../src/lib/state/operations.js';
import type { Operation, OperationPhase } from '../src/lib/api/types.js';

const NOW = new Date('2026-09-23T12:00:00Z');

function op(phase: OperationPhase, extra: Partial<Operation> = {}): Operation {
  return {
    metadata: { name: 'op-1', creationTimestamp: '2026-09-23T11:00:00Z' },
    spec: { machine: 'ws-1', type: 'Reconcile', transport: 'agentd' },
    status: { phase },
    ...extra,
  };
}

describe('操作相位呈现', () => {
  it('未决集合与 §4.3 唯一索引谓词一致（含 AwaitingConfirmation/Unknown）', () => {
    expect(isUnresolvedPhase('Pending')).toBe(true);
    expect(isUnresolvedPhase('Running')).toBe(true);
    expect(isUnresolvedPhase('AwaitingConfirmation')).toBe(true);
    expect(isUnresolvedPhase('CancelRequested')).toBe(true);
    expect(isUnresolvedPhase('Unknown')).toBe(true);
    expect(isUnresolvedPhase('Succeeded')).toBe(false);
    expect(isUnresolvedPhase('Failed')).toBe(false);
  });

  it('带修饰的成功不计入成功（Skipped/Superseded），修饰说明可本地化', () => {
    const skipped = viewOperation(op('Succeeded', { status: { phase: 'Succeeded', terminalModifier: 'Skipped' } }), NOW);
    expect(skipped.countsAsSuccess).toBe(false);
    expect(skipped.tone).toBe('warn');
    // 跳过的说明必须讲清风险：机器可能仍在写入、不计入成功。
    expect(skipped.modifierNote).toContain('可能仍在写入');
    expect(skipped.modifierNote).toContain('不计入成功');
    expect(modifierNote('Skipped', 'en')).toContain('may still be writing');

    const plain = viewOperation(op('Succeeded'), NOW);
    expect(plain.countsAsSuccess).toBe(true);
    expect(plain.tone).toBe('ok');
  });

  it('自动来源与手动来源可区分（AutoPlan/readOnly）', () => {
    expect(viewOperation(op('Succeeded', { spec: { machine: 'ws-1', type: 'AutoPlan', readOnly: true } }), NOW).source).toBe(
      'automatic',
    );
    expect(viewOperation(op('Succeeded'), NOW).source).toBe('manual');
  });

  it('AwaitingConfirmation 呈现剩余确认时间与到期标记', () => {
    const live = viewOperation(
      op('AwaitingConfirmation', {
        spec: { machine: 'ws-1', type: 'Reconcile', planDigest: 'sha256:plan' },
        status: { phase: 'AwaitingConfirmation', planExpiresAt: '2026-09-23T12:10:00Z' },
      }),
      NOW,
    );
    expect(live.planExpired).toBe(false);
    expect(live.planRemainingMs).toBe(600_000);
    expect(live.planDigest).toBe('sha256:plan');

    const expired = viewOperation(
      op('AwaitingConfirmation', {
        status: { phase: 'AwaitingConfirmation', planExpiresAt: '2026-09-23T11:59:00Z' },
      }),
      NOW,
    );
    expect(expired.planExpired).toBe(true);
  });
});

describe('未决操作与阻塞', () => {
  it('未决期间变更类动作被阻塞，取消/跳过可用；非未决时相反', () => {
    const busy = actionAvailability([op('Unknown')], NOW);
    expect(busy.blockedBy?.id).toBe('op-1');
    const byAction = Object.fromEntries(busy.actions.map((a) => [a.action, a.blocked]));
    expect(byAction.reconcile).toBe(true);
    expect(byAction.rollback).toBe(true);
    expect(byAction.deployment).toBe(true);
    expect(byAction['auto-apply']).toBe(true);
    expect(byAction.cancel).toBe(false);
    expect(byAction.skip).toBe(false);
    expect(byAction.confirm).toBe(true); // 只有 AwaitingConfirmation 可确认

    const idle = actionAvailability([op('Succeeded')], NOW);
    expect(idle.blockedBy).toBeNull();
    expect(idle.actions.find((a) => a.action === 'reconcile')?.blocked).toBe(false);
    expect(idle.actions.find((a) => a.action === 'cancel')?.blocked).toBe(true);
  });

  it('确认仅在 AwaitingConfirmation 时可用', () => {
    const awaiting = actionAvailability([op('AwaitingConfirmation')], NOW);
    expect(awaiting.actions.find((a) => a.action === 'confirm')?.blocked).toBe(false);
  });

  it('unresolvedRef 优先使用操作列表，回退到 status.unresolvedOperation', () => {
    expect(unresolvedRef([op('Running')], undefined, NOW)?.id).toBe('op-1');
    expect(unresolvedRef([], { id: 'op-9', phase: 'CancelRequested' }, NOW)?.id).toBe('op-9');
    expect(unresolvedRef([], undefined, NOW)).toBeNull();
  });

  it('只把未决相位计入未决列表', () => {
    const ops = [op('Succeeded'), op('Failed'), op('Running', { metadata: { name: 'op-2' } })];
    expect(unresolvedOperations(ops, NOW).map((o) => o.id)).toEqual(['op-2']);
  });

  it('跳过后果必须写明"不代表机器已停止"且不假装成功（两语言）', () => {
    const zh = skipConsequences();
    expect(zh).toContain('不代表机器已停止');
    expect(zh).toContain('不计入成功');
    expect(zh).toContain('审计');
    const en = skipConsequences('en');
    expect(en).toContain('does not mean the machine has stopped');
    expect(en).toContain('does not count as success');
  });

  it('相位与修饰的说明随语言：未知相位要讲清锁仍被占用与可采取的行动', () => {
    const zhUnknown = phaseNote('Unknown');
    expect(zhUnknown).toContain('未知');
    expect(zhUnknown).toContain('机器锁');
    expect(phaseNote('Unknown', 'en')).toContain('lock is still held');
    // 后端新出现的相位没有说明，也不得编造。
    expect(phaseNote('BrandNewPhase' as OperationPhase)).toBe('');
    expect(operationPhaseLabel('BrandNewPhase' as OperationPhase)).toBe('BrandNewPhase');
  });

  it('动作清单的标签与例外说明随语言（zh 默认 / en）', () => {
    const busy = actionAvailability([op('Unknown')], NOW);
    const reconcile = busy.actions.find((a) => a.action === 'reconcile')!;
    expect(reconcile.label).toBe('发起收敛');
    expect(busy.actions.find((a) => a.action === 'deployment')!.exception).toContain('不计入失败');
    const enBusy = actionAvailability([op('AwaitingConfirmation')], NOW, 'en');
    expect(enBusy.actions.find((a) => a.action === 'reconcile')!.label).toBe('Reconcile');
    expect(enBusy.actions.find((a) => a.action === 'confirm')!.exception).toContain('re-checked at execution');
  });
});
