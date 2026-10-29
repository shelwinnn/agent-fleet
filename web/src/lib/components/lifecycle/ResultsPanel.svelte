<script lang="ts">
  /**
   * 操作结果面板（spec User Stories 25/27/29/30/32）：
   *  - 逐目标呈现成功/失败/未执行/未知 + 原因 + 下一步；未知永不与成功同观感；
   *  - 仅确定失败目标可重试，重试沿用原操作并关联（retryOf），不重复确认；
   *  - needsReconfirmation 如实呈现：执行已暂停，先核对再决定。
   */
  import type { LifecycleOperation, LifecycleTarget } from '$lib/api/types.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import {
    failedResults,
    formatOpTitle,
    isOperationSettled,
    needsReconfirmationView,
    operationProgress,
    targetResultViews,
  } from '$lib/state/lifecycle.js';
  import { formatRelativeTime, formatTime } from '$lib/state/format.js';
  import { t, i18n } from '$lib/i18n/index.js';

  interface Props {
    operations: LifecycleOperation[];
    retryingOpId?: string | null;
    lookupError?: string | null;
    message?: string | null;
    onretry: (op: LifecycleOperation, targets: LifecycleTarget[]) => void;
    onlookup: (id: string) => void;
  }
  let { operations, retryingOpId = null, lookupError = null, message = null, onretry, onlookup }: Props = $props();

  const loc = $derived(i18n.locale);
  let lookupId = $state('');
</script>

<Card>
  <CardHeader>
    <CardTitle class="text-sm">{t('lc.results.title')}</CardTitle>
  </CardHeader>
  <CardContent class="grid gap-4 text-sm">
    <p class="text-xs text-muted-foreground">{t('lc.results.scope')}</p>

    <div class="flex flex-wrap items-end gap-2">
      <div class="grid gap-1">
        <Label for="lc-lookup" class="text-xs">{t('lc.results.openByIdLabel')}</Label>
        <Input
          id="lc-lookup"
          class="w-64"
          placeholder="lc-op-001"
          bind:value={lookupId}
          onchange={() => lookupId && onlookup(lookupId.trim())}
        />
      </div>
      <Button size="sm" variant="outline" disabled={!lookupId.trim()} onclick={() => onlookup(lookupId.trim())}>
        {t('lc.results.open')}
      </Button>
    </div>
    {#if lookupError}
      <Alert.Root variant="destructive">
        <Alert.Title>{t('lc.results.opNotFound')}</Alert.Title>
        <Alert.Description>{lookupError}</Alert.Description>
      </Alert.Root>
    {/if}

    {#if message}
      <p class="text-xs text-muted-foreground" role="status">{message}</p>
    {/if}

    {#if operations.length === 0}
      <div class="rounded-md border border-dashed p-4 text-muted-foreground" role="status">{t('lc.results.none')}</div>
    {/if}

    {#each operations as op (op.metadata.name)}
      {@const progress = operationProgress(op)}
      {@const failed = failedResults(op)}
      {@const reconfirm = needsReconfirmationView(op)}
      <div class="rounded-md border p-3" data-testid="lc-op">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-medium">{formatOpTitle(op, loc)}</span>
          {#if op.retryOf}
            <span class="text-xs text-muted-foreground">{t('lc.results.retryOf', { id: op.retryOf })}</span>
          {/if}
          {#if op.status.confirmedAt}
            <span class="text-xs text-muted-foreground">
              {t('lc.results.confirmedAt', { time: formatTime(op.status.confirmedAt, loc) })}
            </span>
          {/if}
        </div>
        {#if progress.total > 0}
          <p class="mt-1 text-xs text-muted-foreground">{t('lc.results.progress', { ...progress })}</p>
        {/if}

        {#if reconfirm}
          <Alert.Root class="mt-2">
            <Alert.Title>{t('lc.results.needsReconfirmation')}</Alert.Title>
            <Alert.Description>{reconfirm.text}</Alert.Description>
          </Alert.Root>
        {/if}

        <div class="mt-2">
          <Table.Root>
            <Table.Header>
              <Table.Row>
                <Table.Head>{t('lc.results.col.target')}</Table.Head>
                <Table.Head>{t('lc.results.col.state')}</Table.Head>
                <Table.Head>{t('lc.results.col.reason')}</Table.Head>
                <Table.Head>{t('lc.results.col.next')}</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#each targetResultViews(op, loc) as view, i (i)}
                <Table.Row>
                  <Table.Cell class="font-mono text-xs">
                    {view.targetText}
                    {#if view.path}
                      <div class="text-muted-foreground">{view.path}</div>
                    {/if}
                  </Table.Cell>
                  <Table.Cell>
                    <div class="flex flex-col items-start gap-1">
                      <StateBadge tone={view.tone} label={view.stateText} />
                      {#if view.observedVersion !== undefined}
                        <span class="text-xs text-muted-foreground">
                          {view.observedVersion === ''
                            ? t('lc.results.observedAbsent')
                            : t('lc.results.observed', { version: view.observedVersion })}
                        </span>
                      {/if}
                      {#if view.message}
                        <span class="text-xs text-muted-foreground">{view.message}</span>
                      {/if}
                    </div>
                  </Table.Cell>
                  <Table.Cell class="text-xs">{view.reasonText ?? '—'}</Table.Cell>
                  <Table.Cell class="text-xs">{view.nextStep}</Table.Cell>
                </Table.Row>
              {/each}
            </Table.Body>
          </Table.Root>
        </div>

        <div class="mt-2 flex flex-wrap items-center gap-2">
          {#if !isOperationSettled(op)}
            <span class="text-xs text-muted-foreground" role="status">{t('lc.next.running')}</span>
          {:else if failed.length > 0}
            <Button
              size="sm"
              variant="outline"
              disabled={retryingOpId === op.metadata.name}
              onclick={() => onretry(op, failed.map((f) => f.target as LifecycleTarget))}
            >
              {retryingOpId === op.metadata.name ? t('lc.results.retrySubmitting') : t('lc.results.retry')}
            </Button>
            <span class="text-xs text-muted-foreground">
              {t('lc.results.retryExplain', { n: failed.length, id: op.metadata.name })}
            </span>
          {:else}
            <span class="text-xs text-muted-foreground">{t('lc.results.retryNone')}</span>
          {/if}
        </div>
      </div>
    {/each}
  </CardContent>
</Card>
