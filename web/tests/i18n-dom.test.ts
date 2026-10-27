// @vitest-environment happy-dom
/**
 * i18n 的 DOM 行为（KM-43）：真实挂载组件后切换语言，当前页面立即更新
 * （不重挂载、不刷新）；语言偏好写入 localStorage 并在 initLocale 时恢复；
 * <html lang> 随之同步（屏幕阅读器跟随）。
 */
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { mount, unmount, flushSync } from 'svelte';
import { initLocale, setLocale, i18n, LOCALE_STORAGE_KEY } from '../src/lib/i18n/index.js';
import Harness from './Harness.svelte';

let app: ReturnType<typeof mount> | null = null;

beforeEach(() => {
  window.localStorage.clear();
  initLocale();
});

afterEach(() => {
  if (app) {
    unmount(app);
    app = null;
  }
});

describe('语言切换与持久化（浏览器行为）', () => {
  it('挂载后点击切换控件，当前页面立即更新为英文（无刷新）', () => {
    app = mount(Harness, { target: document.body });
    expect(document.body.querySelector('[data-testid="nav"]')!.textContent).toBe('机器');
    expect(document.body.querySelector('[data-testid="time"]')!.textContent).toBe('3 分钟前');

    document.body.querySelector<HTMLButtonElement>('[data-testid="switch"]')!.click();
    flushSync();

    expect(i18n.locale).toBe('en');
    expect(document.body.querySelector('[data-testid="nav"]')!.textContent).toBe('Machines');
    expect(document.body.querySelector('[data-testid="time"]')!.textContent).toBe('3 minutes ago');
    expect(document.documentElement.lang).toBe('en');
  });

  it('切换后偏好写入 localStorage；再次 initLocale 恢复所选语言', () => {
    setLocale('en');
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe('en');

    // 模拟下次访问：全新状态重新初始化
    i18n.locale = 'zh-CN';
    initLocale();
    expect(i18n.locale).toBe('en');
  });

  it('存储中的非法值不生效，回落 zh-CN 默认', () => {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, 'fr');
    initLocale();
    expect(i18n.locale).toBe('zh-CN');
    expect(document.documentElement.lang).toBe('zh-CN');
  });

  it('首次访问（无存储）默认 zh-CN', () => {
    initLocale();
    expect(i18n.locale).toBe('zh-CN');
    expect(document.documentElement.lang).toBe('zh-CN');
  });
});
