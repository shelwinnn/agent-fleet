<script lang="ts">
  /**
   * 能力矩阵状态徽标（KM-46 §7 的呈现）：supported / unverified / blocked 如实展示，
   * 未验证不标支持；能力未知（无记录）也如实说明。悬停可见判定原因与证据级别。
   */
  import type { LifecycleCapability } from '$lib/api/types.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import { capabilityTone } from '$lib/state/lifecycle.js';
  import { t } from '$lib/i18n/index.js';

  interface Props {
    capability?: LifecycleCapability;
  }
  let { capability }: Props = $props();

  const label = $derived(
    capability?.state === 'supported'
      ? t('lc.cap.supported')
      : capability?.state === 'unverified'
        ? t('lc.cap.unverified')
        : capability?.state === 'blocked'
          ? t('lc.cap.blocked')
          : t('lc.cap.unknown'),
  );
  const tone = $derived(capabilityTone(capability?.state));
  const title = $derived.by(() => {
    if (!capability) return t('lc.cap.unknown');
    const evidence = capability.evidenceLevel === 'isolated'
      ? t('lc.cap.evidence.isolated')
      : capability.evidenceLevel === 'real_machine'
        ? t('lc.cap.evidence.real_machine')
        : t('lc.cap.evidence.none');
    return `${capability.state}${evidence}${capability.reason ? ` · ${capability.reason}` : ''}`;
  });
</script>

<StateBadge {tone} {label} {title} />
