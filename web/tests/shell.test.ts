// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import App from '../src/App.svelte';
import { fleetStore } from '../src/lib/state/fleet.svelte.js';
import { setLocale } from '../src/lib/i18n/index.js';

let app: ReturnType<typeof mount> | null = null;

function buttonByText(text: string): HTMLButtonElement {
  const el = [...document.body.querySelectorAll('button')].find((b) => b.textContent?.trim() === text);
  if (!el) throw new Error(`missing button: ${text}`);
  return el as HTMLButtonElement;
}

beforeEach(() => {
  setLocale('zh-CN');
  window.location.hash = '#/';
  document.body.innerHTML = '';
  fleetStore.machines = [];
  vi.stubGlobal(
    'fetch',
    () =>
      Promise.resolve(
        new Response(JSON.stringify({ items: [] }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        }),
      ),
  );
});

afterEach(() => {
  if (app) unmount(app);
  app = null;
  fleetStore.stop();
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

function mountApp() {
  app = mount(App, { target: document.body });
  flushSync();
}

describe('控制台外壳', () => {
  it('锁头是 agent-fleet，导航有总览、设备、应用、技能，收起后页脚改成展开菜单', () => {
    mountApp();
    expect(document.title).toBe('agent-fleet');
    expect(document.querySelector('.console-word')?.textContent).toBe('agent-fleet');
    const logo = document.querySelector('.logo-mark');
    expect(logo?.querySelectorAll('text, tspan').length).toBe(0);
    expect(logo?.textContent?.trim()).toBe('');

    const nav = document.querySelector('nav');
    expect(nav).toBeTruthy();
    for (const label of ['总览', '资源', '设备', '软件', '应用', '技能', '收起菜单']) {
      expect(nav?.textContent).toContain(label);
    }
    for (const hidden of ['配置档', '发布', 'SSH 清单', 'Codex 维护']) {
      expect(nav?.textContent).not.toContain(hidden);
    }

    buttonByText('收起菜单').click();
    flushSync();
    expect(buttonByText('展开菜单').textContent?.trim()).toBe('展开菜单');
    for (const label of ['总览', '资源', '设备', '软件', '应用', '技能']) {
      expect(nav?.textContent).not.toContain(label);
    }

    buttonByText('展开菜单').click();
    flushSync();
    expect(nav?.textContent).toContain('收起菜单');
    expect(nav?.textContent).toContain('总览');
  });

  it('范围列出全部设备，帮助列出四种结果，斜杠聚焦搜索', () => {
    mountApp();
    buttonByText('我的 0 台').click();
    flushSync();
    expect(document.body.textContent).toContain('全部设备');

    document.querySelector<HTMLButtonElement>('[aria-label="帮助"]')!.click();
    flushSync();
    for (const word of ['成功', '失败', '未执行', '未知']) {
      expect(document.body.textContent).toContain(word);
    }
    const unknown = [...document.body.querySelectorAll('.tone-warn')].some((el) => el.textContent?.includes('未知'));
    expect(unknown).toBe(true);
    const unknownAsOk = [...document.body.querySelectorAll('.tone-ok')].some((el) => el.textContent?.includes('未知'));
    expect(unknownAsOk).toBe(false);

    const search = document.querySelector<HTMLInputElement>('#console-search');
    search?.blur();
    window.dispatchEvent(new KeyboardEvent('keydown', { key: '/', bubbles: true }));
    expect(document.activeElement).toBe(search);
  });

  it('应用只有标题，生命周期仍画在外壳里，换路由会关掉帮助', () => {
    mountApp();
    document.querySelector<HTMLButtonElement>('[aria-label="帮助"]')!.click();
    flushSync();
    expect(document.body.textContent).toContain('成功');

    window.location.hash = '#/apps';
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    flushSync();
    expect(document.querySelector('h1')?.textContent?.trim()).toBe('应用');
    expect(document.querySelector('.console-word')?.textContent).toBe('agent-fleet');
    expect(document.body.textContent).not.toContain('成功');

    window.location.hash = '#/lifecycle';
    window.dispatchEvent(new HashChangeEvent('hashchange'));
    flushSync();
    expect(document.querySelector('.logo-mark')).toBeTruthy();
    expect(document.querySelector('nav')?.textContent).not.toContain('Codex 维护');
    expect(document.body.textContent).toContain('Codex 维护');
  });
});
