<script lang="ts">
  /**
   * 上游更新卡（spec User Stories 12–14）：手动检查一次，逐来源展示
   * 来源/渠道/可用版本/查询时间；查询失败展示失败原因与缓存时间——
   * 未知永远 ≠ 最新，绝不把失败渲染成"已是最新"。
   */
  import type { UpstreamRowView } from '$lib/state/lifecycle.js';
  import { Card, CardContent, CardHeader, CardTitle, CardDescription, CardAction } from '$lib/components/ui/card/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { failureText } from '$lib/state/ssh.js';
  import { formatRelativeTime } from '$lib/state/format.js';
  import { t, i18n } from '$lib/i18n/index.js';

  interface Props {
    state: 'idle' | 'loading' | 'ok' | 'error';
    rows: UpstreamRowView[];
    checkedAt?: string;
    error?: unknown;
    disabled?: boolean;
    oncheck: () => void;
  }
  let { state, rows, checkedAt, error, disabled = false, oncheck }: Props = $props();

  const loc = $derived(i18n.locale);
  const loadError = $derived(
    state === 'error' ? `${t('lc.upstream.loadFailed')}${failureText(error, loc)} ${t('error.loadRetryHint')}` : undefined,
  );
</script>

<Card>
  <CardHeader>
    <CardTitle class="text-sm">{t('lc.upstream.title')}</CardTitle>
    <CardDescription>{t('lc.upstream.explain')}</CardDescription>
    <CardAction class="flex items-center gap-2">
      {#if checkedAt}
        <span class="text-xs text-muted-foreground">
          {t('lc.upstream.checkedAt', { time: formatRelativeTime(checkedAt, new Date(), loc) })}
        </span>
      {/if}
      <Button size="sm" variant="outline" onclick={oncheck} disabled={disabled || state === 'loading'}>
        {state === 'loading' ? t('lc.upstream.checking') : t('lc.upstream.check')}
      </Button>
    </CardAction>
  </CardHeader>
  <CardContent class="grid gap-2 text-sm">
    {#if state === 'idle' && !error}
      <div class="rounded-md border border-dashed p-4 text-sm text-muted-foreground" role="status">
        {t('lc.upstream.check')} →
      </div>
    {/if}
    <AsyncState loading={state === 'loading'} error={loadError} rows={3} />
    {#if state === 'ok'}
      {#each rows as row, i (i)}
        <div class="rounded-md border p-3">
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-medium">{row.method}{row.pkg ? ` · ${row.pkg}` : ''}</span>
            <Badge variant="outline" class="text-xs">{t('lc.upstream.channel')}: {row.channel}</Badge>
            {#if row.kind === 'ok'}
              <span class="ml-auto text-xs text-muted-foreground">
                {t('lc.upstream.queriedAt', { time: formatRelativeTime(row.queriedAt, new Date(), loc) })}
              </span>
            {/if}
          </div>
          {#if row.kind === 'ok'}
            <div class="mt-1 flex flex-wrap items-center gap-2">
              <span>{t('lc.upstream.available')}：</span>
              <span class="font-mono font-medium">{row.version}</span>
              <Badge variant="outline" class="text-xs">
                {row.scheme === 'semver'
                  ? t('lc.upstream.scheme.semver')
                  : row.scheme === 'source_rule'
                    ? t('lc.upstream.scheme.source_rule')
                    : t('lc.upstream.scheme.unparsed')}
              </Badge>
            </div>
          {:else if row.kind === 'failed'}
            <div class="mt-1 grid gap-1">
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-destructive">{t('lc.upstream.failed')}：{row.reasonText}</span>
                {#if row.attempts !== undefined}
                  <span class="text-xs text-muted-foreground">{t('lc.upstream.attempts', { n: row.attempts })}</span>
                {/if}
              </div>
              {#if row.detail}
                <p class="text-xs text-muted-foreground">{row.detail}</p>
              {/if}
              {#if row.cacheVersion}
                <p class="text-xs text-muted-foreground">
                  {t('lc.upstream.failedWithCache', {
                    time: formatRelativeTime(row.cacheQueriedAt ?? '', new Date(), loc),
                    version: row.cacheVersion,
                  })}
                </p>
              {:else}
                <p class="text-xs font-medium text-amber-700 dark:text-amber-300">{t('lc.upstream.unknown')}</p>
                <p class="text-xs text-muted-foreground">{t('lc.upstream.unknownDetail')}</p>
              {/if}
            </div>
          {:else}
            <div class="mt-1 grid gap-1">
              <span class="text-muted-foreground">{t('lc.upstream.unsupported')}：{row.reasonText}</span>
              {#if row.detail}
                <p class="text-xs text-muted-foreground">{row.detail}</p>
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    {/if}
  </CardContent>
</Card>
