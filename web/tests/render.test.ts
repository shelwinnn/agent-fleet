/**
 * 页面与状态呈现证据。
 *
 * 用 `svelte/server` 把**真实的页面组件**渲染成 HTML，断言用户可观察的正确含义：
 * drift 三态（Unknown 不得显示为"一致"）、未决操作与阻塞、Deployment 目标的
 * Superseded/Skipped 原因、真实限制与风险的呈现、禁用入口的原因标注、空态与
 * 加载/错误的无障碍呈现。
 *
 * 说明：SSR 不跑 $effect/onMount（数据加载在浏览器侧），所以这里先把 store 填好
 * 再渲染——渲染路径与浏览器一致（同一批组件、同一批派生视图）。默认语言为
 * zh-CN（源语言）；英文下的整页渲染证据见 i18n.test.ts。
 */
import { describe, expect, it } from 'vitest';
import { render } from 'svelte/server';
import { FleetStore } from '../src/lib/state/store.svelte.js';
import { ROUTE_TABLE } from '../src/lib/state/router.svelte.js';
import { machineRow } from '../src/lib/state/machines.js';
import Overview from '../src/routes/Overview.svelte';
import Machines from '../src/routes/Machines.svelte';
import MachineDetail from '../src/routes/MachineDetail.svelte';
import Deployments from '../src/routes/Deployments.svelte';
import Profiles from '../src/routes/Profiles.svelte';
import Skills from '../src/routes/Skills.svelte';
import SshInventory from '../src/routes/SshInventory.svelte';
import type { Deployment, Machine, Operation } from '../src/lib/api/types.js';

const enabled = process.env.FLEET_INTEGRATION === '1';
const base = process.env.FLEET_API ?? 'http://127.0.0.1:7788';

/** 与真实后端同形的固定数据：即使不连后端也能证明渲染路径（值取自联调观测的形状）。 */
const machineFixture: Machine = {
  metadata: { name: 'ws-drift-demo', resourceVersion: 7, creationTimestamp: '2026-09-23T08:00:00Z' },
  spec: { managementMode: 'agentd', profileRef: 'default-dev' },
  status: {
    conditions: [
      { type: 'SSHReachable', status: 'True', lastTransitionTime: '2026-09-23T08:00:00Z' },
      { type: 'AgentConnected', status: 'True', lastTransitionTime: '2026-09-23T08:00:00Z' },
      { type: 'InventoryReady', status: 'True', lastTransitionTime: '2026-09-23T08:00:00Z' },
      {
        type: 'Drifted',
        status: 'Unknown',
        reason: 'StaleObservation',
        message: 'observation is older than the freshness window',
        lastTransitionTime: '2026-09-23T08:00:00Z',
      },
      { type: 'Reconciled', status: 'Unknown', reason: 'StaleObservation', lastTransitionTime: '2026-09-23T08:00:00Z' },
      { type: 'Degraded', status: 'False', lastTransitionTime: '2026-09-23T08:00:00Z' },
    ],
    observedGeneration: 3,
    desiredGeneration: 4,
    os: 'linux',
    arch: 'amd64',
    agentdVersion: '0.4.0',
    lastHeartbeatAt: '2026-09-23T09:59:00Z',
    lastInventoryAt: '2026-09-23T09:40:00Z',
    inventorySeq: 128,
    desiredProjectionDigest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    observedProjectionDigest: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
    unresolvedOperation: { id: 'op-unresolved', phase: 'AwaitingConfirmation', type: 'Reconcile' },
    adapterCapabilities: [
      { family: 'codex', capability: 'mcp', state: 'verified', verifiedVersions: '0.9.x' },
      { family: 'opencode', capability: 'skills', state: 'unverified', reason: '未实测' },
    ],
  },
} as Machine;

const awaitingOp: Operation = {
  metadata: { name: 'op-unresolved', creationTimestamp: '2026-09-23T09:30:00Z' },
  spec: { machine: 'ws-drift-demo', type: 'Reconcile', transport: 'ssh', planDigest: 'sha256:plan-abc' },
  status: { phase: 'AwaitingConfirmation', planExpiresAt: '2026-09-23T10:30:00Z' },
};

const deploymentFixture: Deployment = {
  metadata: { name: 'rollout-42', creationTimestamp: '2026-09-23T07:00:00Z' },
  spec: { targetGeneration: 12, rollbackOf: 14, strategy: { canary: 1, batchSize: 2, pauseOnFailure: true } },
  status: {
    phase: 'RollingOut',
    targets: [
      { machine: 'ws-a', phase: 'Succeeded', effectiveGeneration: 15 },
      { machine: 'ws-b', phase: 'Superseded', reason: 'SupersededByNewerGeneration', effectiveGeneration: 14 },
      { machine: 'ws-c', phase: 'Skipped', reason: 'OperatorSkipped' },
      { machine: 'ws-d', phase: 'Blocked', reason: 'BlockedByNodeLock' },
      { machine: 'ws-e', phase: 'Failed', reason: 'VerifyFailed' },
    ],
  },
};

function seededStore(): FleetStore {
  const store = new FleetStore();
  store.machines = [machineFixture];
  store.deployments = [deploymentFixture];
  store.profiles = [
    { metadata: { name: 'default-dev' }, spec: { agents: { codex: { enabled: true, version: '0.9.1', provider: 'openai-main' } }, skills: [{ name: 'superpowers', skill: 'skill-src' }], mcp: { serena: { command: 'serena' } } } },
  ];
  store.skills = [
    { metadata: { name: 'skill-src' }, spec: { source: 'github.com/example/skills', ref: 'v1.2.0' }, status: { resolvedRevision: 'v1.2.0', contentDigest: 'sha256:cccccccccccc' } },
  ];
  store.operationOwners.set('op-unresolved', 'ws-drift-demo');
  return store;
}

describe('页面渲染（状态呈现证据）', () => {
  it('路由清单覆盖七个页面', () => {
    const pages = ROUTE_TABLE.map((r) => r.page).join('\n');
    for (const page of ['Overview', 'Machines', 'Machine Detail', 'Profiles', 'Skills', 'Deployments', 'SSH Inventory']) {
      expect(pages).toContain(page);
    }
  });

  it('Overview 渲染六张卡片与"需关注"表，且明确区分未知与一致', () => {
    const store = seededStore();
    const { body } = render(Overview, { props: { store } });
    for (const card of ['机器总数', 'daemon 在线', 'SSH 可达', '已漂移', '降级', '活跃发布']) {
      expect(body).toContain(card);
    }
    expect(body).toContain('需要关注的机器');
    expect(body).toContain('未知或过期'); // drift 三态里的 Unknown
    expect(body).toContain('ws-drift-demo');
    expect(body).not.toContain('已确认一致'); // 该机是 Unknown，绝不能被渲染成一致
  });

  it('Machines 渲染九列中文化的表头，Unknown 不显示为一致，动作入口带可用性原因', () => {
    const store = seededStore();
    const { body } = render(Machines, { props: { store } });
    for (const column of ['机器名', '系统 · 架构', '配置档', 'agentd', 'SSH', '漂移', '收敛', '最近上报', '操作']) {
      expect(body).toContain(column);
    }
    expect(body).toContain('未知或过期');
    // 未决操作以徽章 + 原因呈现（相位翻译为"等待确认"）。
    expect(body).toContain('未决操作');
    expect(body).toContain('等待确认');
    // probe / inventory 是接通的真实动作；SSH 通道限制如实标注。
    expect(body).toContain('SSH 探测');
    expect(body).toContain('观测采集');
    const inventory = body.indexOf('>观测采集<');
    expect(inventory, '观测采集按钮存在').toBeGreaterThan(-1);
    expect(body.slice(inventory - 600, inventory)).toContain('disabled');
    expect(body).toContain('仅 SSH 管理的机器支持手动观测采集');
    // 尚未交付的功能：禁用 + 原因，不提供假入口；不出现内部文档指引。
    expect(body).toContain('引导与修复 agentd 功能尚未提供');
    expect(body).not.toContain('docs/');
    expect(body).not.toContain('属第');
  });

  it('空态告诉用户可以做什么；加载与错误状态有无障碍语义', () => {
    const emptyStore = new FleetStore();
    const { body } = render(Machines, { props: { store: emptyStore } });
    expect(body).toContain('还没有机器');
    expect(body).toContain('添加机器');

    const loadingStore = new FleetStore();
    loadingStore.loading.machines = true;
    const loading = render(Machines, { props: { store: loadingStore } }).body;
    expect(loading).toContain('aria-busy="true"');

    const errorStore = new FleetStore();
    errorStore.errors.machines = new Error('boom');
    const errored = render(Machines, { props: { store: errorStore } }).body;
    expect(errored).toContain('机器列表加载失败');
    expect(errored).toContain('role="alert"');
    expect(errored).toContain('全量刷新'); // 错误提示给出下一步
  });

  it('Machine Detail 渲染 9 个区块，含等待确认、plan 基线与阻塞说明', () => {
    const store = seededStore();
    const detail = {
      machine: machineFixture,
      drift: {
        machine: 'ws-drift-demo',
        drifted: { status: 'Unknown' as const, reason: 'StaleObservation' },
        reconciled: { status: 'Unknown' as const, reason: 'StaleObservation' },
        desiredProjectionDigest: 'sha256:aaaa',
        observedProjectionDigest: 'sha256:bbbb',
        unresolvedOperation: { id: 'op-unresolved', phase: 'AwaitingConfirmation' as const },
      },
      operations: [awaitingOp],
      loading: false,
    };
    store.details = { 'ws-drift-demo': detail };
    const { body } = render(MachineDetail, { props: { store, name: 'ws-drift-demo' } });
    for (const section of [
      '联通与状态',
      'Agents 与版本',
      '期望与观测',
      '技能',
      '模型与 Provider',
      'MCP',
      '漂移详情',
      '操作历史与未决操作',
      '回滚与其他动作',
    ]) {
      expect(body).toContain(section);
    }
    expect(body).toContain('等待确认（剩余');
    expect(body).toContain('确认只表示'); // plan 基线提示：确认的只是这份计划
    expect(body).toContain('未知或过期');
    expect(body).toContain('取消');
    expect(body).toContain('跳过');
    expect(body).toContain('字段级差异暂不可查看'); // 期望与观测：如实说明看不到字段级 diff
    // 六个条件各自保留类型名与三态（回归：曾因 conditionView 不带 type 而全部渲染成 Drifted）
    for (const label of [
      'SSH 可达: True',
      'agentd 在线: True',
      'inventory 就绪: True',
      '漂移: Unknown',
      '收敛: Unknown',
      '降级: False',
    ]) {
      expect(body).toContain(label);
    }
    // 未验证的能力声明逐字转述，不宣称已验证
    expect(body).toContain('unverified');
    expect(body).toContain('未验证的能力不会标为已验证');
  });

  it('drifted 状态下漂移区块如实说明字段级 diff 不可用，入口禁用并指向收敛', () => {
    const store = seededStore();
    const drifted: Machine = {
      ...machineFixture,
      status: {
        ...machineFixture.status,
        conditions: [
          { type: 'Drifted', status: 'True', lastTransitionTime: '2026-09-23T09:00:00Z' },
          { type: 'Reconciled', status: 'True', lastTransitionTime: '2026-09-23T09:00:00Z' },
        ],
      },
    };
    store.machines = [drifted];
    store.details = {
      'ws-drift-demo': {
        machine: drifted,
        drift: {
          machine: 'ws-drift-demo',
          drifted: { status: 'True' },
          reconciled: { status: 'True' },
          desiredProjectionDigest: 'sha256:aaaa',
          observedProjectionDigest: 'sha256:bbbb',
        },
        operations: [],
        loading: false,
      },
    };
    const { body } = render(MachineDetail, { props: { store, name: 'ws-drift-demo' } });
    expect(body).toContain('已漂移');
    expect(body).toContain('字段级对比暂不可用');
    expect(body).toContain('可执行收敛使机器与期望一致'); // 失败/受限时的下一步
    // "查看差异"必须是"禁用 + 原因标注"的入口，而不是只留一句说明。
    expect(body).toContain('查看字段级差异（暂不可用）');
    const showDiff = body.slice(body.indexOf('查看字段级差异') - 1200, body.indexOf('查看字段级差异'));
    expect(showDiff, '查看差异必须是禁用按钮（disabled 属性在按钮上）').toContain('disabled');
  });

  it('Deployments 把 Superseded/Skipped 与 Failed 分开呈现并带原因', () => {
    const store = seededStore();
    const { body } = render(Deployments, { props: { store, name: 'rollout-42' } });
    expect(body).toContain('已被取代');
    expect(body).toContain('已在更新代'); // Superseded 的原因说明（不是失败）
    expect(body).toContain('已跳过');
    expect(body).toContain('不计入成功'); // Skipped 的边界说明
    expect(body).toContain('失败');
    expect(body).toContain('回滚自 gen14 至 gen12 的内容');
    expect(body).toContain('不计入失败'); // Blocked 的边界说明
    // 尚未提供的暂停/恢复：禁用并写明原因。
    expect(body).toContain('暂停功能尚未提供');
    expect(body).toContain('恢复功能尚未提供');
  });

  it('Profiles 与 Skills 渲染空态与选择提示，未交付的解析入口禁用并写明原因', () => {
    const empty = new FleetStore();
    const profiles = render(Profiles, { props: { store: empty, name: null } }).body;
    expect(profiles).toContain('还没有配置档');

    const skills = render(Skills, { props: { store: empty } }).body;
    expect(skills).toContain('还没有技能资源');

    const store = seededStore();
    const skillsPage = render(Skills, { props: { store } }).body;
    expect(skillsPage).toContain('解析功能尚未提供');
    expect(skillsPage).not.toContain('docs/');
    expect(skillsPage).toContain('v1.2.0'); // 已解析修订照常显示
  });

  it('SSH Inventory 渲染别名与服务端渲染说明，安装入口默认禁用（需显式确认）', () => {
    const store = seededStore();
    // SSR 不跑 $effect，预览区为空属预期；页面自身渲染别名表、导出/安装入口与边界说明。
    const { body } = render(SshInventory, { props: { store } });
    expect(body).toContain('OpenSSH include 预览');
    expect(body).toContain('ws-drift-demo'); // 别名表照常渲染
    expect(body).toContain('导出（下载 .conf）');
    expect(body).toContain('host-key 校验保持严格'); // 真实限制如实呈现
    expect(body).toContain('可达'); // SSH 三态展示词（该机 SSHReachable=True）
    // 安装按钮在未勾选确认时必须是禁用的（绝不提供绕过确认的入口）。
    const install = body.indexOf('安装 include 文件');
    expect(install, '安装按钮存在').toBeGreaterThan(-1);
    expect(body.slice(install - 800, install)).toContain('disabled');
  });

  it.runIf(enabled)('连上真实控制面后，Machines 页面渲染真实机器名与三态', async () => {
    const store = new FleetStore({ baseUrl: base, token: process.env.FLEET_TOKEN ?? '' } as never);
    const machines = await fetch(`${base}/api/v1/machines`).then((r) => r.json());
    store.machines = machines.items ?? [];
    const { body } = render(Machines, { props: { store } });
    for (const machine of machines.items ?? []) {
      expect(body).toContain(machine.metadata.name);
      console.log(`${machine.metadata.name}: ${JSON.stringify(machineRow(machine, new Date()).drift.label)}`);
    }
    expect(body).toContain('漂移');
  });
});
