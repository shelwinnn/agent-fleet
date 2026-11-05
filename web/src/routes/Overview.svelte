<script lang="ts">
  import type { Machine } from '$lib/api/types.js';
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import { machineRow, type MachineRow } from '$lib/state/machines.js';
  import { lifecycleStateLabel, lifecycleStateTone } from '$lib/state/lifecycle.js';
  import type { Tone } from '$lib/state/tone.js';
  import { t, i18n, formatNumber } from '$lib/i18n/index.js';
  import { loadErrorText } from '$lib/state/ssh.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  interface AttentionStatus {
    tone: Tone;
    label: string;
  }

  interface OverviewEntry {
    name: string;
    agents: string;
    detail: string;
    change: string;
    online: boolean;
    failed: boolean;
    versionUnknown: boolean;
    updatable: boolean;
    status: AttentionStatus | null;
  }

  const clock = createClock();
  const loc = $derived(i18n.locale);

  const machinesLoadError = $derived(
    store.errors.machines !== undefined
      ? `${t('machines.error.load')}${loadErrorText(store.errors.machines, loc)}`
      : undefined,
  );

  function agentNames(machine: Machine): string {
    const ref = machine.spec?.profileRef;
    const profile = store.profiles?.find((item) => item.metadata.name === ref);
    const specAgents = profile?.spec?.agents;
    if (specAgents) {
      const names = Object.entries(specAgents)
        .filter(([, agent]) => agent?.enabled !== false)
        .map(([name]) => name);
      if (names.length > 0) return names.join(t('app.listSeparator'));
    }
    const families = [
      ...new Set((machine.status?.adapterCapabilities ?? []).map((item) => item.family).filter((family) => family.length > 0)),
    ];
    if (families.length > 0) return families.join(t('app.listSeparator'));
    return '—';
  }

  function isVersionUnknown(row: MachineRow): boolean {
    return row.agentd.connected === 'Unknown' || !row.agentd.version;
  }

  function needsHandling(machine: Machine, row: MachineRow): boolean {
    return isFailed(machine, row) || isVersionUnknown(row) || row.drift.state === 'unknown';
  }

  function isFailed(machine: Machine, row: MachineRow): boolean {
    return row.agentd.connected === 'False' || row.degraded || machine.status?.unresolvedOperation?.phase === 'Failed';
  }

  function attentionStatus(row: MachineRow, versionUnknown: boolean): AttentionStatus {
    if (row.drift.state === 'unknown') return { tone: row.drift.tone, label: row.drift.label };
    if (row.agentd.connected === 'Unknown') return { tone: row.agentd.tone, label: row.agentd.label };
    if (versionUnknown) return { tone: lifecycleStateTone('unknown'), label: lifecycleStateLabel('unknown', loc) };
    if (row.agentd.connected === 'False') return { tone: row.agentd.tone, label: row.agentd.label };
    return { tone: lifecycleStateTone('failed'), label: lifecycleStateLabel('failed', loc) };
  }

  function detailText(row: MachineRow, versionUnknown: boolean): string {
    if (row.attentionReasons.length > 0) return row.attentionReasons.join(t('app.listSeparator'));
    if (versionUnknown) return t('overview.count.versionUnknown');
    if (row.drift.detail) return row.drift.detail;
    return '—';
  }

  const entries = $derived(
    store.machines.map((machine) => {
      const row = machineRow(machine, clock.now, loc);
      const versionUnknown = isVersionUnknown(row);
      const failed = isFailed(machine, row);
      const handling = needsHandling(machine, row);
      return {
        name: row.name,
        agents: agentNames(machine),
        detail: detailText(row, versionUnknown),
        change: row.drift.detail ?? row.drift.label,
        online: row.agentd.connected === 'True',
        failed,
        versionUnknown,
        updatable: row.drift.state === 'drifted',
        status: handling ? attentionStatus(row, versionUnknown) : null,
      } satisfies OverviewEntry;
    }),
  );

  const deviceCount = $derived(entries.length);
  const onlineCount = $derived(entries.filter((entry) => entry.online).length);
  const failedCount = $derived(entries.filter((entry) => entry.failed).length);
  const unknownCount = $derived(entries.filter((entry) => entry.versionUnknown).length);
  const attention = $derived(entries.filter((entry) => entry.status !== null));
  const updates = $derived(entries.filter((entry) => entry.updatable));
</script>

<div class="overview">
  <header class="overview-head">
    <div>
      <h1 class="console-page-title">{t('overview.title')}</h1>
      <p class="overview-lead">{t('overview.lead')}</p>
    </div>
  </header>

  {#if machinesLoadError}
    <p class="overview-error" role="alert">{machinesLoadError}</p>
  {/if}

  <div class="overview-counts">
    <button type="button" class="overview-count" onclick={() => navigate('/machines')}>
      <span class="overview-count-label">{t('overview.count.devices')}</span>
      <span class="overview-count-value">{formatNumber(deviceCount, loc)}</span>
    </button>
    <div class="overview-count">
      <span class="overview-count-label">{t('overview.count.online')}</span>
      <span class="overview-count-value">{formatNumber(onlineCount, loc)}</span>
    </div>
    <div class="overview-count">
      <span class="overview-count-label">{t('overview.count.failed')}</span>
      <span class="overview-count-value">{formatNumber(failedCount, loc)}</span>
    </div>
    <div class="overview-count">
      <span class="overview-count-label">{t('overview.count.versionUnknown')}</span>
      <span class="overview-count-value">{formatNumber(unknownCount, loc)}</span>
    </div>
  </div>

  <section class="overview-panel">
    <h2 id="overview-handle">{t('overview.handle.title')}</h2>
    <table aria-labelledby="overview-handle">
      <thead>
        <tr>
          <th scope="col">{t('overview.table.machine')}</th>
          <th scope="col">{t('overview.handle.agent')}</th>
          <th scope="col">{t('overview.handle.detail')}</th>
          <th scope="col">{t('overview.handle.status')}</th>
        </tr>
      </thead>
      <tbody>
        {#each attention as entry (entry.name)}
          <tr>
            <td>
              <button type="button" class="overview-link" onclick={() => navigate(`/machines/${encodeURIComponent(entry.name)}`)}>
                {entry.name}
              </button>
            </td>
            <td>{entry.agents}</td>
            <td>{entry.detail}</td>
            <td>
              {#if entry.status}
                <StateBadge tone={entry.status.tone} label={entry.status.label} />
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>

  <section class="overview-panel">
    <h2 id="overview-updates">{t('overview.updates.title')}</h2>
    <table aria-labelledby="overview-updates">
      <thead>
        <tr>
          <th scope="col">{t('overview.table.machine')}</th>
          <th scope="col">{t('overview.updates.change')}</th>
        </tr>
      </thead>
      <tbody>
        {#each updates as entry (entry.name)}
          <tr>
            <td>
              <button type="button" class="overview-link" onclick={() => navigate(`/machines/${encodeURIComponent(entry.name)}`)}>
                {entry.name}
              </button>
            </td>
            <td>{entry.change}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </section>
</div>

<style>
  .overview {
    display: grid;
    gap: 16px;
  }

  .overview-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
  }

  .overview-lead {
    margin: 8px 0 0;
    color: #5f6368;
    font-size: 14px;
  }

  .overview-count:focus-visible,
  .overview-link:focus-visible {
    outline: 2px solid #1a73e8;
    outline-offset: 2px;
  }

  .overview-error {
    margin: 0;
    padding: 12px;
    border: 1px solid #f6aea9;
    border-radius: 8px;
    background: #fce8e6;
    color: #c5221f;
    font-size: 14px;
  }

  .overview-counts {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
  }

  .overview-count {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
    margin: 0;
    padding: 16px;
    border: 1px solid #dadce0;
    border-radius: 8px;
    background: #fff;
    color: #202124;
    text-align: left;
  }

  button.overview-count {
    font: inherit;
    cursor: pointer;
  }

  button.overview-count:hover {
    border-color: #1a73e8;
  }

  .overview-count-label {
    color: #5f6368;
    font-size: 12px;
  }

  .overview-count-value {
    font-size: 32px;
    line-height: 1;
    font-variant-numeric: tabular-nums;
  }

  .overview-panel {
    padding: 16px;
    border: 1px solid #dadce0;
    border-radius: 8px;
    background: #fff;
  }

  .overview-panel h2 {
    margin: 0 0 12px;
    color: #202124;
    font-size: 16px;
    font-weight: 500;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 14px;
  }

  th {
    padding: 8px;
    border-bottom: 1px solid #dadce0;
    color: #5f6368;
    font-size: 12px;
    font-weight: 500;
    text-align: left;
  }

  td {
    padding: 8px;
    border-bottom: 1px solid #e8eaed;
    vertical-align: middle;
  }

  .overview-link {
    padding: 0;
    border: none;
    background: none;
    color: #1967d2;
    font: inherit;
    cursor: pointer;
  }

  .overview-link:hover {
    text-decoration: underline;
  }

  @media (max-width: 800px) {
    .overview-head {
      flex-direction: column;
    }

    .overview-counts {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
</style>
