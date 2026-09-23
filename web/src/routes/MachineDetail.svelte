<script lang="ts">
  /**
   * Machine Detail（spec §24.3 的 9 个区块，顺序一致）：
   *  1) 联通与状态  2) 当前 Agents 与版本  3) desired vs observed  4) Skills
   *  5) model/provider  6) MCP  7) drift diff  8) 操作历史  9) bootstrap/repair 动作
   * 无交互 shell（非目标 #2/#3）。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
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
  import { actionAvailability, CANCEL_CONSEQUENCES, SKIP_CONSEQUENCES, viewOperations } from '$lib/state/operations.js';
  import {
    agentVersionNotice,
    awaitingConfirmations,
    describeActionFailure,
    hostKeyNotice,
    PLAN_TIMEOUT_NOTICE,
    UNIMPLEMENTED_SSH_ENDPOINTS,
  } from '$lib/state/ssh.js';
  import { formatDuration, formatRelativeTime, formatTime, prettyJSON, shortDigest } from '$lib/state/format.js';
  import { untrack } from 'svelte';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import type { RenderPreview } from '$lib/api/types.js';
  import { ApiError } from '$lib/api/client.js';

  interface Props {
    store: FleetStore;
    name: string;
  }
  let { store, name }: Props = $props();

  const clock = createClock(15_000);
  const detail = $derived(store.details[name]);
  const machine = $derived(detail?.machine ?? store.machineByName(name));
  const conditions = $derived(allConditionSummaries(machine?.status?.conditions));
  const drift = $derived(detail?.drift ? driftView(driftInputFromApi(detail.drift), clock.now) : null);
  const ops = $derived(viewOperations(detail?.operations ?? [], clock.now));
  const availability = $derived(actionAvailability(detail?.operations ?? [], clock.now));
  const awaiting = $derived(awaitingConfirmations(detail?.operations ?? [], clock.now));
  const hostKey = $derived(machine ? hostKeyNotice(machine) : null);
  const agentVersion = $derived(machine ? agentVersionNotice(machine) : null);
  const profileRef = $derived(machine?.spec?.profileRef);

  let render = $state<RenderPreview | null>(null);
  let renderError = $state<string | null>(null);
  let renderLoading = $state(false);
  let actionMessage = $state<{ kind: 'ok' | 'error'; text: string; detail?: string } | null>(null);
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
      renderError = describeActionFailure(err).detail;
    } finally {
      renderLoading = false;
    }
  }

  async function act(key: string, fn: () => Promise<string>, detailText?: string) {
    busy = key;
    actionMessage = null;
    try {
      const text = await fn();
      actionMessage = { kind: 'ok', text, detail: detailText };
      await store.afterMutation('machines', 'operations', 'deployments');
      if (profileRef) await loadRender(profileRef);
    } catch (err) {
      const failure = describeActionFailure(err);
      actionMessage = {
        kind: 'error',
        text: `${failure.title}：${failure.detail}`,
        detail:
          failure.unresolvedOperationId !== undefined
            ? `未决操作 ${failure.unresolvedOperationId}（相位 ${failure.unresolvedPhase ?? '未知'}）`
            : failure.replanHint,
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
        return `已受理收敛请求：operation ${op.metadata.name}（${op.status.phase}）。`;
      },
      '若该机存在未决操作，服务端会返回 409 MachineBusy 并在提示里给出未决操作引用。',
    );

  const confirmPlan = (digest: string) =>
    act(
      'confirm',
      async () => {
        const op = await store.client.reconcile(name, digest);
        return `已确认计划并推进：operation ${op.metadata.name}（${op.status.phase}）。`;
      },
      '确认只代表"确认的是这份计划"；apply 仍会重算并比对基线，基线不一致会报 ReplanRequired。',
    );

  const cancelOp = (opId: string) =>
    act('cancel', async () => {
      const op = await store.client.cancelOperation(name, opId);
      return `已请求取消：operation ${op.metadata.name}（${op.status.phase}）。`;
    }, CANCEL_CONSEQUENCES);

  const skipOp = (opId: string) =>
    act(
      'skip',
      async () => {
        if (!skipReason.trim()) throw new Error('跳过必须填写原因（审计要求，FR-15.4）');
        const op = await store.client.skipOperation(name, opId, skipReason.trim());
        skipReason = '';
        return `已跳过：operation ${op.metadata.name}（${op.status.phase} / ${op.status.terminalModifier ?? '无修饰'}）。`;
      },
      SKIP_CONSEQUENCES,
    );

  const rollback = () =>
    act('rollback', async () => {
      const gen = Number(rollbackGeneration);
      if (!Number.isInteger(gen) || gen <= 0) throw new Error('目标 generation 必须是正整数');
      const op = await store.client.rollbackMachine(name, gen);
      rollbackGeneration = '';
      return `已受理回滚：operation ${op.metadata.name}，内容目标代 gen${gen}（回滚不把机器拉回旧代，而是物化新代）。`;
    });
</script>

<div class="grid gap-5">
  <div class="flex flex-wrap items-end justify-between gap-3">
    <div>
      <button class="text-xs text-muted-foreground underline" onclick={() => navigate('/machines')}>
        ← Machines
      </button>
      <h1 class="text-lg font-semibold">{name}</h1>
      <p class="text-xs text-muted-foreground">
        managementMode {machine?.spec?.managementMode ?? 'agentd'} · profileRef {profileRef ?? '—'} ·
        最后更新 {formatRelativeTime(machine?.metadata?.creationTimestamp ?? '', clock.now)}
      </p>
    </div>
    <div class="flex flex-wrap gap-2">
      <Button size="sm" variant="outline" disabled={availability.actions[0].blocked || busy !== null} onclick={reconcile}>
        Reconcile
      </Button>
      <Button size="sm" variant="outline" disabled title={UNIMPLEMENTED_SSH_ENDPOINTS.join('、') + ' 属第 6 片'}>
        Bootstrap / Repair agentd
      </Button>
    </div>
  </div>

  <AsyncState loading={detail?.loading && !machine} error={detail?.error} empty={!machine ? `机器 ${name} 不存在（可能已被删除）。` : undefined} />

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
        <Alert.Title>{actionMessage.kind === 'error' ? '动作被拒绝' : '动作已受理'}</Alert.Title>
        <Alert.Description>
          {actionMessage.text}
          {#if actionMessage.detail}<div class="mt-1">{actionMessage.detail}</div>{/if}
        </Alert.Description>
      </Alert.Root>
    {/if}

    <!-- 1. 联通与状态 -->
    <Card>
      <CardHeader><CardTitle class="text-sm">1 · 联通与状态</CardTitle></CardHeader>
      <CardContent class="grid gap-4">
        <div class="flex flex-wrap gap-2">
          {#each conditions as cond (cond.type)}
            <StateBadge
              tone={cond.tone}
              label={cond.label}
              title={`${cond.message ?? ''}${cond.reason ? ` (reason=${cond.reason})` : ''} 最近转变 ${formatTime(cond.lastTransitionTime)}`}
            />
          {/each}
        </div>
        <div class="grid grid-cols-2 gap-x-6 gap-y-1 text-xs md:grid-cols-4">
          <div><span class="text-muted-foreground">OS/Arch：</span>{machine.status?.os ?? '—'}/{machine.status?.arch ?? '—'}</div>
          <div><span class="text-muted-foreground">hostname：</span>{machine.status?.hostname ?? '—'}</div>
          <div><span class="text-muted-foreground">homeDir：</span>{machine.status?.homeDir ?? '—'}</div>
          <div><span class="text-muted-foreground">kernel：</span>{machine.status?.kernelVersion ?? '—'}</div>
          <div><span class="text-muted-foreground">agentd：</span>{machine.status?.agentdVersion ?? '—'}</div>
          <div><span class="text-muted-foreground">lastHeartbeat：</span>{formatRelativeTime(machine.status?.lastHeartbeatAt ?? '', clock.now)}</div>
          <div><span class="text-muted-foreground">lastInventory：</span>{formatRelativeTime(machine.status?.lastInventoryAt ?? '', clock.now)}</div>
          <div><span class="text-muted-foreground">inventorySeq：</span>{machine.status?.inventorySeq ?? '—'}</div>
          <div><span class="text-muted-foreground">desiredGeneration：</span>{machine.status?.desiredGeneration ?? '—'}</div>
          <div><span class="text-muted-foreground">observedGeneration：</span>{machine.status?.observedGeneration ?? '—'}</div>
        </div>
        <p class="text-xs text-muted-foreground">
          FR-1.9：Drifted/Reconciled 在"从未采集/采集已过期/期望代已变化且未重采"三种情形下必须为
          Unknown，界面不把 Unknown 渲染成一致。
        </p>
      </CardContent>
    </Card>

    <!-- 2. 当前 Agents 与版本 -->
    <Card>
      <CardHeader>
        <CardTitle class="text-sm">2 · 当前 Agents 与版本</CardTitle>
        <p class="text-xs text-muted-foreground">
          左侧为期望代渲染结果（权威、可复算）；右侧为节点上报的能力声明（控制面只读转述，KM-24）。
        </p>
      </CardHeader>
      <CardContent class="grid gap-4 lg:grid-cols-2">
        <div>
          {#if renderLoading}
            <AsyncState loading rows={2} />
          {:else if renderError}
            <Alert.Root variant="destructive">
              <Alert.Title>期望状态不可渲染（DesiredStateInvalid）</Alert.Title>
              <Alert.Description>{renderError}</Alert.Description>
            </Alert.Root>
          {:else if render?.desired?.agents && Object.keys(render.desired.agents).length > 0}
            <Table.Root>
              <Table.Header>
                <Table.Row><Table.Head>家族</Table.Head><Table.Head>期望版本</Table.Head></Table.Row>
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
            <p class="text-xs text-muted-foreground">期望代没有 agents 条目（profile 未配置）。</p>
          {/if}
        </div>
        <div>
          {#if (machine.status?.adapterCapabilities ?? []).length > 0}
            <Table.Root>
              <Table.Header>
                <Table.Row>
                  <Table.Head>家族</Table.Head><Table.Head>能力</Table.Head><Table.Head>状态</Table.Head>
                  <Table.Head>已验证版本</Table.Head><Table.Head>说明</Table.Head>
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
            <p class="mt-2 text-xs text-muted-foreground">
              状态与说明逐字取自节点上报，不把未验证能力表述为已验证（含 macOS/Windows/WSL、安装/升级未实现等未验证项）。
            </p>
          {:else}
            <p class="text-xs text-muted-foreground">
              节点尚未上报能力声明（无活跃 Connect 流或未上报）。观测侧的完整 inventory 正文未由 API 暴露（契约缺口）。
            </p>
          {/if}
        </div>
      </CardContent>
    </Card>

    <!-- 3. desired vs observed -->
    <Card>
      <CardHeader><CardTitle class="text-sm">3 · desired vs observed</CardTitle></CardHeader>
      <CardContent class="grid gap-2 text-xs">
        <div>
          期望代 gen{machine.status?.desiredGeneration ?? '—'} ↔ 观测代 gen{machine.status?.observedGeneration ?? '—'}
          {#if machine.status?.desiredGeneration !== machine.status?.observedGeneration}
            <StateBadge tone="warn" label="代不一致" title="观测尚未按当前期望代采集；此时 Drifted 应为 Unknown（FR-1.9）" />
          {:else}
            <StateBadge tone="ok" label="代一致" />
          {/if}
        </div>
        <div class="grid gap-1 font-mono">
          <div>desiredProjectionDigest ：{shortDigest(machine.status?.desiredProjectionDigest)}</div>
          <div>observedProjectionDigest：{shortDigest(machine.status?.observedProjectionDigest)}</div>
          <div>canonicalizationVersion ：{machine.status?.canonicalizationVersion ?? '—'}</div>
          <div>期望快照摘要（render）：{render?.digest ? shortDigest(render.digest) : '—'}</div>
        </div>
        <p class="text-muted-foreground">
          判据唯一：两侧受管投影摘要相等且 canonicalizationVersion 一致（FR-8.6）。逐字段差异与观测正文未由
          REST 暴露，界面不臆造 diff（见 §7 区块说明）。
        </p>
      </CardContent>
    </Card>

    <!-- 4. Skills -->
    <Card>
      <CardHeader><CardTitle class="text-sm">4 · Skills</CardTitle></CardHeader>
      <CardContent>
        {#if render?.desired?.skills && Object.keys(render.desired.skills).length > 0}
          <Table.Root>
            <Table.Header>
              <Table.Row>
                <Table.Head>名称</Table.Head><Table.Head>修订</Table.Head><Table.Head>内容 digest</Table.Head>
                <Table.Head>工件 digest</Table.Head>
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
          <p class="text-xs text-muted-foreground">
            该机期望代没有已解析的 Skill（profile 未引用，或 Skill 未 resolve——渲染会以
            DesiredStateInvalid 显式失败，不做 latest 式浮动引用）。
          </p>
        {/if}
      </CardContent>
    </Card>

    <!-- 5. model/provider -->
    <Card>
      <CardHeader><CardTitle class="text-sm">5 · model / provider</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        {#if machine.spec?.managementMode === 'ssh'}
          <p class="text-xs text-muted-foreground">SSH-only 机器不做后台轮询，故这里只在有操作时呈现期望侧。</p>
        {/if}
        {#if render?.desired?.agents}
          <Table.Root>
            <Table.Header>
              <Table.Row><Table.Head>家族</Table.Head><Table.Head>provider（profile 配置）</Table.Head><Table.Head>期望 config</Table.Head></Table.Row>
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
          <p class="text-xs text-muted-foreground">无期望 agents 条目。</p>
        {/if}
        {#if store.providers.length > 0}
          <div class="text-xs text-muted-foreground">
            已登记 Provider：{store.providers.map((p) => p.metadata.name).join('、')}
            （只存 apiKeyEnv 环境变量名，界面永不显示任何凭据值）。
          </div>
        {/if}
      </CardContent>
    </Card>

    <!-- 6. MCP -->
    <Card>
      <CardHeader><CardTitle class="text-sm">6 · MCP</CardTitle></CardHeader>
      <CardContent>
        {#if render?.desired?.mcp && Object.keys(render.desired.mcp).length > 0}
          <Table.Root>
            <Table.Header><Table.Row><Table.Head>名称</Table.Head><Table.Head>归一化条目</Table.Head></Table.Row></Table.Header>
            <Table.Body>
              {#each Object.entries(render.desired.mcp) as [mcpName, entry] (mcpName)}
                <Table.Row>
                  <Table.Cell class="text-xs">{mcpName}</Table.Cell>
                  <Table.Cell class="font-mono text-xs">{prettyJSON(entry)}</Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
          <p class="mt-2 text-xs text-muted-foreground">
            归一化条目中的 envRefs 只含环境变量名映射（FR-4.1）；未点名的 MCP 条目属用户所有，不在受管投影内。
          </p>
        {:else}
          <p class="text-xs text-muted-foreground">期望代未点名 MCP 条目。</p>
        {/if}
      </CardContent>
    </Card>

    <!-- 7. drift diff -->
    <Card>
      <CardHeader><CardTitle class="text-sm">7 · drift diff</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        {#if drift}
          <div class="flex flex-wrap items-center gap-2">
            <StateBadge tone={drift.tone} label={drift.label} />
            {#if drift.reasonCode}<span class="font-mono text-xs text-muted-foreground">reason={drift.reasonCode}</span>{/if}
            <Button size="sm" variant="outline" disabled={availability.actions[0].blocked || busy !== null} onclick={reconcile}>
              Reconcile（收敛）
            </Button>
          </div>
          <p class="text-xs">{drift.detail}</p>
          <div class="grid gap-1 font-mono text-xs">
            <div>期望受管投影摘要：{shortDigest(drift.desiredDigest, 12)}</div>
            <div>观测受管投影摘要：{shortDigest(drift.observedDigest, 12)}</div>
          </div>
          {#if drift.state === 'drifted'}
            <Alert.Root>
              <Alert.Title>逐字段 diff 未由 API 暴露</Alert.Title>
              <Alert.Description>
                本片 REST 只提供漂移判定与两侧摘要（GET /machines/{name}/drift），没有字段级 diff 端点。
                界面只呈现摘要与判定，不推断"哪些字段变了"；收敛入口为 Reconcile。
              </Alert.Description>
            </Alert.Root>
          {/if}
        {:else}
          <p class="text-xs text-muted-foreground">drift 视图不可用（该机可能刚被删除或读取失败）。</p>
        {/if}
      </CardContent>
    </Card>

    <!-- 8. 操作历史 -->
    <Card>
      <CardHeader>
        <CardTitle class="text-sm">8 · 操作历史与未决操作</CardTitle>
        {#if availability.blockedBy}
          <Alert.Root class="mt-2">
            <Alert.Title>该机有未决操作（{availability.blockedBy.phase}）</Alert.Title>
            <Alert.Description>
              operation {availability.blockedBy.id}。新的变更类操作会被拒绝（409 MachineBusy），
              Deployment 派发到该机保持阻塞；可用入口只有对同一操作的确认/取消/跳过。
            </Alert.Description>
          </Alert.Root>
        {/if}
      </CardHeader>
      <CardContent class="grid gap-4">
        {#if awaiting.length > 0}
          {#each awaiting as item (item.operation.id)}
            <Alert.Root>
              <Alert.Title>
                等待确认（剩余 {item.remainingText}）· operation {item.operation.id}
              </Alert.Title>
              <Alert.Description>
                <div>planDigest：<code class="font-mono text-xs">{shortDigest(item.planDigest, 10)}</code></div>
                <div class="mt-1">{item.baselineNotice}</div>
                <div class="mt-1">{PLAN_TIMEOUT_NOTICE}</div>
                <div class="mt-2 flex gap-2">
                  <Button size="sm" disabled={busy !== null} onclick={() => confirmPlan(item.planDigest ?? '')}>确认计划</Button>
                  <Button size="sm" variant="outline" disabled={busy !== null} onclick={() => cancelOp(item.operation.id)}>取消</Button>
                </div>
              </Alert.Description>
            </Alert.Root>
          {/each}
        {/if}

        {#if ops.length === 0}
          <p class="text-xs text-muted-foreground">该机还没有操作记录。</p>
        {:else}
          <Table.Root>
            <Table.Header>
              <Table.Row>
                <Table.Head>操作</Table.Head><Table.Head>类型/来源</Table.Head><Table.Head>相位</Table.Head>
                <Table.Head>修饰</Table.Head><Table.Head>门禁证据</Table.Head><Table.Head>时间</Table.Head>
                <Table.Head>处置</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#each ops as op (op.id)}
                <Table.Row>
                  <Table.Cell class="font-mono text-xs">{shortDigest(op.id, 6)}</Table.Cell>
                  <Table.Cell class="text-xs">
                    {op.typeLabel}
                    <div class="text-muted-foreground">
                      {op.source === 'automatic' ? '自动（只读 plan）' : '手动'}
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
                        label={op.terminalModifier}
                        title={op.modifierNote ?? ''}
                      />
                    {:else}—{/if}
                  </Table.Cell>
                  <Table.Cell class="text-xs text-muted-foreground">
                    {op.adapterHealth ? `adapterHealth=${op.adapterHealth}` : '—'}
                    {#if op.verifyInventorySeq !== undefined}<div>inventorySeq={op.verifyInventorySeq}</div>{/if}
                  </Table.Cell>
                  <Table.Cell class="text-xs text-muted-foreground">
                    {formatRelativeTime(op.finishedAt ?? op.startedAt ?? op.createdAt ?? '', clock.now)}
                  </Table.Cell>
                  <Table.Cell>
                    {#if op.isUnresolved}
                      <div class="flex flex-col gap-1">
                        <Button size="sm" variant="outline" disabled={busy !== null} onclick={() => cancelOp(op.id)}>取消</Button>
                        <Input class="h-7 w-40 text-xs" placeholder="跳过原因（必填）" bind:value={skipReason} />
                        <Button size="sm" variant="ghost" disabled={busy !== null || skipReason.trim() === ''} onclick={() => skipOp(op.id)}>
                          跳过
                        </Button>
                      </div>
                    {:else}—{/if}
                  </Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
          <p class="text-xs text-muted-foreground">
            跳过必须填原因（审计），且不表示节点已停止；Unknown 相位不得由系统自动夺锁（§5.6）。
          </p>
        {/if}

        <Tabs.Root value="blocked">
          <Tabs.List>
            <Tabs.Trigger value="blocked">未决期间的阻塞与例外</Tabs.Trigger>
          </Tabs.List>
          <Tabs.Content value="blocked">
            <Table.Root>
              <Table.Header><Table.Row><Table.Head>动作</Table.Head><Table.Head>状态</Table.Head><Table.Head>说明</Table.Head></Table.Row></Table.Header>
              <Table.Body>
                {#each availability.actions as action (action.action)}
                  <Table.Row>
                    <Table.Cell class="text-xs">{action.label}</Table.Cell>
                    <Table.Cell>
                      <StateBadge
                        tone={action.blocked ? 'warn' : 'ok'}
                        label={action.blocked ? '被阻塞/不可用' : '可用'}
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

    <!-- 9. bootstrap / repair 动作 -->
    <Card>
      <CardHeader><CardTitle class="text-sm">9 · bootstrap / repair / 回滚动作</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        <div class="flex flex-wrap items-end gap-3">
          <div class="grid gap-1">
            <Label for="rollback-gen" class="text-xs">回滚到内容代（正整数）</Label>
            <Input id="rollback-gen" class="w-32" bind:value={rollbackGeneration} placeholder="12" />
          </div>
          <Button size="sm" variant="outline" disabled={busy !== null || availability.actions[1].blocked} onclick={rollback}>
            Rollback
          </Button>
          <Button size="sm" variant="outline" disabled title="POST /machines/:name/bootstrap 属第 6 片">
            Bootstrap
          </Button>
          <Button size="sm" variant="outline" disabled title="POST /machines/:name/repair-agentd 属第 6 片">
            Repair agentd
          </Button>
          <Button size="sm" variant="outline" disabled title="POST /machines/:name/inventory 属第 6 片">
            Request inventory
          </Button>
        </div>
        <p class="text-xs text-muted-foreground">
          Rollback 语义：内容取自历史代，但会为该机物化**新代**（回滚不把机器拉回旧代，FR-7.5/ADR-2）。
          图中未提供"签发 enrollment token"入口：token 是凭据，前端不展示、不存储 secret（本片边界）。
          SSH 路径动作（probe/bootstrap/repair-agentd/inventory）属第 6 片，当前未注册。
        </p>
      </CardContent>
    </Card>
  {/if}
</div>
