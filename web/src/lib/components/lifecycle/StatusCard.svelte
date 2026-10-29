<script lang="ts">
  /**
   * 期望状态与偏差卡（spec User Story 15：与上游更新分开展示，各有解释）。
   *
   * 期望取自 lifecycle-desired（从未纳管是显式状态）；实际来自实例发现。
   * 偏差判定不把"版本未知"当一致、不把未纳管当缺席。
   */
  import type { LifecycleDesiredDocument, LifecycleEntry } from '$lib/api/types.js';
  import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '$lib/components/ui/card/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { deviationView, desiredEntryFor } from '$lib/state/lifecycle.js';
  import { failureText } from '$lib/state/ssh.js';
  import { formatRelativeTime } from '$lib/state/format.js';
  import { t, i18n } from '$lib/i18n/index.js';

  interface Props {
    doc: LifecycleDesiredDocument | null | undefined;
    loading?: boolean;
    error?: unknown;
    /** 实际安装情况（实例发现；present=false 表示尚未安装）。 */
    installed: { present: boolean; version?: string; versionError?: string };
    observedAt?: string;
  }
  let { doc, loading = false, error, installed, observedAt }: Props = $props();

  const loc = $derived(i18n.locale);
  const entry: LifecycleEntry | undefined = $derived(desiredEntryFor(doc));
  const intentText = $derived.by(() => {
    if (!entry) return t('lc.intent.none');
    if (entry.intent === 'present') return t('lc.intent.present', { version: entry.version ?? '—', method: entry.method ?? '—' });
    if (entry.intent === 'absent') return t('lc.intent.absent');
    return t('lc.intent.unmanaged');
  });
  const intentDetail = $derived(!entry ? t('lc.intent.noneDetail') : undefined);
  const deviation = $derived(deviationView(entry, installed, loc));
  const loadError = $derived(
    error !== undefined ? `${t('lc.intent.loadFailed')}${failureText(error, loc)}` : undefined,
  );
</script>

<Card>
  <CardHeader>
    <CardTitle class="text-sm">{t('lc.intent.title')}</CardTitle>
    <CardDescription>{t('lc.intent.explain')}</CardDescription>
  </CardHeader>
  <CardContent class="grid gap-2 text-sm">
    <AsyncState {loading} error={loadError} rows={2} />
    {#if !loading && error === undefined}
      <div class="flex flex-wrap items-center gap-2">
        <span class="text-xs text-muted-foreground">{t('lc.title')}：</span>
        <span class="font-medium">{intentText}</span>
      </div>
      {#if intentDetail}
        <p class="text-xs text-muted-foreground">{intentDetail}</p>
      {/if}
      <div class="flex flex-wrap items-center gap-2">
        <StateBadge
          tone={deviation.level === 'ok' ? 'ok' : deviation.level === 'deviate' ? 'warn' : 'muted'}
          label={deviation.level === 'ok' ? t('lc.deviation.level.ok') : deviation.level === 'deviate' ? t('lc.deviation.level.deviate') : '—'}
        />
        <span>{deviation.text}</span>
      </div>
      {#if deviation.detail}
        <p class="text-xs text-muted-foreground">{deviation.detail}</p>
      {/if}
      {#if observedAt}
        <p class="text-xs text-muted-foreground">{t('lc.instance.observedAt', { time: formatRelativeTime(observedAt, new Date(), loc) })}</p>
      {/if}
    {/if}
  </CardContent>
</Card>
