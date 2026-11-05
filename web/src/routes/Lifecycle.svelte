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
  import { ApiError } from '$lib/api/client';
  import type {
    LifecycleDesiredDocument,
    LifecycleOperation,
    LifecycleOperationType,
    LifecycleTarget,
    LifecycleTargetState,
    Machine,
    ResourceEvent,
  } from '$lib/api/types.js';
  import type { LifecycleGateway, AgentInstance, AgentInstanceInventory } from '$lib/state/lifecycleGateway.js';
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
    failedResults,
    impactFingerprint,
    instanceKey,
    instanceTitle,
    instancesFor,
    isExactVersion,
    isOperationSettled,
    capabilityGateView,
    lifecycleStateLabel,
    lifecycleStateTone,
    methodOfferings,
    newIdempotencyKey,
    operationProgress,
    opTypeOf,
    upstreamRowView,
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
  /**
   * 幂等键保留窗口（T7 P6）：同一已批准意图（指纹不变）在结果明确前复用同一
   * 幂等键——创建请求响应丢失后重试，服务端按键复用同一操作，不产生重复变更。
   * 结果明确（操作已创建 / 服务端明确拒绝）后关闭窗口；意图（指纹）变化即换键。
   */
  let submitWindow = $state<{ key: string; fp: string } | null>(null);
  function idempotencyKeyFor(fp: string): string {
    if (submitWindow === null || submitWindow.fp !== fp) {
      submitWindow = { key: newIdempotencyKey(), fp };
    }
    return submitWindow.key;
  }

  // ---- 结果 ----
  type TrackedOp = Awaited<ReturnType<LifecycleGateway['getOperation']>>;
  let tracked = $state<TrackedOp[]>([]);
  let retryingOpId = $state<string | null>(null);
  let confirmingOpId = $state<string | null>(null);
  let lookupError = $state<string | null>(null);
  let resultsMsg = $state<string | null>(null);

  let hostInventory = $state<Record<string, AgentInstanceInventory | null>>({});
  let desiredDocs = $state<Record<string, LifecycleDesiredDocument | null>>({});
  let upgradeOpen = $state(false);
  let upgradeStep = $state<'select' | 'preview'>('select');
  let upgradeSelected = $state<string[]>([]);
  let uninstallOpen = $state(false);

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
  const capFor = $derived((method: string) => capabilityFor(capabilities, os, arch, method));
  const activeMethod = $derived(
    previewKind === 'install' ? installMethod : (selectedInstance?.method ?? installMethod),
  );
  const activeCapability = $derived(capFor(activeMethod));
  // 操作入口仅对有证据的 supported 组合开放（T7 P7）：unverified 的执行会被节点准入拒绝。
  const activeSupported = $derived(activeCapability?.state === 'supported');
  const gate = $derived(capabilityGateView(activeCapability, loc));
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

  $effect(() => {
    const names = machines.map((item) => item.metadata.name);
    let stop = false;
    for (const name of names) {
      void gateway.getInstanceInventory(name).then(
        (inv) => {
          if (!stop) hostInventory[name] = inv;
        },
        () => {
          if (!stop) hostInventory[name] = null;
        },
      );
      void gateway.getDesired(name).then(
        (doc) => {
          if (!stop) desiredDocs[name] = doc;
        },
        () => {
          if (!stop) desiredDocs[name] = null;
        },
      );
    }
    return () => {
      stop = true;
    };
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
      upstreamRows = report.sources.map((src) => upstreamRowView(src, i18n.locale));
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
    previewKind ? impactFingerprint(opTypeOf(previewKind), previewVersion || undefined, previewTargets) : '',
  );

  function openPreview(kind: 'install' | 'upgrade' | 'uninstall') {
    if (!activeSupported) return; // 防线：无证据组合不进入预览（入口本已隐藏）
    previewKind = kind;
    versionTouched = true;
    feedback = null;
  }

  async function executePreview(includeDesired: boolean) {
    if (!previewKind || previewTargets.length === 0 || !machine) return;
    const type: LifecycleOperationType = opTypeOf(previewKind);
    const req = {
      type,
      idempotencyKey: idempotencyKeyFor(previewFingerprint),
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
    // 结果明确（操作已创建，或服务端给出明确裁决）：窗口关闭，下一次意图换新键；
    // 其余失败（网络错误等响应不明）：保留幂等键，重试同一意图时服务端按键去重。
    if (op || opError instanceof ApiError) submitWindow = null;
    // 确认是预览确认动作的后端记录（未确认不派发）：失败时操作仍在但停在
    // 未确认，结果面板给出"确认派发"恢复入口；期望状态不写（前提未成立）。
    let confirmError: unknown = null;
    if (op) {
      try {
        op = await gateway.confirmOperation(op.metadata.name);
        addTracked(op);
      } catch (err) {
        confirmError = err;
      }
    }
    // 期望状态只在操作创建并确认成功后写入（预览里分开列明，成败如实上报）。
    let desiredUpdateError: unknown = null;
    if (op && confirmError === null && includeDesired) {
      try {
        const update =
          type === 'Uninstall'
            ? desiredAbsentUpdate(req.targets[0].method, req.targets[0].package)
            : desiredPresentUpdate(previewVersion, req.targets[0].method, req.targets[0].package);
        desired = await gateway.putDesired(machine.metadata.name, update);
      } catch (err) {
        desiredUpdateError = err;
      }
    }
    submitting = false;
    if (!op) {
      feedback = { kind: 'error', text: `${t('lc.preview.failed')}${failureText(opError, loc)}` };
    } else if (confirmError !== null) {
      feedback = {
        kind: 'error',
        text: `${t('lc.results.confirmFailed')}${failureText(confirmError, loc)} ${t('lc.results.unconfirmedDetail')}`,
      };
    } else if (desiredUpdateError !== null) {
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
    if (op && confirmError === null) {
      previewKind = null;
      versionInput = '';
      versionTouched = false;
    }
  }

  /** 结果面板里的确认/重新确认（未确认不派发；needsReconfirmation 经重新确认清除）。 */
  async function confirmTracked(op: TrackedOp) {
    confirmingOpId = op.metadata.name;
    try {
      const confirmed = await gateway.confirmOperation(op.metadata.name);
      addTracked(confirmed);
      feedback = {
        kind: 'ok',
        text: op.status.needsReconfirmation
          ? t('lc.results.reconfirmDone')
          : t('lc.results.confirmDone', { id: op.metadata.name }),
      };
    } catch (err) {
      feedback = { kind: 'error', text: `${t('lc.results.confirmFailed')}${failureText(err, loc)}` };
    } finally {
      confirmingOpId = null;
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

  function codexInstances(name: string): AgentInstance[] {
    const inv = hostInventory[name];
    if (!inv) return [];
    return inv.instances.filter((instance) => instance.agent === CODEX_AGENT);
  }

  function hostResultState(name: string): LifecycleTargetState {
    const inv = hostInventory[name];
    if (!inv) return 'unknown';
    if (codexInstances(name).some((instance) => instance.versionError || !instance.version)) return 'unknown';
    return 'not_executed';
  }

  function hostVersion(name: string): string {
    return codexInstances(name)
      .map((instance) => instance.version)
      .filter((version): version is string => Boolean(version))
      .join(', ');
  }

  const pinnedVersion = $derived.by(() => {
    const ok = upstreamRows.find((row) => row.kind === 'ok');
    if (ok && ok.kind === 'ok') return ok.version;
    for (const entry of machines) {
      const version = desiredDocs[entry.metadata.name]?.entries?.[CODEX_AGENT]?.version;
      if (version) return version;
    }
    return '';
  });

  function hostOnPinned(name: string): boolean {
    if (!pinnedVersion) return false;
    const instances = codexInstances(name);
    if (instances.length === 0) return false;
    return instances.every((instance) => !instance.versionError && instance.version === pinnedVersion);
  }

  const anyInstalled = $derived(machines.some((item) => codexInstances(item.metadata.name).length > 0));

  const selectableUpgrade = $derived(upgradeSelected.filter((name) => !hostOnPinned(name)));

  const resultRows = $derived.by(() => {
    const fromOps = tracked.flatMap((op) => op.status.results ?? []);
    const source =
      fromOps.length > 0
        ? fromOps.map((result) => ({
            machine: result.target?.machine ?? '—',
            version: result.evidence?.observedVersion ?? '',
            state: result.state,
          }))
        : machines.map((item) => ({
            machine: item.metadata.name,
            version: hostVersion(item.metadata.name),
            state: hostResultState(item.metadata.name),
          }));
    return source.map((row) => ({
      ...row,
      tone: lifecycleStateTone(row.state),
      label: lifecycleStateLabel(row.state, loc),
    }));
  });

  const viewOp = $derived.by((): LifecycleOperation => {
    const results = resultRows.map((row) => ({
      state: row.state as LifecycleTargetState,
      target: {
        machine: row.machine,
        user: '',
        agent: CODEX_AGENT,
        method: '',
        package: '',
      },
    }));
    return {
      metadata: { name: 'agent-page' },
      spec: { type: 'Upgrade', idempotencyKey: 'agent-page', targets: [] },
      status: { results },
    };
  });
  const progress = $derived(operationProgress(viewOp));
  const failed = $derived(failedResults(viewOp));

  function openUpgrade() {
    upgradeSelected = [];
    upgradeStep = 'select';
    uninstallOpen = false;
    upgradeOpen = true;
  }

  function toggleUpgrade(name: string, checked: boolean) {
    if (hostOnPinned(name)) return;
    upgradeSelected = checked ? [...upgradeSelected, name] : upgradeSelected.filter((item) => item !== name);
  }

  function showUpgradePreview() {
    if (selectableUpgrade.length === 0) return;
    upgradeStep = 'preview';
  }

  function openUninstall() {
    if (!anyInstalled) return;
    upgradeOpen = false;
    uninstallOpen = true;
  }
</script>

<div class="agent-page grid gap-4">
  <nav class="flex items-center gap-2 text-sm" aria-label={t('nav.apps')}>
    <a class="text-[#1a73e8]" href="#/apps">{t('nav.apps')}</a>
  </nav>

  <div class="flex flex-wrap items-start justify-between gap-3">
    <h1 class="console-page-title">{t('lc.title')}</h1>
    <div class="flex flex-wrap gap-2">
      <button type="button" class="h-9 rounded border border-[#dadce0] bg-white px-3 text-sm" onclick={checkUpstream}>
        {t('lc.upstream.check')}
      </button>
      <button type="button" class="h-9 rounded bg-[#1a73e8] px-3 text-sm text-white" onclick={openUpgrade}>
        {t('agent.upgradeTo', { version: pinnedVersion })}
      </button>
      <button
        type="button"
        class="h-9 rounded border border-[#dadce0] bg-white px-3 text-sm disabled:opacity-50"
        disabled={!anyInstalled}
        onclick={openUninstall}
      >
        {t('lc.action.uninstall')}
      </button>
    </div>
  </div>

  <div class="agent-counts grid grid-cols-2 gap-3 sm:grid-cols-4">
    <div class="agent-count rounded border border-[#dadce0] bg-white p-3 tone-{lifecycleStateTone('succeeded')}">
      <div>{lifecycleStateLabel('succeeded', loc)}</div>
      <div>{progress.succeeded}</div>
    </div>
    <div class="agent-count rounded border border-[#dadce0] bg-white p-3 tone-{lifecycleStateTone('failed')}">
      <div>{lifecycleStateLabel('failed', loc)}</div>
      <div>{failed.length}</div>
    </div>
    <div class="agent-count rounded border border-[#dadce0] bg-white p-3 tone-{lifecycleStateTone('not_executed')}">
      <div>{lifecycleStateLabel('not_executed', loc)}</div>
      <div>{progress.notExecuted}</div>
    </div>
    <div class="agent-count rounded border border-[#dadce0] bg-white p-3 tone-{lifecycleStateTone('unknown')}">
      <div>{lifecycleStateLabel('unknown', loc)}</div>
      <div>{progress.unknown}</div>
    </div>
  </div>

  {#if upstreamState === 'ok'}
    <div class="grid gap-2">
      {#each upstreamRows as row, i (i)}
        <div class="agent-upstream-row rounded border border-[#dadce0] bg-white p-3 text-sm" data-kind={row.kind}>
          <div>{row.method}</div>
          {#if row.kind === 'ok'}
            <div>{t('lc.upstream.available')} {row.version}</div>
          {:else if row.kind === 'failed'}
            <div>{row.reasonText}</div>
            {#if row.unknown}
              <div>{t('lc.upstream.unknown')}</div>
            {/if}
          {:else}
            <div>{row.reasonText}</div>
          {/if}
        </div>
      {/each}
    </div>
  {:else if upstreamState === 'error'}
    <p role="alert">{t('lc.upstream.loadFailed')}{failureText(upstreamError, loc)} {t('error.loadRetryHint')}</p>
  {/if}

  <section>
    <h2 class="mb-2 text-sm font-medium">{t('agent.perHost')}</h2>
    <div class="overflow-hidden rounded-lg border border-[#dadce0] bg-white">
      <table class="w-full text-sm" data-failed={failed.length}>
        <thead>
          <tr class="border-b text-left text-[#5f6368]">
            <th class="px-3 py-2 font-medium">{t('agent.col.machine')}</th>
            <th class="px-3 py-2 font-medium">{t('agent.col.version')}</th>
            <th class="px-3 py-2 font-medium">{t('agent.col.state')}</th>
          </tr>
        </thead>
        <tbody>
          {#each resultRows as row, i (i)}
            <tr class="agent-result-row border-b last:border-0">
              <td class="px-3 py-2">{row.machine}</td>
              <td class="px-3 py-2 font-mono">{row.version || '—'}</td>
              <td class="px-3 py-2">
                <span class="inline-flex rounded border px-2 py-0.5 tone-{row.tone}">{row.label}</span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>

  {#if upgradeOpen}
    <div class="grid gap-3 rounded-lg border border-[#dadce0] bg-white p-4" role="dialog" aria-label={t('agent.upgradeTo', { version: pinnedVersion })}>
      {#if upgradeStep === 'select'}
        <p>{t('agent.alreadyPinned')}</p>
        <ul class="grid gap-2">
          {#each machines as item (item.metadata.name)}
            <li>
              <label class="flex items-center gap-2">
                <input
                  type="checkbox"
                  data-machine={item.metadata.name}
                  disabled={hostOnPinned(item.metadata.name)}
                  checked={selectableUpgrade.includes(item.metadata.name)}
                  onchange={(event) => toggleUpgrade(item.metadata.name, event.currentTarget.checked)}
                />
                <span>{item.metadata.name}</span>
              </label>
            </li>
          {/each}
        </ul>
        <div class="flex gap-2">
          <button type="button" disabled={selectableUpgrade.length === 0} onclick={showUpgradePreview}>
            {t('agent.preview')}
          </button>
          <button type="button" onclick={() => (upgradeOpen = false)}>{t('common.cancel')}</button>
        </div>
      {:else}
        <ul>
          {#each selectableUpgrade as name (name)}
            <li class="agent-preview-row">{name}</li>
          {/each}
        </ul>
        <button type="button" onclick={() => (upgradeStep = 'select')}>{t('agent.back')}</button>
      {/if}
    </div>
  {/if}

  {#if uninstallOpen}
    <div class="grid gap-3 rounded-lg border border-[#dadce0] bg-white p-4" role="dialog" aria-label={t('lc.action.uninstall')}>
      <p>{t('lc.action.uninstall')}</p>
      <ul>
        {#each machines.filter((item) => codexInstances(item.metadata.name).length > 0) as item (item.metadata.name)}
          <li>{item.metadata.name}</li>
        {/each}
      </ul>
      <button type="button" onclick={() => (uninstallOpen = false)}>{t('common.cancel')}</button>
    </div>
  {/if}
</div>

<div class="grid gap-5" style="display: none">
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
                      <CapabilityBadge capability={capFor(inst.method)} />
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
      targetSelected={sel.user !== ''}
      now={clock.now}
    />
  {/if}

  <!-- ③ 上游更新 -->
  <UpstreamCard
    state={upstreamState}
    rows={upstreamRows}
    checkedAt={upstreamCheckedAt}
    error={upstreamError}
    disabled={!sel.machine}
    now={clock.now}
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
                  title={of.offered ? undefined : (capabilityGateView(of.capability, loc)?.title ?? undefined)}
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
                <CapabilityBadge capability={capFor(installMethod)} />
              </div>
            {/if}
          </div>
        {:else if selectedInstance}
          <!-- 升级 / 卸载入口（实例级；来源已知） -->
          <div class="grid gap-2">
            <div class="text-xs font-medium text-muted-foreground">{t('lc.action.pickInstance')}</div>
            <p class="font-mono text-xs">{instanceTitle(selectedInstance, loc)} · {selectedInstance.executablePath ?? ''}</p>
            {#if !activeSupported}
              <!-- 未验证/blocked/能力未知：如实展示状态与原因，不提供操作入口（T7 P7） -->
              <div class="rounded-md border border-dashed p-3" role="status">
                <p class="text-sm font-medium">{gate?.title}</p>
                {#if gate?.detail}
                  <p class="mt-1 text-xs text-muted-foreground">{gate.detail}</p>
                {/if}
                {#if activeCapability?.state === 'unverified'}
                  <p class="mt-1 text-xs text-muted-foreground">{t('lc.cap.unverifiedHint')}</p>
                {/if}
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
    {confirmingOpId}
    {lookupError}
    message={resultsMsg}
    onretry={retryFailed}
    onconfirmOp={confirmTracked}
    onlookup={lookupOperation}
  />
</div>
