/**
 * SSE 事件 → 选择性重取（FR-14.2："事件含资源类型/ID + revision，供 UI 选择性重取"）。
 *
 * 本模块是**纯逻辑**（无 Svelte、无 fetch），便于单测：
 *  - `RevisionTracker` 按 (资源类型, id) 记账，丢弃重复/更旧 revision 的事件，
 *    避免"同一对象连写多次"造成重复请求；
 *  - `planRefetch` 把已接受的事件折叠成"本轮要刷新的视图集合"。
 */
import type { ResourceEvent, ResourceType } from '../api/types';

export class RevisionTracker {
  private seen = new Map<string, number>();

  private static key(resource: ResourceType, id: string): string {
    return `${resource}/${id}`;
  }

  /**
   * 记录事件。返回 true 表示这是**新的**变更（应触发重取）：
   * revision 大于已见值，或首次见到该对象。
   * 注意删除事件同样带递增版本（后端取"删除前版本 +1"），因此删除也会被接受。
   */
  accept(resource: ResourceType, id: string, revision: number): boolean {
    const key = RevisionTracker.key(resource, id);
    const prev = this.seen.get(key);
    if (prev !== undefined && revision <= prev) return false;
    this.seen.set(key, revision);
    return true;
  }

  lastRevision(resource: ResourceType, id: string): number | undefined {
    return this.seen.get(RevisionTracker.key(resource, id));
  }

  /** 全量重取（重连、手动刷新）后调用：允许后续事件重新触发。 */
  reset(): void {
    this.seen.clear();
  }

  get size(): number {
    return this.seen.size;
  }
}

export interface RefetchPlan {
  /** 需要重取的集合型视图。 */
  lists: Set<ResourceType>;
  /** 需要重取的机器详情（machines 事件按机器名；operations 事件按缓存映射回机器）。 */
  machines: Set<string>;
  /** 需要重取的 Deployment 详情。 */
  deployments: Set<string>;
}

export function emptyPlan(): RefetchPlan {
  return { lists: new Set(), machines: new Set(), deployments: new Set() };
}

export function isPlanEmpty(plan: RefetchPlan): boolean {
  return plan.lists.size === 0 && plan.machines.size === 0 && plan.deployments.size === 0;
}

export interface PlanContext {
  /** 当前打开的机器详情（无则 null）。 */
  activeMachine?: string | null;
  /** 当前打开的 Deployment（无则 null）。 */
  activeDeployment?: string | null;
  /** 已缓存的"操作 id → 机器名"映射（operations 事件的 id 是操作 id）。 */
  operationOwners?: Map<string, string>;
}

/**
 * 事件 → 重取计划。
 *
 * 取舍：`machines` 事件在**任一机器**变更时都重取机器列表（列表是 Overview 与
 * Machines 页的数据源，规模是几十台，重取成本远低于漏更新的代价）；若变更的正是
 * 当前打开的机器，则连详情（drift + operations）一起重取。
 * `operations` 事件的 id 是操作 id：只能经已缓存的映射定位机器；定位不到时不猜测，
 * 交由机器状态事件（同一次状态写入也会产生 machines 事件）驱动收敛。
 */
export function planRefetch(events: ResourceEvent[], ctx: PlanContext = {}): RefetchPlan {
  const plan = emptyPlan();
  for (const ev of events) {
    switch (ev.resource) {
      case 'machines':
        plan.lists.add('machines');
        if (ctx.activeMachine && ev.id === ctx.activeMachine) {
          plan.machines.add(ev.id);
          plan.lists.add('operations');
        }
        break;
      case 'operations': {
        const owner = ctx.operationOwners?.get(ev.id);
        if (owner) {
          plan.machines.add(owner);
          if (ctx.activeMachine && owner === ctx.activeMachine) plan.lists.add('operations');
        } else if (ctx.activeMachine) {
          // 未知操作：只对当前打开的机器做一次操作列表重取（宁可多取一次，
          // 也不让"未决操作"这类必须显眼的状态停留在旧值上）。
          plan.machines.add(ctx.activeMachine);
          plan.lists.add('operations');
        }
        break;
      }
      case 'deployments':
        plan.lists.add('deployments');
        if (ctx.activeDeployment && ev.id === ctx.activeDeployment) plan.deployments.add(ev.id);
        break;
      case 'profiles':
      case 'skills':
      case 'providers':
        plan.lists.add(ev.resource);
        break;
    }
  }
  return plan;
}
