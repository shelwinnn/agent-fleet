<script lang="ts">
  /**
   * Machines（spec §24.2）：列 Name | OS/Arch | Profile | agentd | SSH | Drift |
   * Reconciled | Last Seen；动作 Add Machine / Probe SSH / Inventory / Reconcile。
   *
   * 动作可用性如实标注：probe / inventory（KM-26 注册的 SSH 路径，KM-28 接通）
   * 逐行可用——inventory 仅 SSH 通道机器；bootstrap / repair-agentd 仍未实现
   * （docs/ssh-only-oneshot.md），按钮禁用并写明原因——不做假动作。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { machineRows } from '$lib/state/machines.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { describeActionFailure, UNIMPLEMENTED_SSH_ENDPOINTS, UNIMPLEMENTED_NOTICE } from '$lib/state/ssh.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  const clock = createClock(15_000);
  const rows = $derived(machineRows(store.machines, clock.now));

  let showAdd = $state(false);
  let newName = $state('');
  let newProfile = $state('');
  let newMode = $state<'agentd' | 'ssh'>('agentd');
  let addError = $state<string | null>(null);
  let adding = $state(false);
  let actionMessage = $state<{ kind: 'ok' | 'error'; text: string; detail?: string } | null>(null);
  const pendingAction = $state<Record<string, boolean>>({});

  async function addMachine() {
    adding = true;
    addError = null;
    try {
      await store.client.createMachine({
        metadata: { name: newName.trim() },
        spec: {
          managementMode: newMode,
          ...(newProfile.trim() ? { profileRef: newProfile.trim() } : {}),
        },
      });
      showAdd = false;
      newName = '';
      newProfile = '';
      await store.afterMutation('machines');
    } catch (err) {
      const failure = describeActionFailure(err);
      addError = `${failure.title}：${failure.detail}`;
    } finally {
      adding = false;
    }
  }

  async function reconcile(name: string) {
    const key = `reconcile:${name}`;
    pendingAction[key] = true;
    actionMessage = null;
    try {
      const op = await store.client.reconcile(name);
      actionMessage = {
        kind: 'ok',
        text: `已受理 ${name} 的收敛请求（operation ${op.metadata.name}，相位 ${op.status.phase}）。`,
        detail: '未决操作会阻塞该机新的变更类操作，可在 Machine Detail 取消/跳过。',
      };
      await store.afterMutation('machines', 'operations');
    } catch (err) {
      const failure = describeActionFailure(err);
      actionMessage = {
        kind: 'error',
        text: `${failure.title}：${failure.detail}`,
        detail: failure.unresolvedOperationId
          ? `未决操作 ${failure.unresolvedOperationId}（${failure.unresolvedPhase ?? '相位未知'}）`
          : undefined,
      };
      await store.afterMutation('machines');
    } finally {
      pendingAction[key] = false;
    }
  }

  /** SSH 探测（POST /machines/:name/ssh/probe，KM-26 注册）：200 即 SSHReachable=True。 */
  async function probeSsh(name: string) {
    const key = `probe:${name}`;
    pendingAction[key] = true;
    actionMessage = null;
    try {
      const m = await store.client.sshProbe(name);
      actionMessage = {
        kind: 'ok',
        text: `已探测 ${name}：SSH 可达（SSHReachable=True）。`,
        detail: `平台 ${m.status?.os ?? '—'}/${m.status?.arch ?? '—'} · homeDir ${m.status?.homeDir ?? '—'}。探测采集 uname 与 printenv HOME；hostname 等完整观测由 inventory 上报。`,
      };
      await store.afterMutation('machines');
    } catch (err) {
      const failure = describeActionFailure(err);
      actionMessage = {
        kind: 'error',
        text: `SSH 探测失败——${failure.title}：${failure.detail}`,
        detail: '失败原因已写回该机的 SSHReachable 条件（§30.1 reason），Machine Detail 可查看。',
      };
      await store.afterMutation('machines');
    } finally {
      pendingAction[key] = false;
    }
  }

  /** SSH 观测采集（POST /machines/:name/ssh/inventory）：202 + readOnly 的 Operation。 */
  async function sshInventory(name: string) {
    const key = `inventory:${name}`;
    pendingAction[key] = true;
    actionMessage = null;
    try {
      const op = await store.client.sshInventory(name);
      actionMessage = {
        kind: 'ok',
        text: `已受理 ${name} 的 SSH 观测采集（operation ${op.metadata.name}，相位 ${op.status.phase}）。`,
        detail: '采集经 SSH 只读执行（validate→inventory→plan），不产生任何变更；结果体现在该机 Drift 与 lastInventory。',
      };
      await store.afterMutation('machines', 'operations');
    } catch (err) {
      const failure = describeActionFailure(err);
      actionMessage = {
        kind: 'error',
        text: `SSH 观测采集失败——${failure.title}：${failure.detail}`,
      };
      await store.afterMutation('machines', 'operations');
    } finally {
      pendingAction[key] = false;
    }
  }
</script>

<div class="grid gap-5">
  <div class="flex flex-wrap items-end justify-between gap-3">
    <div>
      <h1 class="text-lg font-semibold">Machines</h1>
      <p class="text-xs text-muted-foreground">
        共 {rows.length} 台；Drift/Reconciled 为三态呈现，未知与一致分开显示（FR-14.5）。
      </p>
    </div>
    <div class="flex items-center gap-2">
      <Button size="sm" variant="outline" onclick={() => (showAdd = !showAdd)}>Add Machine</Button>
      <Button size="sm" variant="outline" disabled title="POST /machines/:name/ssh/bootstrap、POST /machines/:name/ssh/repair-agentd 未实现，见 docs/ssh-only-oneshot.md">
        Bootstrap / Repair agentd
      </Button>
    </div>
  </div>

  {#if showAdd}
    <Card>
      <CardHeader><CardTitle class="text-sm">Add Machine（POST /api/v1/machines）</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        <div class="grid gap-1">
          <Label for="machine-name" class="text-xs">名称（类型内唯一，DNS 风格）</Label>
          <Input id="machine-name" bind:value={newName} placeholder="gpu-home" class="w-72" />
        </div>
        <div class="grid gap-1">
          <Label for="machine-profile" class="text-xs">profileRef（可留空）</Label>
          <Input id="machine-profile" bind:value={newProfile} placeholder="default-dev" class="w-72" />
        </div>
        <div class="grid gap-1">
          <Label class="text-xs">managementMode</Label>
          <div class="flex gap-2">
            {#each ['agentd', 'ssh'] as mode (mode)}
              <Button
                size="sm"
                variant={newMode === mode ? 'default' : 'outline'}
                onclick={() => (newMode = mode as 'agentd' | 'ssh')}>{mode}</Button
              >
            {/each}
          </div>
        </div>
        {#if addError}
          <Alert.Root variant="destructive">
            <Alert.Title>创建失败</Alert.Title>
            <Alert.Description>{addError}</Alert.Description>
          </Alert.Root>
        {/if}
        <div class="flex gap-2">
          <Button size="sm" onclick={addMachine} disabled={adding || newName.trim() === ''}>
            {adding ? '创建中…' : '创建'}
          </Button>
          <Button size="sm" variant="ghost" onclick={() => (showAdd = false)}>取消</Button>
        </div>
      </CardContent>
    </Card>
  {/if}

  {#if actionMessage}
    <Alert.Root variant={actionMessage.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{actionMessage.kind === 'error' ? '动作被拒绝' : '动作已受理'}</Alert.Title>
      <Alert.Description>
        {actionMessage.text}
        {#if actionMessage.detail}<div class="mt-1">{actionMessage.detail}</div>{/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  <Card>
    <CardContent class="pt-6">
      <AsyncState
        loading={store.loading.machines}
        error={store.errors.machines}
        empty={rows.length === 0 ? '还没有机器。用 Add Machine 创建第一台。' : undefined}
        rows={5}
      />
      {#if rows.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>Name</Table.Head>
              <Table.Head>OS/Arch</Table.Head>
              <Table.Head>Profile</Table.Head>
              <Table.Head>agentd</Table.Head>
              <Table.Head>SSH</Table.Head>
              <Table.Head>Drift</Table.Head>
              <Table.Head>Reconciled</Table.Head>
              <Table.Head>Last Seen</Table.Head>
              <Table.Head>动作</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each rows as row (row.name)}
              <Table.Row>
                <Table.Cell>
                  <button class="font-medium underline" onclick={() => navigate(`/machines/${row.name}`)}>
                    {row.name}
                  </button>
                  {#if row.unresolvedPhase}
                    <StateBadge tone="warn" label={`未决 ${row.unresolvedPhase}`} title="存在未决操作：新的变更类操作会被拒绝（FR-1.10）" />
                  {/if}
                </Table.Cell>
                <Table.Cell class="text-xs">{row.osArch}</Table.Cell>
                <Table.Cell class="text-xs">{row.profile}</Table.Cell>
                <Table.Cell><StateBadge tone={row.agentd.tone} label={row.agentd.label} /></Table.Cell>
                <Table.Cell>
                  <StateBadge tone={row.ssh.tone} label={`${row.ssh.mode}: ${row.ssh.label}`} />
                </Table.Cell>
                <Table.Cell>
                  <StateBadge tone={row.drift.tone} label={row.drift.label} title={row.drift.detail ?? ''} />
                </Table.Cell>
                <Table.Cell>
                  <StateBadge tone={row.reconciled.tone} label={row.reconciled.label} title={row.reconciled.reason ?? ''} />
                </Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">{row.lastSeenText}</Table.Cell>
                <Table.Cell>
                  <div class="flex gap-1">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={pendingAction[`reconcile:${row.name}`]}
                      onclick={() => reconcile(row.name)}
                      title="POST /machines/:name/reconcile；该机存在未决操作时返回 409 MachineBusy"
                    >
                      Reconcile
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={pendingAction[`probe:${row.name}`]}
                      onclick={() => probeSsh(row.name)}
                      title="POST /machines/:name/ssh/probe（KM-26 注册）：ssh -G + 远端平台采集，200 即 SSHReachable=True；不可达以 §30.1 reason 报错"
                    >
                      {pendingAction[`probe:${row.name}`] ? '探测中…' : 'Probe SSH'}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={row.ssh.mode !== 'ssh' || pendingAction[`inventory:${row.name}`]}
                      onclick={() => sshInventory(row.name)}
                      title={
                        row.ssh.mode !== 'ssh'
                          ? 'POST /machines/:name/ssh/inventory 仅 SSH 通道机器可用（agentd 通道的手动采集未接线，服务端返回 400 Invalid）'
                          : 'POST /machines/:name/ssh/inventory：经 SSH 只读采集观测（validate→inventory→plan），不产生任何变更'
                      }
                    >
                      {pendingAction[`inventory:${row.name}`] ? '采集中…' : 'Inventory'}
                    </Button>
                    <Button size="sm" variant="ghost" onclick={() => navigate(`/machines/${row.name}`)}>
                      详情
                    </Button>
                  </div>
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  <Alert.Root>
    <Alert.Title>仍未实现的动作端点</Alert.Title>
    <Alert.Description>
      {UNIMPLEMENTED_SSH_ENDPOINTS.join('、')} {UNIMPLEMENTED_NOTICE}。按钮保持禁用并标注原因，不提供假入口。
      Skill 的 resolve 未实现（见 docs/web-ui.md）。SSH 路径的 probe 与 inventory
      已注册并接通（KM-26/KM-28）：Probe SSH 逐行可用，Inventory 仅 SSH 通道机器可用。
    </Alert.Description>
  </Alert.Root>
</div>
