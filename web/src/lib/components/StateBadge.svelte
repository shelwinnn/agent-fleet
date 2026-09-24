<script lang="ts">
  /**
   * 语义状态标记（三态/相位/目标结果的统一呈现）。
   * `unknown` 永远走 warn 色，绝不与"一致/成功"共用外观（FR-14.5 / M5）。
   */
  import { Badge } from '$lib/components/ui/badge/index.js';
  import type { Tone } from '$lib/state/tone.js';

  interface Props {
    tone: Tone;
    label: string;
    title?: string;
  }
  let { tone, label, title }: Props = $props();

  const cls = $derived(
    {
      ok: 'tone-ok',
      warn: 'tone-warn',
      danger: 'tone-danger',
      muted: 'tone-muted',
      info: 'tone-info',
    }[tone],
  );
</script>

<span title={title} class="inline-flex">
  <Badge variant="outline" class="border {cls}">{label}</Badge>
</span>
