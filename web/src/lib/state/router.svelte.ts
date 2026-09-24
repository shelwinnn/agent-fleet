/**
 * 极简 hash 路由（无第三方依赖）。
 *
 * 选择 hash 而非 history API：SPA 静态产物可被任意静态服务器托管，
 * 不需要服务端 rewrite 规则（本切片未把静态资源服务并入控制面进程，
 * 见 KM-25 交付评论的契约/集成缺口清单）。
 */
export interface Route {
  /** 归一化路径，如 `/machines/ws-1`。 */
  path: string;
  segments: string[];
}

function parse(hash: string): Route {
  const raw = hash.replace(/^#/, '');
  const path = raw.startsWith('/') ? raw : `/${raw}`;
  const clean = path.split('?')[0].replace(/\/+$/, '') || '/';
  return { path: clean, segments: clean.split('/').filter(Boolean) };
}

export const router = $state<Route>(parse(typeof location === 'undefined' ? '' : location.hash));

export function initRouter(): void {
  if (typeof window === 'undefined') return;
  window.addEventListener('hashchange', () => {
    const next = parse(window.location.hash);
    router.path = next.path;
    router.segments = next.segments;
  });
}

export function navigate(path: string): void {
  if (typeof window === 'undefined') return;
  window.location.hash = path.startsWith('/') ? path : `/${path}`;
}

export function currentMachineName(): string | null {
  return router.segments[0] === 'machines' && router.segments[1] ? decodeURIComponent(router.segments[1]) : null;
}

export function currentProfileName(): string | null {
  return router.segments[0] === 'profiles' && router.segments[1] ? decodeURIComponent(router.segments[1]) : null;
}

export function currentDeploymentName(): string | null {
  return router.segments[0] === 'deployments' && router.segments[1]
    ? decodeURIComponent(router.segments[1])
    : null;
}

/** 路由清单（交付证据：UI 的全部可达路径）。 */
export const ROUTE_TABLE: { path: string; page: string }[] = [
  { path: '#/', page: 'Overview（卡片 + 三张表）' },
  { path: '#/machines', page: 'Machines（列表与动作入口）' },
  { path: '#/machines/:name', page: 'Machine Detail（9 个区块，无交互 shell）' },
  { path: '#/profiles', page: 'Profiles（JSON/YAML 预览 + 消费机器）' },
  { path: '#/profiles/:name', page: 'Profile Detail（渲染预览）' },
  { path: '#/skills', page: 'Skills' },
  { path: '#/deployments', page: 'Deployments（批次进度与逐机结果）' },
  { path: '#/deployments/:name', page: 'Deployment Detail（目标与跳过入口）' },
  { path: '#/ssh-inventory', page: 'SSH Inventory（include 预览与导出说明）' },
];
