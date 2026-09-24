/**
 * 应用级单例 store。
 *
 * 放在模块里而不是 App.svelte 的实例脚本：Svelte 5 runes 模式下组件实例脚本
 * 不再导出绑定，而页面组件需要共享同一个 store。
 */
import { FleetStore } from './store.svelte.js';

export const fleetStore = new FleetStore();
