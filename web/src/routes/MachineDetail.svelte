<script lang="ts">
  /**
   * Machine Detail 的 9 个区块：联通与状态 / Agents 与版本 / 期望与观测 / 技能 /
   * 模型与 Provider / MCP / 漂移详情 / 操作历史与未决操作 / 回滚与其他动作。
   *
   * 全部文案经 i18n 取值；行级视图（drift/operations/availability/awaiting）
   * 按 i18n.locale 重建，切换语言立即跟随。动作反馈在渲染期翻译或本地化。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import type { OperationPhase } from '$lib/api/types.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import * as Tabs from '$lib/components/ui/tabs/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { allConditionSummaries, driftInputFromApi, driftView } from '$lib/state/drift.js';
  import {
    actionAvailability,
    modifierLabel,
    operationPhaseLabel,
    viewOperations,
  } from '$lib/state/operations.js';
  import {
    actionOutcomeTitle,
    agentVersionNotice,
    awaitingConfirmations,
    describeActionFailure,
    failureText,
    hostKeyNotice,
    loadErrorText,
    planTimeoutNotice,
  } from '$lib/state/ssh.js';
  import { formatRelativeTime, formatTime, prettyJSON, shortDigest } from '$lib/state/format.js';
  import { untrack } from 'svelte';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import type { RenderPreview } from '$lib/api/types.js';
  import { t, i18n, type MessageKey, type MessageParams } from '$lib/i18n/index.js';

  interface Props {
    store: FleetStore;
    name: string;
  }
  let { store, name }: Props = $props();

  const clock = createClock(15_000);
  const loc = $derived(i18n.locale);
  const detail = $derived(store.details[name]);
  const machine = $derived(detail?.machine ?? store.machineByName(name));
  const conditions = $derived(allConditionSummaries(machine?.status?.conditions, loc));
  const drift = $derived(detail?.drift ? driftView(driftInputFromApi(detail.drift), clock.now, loc) : null);
  const ops = $derived(viewOperations(detail?.operations ?? [], clock.now, loc));
  const availability = $derived(actionAvailability(detail?.operations ?? [], clock.now, loc));
  const awaiting = $derived(awaitingConfirmations(detail?.operations ?? [], clock.now, loc));
  const hostKey = $derived(machine ? hostKeyNotice(machine, loc) : null);
  const agentVersion = $derived(machine ? agentVersionNotice(machine, loc) : null);
  const profileRef = $derived(machine?.spec?.profileRef);
  const loadError = $derived(
    detail?.error !== undefined
      ? `${t('detail.error.load')}${loadErrorText(detail.error, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );

  let render = $state<RenderPreview | null>(null);
  let renderError = $state<unknown>(null);
  let renderLoading = $state(false);

  /** 本地校验失败：不走错误映射，直接给固定文案的 key。 */
  class SkipReasonRequired extends Error {
    constructor() {
      super('skip-reason-required');
    }
  }
  class RollbackGenInvalid extends Error {
    constructor() {
      super('rollback-gen-invalid');
    }
  }

  function validationKey(err: unknown): MessageKey | undefined {
    if (err instanceof SkipReasonRequired) return 'detail.skip.reasonRequired';
    if (err instanceof RollbackGenInvalid) return 'detail.rollback.genInvalid';
    return undefined;
  }

  /** 动作反馈：成功存 key+params（渲染期翻译）；失败存原始错误（渲染期本地化）。 */
  type ActionMessage =
    | { kind: 'ok'; titleKey: MessageKey; key: MessageKey; params: MessageParams; detailKey?: MessageKey; detailParams?: MessageParams; phase?: OperationPhase }
    | { kind: 'error'; error: unknown; unresolved?: { id: string; phase?: string }; replanHint?: boolean };
  let actionMessage = $state<ActionMessage | null>(null);
  let skipReason = $state('');
  let rollbackGeneration = $state('');
  let busy = $state<string | null>(null);

  // 路由切换时登记当前机器（SSE 重取计划据此裁剪）并拉取详情。
  // 注意：loadMachine 会读 store.details，若直接放在 effect 里会形成
  // "加载 → details 变化 → 再次加载"的自激循环，因此用 untrack 只依赖 name。
  $effect(() => {
    const target = name;
    store.activeMachine = target;
    untrack(() => {
      void store.loadMachine(target);
    });
    return () => {
      if (store.activeMachine === target) store.activeMachine = null;
    };
  });

  $effect(() => {
    if (!profileRef || !machine) return;
    void loadRender(profileRef);
  });

  async function loadRender(profile: string) {
    renderLoading = true;
    renderError = null;
    try {
      render = await store.client.renderProfile(profile, name);
    } catch (err) {
      render = null;
      renderError = err;
    } finally {
      renderLoading = false;
    }
  }

  async function act(
    key: string,
    fn: () => Promise<{ titleKey: MessageKey; key: MessageKey; params: MessageParams; phase?: OperationPhase }>,
    detailKey?: MessageKey,
  ) {
    busy = key;
    actionMessage = null;
    try {
      const result = await fn();
      actionMessage = { kind: 'ok', ...result, detailKey };
      await store.afterMutation('machines', 'operations', 'deployments');
      if (profileRef) await loadRender(profileRef);
    } catch (err) {
      const failure = describeActionFailure(err, i18n.locale);
      actionMessage = {
        kind: 'error',
        error: err,
        unresolved:
          failure.unresolvedOperationId !== undefined
            ? { id: failure.unresolvedOperationId, phase: failure.unresolvedPhase }
            : undefined,
        replanHint: failure.replanHint !== undefined,
      };
    } finally {
      busy = null;
    }
  }

  const reconcile = () =>
    act(
      'reconcile',
      async () => {
        const op = await store.client.reconcile(name);
        return { titleKey: 'ok.reconcileAccepted', key: 'machines.reconcile.ok', params: { name, id: op.metadata.name }, phase: op.status.phase };
      },
      'detail.confirm.hint',
    );

  const confirmPlan = (digest: string) =>
    act(
      'confirm',
      async () => {
        const op = await store.client.reconcile(name, digest);
        return { titleKey: 'ok.planConfirmed', key: 'detail.confirm.ok', params: { id: op.metadata.name }, phase: op.status.phase };
      },
      'detail.confirm.hint',
    );

  const cancelOp = (opId: string) =>
    act('cancel', async () => {
      const op = await store.client.cancelOperation(name, opId);
      return { titleKey: 'ok.cancelRequested', key: 'detail.cancel.ok', params: { id: op.metadata.name }, phase: op.status.phase };
    }, 'ops.cancelConsequences');

  const skipOp = (opId: string) =>
    act(
      'skip',
      async () => {
        if (!skipReason.trim()) throw new SkipReasonRequired();
        const op = await store.client.skipOperation(name, opId, skipReason.trim());
        skipReason = '';
        return { titleKey: 'ok.skipped', key: 'detail.skip.ok', params: { id: op.metadata.name }, phase: op.status.phase };
      },
      'ops.skipConsequences',
    );

  const rollback = () =>
    act('rollback', async () => {
      const gen = Number(rollbackGeneration);
      if (!Number.isInteger(gen) || gen <= 0) throw new RollbackGenInvalid();
      const op = await store.client.rollbackMachine(name, gen);
      rollbackGeneration = '';
      return { titleKey: 'ok.rollbackAccepted', key: 'detail.rollback.ok', params: { id: op.metadata.name, gen }, phase: op.status.phase };
    });

  /** SSH 探测：200 即 SSHReachable=True。 */
  const probeSsh = () =>
    act(
      'probe',
      async () => {
        const m = await store.client.sshProbe(name);
        return {
          titleKey: 'ok.probeDone',
          key: 'detail.probe.ok',
          params: { os: m.status?.os ?? '—', arch: m.status?.arch ?? '—', homeDir: m.status?.homeDir ?? '—' },
        };
      },
      'machines.probeTitle',
    );

  /** SSH 观测采集：202 + readOnly 的 Operation。 */
  const requestInventory = () =>
    act(
      'inventory',
      async () => {
        const op = await store.client.sshInventory(name);
        return { titleKey: 'ok.inventoryAccepted', key: 'detail.inventory.ok', params: { id: op.metadata.name }, phase: op.status.phase };
      },
      'detail.inventory.hint',
    );

  const headerSubtitle = $derived(
    `${t('detail.header.mode')} ${machine?.spec?.managementMode ?? 'agentd'} · ${t('detail.header.profile')} ${profileRef ?? '—'} · ${t('detail.header.created')} ${formatRelativeTime(machine?.metadata?.creationTimestamp ?? '', clock.now, loc)}`,
  );
</script>

<div class="grid gap-5">
  <div class="flex flex-wrap items-end justify-between gap-3">
    <div>
      <button class="text-xs text-muted-foreground underline" onclick={() => navigate('/machines')}>
        {t('detail.back')}
      </button>
      <h1 class="text-lg font-semibold">{name}</h1>
      <p class="text-xs text-muted-foreground">{headerSubtitle}</p>
    </div>
    <div class="flex flex-wrap gap-2">
      <Button size="sm" variant="outline" disabled={availability.actions[0].blocked || busy !== null} onclick={reconcile}>
        {t('machines.action.reconcile')}
      </Button>
      <Button size="sm" variant="outline" disabled title={t('machines.bootstrapUnavailable')}>
        {t('machines.bootstrapRepair')}
      </Button>
    </div>
  </div>

  <AsyncState
    loading={detail?.loading && !machine}
    error={loadError}
    empty={!machine ? t('detail.notFound', { name }) : undefined}
  />

  {#if machine}
    {#if hostKey}
      <Alert.Root variant="destructive">
        <Alert.Title>{hostKey.title}</Alert.Title>
        <Alert.Description>{hostKey.detail}</Alert.Description>
      </Alert.Root>
    {/if}
    {#if agentVersion}
      <Alert.Root>
        <Alert.Title>{agentVersion.title}</Alert.Title>
        <Alert.Description>{agentVersion.detail}</Alert.Description>
      </Alert.Root>
    {/if}

    {#if actionMessage}
      <Alert.Root variant={actionMessage.kind === 'error' ? 'destructive' : 'default'}>
        <Alert.Title>{actionMessage.kind === 'error' ? actionOutcomeTitle(actionMessage.error, i18n.locale) : t(actionMessage.titleKey)}</Alert.Title>
        <Alert.Description>
          {#if actionMessage.kind === 'error'}
            {@const validation = validationKey(actionMessage.error)}
            {#if validation}
              {t(validation)}
            {:else}
              {failureText(actionMessage.error, i18n.locale)}
            {/if}
            {#if actionMessage.unresolved}
              <div class="mt-1">
                {t('common.unresolvedOpRef', {
                  id: actionMessage.unresolved.id,
                  phase: operationPhaseLabel((actionMessage.unresolved.phase ?? 'Unknown') as OperationPhase, i18n.locale),
                })}
              </div>
            {:else if actionMessage.replanHint}
              <div class="mt-1">{t('error.replan.hint')}</div>
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

    <!-- 联通与状态 -->
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('detail.section.connectivity')}</CardTitle></CardHeader>
      <CardContent class="grid gap-4">
        <div class="flex flex-wrap gap-2">
          {#each conditions as cond (cond.type)}
            <StateBadge
              tone={cond.tone}
              label={cond.label}
              title={`${cond.message ?? ''}${cond.reason ? ` (reason=${cond.reason})` : ''} ${t('detail.condition.transition')} ${formatTime(cond.lastTransitionTime, i18n.locale)}`}
            />
          {/each}
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" disabled={busy !== null} onclick={probeSsh}>
            {busy === 'probe' ? t('machines.probing') : t('machines.action.probe')}
          </Button>
          <span class="text-xs text-muted-foreground">{t('detail.probe.caption')}</span>
        </div>
        <div class="grid grid-cols-2 gap-x-6 gap-y-1 text-xs md:grid-cols-4">
          <div><span class="text-muted-foreground">{t('detail.field.osArch')}: </span>{machine.status?.os ?? '—'}/{machine.status?.arch ?? '—'}</div>
          <div><span class="text-muted-foreground">{t('detail.field.hostname')}: </span>{machine.status?.hostname ?? '—'}</div>
          <div><span class="text-muted-foreground">{t('detail.field.homeDir')}: </span>{machine.status?.homeDir ?? '—'}</div>
          <div><span class="text-muted-foreground">{t('detail.field.kernel')}: </span>{machine.status?.kernelVersion ?? '—'}</div>
          <div><span class="text-muted-foreground">{t('detail.field.agentdVersion')}: </span>{machine.status?.agentdVersion ?? '—'}</div>
          <div><span class="text-muted-foreground">{t('detail.field.lastHeartbeat')}: </span>{formatRelativeTime(machine.status?.lastHeartbeatAt ?? '', clock.now, loc)}</div>
          <div><span class="text-muted-foreground">{t('detail.field.lastInventory')}: </span>{formatRelativeTime(machine.status?.lastInventoryAt ?? '', clock.now, loc)}</div>
          <div><span class="text-muted-foreground">{t('detail.field.inventorySeq')}: </span>{machine.status?.inventorySeq ?? '—'}</div>
          <div><span class="text-muted-foreground">{t('detail.field.desiredGeneration')}: </span>{machine.status?.desiredGeneration ?? '—'}</div>
          <div><span class="text-muted-foreground">{t('detail.field.observedGeneration')}: </span>{machine.status?.observedGeneration ?? '—'}</div>
        </div>
      </CardContent>
    </Card>

    <!-- Agents 与版本 -->
    <Card>
      <CardHeader>
        <CardTitle class="text-sm">{t('detail.section.agents')}</CardTitle>
        <p class="text-xs text-muted-foreground">{t('detail.agents.subtitle')}</p>
      </CardHeader>
      <CardContent class="grid gap-4 lg:grid-cols-2">
        <div>
          {#if renderLoading}
            <AsyncState loading rows={2} />
          {:else if renderError}
            <Alert.Root variant="destructive">
              <Alert.Title>{t('profiles.render.failed')}</Alert.Title>
              <Alert.Description>{failureText(renderError, i18n.locale)}</Alert.Description>
            </Alert.Root>
          {:else if render?.desired?.agents && Object.keys(render.desired.agents).length > 0}
            <Table.Root>
              <Table.Header>
                <Table.Row><Table.Head>{t('detail.agents.col.family')}</Table.Head><Table.Head>{t('detail.agents.col.desiredVersion')}</Table.Head></Table.Row>
              </Table.Header>
              <Table.Body>
                {#each Object.entries(render.desired.agents) as [family, agent] (family)}
                  <Table.Row>
                    <Table.Cell class="text-xs font-medium">{family}</Table.Cell>
                    <Table.Cell class="text-xs">{agent.version ?? '—'}</Table.Cell>
                  </Table.Row>
                {/each}
              </Table.Body>
            </Table.Root>
          {:else}
            <p class="text-xs text-muted-foreground">{t('detail.agents.desiredEmpty')}</p>
          {/if}
        </div>
        <div>
          {#if (machine.status?.adapterCapabilities ?? []).length > 0}
            <Table.Root>
              <Table.Header>
                <Table.Row>
                  <Table.Head>{t('detail.agents.col.family')}</Table.Head><Table.Head>{t('detail.agents.col.capability')}</Table.Head><Table.Head>{t('detail.agents.col.state')}</Table.Head>
                  <Table.Head>{t('detail.agents.col.verifiedVersions')}</Table.Head><Table.Head>{t('detail.agents.col.note')}</Table.Head>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {#each machine.status?.adapterCapabilities ?? [] as cap (cap.family + cap.capability)}
                  <Table.Row>
                    <Table.Cell class="text-xs">{cap.family}</Table.Cell>
                    <Table.Cell class="text-xs">{cap.capability}</Table.Cell>
                    <Table.Cell class="text-xs">{cap.state}</Table.Cell>
                    <Table.Cell class="text-xs">{cap.verifiedVersions ?? '—'}</Table.Cell>
                    <Table.Cell class="text-xs text-muted-foreground">{cap.reason ?? '—'}</Table.Cell>
                  </Table.Row>
                {/each}
              </Table.Body>
            </Table.Root>
            <p class="mt-2 text-xs text-muted-foreground">{t('detail.agents.capabilitiesNote')}</p>
          {:else}
            <p class="text-xs text-muted-foreground">{t('detail.agents.empty')}</p>
          {/if}
        </div>
      </CardContent>
    </Card>

    <!-- 期望与观测 -->
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('detail.section.desiredObserved')}</CardTitle></CardHeader>
      <CardContent class="grid gap-2 text-xs">
        <div>
          {t('detail.field.desiredGeneration')} gen{machine.status?.desiredGeneration ?? '—'} ↔ {t('detail.field.observedGeneration')} gen{machine.status?.observedGeneration ?? '—'}
          {#if machine.status?.desiredGeneration !== machine.status?.observedGeneration}
            <StateBadge tone="warn" label={t('detail.gen.mismatch')} title={t('detail.gen.mismatchTitle')} />
          {:else}
            <StateBadge tone="ok" label={t('detail.gen.match')} />
          {/if}
        </div>
        <div class="grid gap-1 font-mono">
          <div>desiredProjectionDigest ：{shortDigest(machine.status?.desiredProjectionDigest)}</div>
          <div>observedProjectionDigest：{shortDigest(machine.status?.observedProjectionDigest)}</div>
          <div>canonicalizationVersion ：{machine.status?.canonicalizationVersion ?? '—'}</div>
          <div>{t('detail.desiredRenderDigest')}: {render?.digest ? shortDigest(render.digest) : '—'}</div>
        </div>
        <p class="text-muted-foreground">{t('detail.genBasis')}</p>
      </CardContent>
    </Card>

    <!-- 技能 -->
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('detail.section.skills')}</CardTitle></CardHeader>
      <CardContent>
        {#if render?.desired?.skills && Object.keys(render.desired.skills).length > 0}
          <Table.Root>
            <Table.Header>
              <Table.Row>
                <Table.Head>{t('detail.skills.col.name')}</Table.Head><Table.Head>{t('detail.skills.col.revision')}</Table.Head><Table.Head>{t('detail.skills.col.contentDigest')}</Table.Head>
                <Table.Head>{t('detail.skills.col.artifactDigest')}</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#each Object.entries(render.desired.skills) as [skillName, skill] (skillName)}
                <Table.Row>
                  <Table.Cell class="text-xs">{skillName}</Table.Cell>
                  <Table.Cell class="text-xs font-mono">{skill.revision ?? '—'}</Table.Cell>
                  <Table.Cell class="text-xs font-mono">{shortDigest(skill.contentDigest)}</Table.Cell>
                  <Table.Cell class="text-xs font-mono">{shortDigest(skill.artifactDigest)}</Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
        {:else}
          <p class="text-xs text-muted-foreground">{t('detail.skills.empty')}</p>
        {/if}
      </CardContent>
    </Card>

    <!-- 模型与 Provider -->
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('detail.section.providers')}</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        {#if machine.spec?.managementMode === 'ssh'}
          <p class="text-xs text-muted-foreground">{t('detail.providers.sshNote')}</p>
        {/if}
        {#if render?.desired?.agents}
          <Table.Root>
            <Table.Header>
              <Table.Row><Table.Head>{t('detail.providers.col.family')}</Table.Head><Table.Head>{t('detail.providers.col.provider')}</Table.Head><Table.Head>{t('detail.providers.col.config')}</Table.Head></Table.Row>
            </Table.Header>
            <Table.Body>
              {#each Object.entries(render.desired.agents) as [family, agent] (family)}
                {@const spec = store.profiles.find((p) => p.metadata.name === profileRef)?.spec?.agents?.[family]}
                <Table.Row>
                  <Table.Cell class="text-xs">{family}</Table.Cell>
                  <Table.Cell class="text-xs">{spec?.provider ?? '—'}</Table.Cell>
                  <Table.Cell class="max-w-[420px] truncate font-mono text-xs">{JSON.stringify(agent.config ?? {})}</Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
        {:else}
          <p class="text-xs text-muted-foreground">{t('detail.providers.empty')}</p>
        {/if}
        {#if store.providers.length > 0}
          <div class="text-xs text-muted-foreground">
            {t('detail.providers.registered', { names: store.providers.map((p) => p.metadata.name).join(t('app.listSeparator')) })}
          </div>
        {/if}
      </CardContent>
    </Card>

    <!-- MCP -->
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('detail.section.mcp')}</CardTitle></CardHeader>
      <CardContent>
        {#if render?.desired?.mcp && Object.keys(render.desired.mcp).length > 0}
          <Table.Root>
            <Table.Header><Table.Row><Table.Head>{t('detail.mcp.col.name')}</Table.Head><Table.Head>{t('detail.mcp.col.normalized')}</Table.Head></Table.Row></Table.Header>
            <Table.Body>
              {#each Object.entries(render.desired.mcp) as [mcpName, entry] (mcpName)}
                <Table.Row>
                  <Table.Cell class="text-xs">{mcpName}</Table.Cell>
                  <Table.Cell class="font-mono text-xs">{prettyJSON(entry)}</Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
          <p class="mt-2 text-xs text-muted-foreground">{t('detail.mcp.note')}</p>
        {:else}
          <p class="text-xs text-muted-foreground">{t('detail.mcp.empty')}</p>
        {/if}
      </CardContent>
    </Card>

    <!-- 漂移详情 -->
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('detail.section.drift')}</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        {#if drift}
          <div class="flex flex-wrap items-center gap-2">
            <StateBadge tone={drift.tone} label={drift.label} />
            {#if drift.reasonCode}<span class="font-mono text-xs text-muted-foreground">reason={drift.reasonCode}</span>{/if}
            <Button size="sm" variant="outline" disabled={availability.actions[0].blocked || busy !== null} onclick={reconcile}>
              {t('detail.drift.reconcile')}
            </Button>
            <Button size="sm" variant="ghost" disabled title={t('ssh.showDiff.reason')}>
              {t('ssh.showDiff.title')}
            </Button>
          </div>
          <p class="text-xs">{drift.detail}</p>
          <div class="grid gap-1 font-mono text-xs">
            <div>{t('detail.drift.desiredDigest')}: {shortDigest(drift.desiredDigest, 12)}</div>
            <div>{t('detail.drift.observedDigest')}: {shortDigest(drift.observedDigest, 12)}</div>
          </div>
          {#if drift.state === 'drifted'}
            <Alert.Root>
              <Alert.Title>{t('detail.drift.diffTitle')}</Alert.Title>
              <Alert.Description>{t('detail.drift.diffUnavailable')}</Alert.Description>
            </Alert.Root>
          {/if}
        {:else}
          <p class="text-xs text-muted-foreground">{t('detail.drift.unavailable')}</p>
        {/if}
      </CardContent>
    </Card>

    <!-- 操作历史与未决操作 -->
    <Card>
      <CardHeader>
        <CardTitle class="text-sm">{t('detail.section.operations')}</CardTitle>
        {#if availability.blockedBy}
          <Alert.Root class="mt-2">
            <Alert.Title>
              {t('detail.blocked.title', { phase: operationPhaseLabel(availability.blockedBy.phase, i18n.locale) })}
            </Alert.Title>
            <Alert.Description>
              {t('detail.blocked.detail', { id: availability.blockedBy.id })}
            </Alert.Description>
          </Alert.Root>
        {/if}
      </CardHeader>
      <CardContent class="grid gap-4">
        {#if awaiting.length > 0}
          {#each awaiting as item (item.operation.id)}
            <Alert.Root>
              <Alert.Title>
                {t('detail.awaiting.title', { remaining: item.remainingText, id: item.operation.id })}
              </Alert.Title>
              <Alert.Description>
                <div>planDigest：<code class="font-mono text-xs">{shortDigest(item.planDigest, 10)}</code></div>
                <div class="mt-1">{item.baselineNotice}</div>
                <div class="mt-1">{planTimeoutNotice(i18n.locale)}</div>
                <div class="mt-2 flex gap-2">
                  <Button size="sm" disabled={busy !== null} onclick={() => confirmPlan(item.planDigest ?? '')}>{t('ops.action.confirm')}</Button>
                  <Button size="sm" variant="outline" disabled={busy !== null} onclick={() => cancelOp(item.operation.id)}>{t('common.cancel')}</Button>
                </div>
              </Alert.Description>
            </Alert.Root>
          {/each}
        {/if}

        {#if ops.length === 0}
          <p class="text-xs text-muted-foreground">{t('detail.ops.empty')}</p>
        {:else}
          <Table.Root>
            <Table.Header>
              <Table.Row>
                <Table.Head>{t('detail.ops.col.id')}</Table.Head><Table.Head>{t('detail.ops.col.type')}</Table.Head><Table.Head>{t('detail.ops.col.phase')}</Table.Head>
                <Table.Head>{t('detail.ops.col.modifier')}</Table.Head><Table.Head>{t('detail.ops.col.evidence')}</Table.Head><Table.Head>{t('detail.ops.col.time')}</Table.Head>
                <Table.Head>{t('detail.ops.col.disposition')}</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#each ops as op (op.id)}
                <Table.Row>
                  <Table.Cell class="font-mono text-xs">{shortDigest(op.id, 6)}</Table.Cell>
                  <Table.Cell class="text-xs">
                    {op.typeLabel}
                    <div class="text-muted-foreground">
                      {op.source === 'automatic' ? t('detail.ops.automatic') : t('ops.source.manual')}
                      {#if op.transport}· {op.transport}{/if}
                      {#if op.desiredGeneration}· gen{op.desiredGeneration}{/if}
                    </div>
                  </Table.Cell>
                  <Table.Cell>
                    <StateBadge tone={op.tone} label={op.phaseLabel} title={op.phaseNote} />
                  </Table.Cell>
                  <Table.Cell class="text-xs">
                    {#if op.terminalModifier}
                      <StateBadge
                        tone={op.terminalModifier === 'Skipped' ? 'warn' : 'muted'}
                        label={op.modifierLabel ?? op.terminalModifier}
                        title={op.modifierNote ?? ''}
                      />
                    {:else}—{/if}
                  </Table.Cell>
                  <Table.Cell class="text-xs text-muted-foreground">
                    {op.adapterHealth ? `adapterHealth=${op.adapterHealth}` : '—'}
                    {#if op.verifyInventorySeq !== undefined}<div>inventorySeq={op.verifyInventorySeq}</div>{/if}
                  </Table.Cell>
                  <Table.Cell class="text-xs text-muted-foreground">
                    {formatRelativeTime(op.finishedAt ?? op.startedAt ?? op.createdAt ?? '', clock.now, loc)}
                  </Table.Cell>
                  <Table.Cell>
                    {#if op.isUnresolved}
                      <div class="flex flex-col gap-1">
                        <Button size="sm" variant="outline" disabled={busy !== null} onclick={() => cancelOp(op.id)}>{t('common.cancel')}</Button>
                        <Input class="h-7 w-40 text-xs" placeholder={t('detail.skip.placeholder')} bind:value={skipReason} aria-label={t('detail.skip.placeholder')} />
                        <Button size="sm" variant="ghost" disabled={busy !== null || skipReason.trim() === ''} onclick={() => skipOp(op.id)}>
                          {t('detail.skip.button')}
                        </Button>
                      </div>
                    {:else}—{/if}
                  </Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
          <p class="text-xs text-muted-foreground">{t('detail.actionsNote')}</p>
        {/if}

        <Tabs.Root value="blocked">
          <Tabs.List>
            <Tabs.Trigger value="blocked">{t('detail.blockedTab')}</Tabs.Trigger>
          </Tabs.List>
          <Tabs.Content value="blocked">
            <Table.Root>
              <Table.Header><Table.Row><Table.Head>{t('detail.blocked.col.action')}</Table.Head><Table.Head>{t('detail.blocked.col.status')}</Table.Head><Table.Head>{t('detail.blocked.col.note')}</Table.Head></Table.Row></Table.Header>
              <Table.Body>
                {#each availability.actions as action (action.action)}
                  <Table.Row>
                    <Table.Cell class="text-xs">{action.label}</Table.Cell>
                    <Table.Cell>
                      <StateBadge
                        tone={action.blocked ? 'warn' : 'ok'}
                        label={action.blocked ? t('common.blocked') : t('common.available')}
                      />
                    </Table.Cell>
                    <Table.Cell class="text-xs text-muted-foreground">{action.exception ?? '—'}</Table.Cell>
                  </Table.Row>
                {/each}
              </Table.Body>
            </Table.Root>
          </Tabs.Content>
        </Tabs.Root>
      </CardContent>
    </Card>

    <!-- 回滚与其他动作 -->
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('detail.section.actions')}</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        <div class="flex flex-wrap items-end gap-3">
          <div class="grid gap-1">
            <Label for="rollback-gen" class="text-xs">{t('detail.rollback.label')}</Label>
            <Input id="rollback-gen" class="w-32" bind:value={rollbackGeneration} placeholder="12" />
          </div>
          <Button size="sm" variant="outline" disabled={busy !== null || availability.actions[1].blocked} onclick={rollback}>
            {t('detail.rollback.button')}
          </Button>
          <Button size="sm" variant="outline" disabled title={t('ssh.featureUnavailable')}>
            {t('detail.bootstrap')}
          </Button>
          <Button size="sm" variant="outline" disabled title={t('ssh.featureUnavailable')}>
            {t('detail.repairAgentd')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={busy !== null || machine.spec?.managementMode !== 'ssh'}
            onclick={requestInventory}
            title={machine.spec?.managementMode === 'ssh' ? t('machines.inventoryTitle') : t('machines.inventoryDisabled')}
          >
            {busy === 'inventory' ? t('machines.collecting') : t('machines.action.inventory')}
          </Button>
        </div>
        <p class="text-xs text-muted-foreground">{t('detail.rollbackNote')}</p>
      </CardContent>
    </Card>
  {/if}
</div>
