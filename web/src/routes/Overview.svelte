<script lang="ts">
  /**
   * Overview（spec §24.1）：卡片 = 总机器数 / daemon 在线 / SSH 可达 / 漂移 /
   * 降级 / 活跃发布；表 = 需关注机器、最近发布、最近失败操作。
   *
   * 卡片与"需关注"判定用同一套三态视图（lib/state/machines.ts），
   * 因此不存在"卡片说漂移、表格说一致"的矛盾。
   */
  import { onMount } from 'svelte';
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { machineRows, overviewStats } from '$lib/state/machines.js';
  import { deploymentPhaseLabel, deploymentProgress, viewTarget } from '$lib/state/deployments.js';
  import { viewOperations } from '$lib/state/operations.js';
  import { formatRelativeTime, shortDigest } from '$lib/state/format.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  onMount(() => {
    void store.loadRecentOperations(true);
  });

  const clock = createClock();

  const rows = $derived(machineRows(store.machines, clock.now));
  const stats = $derived(overviewStats(store.machines, store.deployments));
  const attention = $derived(rows.filter((r) => r.needsAttention));
  const recentDeployments = $derived(
    [...store.deployments]
      .sort((a, b) =>
        (b.metadata?.creationTimestamp ?? '').localeCompare(a.metadata?.creationTimestamp ?? ''),
      )
      .slice(0, 5),
  );
  const failedOps = $derived(
    viewOperations(store.recentOperations, clock.now)
      .filter((op) => op.phase === 'Failed' || (op.phase === 'Succeeded' && op.terminalModifier))
      .slice(0, 8),
  );
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">Overview</h1>
    <p class="text-xs text-muted-foreground">
      数据经 REST 重取；表格随 SSE 事件（资源类型/ID + revision）选择性刷新（FR-14.2）。
    </p>
  </div>

  <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
    {#each [
      { title: '机器总数', value: stats.totalMachines, hint: '' },
      { title: 'daemon 在线', value: stats.daemonOnline, hint: 'AgentConnected=True' },
      { title: 'SSH 可达', value: stats.sshReachable, hint: 'SSHReachable=True' },
      {
        title: '已漂移',
        value: stats.drifted,
        hint: stats.unknownDrift > 0 ? `另有 ${stats.unknownDrift} 台未知或过期` : 'Drifted=True',
      },
      { title: '降级', value: stats.degraded, hint: 'Degraded=True' },
      { title: '活跃发布', value: stats.activeDeployments, hint: 'Pending/Canary/RollingOut/Paused' },
    ] as card (card.title)}
      <Card>
        <CardHeader class="pb-2">
          <CardTitle class="text-xs font-medium text-muted-foreground">{card.title}</CardTitle>
        </CardHeader>
        <CardContent>
          <div class="text-2xl font-semibold tabular-nums">{card.value}</div>
          {#if card.hint}<div class="mt-1 text-xs text-muted-foreground">{card.hint}</div>{/if}
        </CardContent>
      </Card>
    {/each}
  </div>

  <Card>
    <CardHeader><CardTitle class="text-sm">需要关注的机器</CardTitle></CardHeader>
    <CardContent>
      <AsyncState
        loading={store.loading.machines}
        error={store.errors.machines}
        empty={attention.length === 0 ? '没有需要关注的机器。' : undefined}
      />
      {#if attention.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>Name</Table.Head>
              <Table.Head>Drift</Table.Head>
              <Table.Head>Reconciled</Table.Head>
              <Table.Head>agentd</Table.Head>
              <Table.Head>Last Seen</Table.Head>
              <Table.Head>原因</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each attention as row (row.name)}
              <Table.Row>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/machines/${row.name}`)}>
                    {row.name}
                  </button>
                </Table.Cell>
                <Table.Cell>
                  <StateBadge
                    tone={row.drift.tone}
                    label={row.drift.label}
                    title={row.drift.detail ?? ''}
                  />
                </Table.Cell>
                <Table.Cell>
                  <StateBadge tone={row.reconciled.tone} label={row.reconciled.label} />
                </Table.Cell>
                <Table.Cell>
                  <StateBadge tone={row.agentd.tone} label={row.agentd.label} />
                </Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">{row.lastSeenText}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">
                  {row.attentionReasons.join('；')}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  <Card>
    <CardHeader><CardTitle class="text-sm">最近发布</CardTitle></CardHeader>
    <CardContent>
      <AsyncState
        loading={store.loading.deployments}
        error={store.errors.deployments}
        empty={recentDeployments.length === 0 ? '暂无 Deployment。' : undefined}
      />
      {#if recentDeployments.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>Name</Table.Head>
              <Table.Head>Phase</Table.Head>
              <Table.Head>进度</Table.Head>
              <Table.Head>目标异常态</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each recentDeployments as dep (dep.metadata.name)}
              {@const progress = deploymentProgress(dep)}
              {@const unusual = (dep.status?.targets ?? [])
                .map((t) => viewTarget(t, clock.now))
                .filter((t) => t.phase === 'Superseded' || t.phase === 'Skipped' || t.phase === 'Failed')}
              <Table.Row>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/deployments/${dep.metadata.name}`)}>
                    {dep.metadata.name}
                  </button>
                  {#if dep.spec?.rollbackOf !== undefined}
                    <Badge variant="outline" class="ml-2">回滚型</Badge>
                  {/if}
                </Table.Cell>
                <Table.Cell>{deploymentPhaseLabel(dep)}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">
                  {progress.succeeded}/{progress.total} 成功（{progress.succeededPercent}%）
                </Table.Cell>
                <Table.Cell class="text-xs">
                  {#if unusual.length === 0}
                    <span class="text-muted-foreground">—</span>
                  {:else}
                    {#each unusual as t (t.machine)}
                      <div>
                        <span class="font-medium">{t.machine}</span>：{t.phaseLabel}
                        {t.reason ? `（${t.reason}）` : ''}
                      </div>
                    {/each}
                  {/if}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  <Card>
    <CardHeader>
      <CardTitle class="text-sm">最近失败/带修饰的操作</CardTitle>
      <p class="text-xs text-muted-foreground">
        契约只提供逐机 operations（无全局端点），本表由逐机聚合得出（并发 6，10 秒内不重复取）。
      </p>
    </CardHeader>
    <CardContent>
      <AsyncState empty={failedOps.length === 0 ? '没有失败或带修饰的操作。' : undefined} />
      {#if failedOps.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>操作</Table.Head>
              <Table.Head>机器</Table.Head>
              <Table.Head>类型/来源</Table.Head>
              <Table.Head>相位</Table.Head>
              <Table.Head>修饰</Table.Head>
              <Table.Head>时间</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each failedOps as op (op.id)}
              <Table.Row>
                <Table.Cell class="font-mono text-xs">{shortDigest(op.id, 6)}</Table.Cell>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/machines/${op.machine}`)}>
                    {op.machine}
                  </button>
                </Table.Cell>
                <Table.Cell class="text-xs">
                  {op.typeLabel} · {op.source === 'automatic' ? '自动' : '手动'}
                </Table.Cell>
                <Table.Cell><StateBadge tone={op.tone} label={op.phaseLabel} /></Table.Cell>
                <Table.Cell class="text-xs">
                  {op.terminalModifier ?? '—'}
                </Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">
                  {formatRelativeTime(op.finishedAt ?? op.createdAt ?? '', clock.now)}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>
</div>
