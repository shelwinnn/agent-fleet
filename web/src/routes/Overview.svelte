<script lang="ts">
  /**
   * Overview（spec §24.1）：卡片 = 总机器数 / daemon 在线 / SSH 可达 / 漂移 /
   * 降级 / 活跃发布；表 = 需关注机器、最近发布、最近失败操作。
   *
   * 卡片与"需关注"判定用同一套三态视图（lib/state/machines.ts），
   * 因此不存在"卡片说漂移、表格说一致"的矛盾。
   *
   * 全部文案经 i18n 取值；行级视图（machineRows/viewOperations/viewTarget）
   * 按 i18n.locale 重建，切换语言后动态内容立即跟随。
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
  import { t, i18n, formatNumber } from '$lib/i18n/index.js';
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

  const loc = $derived(i18n.locale);
  const rows = $derived(machineRows(store.machines, clock.now, loc));
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
    viewOperations(store.recentOperations, clock.now, loc)
      .filter((op) => op.phase === 'Failed' || (op.phase === 'Succeeded' && op.terminalModifier))
      .slice(0, 8),
  );
  const cards = $derived([
    { title: t('overview.card.totalMachines'), value: stats.totalMachines, hint: '' },
    { title: t('overview.card.daemonOnline'), value: stats.daemonOnline, hint: 'AgentConnected=True' },
    { title: t('overview.card.sshReachable'), value: stats.sshReachable, hint: 'SSHReachable=True' },
    {
      title: t('overview.card.drifted'),
      value: stats.drifted,
      hint: stats.unknownDrift > 0 ? t('overview.card.driftHintMore', { n: formatNumber(stats.unknownDrift, loc) }) : 'Drifted=True',
    },
    { title: t('overview.card.degraded'), value: stats.degraded, hint: 'Degraded=True' },
    {
      title: t('overview.card.activeDeployments'),
      value: stats.activeDeployments,
      hint: 'Pending/Canary/RollingOut/Paused',
    },
  ]);
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">{t('overview.title')}</h1>
    <p class="text-xs text-muted-foreground">{t('overview.subtitle')}</p>
  </div>

  <div class="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
    {#each cards as card (card.title)}
      <Card>
        <CardHeader class="pb-2">
          <CardTitle class="text-xs font-medium text-muted-foreground">{card.title}</CardTitle>
        </CardHeader>
        <CardContent>
          <div class="text-2xl font-semibold tabular-nums">{formatNumber(card.value, loc)}</div>
          {#if card.hint}<div class="mt-1 text-xs text-muted-foreground">{card.hint}</div>{/if}
        </CardContent>
      </Card>
    {/each}
  </div>

  <Card>
    <CardHeader><CardTitle class="text-sm">{t('overview.attention.title')}</CardTitle></CardHeader>
    <CardContent>
      <AsyncState
        loading={store.loading.machines}
        error={store.errors.machines}
        empty={attention.length === 0 ? t('overview.attention.empty') : undefined}
      />
      {#if attention.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('overview.table.name')}</Table.Head>
              <Table.Head>{t('overview.table.drift')}</Table.Head>
              <Table.Head>{t('overview.table.reconciled')}</Table.Head>
              <Table.Head>{t('overview.table.agentd')}</Table.Head>
              <Table.Head>{t('overview.table.lastSeen')}</Table.Head>
              <Table.Head>{t('overview.table.reason')}</Table.Head>
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
                  {row.attentionReasons.join(t('app.listSeparator'))}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  <Card>
    <CardHeader><CardTitle class="text-sm">{t('overview.deployments.title')}</CardTitle></CardHeader>
    <CardContent>
      <AsyncState
        loading={store.loading.deployments}
        error={store.errors.deployments}
        empty={recentDeployments.length === 0 ? t('overview.deployments.empty') : undefined}
      />
      {#if recentDeployments.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('overview.table.name')}</Table.Head>
              <Table.Head>{t('overview.table.phase')}</Table.Head>
              <Table.Head>{t('overview.table.progress')}</Table.Head>
              <Table.Head>{t('overview.table.unusualTargets')}</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each recentDeployments as dep (dep.metadata.name)}
              {@const progress = deploymentProgress(dep)}
              {@const unusual = (dep.status?.targets ?? [])
                .map((tgt) => viewTarget(tgt, clock.now, loc))
                .filter((tgt) => tgt.phase === 'Superseded' || tgt.phase === 'Skipped' || tgt.phase === 'Failed')}
              <Table.Row>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/deployments/${dep.metadata.name}`)}>
                    {dep.metadata.name}
                  </button>
                  {#if dep.spec?.rollbackOf !== undefined}
                    <Badge variant="outline" class="ml-2">{t('overview.badge.rollback')}</Badge>
                  {/if}
                </Table.Cell>
                <Table.Cell>{deploymentPhaseLabel(dep, loc)}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">
                  {t('overview.progress', {
                    done: formatNumber(progress.succeeded, loc),
                    total: formatNumber(progress.total, loc),
                    pct: formatNumber(progress.succeededPercent, loc),
                  })}
                </Table.Cell>
                <Table.Cell class="text-xs">
                  {#if unusual.length === 0}
                    <span class="text-muted-foreground">—</span>
                  {:else}
                    {#each unusual as tgt (tgt.machine)}
                      <div>
                        {t('overview.unusualTarget', {
                          machine: tgt.machine,
                          phase: tgt.phaseLabel,
                          reason: tgt.reasonText ?? '',
                        })}
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
      <CardTitle class="text-sm">{t('overview.ops.title')}</CardTitle>
      <p class="text-xs text-muted-foreground">{t('overview.ops.subtitle')}</p>
    </CardHeader>
    <CardContent>
      <AsyncState empty={failedOps.length === 0 ? t('overview.ops.empty') : undefined} />
      {#if failedOps.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('overview.table.operation')}</Table.Head>
              <Table.Head>{t('overview.table.machine')}</Table.Head>
              <Table.Head>{t('overview.table.typeSource')}</Table.Head>
              <Table.Head>{t('overview.table.phaseOp')}</Table.Head>
              <Table.Head>{t('overview.table.modifier')}</Table.Head>
              <Table.Head>{t('overview.table.time')}</Table.Head>
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
                  {op.typeLabel} · {op.source === 'automatic' ? t('ops.source.automatic') : t('ops.source.manual')}
                </Table.Cell>
                <Table.Cell><StateBadge tone={op.tone} label={op.phaseLabel} /></Table.Cell>
                <Table.Cell class="text-xs">
                  {op.terminalModifier ?? '—'}
                </Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">
                  {formatRelativeTime(op.finishedAt ?? op.createdAt ?? '', clock.now, loc)}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>
</div>
