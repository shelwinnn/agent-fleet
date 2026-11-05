// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import MachineDetail from '../src/routes/MachineDetail.svelte';
import type { AdapterCapability, Machine, RenderPreview } from '../src/lib/api/types.js';
import { FleetStore } from '../src/lib/state/store.svelte.js';
import { setLocale } from '../src/lib/i18n/index.js';

let app: ReturnType<typeof mount> | null = null;

function machine(partial: {
  name?: string;
  connected?: 'True' | 'False' | 'Unknown';
  os?: string;
  arch?: string;
  user?: string;
  caps?: AdapterCapability[];
  profileRef?: string;
}): Machine {
  return {
    metadata: { name: partial.name ?? 'lab-1' },
    spec: {
      managementMode: 'agentd',
      ...(partial.profileRef ? { profileRef: partial.profileRef } : {}),
      ...(partial.user ? { ssh: { user: partial.user } } : {}),
    },
    status: {
      os: partial.os ?? 'linux',
      arch: partial.arch ?? 'amd64',
      conditions: partial.connected
        ? [{ type: 'AgentConnected', status: partial.connected, lastTransitionTime: '2026-11-05T00:00:00Z' }]
        : [],
      adapterCapabilities: partial.caps,
    },
  };
}

function storeFor(current: Machine, render?: RenderPreview): FleetStore {
  const store = new FleetStore({
    getMachine: async () => current,
    getDrift: async () => ({
      machine: current.metadata.name,
      drifted: { status: 'Unknown' as const },
      reconciled: { status: 'Unknown' as const },
    }),
    listOperations: async () => ({ items: [] }),
    renderProfile: async () =>
      render ?? {
        profile: current.spec?.profileRef ?? '',
        machine: current.metadata.name,
        digest: '',
        canonicalizationVersion: '',
        desired: {},
      },
  } as never);
  store.machines = [current];
  return store;
}

async function settle(rounds = 6) {
  for (let i = 0; i < rounds; i++) {
    await Promise.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));
    flushSync();
  }
}

async function show(current: Machine, render?: RenderPreview) {
  app = mount(MachineDetail, {
    target: document.body,
    props: { store: storeFor(current, render), name: current.metadata.name },
  });
  await settle();
}

function section(title: string): HTMLElement {
  const heading = [...document.querySelectorAll('h2')].find((item) => item.textContent?.trim() === title);
  const parent = heading?.parentElement;
  if (!parent) throw new Error(`missing section: ${title}`);
  return parent;
}

function textHosts(needle: string): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('body *')].filter((el) =>
    [...el.childNodes].some((node) => node.nodeType === Node.TEXT_NODE && (node.textContent ?? '').includes(needle)),
  );
}

function expectOutsideToneOk(needle: string) {
  const hosts = textHosts(needle);
  expect(hosts.length, `没有「${needle}」就不能把这条当成通过`).toBeGreaterThan(0);
  for (const el of hosts) {
    expect(el.closest('.tone-ok'), `「${needle}」落在成功色里`).toBeNull();
  }
  const painted = [...document.querySelectorAll('.tone-ok')].filter((el) => el.textContent?.includes(needle));
  expect(painted, `成功色元素里出现了「${needle}」`).toEqual([]);
}

const unknownCaps: AdapterCapability[] = [
  { family: 'codex', capability: 'version', state: 'unknown', verifiedVersions: '0.9.1', reason: '结果没有回来' },
  { family: 'claude', capability: 'version', state: 'failed', verifiedVersions: '1.2.3', reason: '查询失败' },
];

beforeEach(() => {
  setLocale('zh-CN');
  document.body.innerHTML = '';
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  setLocale('zh-CN');
  document.body.innerHTML = '';
});

describe('单机页', () => {
  it('面包屑从设备回到机器列表，两节是代理和技能，没有技能时写出那句', async () => {
    await show(machine({ connected: 'True', user: 'alice', caps: unknownCaps }));

    const link = document.querySelector('nav a');
    expect(link?.textContent?.trim()).toBe('设备');
    expect(link?.getAttribute('href')).toBe('#/machines');
    expect(link?.parentElement?.textContent?.trim().startsWith('设备')).toBe(true);
    (link as HTMLAnchorElement).click();
    expect(window.location.hash).toBe('#/machines');

    const meta = document.querySelector('.host-meta');
    expect(meta?.textContent).toContain('在线');
    expect(meta?.textContent).toContain('linux');
    expect(meta?.textContent).toContain('amd64');
    expect(meta?.textContent).toContain('alice');

    expect([...document.querySelectorAll('h2')].map((item) => item.textContent?.trim())).toEqual(['代理', '技能']);
    expect([...section('代理').querySelectorAll('th')].map((item) => item.textContent?.trim())).toEqual([
      '名称',
      '版本',
      '状态',
      '说明',
    ]);
    expect(section('技能').textContent).toContain('这台机器上还没有技能');
  });

  it('夹具里的未知状态保留上次看到，并且未知和失败版本都不在 tone-ok 里', async () => {
    await show(machine({ connected: 'True', user: 'alice', caps: unknownCaps }));

    expect(document.body.textContent).toContain('上次看到 0.9.1');
    expect(document.body.textContent).not.toContain('1.2.3');
    expectOutsideToneOk('未知');
    expectOutsideToneOk('上次看到');
    expectOutsideToneOk('无');
    expect(textHosts('在线').some((el) => el.closest('.tone-ok'))).toBe(true);
  });

  it('没有未知行时不把未知检查当成通过', async () => {
    await show(
      machine({
        connected: 'True',
        user: 'alice',
        caps: [{ family: 'codex', capability: 'version', state: 'verified', verifiedVersions: '0.55.0' }],
      }),
    );

    expect(document.body.textContent).toContain('0.55.0');
    expect(document.body.textContent).toContain('已安装');
    expect(textHosts('未知')).toEqual([]);
    expect(textHosts('上次看到')).toEqual([]);
  });

  it('离线机器显示离线，技能有修订时不写空态', async () => {
    const current = machine({
      connected: 'False',
      user: 'alice',
      profileRef: 'dev',
      caps: [{ family: 'codex', capability: 'version', state: 'verified', verifiedVersions: '0.55.0' }],
    });
    const render: RenderPreview = {
      profile: 'dev',
      machine: current.metadata.name,
      digest: 'sha256:abc',
      canonicalizationVersion: '1',
      desired: { skills: { review: { revision: 'v3' } } },
    };
    await show(current, render);

    const meta = document.querySelector('.host-meta');
    expect(meta?.textContent).toContain('离线');
    expect(meta?.textContent).not.toContain('在线');
    expect(document.body.textContent).not.toContain('这台机器上还没有技能');
    expect(section('技能').textContent).toContain('review');
    expect(section('技能').textContent).toContain('v3');
  });

  it('英文下未知同样不在成功色里', async () => {
    setLocale('en');
    await show(machine({ connected: 'True', user: 'alice', caps: unknownCaps }));

    expect(document.querySelector('nav a')?.textContent?.trim()).toBe('Devices');
    expect([...document.querySelectorAll('h2')].map((item) => item.textContent?.trim())).toEqual(['Agents', 'Skills']);
    expect(section('Skills').textContent).toContain('This machine has no skills yet.');
    expect(document.body.textContent).toContain('Last seen 0.9.1');
    expect(document.body.textContent).not.toContain('1.2.3');
    expectOutsideToneOk('Unknown');
    expectOutsideToneOk('Last seen');
  });
});
