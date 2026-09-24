<script lang="ts">
  /** 空态/加载/错误三件套：页面统一使用，避免各处自行发明文案。 */
  import { Skeleton } from '$lib/components/ui/skeleton/index.js';

  interface Props {
    loading?: boolean;
    error?: string;
    empty?: string;
    rows?: number;
  }
  let { loading = false, error, empty, rows = 3 }: Props = $props();
</script>

{#if loading}
  <div class="grid gap-2" aria-busy="true">
    {#each Array(rows) as _, i (i)}
      <Skeleton class="h-8 w-full" />
    {/each}
  </div>
{:else if error}
  <div class="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
    {error}
  </div>
{:else if empty}
  <div class="rounded-md border border-dashed p-4 text-sm text-muted-foreground">{empty}</div>
{/if}
