<script lang="ts">
  /**
   * Deployments（spec §24.6）：canary/batch 进度与逐机结果；动作 Pause / Resume /
   * Rollback。
   *
   * FR-14.5 第 3 组：目标 `Superseded`（机器已在更新代）与 `Skipped`（操作者显式跳过）
   * 必须与 `Failed` 分开显示，且各自带原因。
   * 可用性：本片后端只注册了 rollback 与 skip-target；pause/resume 未注册（按钮禁用）。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import {
    deploymentPhaseLabel,
    deploymentProgress,
    rollbackLabel,
    strategyLabel,
    viewTarget,
  } from '$lib/state/deployments.js';
  import { formatRelativeTime, shortDigest } from '$lib/state/format.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { describeActionFailure } from '$lib/state/ssh.js';

  interface Props {
    store: FleetStore;
    name: string | null;
  }
  let { store, name }: Props = $props();

  const clock = createClock(15_000);
  const selected = $derived(store.deployments.find((d) => d.metadata.name === name) ?? null);
  let skipReason = $state('');
  let message = $state<{ kind: 'ok' | 'error'; text: string; detail?: string } | null>(null);
  let busy = $state<string | null>(null);

  $effect(() => {
    store.activeDeployment = name;
    return () => {
      if (store.activeDeployment === name) store.activeDeployment = null;
    };
  });

  async function skipTarget(deployment: string, machine: string) {
    busy = `skip:${machine}`;
    message = null;
    try {
      if (!skipReason.trim()) throw new Error('跳过必须填写原因（审计要求，FR-10.7）');
      await store.client.skipDeploymentTarget(deployment, machine, skipReason.trim());
      message = {
        kind: 'ok',
        text: `已跳过 ${machine}（记录 Skipped + 审计；不计入成功，且不改变节点实际状态）。`,
        detail: '若该机仍在写，请以节点实际状态为准；跳过只释放发布侧推进。',
      };
      skipReason = '';
      await store.afterMutation('deployments');
    } catch (err) {
      message = { kind: 'error', text: describeActionFailure(err).detail };
    } finally {
      busy = null;
    }
  }

  async function rollback(deployment: string) {
    busy = `rollback:${deployment}`;
    message = null;
    try {
      const created = await store.client.rollbackDeployment(deployment);
      message = {
        kind: 'ok',
        text: `已创建回滚型 Deployment ${created.metadata.name}（内容代 gen${created.spec?.targetGeneration ?? '?'}）。`,
        detail: '回滚逐机物化新代：机器不会被拉回旧代，代计数不回退。',
      };
      await store.afterMutation('deployments');
    } catch (err) {
      message = { kind: 'error', text: describeActionFailure(err).detail };
    } finally {
      busy = null;
    }
  }
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">Deployments</h1>
    <p class="text-xs text-muted-foreground">
      批次推进与逐机结果；Superseded（机器已在更新代）与 Skipped（显式跳过）与 Failed 分开呈现。
    </p>
  </div>

  {#if message}
    <Alert.Root variant={message.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{message.kind === 'error' ? '动作被拒绝' : '已完成'}</Alert.Title>
      <Alert.Description>
        {message.text}
        {#if message.detail}<div class="mt-1">{message.detail}</div>{/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  <Card>
    <CardHeader><CardTitle class="text-sm">全部发布（{store.deployments.length}）</CardTitle></CardHeader>
    <CardContent>
      <AsyncState
        loading={store.loading.deployments}
        error={store.errors.deployments}
        empty={store.deployments.length === 0 ? '还没有 Deployment。' : undefined}
      />
      {#if store.deployments.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>Name</Table.Head><Table.Head>Phase</Table.Head><Table.Head>目标代</Table.Head>
              <Table.Head>策略</Table.Head><Table.Head>进度</Table.Head><Table.Head>异常态</Table.Head>
              <Table.Head>动作</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each store.deployments as dep (dep.metadata.name)}
              {@const progress = deploymentProgress(dep)}
              {@const unusual = (dep.status?.targets ?? []).map((t) => viewTarget(t, clock.now)).filter((t) => t.phase !== 'Succeeded')}
              <Table.Row class={dep.metadata.name === name ? 'bg-muted/60' : ''}>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/deployments/${dep.metadata.name}`)}>{dep.metadata.name}</button>
                  {#if dep.spec?.rollbackOf !== undefined}<div class="text-xs text-muted-foreground">回滚型</div>{/if}
                </Table.Cell>
                <Table.Cell class="text-xs">{deploymentPhaseLabel(dep)}</Table.Cell>
                <Table.Cell class="text-xs">gen{dep.spec?.targetGeneration ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">{strategyLabel(dep)}</Table.Cell>
                <Table.Cell class="text-xs">
                  <div class="h-2 w-28 overflow-hidden rounded bg-muted">
                    <div class="h-full bg-primary" style={`width:${progress.succeededPercent}%`}></div>
                  </div>
                  <div class="mt-1 text-muted-foreground">
                    {progress.succeeded}/{progress.total} 成功 · 失败 {progress.failed} · 取代 {progress.superseded} ·
                    跳过 {progress.skipped} · 阻塞 {progress.blocked}
                  </div>
                </Table.Cell>
                <Table.Cell class="text-xs">
                  {#if unusual.length === 0}—{:else}
                    {#each unusual.slice(0, 3) as t (t.machine)}
                      <div>{t.machine}：{t.phaseLabel}</div>
                    {/each}
                    {#if unusual.length > 3}<div class="text-muted-foreground">…共 {unusual.length} 项</div>{/if}
                  {/if}
                </Table.Cell>
                <Table.Cell>
                  <div class="flex flex-wrap gap-1">
                    <Button size="sm" variant="outline" disabled title="POST /deployments/:name/pause 当前未注册（§23.5 列出但本片未实现）">Pause</Button>
                    <Button size="sm" variant="outline" disabled title="POST /deployments/:name/resume 当前未注册">Resume</Button>
                    <Button size="sm" variant="ghost" disabled={busy !== null} onclick={() => rollback(dep.metadata.name)}>Rollback</Button>
                  </div>
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  {#if selected}
    {@const progress = deploymentProgress(selected)}
    <Card>
      <CardHeader>
        <CardTitle class="text-sm">{selected.metadata.name} · 逐机结果</CardTitle>
        <p class="text-xs text-muted-foreground">
          {deploymentPhaseLabel(selected)} · {strategyLabel(selected)}
          {#if rollbackLabel(selected)}<span class="ml-2">{rollbackLabel(selected)}</span>{/if}
        </p>
      </CardHeader>
      <CardContent class="grid gap-4">
        <div class="grid grid-cols-2 gap-3 text-xs md:grid-cols-4">
          <div><span class="text-muted-foreground">目标机：</span>{progress.total}</div>
          <div><span class="text-muted-foreground">成功：</span>{progress.succeeded}</div>
          <div><span class="text-muted-foreground">失败：</span>{progress.failed}</div>
          <div><span class="text-muted-foreground">取代/跳过：</span>{progress.superseded}/{progress.skipped}</div>
        </div>
        {#if selected.status?.reason}
          <div class="text-xs">发布级 reason：<span class="font-mono">{selected.status.reason}</span></div>
        {/if}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>机器</Table.Head><Table.Head>结果</Table.Head><Table.Head>原因/说明</Table.Head>
              <Table.Head>有效目标代</Table.Head><Table.Head>操作</Table.Head><Table.Head>更新</Table.Head>
              <Table.Head>处置</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each (selected.status?.targets ?? []).map((t) => viewTarget(t, clock.now)) as target (target.machine)}
              <Table.Row>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/machines/${target.machine}`)}>{target.machine}</button>
                </Table.Cell>
                <Table.Cell>
                  <StateBadge
                    tone={target.tone}
                    label={target.phaseLabel}
                    title={target.phase === 'Skipped' || target.phase === 'Superseded' ? '与 Failed 不同：不表示执行失败' : ''}
                  />
                </Table.Cell>
                <Table.Cell class="max-w-[360px] text-xs text-muted-foreground">{target.reasonText ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs">gen{target.effectiveGeneration ?? '—'}</Table.Cell>
                <Table.Cell class="font-mono text-xs">{shortDigest(target.operationId, 6)}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">{target.updatedAtText ?? '—'}</Table.Cell>
                <Table.Cell>
                  {#if target.phase === 'Blocked' || target.phase === 'Pending' || target.phase === 'Running'}
                    <div class="flex flex-col gap-1">
                      <Input class="h-7 w-40 text-xs" placeholder="跳过原因（必填）" bind:value={skipReason} />
                      <Button size="sm" variant="ghost" disabled={busy !== null || skipReason.trim() === ''} onclick={() => skipTarget(selected.metadata.name, target.machine)}>
                        跳过该目标
                      </Button>
                    </div>
                  {:else}—{/if}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
        <p class="text-xs text-muted-foreground">
          未决目标（FR-10.7）：目标机存在未决操作（含 AwaitingConfirmation/Unknown）时保持阻塞、不计入失败也不推进批次；
          跳过必须留审计且不计入成功，跳过也不改变节点实际状态。
        </p>
      </CardContent>
    </Card>
  {/if}
</div>
