<script lang="ts">
  /**
   * 预览确认卡（spec User Stories 9/17/20 与全局红线）：
   *  - 展示目标、固定版本、来源与影响，含数据保护范围（"只动程序本体"）；
   *  - 影响（目标/版本）变化时"我已阅读影响"重置，必须重新确认；
   *  - 这是唯一通向变更的路径：确认按钮之外没有任何提交入口。
   */
  import type { LifecycleCapability, LifecycleTarget } from '$lib/api/types.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import CapabilityBadge from './CapabilityBadge.svelte';
  import { envLabel, targetLabel } from '$lib/state/lifecycle.js';
  import { t, i18n } from '$lib/i18n/index.js';

  interface Props {
    kind: 'install' | 'upgrade' | 'uninstall';
    targets: LifecycleTarget[];
    version: string;
    method: string;
    pkg: string;
    capability?: LifecycleCapability;
    machineOffline: boolean;
    /** 确认后是否同时更新期望状态（present/absent）；unmanaged 不走本卡。 */
    desiredIntent: 'present' | 'absent';
    fingerprint: string;
    submitting: boolean;
    onconfirm: (includeDesired: boolean) => void;
    oncancel: () => void;
  }
  let {
    kind,
    targets,
    version,
    method,
    pkg,
    capability,
    machineOffline,
    desiredIntent,
    fingerprint,
    submitting,
    onconfirm,
    oncancel,
  }: Props = $props();

  const loc = $derived(i18n.locale);
  let includeDesired = $state(true);
  let acknowledged = $state('');
  let lastAckFingerprint = $state('');

  // 影响变化（指纹不同）→ 已读作废，必须重新确认（spec：影响变化时要求重新确认）。
  const impactChanged = $derived(acknowledged !== '' && lastAckFingerprint !== fingerprint);
  const canConfirm = $derived(acknowledged !== '' && !impactChanged && !submitting && targets.length > 0);

  function toggleAcknowledged(checked: boolean) {
    acknowledged = checked ? 'ack' : '';
    lastAckFingerprint = checked ? fingerprint : '';
  }

  const title = $derived(
    kind === 'install' ? t('lc.preview.title.install') : kind === 'upgrade' ? t('lc.preview.title.upgrade') : t('lc.preview.title.uninstall'),
  );
  const actionName = $derived(t(`lc.optype.${kind === 'install' ? 'Install' : kind === 'upgrade' ? 'Upgrade' : 'Uninstall'}`));
  const whatText = $derived(
    version
      ? t('lc.preview.createOp', { type: actionName, version, method, pkg })
      : t('lc.preview.createOpNoVersion', { type: actionName, method, pkg }),
  );
</script>

<Card>
  <CardHeader>
    <CardTitle class="text-sm">{title}</CardTitle>
  </CardHeader>
  <CardContent class="grid gap-3 text-sm">
    <div>
      <div class="text-xs font-medium text-muted-foreground">{t('lc.preview.whatWillHappen')}</div>
      <p class="mt-1">{whatText}</p>
    </div>

    <div>
      <div class="text-xs font-medium text-muted-foreground">{t('lc.preview.targets')}</div>
      <ul class="mt-1 grid gap-1">
        {#each targets as tgt, i (i)}
          <li class="font-mono text-xs">{targetLabel(tgt, loc)}{tgt.env ? ` (${envLabel(tgt.env, loc)})` : ''}</li>
        {/each}
      </ul>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <span class="text-xs font-medium text-muted-foreground">{t('lc.preview.impact')}</span>
      <CapabilityBadge {capability} />
      {#if capability?.state === 'unverified'}
        <span class="text-xs text-amber-700 dark:text-amber-300">{t('lc.preview.unverifiedInline')}</span>
      {/if}
    </div>

    <div class="rounded-md border p-3">
      <div class="text-xs font-medium">{t('lc.preview.dataProtection')}</div>
      <p class="mt-1 text-xs text-muted-foreground">{t('lc.preview.dataProtectionDetail')}</p>
    </div>

    {#if machineOffline}
      <Alert.Root>
        <Alert.Title>{t('lc.machine.offline')}</Alert.Title>
        <Alert.Description>{t('lc.action.targetOfflineNote')}</Alert.Description>
      </Alert.Root>
    {/if}

    <div class="flex items-center gap-2">
      <input
        type="checkbox"
        id="lc-desired-{kind}"
        class="size-4 accent-primary"
        checked={includeDesired}
        onchange={(e) => (includeDesired = e.currentTarget.checked)}
      />
      <Label for="lc-desired-{kind}" class="text-xs">
        {t('lc.preview.desiredAlso', { intent: t(`lc.intentName.${desiredIntent}`) })}
      </Label>
    </div>

    <div class="flex items-center gap-2">
      <input
        type="checkbox"
        id="lc-ack-{kind}"
        class="size-4 accent-primary"
        checked={acknowledged !== '' && !impactChanged}
        onchange={(e) => toggleAcknowledged(e.currentTarget.checked)}
        aria-describedby={impactChanged ? 'lc-impact-changed' : undefined}
      />
      <Label for="lc-ack-{kind}" class="text-xs">{t('lc.preview.ack')}</Label>
    </div>
    {#if impactChanged}
      <p id="lc-impact-changed" class="text-xs font-medium text-amber-700 dark:text-amber-300" role="alert">
        {t('lc.preview.impactChanged')}
      </p>
    {/if}

    {#if submitting}
      <p class="text-xs text-muted-foreground" role="status">{t('lc.preview.submitting')}</p>
    {/if}

    <div class="flex gap-2">
      <Button size="sm" disabled={!canConfirm} onclick={() => onconfirm(includeDesired)}>
        {t('lc.preview.confirm')}
      </Button>
      <Button size="sm" variant="ghost" onclick={oncancel} disabled={submitting}>{t('common.cancel')}</Button>
    </div>
  </CardContent>
</Card>
