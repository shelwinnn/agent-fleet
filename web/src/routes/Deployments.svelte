<script lang="ts">
  /**
   * Deployments：canary/batch 进度与逐机结果；动作 Rollback / 跳过目标。
   *
   * 目标 `Superseded`（机器已在更新代）与 `Skipped`（操作者显式跳过）必须与
   * `Failed` 分开显示，且各自带原因；跳过的目标不计入成功。
   * 可用性如实标注：暂停/恢复尚未提供（按钮禁用并写明原因）。
   *
   * 全部文案经 i18n 取值；行级视图按 i18n.locale 重建，切换语言立即跟随。
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
  import { actionOutcomeTitle, loadErrorText, failureText } from '$lib/state/ssh.js';
  import { t, i18n, formatNumber, type MessageKey, type MessageParams } from '$lib/i18n/index.js';

  interface Props {
    store: FleetStore;
    name: string | null;
  }
  let { store, name }: Props = $props();

  const clock = createClock(15_000);
  const loc = $derived(i18n.locale);
  const selected = $derived(store.deployments.find((d) => d.metadata.name === name) ?? null);
  const loadError = $derived(
    store.errors.deployments !== undefined
      ? `${t('deployments.error.load')}${loadErrorText(store.errors.deployments, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );
  let skipReason = $state('');

  /** 动作反馈：成功存 key+params（渲染期翻译）；失败存原始错误（渲染期本地化）。 */
  type Message =
    | { kind: 'ok'; titleKey: MessageKey; key: MessageKey; params: MessageParams; detailKey?: MessageKey }
    | { kind: 'error'; prefixKey?: MessageKey; error: unknown };
  let message = $state<Message | null>(null);
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
      if (!skipReason.trim()) {
        message = { kind: 'error', prefixKey: 'detail.skip.reasonRequired', error: null };
        return;
      }
      await store.client.skipDeploymentTarget(deployment, machine, skipReason.trim());
      message = {
        kind: 'ok',
        titleKey: 'ok.skipped',
        key: 'deployments.skip.ok',
        params: { machine },
        detailKey: 'deployments.skip.hint',
      };
      skipReason = '';
      await store.afterMutation('deployments');
    } catch (err) {
      message = { kind: 'error', error: err };
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
        titleKey: 'ok.rolloutCreated',
        key: 'deployments.rollback.ok',
        params: { name: created.metadata.name, gen: created.spec?.targetGeneration ?? '?' },
        detailKey: 'deployments.rollback.hint',
      };
      await store.afterMutation('deployments');
    } catch (err) {
      message = { kind: 'error', error: err };
    } finally {
      busy = null;
    }
  }
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">{t('nav.deployments')}</h1>
    <p class="text-xs text-muted-foreground">{t('deployments.subtitle')}</p>
  </div>

  {#if message}
    <Alert.Root variant={message.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{message.kind === 'error' ? (message.prefixKey ? t('common.actionNotPerformed') : actionOutcomeTitle(message.error, i18n.locale)) : t(message.titleKey)}</Alert.Title>
      <Alert.Description>
        {#if message.kind === 'error'}
          {#if message.prefixKey}{t(message.prefixKey)}{:else}{failureText(message.error, i18n.locale)}{/if}
        {:else}
          {t(message.key, message.params)}
          {#if message.detailKey}<div class="mt-1">{t(message.detailKey)}</div>{/if}
        {/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  <Card>
    <CardHeader><CardTitle class="text-sm">{t('deployments.all', { n: formatNumber(store.deployments.length, loc) })}</CardTitle></CardHeader>
    <CardContent>
      <AsyncState loading={store.loading.deployments} error={loadError} empty={store.deployments.length === 0 ? t('deployments.empty') : undefined} />
      {#if store.deployments.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('deployments.col.name')}</Table.Head><Table.Head>{t('deployments.col.phase')}</Table.Head><Table.Head>{t('deployments.col.targetGen')}</Table.Head>
              <Table.Head>{t('deployments.col.strategy')}</Table.Head><Table.Head>{t('deployments.col.progress')}</Table.Head><Table.Head>{t('deployments.col.unusual')}</Table.Head>
              <Table.Head>{t('deployments.col.actions')}</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each store.deployments as dep (dep.metadata.name)}
              {@const progress = deploymentProgress(dep)}
              {@const unusual = (dep.status?.targets ?? []).map((tgt) => viewTarget(tgt, clock.now, loc)).filter((tgt) => tgt.phase !== 'Succeeded')}
              <Table.Row class={dep.metadata.name === name ? 'bg-muted/60' : ''}>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/deployments/${dep.metadata.name}`)}>{dep.metadata.name}</button>
                  {#if dep.spec?.rollbackOf !== undefined}<div class="text-xs text-muted-foreground">{t('deployments.badge.rollback')}</div>{/if}
                </Table.Cell>
                <Table.Cell class="text-xs">{deploymentPhaseLabel(dep, loc)}</Table.Cell>
                <Table.Cell class="text-xs">gen{dep.spec?.targetGeneration ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">{strategyLabel(dep, loc)}</Table.Cell>
                <Table.Cell class="text-xs">
                  <div class="h-2 w-28 overflow-hidden rounded bg-muted">
                    <div class="h-full bg-primary" style={`width:${progress.succeededPercent}%`}></div>
                  </div>
                  <div class="mt-1 text-muted-foreground">
                    {t('deployments.progress', {
                      done: formatNumber(progress.succeeded, loc),
                      total: formatNumber(progress.total, loc),
                      failed: formatNumber(progress.failed, loc),
                      superseded: formatNumber(progress.superseded, loc),
                      skipped: formatNumber(progress.skipped, loc),
                      blocked: formatNumber(progress.blocked, loc),
                    })}
                  </div>
                </Table.Cell>
                <Table.Cell class="text-xs">
                  {#if unusual.length === 0}—{:else}
                    {#each unusual.slice(0, 3) as tgt (tgt.machine)}
                      <div>{tgt.machine}: {tgt.phaseLabel}</div>
                    {/each}
                    {#if unusual.length > 3}<div class="text-muted-foreground">{t('deployments.more', { n: formatNumber(unusual.length, loc) })}</div>{/if}
                  {/if}
                </Table.Cell>
                <Table.Cell>
                  <div class="flex flex-wrap gap-1">
                    <Button size="sm" variant="outline" disabled title={t('deployments.pause.title')}>{t('deployments.pause')}</Button>
                    <Button size="sm" variant="outline" disabled title={t('deployments.resume.title')}>{t('deployments.resume')}</Button>
                    <Button size="sm" variant="ghost" disabled={busy !== null} onclick={() => rollback(dep.metadata.name)}>{t('detail.rollback.button')}</Button>
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
        <CardTitle class="text-sm">{selected.metadata.name} · {t('deployments.detailTitle')}</CardTitle>
        <p class="text-xs text-muted-foreground">
          {deploymentPhaseLabel(selected, loc)} · {strategyLabel(selected, loc)}
          {#if rollbackLabel(selected, loc)}<span class="ml-2">{rollbackLabel(selected, loc)}</span>{/if}
        </p>
      </CardHeader>
      <CardContent class="grid gap-4">
        <div class="grid grid-cols-2 gap-3 text-xs md:grid-cols-4">
          <div><span class="text-muted-foreground">{t('deployments.stat.total')}: </span>{formatNumber(progress.total, loc)}</div>
          <div><span class="text-muted-foreground">{t('deployments.stat.succeeded')}: </span>{formatNumber(progress.succeeded, loc)}</div>
          <div><span class="text-muted-foreground">{t('deployments.stat.failed')}: </span>{formatNumber(progress.failed, loc)}</div>
          <div><span class="text-muted-foreground">{t('deployments.stat.modified')}: </span>{formatNumber(progress.superseded, loc)}/{formatNumber(progress.skipped, loc)}</div>
        </div>
        {#if selected.status?.reason}
          <div class="text-xs">{t('deployments.levelReason')}: <span class="font-mono">{selected.status.reason}</span></div>
        {/if}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('deployments.col.machine')}</Table.Head><Table.Head>{t('deployments.col.result')}</Table.Head><Table.Head>{t('deployments.col.reason')}</Table.Head>
              <Table.Head>{t('deployments.col.effectiveGen')}</Table.Head><Table.Head>{t('deployments.col.operation')}</Table.Head><Table.Head>{t('deployments.col.updated')}</Table.Head>
              <Table.Head>{t('deployments.col.disposition')}</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each (selected.status?.targets ?? []).map((tgt) => viewTarget(tgt, clock.now, loc)) as target (target.machine)}
              <Table.Row>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/machines/${target.machine}`)}>{target.machine}</button>
                </Table.Cell>
                <Table.Cell>
                  <StateBadge
                    tone={target.tone}
                    label={target.phaseLabel}
                    title={target.phase === 'Skipped' || target.phase === 'Superseded' ? t('deployments.notFailureTitle') : ''}
                  />
                </Table.Cell>
                <Table.Cell class="max-w-[360px] text-xs text-muted-foreground">{target.reasonText ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs">gen{target.effectiveGeneration ?? '—'}</Table.Cell>
                <Table.Cell class="font-mono text-xs">{shortDigest(target.operationId, 6)}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">{target.updatedAtText ?? '—'}</Table.Cell>
                <Table.Cell>
                  {#if target.phase === 'Blocked' || target.phase === 'Pending' || target.phase === 'Running'}
                    <div class="flex flex-col gap-1">
                      <Input class="h-7 w-40 text-xs" placeholder={t('detail.skip.placeholder')} bind:value={skipReason} aria-label={t('detail.skip.placeholder')} />
                      <Button size="sm" variant="ghost" disabled={busy !== null || skipReason.trim() === ''} onclick={() => skipTarget(selected.metadata.name, target.machine)}>
                        {t('deployments.skip.button')}
                      </Button>
                    </div>
                  {:else}—{/if}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
        <p class="text-xs text-muted-foreground">{t('deployments.footer')}</p>
      </CardContent>
    </Card>
  {/if}
</div>
