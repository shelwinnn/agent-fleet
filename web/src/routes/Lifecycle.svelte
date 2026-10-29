<script lang="ts">
  /**
   * Codex 生命周期维护页（KM-50；spec：docs/superpowers/specs/2026-10-27-codex-lifecycle-spec.md）。
   *
   * 一个页面完成闭环：选主机 → 选环境/用户 → 选实例（或看到"尚未安装"）→
   * 看已装版本与上游更新 → 预览确认安装/升级/卸载 → 逐目标看结果与下一步。
   *
   * 红线：变更只经预览确认卡发生；"停止管理"与"卸载"严格区分；未验证/阻塞
   * 组合如实展示，阻塞组合不提供操作入口；上游查询失败显示未知，不显示"最新"。
   */
  import { onMount } from 'svelte';
  import type { LifecycleOperationType, LifecycleTarget, Machine, ResourceEvent } from '$lib/api/types.js';
  import type { LifecycleGateway, AgentInstanceInventory } from '$lib/state/lifecycleGateway.js';
  import { InventoryNotWiredError } from '$lib/state/lifecycleGateway.js';
  import type { UpstreamRowView } from '$lib/state/lifecycle.js';
  import {
    CODEX_AGENT,
    buildTarget,
    capabilityFor,
    desiredAbsentUpdate,
    desiredPresentUpdate,
    desiredUnmanagedUpdate,
    envLabel,
    impactFingerprint,
    instanceKey,
    instanceTitle,
    instancesFor,
    isExactVersion,
    isOperationSettled,
    methodOfferings,
    newIdempotencyKey,
    upstreamRowViews,
  } from '$lib/state/lifecycle.js';
  import { machineRow } from '$lib/state/machines.js';
  import { failureText } from '$lib/state/ssh.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '$lib/components/ui/card/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import CapabilityBadge from '$lib/components/lifecycle/CapabilityBadge.svelte';
  import StatusCard from '$lib/components/lifecycle/StatusCard.svelte';
  import UpstreamCard from '$lib/components/lifecycle/UpstreamCard.svelte';
  import PreviewConfirm from '$lib/components/lifecycle/PreviewConfirm.svelte';
  import ResultsPanel from '$lib/components/lifecycle/ResultsPanel.svelte';
  import { formatRelativeTime } from '$lib/state/format.js';
  import { t, i18n } from '$lib/i18n/index.js';

  interface Props {
    machines: Machine[];
    gateway: LifecycleGateway;
    /** 生命周期 SSE 事件源（App 传入 store.onLifecycleEvent；测试可不传）。 */
    onLifecycleEvent?: (cb: (ev: ResourceEvent) => void) => () => void;
  }
  let { machines, gateway, onLifecycleEvent }: Props = $props();

  const clock = createClock(15_000);
  const loc = $derived(i18n.locale);

  // ---- 选择 ----
  const sel = $state({ machine: '', env: '', user: '', instance: '' });

  // ---- 数据 ----
  let capabilities = $state<Awaited<ReturnType<LifecycleGateway['getCapabilities']>>>([]);
  let capabilitiesError = $state<unknown>(undefined);
  let inventory = $state<AgentInstanceInventory | undefined>(undefined);
  let inventoryLoading = $state(false);
  let inventoryError = $state<unknown>(undefined);
  let inventoryNotWired = $state(false);
  let desired = $state<Awaited<ReturnType<LifecycleGateway['getDesired']>> | undefined>(undefined);
  let desiredLoading = $state(false);
  let desiredError = $state<unknown>(undefined);

  // ---- 上游检查 ----
  let upstreamState = $state<'idle' | 'loading' | 'ok' | 'error'>('idle');
  let upstreamRows = $state<UpstreamRowView[]>([]);
  let upstreamCheckedAt = $state<string | undefined>(undefined);
  let upstreamError = $state<unknown>(undefined);

  // ---- 操作表单与预览 ----
  let installMethod = $state('');
  let versionInput = $state('');
  let versionTouched = $state(false);
  let previewKind = $state<'install' | 'upgrade' | 'uninstall' | null>(null);
  let stopOpen = $state(false);
  let submitting = $state(false);

  // ---- 结果 ----
  type TrackedOp = Awaited<ReturnType<LifecycleGateway['getOperation']>>;
  let tracked = $state<TrackedOp[]>([]);
  let retryingOpId = $state<string | null>(null);
  let lookupError = $state<string | null>(null);
  let resultsMsg = $state<string | null>(null);

  // ---- 动作反馈 ----
  type Feedback = { kind: 'ok' | 'error'; text: string };
  let feedback = $state<Feedback | null>(null);

  // ---- 派生 ----
  const machine = $derived(machines.find((m) => m.metadata.name === sel.machine));
  const machineView = $derived(machine ? machineRow(machine, clock.now, loc) : undefined);
  const machineOffline = $derived(machineView ? machineView.agentd.connected !== 'True' : false);
  const os = $derived(inventory?.os ?? machine?.status?.os);
  const arch = $derived(inventory?.arch ?? machine?.status?.arch);
  const envOptions = $derived(inventory?.environments ?? []);
  const userOptions = $derived(envOptions.find((e) => (e.env || '') === sel.env)?.users ?? []);
  const instances = $derived(instancesFor(inventory, sel.env, sel.user, CODEX_AGENT));
  const selectedInstance = $derived(instances.find((i) => instanceKey(i) === sel.instance) ?? null);
  const offerings = $derived(methodOfferings(capabilities, os, arch));
  const installedView = $derived({
    present: instances.length > 0,
    version: (selectedInstance ?? instances[0])?.version,
    versionError: (selectedInstance ?? instances[0])?.versionError,
  });

  // 操作能力判定：安装看所选方式；升级/卸载看所选实例的方式。
  const activeMethod = $derived(
    previewKind === 'install' ? installMethod : (selectedInstance?.method ?? installMethod),
  );
  const activeCapability = $derived(capabilityFor(capabilities, os, arch, activeMethod));
  const activeBlocked = $derived(activeCapability?.state === 'blocked');
  const versionInvalid = $derived(versionInput.trim() !== '' && !isExactVersion(versionInput));
  const upstreamFillVersion = $derived(
    upstreamRows.find((r) => r.kind === 'ok' && r.method === (selectedInstance?.method ?? installMethod)) as
      | Extract<UpstreamRowView, { kind: 'ok' }>
      | undefined,
  );

  // ---- 加载 ----
  async function loadInventory(name: string) {
    inventoryLoading = true;
    inventoryError = undefined;
    inventoryNotWired = false;
    try {
      inventory = await gateway.getInstanceInventory(name);
    } catch (err) {
      inventory = undefined;
      if (err instanceof InventoryNotWiredError) inventoryNotWired = true;
      else inventoryError = err;
    } finally {
      inventoryLoading = false;
    }
  }

  async function loadDesired(name: string) {
    desiredLoading = true;
    desiredError = undefined;
    try {
      desired = await gateway.getDesired(name);
    } catch (err) {
      desired = undefined;
      desiredError = err;
    } finally {
      desiredLoading = false;
    }
  }

  async function loadCapabilities() {
    capabilitiesError = undefined;
    try {
      capabilities = await gateway.getCapabilities();
    } catch (err) {
      capabilitiesError = err;
    }
  }

  onMount(() => {
    void loadCapabilities();
    if (onLifecycleEvent) {
      return onLifecycleEvent((ev) => {
        if (ev.resource === 'lifecycle-operations' && tracked.some((o) => o.metadata.name === ev.id)) {
          void refreshOperation(ev.id);
        } else if (ev.resource === 'lifecycle-desired' && ev.id === sel.machine) {
          void loadDesired(sel.machine);
        }
      });
    }
  });

  function pickMachine(name: string) {
    sel.machine = name;
    sel.env = '';
    sel.user = '';
    sel.instance = '';
    previewKind = null;
    stopOpen = false;
    inventory = undefined;
    desired = undefined;
    installMethod = '';
    versionInput = '';
    versionTouched = false;
    upstreamState = 'idle';
    upstreamRows = [];
    upstreamCheckedAt = undefined;
    if (name) {
      void loadInventory(name);
      void loadDesired(name);
    }
  }
  // 实例清单变化时归一选择：单实例自动选中；多实例保持显式选择；失效选择清空。
  $effect(() => {
    const keys = instances.map(instanceKey);
    if (keys.length === 1 && sel.instance !== keys[0]) sel.instance = keys[0];
    else if (keys.length === 0 && sel.instance !== '') sel.instance = '';
    else if (keys.length > 1 && sel.instance !== '' && !keys.includes(sel.instance)) sel.instance = '';
  });

  function pickEnv(env: string) {
    sel.env = env;
    sel.user = '';
    sel.instance = '';
    previewKind = null;
    installMethod = '';
  }
  function pickUser(user: string) {
    sel.user = user;
    sel.instance = '';
    previewKind = null;
  }

  // ---- 上游检查 ----
  async function checkUpstream() {
    upstreamState = 'loading';
    upstreamError = undefined;
    try {
      const report = await gateway.checkUpstream(CODEX_AGENT);
      upstreamRows = report.sources.map((s) => upstreamRowViews(s, i18n.locale));
      upstreamCheckedAt = report.checkedAt;
      upstreamState = 'ok';
    } catch (err) {
      upstreamState = 'error';
      upstreamError = err;
    }
  }

  // ---- 预览与执行 ----
  const previewTargets = $derived.by(() => {
    if (!sel.machine || !sel.user) return [];
    if (previewKind === 'install') {
      return [
        buildTarget({
          machine: sel.machine,
          env: sel.env,
          user: sel.user,
          method: installMethod,
          os,
          arch,
          capability: capabilityFor(capabilities, os, arch, installMethod),
        }),
      ];
    }
    if (previewKind && selectedInstance) {
      return [
        buildTarget({
          machine: sel.machine,
          env: sel.env,
          user: sel.user,
          method: selectedInstance.method,
          pkg: selectedInstance.package,
          os,
          arch,
          capability: capabilityFor(capabilities, os, arch, selectedInstance.method),
        }),
      ];
    }
    return [];
  });
  const previewVersion = $derived(previewKind === 'install' || previewKind === 'upgrade' ? versionInput.trim() : '');
  const previewFingerprint = $derived(
    previewKind
      ? impactFingerprint(
          previewKind === 'install' ? 'Install' : previewKind === 'upgrade' ? 'Upgrade' : 'Uninstall',
          previewVersion || undefined,
          previewTargets,
        )
      : '',
  );

  function openPreview(kind: 'install' | 'upgrade' | 'uninstall') {
    previewKind = kind;
    versionTouched = true;
    feedback = null;
  }

  async function executePreview(includeDesired: boolean) {
    if (!previewKind || previewTargets.length === 0 || !machine) return;
    const type: LifecycleOperationType =
      previewKind === 'install' ? 'Install' : previewKind === 'upgrade' ? 'Upgrade' : 'Uninstall';
    const req = {
      type,
      idempotencyKey: newIdempotencyKey(),
      ...(previewVersion ? { version: previewVersion } : {}),
      targets: previewTargets,
    };
    submitting = true;
    let op: TrackedOp | null = null;
    let reused = false;
    let opError: unknown = null;
    try {
      const created = await gateway.createOperation(req);
      op = created.operation;
      reused = created.reused;
      addTracked(op);
    } catch (err) {
      opError = err;
    }
    // 期望状态与操作是两次显式写入（预览里分开列明）；各自的成败如实上报。
    let desiredOk = true;
    let desiredUpdateError: unknown = null;
    if (includeDesired) {
      try {
        const update =
          type === 'Uninstall'
            ? desiredAbsentUpdate(req.targets[0].method, req.targets[0].package)
            : desiredPresentUpdate(previewVersion, req.targets[0].method, req.targets[0].package);
        desired = await gateway.putDesired(machine.metadata.name, update);
      } catch (err) {
        desiredOk = false;
        desiredUpdateError = err;
      }
    }
    submitting = false;
    if (op) {
      if (!desiredOk) {
        feedback = {
          kind: 'error',
          text: t('lc.preview.partialDesiredFailed', {
            id: op.metadata.name,
            reason: failureText(desiredUpdateError, loc),
          }),
        };
      } else {
        feedback = {
          kind: 'ok',
          text: reused ? t('lc.preview.reused', { id: op.metadata.name }) : t('lc.preview.created', { id: op.metadata.name }),
        };
      }
      previewKind = null;
      versionInput = '';
      versionTouched = false;
    } else {
      feedback = {
        kind: 'error',
        text: `${t('lc.preview.failed')}${failureText(opError, loc)}${
          desiredOk && includeDesired ? ` ${t('lc.preview.desiredOkButOpFailed')}` : ''
        }`,
      };
      // 期望状态可能已更新而操作未创建：重取意图，偏差视图如实反映。
      void loadDesired(sel.machine);
    }
  }

  // ---- 停止管理（保留程序；与卸载严格区分） ----
  async function executeStopManaging() {
    if (!machine) return;
    submitting = true;
    try {
      desired = await gateway.putDesired(machine.metadata.name, desiredUnmanagedUpdate());
      feedback = { kind: 'ok', text: t('lc.stop.done') };
      stopOpen = false;
    } catch (err) {
      feedback = { kind: 'error', text: `${t('lc.stop.failed')}${failureText(err, loc)}` };
    } finally {
      submitting = false;
    }
  }

  // ---- 结果：追踪 / 轮询 / 重试 / 按 ID 查询 ----
  function addTracked(op: TrackedOp) {
    tracked = [op, ...tracked.filter((o) => o.metadata.name !== op.metadata.name)].slice(0, 20);
  }

  async function refreshOperation(opId: string) {
    try {
      addTracked(await gateway.getOperation(opId));
    } catch {
      // 轮询失败不打断页面：下次事件或轮询会再试。
    }
  }

  $effect(() => {
    const active = tracked.filter((o) => !isOperationSettled(o)).map((o) => o.metadata.name);
    if (active.length === 0) return;
    const timer = setInterval(() => {
      for (const id of active) void refreshOperation(id);
    }, 3000);
    return () => clearInterval(timer);
  });

  async function retryFailed(op: TrackedOp, targets: LifecycleTarget[]) {
    retryingOpId = op.metadata.name;
    resultsMsg = null;
    try {
      const retry = await gateway.retryOperation(op.metadata.name, { targets });
      addTracked(retry);
      resultsMsg = t('lc.results.retryCreated', { id: retry.metadata.name, origin: op.metadata.name });
    } catch (err) {
      resultsMsg = `${t('lc.results.retryFailed')}${failureText(err, loc)}`;
    } finally {
      retryingOpId = null;
    }
  }

  async function lookupOperation(id: string) {
    lookupError = null;
    resultsMsg = null;
    try {
      addTracked(await gateway.getOperation(id));
    } catch (err) {
      lookupError = failureText(err, loc);
    }
  }
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">{t('lc.title')}</h1>
    <p class="text-xs text-muted-foreground">{t('lc.subtitle')}</p>
  </div>

  {#if feedback}
    <Alert.Root variant={feedback.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{feedback.text}</Alert.Title>
    </Alert.Root>
  {/if}

  <!-- ① 目标选择 -->
  <Card>
    <CardHeader>
      <CardTitle class="text-sm">{t('lc.step.target')}</CardTitle>
    </CardHeader>
    <CardContent class="grid gap-3 text-sm">
      <div class="grid gap-1">
        <Label for="lc-machine" class="text-xs">{t('lc.machine.label')}</Label>
        <select
          id="lc-machine"
          class="h-9 w-72 rounded-md border bg-background px-3 text-sm"
          value={sel.machine}
          onchange={(e) => pickMachine(e.currentTarget.value)}
        >
          <option value="">—</option>
          {#each machines as m (m.metadata.name)}
            <option value={m.metadata.name}>{m.metadata.name}</option>
          {/each}
        </select>
      </div>
      {#if machines.length === 0}
        <div class="rounded-md border border-dashed p-4 text-muted-foreground" role="status">{t('lc.machine.empty')}</div>
      {/if}

      {#if machine}
        <div class="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span class="font-mono">{os ?? '—'} / {arch ?? '—'}</span>
          <Badge variant="outline" class={machineView?.agentd.connected === 'True' ? 'tone-ok' : 'tone-warn'}>
            {machineView?.agentd.label}
          </Badge>
          {#if machineOffline}
            <span>{t('lc.machine.offlineHint')}</span>
          {/if}
        </div>
      {/if}

      {#if sel.machine}
        {#if inventoryNotWired}
          <div class="rounded-md border border-dashed p-4" role="status">
            <p class="text-sm font-medium">{t('lc.env.inventoryUnavailable')}</p>
            <p class="mt-1 text-xs text-muted-foreground">{t('lc.env.inventoryUnavailableDetail')}</p>
          </div>
        {:else if inventoryError !== undefined}
          <div class="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive" role="alert">
            {t('lc.env.loadFailed')}{failureText(inventoryError, loc)} {t('error.loadRetryHint')}
          </div>
        {:else if inventoryLoading}
          <AsyncState loading rows={2} />
        {:else if inventory}
          <div class="grid gap-3 sm:grid-cols-2">
            <div class="grid gap-1">
              <Label for="lc-env" class="text-xs">{t('lc.env.label')}</Label>
              <!-- 原生环境的契约值是空串，占位用哨兵值避免两个 value="" 选项混淆 -->
              <select
                id="lc-env"
                class="h-9 rounded-md border bg-background px-3 text-sm"
                value={sel.env === '' ? '__pick__' : sel.env}
                onchange={(e) => pickEnv(e.currentTarget.value === '__pick__' ? '' : e.currentTarget.value)}
              >
                <option value="__pick__">—</option>
                {#each envOptions as e (e.env || 'native')}
                  <option value={e.env || ''}>{envLabel(e.env, loc)}</option>
                {/each}
              </select>
            </div>
            <div class="grid gap-1">
              <Label for="lc-user" class="text-xs">{t('lc.env.user')}</Label>
              <select
                id="lc-user"
                class="h-9 rounded-md border bg-background px-3 text-sm"
                value={sel.user}
                onchange={(e) => pickUser(e.currentTarget.value)}
                disabled={sel.env === ''}
              >
                <option value="">—</option>
                {#each userOptions as u (u)}
                  <option value={u}>{u}</option>
                {/each}
              </select>
            </div>
          </div>

          {#if inventory.stale}
            <Alert.Root>
              <Alert.Title>{t('lc.env.stale')}</Alert.Title>
              <Alert.Description>{t('lc.env.staleHint')}</Alert.Description>
            </Alert.Root>
          {/if}
          {#if inventory.discoveredAt}
            <p class="text-xs text-muted-foreground">
              {t('lc.instance.observedAt', { time: formatRelativeTime(inventory.discoveredAt, clock.now, loc) })}
            </p>
          {/if}

          {#if sel.user}
            <!-- 实例选择：多实例必须显式选择；无实例 = 显式"尚未安装" -->
            <div class="grid gap-2">
              <div class="text-xs font-medium text-muted-foreground">{t('lc.instance.label')}</div>
              {#if instances.length === 0}
                <div class="flex flex-wrap items-center gap-2 rounded-md border border-dashed p-3" role="status">
                  <StateBadge tone="muted" label={t('lc.instance.none')} />
                  <span class="text-xs text-muted-foreground">{t('lc.instance.noneHint')}</span>
                </div>
              {:else}
                {#if instances.length > 1}
                  <p class="text-xs text-muted-foreground">{t('lc.instance.choose')}</p>
                {/if}
                {#each instances as inst (instanceKey(inst))}
                  {@const key = instanceKey(inst)}
                  <div class="flex flex-wrap items-center gap-2 rounded-md border p-3 {sel.instance === key ? 'border-primary' : ''}">
                    <input
                      type="radio"
                      name="lc-instance"
                      id="lc-inst-{key}"
                      class="size-4 accent-primary"
                      checked={sel.instance === key}
                      onchange={() => {
                        sel.instance = key;
                        previewKind = null;
                      }}
                    />
                    <label for="lc-inst-{key}" class="flex cursor-pointer flex-wrap items-center gap-2">
                      <span class="font-mono text-xs">{instanceTitle(inst, loc)}</span>
                      <span class="text-xs text-muted-foreground">{inst.executablePath ?? ''}</span>
                      <CapabilityBadge capability={capabilityFor(capabilities, os, arch, inst.method)} />
                    </label>
                  </div>
                {/each}
                {#if selectedInstance?.versionError}
                  <p class="text-xs text-amber-700 dark:text-amber-300">
                    {t('lc.instance.versionUnresolvedHint')}
                  </p>
                {/if}
              {/if}
            </div>
          {/if}
        {/if}
      {/if}
    </CardContent>
  </Card>

  {#if machine && (inventory || desired !== undefined)}
    <!-- ② 期望状态与偏差（与上游更新分开展示） -->
    <StatusCard
      doc={desired}
      loading={desiredLoading}
      error={desiredError}
      installed={installedView}
      observedAt={inventory?.discoveredAt}
    />
  {/if}

  <!-- ③ 上游更新 -->
  <UpstreamCard
    state={upstreamState}
    rows={upstreamRows}
    checkedAt={upstreamCheckedAt}
    error={upstreamError}
    disabled={!sel.machine}
    oncheck={checkUpstream}
  />

  {#if machine && sel.user}
    <!-- ④ 发起操作 -->
    <Card>
      <CardHeader>
        <CardTitle class="text-sm">{t('lc.step.act')}</CardTitle>
      </CardHeader>
      <CardContent class="grid gap-3 text-sm">
        {#if capabilitiesError !== undefined}
          <div class="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-destructive" role="alert">
            {t('lc.cap.unknown')}{failureText(capabilitiesError, loc)} {t('error.loadRetryHint')}
          </div>
        {/if}

        {#if instances.length === 0}
          <!-- 安装：选方式 + 精确版本；阻塞组合只展示不提供入口 -->
          <div class="grid gap-2">
            <div class="text-xs font-medium text-muted-foreground">{t('lc.action.pickMethod')}</div>
            {#each offerings as of (of.method)}
              <div class="flex flex-wrap items-center gap-2">
                <Button
                  size="sm"
                  variant={installMethod === of.method ? 'default' : 'outline'}
                  aria-pressed={installMethod === of.method}
                  disabled={!of.offered}
                  title={of.offered ? undefined : t('lc.cap.blockedHint')}
                  onclick={() => {
                    installMethod = of.method;
                    previewKind = null;
                  }}
                >
                  {of.method}
                </Button>
                <CapabilityBadge capability={of.capability} />
                {#if !of.offered}
                  <span class="text-xs text-muted-foreground">{of.capability?.reason}</span>
                {/if}
              </div>
            {/each}
            <div class="grid gap-1">
              <Label for="lc-version" class="text-xs">{t('lc.action.version.label')}</Label>
              <div class="flex flex-wrap items-center gap-2">
                <Input
                  id="lc-version"
                  class="w-56"
                  bind:value={versionInput}
                  onchange={() => (versionTouched = true)}
                  aria-invalid={versionInvalid}
                />
                {#if upstreamFillVersion}
                  <Button size="sm" variant="ghost" onclick={() => (versionInput = upstreamFillVersion.version)}>
                    {t('lc.action.version.fromUpstream')}
                  </Button>
                {/if}
              </div>
              {#if versionInvalid}
                <p class="text-xs text-destructive" role="alert">{t('lc.action.version.invalid')}</p>
              {/if}
            </div>
            {#if installMethod}
              <div class="flex flex-wrap items-center gap-2">
                <Button
                  size="sm"
                  disabled={!isExactVersion(versionInput)}
                  onclick={() => openPreview('install')}
                >
                  {t('lc.action.preview')}
                </Button>
                <CapabilityBadge capability={capabilityFor(capabilities, os, arch, installMethod)} />
                {#if capabilityFor(capabilities, os, arch, installMethod)?.state === 'unverified'}
                  <span class="text-xs text-muted-foreground">{t('lc.cap.unverifiedHint')}</span>
                {/if}
              </div>
            {/if}
          </div>
        {:else if selectedInstance}
          <!-- 升级 / 卸载入口（实例级；来源已知） -->
          <div class="grid gap-2">
            <div class="text-xs font-medium text-muted-foreground">{t('lc.action.pickInstance')}</div>
            <p class="font-mono text-xs">{instanceTitle(selectedInstance, loc)} · {selectedInstance.executablePath ?? ''}</p>
            {#if activeBlocked}
              <div class="rounded-md border border-dashed p-3" role="status">
                <p class="text-sm font-medium">{t('lc.action.blocked')}</p>
                <p class="mt-1 text-xs text-muted-foreground">{activeCapability?.reason}</p>
              </div>
            {:else if !selectedInstance.method}
              <!-- 来源未知：拒绝卸载/升级并说明原因（spec User Story 22） -->
              <div class="rounded-md border border-dashed p-3" role="status">
                <p class="text-sm font-medium">{t('lc.action.uninstallSourceUnknown')}</p>
              </div>
            {:else}
              <div class="grid gap-1">
                <Label for="lc-version-up" class="text-xs">{t('lc.action.version.label')}</Label>
                <div class="flex flex-wrap items-center gap-2">
                  <Input
                    id="lc-version-up"
                    class="w-56"
                    bind:value={versionInput}
                    onchange={() => (versionTouched = true)}
                    aria-invalid={versionInvalid}
                  />
                  {#if upstreamFillVersion}
                    <Button size="sm" variant="ghost" onclick={() => (versionInput = upstreamFillVersion.version)}>
                      {t('lc.action.version.fromUpstream')}
                    </Button>
                  {/if}
                </div>
                {#if versionInvalid}
                  <p class="text-xs text-destructive" role="alert">{t('lc.action.version.invalid')}</p>
                {/if}
              </div>
              <div class="flex flex-wrap items-center gap-2">
                <Button size="sm" disabled={!isExactVersion(versionInput)} onclick={() => openPreview('upgrade')}>
                  {t('lc.action.upgrade')}
                </Button>
                <Button size="sm" variant="outline" onclick={() => openPreview('uninstall')}>
                  {t('lc.action.uninstall')}
                </Button>
                <CapabilityBadge capability={activeCapability} />
                {#if activeCapability?.state === 'unverified'}
                  <span class="text-xs text-muted-foreground">{t('lc.cap.unverifiedHint')}</span>
                {/if}
              </div>
            {/if}
          </div>
        {:else if instances.length > 0}
          <p class="text-xs text-muted-foreground">{t('lc.instance.choose')}</p>
        {/if}

      </CardContent>
    </Card>
  {/if}

  {#if machine}
    <!-- 停止管理：机器级入口，与卸载严格区分（保留程序，仅解除纳管） -->
    <Card>
      <CardContent class="pt-6 text-sm">
        {#if stopOpen}
          <div class="grid gap-2">
            <p class="font-medium">{t('lc.stop.title')}</p>
            <p class="text-xs text-muted-foreground">{t('lc.stop.detail')}</p>
            <div class="flex gap-2">
              <Button size="sm" variant="secondary" disabled={submitting} onclick={executeStopManaging}>
                {submitting ? t('lc.stop.submitting') : t('lc.stop.confirm')}
              </Button>
              <Button size="sm" variant="ghost" onclick={() => (stopOpen = false)} disabled={submitting}>
                {t('common.cancel')}
              </Button>
            </div>
          </div>
        {:else}
          <Button size="sm" variant="ghost" onclick={() => (stopOpen = true)}>{t('lc.action.stopManaging')}</Button>
        {/if}
      </CardContent>
    </Card>
  {/if}

  {#if previewKind}
    <PreviewConfirm
      kind={previewKind}
      targets={previewTargets}
      version={previewVersion}
      method={activeMethod}
      pkg={previewTargets[0]?.package ?? ''}
      capability={activeCapability}
      {machineOffline}
      desiredIntent={previewKind === 'uninstall' ? 'absent' : 'present'}
      fingerprint={previewFingerprint}
      {submitting}
      onconfirm={executePreview}
      oncancel={() => (previewKind = null)}
    />
  {/if}

  <!-- ⑤ 操作结果 -->
  <ResultsPanel
    operations={tracked}
    {retryingOpId}
    {lookupError}
    message={resultsMsg}
    onretry={retryFailed}
    onlookup={lookupOperation}
  />
</div>
