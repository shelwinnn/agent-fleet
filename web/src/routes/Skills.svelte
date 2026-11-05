<script lang="ts">
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import type { Skill } from '$lib/api/types.js';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { loadErrorText } from '$lib/state/ssh.js';
  import { t, i18n } from '$lib/i18n/index.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  type Posture =
    | { kind: 'current' }
    | { kind: 'behind'; revision: string }
    | { kind: 'unknown' };

  const loc = $derived(i18n.locale);
  const skills = $derived(store.skills ?? []);
  const loadError = $derived(
    store.errors.skills !== undefined
      ? `${t('skills.error.load')}${loadErrorText(store.errors.skills, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );

  let sourceOpen = $state(false);
  let sourceName = $state('');
  let sourceMissing = $state(false);
  let upgradeName = $state<string | null>(null);

  const upgradeSkill = $derived(skills.find((skill) => skill.metadata.name === upgradeName) ?? null);

  function field(value: unknown): string {
    return typeof value === 'string' ? value.trim() : '';
  }

  function postureOf(skill: Skill): Posture {
    const resolved = field(skill.status?.resolvedRevision);
    const latest = field(skill.status?.latestRevision);
    if (resolved === '' || latest === '') return { kind: 'unknown' };
    if (latest === resolved) return { kind: 'current' };
    return { kind: 'behind', revision: latest };
  }

  function specField(spec: Record<string, unknown> | undefined, ...keys: string[]): string {
    for (const key of keys) {
      const value = field(spec?.[key]);
      if (value !== '') return value;
    }
    return '—';
  }

  function agentsFor(skillName: string): string[] {
    const names: string[] = [];
    for (const profile of store.profiles ?? []) {
      const refs = profile.spec?.skills ?? [];
      if (!refs.some((ref) => ref.skill === skillName || ref.name === skillName)) continue;
      for (const [agentName, agent] of Object.entries(profile.spec?.agents ?? {})) {
        if (agent?.enabled === false) continue;
        if (!names.includes(agentName)) names.push(agentName);
      }
    }
    return names;
  }

  function openSource() {
    upgradeName = null;
    sourceName = '';
    sourceMissing = false;
    sourceOpen = true;
  }

  function closeSource() {
    sourceOpen = false;
    sourceMissing = false;
  }

  function submitSource(event: SubmitEvent) {
    event.preventDefault();
    sourceMissing = sourceName.trim() === '';
  }

  function openUpgrade(name: string) {
    sourceOpen = false;
    upgradeName = name;
  }

  function closeUpgrade() {
    upgradeName = null;
  }

  function onBackdrop(event: MouseEvent, close: () => void) {
    if (event.target === event.currentTarget) close();
  }

  $effect(() => {
    if (!sourceOpen && upgradeName === null) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      closeSource();
      closeUpgrade();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });
</script>

<div class="skills-page">
  <header class="skills-head">
    <div>
      <h1 class="console-page-title">{t('nav.skills')}</h1>
      <p class="skills-lead">{t('skills.lead')}</p>
    </div>
    <button type="button" class="skills-primary" onclick={openSource}>{t('skills.addSource')}</button>
  </header>

  <div class="skills-panel">
    <AsyncState
      loading={store.loading.skills}
      error={loadError}
      empty={skills.length === 0 ? t('skills.empty') : undefined}
    />
    {#if skills.length > 0}
      <table>
        <thead>
          <tr>
            <th>{t('skills.col.name')}</th>
            <th>{t('skills.col.machines')}</th>
            <th>{t('skills.col.agent')}</th>
            <th>{t('skills.col.source')}</th>
            <th>{t('skills.col.revision')}</th>
            <th>{t('skills.col.posture')}</th>
            <th>{t('skills.col.actions')}</th>
          </tr>
        </thead>
        <tbody>
          {#each skills as skill (skill.metadata.name)}
            {@const posture = postureOf(skill)}
            {@const machines = store.machinesUsingSkill(skill.metadata.name)}
            {@const agents = agentsFor(skill.metadata.name)}
            <tr data-skill={skill.metadata.name}>
              <td class="skills-name">{skill.metadata.name}</td>
              <td>
                {#if machines.length === 0}
                  —
                {:else}
                  {#each machines as machine, index (machine.metadata.name)}
                    {#if index > 0}{t('app.listSeparator')}{/if}
                    <a class="skills-link" href={`#/machines/${encodeURIComponent(machine.metadata.name)}`}>{machine.metadata.name}</a>
                  {/each}
                {/if}
              </td>
              <td>{agents.length === 0 ? '—' : agents.join(t('app.listSeparator'))}</td>
              <td>{specField(skill.spec, 'source', 'url', 'repo')}</td>
              <td class="skills-revision">{skill.status?.resolvedRevision?.trim() ? skill.status.resolvedRevision : t('skills.unresolved')}</td>
              <td data-posture={posture.kind}>
                {#if posture.kind === 'current'}
                  {t('skills.current')}
                {:else if posture.kind === 'behind'}
                  {t('skills.canUpgradeTo', { revision: posture.revision })}
                {/if}
              </td>
              <td>
                <div class="skills-actions">
                  {#if posture.kind === 'behind'}
                    <button type="button" class="skills-upgrade" onclick={() => openUpgrade(skill.metadata.name)}>
                      {t('skills.upgrade')}
                    </button>
                  {/if}
                  <button type="button" class="skills-resolve" disabled title={t('skills.resolve.title')}>
                    {t('skills.resolve')}
                  </button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </div>
</div>

{#if sourceOpen}
  <div class="skills-backdrop" role="presentation" onclick={(event) => onBackdrop(event, closeSource)}>
    <div class="skills-dialog-card" role="dialog" aria-modal="true" aria-labelledby="skill-source-title">
      <h2 id="skill-source-title">{t('skills.addSource')}</h2>
      <form onsubmit={submitSource}>
        <label class="skills-field">
          <span>{t('skills.sourceName')}</span>
          <input
            bind:value={sourceName}
            aria-label={t('skills.sourceName')}
            aria-invalid={sourceMissing ? 'true' : undefined}
            aria-describedby={sourceMissing ? 'skill-source-error' : undefined}
          />
        </label>
        {#if sourceMissing}
          <p id="skill-source-error" class="skills-error" role="alert">{t('skills.sourceNameMissing')}</p>
        {/if}
        <div class="skills-dialog-actions">
          <button type="button" class="skills-quiet" onclick={closeSource}>{t('skills.close')}</button>
          <button type="submit" class="skills-primary">{t('skills.addSource')}</button>
        </div>
      </form>
    </div>
  </div>
{/if}

{#if upgradeSkill}
  {@const upgradePosture = postureOf(upgradeSkill)}
  <div class="skills-backdrop" role="presentation" onclick={(event) => onBackdrop(event, closeUpgrade)}>
    <div class="skills-dialog-card" role="dialog" aria-modal="true" aria-labelledby="skill-upgrade-title">
      <h2 id="skill-upgrade-title">{t('skills.upgrade')}</h2>
      <p class="skills-upgrade-name">{upgradeSkill.metadata.name}</p>
      {#if upgradePosture.kind === 'behind'}
        <p>{t('skills.canUpgradeTo', { revision: upgradePosture.revision })}</p>
      {/if}
      <div class="skills-dialog-actions">
        <button type="button" class="skills-quiet" onclick={closeUpgrade}>{t('skills.close')}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .skills-page {
    display: grid;
    gap: 16px;
    color: #202124;
  }

  .skills-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
  }

  .skills-lead {
    margin: 4px 0 0;
    color: #5f6368;
    font-size: 13px;
    line-height: 1.5;
  }

  .skills-primary {
    flex: none;
    height: 36px;
    padding: 0 16px;
    border: none;
    border-radius: 4px;
    background: #1a73e8;
    color: #fff;
    font-size: 14px;
    cursor: pointer;
  }

  .skills-primary:hover {
    background: #1967d2;
  }

  .skills-panel {
    background: #fff;
    border: 1px solid #dadce0;
    border-radius: 8px;
    overflow: hidden;
  }

  .skills-panel :global([role='status']),
  .skills-panel :global([role='alert']),
  .skills-panel :global([aria-busy='true']) {
    margin: 16px;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }

  th {
    padding: 12px 16px;
    border-bottom: 1px solid #dadce0;
    background: #f8f9fa;
    color: #5f6368;
    font-weight: 500;
    text-align: left;
  }

  td {
    padding: 12px 16px;
    border-bottom: 1px solid #e8eaed;
    vertical-align: middle;
  }

  tr:last-child td {
    border-bottom: none;
  }

  .skills-name,
  .skills-revision {
    font-variant-numeric: tabular-nums;
  }

  .skills-link {
    color: #1a73e8;
    text-decoration: none;
  }

  .skills-link:hover {
    text-decoration: underline;
  }

  .skills-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .skills-upgrade,
  .skills-resolve,
  .skills-quiet {
    height: 32px;
    padding: 0 12px;
    border-radius: 4px;
    font-size: 13px;
  }

  .skills-upgrade {
    border: 1px solid #d2e3fc;
    background: #e8f0fe;
    color: #1967d2;
    cursor: pointer;
  }

  .skills-resolve:disabled {
    border: 1px solid #dadce0;
    background: #f1f3f4;
    color: #80868b;
    cursor: not-allowed;
  }

  .skills-quiet {
    border: 1px solid #dadce0;
    background: #fff;
    color: #202124;
    cursor: pointer;
  }

  .skills-backdrop {
    position: fixed;
    inset: 0;
    z-index: 40;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
    background: rgba(32, 33, 36, 0.32);
  }

  .skills-dialog-card {
    width: min(420px, 100%);
    padding: 20px;
    border-radius: 8px;
    background: #fff;
    box-shadow: 0 8px 28px rgba(32, 33, 36, 0.28);
  }

  .skills-dialog-card h2 {
    margin: 0 0 12px;
    font-size: 18px;
    font-weight: 500;
  }

  .skills-field {
    display: grid;
    gap: 6px;
    color: #5f6368;
    font-size: 13px;
  }

  .skills-field input {
    height: 36px;
    padding: 0 12px;
    border: 1px solid #dadce0;
    border-radius: 4px;
    color: #202124;
    font-size: 14px;
  }

  .skills-error {
    margin: 8px 0 0;
    color: #d93025;
    font-size: 13px;
  }

  .skills-upgrade-name {
    margin: 0 0 8px;
    font-weight: 500;
  }

  .skills-dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 16px;
  }
</style>
