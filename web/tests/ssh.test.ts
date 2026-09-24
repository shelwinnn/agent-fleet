/**
 * SSH 路径状态呈现（KM-28）：错误映射沿用第 5 片的 describeActionFailure，
 * 未实现端点的口径不再按"切片"叙述（docs/ssh-only-oneshot.md 登记遗留）。
 */
import { describe, expect, it } from 'vitest';
import { ApiError } from '../src/lib/api/client.js';
import { describeActionFailure, UNIMPLEMENTED_ACTION_ENDPOINTS, UNIMPLEMENTED_NOTICE } from '../src/lib/state/ssh.js';

describe('未实现端点的可用性口径', () => {
  it('清单只含仍未实现的动作，不含 KM-26 已注册的 probe/inventory/include', () => {
    expect(UNIMPLEMENTED_ACTION_ENDPOINTS).toEqual([
      'POST /machines/{name}/bootstrap',
      'POST /machines/{name}/repair-agentd',
      'POST /skills/{name}/resolve',
    ]);
    expect(UNIMPLEMENTED_NOTICE).toContain('docs/ssh-only-oneshot.md');
    // 旧口径必须绝迹：不再暗示"属第 N 片，下一片就会有"。
    expect(UNIMPLEMENTED_NOTICE).not.toContain('片');
  });
});

describe('describeActionFailure（§6.4 错误体 → UI 提示）', () => {
  it('409 MachineBusy / ReplanRequired 沿用既有语义', () => {
    const busy = describeActionFailure(
      new ApiError(409, {
        reason: 'MachineBusy',
        message: 'machine has an unresolved operation',
        timestamp: '',
        diagnostics: { operationId: 'op-1', phase: 'Running' },
      }),
    );
    expect(busy.title).toContain('MachineBusy');
    expect(busy.unresolvedOperationId).toBe('op-1');

    const replan = describeActionFailure(new ApiError(409, { reason: 'ReplanRequired', message: 'baseline changed', timestamp: '' }));
    expect(replan.title).toContain('ReplanRequired');
    expect(replan.replanHint).toBeDefined();
  });

  it('400 Invalid（如 include 值含空白/引号、agentd 通道手动采集）如实呈现服务端拒绝理由', () => {
    const invalid = describeActionFailure(
      new ApiError(400, {
        reason: 'Invalid',
        message: 'user "o p" contains whitespace; refusing to render an unparseable OpenSSH include line (FR-12.6)',
        timestamp: '',
      }),
    );
    expect(invalid.title).toContain('400 Invalid');
    expect(invalid.detail).toContain('contains whitespace');
  });

  it('502 传输分类 reason 原样透出（host-key / DNS / 超时不坍缩为通用失败）', () => {
    const err = describeActionFailure(
      new ApiError(502, { reason: 'HostKeyVerificationFailed', message: 'host key mismatch for localhost', timestamp: '' }),
    );
    expect(err.title).toBe('HostKeyVerificationFailed');
    expect(err.detail).toContain('host key mismatch');
  });

  it('非 ApiError 的意外失败不抛出，转普通提示', () => {
    const failure = describeActionFailure(new Error('network down'));
    expect(failure.title).toBe('请求失败');
    expect(failure.detail).toBe('network down');
  });
});
