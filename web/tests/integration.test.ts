/**
 * 与真实控制面的联调检查（KM-25 验收证据）。
 *
 * 默认跳过；显式开启（控制面已在 FLEET_API 或 127.0.0.1:7788 运行）：
 *   FLEET_INTEGRATION=1 pnpm integration
 *
 * 复用 Web UI 自己的类型化客户端与状态呈现模块（不是另写一套 HTTP 调用），
 * 因此通过意味着"UI 依赖的契约与状态语义在真实后端上成立"：
 *   1. 探针与五类资源列表读通；
 *   2. REST 写入 → SSE 事件（资源类型/ID + revision）→ revision 单调（选择性重取判据）；
 *   3. drift 三态（Unknown 必带原因）与 Machine status 投影一致；
 *   4. 未决操作 → 阻塞动作集合一致（FR-1.10）；
 *   5. Deployment 逐机结果（Superseded/Skipped 与 Failed 分开、不计入成功）；
 *   6. 动作端点错误体映射（NotFound / 409 MachineBusy 的 diagnostics）。
 */
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { ApiError, FleetClient } from '../src/lib/api/client.js';
import { subscribeEvents, type StreamState } from '../src/lib/api/sse.js';
import { driftInputFromApi, driftView } from '../src/lib/state/drift.js';
import { actionAvailability, unresolvedOperations, viewOperations } from '../src/lib/state/operations.js';
import { deploymentProgress, viewTarget } from '../src/lib/state/deployments.js';
import { machineRow } from '../src/lib/state/machines.js';
import type { ResourceEvent } from '../src/lib/api/types.js';

const enabled = process.env.FLEET_INTEGRATION === '1';
const base = process.env.FLEET_API ?? 'http://127.0.0.1:7788';
const token = process.env.FLEET_TOKEN ?? '';
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe.skipIf(!enabled)(`与真实控制面联调（${base}）`, () => {
  const client = new FleetClient({ baseUrl: base, token });
  const stamp = Date.now().toString(36);
  const fixtureProfile = `km25-fixture-p-${stamp}`;
  const fixtureMachine = `km25-fixture-m-${stamp}`;

  // 夹具：一台可渲染期望状态并已物化第 1 代的机器。测试之间不依赖彼此的执行
  // 顺序（此前 3/4 依赖前序测试的残留数据，干净库上会失败）。
  beforeAll(async () => {
    await client.createProfile({
      metadata: { name: fixtureProfile },
      spec: { agents: { fixture: { enabled: true, version: '1.0.0' } } },
    });
    await client.createMachine({
      metadata: { name: fixtureMachine },
      spec: { managementMode: 'agentd', profileRef: fixtureProfile },
    });
    await client.reconcile(fixtureMachine).catch(() => undefined);
  });

  afterAll(async () => {
    await client.deleteMachine(fixtureMachine).catch(() => undefined);
    await client.deleteProfile(fixtureProfile).catch(() => undefined);
  });

  it('1. 探针就绪，五类资源列表可读', async () => {
    const ready = await client.readyz();
    expect(ready.status).toBe('ready');
    const [machines, profiles, skills, providers, deployments] = await Promise.all([
      client.listMachines(),
      client.listProfiles(),
      client.listSkills(),
      client.listProviders(),
      client.listDeployments(),
    ]);
    for (const list of [machines, profiles, skills, providers, deployments]) {
      expect(Array.isArray(list.items)).toBe(true);
    }
    console.log(
      `资源：machines=${machines.items.length} profiles=${profiles.items.length} skills=${skills.items.length} ` +
        `providers=${providers.items.length} deployments=${deployments.items.length}`,
    );
  });

  it('2. SSE 事件按 §23.6 形状到达，且事件 revision == 落库 resourceVersion（M1 不变量）', async () => {
    const seen: ResourceEvent[] = [];
    const states: StreamState[] = [];
    const subscriptionRef: { current: { close: () => void } | null } = { current: null };
    const opened = new Promise<void>((resolve) => {
      const timer = setTimeout(resolve, 8000);
      subscriptionRef.current = subscribeEvents(
        {
          onEvent: (ev) => seen.push(ev),
          onState: (state) => {
            states.push(state);
            if (state === 'open') {
              clearTimeout(timer);
              resolve();
            }
          },
        },
        { baseUrl: base, token, maxBackoffMs: 500 },
      );
    });
    try {
      await opened;
      expect(states).toContain('open');

      const name = `km25-check-${Date.now().toString(36)}`;
      const created = await client.createProfile({
        metadata: { name },
        spec: { agents: { fixture: { enabled: true, version: '1.0.0' } } },
      });
      await sleep(700);
      const first = seen.find((e) => e.resource === 'profiles' && e.id === name);
      expect(first, `未收到 profiles 事件，已收到 ${JSON.stringify(seen)}`).toBeTruthy();
      // 不变量（KM-25 M1）：事件 revision 必须等于落库的 resourceVersion。
      const afterCreate = await client.getProfile(name);
      expect(first?.revision, '创建事件 revision 必须等于落库 resourceVersion').toBe(
        afterCreate.metadata.resourceVersion,
      );

      const updated = await client.updateProfile(name, {
        metadata: { name },
        spec: { agents: { fixture: { enabled: true, version: '1.0.1' } } },
      });
      await sleep(700);
      const events = seen.filter((e) => e.resource === 'profiles' && e.id === name);
      const last = events[events.length - 1];
      const afterUpdate = await client.getProfile(name);
      expect(last.revision, '更新事件 revision 必须等于落库 resourceVersion').toBe(
        afterUpdate.metadata.resourceVersion,
      );
      expect(last.revision).toBeGreaterThan(first?.revision ?? 0);
      expect(updated.metadata.resourceVersion).toBe(afterUpdate.metadata.resourceVersion);
      console.log(
        `profile ${name}：事件 revision=${events.map((e) => e.revision).join('→')}，` +
          `落库 resourceVersion=${afterUpdate.metadata.resourceVersion}（事件版本与落库版本一致）`,
      );
      await client.deleteProfile(name).catch(() => undefined);
    } finally {
      subscriptionRef.current?.close();
    }
  });

  it('3. drift 三态与 Machine status 投影一致；Unknown 必带原因', async () => {
    // 本环境（无在线 agentd / 无 SSH 路径）只能产生 Unknown：断言三态映射与
    // "Unknown 必带原因"，并显式断言 Unknown 不得渲染成"一致"。
    const name = fixtureMachine;

    const machine = await client.getMachine(name);
    const row = machineRow(machine, new Date());
    const drift = await client.getDrift(name);
    const view = driftView(driftInputFromApi(drift), new Date());

    expect(['in-sync', 'unknown', 'drifted']).toContain(view.state);
    expect(view.state, 'drift 视图与 Machine status 投影必须一致').toBe(row.drift.state);
    expect(view.state, '无观测的机器必须是 Unknown（不得是 in-sync）').toBe('unknown');
    expect(view.reasonCode, 'Unknown 必须携带原因码').toBeTruthy();
    expect(view.label, 'Unknown 不得渲染成"一致"').not.toContain('一致');
    expect(view.detail ?? '').toContain('从未采集');
    console.log(
      `机器 ${name}：drift=${view.label} reason=${view.reasonCode} detail=${view.detail}；` +
        `agentd=${row.agentd.label} ssh=${row.ssh.label} reconciled=${row.reconciled.label}`,
    );
  });

  // 明确标注（不是"通过"）：这两态需要节点在场，联调环境产生不出来。
  it.skip('3b. drift=in-sync / drifted 需要在线 agentd 上报观测（第 6 片 SSH 路径或节点接线），本环境无法产生', () => {});

  it('4. 无未决操作时阻塞集合为空（FR-1.10 的另一半）', async () => {
    const name = fixtureMachine;
    const ops = (await client.listOperations(name)).items;
    const unresolved = unresolvedOperations(ops, new Date());
    const availability = actionAvailability(ops, new Date());

    // 本环境（无在线 agentd）的操作都会以 Failed 终结，因此这里断言"无未决"分支；
    // 有未决的分支见 4b（显式标注为不可覆盖）。
    expect(unresolved, '本环境不应存在未决操作（无在线节点，操作即时终结）').toHaveLength(0);
    expect(availability.blockedBy).toBeNull();
    expect(availability.actions.find((a) => a.action === 'reconcile')?.blocked).toBe(false);
    expect(availability.actions.find((a) => a.action === 'rollback')?.blocked).toBe(false);
    expect(availability.actions.find((a) => a.action === 'cancel')?.blocked).toBe(true);
    expect(availability.actions.find((a) => a.action === 'skip')?.blocked).toBe(true);
    expect(availability.actions.find((a) => a.action === 'confirm')?.blocked).toBe(true);
    console.log(
      `机器 ${name} 操作 ${ops.length} 条：` +
        viewOperations(ops, new Date())
          .slice(-5)
          .map((o) => `${o.id.slice(0, 8)}=${o.phase}${o.terminalModifier ? `(${o.terminalModifier})` : ''}/${o.source}`)
          .join(' ') +
        `；未决 ${unresolved.length} 条（阻塞集合已断言）`,
    );
  });

  it.skip('4b. 未决操作（Pending/Running/AwaitingConfirmation/Unknown）需要节点在场或 SSH 路径：本环境无法产生', () => {});

  it('5. 驱动一次真实 reconcile：无在线节点 → Failed(AgentDisconnected)，且机器可再次收敛', async () => {
    const stamp = Date.now().toString(36);
    const profileName = `km25-drive-${stamp}`;
    const machineName = `km25-drive-${stamp}`;
    await client.createProfile({
      metadata: { name: profileName },
      spec: { agents: { fixture: { enabled: true, version: '1.0.0' } } },
    });
    await client.createMachine({
      metadata: { name: machineName },
      spec: { managementMode: 'agentd', profileRef: profileName },
    });
    try {
      // profileRef 指向的期望状态可渲染 → 动作被受理（202 语义）。
      const op = await client.reconcile(machineName);
      expect(op.spec.desiredGeneration, '受理的收敛请求应绑定期望代').toBeGreaterThan(0);
      console.log(`reconcile → operation ${op.metadata.name.slice(0, 8)} phase=${op.status.phase} gen=${op.spec.desiredGeneration}`);

      // 无活跃 Connect 流：控制器按 §4.3 明确失败（AgentDisconnected），不排队、不留未决。
      const after = await client.listOperations(machineName);
      const view = viewOperations(after.items, new Date()).find((o) => o.id === op.metadata.name);
      expect(view, '操作必须能在审计列表里查到').toBeTruthy();
      expect(view?.phase, '无 agent 连接时操作以 Failed 终结（failOperation）').toBe('Failed');
      expect(view?.isUnresolved, 'Failed 是终态：不占用互斥').toBe(false);
      expect(unresolvedOperations(after.items, new Date())).toHaveLength(0);

      // 互斥已释放：同一台机器可以再次发起收敛（不是 409）。
      const again = await client.reconcile(machineName);
      expect(again.metadata.name).toBeTruthy();
      console.log(
        `操作 ${view?.id.slice(0, 8)} 终结相位=${view?.phase}；互斥已释放，第二次 reconcile 仍被受理` +
          `（operation ${again.metadata.name.slice(0, 8)}，phase=${again.status.phase}）`,
      );
    } finally {
      await client.deleteMachine(machineName).catch(() => undefined);
      await client.deleteProfile(profileName).catch(() => undefined);
    }
  });

  // 明确标注（不是"通过"）：409 MachineBusy 与 skip 都需要"未决操作"这一前提。
  it.skip('5b. 409 MachineBusy / skip（互斥例外端点）需要未决操作（需在线 agentd 或 SSH 路径），本环境无法产生', () => {});

  it('6. 经 REST 真实创建发布 → 目标行物化 → 控制器推进（M2 回归）', async () => {
    const stamp = Date.now().toString(36);
    const profileName = `km25-dep-${stamp}`;
    const machineName = `km25-dep-${stamp}`;
    const deploymentName = `km25-rollout-${stamp}`;
    await client.createProfile({
      metadata: { name: profileName },
      spec: { agents: { fixture: { enabled: true, version: '1.0.0' } } },
    });
    await client.createMachine({
      metadata: { name: machineName },
      spec: { managementMode: 'agentd', profileRef: profileName },
    });
    try {
      // 先物化第 1 代期望快照（发布的目标代必须可解析，FR-10.1）。
      await client.reconcile(machineName);

      // 真实创建：POST /api/v1/deployments
      const created = await client.createDeployment({
        metadata: { name: deploymentName },
        spec: {
          machineNames: [machineName],
          targetGeneration: 1,
          strategy: { canary: 1, batchSize: 1, maxUnavailable: 1, pauseOnFailure: true },
        },
      });
      const createdTargets = created.status?.targets ?? [];
      expect(createdTargets, '创建响应必须带物化后的目标行（M2）').toHaveLength(1);
      expect(createdTargets[0].machine).toBe(machineName);
      expect(createdTargets[0].phase).toBe('Pending');
      expect(created.status?.phase).toBe('Pending');
      console.log(`创建 ${deploymentName}：phase=${created.status?.phase} targets=${createdTargets.map((t) => `${t.machine}=${t.phase}`).join(' ')}`);

      // 控制器扫描循环（默认 2s）推进：目标离开 Pending 并带上失败原因；
      // 发布级 reason 绝不能是 M2 的病灶 "no target succeeded"（0 目标才会那样）。
      let dep = created;
      for (let i = 0; i < 12; i++) {
        await sleep(500);
        dep = await client.getDeployment(deploymentName);
        const phase = dep.status?.targets?.[0]?.phase;
        if (phase && phase !== 'Pending' && phase !== 'Running') break;
      }
      const status = dep.status ?? {};
      const progress = deploymentProgress(dep);
      const targets = (status.targets ?? []).map((t) => viewTarget(t, new Date()));
      expect(targets, '推进后仍必须有目标行').toHaveLength(1);
      expect(status.reason ?? '', '不得出现 0 目标的 "no target succeeded"').not.toBe('no target succeeded');
      expect(targets[0].phase, '目标离开 Pending（无 agent 连接 → Failed）').toBe('Failed');
      expect(targets[0].reasonText, 'Failed 目标必须带原因').toBeTruthy();
      expect(targets[0].countsAsSuccess, 'Failed 不计入成功').toBe(false);
      expect(progress.total).toBe(1);
      expect(progress.succeeded).toBe(0);
      console.log(
        `推进后 ${deploymentName}：phase=${status.phase} reason=${status.reason ?? '—'}；` +
          `目标 ${targets.map((t) => `${t.machine}=${t.phaseLabel}(${t.reason ?? '—'})`).join(' ')}` +
          `；进度 成功${progress.succeeded}/失败${progress.failed}/取代${progress.superseded}/跳过${progress.skipped}`,
      );
    } finally {
      await client.deleteDeployment(deploymentName).catch(() => undefined);
      await client.deleteMachine(machineName).catch(() => undefined);
      await client.deleteProfile(profileName).catch(() => undefined);
    }
  });

  // 明确标注（不是"通过"）：Superseded/Skipped 需要真实推进历史（节点在场 + 更新代）。
  it.skip('6b. 目标的 Superseded / Skipped 呈现需要真实推进历史（节点在场且出现更新代），本环境无法产生', () => {});

  it('7. 动作端点错误体映射为 ApiError（NotFound / Invalid）', async () => {
    const missing = await client
      .reconcile('definitely-not-a-machine')
      .then(() => null)
      .catch((err: unknown) => err);
    expect(missing).toBeInstanceOf(ApiError);
    expect((missing as ApiError).reason).toBe('NotFound');
    console.log(`未知机器 reconcile → ${(missing as ApiError).status} ${(missing as ApiError).reason}`);

    // 无 profileRef 的机器：期望状态不可渲染 → 400 Invalid（不是 5xx，也不是静默成功）。
    const stamp = Date.now().toString(36);
    const name = `km25-noprofile-${stamp}`;
    await client.createMachine({ metadata: { name }, spec: { managementMode: 'agentd' } });
    try {
      const invalid = await client
        .reconcile(name)
        .then(() => null)
        .catch((err: unknown) => err);
      expect(invalid).toBeInstanceOf(ApiError);
      expect((invalid as ApiError).reason).toBe('Invalid');
      console.log(`无 profileRef 的机器 reconcile → ${(invalid as ApiError).status} ${(invalid as ApiError).reason}：${(invalid as ApiError).message}`);
    } finally {
      await client.deleteMachine(name).catch(() => undefined);
    }
  });
});

/**
 * SSH 路径联调（KM-28 验收证据）：KM-26 注册的 probe / inventory / include 三端点
 * 在真实控制面上的成功与失败路径。前提：控制面以 SSH 通道装配启动（KM-26 的
 * sshops/IncludeAPI 均非 nil），且 FLEET_SSH_ALIAS（默认 localhost）经控制面的
 * OpenSSH 客户端配置可达（host-key 走严格校验，不允许放宽）。
 */
describe.skipIf(!enabled)(`SSH 路径联调（${base}，KM-28）`, () => {
  const client = new FleetClient({ baseUrl: base, token });
  const stamp = Date.now().toString(36);
  const alias = process.env.FLEET_SSH_ALIAS ?? 'localhost';
  const user = process.env.FLEET_SSH_USER ?? '';
  const profileName = `km28-ssh-p-${stamp}`;
  const machineName = `km28-ssh-m-${stamp}`;

  beforeAll(async () => {
    await client.createProfile({
      metadata: { name: profileName },
      // 节点侧 oneshot 只注册了真实适配器（codex/opencode/omp，KM-26 集成测试同款
      // 夹具）：fixture 家族没有节点适配器；codex 适配器还要求期望 config 是
      // JSON 对象，故与 KM-26 一致带 config.model。
      spec: { agents: { codex: { enabled: true, version: '0.154.0', config: { model: 'gpt-5' } } } },
    });
    await client.createMachine({
      metadata: { name: machineName },
      spec: {
        managementMode: 'ssh',
        profileRef: profileName,
        // hostName 非空的机器才会被 include 导出（FR-12.6 的纳入条件）。
        ssh: { hostAlias: alias, hostName: alias, ...(user ? { user } : {}) },
      },
    });
  });

  afterAll(async () => {
    await client.deleteMachine(machineName).catch(() => undefined);
    await client.deleteProfile(profileName).catch(() => undefined);
  });

  it('1. probe 成功：200 + SSHReachable=True + 平台静态信息写回', async () => {
    const m = await client.sshProbe(machineName);
    const cond = m.status?.conditions?.find((c) => c.type === 'SSHReachable');
    expect(cond?.status, '探测成功必须写 SSHReachable=True').toBe('True');
    expect(m.status?.os, '探测必须带回远端平台（uname）').toBeTruthy();
    expect(m.status?.homeDir, '探测必须带回远端 HOME（printenv）').toBeTruthy();
    console.log(`probe ${machineName} → SSHReachable=${cond?.status} 平台=${m.status?.os}/${m.status?.arch} homeDir=${m.status?.homeDir ?? '—'}（SSH probe 只采 uname/printenv HOME，不含 hostname）`);
  });

  it('2. inventory 成功：202 + readOnly 的 AutoPlan（SSH 通道）Operation，终态 Succeeded', async () => {
    const op = await client.sshInventory(machineName);
    expect(op.spec.type, 'inventory 动作必须是只读 AutoPlan').toBe('AutoPlan');
    expect(op.spec.readOnly, 'inventory 绝不产生变更（readOnly）').toBe(true);
    expect(op.spec.transport).toBe('ssh');
    const listed = (await client.listOperations(machineName)).items.find((o) => o.metadata.name === op.metadata.name);
    expect(listed?.status.phase, '同步执行的采集在操作审计里可查且终态 Succeeded').toBe('Succeeded');
    console.log(`inventory ${machineName} → operation ${op.metadata.name.slice(0, 8)} readOnly=${op.spec.readOnly} phase=${listed?.status.phase}`);
  });

  it('3. include preview 成功：服务端渲染全文 + 计数头包含该机', async () => {
    const preview = await client.sshIncludePreview();
    expect(preview.content).toContain(`Host ${machineName}`);
    expect(preview.content).toContain(`HostName ${alias}`);
    expect(preview.includedHosts, 'hostName 非空的机器必须被纳入').toBeGreaterThan(0);
    console.log(`include preview → included=${preview.includedHosts} skipped=${preview.skippedHosts}，含 ${machineName} 段`);
  });

  it('4. include install 未确认 → 400 Invalid（服务端不落盘，绝不静默改写）', async () => {
    const resp = await fetch(`${base}/api/v1/ssh/include`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ action: 'install' }), // 无 confirm
    });
    expect(resp.status).toBe(400);
    const body = (await resp.json()) as { reason: string; message: string };
    expect(body.reason).toBe('Invalid');
    expect(body.message).toContain('confirm');
    console.log(`install 未确认 → ${resp.status} ${body.reason}：${body.message}`);
  });

  it('5. include install 显式确认 → 200 + path/digest/Include 指令回显', async () => {
    const result = await client.sshIncludeInstall();
    expect(result.path, '安装目标只能是 ~/.ssh/agent-fleet.conf').toContain('agent-fleet.conf');
    expect(result.contentDigest).toMatch(/^sha256:/);
    expect(result.includedHosts).toContain(machineName);
    expect(result.includeDirective).toContain('Include ~/.ssh/agent-fleet.conf');
    const after = await client.sshIncludePreview();
    expect(after.content).toContain(`Host ${machineName}`);
    console.log(`install → path=${result.path} digest=${result.contentDigest.slice(0, 19)}… included=[${result.includedHosts.join(',')}]`);
    console.log(`primaryConfigHint：${result.primaryConfigHint}`);
  });

  it('6. 失败路径：未知机器 probe → 404 NotFound；agentd 通道机器 inventory → 400 Invalid', async () => {
    const missing = await client.sshProbe('definitely-not-a-machine').then(() => null).catch((err: unknown) => err);
    expect(missing).toBeInstanceOf(ApiError);
    expect((missing as ApiError).status).toBe(404);
    expect((missing as ApiError).reason).toBe('NotFound');

    // 该端点只服务 SSH 通道：agentd 机器的手动采集被显式拒绝（不是 5xx、不是静默成功）。
    const stamp = Date.now().toString(36);
    const agentdMachine = `km28-agentd-${stamp}`;
    await client.createMachine({ metadata: { name: agentdMachine }, spec: { managementMode: 'agentd' } });
    try {
      const invalid = await client.sshInventory(agentdMachine).then(() => null).catch((err: unknown) => err);
      expect(invalid).toBeInstanceOf(ApiError);
      expect((invalid as ApiError).status).toBe(400);
      expect((invalid as ApiError).reason).toBe('Invalid');
      console.log(`未知机器 probe → 404 NotFound；agentd 机器 inventory → 400 Invalid：${(invalid as ApiError).message}`);
    } finally {
      await client.deleteMachine(agentdMachine).catch(() => undefined);
    }
  });
});
