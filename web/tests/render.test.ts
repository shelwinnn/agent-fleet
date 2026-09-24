/**
 * 页面与状态呈现证据（KM-25 验收项："页面与状态呈现的证据"）。
 *
 * 用 `svelte/server` 把**真实的页面组件**渲染成 HTML，数据来自真实控制面
 * （FLEET_INTEGRATION=1 时）；断言 FR-14.5 的四组状态确实出现在渲染结果里：
 * drift 三态（Unknown 不得显示为"一致"）、未决操作与阻塞、Deployment 目标的
 * Superseded/Skipped 原因、以及"无证书吊销"限制标注与路由清单。
 *
 * 说明：SSR 不跑 $effect/onMount（数据加载在浏览器侧），所以这里先把 store 填好
 * 再渲染——渲染路径与浏览器一致（同一批组件、同一批派生视图）。
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
  it('路由清单覆盖 FR-14.3 的七个页面', () => {
    const pages = ROUTE_TABLE.map((r) => r.page).join('\n');
    for (const page of ['Overview', 'Machines', 'Machine Detail', 'Profiles', 'Skills', 'Deployments', 'SSH Inventory']) {
      expect(pages).toContain(page);
    }
    console.log(ROUTE_TABLE.map((r) => `${r.path}  →  ${r.page}`).join('\n'));
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

  it('Machines 渲染八列，并对 Unknown 使用"未知或过期"而不是"一致"', () => {
    const store = seededStore();
    const { body } = render(Machines, { props: { store } });
    for (const column of ['Name', 'OS/Arch', 'Profile', 'agentd', 'SSH', 'Drift', 'Reconciled', 'Last Seen']) {
      expect(body).toContain(column);
    }
    expect(body).toContain('未知或过期');
    expect(body).toContain('未决 AwaitingConfirmation');
    // KM-28：probe / inventory 是接通的真实动作（逐行按钮）；仍未实现的端点必须
    // 标注"未实现，见 docs/ssh-only-oneshot.md"，不暗示后续切片会自动有。
    expect(body).toContain('Probe SSH');
    expect(body).toContain('Inventory');
    expect(body).toContain('未实现，见 docs/ssh-only-oneshot.md');
    expect(body).not.toContain('属第 6 片');
    // 该夹具是 agentd 机器：inventory 入口必须禁用（仅 SSH 通道机器可用）。
    const inventory = body.indexOf('>Inventory<');
    expect(inventory, 'Inventory 按钮存在').toBeGreaterThan(-1);
    expect(body.slice(inventory - 600, inventory)).toContain('disabled');
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
      '1 · 联通与状态',
      '2 · 当前 Agents 与版本',
      '3 · desired vs observed',
      '4 · Skills',
      '5 · model / provider',
      '6 · MCP',
      '7 · drift diff',
      '8 · 操作历史与未决操作',
      '9 · bootstrap / repair / 回滚动作',
    ]) {
      expect(body).toContain(section);
    }
    expect(body).toContain('等待确认（剩余');
    expect(body).toContain('确认不等于'); // plan 基线失效提示
    expect(body).toContain('未知或过期');
    expect(body).toContain('取消');
    expect(body).toContain('跳过');
    expect(body).toContain('不臆造 diff'); // §3：不推断字段级差异
    // 六个条件各自保留类型名（回归：曾因 conditionView 不带 type 而全部渲染成 Drifted）
    for (const label of [
      'SSH 可达: True',
      'agentd 在线: True',
      'inventory 就绪: True',
      '漂移: Unknown',
      '已收敛: Unknown',
      '降级: False',
    ]) {
      expect(body).toContain(label);
    }
    // 未验证的能力声明逐字转述，不宣称已验证
    expect(body).toContain('unverified');
  });

  it('drifted 状态下 §7 区块显式说明"逐字段 diff 未由 API 暴露"', () => {
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
    expect(body).toContain('逐字段 diff 未由 API 暴露');
    expect(body).toContain('不推断');
    // 裁定 3：Show Diff 必须是"禁用 + 原因标注"的入口，而不是只留一句说明。
    expect(body).toContain('Show Diff（未实现：缺字段级 diff 端点）');
    expect(body).toContain('没有字段级 diff 端点');
    const showDiff = body.slice(body.indexOf('Show Diff') - 1200, body.indexOf('Show Diff'));
    expect(showDiff, 'Show Diff 必须是禁用按钮（disabled 属性在按钮上）').toContain('disabled');
  });

  it('Deployments 把 Superseded/Skipped 与 Failed 分开呈现并带原因', () => {
    const store = seededStore();
    const { body } = render(Deployments, { props: { store, name: 'rollout-42' } });
    expect(body).toContain('已被取代');
    expect(body).toContain('已在更新代'); // Superseded 的原因说明
    expect(body).toContain('已跳过');
    expect(body).toContain('不计入成功'); // Skipped 的边界说明
    expect(body).toContain('失败');
    expect(body).toContain('回滚自 gen14 至 gen12 的内容');
    expect(body).toContain('不计入失败'); // Blocked 的边界说明
  });

  it('SSH Inventory 渲染别名与服务端渲染说明，安装入口默认禁用（需显式确认）', () => {
    const store = seededStore();
    const { body } = render(SshInventory, { props: { store } });
    // KM-28：include 内容改为服务端渲染（SSR 不跑 $effect，预览区为空属预期），
    // 页面自身渲染别名表、导出/安装入口与 FR-12.6 边界说明。
    expect(body).toContain('OpenSSH include 预览（服务端渲染）');
    expect(body).toContain('ws-drift-demo'); // 别名表照常渲染
    expect(body).toContain('Export（下载 .conf）');
    expect(body).toContain('无证书吊销');
    expect(body).toContain('FR-12.3'); // host-key 保持标准策略的边界说明
    // 安装按钮在未勾选确认时必须是禁用的（绝不提供绕过确认的入口）。
    const install = body.indexOf('安装/更新 include 文件（需显式确认）');
    expect(install, '安装按钮存在').toBeGreaterThan(-1);
    expect(body.slice(install - 800, install)).toContain('disabled');
  });

  it.runIf(enabled)('连上真实控制面后，Machines 页面渲染真实机器名与三态', async () => {
    const store = new FleetStore({ baseUrl: base, token: process.env.FLEET_TOKEN ?? '' } as never);
    const client = new FleetStore();
    void client;
    const machines = await fetch(`${base}/api/v1/machines`).then((r) => r.json());
    store.machines = machines.items ?? [];
    const { body } = render(Machines, { props: { store } });
    for (const machine of machines.items ?? []) {
      expect(body).toContain(machine.metadata.name);
      console.log(`${machine.metadata.name}: ${JSON.stringify(machineRow(machine, new Date()).drift.label)}`);
    }
    expect(body).toContain('Drift');
  });
});
