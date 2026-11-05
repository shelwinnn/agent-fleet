<script lang="ts">
  import type { AdapterCapability, Machine, RenderPreview } from '$lib/api/types.js';
  import { Card, CardContent } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import { t, i18n, translate, type Locale } from '$lib/i18n/index.js';
  import { conditionView } from '$lib/state/drift.js';
  import { lifecycleStateLabel, lifecycleStateTone } from '$lib/state/lifecycle.js';
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { loadErrorText } from '$lib/state/ssh.js';
  import type { Tone } from '$lib/state/tone.js';
  import { untrack } from 'svelte';

  interface Props {
    store: FleetStore;
    name: string;
  }
  let { store, name }: Props = $props();

  const loc = $derived(i18n.locale);
  const detail = $derived(store.details?.[name]);
  const machine = $derived(detail?.machine ?? store.machineByName?.(name));
  const profile = $derived((store.profiles ?? []).find((item) => item.metadata.name === machine?.spec?.profileRef));
  const profileRef = $derived(machine?.spec?.profileRef);

  let rendered = $state<RenderPreview | null>(null);

  const loadError = $derived(
    detail?.error !== undefined
      ? `${t('detail.error.load')}${loadErrorText(detail.error, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );

  const presence = $derived.by(() => {
    const status = conditionView(machine?.status?.conditions, 'AgentConnected')?.status;
    if (status === 'True') return { label: translate(loc, 'host.meta.online'), tone: lifecycleStateTone('succeeded') };
    if (status === 'False') return { label: translate(loc, 'host.meta.offline'), tone: lifecycleStateTone('failed') };
    return { label: lifecycleStateLabel('unknown', loc), tone: lifecycleStateTone('unknown') };
  });
  const osText = $derived(machine?.status?.os?.trim() || '—');
  const archText = $derived(machine?.status?.arch?.trim() || '—');
  const userText = $derived(sshUser(machine) || '—');
  const agents = $derived(agentRows(machine, profile?.spec?.agents, rendered?.desired?.agents, loc));
  const skills = $derived(skillRows(profile?.spec?.skills, store.skills ?? [], rendered?.desired?.skills, loc));

  $effect(() => {
    const target = name;
    if (typeof store.loadMachine !== 'function') return;
    store.activeMachine = target;
    untrack(() => {
      void store.loadMachine(target);
    });
    return () => {
      if (store.activeMachine === target) store.activeMachine = null;
    };
  });

  $effect(() => {
    const profileName = profileRef;
    const target = name;
    const client = store.client;
    if (!profileName || !client) {
      rendered = null;
      return;
    }
    let cancelled = false;
    void client.renderProfile(profileName, target).then(
      (next) => {
        if (!cancelled) rendered = next;
      },
      () => {
        if (!cancelled) rendered = null;
      },
    );
    return () => {
      cancelled = true;
    };
  });

  function sshUser(current: Machine | undefined): string {
    const ssh = current?.spec?.ssh;
    if (!ssh || typeof ssh !== 'object') return '';
    const user = ssh.user;
    return typeof user === 'string' ? user.trim() : '';
  }

  const FAILED = new Set(['failed', 'error', 'failure']);
  const POSITIVE = new Set(['supported', 'verified', 'installed', 'succeeded', 'ok']);

  type AgentKind = 'unknown' | 'failed' | 'unverified' | 'installed' | 'absent';

  interface AgentRow {
    name: string;
    versionText: string;
    versionTone: Tone;
    statusText: string;
    statusTone: Tone;
    note: string;
  }

  interface SkillRow {
    name: string;
    revision: string;
    missing: boolean;
  }

  function reportedVersion(caps: AdapterCapability[]): string {
    const versionCap = caps.find((cap) => cap.capability === 'version');
    const raw = versionCap?.verifiedVersions ?? caps.find((cap) => cap.verifiedVersions)?.verifiedVersions ?? '';
    return raw.trim();
  }

  function agentKind(caps: AdapterCapability[]): AgentKind {
    const states = caps.map((cap) => cap.state.trim().toLowerCase());
    if (states.includes('unknown')) return 'unknown';
    const primary = (caps.find((cap) => cap.capability === 'version') ?? caps[0])?.state.trim().toLowerCase() ?? '';
    if (FAILED.has(primary)) return 'failed';
    if (primary === 'unverified') return 'unverified';
    if (POSITIVE.has(primary) && reportedVersion(caps)) return 'installed';
    return 'absent';
  }

  function versionPresentation(kind: AgentKind, reported: string, locale: Locale): { text: string; tone: Tone } {
    if (kind === 'unknown') {
      return {
        text: reported ? translate(locale, 'host.version.lastSeen', { version: reported }) : translate(locale, 'host.version.lastSeenOnly'),
        tone: lifecycleStateTone('unknown'),
      };
    }
    if (kind === 'unverified' && reported) return { text: reported, tone: lifecycleStateTone('unknown') };
    if (kind === 'installed') return { text: reported, tone: lifecycleStateTone('succeeded') };
    return {
      text: translate(locale, 'host.version.none'),
      tone: kind === 'failed' ? lifecycleStateTone('failed') : lifecycleStateTone('not_executed'),
    };
  }

  function statusPresentation(kind: AgentKind, locale: Locale): { text: string; tone: Tone } {
    switch (kind) {
      case 'unknown':
        return { text: lifecycleStateLabel('unknown', locale), tone: lifecycleStateTone('unknown') };
      case 'failed':
        return { text: lifecycleStateLabel('failed', locale), tone: lifecycleStateTone('failed') };
      case 'unverified':
        return { text: translate(locale, 'host.status.unverified'), tone: lifecycleStateTone('unknown') };
      case 'installed':
        return { text: translate(locale, 'host.status.installed'), tone: lifecycleStateTone('succeeded') };
      case 'absent':
        return { text: translate(locale, 'host.status.absent'), tone: lifecycleStateTone('not_executed') };
    }
  }

  function agentRows(
    current: Machine | undefined,
    profileAgents: Record<string, { version?: string }> | undefined,
    renderedAgents: Record<string, { version?: string }> | undefined,
    locale: Locale,
  ): AgentRow[] {
    const caps = current?.status?.adapterCapabilities ?? [];
    const desired = new Map<string, string>();
    for (const [agentName, agent] of Object.entries(profileAgents ?? {})) {
      const version = agent?.version?.trim();
      if (version) desired.set(agentName, version);
    }
    for (const [agentName, agent] of Object.entries(renderedAgents ?? {})) {
      const version = agent?.version?.trim();
      if (version) desired.set(agentName, version);
    }
    const names = new Set<string>([...desired.keys(), ...caps.map((cap) => cap.family).filter(Boolean)]);
    return [...names].sort((a, b) => a.localeCompare(b)).map((agentName) => {
      const family = caps.filter((cap) => cap.family === agentName);
      const kind = agentKind(family);
      const version = versionPresentation(kind, reportedVersion(family), locale);
      const status = statusPresentation(kind, locale);
      const reasons = [...new Set(family.map((cap) => cap.reason?.trim()).filter((reason): reason is string => Boolean(reason)))];
      const wanted = desired.get(agentName);
      const note = reasons.join(translate(locale, 'app.listSeparator')) || (kind === 'absent' && wanted ? translate(locale, 'host.agent.desired', { version: wanted }) : '—');
      return {
        name: agentName,
        versionText: version.text,
        versionTone: version.tone,
        statusText: status.text,
        statusTone: status.tone,
        note,
      };
    });
  }

  function skillRows(
    refs: { name?: string; skill?: string }[] | undefined,
    catalog: { metadata: { name: string }; status?: { resolvedRevision?: string } }[],
    renderedSkills: Record<string, { revision?: string }> | undefined,
    locale: Locale,
  ): SkillRow[] {
    const none = translate(locale, 'host.version.none');
    if (renderedSkills && Object.keys(renderedSkills).length > 0) {
      return Object.entries(renderedSkills).map(([skillName, skill]) => {
        const revision = skill.revision?.trim() ?? '';
        return { name: skillName, revision: revision || none, missing: !revision };
      });
    }
    return (refs ?? []).flatMap((ref) => {
      const skillName = ref.name || ref.skill || '';
      if (!skillName) return [];
      const record = catalog.find((item) => item.metadata.name === (ref.skill || ref.name));
      const revision = record?.status?.resolvedRevision?.trim() ?? '';
      return [{ name: skillName, revision: revision || none, missing: !revision }];
    });
  }
</script>

<div class="host-page">
  <header>
    <nav class="host-crumb" aria-label={t('host.crumb.label')}>
      <a href="#/machines">{t('host.crumb.devices')}</a>
      <span aria-hidden="true">/</span>
      <span aria-current="page">{name}</span>
    </nav>
    <h1 class="console-page-title">{name}</h1>
  </header>

  {#if !machine}
    <AsyncState
      loading={detail?.loading}
      error={loadError}
      empty={!detail?.loading && !loadError ? t('detail.notFound', { name }) : undefined}
    />
  {:else}
    {#if loadError}
      <div class="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive" role="alert">
        {loadError}
      </div>
    {/if}

    <p class="host-meta">
      <StateBadge tone={presence.tone} label={presence.label} />
      <span>{osText}</span>
      <span aria-hidden="true">·</span>
      <span>{archText}</span>
      <span aria-hidden="true">·</span>
      <span>{userText}</span>
    </p>

    <section class="host-section">
      <h2>{t('host.section.agents')}</h2>
      <Card>
        <CardContent>
          <Table.Root>
            <Table.Header>
              <Table.Row>
                <Table.Head>{t('host.agents.col.name')}</Table.Head>
                <Table.Head>{t('host.agents.col.version')}</Table.Head>
                <Table.Head>{t('host.agents.col.status')}</Table.Head>
                <Table.Head>{t('host.agents.col.note')}</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#if agents.length === 0}
                <Table.Row>
                  <Table.Cell colspan={4} class="whitespace-normal text-muted-foreground">{t('host.agents.empty')}</Table.Cell>
                </Table.Row>
              {:else}
                {#each agents as agent (agent.name)}
                  <Table.Row>
                    <Table.Cell>{agent.name}</Table.Cell>
                    <Table.Cell>
                      <StateBadge tone={agent.versionTone} label={agent.versionText} />
                    </Table.Cell>
                    <Table.Cell>
                      <StateBadge tone={agent.statusTone} label={agent.statusText} />
                    </Table.Cell>
                    <Table.Cell class="whitespace-normal text-muted-foreground">{agent.note}</Table.Cell>
                  </Table.Row>
                {/each}
              {/if}
            </Table.Body>
          </Table.Root>
        </CardContent>
      </Card>
    </section>

    <section class="host-section">
      <h2>{t('host.section.skills')}</h2>
      <Card>
        <CardContent>
          {#if skills.length === 0}
            <p>{t('host.skills.empty')}</p>
          {:else}
            <Table.Root>
              <Table.Header>
                <Table.Row>
                  <Table.Head>{t('detail.skills.col.name')}</Table.Head>
                  <Table.Head>{t('detail.skills.col.revision')}</Table.Head>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {#each skills as skill, i (skill.name + i)}
                  <Table.Row>
                    <Table.Cell>{skill.name}</Table.Cell>
                    <Table.Cell>
                      {#if skill.missing}
                        <StateBadge tone={lifecycleStateTone('not_executed')} label={skill.revision} />
                      {:else}
                        {skill.revision}
                      {/if}
                    </Table.Cell>
                  </Table.Row>
                {/each}
              </Table.Body>
            </Table.Root>
          {/if}
        </CardContent>
      </Card>
    </section>
  {/if}
</div>

<style>
  .host-page {
    display: grid;
    gap: 20px;
  }
  .host-crumb {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    margin: 0 0 8px;
    color: #5f6368;
    font-size: 13px;
  }
  .host-crumb a {
    color: #1a73e8;
    text-decoration: none;
  }
  .host-crumb a:hover {
    text-decoration: underline;
  }
  .host-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    margin: 12px 0 0;
    color: #5f6368;
    font-size: 13px;
  }
  .host-section {
    display: grid;
    gap: 8px;
  }
  .host-section h2 {
    margin: 0;
    color: #202124;
    font-size: 16px;
    font-weight: 500;
  }
</style>
