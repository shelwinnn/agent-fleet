<script lang="ts">
  /**
   * 应用列表：要安装到机器上的代理。
   *
   * 可用版本只走 upstreamRowView。kind 为 ok 时才画出成功查询的可用版本行；
   * 失败、不受支持、还没查到，都不画那一行。缓存版本留在失败说明里，不充当这次的可用版本。
   */
  import { onMount } from 'svelte';
  import type { AgentProfile, Machine, UpstreamVersionReport } from '$lib/api/types.js';
  import { fleetStore } from '$lib/state/fleet.svelte.js';
  import { formatRelativeTime } from '$lib/state/format.js';
  import { t, i18n, type Locale } from '$lib/i18n/index.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { lifecycleStateTone, upstreamRowView, type UpstreamRowView } from '$lib/state/lifecycle.js';
  import StateBadge from '$lib/components/StateBadge.svelte';

  type UpstreamEntry =
    | { status: 'loading' }
    | { status: 'ok'; report: UpstreamVersionReport }
    | { status: 'error' };

  let reports = $state<Record<string, UpstreamEntry>>({});
  let desiredAgents = $state<Record<string, string[]>>({});
  let alive = true;
  const upstreamRequested = new Set<string>();
  const desiredRequested = new Set<string>();

  onMount(() => {
    alive = true;
    return () => {
      alive = false;
    };
  });

  const names = $derived.by(() => collectNames(fleetStore.profiles, fleetStore.machines, desiredAgents));

  $effect(() => {
    for (const machine of fleetStore.machines) {
      const machineName = machine.metadata.name;
      if (!machineName || desiredRequested.has(machineName)) continue;
      desiredRequested.add(machineName);
      void fleetStore.client.getLifecycleDesired(machineName).then(
        (doc) => {
          if (!alive) return;
          desiredAgents[machineName] = Object.keys(doc.entries ?? {});
        },
        () => {
          if (!alive) return;
          desiredAgents[machineName] = [];
        },
      );
    }
  });

  $effect(() => {
    for (const agent of names) {
      if (upstreamRequested.has(agent)) continue;
      upstreamRequested.add(agent);
      reports[agent] = { status: 'loading' };
      void fleetStore.client.checkUpstreamVersions(agent).then(
        (report) => {
          if (!alive) return;
          reports[agent] = { status: 'ok', report };
        },
        () => {
          if (!alive) return;
          reports[agent] = { status: 'error' };
        },
      );
    }
  });

  function sourceViews(agent: string, locale: Locale): UpstreamRowView[] | null {
    const entry = reports[agent];
    if (!entry || entry.status !== 'ok') return null;
    return (entry.report.sources ?? []).map((src) => upstreamRowView(src, locale));
  }

  function openAgent(agent: string) {
    navigate(`/lifecycle/${encodeURIComponent(agent)}`);
  }

  function onRowKeydown(event: KeyboardEvent, agent: string) {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    openAgent(agent);
  }

  function collectNames(
    profiles: AgentProfile[],
    machines: Machine[],
    desired: Record<string, string[]>,
  ): string[] {
    const found = new Set<string>();
    for (const profile of profiles) {
      for (const name of Object.keys(profile.spec?.agents ?? {})) {
        if (name) found.add(name);
      }
    }
    for (const machine of machines) {
      for (const cap of machine.status?.adapterCapabilities ?? []) {
        if (cap.family) found.add(cap.family);
      }
      for (const name of desired[machine.metadata.name] ?? []) {
        if (name) found.add(name);
      }
    }
    return [...found].sort((a, b) => a.localeCompare(b, 'zh-CN'));
  }

  function coverageOf(agent: string): number {
    const profilesByName = new Map(fleetStore.profiles.map((profile) => [profile.metadata.name, profile]));
    let count = 0;
    for (const machine of fleetStore.machines) {
      const ref = machine.spec?.profileRef;
      const profile = ref ? profilesByName.get(ref) : undefined;
      const specAgent = profile?.spec?.agents?.[agent];
      const inProfile = specAgent !== undefined && specAgent.enabled !== false;
      const inCaps = (machine.status?.adapterCapabilities ?? []).some((cap) => cap.family === agent);
      const inDesired = (desiredAgents[machine.metadata.name] ?? []).includes(agent);
      if (inProfile || inCaps || inDesired) count += 1;
    }
    return count;
  }
</script>

<h1 class="console-page-title">{t('nav.apps')}</h1>
<p class="apps-lead">{t('apps.lead')}</p>

<div class="apps-table-wrap">
  <table class="apps-table">
    <thead>
      <tr>
        <th scope="col">{t('apps.col.name')}</th>
        <th scope="col">{t('apps.col.available')}</th>
        <th scope="col">{t('apps.col.coverage')}</th>
        <th scope="col">{t('apps.col.status')}</th>
      </tr>
    </thead>
    <tbody>
      {#each names as agent (agent)}
        {@const views = sourceViews(agent, i18n.locale)}
        {@const allAvailable = views !== null && views.length > 0 && views.every((view) => view.kind === 'ok')}
        {@const allFailedKnown =
          views !== null &&
          views.length > 0 &&
          views.every((view) => view.kind === 'failed' && !view.unknown)}
        <tr
          class="apps-row"
          data-agent={agent}
          tabindex="0"
          onclick={() => openAgent(agent)}
          onkeydown={(event) => onRowKeydown(event, agent)}
        >
          <td>
            <a class="apps-name" href={`#/lifecycle/${encodeURIComponent(agent)}`}>{agent}</a>
          </td>
          <td data-col="available">
            {#if views === null || views.length === 0}
              <p class="apps-source" data-kind="unknown">{t('lc.upstream.unknown')}</p>
            {:else}
              {#each views as view, index (`${view.method}:${index}`)}
                {#if view.kind === 'ok'}
                  <p class="apps-source apps-available-line" data-kind="ok">
                    <span>{t('lc.upstream.available')}：</span>
                    <span class="apps-version">{view.version}</span>
                  </p>
                {:else if view.kind === 'failed'}
                  <div class="apps-source" data-kind="failed">
                    {#if view.unknown}
                      <p>{t('lc.upstream.unknown')}</p>
                    {:else}
                      <p>{t('lc.upstream.failed')}：{view.reasonText}</p>
                      {#if view.cacheVersion}
                        <p>
                          {t('lc.upstream.failedWithCache', {
                            time: formatRelativeTime(view.cacheQueriedAt ?? '', new Date(), i18n.locale),
                            version: view.cacheVersion,
                          })}
                        </p>
                      {/if}
                    {/if}
                  </div>
                {:else}
                  <p class="apps-source" data-kind="unsupported">{t('lc.upstream.unsupported')}</p>
                {/if}
              {/each}
            {/if}
          </td>
          <td data-col="coverage">{t('apps.coverage', { n: coverageOf(agent) })}</td>
          <td data-col="status">
            {#if allAvailable}
              <StateBadge tone="ok" label={t('common.available')} />
            {:else if allFailedKnown}
              <StateBadge tone={lifecycleStateTone('failed')} label={t('lc.state.failed')} />
            {:else}
              <StateBadge tone={lifecycleStateTone('unknown')} label={t('lc.state.unknown')} />
            {/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .apps-lead {
    margin: 8px 0 16px;
    color: #5f6368;
    font-size: 14px;
  }

  .apps-table-wrap {
    background: #fff;
    border: 1px solid #dadce0;
    border-radius: 8px;
    overflow: hidden;
  }

  .apps-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 14px;
  }

  .apps-table th {
    padding: 12px 16px;
    border-bottom: 1px solid #dadce0;
    color: #5f6368;
    font-weight: 500;
    text-align: left;
  }

  .apps-table td {
    padding: 12px 16px;
    border-bottom: 1px solid #e8eaed;
    vertical-align: top;
  }

  .apps-row {
    cursor: pointer;
  }

  .apps-row:hover {
    background: #f8f9fa;
  }

  .apps-name {
    color: #1967d2;
    text-decoration: none;
  }

  .apps-version {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-weight: 500;
  }

  .apps-source {
    margin: 0;
  }

  .apps-source + .apps-source {
    margin-top: 8px;
  }
</style>
