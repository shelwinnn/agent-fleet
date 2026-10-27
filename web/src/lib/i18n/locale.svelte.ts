/**
 * 响应式语言状态（Svelte 5 runes 模块级单例）。
 *
 * - 首次默认 zh-CN（spec 明确不按浏览器语言嗅探）；
 * - 偏好持久化在 localStorage，非法/缺失值一律回落 zh-CN；
 * - setLocale 同步 <html lang>，让屏幕阅读器随之切换发音（spec US16）。
 *
 * 组件/派生值里读 `i18n.locale` 即建立依赖：切换语言后当前页面立即重渲染，
 * 无需刷新。SSR（svelte/server）与 node 测试环境没有 window/document，
 * 相关分支全部显式守卫。
 */
import type { Locale } from './messages.js';

export const LOCALE_STORAGE_KEY = 'agent-fleet.locale';

export const i18n = $state<{ locale: Locale }>({ locale: 'zh-CN' });

function isLocale(value: unknown): value is Locale {
  return value === 'zh-CN' || value === 'en';
}

/** 应用启动时调用一次（main.ts，先于 mount，保证首帧就是所选语言）。 */
export function initLocale(): void {
  let stored: string | null = null;
  if (typeof window !== 'undefined') {
    try {
      stored = window.localStorage.getItem(LOCALE_STORAGE_KEY);
    } catch {
      stored = null; // 存储不可用（隐私模式等）时按首次访问处理
    }
  }
  i18n.locale = isLocale(stored) ? stored : 'zh-CN';
  applyDocumentLocale();
}

export function setLocale(next: Locale): void {
  i18n.locale = next;
  if (typeof window !== 'undefined') {
    try {
      window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
    } catch {
      // 写不进去就只在当前会话生效，不失败
    }
  }
  applyDocumentLocale();
}

function applyDocumentLocale(): void {
  if (typeof document !== 'undefined') document.documentElement.lang = i18n.locale;
}
