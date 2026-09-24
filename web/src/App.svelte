<script lang="ts">
  /**
   * 应用外壳：顶部状态条（实时连接/手动刷新/令牌）+ 左侧导航 + 路由出口。
   * 无交互 shell、无会话代理（非目标 #2/#3；spec §24.3）。
   */
  import { onMount } from 'svelte';
  import { fleetStore as store } from '$lib/state/fleet.svelte.js';
  import { initRouter, navigate, router } from '$lib/state/router.svelte.js';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import { Separator } from '$lib/components/ui/separator/index.js';
  import { formatRelativeTime } from '$lib/state/format.js';
  import Overview from './routes/Overview.svelte';
  import Machines from './routes/Machines.svelte';
  import MachineDetail from './routes/MachineDetail.svelte';
  import Profiles from './routes/Profiles.svelte';
  import Skills from './routes/Skills.svelte';
  import Deployments from './routes/Deployments.svelte';
  import SshInventory from './routes/SshInventory.svelte';

  const NAV = [
    { path: '/', label: 'Overview' },
    { path: '/machines', label: 'Machines' },
    { path: '/profiles', label: 'Profiles' },
    { path: '/skills', label: 'Skills' },
    { path: '/deployments', label: 'Deployments' },
    { path: '/ssh-inventory', label: 'SSH Inventory' },
  ];

  let tokenInput = $state('');
  let showToken = $state(false);
  let refreshing = $state(false);

  onMount(() => {
    initRouter();
    store.start();
    return () => store.stop();
  });

  const streamTone = $derived(
    store.streamState === 'open'
      ? 'tone-ok'
      : store.streamState === 'connecting'
        ? 'tone-info'
        : store.streamState === 'closed'
          ? 'tone-muted'
          : 'tone-warn',
  );

  const streamLabel = $derived(
    {
      open: 'SSE 已连接',
      connecting: 'SSE 连接中',
      reconnecting: 'SSE 重连中',
      closed: 'SSE 已关闭',
    }[store.streamState],
  );

  const isActive = (path: string) =>
    path === '/' ? router.path === '/' : router.path === path || router.path.startsWith(`${path}/`);

  async function refresh() {
    refreshing = true;
    await store.refreshAll();
    refreshing = false;
  }

  function applyToken() {
    store.setToken(tokenInput.trim());
    showToken = false;
  }
</script>

<div class="min-h-dvh bg-background text-foreground">
  <header class="sticky top-0 z-10 border-b bg-background/95 backdrop-blur">
    <div class="mx-auto flex max-w-[1400px] flex-wrap items-center gap-3 px-4 py-3">
      <div class="mr-2">
        <div class="text-sm font-semibold tracking-tight">agent-fleet 控制面</div>
        <div class="text-xs text-muted-foreground">
          单操作者机群管理 · 契约 api/openapi/fleet-v1.yaml
        </div>
      </div>

      <Badge class={streamTone}>{streamLabel}</Badge>
      {#if store.lastEventAt}
        <span class="text-xs text-muted-foreground">
          最近事件 {formatRelativeTime(store.lastEventAt)} · 已处理 {store.eventsSeen} 条
          {#if store.eventsCoalesced > 0}（去重 {store.eventsCoalesced}）{/if}
        </span>
      {:else}
        <span class="text-xs text-muted-foreground">尚未收到事件</span>
      {/if}
      {#if store.streamDetail && store.streamState !== 'open'}
        <span class="text-xs text-muted-foreground">{store.streamDetail}</span>
      {/if}

      <div class="ml-auto flex items-center gap-2">
        {#if store.lastRefreshAt}
          <span class="text-xs text-muted-foreground">
            数据刷新于 {formatRelativeTime(store.lastRefreshAt)}
          </span>
        {/if}
        <Button variant="outline" size="sm" onclick={refresh} disabled={refreshing}>
          {refreshing ? '刷新中…' : '全量刷新'}
        </Button>
        <Button variant="ghost" size="sm" onclick={() => (showToken = !showToken)}>
          管理令牌
        </Button>
      </div>
    </div>

    {#if showToken}
      <div class="mx-auto flex max-w-[1400px] flex-wrap items-end gap-2 px-4 pb-3">
        <div class="grid gap-1">
          <Label for="admin-token" class="text-xs">
            admin token（§29.14；仅本次会话内存，不写入任何存储）
          </Label>
          <Input
            id="admin-token"
            type="password"
            class="w-72"
            placeholder="回环监听且服务端未配置时可留空"
            bind:value={tokenInput}
          />
        </div>
        <Button size="sm" onclick={applyToken}>应用并重连事件流</Button>
      </div>
    {/if}
  </header>

  <div class="mx-auto flex max-w-[1400px] gap-6 px-4 py-5">
    <nav class="w-44 shrink-0">
      <ul class="grid gap-1">
        {#each NAV as item (item.path)}
          <li>
            <button
              class="w-full rounded-md px-3 py-2 text-left text-sm transition-colors {isActive(item.path)
                ? 'bg-secondary font-medium text-secondary-foreground'
                : 'text-muted-foreground hover:bg-muted'}"
              onclick={() => navigate(item.path)}
            >
              {item.label}
            </button>
          </li>
        {/each}
      </ul>
      <Separator class="my-4" />
      <p class="px-3 text-xs leading-relaxed text-muted-foreground">
        当前限制：无证书吊销（删除机器即拒绝其证书）。
      </p>
    </nav>

    <main class="min-w-0 flex-1">
      {#if router.path === '/'}
        <Overview {store} />
      {:else if router.segments[0] === 'machines' && router.segments[1]}
        <MachineDetail {store} name={decodeURIComponent(router.segments[1])} />
      {:else if router.segments[0] === 'machines'}
        <Machines {store} />
      {:else if router.segments[0] === 'profiles'}
        <Profiles {store} name={router.segments[1] ? decodeURIComponent(router.segments[1]) : null} />
      {:else if router.segments[0] === 'skills'}
        <Skills {store} />
      {:else if router.segments[0] === 'deployments'}
        <Deployments
          {store}
          name={router.segments[1] ? decodeURIComponent(router.segments[1]) : null}
        />
      {:else if router.segments[0] === 'ssh-inventory'}
        <SshInventory {store} />
      {:else}
        <div class="rounded-lg border p-6 text-sm">
          未知路由 <code>{router.path}</code>。<button class="underline" onclick={() => navigate('/')}
            >返回 Overview</button
          >
        </div>
      {/if}
    </main>
  </div>
</div>
