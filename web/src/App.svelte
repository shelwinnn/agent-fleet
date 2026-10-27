<script lang="ts">
  /**
   * 应用外壳：顶部状态条（实时连接/手动刷新/令牌/语言切换）+ 左侧导航 + 路由出口。
   * 无交互 shell、无会话代理（非目标 #2/#3；spec §24.3）。
   *
   * 文案全部经 i18n 的 t() 取值（读 i18n.locale 即响应式），切换语言当前页立即更新。
   */
  import { onMount } from 'svelte';
  import { fleetStore as store } from '$lib/state/fleet.svelte.js';
  import { initRouter, navigate, router } from '$lib/state/router.svelte.js';
  import { t, i18n, setLocale, formatNumber, type MessageKey, type Locale } from '$lib/i18n/index.js';
  import { formatRelativeTime } from '$lib/state/format.js';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import { Separator } from '$lib/components/ui/separator/index.js';
  import Overview from './routes/Overview.svelte';
  import Machines from './routes/Machines.svelte';
  import MachineDetail from './routes/MachineDetail.svelte';
  import Profiles from './routes/Profiles.svelte';
  import Skills from './routes/Skills.svelte';
  import Deployments from './routes/Deployments.svelte';
  import SshInventory from './routes/SshInventory.svelte';

  const NAV: { path: string; labelKey: MessageKey }[] = [
    { path: '/', labelKey: 'nav.overview' },
    { path: '/machines', labelKey: 'nav.machines' },
    { path: '/profiles', labelKey: 'nav.profiles' },
    { path: '/skills', labelKey: 'nav.skills' },
    { path: '/deployments', labelKey: 'nav.deployments' },
    { path: '/ssh-inventory', labelKey: 'nav.sshInventory' },
  ];

  const LANGUAGES: Locale[] = ['zh-CN', 'en'];

  let tokenInput = $state('');
  let showToken = $state(false);
  let refreshing = $state(false);

  onMount(() => {
    initRouter();
    store.start();
    return () => store.stop();
  });

  // 页面标题随语言更新（SSR 不执行 $effect，无副作用）。
  $effect(() => {
    if (typeof document !== 'undefined') document.title = t('app.title');
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
      open: t('app.stream.open'),
      connecting: t('app.stream.connecting'),
      reconnecting: t('app.stream.reconnecting'),
      closed: t('app.stream.closed'),
    }[store.streamState],
  );

  // sse 层只给语言中立的原因码；这里按当前语言渲染（切换语言立即跟随）。
  const streamDetailText = $derived.by(() => {
    const d = store.streamDetail;
    if (!d || store.streamState === 'open') return '';
    if (d.startsWith('http:')) return t('app.stream.reason.http', { status: d.slice('http:'.length) });
    if (d.startsWith('content-type:')) return t('app.stream.reason.contentType', { type: d.slice('content-type:'.length) });
    if (d === 'server-closed') return t('app.stream.reason.serverClosed');
    return t('app.stream.reason.network');
  });

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
        <div class="text-sm font-semibold tracking-tight">{t('app.title')}</div>
        <div class="text-xs text-muted-foreground">{t('app.subtitle')}</div>
      </div>

      <Badge class={streamTone}>{streamLabel}</Badge>
      {#if store.lastEventAt}
        <span class="text-xs text-muted-foreground">
          {t('app.lastEventAt', {
            time: formatRelativeTime(store.lastEventAt, new Date(), i18n.locale),
            count: formatNumber(store.eventsSeen, i18n.locale),
          })}
          {#if store.eventsCoalesced > 0}
            {t('app.eventsCoalesced', { count: formatNumber(store.eventsCoalesced, i18n.locale) })}
          {/if}
        </span>
      {:else}
        <span class="text-xs text-muted-foreground">{t('app.noEvents')}</span>
      {/if}
      {#if streamDetailText && store.streamState !== 'open'}
        <span class="text-xs text-muted-foreground">{streamDetailText}</span>
      {/if}

      <div class="ml-auto flex items-center gap-2">
        {#if store.lastRefreshAt}
          <span class="text-xs text-muted-foreground">
            {t('app.lastRefresh', { time: formatRelativeTime(store.lastRefreshAt, new Date(), i18n.locale) })}
          </span>
        {/if}
        <Button variant="outline" size="sm" onclick={refresh} disabled={refreshing}>
          {refreshing ? t('app.refreshing') : t('app.refresh')}
        </Button>
        <Button variant="ghost" size="sm" onclick={() => (showToken = !showToken)}>
          {t('app.manageToken')}
        </Button>
        <div class="flex items-center gap-1" role="group" aria-label={t('app.language')}>
          {#each LANGUAGES as lang (lang)}
            <Button
              variant={i18n.locale === lang ? 'secondary' : 'ghost'}
              size="sm"
              aria-pressed={i18n.locale === lang}
              lang={lang}
              onclick={() => setLocale(lang)}
            >
              {lang === 'zh-CN' ? '中文' : 'English'}
            </Button>
          {/each}
        </div>
      </div>
    </div>

    {#if showToken}
      <div class="mx-auto flex max-w-[1400px] flex-wrap items-end gap-2 px-4 pb-3">
        <div class="grid gap-1">
          <Label for="admin-token" class="text-xs">
            {t('app.tokenLabel')}
          </Label>
          <Input
            id="admin-token"
            type="password"
            class="w-72"
            placeholder={t('app.tokenPlaceholder')}
            bind:value={tokenInput}
          />
        </div>
        <Button size="sm" onclick={applyToken}>{t('app.applyToken')}</Button>
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
              {t(item.labelKey)}
            </button>
          </li>
        {/each}
      </ul>
      <Separator class="my-4" />
      <p class="px-3 text-xs leading-relaxed text-muted-foreground">
        {t('app.limitNotice')}
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
          {t('app.unknownRoute')} <code>{router.path}</code> <button class="underline" onclick={() => navigate('/')}
            >{t('app.backToOverview')}</button
          >
        </div>
      {/if}
    </main>
  </div>
</div>
