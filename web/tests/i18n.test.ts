/**
 * i18n 基础能力（KM-43）与全页面迁移（KM-44）：首次语言、切换即时性、动态内容
 * 跟随、本地化格式化规则与缺失翻译的回退策略；外加 KM-44 新增面的双语言证据
 * （条件标签、策略/回滚文案、等待确认提示、整页 en 渲染）。
 *
 * 本文件是纯 node 环境（消息目录、翻译解析、状态视图、格式化函数）；
 * 需要真实 DOM 的挂载切换与 localStorage 持久化见 i18n-dom.test.ts。
 */
import { describe, expect, it } from 'vitest';
import { render } from 'svelte/server';
import {
  catalogs,
  formatNumber,
  initLocale,
  setLocale,
  t,
  translate,
  i18n,
  type Message,
  type MessageKey,
} from '../src/lib/i18n/index.js';
import { formatDuration, formatRelativeTime, formatTime } from '../src/lib/state/format.js';
import { machineRow } from '../src/lib/state/machines.js';
import { deploymentPhaseLabel, rollbackLabel, strategyLabel, viewTarget } from '../src/lib/state/deployments.js';
import { viewOperation } from '../src/lib/state/operations.js';
import { allConditionSummaries } from '../src/lib/state/drift.js';
import { awaitingConfirmations } from '../src/lib/state/ssh.js';
import Overview from '../src/routes/Overview.svelte';
import Machines from '../src/routes/Machines.svelte';
import MachineDetail from '../src/routes/MachineDetail.svelte';
import Deployments from '../src/routes/Deployments.svelte';
import SshInventory from '../src/routes/SshInventory.svelte';
import Profiles from '../src/routes/Profiles.svelte';
import Skills from '../src/routes/Skills.svelte';
import type { Deployment, Machine, Operation } from '../src/lib/api/types.js';

const NOW = new Date('2026-09-23T12:00:00Z');

const machineFixture: Machine = {
  metadata: { name: 'ws-1', resourceVersion: 1, creationTimestamp: '2026-09-23T08:00:00Z' },
  spec: { managementMode: 'agentd', profileRef: 'default-dev' },
  status: {
    conditions: [
      { type: 'AgentConnected', status: 'True', lastTransitionTime: '2026-09-23T08:00:00Z' },
      {
        type: 'Drifted',
        status: 'Unknown',
        reason: 'StaleObservation',
        lastTransitionTime: '2026-09-23T08:00:00Z',
      },
      { type: 'Degraded', status: 'True', lastTransitionTime: '2026-09-23T08:00:00Z' },
    ],
    agentdVersion: '0.4.0',
    lastHeartbeatAt: '2026-09-23T09:59:00Z',
  },
} as Machine;

const deploymentFixture: Deployment = {
  metadata: { name: 'rollout-42', creationTimestamp: '2026-09-23T07:00:00Z' },
  spec: { targetGeneration: 12 },
  status: {
    phase: 'RollingOut',
    targets: [
      { machine: 'ws-a', phase: 'Succeeded' },
      { machine: 'ws-b', phase: 'Superseded', reason: 'SupersededByNewerGeneration' },
      { machine: 'ws-c', phase: 'Skipped', reason: 'OperatorSkipped' },
      { machine: 'ws-d', phase: 'Failed', reason: 'VerifyFailed' },
    ],
  },
};

const operationFixture: Operation = {
  metadata: { name: 'op-1', creationTimestamp: '2026-09-23T11:00:00Z' },
  spec: { machine: 'ws-1', type: 'Reconcile', transport: 'agentd' },
  status: { phase: 'AwaitingConfirmation' },
};

/** Overview "最近失败操作"表只收 Failed / 带修饰的成功。 */
const failedOpFixture: Operation = {
  metadata: { name: 'op-2', creationTimestamp: '2026-09-23T11:30:00Z' },
  spec: { machine: 'ws-1', type: 'Reconcile', transport: 'agentd' },
  status: { phase: 'Failed', finishedAt: '2026-09-23T11:40:00Z' },
};

describe('消息目录完整性（两语言同键集，值为非空文案）', () => {
  it('zh-CN 与 en 的键集完全一致', () => {
    const zhKeys = Object.keys(catalogs['zh-CN']).sort();
    const enKeys = Object.keys(catalogs.en).sort();
    expect(enKeys).toEqual(zhKeys);
  });

  it('每个消息都是非空字符串或函数（不存在空文案）', () => {
    for (const catalog of [catalogs['zh-CN'], catalogs.en]) {
      for (const [key, value] of Object.entries(catalog) as [string, Message][]) {
        if (typeof value === 'string') {
          expect(value.length, `空文案：${key}`).toBeGreaterThan(0);
        } else {
          expect(typeof value, `非函数/字符串：${key}`).toBe('function');
        }
      }
    }
  });
});

describe('首次语言与切换', () => {
  it('默认（首次打开）语言是 zh-CN', () => {
    initLocale(); // node 无 window，存储分支跳过 → 保持默认
    expect(i18n.locale).toBe('zh-CN');
    expect(t('nav.machines')).toBe('机器');
  });

  it('setLocale 后 t() 立即返回所选语言文案，可随时切回', () => {
    setLocale('en');
    expect(i18n.locale).toBe('en');
    expect(t('nav.machines')).toBe('Machines');
    expect(t('app.title')).toBe('agent-fleet console');

    setLocale('zh-CN');
    expect(t('nav.machines')).toBe('机器');
    // 品牌名不翻译（spec：品牌原样保留）
    expect(t('app.title')).toBe('agent-fleet 控制面');
  });
});

describe('动态内容跟随所选语言', () => {
  it('machineRow 的行级标签随 locale 重建（en），默认 zh 与既有行为一致', () => {
    const zh = machineRow(machineFixture, NOW);
    expect(zh.drift.label).toBe('未知或过期');
    expect(zh.agentd.label).toBe('在线 0.4.0');
    expect(zh.attentionReasons).toContain('drift 未知：StaleObservation');
    expect(zh.attentionReasons).toContain('Degraded=True'); // 条件表达式按标识原样保留

    const en = machineRow(machineFixture, NOW, 'en');
    expect(en.drift.label).toBe('Unknown or stale');
    expect(en.drift.detail).toContain('The observation is stale');
    expect(en.agentd.label).toBe('online 0.4.0');
    expect(en.attentionReasons).toContain('drift unknown: StaleObservation');
    expect(en.attentionReasons).not.toContain('未');
  });

  it('Deployment 目标相位与原因随 locale，未知原因码保留原码', () => {
    const superseded = viewTarget(deploymentFixture.status!.targets![1], NOW, 'en');
    expect(superseded.phaseLabel).toBe('Superseded');
    expect(superseded.reasonText).toContain('newer generation');

    const failed = viewTarget(deploymentFixture.status!.targets![3], NOW, 'en');
    expect(failed.reasonText).toBe('Backend reason code: VerifyFailed');

    const skippedZh = viewTarget(deploymentFixture.status!.targets![2], NOW);
    expect(skippedZh.phaseLabel).toBe('已跳过');
    // 附带未知原因码时保留原码（与既有行为一致）；未附原因码才用跳过的语义说明
    expect(skippedZh.reasonText).toBe('后端原因码：OperatorSkipped');
    const noReasonSkipped = viewTarget({ machine: 'ws-c', phase: 'Skipped' }, NOW);
    expect(noReasonSkipped.reasonText).toContain('不计入成功');
  });

  it('部署相位与操作相位/类型随 locale；未知相位原样展示（不露消息 key）', () => {
    expect(deploymentPhaseLabel(deploymentFixture, 'en')).toBe('Rolling out');
    expect(deploymentPhaseLabel(deploymentFixture)).toBe('滚动中');
    expect(
      deploymentPhaseLabel(
        { ...deploymentFixture, status: { phase: 'BrandNewPhase' } } as unknown as Deployment,
        'en',
      ),
    ).toBe('BrandNewPhase');

    const enOp = viewOperation(operationFixture, NOW, 'en');
    expect(enOp.typeLabel).toBe('Reconcile');
    expect(enOp.phaseLabel).toBe('Awaiting confirmation');

    const zhOp = viewOperation(operationFixture, NOW);
    expect(zhOp.typeLabel).toBe('收敛');
    expect(zhOp.phaseLabel).toBe('等待确认');
  });

  it('Overview 页面 SSR：同一份夹具在 en 下渲染英文，zh-CN 下渲染中文', () => {
    const store = {
      loading: {},
      errors: {},
      machines: [machineFixture],
      deployments: [deploymentFixture],
      recentOperations: [failedOpFixture],
    } as never;

    setLocale('en');
    const en = render(Overview, { props: { store } }).body;
    expect(en).toContain('Machines needing attention');
    expect(en).toContain('Unknown or stale');
    expect(en).toContain('Recent rollouts');
    expect(en).toContain('Manual');

    setLocale('zh-CN');
    const zh = render(Overview, { props: { store } }).body;
    expect(zh).toContain('需要关注的机器');
    expect(zh).toContain('未知或过期');
    expect(zh).toContain('最近发布');
    expect(zh).toContain('手动');
    // 两语言都不允许出现消息 key 泄漏（形如 ns.name 的裸 key）
    for (const body of [en, zh]) {
      expect(body).not.toMatch(
        /\b(app|nav|overview|machine|machines|drift|target|deploy|deployments|ops|time|common|error|condition|detail|profiles|skills|ssh|sshinv)\.[a-zA-Z]+/,
      );
    }
  });

  it('Machines 整页 SSR：en 下表头、按钮与空态全部为英文；zh 下为中文', () => {
    const emptyStore = {
      loading: {},
      errors: {},
      machines: [],
      deployments: [],
      profiles: [],
      skills: [],
    } as never;

    setLocale('en');
    const en = render(Machines, { props: { store: emptyStore } }).body;
    expect(en).toContain('Machines');
    expect(en).toContain('Add machine');
    expect(en).toContain('No machines yet');
    expect(en).toContain('Bootstrap / repair agentd');
    expect(en).not.toContain('还没有');

    setLocale('zh-CN');
    const zh = render(Machines, { props: { store: emptyStore } }).body;
    expect(zh).toContain('机器');
    expect(zh).toContain('添加机器');
    expect(zh).toContain('还没有机器');
    expect(zh).toContain('纳管 / 修复 agentd');
    // 两语言都不允许裸 key 泄漏。
    for (const body of [en, zh]) {
      expect(body).not.toMatch(/\b(app|nav|machines|common|error)\.[a-zA-Z]+/);
    }
  });

  it('六条件的展示名随 locale；缺失的条件说明同样本地化', () => {
    const conditions = [
      { type: 'SSHReachable' as const, status: 'True' as const, lastTransitionTime: '2026-09-23T11:00:00Z' },
      { type: 'AgentConnected' as const, status: 'False' as const, lastTransitionTime: '2026-09-23T11:00:00Z' },
    ];
    const zh = allConditionSummaries(conditions);
    expect(zh.map((c) => c.label)).toEqual(['SSH 可达: True', 'agentd 在线: False', 'inventory 就绪: Unknown', '漂移: Unknown', '收敛: Unknown', '降级: Unknown']);
    expect(zh[2].message).toBe('该条件尚未记录');

    const en = allConditionSummaries(conditions, 'en');
    expect(en[0].label).toBe('SSH reachable: True');
    expect(en[1].label).toBe('agentd connected: False');
    expect(en[2].message).toBe('This condition has not been recorded');
  });

  it('发布策略与回滚说明随 locale', () => {
    const dep: Deployment = {
      metadata: { name: 'rollout-2' },
      spec: { rollbackOf: 14, targetGeneration: 12, strategy: { canary: 1, batchSize: 2, pauseOnFailure: true } },
    };
    expect(strategyLabel(dep)).toBe('金丝雀 1 台 · 批大小 2 · 失败即暂停');
    expect(strategyLabel(dep, 'en')).toBe('canary 1 · batch size 2 · pause on failure');
    expect(rollbackLabel(dep)).toContain('回滚自 gen14 至 gen12 的内容');
    expect(rollbackLabel(dep, 'en')).toContain('Rolls back content from gen14 to gen12');
    expect(rollbackLabel({ metadata: { name: 'x' } }, 'en')).toBeNull();
  });

  it('等待确认提示随 locale：剩余时长与基线提示都在当前语言', () => {
    const op: Operation = {
      metadata: { name: 'op-1', creationTimestamp: '2026-09-23T11:00:00Z' },
      spec: { machine: 'ws-1', type: 'Reconcile', planDigest: 'sha256:plan' },
      status: { phase: 'AwaitingConfirmation', planExpiresAt: '2026-09-23T12:10:00Z' },
    };
    const zh = awaitingConfirmations([op], NOW);
    expect(zh[0].remainingText).toBe('10 分 0 秒');
    expect(zh[0].baselineNotice).toContain('确认只表示');

    const en = awaitingConfirmations([op], NOW, 'en');
    expect(en[0].remainingText).toBe('10 min 0 sec');
    expect(en[0].baselineNotice).toContain('Confirming only approves this exact plan');
  });

  it('Machine Detail / Deployments / SSH Inventory 整页 SSR：en 下区块、表头与说明全部为英文', () => {
    const driftedMachine: Machine = {
      metadata: { name: 'ws-1', creationTimestamp: '2026-09-23T08:00:00Z' },
      spec: { managementMode: 'ssh' },
      status: {
        conditions: [
          { type: 'Drifted', status: 'True', lastTransitionTime: '2026-09-23T09:00:00Z' },
          { type: 'Reconciled', status: 'True', lastTransitionTime: '2026-09-23T09:00:00Z' },
        ],
        os: 'linux',
        arch: 'amd64',
        desiredGeneration: 2,
        observedGeneration: 2,
      },
    } as Machine;
    const store = {
      loading: {},
      errors: {},
      machines: [driftedMachine],
      deployments: [deploymentFixture],
      profiles: [],
      skills: [],
      providers: [],
      details: {
        'ws-1': {
          machine: driftedMachine,
          drift: {
            machine: 'ws-1',
            drifted: { status: 'True' },
            reconciled: { status: 'True' },
            desiredProjectionDigest: 'sha256:aaaa',
            observedProjectionDigest: 'sha256:bbbb',
          },
          operations: [],
          loading: false,
        },
      },
    } as never;

    setLocale('en');
    const detail = render(MachineDetail, { props: { store, name: 'ws-1' } }).body;
    expect(detail).toContain('Connectivity ');
    expect(detail).toContain('Operation history ');
    expect(detail).toContain('Drifted');
    expect(detail).toContain('Field-level diff is not available');
    expect(detail).toContain('Roll back to content generation (positive integer)');
    expect(detail).not.toContain('联通与状态');

    const dep = render(Deployments, { props: { store, name: 'rollout-42' } }).body;
    expect(dep).toContain('Per-machine results');
    expect(dep).toContain('Target machines');
    expect(dep).toContain('Rolling out');

    const ssh = render(SshInventory, { props: { store } }).body;
    expect(ssh).toContain('Machine aliases');
    expect(ssh).toContain('OpenSSH include preview');
    expect(ssh).toContain('Export (download .conf)');
    expect(ssh).not.toContain('机器别名');

    setLocale('zh-CN');
    // 回到中文后同一页面立即回到中文（响应式语言状态）。
    const detailZh = render(MachineDetail, { props: { store, name: 'ws-1' } }).body;
    expect(detailZh).toContain('联通与状态');
    for (const body of [detail, dep, ssh, detailZh]) {
      expect(body).not.toMatch(/\b(detail|deployments|sshinv|machines|common|error)\.[a-zA-Z]+/);
    }
  });

  it('Profiles 与 Skills 整页 SSR：按钮与占位符等可见文案两语言都经目录取值', () => {
    const profileFixture = {
      metadata: { name: 'default-dev', creationTimestamp: '2026-09-23T08:00:00Z' },
      spec: { agents: { codex: { enabled: true } }, skills: [{ name: 'superpowers', skill: 'skill-src' }] },
    };
    const skillFixture = {
      metadata: { name: 'skill-src' },
      spec: { source: 'github.com/example/skills', ref: 'v1.2.0' },
      status: { resolvedRevision: 'v1.2.0', contentDigest: 'sha256:cccc' },
    };
    const profilesStore = {
      loading: {},
      errors: {},
      machines: [],
      profiles: [profileFixture],
      machinesUsingProfile: () => [],
    } as never;
    const skillsStore = {
      loading: {},
      errors: {},
      machines: [],
      skills: [skillFixture],
      machinesUsingSkill: () => [],
    } as never;

    setLocale('en');
    const profilesEn = render(Profiles, { props: { store: profilesStore, name: 'default-dev' } }).body;
    expect(profilesEn).toContain('Render preview');
    expect(profilesEn).toContain('Target machine');
    expect(profilesEn).toContain('placeholder="ws-1"'); // 语言中立示例标识
    expect(profilesEn).not.toContain('machine name');
    const skillsEn = render(Skills, { props: { store: skillsStore } }).body;
    expect(skillsEn).toContain('Resolved revision');
    expect(skillsEn).toContain('Resolve is not available yet');
    expect(skillsEn).toMatch(/>\s*Resolve\s*</); // 按钮可见文本经目录取值

    setLocale('zh-CN');
    const profilesZh = render(Profiles, { props: { store: profilesStore, name: 'default-dev' } }).body;
    expect(profilesZh).toContain('渲染预览');
    expect(profilesZh).toContain('目标机器');
    expect(profilesZh).toContain('placeholder="ws-1"');
    expect(profilesZh).not.toContain('machine name');
    const skillsZh = render(Skills, { props: { store: skillsStore } }).body;
    expect(skillsZh).toContain('已解析修订');
    expect(skillsZh).toContain('解析功能尚未提供');
    expect(skillsZh).toMatch(/>\s*解析\s*</); // 按钮文本是「解析」，不再是英文 Resolve
    expect(skillsZh).not.toContain('Resolve');
    for (const body of [profilesEn, skillsEn, profilesZh, skillsZh]) {
      expect(body).not.toMatch(/\b(profiles|skills)\.[a-zA-Z]+/);
    }
  });
});

describe('格式化规则', () => {
  it('数字按 locale 分组', () => {
    expect(formatNumber(1234567, 'en')).toBe('1,234,567');
    expect(formatNumber(1234567, 'zh-CN')).toBe('1,234,567');
  });

  it('绝对时间：zh 24 小时制，en 使用本地惯例（AM/PM）', () => {
    const at = new Date(2026, 8, 23, 17, 0, 0); // 本地 17:00
    expect(formatTime(at, 'zh-CN')).toContain('17:');
    expect(formatTime(at, 'en')).toMatch(/(PM|AM)/);
    expect(formatTime(at)).toBe(formatTime(at, 'zh-CN')); // 默认 zh-CN
  });

  it('相对时间：分钟/小时/天与刚刚，逐字对照', () => {
    expect(formatRelativeTime(new Date(NOW.getTime() - 3 * 60_000), NOW)).toBe('3 分钟前');
    expect(formatRelativeTime(new Date(NOW.getTime() - 3 * 60_000), NOW, 'en')).toBe('3 minutes ago');
    expect(formatRelativeTime(new Date(NOW.getTime() - 1 * 60_000), NOW, 'en')).toBe('1 minute ago');
    expect(formatRelativeTime(new Date(NOW.getTime() - 5 * 3_600_000), NOW, 'en')).toBe('5 hours ago');
    expect(formatRelativeTime(new Date(NOW.getTime() - 2 * 86_400_000), NOW, 'en')).toBe('2 days ago');
    expect(formatRelativeTime(new Date(NOW.getTime() - 10_000), NOW, 'en')).toBe('just now');
    expect(formatRelativeTime(new Date(NOW.getTime() - 10_000), NOW)).toBe('刚刚');
    // 未来时间：zh 用"…后"，en 用 "in …"
    expect(formatRelativeTime(new Date(NOW.getTime() + 65_000), NOW, 'en')).toMatch(/^in 1 min/);
    expect(formatRelativeTime(new Date(NOW.getTime() + 65_000), NOW)).toMatch(/后$/);
  });

  it('时长：剩余窗口的中英文（用于等待确认等）', () => {
    expect(formatDuration(12 * 60_000 + 5_000)).toBe('12 分 5 秒');
    expect(formatDuration(12 * 60_000 + 5_000, 'en')).toBe('12 min 5 sec');
    expect(formatDuration(3 * 3_600_000, 'en')).toBe('3 hr 0 min');
    expect(formatDuration(3 * 3_600_000)).toBe('3 小时 0 分');
    expect(formatDuration(-1)).toBe('已到期');
    expect(formatDuration(-1, 'en')).toBe('expired');
  });
});

describe('缺失翻译的回退策略', () => {
  it('en 目录缺 key → 回退源语言 zh-CN 的文案，而不是裸 key', () => {
    const original = catalogs.en['nav.machines'];
    try {
      delete (catalogs.en as Record<string, Message>)['nav.machines'];
      expect(translate('en', 'nav.machines' as MessageKey)).toBe('机器');
    } finally {
      catalogs.en['nav.machines'] = original!;
    }
  });

  it('两端都缺 key（类型系统之外）才返回 key 本身，该分支仅是运行时兜底', () => {
    expect(translate('en', '__nonexistent.key__')).toBe('__nonexistent.key__');
    expect(t('__nonexistent.key__' as MessageKey)).toBe('__nonexistent.key__');
  });
});
