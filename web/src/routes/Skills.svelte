<script lang="ts">
  /**
   * Skills（spec §24.5）：Name | Source | Requested Ref | Resolved Revision |
   * Digest | Machines；动作 Resolve / 查看元数据 / 改 ref。
   *
   * 可用性如实标注：`POST /skills/{name}/resolve` 与 `GET /skills/{name}/revisions`
   * 未实现，见 docs/web-ui.md（调用返回 404）。因此 Resolve 按钮禁用；
   * "改 ref" 只能改 spec 里已存在的键（不发明字段名）；解析结果在有值时照常显示。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { prettyJSON, shortDigest } from '$lib/state/format.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { describeActionFailure } from '$lib/state/ssh.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  let expanded = $state<string | null>(null);
  let refDraft = $state('');
  let message = $state<{ kind: 'ok' | 'error'; text: string } | null>(null);

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
      message = {
        kind: 'error',
        text: '该 Skill 的 spec 没有 ref 字段：本片不发明字段名（Skill spec 形态属后续切片），请直接改 spec。',
      };
      return;
    }
    spec.ref = refDraft;
    try {
      await store.client.updateSkill(name, { metadata: { name }, spec });
      message = { kind: 'ok', text: `已更新 ${name} 的 ref（需 resolve 后才会影响渲染）。` };
      await store.afterMutation('skills');
    } catch (err) {
      message = { kind: 'error', text: describeActionFailure(err).detail };
    }
  }
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">Skills</h1>
    <p class="text-xs text-muted-foreground">
      已解析的精确修订是快照的输入之一（禁止 latest 式浮动引用）；未解析的 Skill 会让渲染显式失败。
    </p>
  </div>

  {#if message}
    <Alert.Root variant={message.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{message.kind === 'error' ? '未执行' : '已完成'}</Alert.Title>
      <Alert.Description>{message.text}</Alert.Description>
    </Alert.Root>
  {/if}

  <Card>
    <CardContent class="pt-6">
      <AsyncState
        loading={store.loading.skills}
        error={store.errors.skills}
        empty={store.skills.length === 0 ? '还没有 Skill 资源。' : undefined}
      />
      {#if store.skills.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>Name</Table.Head><Table.Head>Source</Table.Head><Table.Head>Requested Ref</Table.Head>
              <Table.Head>Resolved Revision</Table.Head><Table.Head>Digest</Table.Head><Table.Head>Machines</Table.Head>
              <Table.Head>动作</Table.Head>
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
                  {skill.status?.resolvedRevision ?? '未解析'}
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
                    <Button size="sm" variant="outline" disabled title="POST /skills/:name/resolve 未实现，见 docs/web-ui.md（调用返回 404）">
                      Resolve
                    </Button>
                    <Button size="sm" variant="ghost" onclick={() => (expanded = expanded === skill.metadata.name ? null : skill.metadata.name)}>
                      元数据
                    </Button>
                  </div>
                </Table.Cell>
              </Table.Row>
              {#if expanded === skill.metadata.name}
                <Table.Row>
                  <Table.Cell colspan={7}>
                    <div class="grid gap-2 rounded-md border bg-muted/40 p-3">
                      <div class="text-xs text-muted-foreground">spec（原样）</div>
                      <pre class="max-h-48 overflow-auto text-xs">{prettyJSON(skill.spec ?? {})}</pre>
                      <div class="text-xs text-muted-foreground">status（resolve 控制器维护）</div>
                      <pre class="max-h-32 overflow-auto text-xs">{prettyJSON(skill.status ?? {})}</pre>
                      <div class="flex flex-wrap items-end gap-2">
                        <input
                          class="h-8 w-56 rounded-md border bg-background px-2 font-mono text-xs"
                          placeholder="新的 ref（写入 spec.ref）"
                          bind:value={refDraft}
                        />
                        <Button size="sm" variant="outline" onclick={() => saveRef(skill.metadata.name)}>改 ref</Button>
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

  <Alert.Root>
    <Alert.Title>未实现的 Skill 端点</Alert.Title>
    <Alert.Description>
      POST /api/v1/skills/{'{'}name{'}'}/resolve 与 GET /api/v1/skills/{'{'}name{'}'}/revisions
      未实现，见 docs/web-ui.md（调用返回 404）。因此"Resolved Revision / Digest"只有在
      status 已有值时才显示，界面不会伪造解析结果。
    </Alert.Description>
  </Alert.Root>
</div>
