<script lang="ts">
  /**
   * Profiles：可编辑的归一化期望状态 + JSON/YAML 预览 + 使用该配置档的机器。
   * 渲染预览不会落库、不产生新版本；引用无法解析时（如未注册的 agent 家族）
   * 服务端显式报错——界面如实呈现，不做特例。
   *
   * 全部文案经 i18n 取值，切换语言立即跟随。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import * as Tabs from '$lib/components/ui/tabs/index.js';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { formatRelativeTime, prettyJSON, prettyYAML, shortDigest } from '$lib/state/format.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { failureText, loadErrorText } from '$lib/state/ssh.js';
  import type { RenderPreview } from '$lib/api/types.js';
  import { t, i18n, formatNumber } from '$lib/i18n/index.js';

  interface Props {
    store: FleetStore;
    name: string | null;
  }
  let { store, name }: Props = $props();

  const clock = createClock(30_000);
  const loc = $derived(i18n.locale);
  const selected = $derived(store.profiles.find((p) => p.metadata.name === name) ?? null);
  const consumers = $derived(selected ? store.machinesUsingProfile(selected.metadata.name) : []);
  const loadError = $derived(
    store.errors.profiles !== undefined
      ? `${t('profiles.error.load')}${loadErrorText(store.errors.profiles, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );

  let renderMachine = $state('');
  let render = $state<RenderPreview | null>(null);
  let renderError = $state<unknown>(null);
  let renderLoading = $state(false);
  let editText = $state('');
  let editError = $state<{ json?: string; failure?: unknown } | null>(null);
  let saving = $state(false);

  $effect(() => {
    // 切换 profile 时重置编辑区与渲染结果。
    if (selected) {
      editText = prettyJSON(selected.spec ?? {});
      render = null;
      renderError = null;
      editError = null;
      renderMachine = consumers[0]?.metadata.name ?? store.machines[0]?.metadata.name ?? '';
    }
  });

  async function runRender() {
    if (!selected || !renderMachine) return;
    renderLoading = true;
    renderError = null;
    try {
      render = await store.client.renderProfile(selected.metadata.name, renderMachine);
    } catch (err) {
      render = null;
      renderError = err;
    } finally {
      renderLoading = false;
    }
  }

  async function save() {
    if (!selected) return;
    editError = null;
    saving = true;
    try {
      const spec = JSON.parse(editText) as Record<string, unknown>;
      await store.client.updateProfile(selected.metadata.name, {
        metadata: { name: selected.metadata.name },
        spec,
      });
      await store.afterMutation('profiles', 'machines');
    } catch (err) {
      editError = err instanceof SyntaxError ? { json: err.message } : { failure: err };
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!selected) return;
    if (!confirm(t('profiles.delete.confirm', { name: selected.metadata.name }))) return;
    try {
      await store.client.deleteProfile(selected.metadata.name);
      navigate('/profiles');
      await store.afterMutation('profiles', 'machines');
    } catch (err) {
      editError = { failure: err };
    }
  }
</script>

<div class="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
  <Card>
    <CardHeader>
      <CardTitle class="text-sm">{t('profiles.count', { n: formatNumber(store.profiles.length, loc) })}</CardTitle>
      <p class="text-xs text-muted-foreground">{t('profiles.subtitle')}</p>
    </CardHeader>
    <CardContent>
      <AsyncState loading={store.loading.profiles} error={loadError} empty={store.profiles.length === 0 ? t('profiles.empty') : undefined} />
      {#if store.profiles.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('profiles.col.name')}</Table.Head><Table.Head>agents</Table.Head><Table.Head>skills</Table.Head>
              <Table.Head>{t('profiles.col.consumers')}</Table.Head><Table.Head>{t('profiles.col.updated')}</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each store.profiles as profile (profile.metadata.name)}
              <Table.Row class={profile.metadata.name === name ? 'bg-muted/60' : ''}>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/profiles/${profile.metadata.name}`)}>
                    {profile.metadata.name}
                  </button>
                </Table.Cell>
                <Table.Cell class="text-xs">{Object.keys(profile.spec?.agents ?? {}).join(t('app.listSeparator')) || '—'}</Table.Cell>
                <Table.Cell class="text-xs">{formatNumber((profile.spec?.skills ?? []).length, loc)}</Table.Cell>
                <Table.Cell class="text-xs">{formatNumber(store.machinesUsingProfile(profile.metadata.name).length, loc)}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">
                  {formatRelativeTime(profile.metadata.creationTimestamp ?? '', clock.now, loc)}
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  {#if selected}
    <div class="grid gap-5">
      <Card>
        <CardHeader>
          <CardTitle class="text-sm">{t('profiles.detail.title', { name: selected.metadata.name })}</CardTitle>
          <p class="text-xs text-muted-foreground">{t('profiles.spec.note')}</p>
        </CardHeader>
        <CardContent class="grid gap-3">
          <Tabs.Root value="json">
            <Tabs.List>
              <Tabs.Trigger value="json">JSON</Tabs.Trigger>
              <Tabs.Trigger value="yaml">YAML</Tabs.Trigger>
              <Tabs.Trigger value="edit">{t('profiles.tab.edit')}</Tabs.Trigger>
            </Tabs.List>
            <Tabs.Content value="json">
              <pre class="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{prettyJSON(selected.spec ?? {})}</pre>
            </Tabs.Content>
            <Tabs.Content value="yaml">
              <pre class="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{prettyYAML(selected.spec ?? {})}</pre>
            </Tabs.Content>
            <Tabs.Content value="edit">
              <div class="grid gap-2">
                <Label for="spec-json" class="text-xs">spec (JSON)</Label>
                <textarea
                  id="spec-json"
                  class="h-64 w-full rounded-md border bg-background p-2 font-mono text-xs"
                  bind:value={editText}
                  aria-label="spec (JSON)"
                ></textarea>
                {#if editError}
                  <Alert.Root variant="destructive">
                    <Alert.Title>{t('profiles.saveFailed')}</Alert.Title>
                    <Alert.Description>
                      {#if editError.json}{t('profiles.jsonInvalid', { message: editError.json })}{:else if editError.failure}{failureText(editError.failure, i18n.locale)}{/if}
                    </Alert.Description>
                  </Alert.Root>
                {/if}
                <div class="flex gap-2">
                  <Button size="sm" onclick={save} disabled={saving}>{saving ? t('profiles.saving') : t('profiles.save')}</Button>
                  <Button size="sm" variant="ghost" onclick={remove}>{t('profiles.delete')}</Button>
                </div>
              </div>
            </Tabs.Content>
          </Tabs.Root>

          <div class="text-xs">
            <span class="text-muted-foreground">{t('profiles.consumers')}: </span>
            {#if consumers.length === 0}
              —
            {:else}
              {#each consumers as m (m.metadata.name)}
                <button class="mr-2 underline" onclick={() => navigate(`/machines/${m.metadata.name}`)}>{m.metadata.name}</button>
              {/each}
            {/if}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle class="text-sm">{t('profiles.render.title')}</CardTitle>
          <p class="text-xs text-muted-foreground">{t('profiles.render.note')}</p>
        </CardHeader>
        <CardContent class="grid gap-3">
          <div class="flex flex-wrap items-end gap-2">
            <div class="grid gap-1">
              <Label for="render-machine" class="text-xs">{t('profiles.render.machine')}</Label>
              <Input id="render-machine" class="w-56" bind:value={renderMachine} placeholder="machine name" />
            </div>
            <Button size="sm" variant="outline" onclick={runRender} disabled={renderLoading || !renderMachine}>
              {renderLoading ? t('profiles.rendering') : t('profiles.render.button')}
            </Button>
          </div>
          {#if renderError}
            <Alert.Root variant="destructive">
              <Alert.Title>{t('profiles.render.failed')}</Alert.Title>
              <Alert.Description>{failureText(renderError, i18n.locale)}</Alert.Description>
            </Alert.Root>
          {/if}
          {#if render}
            <div class="grid gap-1 text-xs">
              <div>digest: <span class="font-mono">{shortDigest(render.digest, 12)}</span></div>
              <div>canonicalizationVersion: <span class="font-mono">{render.canonicalizationVersion}</span></div>
              <div>schemaVersion: <span class="font-mono">{render.desired?.schemaVersion ?? '—'}</span></div>
            </div>
            <pre class="max-h-72 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{prettyJSON(render.desired ?? {})}</pre>
          {:else if !renderError}
            <p class="text-xs text-muted-foreground">{t('profiles.render.idle')}</p>
          {/if}
        </CardContent>
      </Card>
    </div>
  {:else}
    <Card>
      <CardContent class="pt-6 text-sm text-muted-foreground">
        {t('profiles.selectPrompt')}
      </CardContent>
    </Card>
  {/if}
</div>
