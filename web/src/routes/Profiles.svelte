<script lang="ts">
  /**
   * Profiles（spec §24.4）：可编辑的归一化期望状态 + JSON/YAML 预览 + 消费机器。
   * 渲染预览（GET /profiles/{name}/render?machine=）暴露 DesiredStateInvalid，
   * 例如 profile 里写未注册家族（zcode）时节点阶段 1 会显式失败——界面如实呈现，
   * 不为 ZCode 做特例（KM-27 范围决策）。
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
  import { describeActionFailure } from '$lib/state/ssh.js';
  import type { RenderPreview } from '$lib/api/types.js';

  interface Props {
    store: FleetStore;
    name: string | null;
  }
  let { store, name }: Props = $props();

  const clock = createClock(30_000);
  const selected = $derived(store.profiles.find((p) => p.metadata.name === name) ?? null);
  const consumers = $derived(selected ? store.machinesUsingProfile(selected.metadata.name) : []);

  let renderMachine = $state('');
  let render = $state<RenderPreview | null>(null);
  let renderError = $state<string | null>(null);
  let renderLoading = $state(false);
  let editText = $state('');
  let editError = $state<string | null>(null);
  let saving = $state(false);

  $effect(() => {
    // 切换 profile 时重置编辑区与渲染结果。
    if (selected) {
      editText = prettyJSON(selected.spec ?? {});
      render = null;
      renderError = null;
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
      renderError = `${describeActionFailure(err).title}：${describeActionFailure(err).detail}`;
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
      editError = err instanceof SyntaxError ? `JSON 解析失败：${err.message}` : describeActionFailure(err).detail;
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!selected) return;
    if (!confirm(`删除 profile ${selected.metadata.name}？引用它的机器将渲染失败（DesiredStateInvalid）。`)) return;
    try {
      await store.client.deleteProfile(selected.metadata.name);
      navigate('/profiles');
      await store.afterMutation('profiles', 'machines');
    } catch (err) {
      editError = describeActionFailure(err).detail;
    }
  }
</script>

<div class="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
  <Card>
    <CardHeader>
      <CardTitle class="text-sm">Profiles（{store.profiles.length}）</CardTitle>
      <p class="text-xs text-muted-foreground">选择一项查看预览、消费机器与渲染结果。</p>
    </CardHeader>
    <CardContent>
      <AsyncState
        loading={store.loading.profiles}
        error={store.errors.profiles}
        empty={store.profiles.length === 0 ? '还没有 profile。' : undefined}
      />
      {#if store.profiles.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>Name</Table.Head><Table.Head>agents</Table.Head><Table.Head>skills</Table.Head>
              <Table.Head>消费机器</Table.Head><Table.Head>更新</Table.Head>
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
                <Table.Cell class="text-xs">{Object.keys(profile.spec?.agents ?? {}).join('、') || '—'}</Table.Cell>
                <Table.Cell class="text-xs">{(profile.spec?.skills ?? []).length}</Table.Cell>
                <Table.Cell class="text-xs">{store.machinesUsingProfile(profile.metadata.name).length}</Table.Cell>
                <Table.Cell class="text-xs text-muted-foreground">
                  {formatRelativeTime(profile.metadata.creationTimestamp ?? '', clock.now)}
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
          <CardTitle class="text-sm">{selected.metadata.name} · 期望状态</CardTitle>
          <p class="text-xs text-muted-foreground">
            spec 可编辑（PUT /api/v1/profiles/{name}）；status 由控制器维护，更新时不接受改写。
          </p>
        </CardHeader>
        <CardContent class="grid gap-3">
          <Tabs.Root value="json">
            <Tabs.List>
              <Tabs.Trigger value="json">JSON</Tabs.Trigger>
              <Tabs.Trigger value="yaml">YAML</Tabs.Trigger>
              <Tabs.Trigger value="edit">编辑 spec</Tabs.Trigger>
            </Tabs.List>
            <Tabs.Content value="json">
              <pre class="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{prettyJSON(selected.spec ?? {})}</pre>
            </Tabs.Content>
            <Tabs.Content value="yaml">
              <pre class="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{prettyYAML(selected.spec ?? {})}</pre>
            </Tabs.Content>
            <Tabs.Content value="edit">
              <div class="grid gap-2">
                <Label for="spec-json" class="text-xs">spec（JSON）</Label>
                <textarea
                  id="spec-json"
                  class="h-64 w-full rounded-md border bg-background p-2 font-mono text-xs"
                  bind:value={editText}
                ></textarea>
                {#if editError}
                  <Alert.Root variant="destructive">
                    <Alert.Title>保存失败</Alert.Title>
                    <Alert.Description>{editError}</Alert.Description>
                  </Alert.Root>
                {/if}
                <div class="flex gap-2">
                  <Button size="sm" onclick={save} disabled={saving}>{saving ? '保存中…' : '保存 spec'}</Button>
                  <Button size="sm" variant="ghost" onclick={remove}>删除 profile</Button>
                </div>
              </div>
            </Tabs.Content>
          </Tabs.Root>

          <div class="text-xs">
            <span class="text-muted-foreground">消费该 profile 的机器：</span>
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
          <CardTitle class="text-sm">渲染预览（FR-7.4：不落库、不增代）</CardTitle>
        </CardHeader>
        <CardContent class="grid gap-3">
          <div class="flex flex-wrap items-end gap-2">
            <div class="grid gap-1">
              <Label for="render-machine" class="text-xs">目标机器</Label>
              <Input id="render-machine" class="w-56" bind:value={renderMachine} placeholder="machine name" />
            </div>
            <Button size="sm" variant="outline" onclick={runRender} disabled={renderLoading || !renderMachine}>
              {renderLoading ? '渲染中…' : '渲染'}
            </Button>
          </div>
          {#if renderError}
            <Alert.Root variant="destructive">
              <Alert.Title>渲染失败（DesiredStateInvalid）</Alert.Title>
              <Alert.Description>{renderError}</Alert.Description>
            </Alert.Root>
          {/if}
          {#if render}
            <div class="grid gap-1 text-xs">
              <div>digest：<span class="font-mono">{shortDigest(render.digest, 12)}</span></div>
              <div>canonicalizationVersion：<span class="font-mono">{render.canonicalizationVersion}</span></div>
              <div>schemaVersion：<span class="font-mono">{render.desired?.schemaVersion ?? '—'}</span></div>
            </div>
            <pre class="max-h-72 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{prettyJSON(render.desired ?? {})}</pre>
          {:else if !renderError}
            <p class="text-xs text-muted-foreground">
              选择目标机器后点"渲染"：种子中的浮动引用（未解析 Skill、未知 Provider）会在此显式失败，
              而不是被静默忽略。
            </p>
          {/if}
        </CardContent>
      </Card>
    </div>
  {:else}
    <Card>
      <CardContent class="pt-6 text-sm text-muted-foreground">
        从左侧选择一个 profile 查看 JSON/YAML 预览、消费机器与渲染结果。
      </CardContent>
    </Card>
  {/if}
</div>
