// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import App from '../src/App.svelte';
import Apps from '../src/routes/Apps.svelte';
import type { AgentProfile, Machine, UpstreamVersionReport } from '../src/lib/api/types.js';
import { fleetStore } from '../src/lib/state/fleet.svelte.js';
import { setLocale } from '../src/lib/i18n/index.js';
import { translate } from '../src/lib/i18n/messages.js';

let app: ReturnType<typeof mount> | null = null;

const profile: AgentProfile = {
  metadata: { name: 'dev' },
  spec: { agents: { codex: { version: '0.55.0' }, grok: { version: '1.0.0' } } },
};

const machine: Machine = {
  metadata: { name: 'nuc' },
  spec: { profileRef: 'dev', managementMode: 'agentd' },
  status: { adapterCapabilities: [{ family: 'opencode', capability: 'skills', state: 'unverified' }] },
};

function jsonResponse(body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'content-type': 'application/json' },
    }),
  );
}

function mixedReport(): UpstreamVersionReport {
  return {
    agent: 'codex',
    checkedAt: '2026-11-01T00:00:00Z',
    sources: [
      {
        agentId: 'codex',
        method: 'npm',
        channel: 'latest',
        status: 'ok',
        version: { raw: '1.2.3', scheme: 'semver' },
        queriedAt: '2026-11-01T00:00:00Z',
      },
      {
        agentId: 'codex',
        method: 'brew',
        channel: 'stable',
        status: 'failed',
        version: { raw: '9.9.9', scheme: 'semver' },
        failure: { reason: 'query_failed' },
        lastSuccess: { version: { raw: '0.1.0', scheme: 'semver' }, queriedAt: '2026-10-01T00:00:00Z' },
      },
    ],
  };
}

const successLine = `${translate('zh-CN', 'lc.upstream.available')}：`;

beforeEach(() => {
  setLocale('zh-CN');
  window.location.hash = '#/apps';
  document.body.innerHTML = '';
  fleetStore.stop();
  fleetStore.machines = [];
  fleetStore.profiles = [];
  fleetStore.loading = {};
  fleetStore.errors = {};
});

afterEach(async () => {
  if (app) unmount(app);
  app = null;
  fleetStore.stop();
  fleetStore.machines = [];
  fleetStore.profiles = [];
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  await new Promise((resolve) => setTimeout(resolve, 0));
});

async function settle() {
  for (let i = 0; i < 8; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
    flushSync();
  }
}

function columnNames(): string[] {
  return [...document.querySelectorAll('th')].map((cell) => cell.textContent?.trim() ?? '');
}

describe('应用列表', () => {
  it('标题是应用，导语和四列名可见', () => {
    app = mount(Apps, { target: document.body });
    flushSync();

    expect(document.querySelector('h1')?.textContent?.trim()).toBe('应用');
    expect(document.querySelector('.apps-lead')?.textContent?.trim()).toBe('要安装到机器上的代理。');
    expect(columnNames()).toEqual(['名称', '可用版本', '覆盖', '状态']);
  });

  it('失败来源不显示成功时的可用版本行', async () => {
    fleetStore.profiles = [profile];
    fleetStore.machines = [machine];
    vi.stubGlobal('fetch', (input: RequestInfo) => {
      const url = String(input);
      if (url.includes('/upstream-versions/check') && url.includes('/codex/')) return jsonResponse(mixedReport());
      if (url.includes('/upstream-versions/check')) {
        return jsonResponse({
          agent: 'other',
          checkedAt: '2026-11-01T00:00:00Z',
          sources: [
            {
              agentId: 'other',
              method: 'binary',
              channel: 'latest',
              status: 'failed',
              failure: { reason: 'query_failed' },
            },
          ],
        } satisfies UpstreamVersionReport);
      }
      if (url.includes('/lifecycle/desired')) {
        return jsonResponse({
          schemaVersion: 'lifecycle-v1',
          entries: { grok: { intent: 'present', version: '1.0.0', method: 'npm', package: 'grok' } },
          revision: 1,
          digest: 'sha256:abc',
        });
      }
      return jsonResponse({ items: [] });
    });

    app = mount(Apps, { target: document.body });
    flushSync();
    await settle();

    const codex = document.querySelector('tr[data-agent="codex"]');
    expect(codex).toBeTruthy();
    const failed = codex?.querySelector('[data-kind="failed"]');
    const ok = codex?.querySelector('[data-kind="ok"]');
    expect(ok?.textContent).toContain(successLine);
    expect(ok?.textContent).toContain('1.2.3');
    expect(failed?.textContent).not.toContain(successLine);
    expect(failed?.textContent).not.toContain('9.9.9');
    expect(failed?.textContent).not.toContain('1.2.3');
    expect(failed?.querySelector('.tone-ok')).toBeNull();

    const status = codex?.querySelector('[data-col="status"]');
    expect(status?.querySelector('.tone-ok')).toBeNull();
    expect(status?.textContent).toContain('未知');

    for (const agent of ['grok', 'opencode']) {
      const row = document.querySelector(`tr[data-agent="${agent}"]`);
      const source = row?.querySelector('[data-kind="failed"]');
      expect(source?.textContent).toContain('可用版本未知');
      expect(source?.textContent ?? '').not.toContain(successLine);
      expect(row?.querySelector('[data-col="status"] .tone-ok')).toBeNull();
    }
  });

  it('点一行进入该代理路由，锁头还在；配置档和 SSH 清单仍能打开且不进导航', async () => {
    vi.stubGlobal('fetch', (input: RequestInfo) => {
      const url = String(input);
      if (url.includes('/api/v1/profiles')) return jsonResponse({ items: [profile] });
      if (url.includes('/upstream-versions/check')) {
        return jsonResponse({
          agent: 'codex',
          checkedAt: '2026-11-01T00:00:00Z',
          sources: [
            {
              agentId: 'codex',
              method: 'npm',
              channel: 'latest',
              status: 'failed',
              failure: { reason: 'timeout' },
            },
          ],
        } satisfies UpstreamVersionReport);
      }
      return jsonResponse({ items: [] });
    });

    app = mount(App, { target: document.body });
    flushSync();
    window.location.hash = '#/apps';
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    flushSync();
    await settle();

    expect(document.querySelector('h1')?.textContent?.trim()).toBe('应用');
    expect(document.querySelector('.apps-lead')?.textContent).toContain('要安装到机器上的代理。');
    expect(columnNames()).toEqual(['名称', '可用版本', '覆盖', '状态']);
    expect(document.querySelector('nav')?.textContent).not.toContain('配置档');
    expect(document.querySelector('nav')?.textContent).not.toContain('SSH 清单');

    const row = document.querySelector<HTMLTableRowElement>('tr[data-agent="codex"]');
    expect(row).toBeTruthy();
    expect(row?.querySelector('[data-kind="failed"]')?.textContent ?? '').not.toContain(successLine);
    row?.click();
    flushSync();

    expect(window.location.hash).toBe('#/lifecycle/codex');
    expect(document.querySelector('.console-word')?.textContent).toBe('Agent Fleet');
    expect(document.querySelector('.console-lockup')).toBeTruthy();

    window.location.hash = '#/profiles';
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    flushSync();
    expect(document.body.textContent).toContain('配置档');
    expect(document.querySelector('nav')?.textContent).not.toContain('配置档');

    window.location.hash = '#/ssh-inventory';
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    flushSync();
    expect(document.querySelector('h1')?.textContent?.trim()).toBe('SSH 清单');
    expect(document.querySelector('nav')?.textContent).not.toContain('SSH 清单');
    expect(document.querySelector('.console-word')?.textContent).toBe('Agent Fleet');
  });
});
