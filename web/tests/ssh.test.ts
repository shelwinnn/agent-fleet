/**
 * SSH 路径状态呈现：错误映射（业务 reason → 本地化文案，原码保留）、
 * 未交付功能的禁用口径、include install 结果的呈现。
 */
import { describe, expect, it } from 'vitest';
import { ApiError } from '../src/lib/api/client.js';
import {
  describeActionFailure,
  featureUnavailableNotice,
  failureText,
  formatSkippedHosts,
  hostKeyNotice,
  agentVersionNotice,
  loadErrorText,
} from '../src/lib/state/ssh.js';
import type { Condition, Machine } from '../src/lib/api/types.js';

describe('未交付功能的禁用口径', () => {
  it('如实说明尚未提供，不指向内部文档，也不假装可用', () => {
    const zh = featureUnavailableNotice();
    expect(zh).toContain('尚未提供');
    expect(zh).not.toContain('docs/');
    const en = featureUnavailableNotice('en');
    expect(en).toContain('not available');
    expect(en).not.toContain('docs/');
  });
});

describe('include install 结果的呈现', () => {
  it('skippedHosts 是服务端 SkippedHost（{Name,Reason}），按 Name（Reason）呈现而非 join 对象；分隔符随语言', () => {
    expect(
      formatSkippedHosts([
        { Name: 'km-agentd', Reason: 'hostName 未配置' },
        { Name: 'imported-only', Reason: '' },
      ]),
    ).toBe('km-agentd（hostName 未配置）、imported-only');
    expect(formatSkippedHosts([{ Name: 'a', Reason: 'no hostname' }], 'en')).toBe('a (no hostname)');
    expect(formatSkippedHosts([])).toBe('');
  });
});

describe('describeActionFailure（业务错误 → 本地化提示）', () => {
  it('409 MachineBusy / ReplanRequired 说明发生了什么与可采取的行动，两语言一致', () => {
    const busy = describeActionFailure(
      new ApiError(409, {
        reason: 'MachineBusy',
        message: 'machine has an unresolved operation',
        timestamp: '',
        diagnostics: { operationId: 'op-1', phase: 'Running' },
      }),
    );
    expect(busy.title).toContain('MachineBusy');
    expect(busy.detail).toContain('machine has an unresolved operation');
    expect(busy.detail).toContain('确认、取消或跳过');
    expect(busy.unresolvedOperationId).toBe('op-1');
    const busyEn = describeActionFailure(
      new ApiError(409, { reason: 'MachineBusy', message: 'busy', timestamp: '' }),
      'en',
    );
    expect(busyEn.detail).toContain('confirm, cancel or skip');

    const replan = describeActionFailure(new ApiError(409, { reason: 'ReplanRequired', message: 'baseline changed', timestamp: '' }));
    expect(replan.title).toContain('ReplanRequired');
    expect(replan.replanHint).toContain('重新生成计划');
  });

  it('400 Invalid 如实保留服务端拒绝理由，并给出检查输入的下一步', () => {
    const invalid = describeActionFailure(
      new ApiError(400, {
        reason: 'Invalid',
        message: 'resource invalid: manual inventory over the agentd channel is not wired in this slice',
        timestamp: '',
      }),
    );
    expect(invalid.title).toContain('Invalid');
    expect(invalid.detail).toContain('agentd channel');
    expect(invalid.detail).toContain('检查输入');
  });

  it('未识别 reason 原样保留原码，说明用通用文案；不坍缩传输分类', () => {
    const hostKey = describeActionFailure(
      new ApiError(502, { reason: 'HostKeyVerificationFailed', message: 'host key mismatch for localhost', timestamp: '' }),
    );
    expect(hostKey.title).toBe('操作失败（HostKeyVerificationFailed）');
    expect(hostKey.detail).toContain('host key mismatch');
    expect(hostKey.title).not.toBe('操作失败（MachineBusy）');

    const internal = describeActionFailure(
      new ApiError(502, {
        reason: 'Internal',
        message: 'resource invalid: ssh.user "o p" contains whitespace; refusing to render an unparseable OpenSSH include line',
        timestamp: '',
      }),
    );
    expect(internal.title).toContain('Internal');
    expect(internal.detail).toContain('contains whitespace');
  });

  it('非 ApiError 的未知失败给准确的通用说明，不透出原始异常文本', () => {
    const failure = describeActionFailure(new Error('network down'));
    expect(failure.title).toBe('请求失败');
    expect(failure.detail).toContain('意外');
    expect(failure.detail).not.toContain('network down');
    expect(failureText(new Error('network down'))).not.toContain('network down');
    const en = describeActionFailure(new Error('network down'), 'en');
    expect(en.title).toBe('Request failed');
    expect(en.detail).not.toContain('network down');
  });

  it('加载失败提示 = 对象 + 原因（loadErrorText 由页面拼对象名）', () => {
    const text = `机器列表加载失败${loadErrorText(new ApiError(500, { reason: 'Internal', message: 'boom', timestamp: '' }))}`;
    expect(text).toContain('机器列表加载失败');
    expect(text).toContain('Internal');
  });
});

function machineWith(conditions: Condition[]): Machine {
  return { metadata: { name: 'ws-1' }, spec: { managementMode: 'ssh' }, status: { conditions } } as Machine;
}

describe('SSH 通知（host-key / 版本协商）', () => {
  it('host-key 失败说明校验保持严格并给出核对指纹的下一步；两语言', () => {
    const m = machineWith([
      { type: 'SSHReachable', status: 'False', reason: 'HostKeyVerificationFailed', message: 'host key mismatch', lastTransitionTime: '2026-09-23T11:00:00Z' },
    ]);
    const zh = hostKeyNotice(m);
    expect(zh?.title).toBe('SSH host-key 校验失败');
    expect(zh?.detail).toContain('host key mismatch');
    expect(zh?.detail).toContain('指纹');
    expect(zh?.detail).toContain('reason=HostKeyVerificationFailed');
    const en = hostKeyNotice(m, 'en');
    expect(en?.title).toBe('SSH host-key verification failed');
    expect(en?.detail).toContain('never bypasses');
  });

  it('版本协商被拒说明服务端拒绝下发期望状态并要求升级', () => {
    const m = machineWith([
      { type: 'AgentConnected', status: 'Unknown', reason: 'AgentVersionIncompatible', message: 'daemon 0.3 not supported', lastTransitionTime: '2026-09-23T11:00:00Z' },
    ]);
    const zh = agentVersionNotice(m);
    expect(zh?.title).toBe('需升级 agentd');
    expect(zh?.detail).toContain('不受支持的 agentd 版本');
    expect(zh?.detail).toContain('请升级该机的 agentd');
    const en = agentVersionNotice(m, 'en');
    expect(en?.title).toBe('agentd upgrade required');
    expect(en?.detail).toContain('upgrade agentd');
  });
});
