/**
 * FR-14.5 的核心断言：drift 三态绝不混用（Unknown 不得渲染成"一致"）。
 */
import { describe, expect, it } from 'vitest';
import {
  conditionView,
  driftInputFromMachine,
  driftReasonText,
  driftView,
} from '../src/lib/state/drift.js';
import type { Condition, Machine } from '../src/lib/api/types.js';

const NOW = new Date('2026-09-23T12:00:00Z');

function machine(conditions: Condition[], status: Record<string, unknown> = {}): Machine {
  return {
    metadata: { name: 'ws-1' },
    spec: { managementMode: 'agentd', profileRef: 'default' },
    status: { conditions, ...status },
  };
}

const cond = (type: Condition['type'], status: Condition['status'], extra: Partial<Condition> = {}): Condition => ({
  type,
  status,
  lastTransitionTime: '2026-09-23T11:30:00Z',
  ...extra,
});

describe('drift 三态', () => {
  it('Drifted=False → 已确认一致，并给出上次确认时间', () => {
    const view = driftView(
      driftInputFromMachine(
        machine(
          [cond('Drifted', 'False'), cond('Reconciled', 'False')],
          { desiredProjectionDigest: 'sha256:aa', observedProjectionDigest: 'sha256:aa' },
        ),
      ),
      NOW,
    );
    expect(view.state).toBe('in-sync');
    expect(view.label).toBe('已确认一致');
    expect(view.detail).toContain('上次确认于');
    expect(view.confirmedAt).toBe('2026-09-23T11:30:00Z');
  });

  it('Drifted=Unknown 携带原因，且绝不显示为"已确认一致"', () => {
    for (const [reason, expected] of [
      ['NeverInventoried', '从未采集'],
      ['StaleObservation', '观测已过期'],
      ['ObservationPredatesDesired', '期望代已变化'],
      ['ProjectionVersionMismatch', '规范化版本不一致'],
    ] as const) {
      const view = driftView(
        driftInputFromMachine(machine([cond('Drifted', 'Unknown', { reason })])),
        NOW,
      );
      expect(view.state).toBe('unknown');
      expect(view.tone).toBe('warn');
      expect(view.reasonCode).toBe(reason);
      expect(view.detail).toContain(expected);
      expect(view.label).not.toContain('一致');
      expect(view.state).not.toBe('in-sync');
    }
  });

  it('缺少 Drifted 条件记录 → Unknown/NeverInventoried（与后端 conditionView 一致）', () => {
    const view = driftView(driftInputFromMachine(machine([])), NOW);
    expect(view.state).toBe('unknown');
    expect(view.reasonCode).toBe('NeverInventoried');
  });

  it('Drifted=True → 已漂移，附两侧摘要，且不声称有逐字段 diff', () => {
    const view = driftView(
      driftInputFromMachine(
        machine([cond('Drifted', 'True')], {
          desiredProjectionDigest: 'sha256:aa',
          observedProjectionDigest: 'sha256:bb',
        }),
      ),
      NOW,
    );
    expect(view.state).toBe('drifted');
    expect(view.tone).toBe('danger');
    expect(view.desiredDigest).toBe('sha256:aa');
    expect(view.observedDigest).toBe('sha256:bb');
    expect(view.diffAvailable).toBe(false);
  });

  it('SSH-only 机器的 Unknown 会说明这是预期语义（§6.2）', () => {
    const view = driftView(
      {
        drifted: { status: 'Unknown', reason: 'StaleObservation' },
        managementMode: 'ssh',
        lastInventoryAt: '2026-09-23T10:00:00Z',
      },
      NOW,
    );
    expect(view.state).toBe('unknown');
    expect(view.detail).toContain('SSH-only');
  });

  it('未知 reason code 也不丢失原码', () => {
    expect(driftReasonText('SomeNewReason', '后端消息')).toBe('后端消息');
    expect(driftReasonText('SomeNewReason')).toBe('SomeNewReason');
  });

  it('conditionView 读取条件三态与原因', () => {
    const view = conditionView([cond('Degraded', 'True', { reason: 'RepeatedFailure' })], 'Degraded');
    expect(view).toEqual({
      status: 'True',
      reason: 'RepeatedFailure',
      message: undefined,
      lastTransitionTime: '2026-09-23T11:30:00Z',
    });
  });
});
