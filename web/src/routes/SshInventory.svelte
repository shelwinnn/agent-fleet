<script lang="ts">
  /**
   * SSH Inventory（spec §24.7）：Fleet 机器别名 + 渲染后的 OpenSSH include 预览；
   * 动作 Export（下载预览）与"确认后安装/更新 include 文件"。
   *
   * 边界：include 文件的**渲染端点（§7.7）与安装动作属第 6 片**，服务端当前未提供。
   * 因此这里只按标准 OpenSSH 语法在前端生成预览（可导出），安装按钮禁用并写明原因——
   * 不假装已经写了 ~/.ssh/config。host-key 校验保持标准策略（FR-12.3）。
   */
  import type { FleetStore } from '$lib/state/store.svelte.js';
  import { Card, CardContent, CardHeader, CardTitle } from '$lib/components/ui/card/index.js';
  import * as Table from '$lib/components/ui/table/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import * as Alert from '$lib/components/ui/alert/index.js';
  import StateBadge from '$lib/components/StateBadge.svelte';
  import AsyncState from '$lib/components/AsyncState.svelte';
  import { machineRows } from '$lib/state/machines.js';
  import { createClock } from '$lib/state/clock.svelte.js';
  import { navigate } from '$lib/state/router.svelte.js';
  import { formatTime } from '$lib/state/format.js';

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

  const includePreview = $derived(
    [
      '# 由 agent-fleet Web UI 生成（预览）。安装动作属第 6 片（服务端渲染端点 §7.7 尚未提供）。',
      '# 生成时间：' + formatTime(new Date()),
      '',
      ...aliases.map((a) =>
        [
          `Host ${a.alias}`,
          `    HostName ${a.hostName}`,
          a.user ? `    User ${a.user}` : null,
          a.port ? `    Port ${a.port}` : null,
          a.identityFile ? `    IdentityFile ${a.identityFile}` : null,
          '    # host-key 校验保持标准策略（不设置 StrictHostKeyChecking=no，FR-12.3）',
          '',
        ]
          .filter((line): line is string => line !== null)
          .join('\n'),
      ),
    ].join('\n'),
  );

  function download() {
    const blob = new Blob([includePreview], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = 'agent-fleet-inventory.conf';
    link.click();
    URL.revokeObjectURL(url);
  }
</script>

<div class="grid gap-5">
  <div>
    <h1 class="text-lg font-semibold">SSH Inventory</h1>
    <p class="text-xs text-muted-foreground">
      Fleet 机器别名与 OpenSSH include 预览（spec §24.7）。当前限制：无证书吊销（删除机器即拒绝其证书）。
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
      <CardTitle class="text-sm">OpenSSH include 预览</CardTitle>
      <p class="text-xs text-muted-foreground">
        前端按标准 OpenSSH 语法生成：只使用机器 spec.ssh 中已配置的字段，缺失项不填默认值。
      </p>
    </CardHeader>
    <CardContent class="grid gap-3">
      <pre class="max-h-80 overflow-auto rounded-md border bg-muted/40 p-3 text-xs">{includePreview}</pre>
      <div class="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" onclick={download}>Export（下载 .conf）</Button>
        <Button
          size="sm"
          disabled
          title="安装/更新 managed include 文件属第 6 片：服务端渲染端点（§7.7）与写入路径当前未提供，且写入远端文件需要显式确认"
        >
          安装/更新 include 文件（需显式确认）
        </Button>
      </div>
    </CardContent>
  </Card>

  <Alert.Root>
    <Alert.Title>为什么不能直接"安装"</Alert.Title>
    <Alert.Description>
      安装动作会改动远端机器上的 ~/.ssh/config（受管 include 文件）。架构 §7.7 要求该路径由服务端渲染并
      经受控通道写入，本片（第 5 片）只交付只读预览与导出；第 6 片落地 SSH 路径后再提供"显式确认后安装"。
      本页不提供任何绕过确认的入口。
    </Alert.Description>
  </Alert.Root>
</div>
