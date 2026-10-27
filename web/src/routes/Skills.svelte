<script lang="ts">
  /**
   * Skills：名称 | 来源 | 请求 ref | 已解析修订 | 内容摘要 | 机器 | 操作。
   *
   * 可用性如实标注：`resolve` 尚未提供（按钮禁用并写明原因）；
   * "改 ref" 只能改 spec 里已存在的键（不发明字段名）；解析结果在有值时照常显示。
   *
   * 全部文案经 i18n 取值，切换语言立即跟随。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { prettyJSON, shortDigest } from '$lib/state/format.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { actionOutcomeTitle, failureText, loadErrorText } from '$lib/state/ssh.js';
  import { t, i18n, type MessageKey, type MessageParams } from '$lib/i18n/index.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  const loc = $derived(i18n.locale);
  const loadError = $derived(
    store.errors.skills !== undefined
      ? `${t('skills.error.load')}${loadErrorText(store.errors.skills, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );

  let expanded = $state<string | null>(null);
  let refDraft = $state('');

  /** 动作反馈：成功存 key+params（渲染期翻译）；失败存固定文案 key 或原始错误。 */
  type Message =
    | { kind: 'ok'; titleKey: MessageKey; key: MessageKey; params: MessageParams }
    | { kind: 'error'; textKey?: MessageKey; error?: unknown };
  let message = $state<Message | null>(null);

  const specField = (spec: Record<string, unknown> | undefined, ...keys: string[]): string => {
    for (const key of keys) {
      const value = spec?.[key];
      if (typeof value === 'string' && value) return value;
    }
    return '—';
  };

  async function saveRef(name: string) {
    const skill = store.skills.find((s) => s.metadata.name === name);
    if (!skill) return;
    const spec = { ...(skill.spec ?? {}) };
    // 只改已存在的字段：Skill spec 形态属后续切片，界面不发明字段名。
    if (!('ref' in spec)) {
      message = { kind: 'error', textKey: 'skills.ref.missingField' };
      return;
    }
    spec.ref = refDraft;
    try {
      await store.client.updateSkill(name, { metadata: { name }, spec });
      message = { kind: 'ok', titleKey: 'ok.refUpdated', key: 'skills.ref.ok', params: { name } };
      await store.afterMutation('skills');
    } catch (err) {
      message = { kind: 'error', error: err };
    }
  }
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">{t('nav.skills')}</h1>
    <p class="text-xs text-muted-foreground">{t('skills.subtitle')}</p>
  </div>

  {#if message}
    <Alert.Root variant={message.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{message.kind === 'error' ? (message.textKey ? t('common.actionNotPerformed') : actionOutcomeTitle(message.error, i18n.locale)) : t(message.titleKey)}</Alert.Title>
      <Alert.Description>
        {#if message.kind === 'error'}
          {message.textKey ? t(message.textKey) : failureText(message.error, i18n.locale)}
        {:else}
          {t(message.key, message.params)}
        {/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  <Card>
    <CardContent class="pt-6">
      <AsyncState loading={store.loading.skills} error={loadError} empty={store.skills.length === 0 ? t('skills.empty') : undefined} />
      {#if store.skills.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('skills.col.name')}</Table.Head><Table.Head>{t('skills.col.source')}</Table.Head><Table.Head>{t('skills.col.ref')}</Table.Head>
              <Table.Head>{t('skills.col.revision')}</Table.Head><Table.Head>{t('skills.col.digest')}</Table.Head><Table.Head>{t('skills.col.machines')}</Table.Head>
              <Table.Head>{t('skills.col.actions')}</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each store.skills as skill (skill.metadata.name)}
              {@const machines = store.machinesUsingSkill(skill.metadata.name)}
              <Table.Row>
                <Table.Cell class="text-xs font-medium">{skill.metadata.name}</Table.Cell>
                <Table.Cell class="text-xs">{specField(skill.spec, 'source', 'url', 'repo')}</Table.Cell>
                <Table.Cell class="text-xs font-mono">{specField(skill.spec, 'ref', 'revision', 'requestedRef')}</Table.Cell>
                <Table.Cell class="text-xs font-mono">
                  {skill.status?.resolvedRevision ?? t('skills.unresolved')}
                </Table.Cell>
                <Table.Cell class="text-xs font-mono">{shortDigest(skill.status?.contentDigest)}</Table.Cell>
                <Table.Cell class="text-xs">
                  {#if machines.length === 0}—{:else}
                    {#each machines as m (m.metadata.name)}
                      <button class="mr-2 underline" onclick={() => navigate(`/machines/${m.metadata.name}`)}>{m.metadata.name}</button>
                    {/each}
                  {/if}
                </Table.Cell>
                <Table.Cell>
                  <div class="flex flex-wrap items-center gap-1">
                    <Button size="sm" variant="outline" disabled title={t('skills.resolve.title')}>
                      Resolve
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-expanded={expanded === skill.metadata.name}
                      onclick={() => (expanded = expanded === skill.metadata.name ? null : skill.metadata.name)}
                    >
                      {t('skills.metadata')}
                    </Button>
                  </div>
                </Table.Cell>
              </Table.Row>
              {#if expanded === skill.metadata.name}
                <Table.Row>
                  <Table.Cell colspan={7}>
                    <div class="grid gap-2 rounded-md border bg-muted/40 p-3">
                      <div class="text-xs text-muted-foreground">{t('skills.specRaw')}</div>
                      <pre class="max-h-48 overflow-auto text-xs">{prettyJSON(skill.spec ?? {})}</pre>
                      <div class="text-xs text-muted-foreground">{t('skills.statusNote')}</div>
                      <pre class="max-h-32 overflow-auto text-xs">{prettyJSON(skill.status ?? {})}</pre>
                      <div class="flex flex-wrap items-end gap-2">
                        <input
                          class="h-8 w-56 rounded-md border bg-background px-2 font-mono text-xs"
                          placeholder={t('skills.ref.placeholder')}
                          aria-label={t('skills.ref.placeholder')}
                          bind:value={refDraft}
                        />
                        <Button size="sm" variant="outline" onclick={() => saveRef(skill.metadata.name)}>{t('skills.ref.button')}</Button>
                      </div>
                    </div>
                  </Table.Cell>
                </Table.Row>
              {/if}
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>
</div>
