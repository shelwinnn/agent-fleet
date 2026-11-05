<script lang="ts">
  import { onMount } from 'svelte';
  import { fleetStore as store } from '$lib/state/fleet.svelte.js';
  import { initRouter, navigate, router } from '$lib/state/router.svelte.js';
  import { t, i18n, type MessageKey } from '$lib/i18n/index.js';
  import { lifecycleStateLabel, lifecycleStateTone } from '$lib/state/lifecycle.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import LogoMark from '$lib/components/shell/LogoMark.svelte';
  import Overview from './routes/Overview.svelte';
  import Machines from './routes/Machines.svelte';
  import MachineDetail from './routes/MachineDetail.svelte';
  import Profiles from './routes/Profiles.svelte';
  import Skills from './routes/Skills.svelte';
  import Deployments from './routes/Deployments.svelte';
  import SshInventory from './routes/SshInventory.svelte';
  import Lifecycle from './routes/Lifecycle.svelte';
  import Apps from './routes/Apps.svelte';
  import { RestLifecycleGateway } from '$lib/state/lifecycleGateway.js';

  type NavMark = 'overview' | 'devices' | 'apps' | 'skills';
  type NavLink = { path: string; labelKey: MessageKey; mark: NavMark };
  type NavGroup = { labelKey: MessageKey | null; items: NavLink[] };
  type Panel = 'scope' | 'help' | 'account';

  const NAV: NavGroup[] = [
    { labelKey: null, items: [{ path: '/', labelKey: 'nav.overview', mark: 'overview' }] },
    { labelKey: 'nav.resources', items: [{ path: '/machines', labelKey: 'nav.devices', mark: 'devices' }] },
    {
      labelKey: 'nav.software',
      items: [
        { path: '/apps', labelKey: 'nav.apps', mark: 'apps' },
        { path: '/skills', labelKey: 'nav.skills', mark: 'skills' },
      ],
    },
  ];

  const HELP_STATES = ['succeeded', 'failed', 'not_executed', 'unknown'] as const;

  let collapsed = $state(false);
  let panel = $state<Panel | null>(null);

  onMount(() => {
    initRouter();
    store.start();
    window.addEventListener('keydown', onWindowKeydown);
    window.addEventListener('hashchange', onHashChange);
    document.addEventListener('click', onDocumentClick);
    return () => {
      store.stop();
      window.removeEventListener('keydown', onWindowKeydown);
      window.removeEventListener('hashchange', onHashChange);
      document.removeEventListener('click', onDocumentClick);
    };
  });

  $effect(() => {
    if (typeof document !== 'undefined') document.title = t('app.title');
  });

  const isActive = (path: string) =>
    path === '/' ? router.path === '/' : router.path === path || router.path.startsWith(`${path}/`);

  function toggle(next: Panel) {
    panel = panel === next ? null : next;
  }

  function onWindowKeydown(event: KeyboardEvent) {
    if (event.key !== '/' || event.ctrlKey || event.metaKey || event.altKey) return;
    const target = event.target;
    if (target instanceof HTMLElement) {
      const tag = target.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || target.isContentEditable) return;
    }
    event.preventDefault();
    document.getElementById('console-search')?.focus();
  }

  function onHashChange() {
    panel = null;
  }

  function onDocumentClick(event: MouseEvent) {
    const node = event.target;
    if (!(node instanceof Node)) return;
    for (const anchor of document.querySelectorAll('.console-anchor')) {
      if (anchor.contains(node)) return;
    }
    panel = null;
  }

  function openMachine(name: string) {
    panel = null;
    navigate(`/machines/${encodeURIComponent(name)}`);
  }

  function openAllDevices() {
    panel = null;
    navigate('/machines');
  }
</script>

{#snippet navMark(kind: NavMark)}
  <svg class="nav-mark" width="20" height="20" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    {#if kind === 'overview'}
      <rect x="3" y="3" width="5.5" height="5.5" rx="1" />
      <rect x="11.5" y="3" width="5.5" height="5.5" rx="1" />
      <rect x="3" y="11.5" width="5.5" height="5.5" rx="1" />
      <rect x="11.5" y="11.5" width="5.5" height="5.5" rx="1" />
    {:else if kind === 'devices'}
      <rect x="3" y="4" width="14" height="9" rx="1.5" />
      <path d="M8 16.5h4M10 13v3.5" />
    {:else if kind === 'apps'}
      <path d="M10 2.5 16.5 6v8L10 17.5 3.5 14V6Z" />
      <path d="M3.5 6 10 9.5 16.5 6M10 9.5v8" />
    {:else}
      <path d="M10 2.5 12 7.2l5.2.5-4 3.5 1.2 5.1L10 13.6 5.6 16.3 6.8 11.2 2.8 7.7l5.2-.5Z" />
    {/if}
  </svg>
{/snippet}

<div class="console">
  <header class="console-top">
    <a class="console-lockup" href="#/">
      <LogoMark />
      <span class="console-word">{t('app.title')}</span>
    </a>

    <div class="console-anchor" data-open={panel === 'scope' ? 'true' : undefined}>
      <button type="button" class="console-scope" aria-expanded={panel === 'scope'} onclick={() => toggle('scope')}>
        {t('nav.scope', { n: store.machines.length })}
      </button>
      {#if panel === 'scope'}
        <div class="console-popover align-start">
          <ul class="console-menu">
            {#each store.machines as machine (machine.metadata.name)}
              <li>
                <button type="button" onclick={() => openMachine(machine.metadata.name)}>
                  {machine.metadata.name}
                </button>
              </li>
            {/each}
            <li>
              <button type="button" onclick={openAllDevices}>{t('nav.scopeAll')}</button>
            </li>
          </ul>
        </div>
      {/if}
    </div>

    <input
      id="console-search"
      class="console-search"
      type="search"
      placeholder={t('nav.search')}
      aria-label={t('nav.search')}
    />

    <div class="console-anchor" data-open={panel === 'help' ? 'true' : undefined}>
      <button
        type="button"
        class="console-icon"
        aria-label={t('nav.help')}
        aria-expanded={panel === 'help'}
        onclick={() => toggle('help')}
      >
        <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true">
          <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="1.8" />
          <path
            d="M9.5 9a2.5 2.5 0 1 1 3.2 2.4c-.7.3-1.2.8-1.2 1.6V14"
            fill="none"
            stroke="currentColor"
            stroke-width="1.8"
            stroke-linecap="round"
          />
          <circle cx="12" cy="17" r="0.9" fill="currentColor" />
        </svg>
      </button>
      {#if panel === 'help'}
        <div class="console-popover align-end">
          <ul class="console-help">
            {#each HELP_STATES as state (state)}
              <li>
                <StateBadge tone={lifecycleStateTone(state)} label={lifecycleStateLabel(state, i18n.locale)} />
              </li>
            {/each}
          </ul>
        </div>
      {/if}
    </div>

    <div class="console-anchor" data-open={panel === 'account' ? 'true' : undefined}>
      <button
        type="button"
        class="console-account"
        aria-label={t('nav.account')}
        aria-expanded={panel === 'account'}
        onclick={() => toggle('account')}
      >
        <svg class="account-mark" width="28" height="28" viewBox="0 0 28 28" aria-hidden="true">
          <circle cx="14" cy="14" r="14" fill="#1a73e8" />
          <circle cx="14" cy="11" r="4" fill="#fff" />
          <path d="M6 23c1.5-4 4.2-6 8-6s6.5 2 8 6" fill="#fff" />
        </svg>
      </button>
      {#if panel === 'account'}
        <div class="console-popover align-end">
          <p class="console-popover-title">{t('nav.account')}</p>
        </div>
      {/if}
    </div>
  </header>

  <div class="console-frame">
    <nav class="console-nav" data-collapsed={collapsed ? 'true' : 'false'} aria-label={t('nav.menu')}>
      <div class="console-nav-scroll">
        {#each NAV as group (group.labelKey ?? 'root')}
          <div class="console-group">
            {#if group.labelKey && !collapsed}
              <div class="console-group-label">{t(group.labelKey)}</div>
            {/if}
            {#each group.items as item (item.path)}
              <a
                class="console-nav-item"
                class:is-selected={isActive(item.path)}
                href={`#${item.path}`}
                aria-current={isActive(item.path) ? 'page' : undefined}
                title={t(item.labelKey)}
              >
                {@render navMark(item.mark)}
                {#if !collapsed}
                  <span>{t(item.labelKey)}</span>
                {/if}
              </a>
            {/each}
          </div>
        {/each}
      </div>
      <button type="button" class="console-collapse" onclick={() => (collapsed = !collapsed)}>
        {collapsed ? t('nav.expand') : t('nav.collapse')}
      </button>
    </nav>

    <main class="console-canvas">
      {#if router.path === '/'}
        <Overview {store} />
      {:else if router.segments[0] === 'machines' && router.segments[1]}
        <MachineDetail {store} name={decodeURIComponent(router.segments[1])} />
      {:else if router.segments[0] === 'machines'}
        <Machines {store} />
      {:else if router.segments[0] === 'apps'}
        <Apps />
      {:else if router.segments[0] === 'lifecycle'}
        <Lifecycle
          machines={store.machines}
          gateway={new RestLifecycleGateway(store.client)}
          onLifecycleEvent={(cb) => store.onLifecycleEvent(cb)}
        />
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
          {t('app.unknownRoute')} <code>{router.path}</code>
          <button type="button" class="underline" onclick={() => navigate('/')}>{t('app.backToOverview')}</button>
        </div>
      {/if}
    </main>
  </div>
</div>
