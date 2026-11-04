/**
 * 生命周期视图模型与契约 mock（KM-50）。
 *
 * 覆盖验收语义：精确版本校验、能力矩阵（未验证不标支持、阻塞组合无入口）、
 * 逐目标四态+原因+下一步（未知不计成功）、上游查询（未知 ≠ 最新）、期望状态
 * 偏差（省略 ≠ 卸载）、影响指纹（影响变化重新确认）、仅失败目标可重试。
 */
import { describe, expect, it } from 'vitest';
import {
  capabilityFor,
  capabilityTone,
  deviationView,
  desiredAbsentUpdate,
  desiredEntryFor,
  desiredPresentUpdate,
  desiredUnmanagedUpdate,
  failedResults,
  impactFingerprint,
  instanceKey,
  instancesFor,
  isExactVersion,
  isOperationSettled,
  lifecycleStateLabel,
  lifecycleStateTone,
  methodOfferings,
  needsReconfirmationView,
  nextStepLabel,
  operationProgress,
  targetReasonLabel,
  targetResultViews,
  upstreamFailureLabel,
  upstreamRowView,
  formatOpTitle,
  CODEX_AGENT,
  buildTarget,
} from '../src/lib/state/lifecycle.js';
import {
  MockLifecycleBackend,
  MOCK_CAPABILITIES,
  mockInventory,
  mockUpstreamReport,
} from '../src/lib/api/lifecycleMock.js';
import { translate } from '../src/lib/i18n/messages.js';
import type { LifecycleOperation, LifecycleTarget } from '../src/lib/api/types.js';

const zh = 'zh-CN' as const;
const en = 'en' as const;

function opWithResults(states: [string, string?][]): LifecycleOperation {
  return {
    metadata: { name: 'lc-op-1', creationTimestamp: '2026-10-29T08:00:00Z' },
    spec: { type: 'Upgrade', idempotencyKey: 'k1', version: '0.56.0', targets: [] },
    status: {
      phase: 'Completed',
      results: states.map(([state, reason], i) => ({
        target: buildTarget({ machine: `m-${i}`, env: '', user: 'u', method: 'npm' }),
        state: state as never,
        reason,
      })),
    },
  };
}

describe('精确固定版本校验', () => {
  it('接受 x.y.z 与预发布/构建元数据；拒绝 latest、范围与残缺版本', () => {
    expect(isExactVersion('0.56.1')).toBe(true);
    expect(isExactVersion('1.2.3-beta.1')).toBe(true);
    expect(isExactVersion('1.2.3+build.5')).toBe(true);
    expect(isExactVersion('latest')).toBe(false);
    expect(isExactVersion('^0.56.0')).toBe(false);
    expect(isExactVersion('>=0.56')).toBe(false);
    expect(isExactVersion('0.56')).toBe(false);
    expect(isExactVersion('')).toBe(false);
  });
});

describe('能力矩阵呈现', () => {
  it('按 os/arch/method 找到能力；unverified 提供入口、blocked 只展示不提供', () => {
    const cap = capabilityFor(MOCK_CAPABILITIES, 'linux', 'amd64', 'npm');
    expect(cap?.state).toBe('unverified');
    expect(cap?.evidenceLevel).toBe('isolated');

    const win = methodOfferings(MOCK_CAPABILITIES, 'windows', 'amd64');
    const npm = win.find((o) => o.method === 'npm')!;
    const binary = win.find((o) => o.method === 'binary')!;
    expect(npm.offered).toBe(false); // windows npm 阻塞
    expect(binary.offered).toBe(false);
    // 状态与原因仍然如实列出（不隐藏限制）
    expect(npm.capability?.state).toBe('blocked');
    expect(npm.capability?.reason).toContain('npm');

    const linux = methodOfferings(MOCK_CAPABILITIES, 'linux', 'amd64');
    expect(linux.find((o) => o.method === 'npm')!.offered).toBe(true);
  });

  it('supported=ok、unverified=warn、blocked=danger，未知=muted（未验证不标支持）', () => {
    expect(capabilityTone('supported')).toBe('ok');
    expect(capabilityTone('unverified')).toBe('warn');
    expect(capabilityTone('blocked')).toBe('danger');
    expect(capabilityTone(undefined)).toBe('muted');
  });
});

describe('逐目标四态与下一步', () => {
  it('四态标签与基调：unknown 永远 warn，不与成功共用外观（两语言）', () => {
    expect(lifecycleStateLabel('succeeded', zh)).toBe('成功');
    expect(lifecycleStateLabel('not_executed', zh)).toBe('未执行');
    expect(lifecycleStateLabel('unknown', zh)).toBe('未知');
    expect(lifecycleStateLabel('unknown', en)).toBe('Unknown');
    expect(lifecycleStateTone('succeeded')).toBe('ok');
    expect(lifecycleStateTone('failed')).toBe('danger');
    expect(lifecycleStateTone('unknown')).toBe('warn');
    expect(lifecycleStateTone('not_executed')).toBe('muted');
    // 后端新状态原样保留，不编造解释
    expect(lifecycleStateLabel('brand-new', zh)).toBe('brand-new');
  });

  it('白名单原因可本地化；未知原因码原样保留', () => {
    expect(targetReasonLabel('target-offline', zh)).toContain('离线');
    expect(targetReasonLabel('target-offline', en)).toContain('offline');
    expect(targetReasonLabel('mystery-code', zh)).toBe('mystery-code');
    expect(targetReasonLabel(undefined, zh)).toBeUndefined();
  });

  it('未知不计成功：下一步要求先核对，且说明不会自动重试（两语言）', () => {
    for (const reason of ['result-lost', 'node-disconnected', 'timeout-no-result']) {
      const zhNext = nextStepLabel('unknown', reason, zh);
      expect(zhNext).toContain('核对');
      expect(zhNext).toContain('不计入成功');
      const enNext = nextStepLabel('unknown', reason, en);
      expect(enNext).toContain('actual state');
      expect(enNext).toContain('does not count as success');
    }
    expect(nextStepLabel('not_executed', 'target-offline', zh)).toContain('不会自动执行');
    expect(nextStepLabel('failed', 'unsupported-combo', zh)).toContain('未声明');
  });

  it('失败视图带原因与证据；重试清单只含确定失败目标', () => {
    const op = opWithResults([
      ['succeeded', 'verified-present'],
      ['failed', 'version-mismatch'],
      ['not_executed', 'target-offline'],
      ['unknown', 'result-lost'],
    ]);
    const views = targetResultViews(op, zh);
    expect(views).toHaveLength(4);
    expect(views[1].reasonText).toContain('不一致');
    expect(views[3].nextStep).toContain('核对');

    const failed = failedResults(op);
    expect(failed).toHaveLength(1);
    expect(failed[0].state).toBe('failed');
    expect(failed[0].target?.machine).toBe('m-1');

    expect(operationProgress(op)).toMatchObject({ total: 4, succeeded: 1, failed: 1, notExecuted: 1, unknown: 1, active: 0 });
    expect(isOperationSettled(op)).toBe(true);
    expect(isOperationSettled(opWithResults([['pending']]))).toBe(false);
  });

  it('needsReconciliation 呈现暂停与原因；操作标题带类型/版本/ID', () => {
    const op = opWithResults([['failed', 'op-failed']]);
    op.status.needsReconfirmation = true;
    op.status.reconfirmReason = 'version-mismatch';
    const view = needsReconfirmationView(op, zh)!;
    expect(view.text).toContain('暂停');
    expect(view.text).toContain('不一致');

    expect(formatOpTitle(op, zh)).toContain('升级');
    expect(formatOpTitle(op, zh)).toContain('0.56.0');
    expect(formatOpTitle(op, zh)).toContain('lc-op-1');
    expect(formatOpTitle(op, en)).toContain('Upgrade');
    expect(needsReconfirmationView({ ...op, status: { ...op.status, needsReconfirmation: false } }, zh)).toBeNull();
  });
});

describe('上游版本发现：未知 ≠ 最新', () => {
  it('ok 行携带来源/渠道/版本/查询时间', () => {
    const row = upstreamRowView(mockUpstreamReport().sources[0], zh);
    expect(row.kind).toBe('ok');
    if (row.kind === 'ok') {
      expect(row.method).toBe('npm');
      expect(row.channel).toBe('latest');
      expect(row.version).toBe('0.56.1');
      expect(row.queriedAt).toBe('2026-10-29T08:00:00Z');
    }
  });

  it('失败 + 有缓存：展示缓存与时间，并注明仅供参考', () => {
    const row = upstreamRowView(mockUpstreamReport().sources[1], zh);
    expect(row.kind).toBe('failed');
    if (row.kind === 'failed') {
      expect(row.reasonText).toContain('超时');
      expect(row.unknown).toBe(false);
      expect(row.cacheVersion).toBe('0.55.0');
    }
    const msg = translate(zh, 'lc.upstream.failedWithCache', { time: 'x', version: '0.55.0' });
    expect(msg).toContain('仅供参考');
  });

  it('失败 + 无缓存：显式"可用版本未知"，绝不显示为最新（两语言）', () => {
    const row = upstreamRowView(mockUpstreamReport().sources[2], zh);
    expect(row.kind).toBe('failed');
    if (row.kind === 'failed') {
      expect(row.unknown).toBe(true);
      expect(row.cacheVersion).toBeUndefined();
    }
    expect(translate(zh, 'lc.upstream.unknownDetail')).toContain('未知不等于最新');
    expect(translate(en, 'lc.upstream.unknownDetail')).toContain('Unknown is not the same as up to date');
    expect(upstreamFailureLabel('method_unsupported', en)).toContain('does not support');
  });
});

describe('期望状态偏差（与上游更新分开展示）', () => {
  it('未纳管是显式状态，不解释为缺席', () => {
    const v = deviationView(undefined, { present: true, version: '0.55.0' }, zh);
    expect(v.level).toBe('info');
    expect(v.text).toContain('未设定');
  });

  it('present 一致/版本偏差/尚未安装/版本未知各自如实呈现', () => {
    const entry = desiredPresentUpdate('0.56.0', 'npm', '@openai/codex').entries[CODEX_AGENT];
    expect(deviationView(entry, { present: true, version: '0.56.0' }, zh).level).toBe('ok');
    const diff = deviationView(entry, { present: true, version: '0.55.0' }, zh);
    expect(diff.level).toBe('deviate');
    expect(diff.text).toContain('0.55.0');
    const missing = deviationView(entry, { present: false }, zh);
    expect(missing.text).toContain('尚未安装');
    const unknownV = deviationView(entry, { present: true, versionError: 'parse failed' }, zh);
    expect(unknownV.text).toContain('无法识别');
  });

  it('absent 与 unmanaged 不混同：消失=一致；停止管理=保留程序', () => {
    const absent = desiredAbsentUpdate('npm', '@openai/codex').entries[CODEX_AGENT];
    expect(deviationView(absent, { present: false }, zh).level).toBe('ok');
    expect(deviationView(absent, { present: true, version: '0.55.0' }, zh).level).toBe('deviate');

    const unmanaged = desiredUnmanagedUpdate().entries[CODEX_AGENT];
    const v = deviationView(unmanaged, { present: true, version: '0.55.0' }, zh);
    expect(v.level).toBe('info');
    expect(v.detail).toContain('保留');
    // 停止管理文案绝不能出现"卸载"语义
    expect(v.detail).not.toContain('卸载');
  });
});

describe('影响指纹与重新确认', () => {
  const t1 = buildTarget({ machine: 'm', env: '', user: 'u', method: 'npm' });
  const t2 = buildTarget({ machine: 'm', env: 'wsl:ubuntu', user: 'u', method: 'npm' });

  it('同一影响得到同一指纹；目标或版本变化则不同', () => {
    const a = impactFingerprint('Upgrade', '0.56.0', [t1]);
    const b = impactFingerprint('Upgrade', '0.56.0', [t1]);
    expect(a).toBe(b);
    expect(impactFingerprint('Upgrade', '0.56.1', [t1])).not.toBe(a);
    // 目标集合相同（顺序无关）→ 指纹相同；加入新目标 → 不同
    expect(impactFingerprint('Upgrade', '0.56.0', [t1, t2])).toBe(impactFingerprint('Upgrade', '0.56.0', [t2, t1]));
    expect(impactFingerprint('Upgrade', '0.56.0', [t1, t2])).not.toBe(impactFingerprint('Upgrade', '0.56.0', [t1]));
    expect(impactFingerprint('Install', '0.56.0', [t1])).not.toBe(a);
  });
});

describe('实例选择辅助', () => {
  it('按环境+用户过滤实例；Windows 原生与 WSL 是不同目标', () => {
    const inv = mockInventory()['win-prod'];
    const native = instancesFor(inv, '', 'ops');
    const wsl = instancesFor(inv, 'wsl:ubuntu', 'dev');
    expect(native).toHaveLength(1);
    expect(wsl).toHaveLength(1);
    expect(instancesFor(inv, 'wsl:ubuntu', 'ops')).toHaveLength(0);
    expect(instanceKey(native[0])).not.toBe(instanceKey(wsl[0]));
  });

  it('构造目标携带 os/arch 与能力快照（T2 LifecycleTarget 形状）', () => {
    const t = buildTarget({
      machine: 'linux-build',
      env: '',
      user: 'ci',
      method: 'npm',
      os: 'linux',
      arch: 'amd64',
      capability: capabilityFor(MOCK_CAPABILITIES, 'linux', 'amd64', 'npm'),
    });
    expect(t).toMatchObject({
      machine: 'linux-build',
      user: 'ci',
      agent: 'codex',
      method: 'npm',
      package: '@openai/codex',
      os: 'linux',
      arch: 'amd64',
      capabilityState: 'unverified',
    });
    expect(t.env).toBeUndefined(); // 空 env = native，字段可缺省
  });
});

describe('契约 mock 后端（T2/T3 形状）', () => {
  it('同幂等键同内容复用同一操作；同键不同内容判冲突', async () => {
    const b = new MockLifecycleBackend();
    const req = {
      type: 'Install' as const,
      idempotencyKey: 'key-1',
      version: '0.56.0',
      targets: [buildTarget({ machine: 'linux-build', env: '', user: 'ci', method: 'npm' })],
    };
    const first = await b.createOperation(req);
    expect(first.reused).toBe(false);
    const second = await b.createOperation({ ...req });
    expect(second.reused).toBe(true);
    expect(second.operation.metadata.name).toBe(first.operation.metadata.name);
    await expect(b.createOperation({ ...req, version: '0.57.0' })).rejects.toMatchObject({ status: 409 });
  });

  it('Install 拒绝非精确版本；blocked 组合创建即拒（422）；unverified 允许创建', async () => {
    const b = new MockLifecycleBackend();
    const base = { idempotencyKey: 'k', targets: [buildTarget({ machine: 'linux-build', env: '', user: 'ci', method: 'npm', os: 'linux', arch: 'amd64' })] };
    await expect(b.createOperation({ ...base, type: 'Install', version: 'latest' })).rejects.toMatchObject({ status: 400 });
    await expect(
      b.createOperation({
        type: 'Install',
        idempotencyKey: 'k2',
        version: '0.56.0',
        targets: [buildTarget({ machine: 'win-prod', env: '', user: 'ops', method: 'npm', os: 'windows', arch: 'amd64' })],
      }),
    ).rejects.toMatchObject({ status: 422 });
    const ok = await b.createOperation({ ...base, type: 'Install', version: '0.56.0' });
    expect(ok.operation.status.results?.[0].state).toBe('pending');
  });

  it('仅 failed 目标可重试；重试继承类型与版本并关联 retryOf；needsReconfirmation 可置位', async () => {
    const b = new MockLifecycleBackend();
    const created = await b.createOperation({
      type: 'Upgrade',
      idempotencyKey: 'k',
      version: '0.56.0',
      targets: [
        buildTarget({ machine: 'a', env: '', user: 'u', method: 'npm' }),
        buildTarget({ machine: 'b', env: '', user: 'u', method: 'npm' }),
      ],
    });
    const opId = created.operation.metadata.name;
    b.settleOperation(opId, [
      { targetKey: (t: LifecycleTarget) => t.machine === 'a', state: 'succeeded', reason: 'verified-present', observedVersion: '0.56.0' },
      { targetKey: (t: LifecycleTarget) => t.machine === 'b', state: 'failed', reason: 'version-mismatch' },
    ]);
    const failed = failedResults(await b.getOperation(opId));
    expect(failed).toHaveLength(1);

    await expect(
      b.retryOperation(opId, { targets: [buildTarget({ machine: 'a', env: '', user: 'u', method: 'npm' })] }),
    ).rejects.toMatchObject({ status: 409 }); // 成功目标不可重试

    const retry = await b.retryOperation(opId, { targets: failed.map((f) => f.target as LifecycleTarget) });
    expect(retry.retryOf).toBe(opId);
    expect(retry.spec.type).toBe('Upgrade');
    expect(retry.spec.version).toBe('0.56.0');

    b.retryNeedsReconfirmation = true;
    const retry2 = await b.retryOperation(opId, { targets: failed.map((f) => f.target as LifecycleTarget) });
    expect(retry2.status.needsReconfirmation).toBe(true);
    expect(needsReconfirmationView(retry2, zh)?.text).toContain('暂停');
  });

  it('意图合并：未提及的 agent 原样保留（省略 ≠ 卸载），内容无变化不新增代', async () => {
    const b = new MockLifecycleBackend();
    b.seedPresent('linux-build', '0.56.0', 'npm');
    const before = (await b.getDesired('linux-build'))!;
    expect(desiredEntryFor(before, 'codex')?.intent).toBe('present');

    // 另一个 agent 的意图写入不得影响 codex（省略 ≠ 卸载）
    await b.putDesired('linux-build', { entries: { other: { intent: 'unmanaged' } } });
    const after = (await b.getDesired('linux-build'))!;
    expect(desiredEntryFor(after, 'codex')?.intent).toBe('present');
    expect(desiredEntryFor(after, 'other')?.intent).toBe('unmanaged');

    const unchanged = await b.putDesired('linux-build', desiredPresentUpdate('0.56.0', 'npm', '@openai/codex'));
    expect(unchanged.revision).toBe(after.revision);
  });

  it('实例清单：多实例必须显式选择；无实例给出尚未安装状态；mac 清单带过期标注', () => {
    const mac = mockInventory()['mac-studio'];
    expect(mac.instances).toHaveLength(2);
    expect(instancesFor(mac, '', 'designer')).toHaveLength(2);
    expect(mac.stale).toBe(true);
    const linux = mockInventory()['linux-build'];
    expect(instancesFor(linux, '', 'ci')).toHaveLength(0);
  });
});
