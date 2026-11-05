// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import Overview from '../src/routes/Overview.svelte';
import { FleetStore } from '../src/lib/state/store.svelte.js';
import { setLocale } from '../src/lib/i18n/index.js';
import type { Machine } from '../src/lib/api/types.js';

let view: ReturnType<typeof mount> | null = null;

function countValue(label: string): string {
  const card = [...document.querySelectorAll('.overview-count')].find((el) => el.textContent?.includes(label));
  return card?.querySelector('.overview-count-value')?.textContent?.trim() ?? '';
}

function buttonByText(text: string): HTMLButtonElement {
  const el = [...document.body.querySelectorAll('button')].find((button) => button.textContent?.includes(text));
  if (!el) throw new Error(`missing button: ${text}`);
  return el as HTMLButtonElement;
}

beforeEach(() => {
  setLocale('zh-CN');
  window.location.hash = '#/';
  document.body.innerHTML = '';
});

afterEach(() => {
  if (view) unmount(view);
  view = null;
  document.body.innerHTML = '';
});

function mountOverview(store: FleetStore) {
  view = mount(Overview, { target: document.body, props: { store } });
  flushSync();
}

const at = '2026-09-23T08:00:00Z';

function unknownVersionMachine(): Machine {
  return {
    metadata: { name: 'ws-unknown' },
    spec: { managementMode: 'agentd', profileRef: 'dev' },
    status: {
      conditions: [
        { type: 'AgentConnected', status: 'True', lastTransitionTime: at },
        { type: 'Drifted', status: 'False', lastTransitionTime: at },
        { type: 'Degraded', status: 'False', lastTransitionTime: at },
      ],
    },
  };
}

describe('总览', () => {
  it('空数据仍显示四个计数、两张表和打开 Codex', () => {
    mountOverview(new FleetStore());
    const text = document.body.textContent ?? '';
    for (const label of ['设备', '在线', '失败', '版本未知']) {
      expect(text).toContain(label);
    }
    expect(text).toContain('要处理');
    expect(text).toContain('可以更新');
    expect(text).toContain('打开 Codex');
    expect(text).toContain('先处理失败和未知。可以更新的列在下面。');

    const handle = document.getElementById('overview-handle')?.closest('section');
    for (const column of ['机器', '代理', '说明', '状态']) {
      expect(handle?.textContent).toContain(column);
    }
    const updates = document.getElementById('overview-updates')?.closest('section');
    expect(updates?.textContent).toContain('机器');
    expect(updates?.textContent).toContain('变化');
    expect(handle?.querySelector('tbody')?.children.length).toBe(0);
    expect(updates?.querySelector('tbody')?.children.length).toBe(0);
  });

  it('设备进入机器列表，打开 Codex 进入代理页', () => {
    mountOverview(new FleetStore());
    buttonByText('设备').click();
    flushSync();
    expect(window.location.hash).toBe('#/machines');

    window.location.hash = '#/';
    buttonByText('打开 Codex').click();
    flushSync();
    expect(window.location.hash).toBe('#/lifecycle');
  });

  it('没有版本的机器用现有未知色，不画成已确认一致', () => {
    const store = new FleetStore();
    store.machines = [unknownVersionMachine()];
    store.profiles = [
      { metadata: { name: 'dev' }, spec: { agents: { codex: { enabled: true, version: '1.2.3' } } } },
    ];
    mountOverview(store);

    expect(countValue('设备')).toBe('1');
    expect(countValue('在线')).toBe('1');
    expect(countValue('失败')).toBe('0');
    expect(countValue('版本未知')).toBe('1');

    const handle = document.getElementById('overview-handle')?.closest('section');
    expect(handle?.textContent).toContain('ws-unknown');
    expect(handle?.textContent).toContain('codex');
    expect(document.body.textContent).not.toContain('已确认一致');
    const unknownAsOk = [...document.querySelectorAll('.tone-ok')].some((el) => el.textContent?.includes('未知'));
    expect(unknownAsOk).toBe(false);
    const unknownAsWarn = [...document.querySelectorAll('.tone-warn')].some((el) => el.textContent?.includes('未知'));
    expect(unknownAsWarn).toBe(true);
  });

  it('已漂移的机器出现在可以更新，未知不进成功色', () => {
    const store = new FleetStore();
    store.machines = [
      unknownVersionMachine(),
      {
        metadata: { name: 'ws-update' },
        spec: { managementMode: 'agentd', profileRef: 'dev' },
        status: {
          conditions: [
            { type: 'AgentConnected', status: 'True', lastTransitionTime: at },
            { type: 'Drifted', status: 'True', lastTransitionTime: at },
            { type: 'Degraded', status: 'False', lastTransitionTime: at },
          ],
          agentdVersion: '1.0.0',
          desiredProjectionDigest: 'sha256:aaaaaaaa',
          observedProjectionDigest: 'sha256:bbbbbbbb',
        },
      },
    ];
    mountOverview(store);

    const handle = document.getElementById('overview-handle')?.closest('section');
    const updates = document.getElementById('overview-updates')?.closest('section');
    expect(handle?.textContent).toContain('ws-unknown');
    expect(handle?.textContent).not.toContain('ws-update');
    expect(updates?.textContent).toContain('ws-update');
    expect(updates?.textContent).toContain('变化');
    expect(countValue('设备')).toBe('2');
    expect(countValue('版本未知')).toBe('1');
    const unknownAsOk = [...document.querySelectorAll('.tone-ok')].some((el) => el.textContent?.includes('未知'));
    expect(unknownAsOk).toBe(false);
  });
});
