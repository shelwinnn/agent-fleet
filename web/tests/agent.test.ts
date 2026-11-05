// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import { initLocale } from '../src/lib/i18n/index.js';
import { MockLifecycleBackend } from '../src/lib/api/lifecycleMock.js';
import { InventoryNotWiredError } from '../src/lib/state/lifecycleGateway.js';
import type { LifecycleGateway } from '../src/lib/state/lifecycleGateway.js';
import type { Machine } from '../src/lib/api/types.js';
import { CODEX_PACKAGE } from '../src/lib/state/lifecycle.js';
import Lifecycle from '../src/routes/Lifecycle.svelte';

let app: ReturnType<typeof mount> | null = null;

function machine(name: string): Machine {
  return {
    metadata: { name },
    spec: { managementMode: 'agentd' },
    status: { os: 'linux', arch: 'amd64' },
  };
}

async function settle(rounds = 8) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));
    flushSync();
  }
}

function buttonByText(text: string): HTMLButtonElement {
  const el = [...document.body.querySelectorAll('button')].find((node) => node.textContent?.trim() === text);
  if (!el) throw new Error(`missing button: ${text}`);
  return el as HTMLButtonElement;
}

function mountPage(machines: Machine[], gateway: LifecycleGateway) {
  app = mount(Lifecycle, {
    target: document.body,
    props: { machines, gateway },
  });
  flushSync();
}

beforeEach(() => {
  window.localStorage.clear();
  document.body.innerHTML = '';
  initLocale();
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  document.body.innerHTML = '';
});

describe('代理页', () => {
  it('没有安装时卸载禁用，未知行不在 tone-ok 里', async () => {
    const backend = new MockLifecycleBackend();
    const gateway: LifecycleGateway = {
      getInstanceInventory: async () => {
        throw new InventoryNotWiredError();
      },
      getDesired: (name) => backend.getDesired(name),
      putDesired: (name, update) => backend.putDesired(name, update),
      getCapabilities: () => backend.getCapabilities(),
      createOperation: (req) => backend.createOperation(req),
      getOperation: (id) => backend.getOperation(id),
      confirmOperation: (id) => backend.confirmOperation(id),
      retryOperation: (id, req) => backend.retryOperation(id, req),
      checkUpstream: () => backend.checkUpstream(),
    };
    mountPage([machine('empty-host')], gateway);
    await settle();

    expect(buttonByText('检查更新').textContent?.trim()).toBe('检查更新');
    expect(buttonByText('卸载').disabled).toBe(true);
    for (const label of ['成功', '失败', '未执行', '未知']) {
      expect(document.body.textContent).toContain(label);
    }
    expect(document.body.textContent).toContain('每台的结果');
    expect(document.querySelector('a[href="#/apps"]')?.textContent).toBe('应用');

    const row = document.querySelector('.agent-result-row');
    expect(row?.textContent).toContain('未知');
    expect(row?.closest('.tone-ok')).toBeNull();
    expect(row?.querySelector('.tone-ok')).toBeNull();
    const unknown = [...(row?.querySelectorAll('[class*="tone-"]') ?? [])].find((el) => el.textContent?.includes('未知'));
    expect(unknown?.className).not.toContain('tone-ok');
    expect(unknown?.className).toContain('tone-warn');
  });

  it('已经是固定版本的机器不能选，预览不安装，失败的上游行没有可用版本', async () => {
    const backend = new MockLifecycleBackend();
    backend.inventory['mac-ci'].instances = [
      {
        env: '',
        user: 'ci2',
        agent: 'codex',
        method: 'npm',
        package: CODEX_PACKAGE,
        version: '0.56.1',
      },
    ];
    const gateway: LifecycleGateway = {
      getInstanceInventory: (name) => backend.getInstanceInventory(name),
      getDesired: (name) => backend.getDesired(name),
      putDesired: (name, update) => backend.putDesired(name, update),
      getCapabilities: () => backend.getCapabilities(),
      createOperation: (req) => backend.createOperation(req),
      getOperation: (id) => backend.getOperation(id),
      confirmOperation: (id) => backend.confirmOperation(id),
      retryOperation: (id, req) => backend.retryOperation(id, req),
      checkUpstream: () => backend.checkUpstream(),
    };
    mountPage([machine('mac-ci'), machine('linux-build')], gateway);
    await settle();

    buttonByText('检查更新').click();
    await settle();

    const failedRows = [...document.querySelectorAll('.agent-upstream-row')].filter(
      (row) => row.getAttribute('data-kind') === 'failed',
    );
    expect(failedRows.length).toBeGreaterThan(0);
    for (const row of failedRows) {
      expect(row.textContent).not.toContain('上游可用版本');
    }
    expect(document.querySelector('.agent-upstream-row[data-kind="ok"]')?.textContent).toContain('上游可用版本');

    const upgrade = [...document.querySelectorAll('button')].find((node) =>
      node.textContent?.trim().startsWith('安装或升级到'),
    );
    expect(upgrade?.textContent?.trim()).toBe('安装或升级到 0.56.1');
    upgrade?.click();
    flushSync();

    expect(document.body.textContent).toContain('已是这个版本的机器不能再选。');
    const pinned = document.querySelector<HTMLInputElement>('input[data-machine="mac-ci"]');
    const older = document.querySelector<HTMLInputElement>('input[data-machine="linux-build"]');
    expect(pinned?.disabled).toBe(true);
    expect(older?.disabled).toBe(false);
    expect(buttonByText('预览').disabled).toBe(true);

    older!.checked = true;
    older!.dispatchEvent(new Event('change', { bubbles: true }));
    flushSync();
    expect(buttonByText('预览').disabled).toBe(false);

    const before = backend.operations.size;
    buttonByText('预览').click();
    flushSync();
    expect(document.querySelector('.agent-preview-row')?.textContent).toBe('linux-build');
    expect(backend.operations.size).toBe(before);
    expect(backend.createKeyLog).toHaveLength(0);

    buttonByText('返回').click();
    flushSync();
    expect(document.querySelector('input[data-machine="linux-build"]')).toBeTruthy();
    expect(document.querySelector('.agent-preview-row')).toBeNull();
  });
});
