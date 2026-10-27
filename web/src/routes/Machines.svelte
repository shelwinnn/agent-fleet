<script lang="ts">
  /**
   * Machines：列 机器名 | 系统·架构 | 配置档 | agentd | SSH | 漂移 | 收敛 | 最近上报 | 操作。
   *
   * 动作可用性如实标注：probe / inventory 逐行可用——inventory 仅 SSH 通道机器；
   * bootstrap / repair-agentd 尚未提供，按钮禁用并写明原因——不做假动作。
   *
   * 全部文案经 i18n 取值；行级视图按 i18n.locale 重建，切换语言立即跟随。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import type { OperationPhase } from '$lib/api/types.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { machineRows, sshStatusLabel } from '$lib/state/machines.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import {
    actionOutcomeTitle,
    describeActionFailure,
    failureText,
    loadErrorText,
  } from '$lib/state/ssh.js';
  import { operationPhaseLabel } from '$lib/state/operations.js';
  import { t, i18n, formatNumber, type MessageKey, type MessageParams } from '$lib/i18n/index.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  const clock = createClock(15_000);
  const loc = $derived(i18n.locale);
  const rows = $derived(machineRows(store.machines, clock.now, loc));

  let showAdd = $state(false);
  let newName = $state('');
  let newProfile = $state('');
  let newMode = $state<'agentd' | 'ssh'>('agentd');
  let addError = $state<unknown>(null);
  let adding = $state(false);

  /** 动作反馈：成功存 key+params（渲染时翻译，切换语言立即跟随）；失败存原始错误。 */
  type ActionMessage =
    | { kind: 'ok'; titleKey: MessageKey; key: MessageKey; params: MessageParams; detailKey?: MessageKey; detailParams?: MessageParams; phase?: OperationPhase }
    | { kind: 'error'; prefixKey: MessageKey; error: unknown; hintKey?: MessageKey; unresolved?: { id: string; phase?: string } };
  let actionMessage = $state<ActionMessage | null>(null);
  const pendingAction = $state<Record<string, boolean>>({});

  const loadError = $derived(
    store.errors.machines !== undefined
      ? `${t('machines.error.load')}${loadErrorText(store.errors.machines, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );
  const subtitle = $derived(t('machines.subtitle', { n: formatNumber(rows.length, loc) }));

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
      addError = err;
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
        titleKey: 'ok.reconcileAccepted',
        key: 'machines.reconcile.ok',
        params: { name, id: op.metadata.name },
        detailKey: 'machines.reconcile.hint',
        phase: op.status.phase,
      };
      await store.afterMutation('machines', 'operations');
    } catch (err) {
      const failure = describeActionFailure(err, loc);
      actionMessage = {
        kind: 'error',
        prefixKey: 'machines.reconcile.failed',
        error: err,
        unresolved: failure.unresolvedOperationId
          ? { id: failure.unresolvedOperationId, phase: failure.unresolvedPhase }
          : undefined,
      };
      await store.afterMutation('machines');
    } finally {
      pendingAction[key] = false;
    }
  }

  /** SSH 探测：200 即 SSHReachable=True。 */
  async function probeSsh(name: string) {
    const key = `probe:${name}`;
    pendingAction[key] = true;
    actionMessage = null;
    try {
      const m = await store.client.sshProbe(name);
      actionMessage = {
        kind: 'ok',
        titleKey: 'ok.probeDone',
        key: 'machines.probe.ok',
        params: { name },
        detailKey: 'machines.probe.detail',
        detailParams: {
          os: m.status?.os ?? '—',
          arch: m.status?.arch ?? '—',
          homeDir: m.status?.homeDir ?? '—',
        },
      };
      await store.afterMutation('machines');
    } catch (err) {
      actionMessage = {
        kind: 'error',
        prefixKey: 'machines.probe.failed',
        error: err,
        hintKey: 'machines.probe.failedHint',
      };
      await store.afterMutation('machines');
    } finally {
      pendingAction[key] = false;
    }
  }

  /** SSH 观测采集：202 + readOnly 的 Operation。 */
  async function sshInventory(name: string) {
    const key = `inventory:${name}`;
    pendingAction[key] = true;
    actionMessage = null;
    try {
      const op = await store.client.sshInventory(name);
      actionMessage = {
        kind: 'ok',
        titleKey: 'ok.inventoryAccepted',
        key: 'machines.inventory.ok',
        params: { name, id: op.metadata.name },
        detailKey: 'machines.inventory.hint',
        phase: op.status.phase,
      };
      await store.afterMutation('machines', 'operations');
    } catch (err) {
      actionMessage = { kind: 'error', prefixKey: 'machines.inventory.failed', error: err };
      await store.afterMutation('machines', 'operations');
    } finally {
      pendingAction[key] = false;
    }
  }
</script>

<div class="grid gap-5">
  <div class="flex flex-wrap items-end justify-between gap-3">
    <div>
      <h1 class="text-lg font-semibold">{t('nav.machines')}</h1>
      <p class="text-xs text-muted-foreground">{subtitle}</p>
    </div>
    <div class="flex items-center gap-2">
      <Button size="sm" variant="outline" onclick={() => (showAdd = !showAdd)} aria-expanded={showAdd}>{t('machines.add')}</Button>
      <Button size="sm" variant="outline" disabled title={t('machines.bootstrapUnavailable')}>
        {t('machines.bootstrapRepair')}
      </Button>
    </div>
  </div>

  {#if showAdd}
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('machines.add')}</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        <div class="grid gap-1">
          <Label for="machine-name" class="text-xs">{t('machines.form.name')}</Label>
          <Input id="machine-name" bind:value={newName} placeholder="gpu-home" class="w-72" />
        </div>
        <div class="grid gap-1">
          <Label for="machine-profile" class="text-xs">{t('machines.form.profile')}</Label>
          <Input id="machine-profile" bind:value={newProfile} placeholder="default-dev" class="w-72" />
        </div>
        <div class="grid gap-1">
          <Label class="text-xs">{t('machines.form.mode')}</Label>
          <div class="flex gap-2">
            {#each ['agentd', 'ssh'] as mode (mode)}
              <Button
                size="sm"
                variant={newMode === mode ? 'default' : 'outline'}
                aria-pressed={newMode === mode}
                onclick={() => (newMode = mode as 'agentd' | 'ssh')}>{mode}</Button
              >
            {/each}
          </div>
        </div>
        {#if addError}
          <Alert.Root variant="destructive">
            <Alert.Title>{t('machines.createFailed')}</Alert.Title>
            <Alert.Description>{failureText(addError, i18n.locale)}</Alert.Description>
          </Alert.Root>
        {/if}
        <div class="flex gap-2">
          <Button size="sm" onclick={addMachine} disabled={adding || newName.trim() === ''}>
            {adding ? t('machines.creating') : t('machines.create')}
          </Button>
          <Button size="sm" variant="ghost" onclick={() => (showAdd = false)}>{t('common.cancel')}</Button>
        </div>
      </CardContent>
    </Card>
  {/if}

  {#if actionMessage}
    <Alert.Root variant={actionMessage.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{actionMessage.kind === 'error' ? actionOutcomeTitle(actionMessage.error, i18n.locale) : t(actionMessage.titleKey)}</Alert.Title>
      <Alert.Description>
        {#if actionMessage.kind === 'error'}
          {t(actionMessage.prefixKey)}{t('app.labelColon')}{failureText(actionMessage.error, i18n.locale)}
          {#if actionMessage.unresolved}
            <div class="mt-1">
              {t('common.unresolvedOpRef', {
                id: actionMessage.unresolved.id,
                phase: operationPhaseLabel((actionMessage.unresolved.phase ?? 'Unknown') as OperationPhase, i18n.locale),
              })}
            </div>
          {:else if actionMessage.hintKey}
            <div class="mt-1">{t(actionMessage.hintKey)}</div>
          {/if}
        {:else}
          {t(actionMessage.key, actionMessage.params)}
          {#if actionMessage.phase}
            <StateBadge tone="info" label={operationPhaseLabel(actionMessage.phase, i18n.locale)} />
          {/if}
          {#if actionMessage.detailKey}
            <div class="mt-1">{t(actionMessage.detailKey, actionMessage.detailParams)}</div>
          {/if}
        {/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  <Card>
    <CardContent class="pt-6">
      <AsyncState loading={store.loading.machines} error={loadError} empty={rows.length === 0 ? t('machines.empty') : undefined} rows={5} />
      {#if rows.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('machines.col.name')}</Table.Head>
              <Table.Head>{t('machines.col.osArch')}</Table.Head>
              <Table.Head>{t('machines.col.profile')}</Table.Head>
              <Table.Head>{t('machines.col.agentd')}</Table.Head>
              <Table.Head>{t('machines.col.ssh')}</Table.Head>
              <Table.Head>{t('machines.col.drift')}</Table.Head>
              <Table.Head>{t('machines.col.reconciled')}</Table.Head>
              <Table.Head>{t('machines.col.lastSeen')}</Table.Head>
              <Table.Head>{t('machines.col.actions')}</Table.Head>
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
                    <StateBadge
                      tone="warn"
                      label={t('machines.unresolvedBadge')}
                      title={t('machines.unresolvedTitle', { phase: operationPhaseLabel(row.unresolvedPhase as OperationPhase, i18n.locale) })}
                    />
                  {/if}
                </Table.Cell>
                <Table.Cell class="text-xs">{row.osArch}</Table.Cell>
                <Table.Cell class="text-xs">{row.profile}</Table.Cell>
                <Table.Cell><StateBadge tone={row.agentd.tone} label={row.agentd.label} /></Table.Cell>
                <Table.Cell>
                  <StateBadge tone={row.ssh.tone} label={`${row.ssh.mode} · ${sshStatusLabel(row.ssh.label, i18n.locale)}`} />
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
                      title={t('machines.action.reconcileTitle')}
                    >
                      {t('machines.action.reconcile')}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={pendingAction[`probe:${row.name}`]}
                      onclick={() => probeSsh(row.name)}
                      title={t('machines.probeTitle')}
                    >
                      {pendingAction[`probe:${row.name}`] ? t('machines.probing') : t('machines.action.probe')}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={row.ssh.mode !== 'ssh' || pendingAction[`inventory:${row.name}`]}
                      onclick={() => sshInventory(row.name)}
                      title={row.ssh.mode !== 'ssh' ? t('machines.inventoryDisabled') : t('machines.inventoryTitle')}
                    >
                      {pendingAction[`inventory:${row.name}`] ? t('machines.collecting') : t('machines.action.inventory')}
                    </Button>
                    <Button size="sm" variant="ghost" onclick={() => navigate(`/machines/${row.name}`)}>
                      {t('machines.action.detail')}
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
</div>
