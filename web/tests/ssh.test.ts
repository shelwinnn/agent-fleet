/**
 * SSH 路径状态呈现（KM-28）：错误映射沿用第 5 片的 describeActionFailure，
 * 未实现端点的口径不再按"切片"叙述（docs/ssh-only-oneshot.md 登记遗留）。
 */
import { describe, expect, it } from 'vitest';
import { ApiError } from '../src/lib/api/client.js';
import {
  describeActionFailure,
  formatSkippedHosts,
  UNIMPLEMENTED_NOTICE,
  UNIMPLEMENTED_SSH_ENDPOINTS,
  UNIMPLEMENTED_SKILL_NOTICE,
} from '../src/lib/state/ssh.js';

describe('未实现端点的可用性口径', () => {
  it('清单只含仍未实现的动作，不含 KM-26 已注册的 probe/inventory/include', () => {
    expect(UNIMPLEMENTED_SSH_ENDPOINTS).toEqual([
      'POST /machines/{name}/ssh/bootstrap',
      'POST /machines/{name}/ssh/repair-agentd',
    ]);
    expect(UNIMPLEMENTED_NOTICE).toContain('docs/ssh-only-oneshot.md');
    // 旧口径必须绝迹：不再暗示"属第 N 片，下一片就会有"。
    expect(UNIMPLEMENTED_NOTICE).not.toContain('片');
  });

  it('Skill 的 resolve 不指向 ssh-only-oneshot.md（其登记在 docs/web-ui.md）', () => {
    expect(UNIMPLEMENTED_SKILL_NOTICE).toContain('docs/web-ui.md');
    expect(UNIMPLEMENTED_SKILL_NOTICE).not.toContain('ssh-only-oneshot');
  });
});

describe('include install 结果的呈现', () => {
  it('skippedHosts 是服务端 SkippedHost（{Name,Reason}），按 Name（Reason）呈现而非 join 对象', () => {
    expect(
      formatSkippedHosts([
        { Name: 'km-agentd', Reason: 'hostName 未配置' },
        { Name: 'imported-only', Reason: '' },
      ]),
    ).toBe('km-agentd（hostName 未配置）、imported-only');
    expect(formatSkippedHosts([])).toBe('');
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

  it('400 Invalid（agentd 通道手动采集等）如实呈现服务端拒绝理由', () => {
    const invalid = describeActionFailure(
      new ApiError(400, {
        reason: 'Invalid',
        message: 'resource invalid: manual inventory over the agentd channel is not wired in this slice',
        timestamp: '',
      }),
    );
    expect(invalid.title).toContain('400 Invalid');
    expect(invalid.detail).toContain('agentd channel');
  });

  it('502 原样透出 reason + message：传输分类不坍缩，include 渲染拒绝当前落在 Internal（状态码映射修复登记 KM-29）', () => {
    const hostKey = describeActionFailure(
      new ApiError(502, { reason: 'HostKeyVerificationFailed', message: 'host key mismatch for localhost', timestamp: '' }),
    );
    expect(hostKey.title).toBe('HostKeyVerificationFailed');
    expect(hostKey.detail).toContain('host key mismatch');

    const internal = describeActionFailure(
      new ApiError(502, {
        reason: 'Internal',
        message: 'resource invalid: ssh.user "o p" contains whitespace; refusing to render an unparseable OpenSSH include line (FR-12.6)',
        timestamp: '',
      }),
    );
    expect(internal.title).toBe('Internal');
    expect(internal.detail).toContain('contains whitespace');
  });

  it('非 ApiError 的意外失败不抛出，转普通提示', () => {
    const failure = describeActionFailure(new Error('network down'));
    expect(failure.title).toBe('请求失败');
    expect(failure.detail).toBe('network down');
  });
});
