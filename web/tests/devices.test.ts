// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import App from '../src/App.svelte';
import Machines from '../src/routes/Machines.svelte';
import { fleetStore } from '../src/lib/state/fleet.svelte.js';
import { FleetStore } from '../src/lib/state/store.svelte.js';
import { setLocale } from '../src/lib/i18n/index.js';
import type { Machine } from '../src/lib/api/types.js';

let app: ReturnType<typeof mount> | null = null;

function jsonResponse(body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }),
  );
}

function machine(name: string, connected: 'True' | 'False', drifted: 'True' | 'False' | 'Unknown'): Machine {
  return {
    metadata: { name },
    spec: { managementMode: 'agentd' },
    status: {
      os: name === 'offline-box' ? 'darwin' : 'linux',
      arch: 'amd64',
      homeDir: `/home/${name}`,
      conditions: [
        { type: 'AgentConnected', status: connected, lastTransitionTime: '2026-09-23T08:00:00Z' },
        { type: 'Drifted', status: drifted, reason: drifted === 'Unknown' ? 'StaleObservation' : undefined, lastTransitionTime: '2026-09-23T08:00:00Z' },
        { type: 'InventoryReady', status: 'True', lastTransitionTime: '2026-09-23T08:00:00Z' },
      ],
    },
  };
}

const online = machine('online-box', 'True', 'False');
const offline = machine('offline-box', 'False', 'False');
const attention = machine('attention-box', 'True', 'Unknown');

function chip(label: string): HTMLButtonElement {
  const el = [...document.querySelectorAll<HTMLButtonElement>('.devices-chip')].find((b) => b.textContent?.trim() === label);
  if (!el) throw new Error(`missing chip: ${label}`);
  return el;
}

function names(): string[] {
  return [...document.querySelectorAll('.devices-name')].map((el) => el.textContent?.trim() ?? '');
}

beforeEach(() => {
  setLocale('zh-CN');
  window.location.hash = '#/';
  document.body.innerHTML = '';
  fleetStore.machines = [];
  fleetStore.profiles = [];
  vi.stubGlobal('fetch', () => jsonResponse({ items: [] }));
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  fleetStore.stop();
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

async function openMachines() {
  app = mount(App, { target: document.body });
  flushSync();
  window.location.hash = '#/machines';
  window.dispatchEvent(new HashChangeEvent('hashchange'));
  flushSync();
  await vi.waitFor(() => {
    expect(fleetStore.loading.machines).toBe(false);
  });
  flushSync();
}

describe('设备列表', () => {
  it('标题、四枚芯片、六列、接入机器，以及查无结果', async () => {
    await openMachines();
    expect(document.querySelector('h1')?.textContent?.trim()).toBe('设备');
    expect(document.querySelector('.devices-lead')?.textContent).toBe('选择一台机器，查看已安装的代理和技能。');
    expect([...document.querySelectorAll('.devices-chip')].map((el) => el.textContent?.trim())).toEqual([
      '全部',
      '在线',
      '要处理',
      '离线',
    ]);
    expect([...document.querySelectorAll('.devices-table th')].map((el) => el.textContent?.trim())).toEqual([
      '名称',
      '状态',
      '系统',
      '用户',
      '软件',
      '要处理',
    ]);
    expect(document.querySelector('.devices-primary')?.textContent?.trim()).toBe('接入机器');

    const search = document.querySelector<HTMLInputElement>('#console-search');
    expect(search).toBeTruthy();
    search!.value = '没有这台机器';
    search!.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    expect(document.body.textContent).toContain('没有匹配的机器');
  });

  it('点芯片和页眉搜索会改可见行，空称呼会提示', async () => {
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/api/v1/machines')) return jsonResponse({ items: [online, offline, attention] });
      return jsonResponse({ items: [] });
    });
    await openMachines();
    await vi.waitFor(() => {
      expect(names().sort()).toEqual(['attention-box', 'offline-box', 'online-box']);
    });

    chip('在线').click();
    flushSync();
    expect(chip('在线').getAttribute('aria-pressed')).toBe('true');
    expect(names().sort()).toEqual(['attention-box', 'online-box']);

    chip('要处理').click();
    flushSync();
    expect(chip('要处理').getAttribute('aria-pressed')).toBe('true');
    expect(names().sort()).toEqual(['attention-box', 'offline-box']);

    chip('离线').click();
    flushSync();
    expect(chip('离线').getAttribute('aria-pressed')).toBe('true');
    expect(names()).toEqual(['offline-box']);

    chip('全部').click();
    flushSync();
    expect(chip('全部').getAttribute('aria-pressed')).toBe('true');
    expect(names().sort()).toEqual(['attention-box', 'offline-box', 'online-box']);

    const search = document.querySelector<HTMLInputElement>('#console-search')!;
    search.value = 'darwin';
    search.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    expect(names()).toEqual(['offline-box']);

    search.value = '没有这台机器';
    search.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    expect(names()).toEqual([]);
    expect(document.body.textContent).toContain('没有匹配的机器');

    search.value = '';
    search.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    expect(names().sort()).toEqual(['attention-box', 'offline-box', 'online-box']);

    document.querySelector<HTMLButtonElement>('.devices-primary')!.click();
    flushSync();
    expect(document.querySelector('dialog')).toBeTruthy();
    document.querySelector<HTMLButtonElement>('.devices-dialog button[type="submit"]')!.click();
    flushSync();
    expect(document.body.textContent).toContain('先写一个称呼');
  });

  it('直接挂上页面时，芯片仍按在线、要处理和离线分行', () => {
    const host = document.createElement('div');
    const search = document.createElement('input');
    search.id = 'console-search';
    document.body.append(search, host);
    const store = new FleetStore();
    store.machines = [online, offline, attention];
    app = mount(Machines, { target: host, props: { store } });
    flushSync();

    expect(names().sort()).toEqual(['attention-box', 'offline-box', 'online-box']);
    chip('离线').click();
    flushSync();
    expect(names()).toEqual(['offline-box']);
    expect(document.body.textContent).not.toContain('online-box');
  });
});
