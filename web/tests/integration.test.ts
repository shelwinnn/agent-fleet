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
import { describe, expect, it } from 'vitest';
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

  it('2. SSE 事件按 §23.6 形状到达，且同一对象的 revision 单调递增', async () => {
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

      await client.updateProfile(name, {
        metadata: { name },
        spec: { agents: { fixture: { enabled: true, version: '1.0.1' } } },
      });
      await sleep(700);
      const events = seen.filter((e) => e.resource === 'profiles' && e.id === name);
      const last = events[events.length - 1];
      expect(last.revision).toBeGreaterThan(created.metadata.resourceVersion ?? 0);
      console.log(`profile ${name}：创建 rv=${created.metadata.resourceVersion}，事件 revision=${events.map((e) => e.revision).join('→')}`);
      await client.deleteProfile(name).catch(() => undefined);
    } finally {
      subscriptionRef.current?.close();
    }
  });

  it('3. drift 三态与 Machine status 投影一致；Unknown 必带原因', async () => {
    const machines = (await client.listMachines()).items;
    if (machines.length === 0) {
      console.log('联调环境没有机器：drift/操作两节以自建机器覆盖');
    }
    const name =
      machines[0]?.metadata.name ??
      (
        await client.createMachine({
          metadata: { name: `km25-m-${Date.now().toString(36)}` },
          spec: { managementMode: 'agentd' },
        })
      ).metadata.name;

    const machine = await client.getMachine(name);
    const row = machineRow(machine, new Date());
    const drift = await client.getDrift(name);
    const view = driftView(driftInputFromApi(drift), new Date());

    expect(['in-sync', 'unknown', 'drifted']).toContain(view.state);
    expect(view.state).toBe(row.drift.state);
    if (view.state === 'unknown') {
      expect(view.reasonCode, 'Unknown 必须携带原因').toBeTruthy();
      expect(view.label).not.toContain('一致');
    }
    console.log(
      `机器 ${name}：drift=${view.label} reason=${view.reasonCode ?? '—'} detail=${view.detail ?? ''}；` +
        `agentd=${row.agentd.label} ssh=${row.ssh.label} reconciled=${row.reconciled.label}`,
    );
  });

  it('4. 未决操作 → 阻塞集合一致；无未决时相反的断言（FR-1.10）', async () => {
    const machine = (await client.listMachines()).items[0];
    if (!machine) {
      console.log('无机器可查，跳过');
      return;
    }
    const name = machine.metadata.name;
    const ops = (await client.listOperations(name)).items;
    const unresolved = unresolvedOperations(ops, new Date());
    const availability = actionAvailability(ops, new Date());

    if (unresolved.length === 0) {
      expect(availability.blockedBy).toBeNull();
      expect(availability.actions.find((a) => a.action === 'reconcile')?.blocked).toBe(false);
      expect(availability.actions.find((a) => a.action === 'cancel')?.blocked).toBe(true);
    } else {
      expect(availability.blockedBy?.id).toBe(unresolved[0].id);
      expect(availability.actions.find((a) => a.action === 'reconcile')?.blocked).toBe(true);
      expect(availability.actions.find((a) => a.action === 'skip')?.blocked).toBe(false);
      expect(availability.actions.find((a) => a.action === 'cancel')?.blocked).toBe(false);
    }
    console.log(
      `机器 ${name} 操作 ${ops.length} 条：` +
        viewOperations(ops, new Date())
          .slice(-5)
          .map((o) => `${o.id.slice(0, 8)}=${o.phase}${o.terminalModifier ? `(${o.terminalModifier})` : ''}/${o.source}`)
          .join(' ') +
        `；未决 ${unresolved.length} 条`,
    );
  });

  it('5. 驱动一次真实 reconcile：未决则 409 MachineBusy 并要求原因才可跳过', async () => {
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
      console.log(`reconcile → operation ${op.metadata.name.slice(0, 8)} phase=${op.status.phase}`);

      const unresolved = unresolvedOperations([op], new Date());
      if (unresolved.length > 0) {
        const busy = await client.reconcile(machineName).catch((err: unknown) => err);
        expect(busy).toBeInstanceOf(ApiError);
        expect((busy as ApiError).isMachineBusy).toBe(true);
        expect((busy as ApiError).unresolvedOperationId).toBeTruthy();
        console.log(
          `第二条变更操作被拒：409 ${(busy as ApiError).reason}` +
            `（operationId=${(busy as ApiError).unresolvedOperationId} phase=${(busy as ApiError).unresolvedPhase}）`,
        );

        const skipped = await client.skipOperation(machineName, op.metadata.name, '联调脚本：确认节点未接线，显式跳过（留审计）');
        expect(skipped.status.phase).toBe('Succeeded');
        expect(['Skipped', 'Superseded']).toContain(skipped.status.terminalModifier ?? '');
        const after = await client.listOperations(machineName);
        expect(unresolvedOperations(after.items, new Date())).toHaveLength(0);
        console.log(`跳过后终态：${skipped.status.phase}(${skipped.status.terminalModifier})，互斥已释放`);
      } else {
        // 无活跃 Connect 流时控制器按 §4.3 明确失败（AgentDisconnected），此处如实记录。
        expect(op.status.phase).toBe('Failed');
        console.log(
          `该机无活跃 agentd 连接：操作以 Failed 终结（verify/原因见操作记录），` +
            `未决分支未触发——409 MachineBusy 的映射由单元测试覆盖（tests/client.test.ts）`,
        );
      }
    } finally {
      await client.deleteMachine(machineName).catch(() => undefined);
      await client.deleteProfile(profileName).catch(() => undefined);
    }
  });

  it('6. Deployment 逐机结果：Superseded/Skipped 与 Failed 分开且不计入成功', async () => {
    const deployments = (await client.listDeployments()).items;
    if (deployments.length === 0) {
      console.log('当前无 Deployment：逐机结果呈现未在本轮联调覆盖（已在交付评论的证据清单中说明）');
      return;
    }
    for (const dep of deployments) {
      const progress = deploymentProgress(dep);
      const targets = (dep.status?.targets ?? []).map((t) => viewTarget(t, new Date()));
      for (const target of targets) {
        if (target.phase === 'Superseded' || target.phase === 'Skipped') {
          expect(target.countsAsSuccess).toBe(false);
          expect(target.tone).not.toBe('danger');
          expect(target.reasonText, 'Superseded/Skipped 必须带原因说明').toBeTruthy();
        }
        if (target.phase === 'Failed') expect(target.tone).toBe('danger');
      }
      console.log(
        `Deployment ${dep.metadata.name}：phase=${dep.status?.phase ?? '—'} 目标 ${progress.total}` +
          `（成功 ${progress.succeeded}/失败 ${progress.failed}/取代 ${progress.superseded}/跳过 ${progress.skipped}）；` +
          targets.map((t) => `${t.machine}=${t.phaseLabel}${t.reason ? `(${t.reason})` : ''}`).join(' '),
      );
    }
  });

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
