<script lang="ts">
  /**
   * SSH Inventory：机器别名 + 服务端渲染的 OpenSSH include 预览；
   * 动作 Export（下载）与"显式确认后安装/更新 include 文件"。
   *
   * include 内容以服务端渲染为唯一权威，前端不自拼。边界：
   *  - 安装必须显式确认；服务端只写 ~/.ssh/agent-fleet.conf，不改写主配置；
   *  - 连接值含空白或引号会被服务端拒绝渲染，拒绝理由如实呈现；
   *  - host-key 校验保持严格。
   *
   * 全部文案经 i18n 取值，切换语言立即跟随。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { Label } from '$lib/components/ui/label/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { machineRows, sshStatusLabel } from '$lib/state/machines.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { actionOutcomeTitle, failureText, formatSkippedHosts, loadErrorText } from '$lib/state/ssh.js';
  import { shortDigest } from '$lib/state/format.js';
  import type { IncludeInstallResult, IncludePreview } from '$lib/api/types.js';
  import { t, i18n, formatNumber, type MessageKey, type MessageParams } from '$lib/i18n/index.js';

  interface Props {
    store: FleetStore;
  }
  let { store }: Props = $props();

  const clock = createClock(30_000);
  const loc = $derived(i18n.locale);
  const rows = $derived(machineRows(store.machines, clock.now, loc));
  const loadError = $derived(
    store.errors.machines !== undefined
      ? `${t('machines.error.load')}${loadErrorText(store.errors.machines, loc)} ${t('error.loadRetryHint')}`
      : undefined,
  );

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
        hostName: str(ssh.hostName) ?? str(ssh.host) ?? t('sshinv.notSet'),
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
  let previewError = $state<unknown>(null);
  let exporting = $state(false);
  let installConfirmed = $state(false);
  let installing = $state(false);
  let installResult = $state<IncludeInstallResult | null>(null);

  /** 动作反馈：成功存 key+params（渲染期翻译）；失败存原始错误（渲染期本地化）。 */
  type Message =
    | { kind: 'ok'; titleKey: MessageKey; key: MessageKey; params: MessageParams }
    | { kind: 'error'; prefixKey: MessageKey; error: unknown };
  let actionMessage = $state<Message | null>(null);

  async function loadPreview() {
    previewLoading = true;
    previewError = null;
    try {
      preview = await store.client.sshIncludePreview();
    } catch (err) {
      preview = null;
      previewError = err;
    } finally {
      previewLoading = false;
    }
  }

  $effect(() => {
    void loadPreview();
  });

  /** Export（下载）：取服务端渲染产物并触发下载。 */
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
        titleKey: 'ok.exported',
        key: 'sshinv.export.ok',
        params: {
          included: formatNumber(file.includedHosts, loc),
          skipped: formatNumber(file.skippedHosts, loc),
        },
      };
    } catch (err) {
      actionMessage = { kind: 'error', prefixKey: 'sshinv.export.failed', error: err };
    } finally {
      exporting = false;
    }
  }

  /** 安装：必须显式确认；成功后重载预览与最新摘要。 */
  async function installInclude() {
    installing = true;
    actionMessage = null;
    installResult = null;
    try {
      installResult = await store.client.sshIncludeInstall();
      actionMessage = {
        kind: 'ok',
        titleKey: 'ok.installed',
        key: 'sshinv.install.ok',
        params: { path: installResult.path, digest: shortDigest(installResult.contentDigest) },
      };
      installConfirmed = false;
      await loadPreview();
    } catch (err) {
      actionMessage = { kind: 'error', prefixKey: 'sshinv.install.failed', error: err };
    } finally {
      installing = false;
    }
  }

  const previewErrorText = $derived(
    previewError !== undefined && previewError !== null
      ? failureText(previewError, i18n.locale)
      : undefined,
  );
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">{t('nav.sshInventory')}</h1>
    <p class="text-xs text-muted-foreground">{t('sshinv.subtitle')}</p>
  </div>

  <Card>
    <CardHeader><CardTitle class="text-sm">{t('sshinv.aliases.title')}</CardTitle></CardHeader>
    <CardContent>
      <AsyncState loading={store.loading.machines} error={loadError} empty={aliases.length === 0 ? t('sshinv.emptyAliases') : undefined} />
      {#if aliases.length > 0}
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head>{t('sshinv.col.alias')}</Table.Head><Table.Head>{t('sshinv.col.hostName')}</Table.Head><Table.Head>{t('sshinv.col.user')}</Table.Head>
              <Table.Head>{t('sshinv.col.port')}</Table.Head><Table.Head>{t('sshinv.col.identityFile')}</Table.Head><Table.Head>{t('sshinv.col.mode')}</Table.Head>
              <Table.Head>{t('sshinv.col.ssh')}</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#each aliases as a (a.machine)}
              <Table.Row>
                <Table.Cell>
                  <button class="underline" onclick={() => navigate(`/machines/${a.machine}`)}>{a.alias}</button>
                  {#if a.alias !== a.machine}
                    <div class="text-xs text-muted-foreground">{t('sshinv.machineRef', { name: a.machine })}</div>
                  {/if}
                </Table.Cell>
                <Table.Cell class="text-xs">{a.hostName}</Table.Cell>
                <Table.Cell class="text-xs">{a.user ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs">{a.port ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs font-mono">{a.identityFile ?? '—'}</Table.Cell>
                <Table.Cell class="text-xs">{a.mode}</Table.Cell>
                <Table.Cell>
                  <StateBadge
                    tone={a.sshStatus === 'True' ? 'ok' : a.sshStatus === 'False' ? 'danger' : 'muted'}
                    label={sshStatusLabel(a.sshStatus, i18n.locale)}
                  />
                </Table.Cell>
              </Table.Row>
            {/each}
          </Table.Body>
        </Table.Root>
      {/if}
    </CardContent>
  </Card>

  <Card>
    <CardHeader>
      <CardTitle class="text-sm">{t('sshinv.preview.title')}</CardTitle>
      <p class="text-xs text-muted-foreground">{t('sshinv.preview.note')}</p>
    </CardHeader>
    <CardContent class="grid gap-3">
      <AsyncState loading={previewLoading} error={previewErrorText} empty={preview && preview.content === '' ? t('sshinv.preview.empty') : undefined} />
      {#if preview}
        <div class="text-xs text-muted-foreground">
          {t('sshinv.preview.counts', {
            included: formatNumber(preview.includedHosts, loc),
            skipped: formatNumber(preview.skippedHosts, loc),
          })}
        </div>
        <pre class="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{preview.content}</pre>
      {/if}
      <div class="flex flex-wrap items-center gap-3">
        <Button size="sm" variant="outline" disabled={exporting || !preview} onclick={exportInclude}>
          {exporting ? t('sshinv.exporting') : t('sshinv.export')}
        </Button>
        <div class="flex items-center gap-2">
          <input
            id="install-confirm"
            type="checkbox"
            class="size-4 accent-primary"
            bind:checked={installConfirmed}
            disabled={installing}
          />
          <Label for="install-confirm" class="text-xs">{t('sshinv.install.confirm')}</Label>
        </div>
        <Button
          size="sm"
          disabled={!installConfirmed || installing || !preview}
          onclick={installInclude}
          title={t('sshinv.install.title')}
        >
          {installing ? t('sshinv.installing') : t('sshinv.install.button')}
        </Button>
      </div>
    </CardContent>
  </Card>

  {#if actionMessage}
    <Alert.Root variant={actionMessage.kind === 'error' ? 'destructive' : 'default'}>
      <Alert.Title>{actionMessage.kind === 'error' ? actionOutcomeTitle(actionMessage.error, i18n.locale) : t(actionMessage.titleKey)}</Alert.Title>
      <Alert.Description>
        {#if actionMessage.kind === 'error'}
          {t(actionMessage.prefixKey)}：{failureText(actionMessage.error, i18n.locale)}
        {:else}
          {t(actionMessage.key, actionMessage.params)}
        {/if}
      </Alert.Description>
    </Alert.Root>
  {/if}

  {#if installResult}
    <Card>
      <CardHeader><CardTitle class="text-sm">{t('sshinv.lastInstall')}</CardTitle></CardHeader>
      <CardContent class="grid gap-1 font-mono text-xs">
        <div>path: {installResult.path}</div>
        <div>contentDigest: {shortDigest(installResult.contentDigest)}</div>
        <div>includedHosts: {installResult.includedHosts.join(t('app.listSeparator')) || '—'}</div>
        <div>skippedHosts: {formatSkippedHosts(installResult.skippedHosts, i18n.locale) || '—'}</div>
        <div>includeDirective: <code>{installResult.includeDirective}</code></div>
        <div class="text-muted-foreground">{installResult.primaryConfigHint}</div>
      </CardContent>
    </Card>
  {/if}

  <Alert.Root>
    <Alert.Title>{t('sshinv.boundary.title')}</Alert.Title>
    <Alert.Description>{t('sshinv.boundary.detail')}</Alert.Description>
  </Alert.Root>
</div>
