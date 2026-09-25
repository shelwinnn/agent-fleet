<script lang="ts">
  /**
   * SSH Inventory（spec §24.7）：Fleet 机器别名 + 服务端渲染的 OpenSSH include
   * 预览；动作 Export（下载）与"显式确认后安装/更新 include 文件"。
   *
   * include 内容以服务端渲染为唯一权威（KM-26 注册的 GET/POST /api/v1/ssh/include，
   * KM-28 接通；架构 §7.7），前端不再自拼。边界（FR-12.6）：
   *  - 安装必须显式确认，服务端对无 confirm 的请求返回 400 且不落盘；
   *  - 服务端只写 ~/.ssh/agent-fleet.conf，绝不改写操作者主配置——主配置里的
   *    Include 行由操作者自己加（响应回显该指令）；
   *  - spec 连接值（User/HostName/ProxyJump/IdentityFile）含空白或引号会被服务端
   *    拒绝渲染，服务端以 400 Invalid 返回渲染器原文（KM-29 修正：此前误映射为
   *    502 Internal），UI 按 §6.4 错误体原样呈现该 message，不在前端猜测改写；
   *  - host-key 校验保持标准策略（FR-12.3），不出现 StrictHostKeyChecking=no。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { machineRows } from '$lib/state/machines.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { describeActionFailure, formatSkippedHosts } from '$lib/state/ssh.js';
  import { shortDigest } from '$lib/state/format.js';
  import type { IncludeInstallResult, IncludePreview } from '$lib/api/types.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  const clock = createClock(30_000);
  const rows = $derived(machineRows(store.machines, clock.now));

  interface AliasView {
    machine: string;
    alias: string;
    hostName: string;
    user?: string;
    port?: number;
    identityFile?: string;
    mode: string;
    sshStatus: string;
  }

  /** 从 machine.spec.ssh 生成别名视图；缺字段时如实留空，不填默认值。 */
  const aliases = $derived<AliasView[]>(
    store.machines.map((m) => {
      const ssh = (m.spec?.ssh ?? {}) as Record<string, unknown>;
      const row = rows.find((r) => r.name === m.metadata.name);
      const str = (v: unknown) => (typeof v === 'string' && v ? v : undefined);
      return {
        machine: m.metadata.name,
        alias: str(ssh.hostAlias) ?? m.metadata.name,
        hostName: str(ssh.hostName) ?? str(ssh.host) ?? '（未配置）',
        user: str(ssh.user),
        port: typeof ssh.port === 'number' ? ssh.port : undefined,
        identityFile: str(ssh.identityFile),
        mode: (m.spec?.managementMode as string) ?? 'agentd',
        sshStatus: row?.ssh.label ?? 'Unknown',
      };
    }),
  );

  let preview = $state<IncludePreview | null>(null);
  let previewLoading = $state(false);
  let previewError = $state<string | null>(null);
  let exporting = $state(false);
  let installConfirmed = $state(false);
  let installing = $state(false);
  let installResult = $state<IncludeInstallResult | null>(null);
  let actionMessage = $state<{ kind: 'ok' | 'error'; text: string; detail?: string } | null>(null);

  async function loadPreview() {
    previewLoading = true;
    previewError = null;
    try {
      preview = await store.client.sshIncludePreview();
    } catch (err) {
      preview = null;
      const failure = describeActionFailure(err);
      previewError = `${failure.title}：${failure.detail}`;
    } finally {
      previewLoading = false;
    }
  }

  $effect(() => {
    void loadPreview();
  });

  /** Export（GET /api/v1/ssh/include?download=1）：取服务端渲染产物并触发下载。 */
  async function exportInclude() {
    exporting = true;
    actionMessage = null;
    try {
      const file = await store.client.sshIncludeExport();
      const blob = new Blob([file.content], { type: 'text/plain;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = 'agent-fleet.conf';
      link.click();
      URL.revokeObjectURL(url);
      actionMessage = {
        kind: 'ok',
        text: `已导出服务端渲染的 include 文件（included=${file.includedHosts}，skipped=${file.skippedHosts}）。`,
      };
    } catch (err) {
      const failure = describeActionFailure(err);
      actionMessage = { kind: 'error', text: `导出失败——${failure.title}：${failure.detail}` };
    } finally {
      exporting = false;
    }
  }

  /** 安装（POST /api/v1/ssh/include）：必须显式确认；成功后重载预览与最新摘要。 */
  async function installInclude() {
    installing = true;
    actionMessage = null;
    installResult = null;
    try {
      installResult = await store.client.sshIncludeInstall();
      actionMessage = {
        kind: 'ok',
        text: `已安装/更新 ${installResult.path}（contentDigest ${installResult.contentDigest}）。`,
        detail: installResult.primaryConfigHint,
      };
      installConfirmed = false;
      await loadPreview();
    } catch (err) {
      const failure = describeActionFailure(err);
      actionMessage = { kind: 'error', text: `安装失败——${failure.title}：${failure.detail}` };
    } finally {
      installing = false;
    }
  }
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">SSH Inventory</h1>
    <p class="text-xs text-muted-foreground">
      Fleet 机器别名与 OpenSSH include（spec §24.7）。当前限制：无证书吊销（删除机器即拒绝其证书）。
    </p>
  </div>

  <Card>
    <CardHeader><CardTitle class="text-sm">机器别名</CardTitle></CardHeader>
    <CardContent>
      <AsyncState
        loading={store.loading.machines}
        error={store.errors.machines}
        empty={aliases.length === 0 ? '还没有机器。' : undefined}
      />
      {#if aliases.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>别名</Table.Head><Table.Head>HostName</Table.Head><Table.Head>User</Table.Head>
              <Table.Head>Port</Table.Head><Table.Head>IdentityFile</Table.Head><Table.Head>管理方式</Table.Head>
              <Table.Head>SSH 条件</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each aliases as a (a.machine)}
              <Table.Row>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/machines/${a.machine}`)}>{a.alias}</button>
                  {#if a.alias !== a.machine}
                    <div class="text-xs text-muted-foreground">machine {a.machine}</div>
                  {/if}
                </Table.Cell>
                <Table.Cell class="text-xs">{a.hostName}</Table.Cell>
                <Table.Cell class="text-xs">{a.user ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs">{a.port ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs font-mono">{a.identityFile ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs">{a.mode}</Table.Cell>
                <Table.Cell><StateBadge tone={a.sshStatus === 'True' ? 'ok' : a.sshStatus === 'False' ? 'danger' : 'muted'} label={a.sshStatus} /></Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  <Card>
    <CardHeader>
      <CardTitle class="text-sm">OpenSSH include 预览（服务端渲染）</CardTitle>
      <p class="text-xs text-muted-foreground">
        内容由控制面渲染（GET /api/v1/ssh/include）：仅导出 Fleet 持有显式连接字段的机器，
        字段顺序固定、空值不输出；拒绝注入——值含空白或引号会被服务端拒绝渲染，
        拒绝理由按 §6.4 错误体原样呈现（状态码以后端实际返回为准）。
      </p>
    </CardHeader>
    <CardContent class="grid gap-3">
      <AsyncState loading={previewLoading} error={previewError ?? undefined} empty={preview && preview.content === '' ? '服务端渲染结果为空。' : undefined} />
      {#if preview}
        <div class="text-xs text-muted-foreground">
          included hosts：{preview.includedHosts} · skipped hosts：{preview.skippedHosts}
        </div>
        <pre class="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{preview.content}</pre>
      {/if}
      <div class="flex flex-wrap items-center gap-3">
        <Button size="sm" variant="outline" disabled={exporting || !preview} onclick={exportInclude}>
          {exporting ? '导出中…' : 'Export（下载 .conf）'}
        </Button>
        <div class="flex items-center gap-2">
          <input
            id="install-confirm"
            type="checkbox"
            class="size-4 accent-primary"
            bind:checked={installConfirmed}
            disabled={installing}
          />
          <Label for="install-confirm" class="text-xs">
            我确认安装/更新 ~/.ssh/agent-fleet.conf（主配置不被触碰，Include 行由我自己添加）
          </Label>
        </div>
        <Button
          size="sm"
          disabled={!installConfirmed || installing || !preview}
          onclick={installInclude}
          title="POST /api/v1/ssh/include（action=install, confirm=true）：服务端只写 ~/.ssh/agent-fleet.conf；无确认的请求返回 400 且不落盘"
        >
          {installing ? '安装中…' : '安装/更新 include 文件（需显式确认）'}
        </Button>
      </div>
    </CardContent>
  </Card>

  {#if actionMessage}
    <Alert.Root variant={actionMessage.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{actionMessage.kind === 'error' ? '动作失败' : '动作完成'}</Alert.Title>
      <Alert.Description>
        {actionMessage.text}
        {#if actionMessage.detail}<div class="mt-1">{actionMessage.detail}</div>{/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  {#if installResult}
    <Card>
      <CardHeader><CardTitle class="text-sm">最近一次安装结果（POST /api/v1/ssh/include）</CardTitle></CardHeader>
      <CardContent class="grid gap-1 font-mono text-xs">
        <div>path：{installResult.path}</div>
        <div>contentDigest：{shortDigest(installResult.contentDigest)}</div>
        <div>includedHosts：{installResult.includedHosts.join('、') || '—'}</div>
        <div>skippedHosts：{formatSkippedHosts(installResult.skippedHosts) || '—'}</div>
        <div>includeDirective：<code>{installResult.includeDirective}</code></div>
        <div class="text-muted-foreground">{installResult.primaryConfigHint}</div>
      </CardContent>
    </Card>
  {/if}

  <Alert.Root>
    <Alert.Title>安装的边界（FR-12.6）</Alert.Title>
    <Alert.Description>
      安装只写 ~/.ssh/agent-fleet.conf（控制面侧同一文件的生成副本留在 data-dir 的 generated/ssh/ 供审计与
      diff），绝不改写操作者的主 SSH 配置；要让 ssh/scp 生效，主配置需包含
      <code class="font-mono">Include ~/.ssh/agent-fleet.conf</code>，这一行由操作者自己添加。
      值含空白或引号的连接字段会被服务端拒绝，UI 按 §6.4 错误体原样呈现拒绝理由；
      host-key 校验保持标准策略（FR-12.3）。
    </Alert.Description>
  </Alert.Root>
</div>
