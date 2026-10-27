/**
 * i18n 对外入口：组件只从这里导入 `t` / `i18n` / `setLocale` 等。
 *
 * `t` 在调用瞬间读取 `i18n.locale`（runes 状态）——在组件模板或 $derived
 * 里调用时即建立响应依赖，切换语言后所有在屏文案立即更新。
 */
import { translate, type MessageKey, type MessageParams } from './messages.js';
import { i18n } from './locale.svelte.js';

export * from './messages.js';
export { i18n, initLocale, setLocale, LOCALE_STORAGE_KEY } from './locale.svelte.js';

export function t(key: MessageKey, params?: MessageParams): string {
  return translate(i18n.locale, key, params);
}
