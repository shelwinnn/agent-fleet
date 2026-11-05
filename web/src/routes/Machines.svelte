<script lang="ts">
  /**
   * 设备列表：名称、状态、系统、用户、软件、要处理。
   * 页眉搜索框过滤这一页的行。未知漂移沿用现有展示，不上成功色。
   */
  import { onMount } from 'svelte';
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import type { AgentProfile, Machine, OperationPhase } from '$lib/api/types.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { machineRows, type MachineRow } from '$lib/state/machines.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { failureText, loadErrorText } from '$lib/state/ssh.js';
  import { operationPhaseLabel } from '$lib/state/operations.js';
  import { t, i18n, type MessageKey } from '$lib/i18n/index.js';
  import type { Tone } from '$lib/state/tone.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  type Chip = 'all' | 'online' | 'attention' | 'offline';

  const CHIPS: { id: Chip; key: MessageKey }[] = [
    { id: 'all', key: 'devices.chip.all' },
    { id: 'online', key: 'devices.chip.online' },
    { id: 'attention', key: 'devices.chip.attention' },
    { id: 'offline', key: 'devices.chip.offline' },
  ];

  const COLUMNS: MessageKey[] = [
    'devices.col.name',
    'devices.col.status',
    'devices.col.system',
    'devices.col.user',
    'devices.col.software',
    'devices.col.attention',
  ];

  const clock = createClock(15_000);
  const loc = $derived(i18n.locale);
  const rows = $derived(machineRows(store.machines, clock.now, loc));

  let chip = $state<Chip>('all');
  let query = $state('');
  let showEnroll = $state(false);
  let enrollName = $state('');
  let needName = $state(false);
  let enrolling = $state(false);
  let enrollError = $state<unknown>(null);
  let dialogEl = $state<HTMLDialogElement | null>(null);

  interface Listed {
    row: MachineRow;
    online: boolean;
    system: string;
    user: string;
    software: string;
    statusLabel: string;
    statusTone: Tone;
  }

  function isOnline(row: MachineRow): boolean {
    if (row.agentd.connected === 'True') return true;
    return row.ssh.mode === 'ssh' && row.ssh.label === 'True';
  }

  function statusTone(row: MachineRow, online: boolean): Tone {
    if (online) return 'ok';
    if (row.agentd.connected === 'False' || row.ssh.label === 'False') return 'danger';
    return 'warn';
  }

  function systemOf(machine: Machine | undefined, fallback: string): string {
    const os = machine?.status?.os;
    const arch = machine?.status?.arch;
    if (os && arch) return `${os} · ${arch}`;
    return os || arch || fallback;
  }

  function userOf(machine: Machine | undefined): string {
    const ssh = machine?.spec?.ssh;
    const named = ssh?.User ?? ssh?.user;
    if (typeof named === 'string' && named.trim()) return named.trim();
    const home = machine?.status?.homeDir;
    if (!home) return '—';
    const parts = home.split(/[/\\]/).filter(Boolean);
    return parts.at(-1) ?? '—';
  }

  function softwareOf(machine: Machine | undefined, profiles: AgentProfile[]): string {
    const ref = machine?.spec?.profileRef;
    if (!ref) return '—';
    const profile = profiles.find((item) => item.metadata.name === ref);
    if (!profile) return '—';
    const agents = Object.entries(profile.spec?.agents ?? {})
      .filter(([, agent]) => agent?.enabled !== false)
      .map(([name]) => name);
    const skills = (profile.spec?.skills ?? [])
      .map((skill) => skill.name)
      .filter((name): name is string => Boolean(name));
    const items = [...agents, ...skills];
    if (items.length === 0) return '—';
    return items.join(loc === 'zh-CN' ? '、' : ', ');
  }

  function extraReasons(row: MachineRow): string[] {
    const drifted = t('machine.attention.drifted');
    const unknownPrefix = loc === 'zh-CN' ? 'drift 未知' : 'drift unknown';
    return row.attentionReasons.filter((reason) => reason !== drifted && !reason.startsWith(unknownPrefix));
  }

  const listed = $derived.by(() => {
    const profiles = store.profiles ?? [];
    const q = query.trim().toLowerCase();
    const out: Listed[] = [];
    for (const row of rows) {
      const machine = store.machines.find((item) => item.metadata.name === row.name);
      const online = isOnline(row);
      const system = systemOf(machine, row.osArch);
      const user = userOf(machine);
      const software = softwareOf(machine, profiles);
      const statusLabel = online ? t('devices.status.online') : t('devices.status.offline');
      if (chip === 'online' && !online) continue;
      if (chip === 'offline' && online) continue;
      if (chip === 'attention' && !row.needsAttention) continue;
      if (q) {
        const hay = [row.name, system, user, software, statusLabel, row.drift.label, ...row.attentionReasons]
          .join('\n')
          .toLowerCase();
        if (!hay.includes(q)) continue;
      }
      out.push({
        row,
        online,
        system,
        user,
        software,
        statusLabel,
        statusTone: statusTone(row, online),
      });
    }
    return out;
  });

  const loadError = $derived(
    store.errors.machines !== undefined
      ? `${t('machines.error.load')}${loadErrorText(store.errors.machines, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );
  const showFleetEmpty = $derived(
    !loadError &&
      !(store.loading.machines && rows.length === 0) &&
      rows.length === 0 &&
      query.trim() === '' &&
      chip === 'all',
  );
  const showNoMatch = $derived(!loadError && !showFleetEmpty && listed.length === 0 && !(store.loading.machines && rows.length === 0));

  function syncQuery() {
    const input = document.getElementById('console-search');
    if (input instanceof HTMLInputElement) query = input.value;
  }

  onMount(() => {
    syncQuery();
    const input = document.getElementById('console-search');
    if (!(input instanceof HTMLInputElement)) return;
    input.addEventListener('input', syncQuery);
    input.addEventListener('search', syncQuery);
    return () => {
      input.removeEventListener('input', syncQuery);
      input.removeEventListener('search', syncQuery);
    };
  });

  $effect(() => {
    if (!showEnroll || typeof document === 'undefined') return;
    const node = dialogEl;
    if (node && !node.open) {
      try {
        node.showModal();
      } catch {
        node.open = true;
      }
    }
    document.getElementById('devices-enroll-name')?.focus();
  });

  function onDialogKeydown(event: KeyboardEvent) {
    if (event.key !== 'Escape') return;
    event.preventDefault();
    closeEnroll();
  }

  function openEnroll() {
    enrollName = '';
    needName = false;
    enrollError = null;
    showEnroll = true;
  }

  function closeEnroll() {
    showEnroll = false;
  }

  async function submitEnroll(event: SubmitEvent) {
    event.preventDefault();
    if (enrollName.trim() === '') {
      enrollError = null;
      needName = true;
      return;
    }
    needName = false;
    enrolling = true;
    enrollError = null;
    try {
      await store.client.createMachine({
        metadata: { name: enrollName.trim() },
        spec: { managementMode: 'agentd' },
      });
      showEnroll = false;
      enrollName = '';
      await store.afterMutation('machines');
    } catch (err) {
      enrollError = err;
    } finally {
      enrolling = false;
    }
  }

  function attentionQuiet(item: Listed): boolean {
    return item.row.drift.state === 'in-sync' && !item.row.unresolvedPhase && extraReasons(item.row).length === 0;
  }
</script>

<section class="devices">
  <div class="devices-head">
    <div>
      <h1 class="console-page-title">{t('devices.heading')}</h1>
      <p class="devices-lead">{t('devices.lead')}</p>
    </div>
    <button type="button" class="devices-primary" onclick={openEnroll}>{t('devices.enroll')}</button>
  </div>

  <div class="devices-chips">
    {#each CHIPS as item (item.id)}
      <button
        type="button"
        class="devices-chip"
        aria-pressed={chip === item.id}
        onclick={() => (chip = item.id)}
      >{t(item.key)}</button>
    {/each}
  </div>

  {#if store.loading.machines && rows.length === 0}
    <AsyncState loading={true} rows={5} />
  {/if}
  {#if loadError}
    <AsyncState error={loadError} />
  {/if}
  {#if showFleetEmpty}
    <AsyncState empty={t('devices.empty')} />
  {/if}

  <div class="devices-card">
    <table class="devices-table">
      <thead>
        <tr>
          {#each COLUMNS as column (column)}
            <th>{t(column)}</th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#if showNoMatch}
          <tr>
            <td class="devices-empty" colspan="6"><p role="status">{t('devices.searchEmpty')}</p></td>
          </tr>
        {:else}
          {#each listed as item (item.row.name)}
            <tr>
              <td>
                <a class="devices-name" href={`#/machines/${encodeURIComponent(item.row.name)}`}>{item.row.name}</a>
              </td>
              <td>
                <StateBadge tone={item.statusTone} label={item.statusLabel} />
              </td>
              <td>{item.system}</td>
              <td>{item.user}</td>
              <td>{item.software}</td>
              <td>
                {#if item.row.drift.state !== 'in-sync'}
                  <StateBadge tone={item.row.drift.tone} label={item.row.drift.label} title={item.row.drift.detail ?? ''} />
                {/if}
                {#if item.row.unresolvedPhase}
                  <StateBadge
                    tone="warn"
                    label={t('machines.unresolvedBadge')}
                    title={t('machines.unresolvedTitle', {
                      phase: operationPhaseLabel(item.row.unresolvedPhase as OperationPhase, loc),
                    })}
                  />
                {/if}
                {#each extraReasons(item.row) as reason (reason)}
                  <span class="devices-reason">{reason}</span>
                {/each}
                {#if attentionQuiet(item)}
                  —
                {/if}
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </div>

  {#if showEnroll}
    <dialog
      class="devices-dialog"
      aria-labelledby="devices-enroll-title"
      bind:this={dialogEl}
      onkeydown={onDialogKeydown}
      onclose={closeEnroll}
    >
      <h2 id="devices-enroll-title">{t('devices.enroll')}</h2>
      <form onsubmit={submitEnroll}>
        <div class="devices-field">
          <label for="devices-enroll-name">{t('devices.name')}</label>
          <input
            id="devices-enroll-name"
            bind:value={enrollName}
            autocomplete="off"
            oninput={() => (needName = false)}
          />
        </div>
        {#if needName}
          <p class="devices-alert" role="alert">{t('devices.needName')}</p>
        {/if}
        {#if enrollError}
          <p class="devices-alert" role="alert">{failureText(enrollError, loc)}</p>
        {/if}
        <div class="devices-actions">
          <button type="button" class="devices-secondary" onclick={closeEnroll}>{t('common.cancel')}</button>
          <button type="submit" class="devices-primary" disabled={enrolling}>
            {enrolling ? t('devices.submitting') : t('devices.enroll')}
          </button>
        </div>
      </form>
    </dialog>
  {/if}
</section>

<style>
  .devices {
    display: grid;
    gap: 16px;
  }

  .devices-head {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
  }

  .devices-lead {
    margin: 4px 0 0;
    color: #5f6368;
    font-size: 13px;
  }

  .devices-primary,
  .devices-secondary,
  .devices-chip {
    font: inherit;
    cursor: pointer;
  }

  .devices-primary {
    height: 36px;
    padding: 0 16px;
    border: none;
    border-radius: 4px;
    background: #1a73e8;
    color: #fff;
  }

  .devices-primary:disabled {
    opacity: 0.6;
    cursor: default;
  }

  .devices-secondary {
    height: 36px;
    padding: 0 16px;
    border: 1px solid #dadce0;
    border-radius: 4px;
    background: #fff;
    color: #202124;
  }

  .devices-primary:focus-visible,
  .devices-secondary:focus-visible,
  .devices-chip:focus-visible,
  .devices-name:focus-visible {
    outline: 2px solid #1a73e8;
    outline-offset: 2px;
  }

  .devices-chips {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .devices-chip {
    height: 32px;
    padding: 0 12px;
    border: 1px solid #dadce0;
    border-radius: 16px;
    background: #fff;
    color: #202124;
  }

  .devices-chip[aria-pressed='true'] {
    background: #e8f0fe;
    border-color: #d2e3fc;
    color: #1967d2;
  }

  .devices-card {
    background: #fff;
    border: 1px solid #dadce0;
    border-radius: 8px;
    overflow: auto;
  }

  .devices-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }

  .devices-table th {
    padding: 12px 16px;
    border-bottom: 1px solid #dadce0;
    color: #5f6368;
    font-weight: 500;
    text-align: left;
    white-space: nowrap;
  }

  .devices-table td {
    padding: 12px 16px;
    border-bottom: 1px solid #f1f3f4;
    vertical-align: middle;
  }

  .devices-table tr:last-child td {
    border-bottom: none;
  }

  .devices-name {
    color: #1967d2;
    font-weight: 500;
    text-decoration: none;
  }

  .devices-name:hover {
    text-decoration: underline;
  }

  .devices-empty {
    padding: 24px 16px;
    color: #5f6368;
  }

  .devices-empty p {
    margin: 0;
  }

  .devices-reason {
    display: inline-block;
    margin-right: 8px;
  }

  .devices-dialog {
    box-sizing: border-box;
    width: min(420px, calc(100vw - 32px));
    margin: auto;
    padding: 24px;
    border: none;
    border-radius: 8px;
    background: #fff;
    color: #202124;
  }

  .devices-dialog::backdrop {
    background: rgba(32, 33, 36, 0.32);
  }

  .devices-dialog h2 {
    margin: 0 0 16px;
    font-size: 18px;
    font-weight: 400;
  }

  .devices-field {
    display: grid;
    gap: 6px;
    margin-bottom: 12px;
    font-size: 13px;
  }

  .devices-field input {
    height: 36px;
    border: 1px solid #dadce0;
    border-radius: 4px;
    padding: 0 12px;
    color: #202124;
    font: inherit;
  }

  .devices-alert {
    margin: 0 0 12px;
    color: #c5221f;
    font-size: 13px;
  }

  .devices-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
