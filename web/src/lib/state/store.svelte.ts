/**
 * 控制面状态容器（Svelte 5 runes）。
 *
 * 数据流只有一条：REST 重取为权威，SSE 事件只负责"让哪些视图失效"
 * （FR-14.2）。因此任何写操作完成后都走同一个 refresh，UI 不会出现
 * "事件推来的第二份状态"与 REST 结果不一致的情况。
 */
import { ApiError, FleetClient } from '../api/client';
import { subscribeEvents, type StreamState, type Subscription } from '../api/sse';
import type {
  AgentProfile,
  Deployment,
  Machine,
  MachineDrift,
  ModelProvider,
  Operation,
  ResourceEvent,
  ResourceType,
  Skill,
} from '../api/types';
import { isPlanEmpty, planRefetch, RevisionTracker } from './refetch';

export interface MachineDetail {
  machine?: Machine;
  drift?: MachineDrift;
  operations: Operation[];
  error?: string;
  loading: boolean;
}

/** 事件折叠窗口：把短时间内的多次写入合并成一轮重取。 */
const FLUSH_DELAY_MS = 150;

export class FleetStore {
  readonly client: FleetClient;

  machines = $state<Machine[]>([]);
  profiles = $state<AgentProfile[]>([]);
  skills = $state<Skill[]>([]);
  providers = $state<ModelProvider[]>([]);
  deployments = $state<Deployment[]>([]);

  details = $state<Record<string, MachineDetail>>({});

  /** Overview 的"最近操作"聚合（契约只提供逐机 operations，见 loadRecentOperations）。 */
  recentOperations = $state<Operation[]>([]);

  loading = $state<Record<string, boolean>>({});
  /** 最近一次加载失败的提示（按视图键，如 "machines"、"machine:ws-1"）。 */
  errors = $state<Record<string, string>>({});
  /** 上次成功重取时间（Overview 顶部显示"数据新鲜度"）。 */
  lastRefreshAt = $state<Date | null>(null);

  streamState = $state<StreamState>('connecting');
  streamDetail = $state<string>('');
  lastEventAt = $state<Date | null>(null);
  /** 已处理的事件计数（联调与排障用，也证明选择性重取在工作）。 */
  eventsSeen = $state(0);
  eventsCoalesced = $state(0);

  /** 当前路由上下文（由 App 在路由变化时写入，供重取计划裁剪）。 */
  activeMachine = $state<string | null>(null);
  activeDeployment = $state<string | null>(null);

  private tracker = new RevisionTracker();
  private pending: ResourceEvent[] = [];
  private flushTimer: ReturnType<typeof setTimeout> | null = null;
  private subscription: Subscription | null = null;
  /** 操作 id → 机器名（operations 事件的 id 是操作 id，重取计划据此定位机器）。 */
  readonly operationOwners = new Map<string, string>();
  private recentOperationsFetchedAt = 0;
  private started = false;

  constructor(client?: FleetClient) {
    this.client = client ?? new FleetClient();
  }

  // ---------- 生命周期 ----------

  start(): void {
    if (this.started) return;
    this.started = true;
    void this.refreshAll();
    this.subscription = subscribeEvents(
      {
        onEvent: (ev) => this.onEvent(ev),
        onState: (state, detail) => {
          const wasOpen = this.streamState === 'open';
          this.streamState = state;
          this.streamDetail = detail ?? '';
          // 重连成功：事件流无重放，必须全量重取一次（§23.6 契约细节）。
          if (state === 'open' && !wasOpen) {
            this.tracker.reset();
            void this.refreshAll();
          }
        },
      },
      { token: this.client.token },
    );
  }

  stop(): void {
    this.subscription?.close();
    this.subscription = null;
    this.started = false;
  }

  /** 切换 admin token（§29.14）：重建事件流并全量重取。 */
  setToken(token: string): void {
    this.client.token = token;
    this.stop();
    this.tracker.reset();
    this.start();
  }

  // ---------- 事件处理 ----------

  private onEvent(ev: ResourceEvent): void {
    this.eventsSeen += 1;
    this.lastEventAt = new Date();
    if (!this.tracker.accept(ev.resource, ev.id, ev.revision)) {
      this.eventsCoalesced += 1;
      return;
    }
    this.pending.push(ev);
    if (this.flushTimer) return;
    this.flushTimer = setTimeout(() => {
      this.flushTimer = null;
      const batch = this.pending;
      this.pending = [];
      void this.applyBatch(batch);
    }, FLUSH_DELAY_MS);
  }

  private async applyBatch(events: ResourceEvent[]): Promise<void> {
    const plan = planRefetch(events, {
      activeMachine: this.activeMachine,
      activeDeployment: this.activeDeployment,
      operationOwners: this.operationOwners,
    });
    if (isPlanEmpty(plan)) return;
    await Promise.all([
      ...[...plan.lists].map((resource) => this.refreshList(resource)),
      ...[...plan.machines].map((name) => this.loadMachine(name)),
      ...[...plan.deployments].map((name) => this.loadDeployment(name)),
    ]);
  }

  // ---------- 读取 ----------

  async refreshAll(): Promise<void> {
    await Promise.all([
      this.refreshList('machines'),
      this.refreshList('profiles'),
      this.refreshList('skills'),
      this.refreshList('providers'),
      this.refreshList('deployments'),
      this.activeMachine ? this.loadMachine(this.activeMachine) : Promise.resolve(),
      this.activeDeployment ? this.loadDeployment(this.activeDeployment) : Promise.resolve(),
    ]);
    this.lastRefreshAt = new Date();
  }

  async refreshList(resource: ResourceType): Promise<void> {
    switch (resource) {
      case 'machines':
        return this.run('machines', async () => {
          this.machines = (await this.client.listMachines()).items ?? [];
        });
      case 'profiles':
        return this.run('profiles', async () => {
          this.profiles = (await this.client.listProfiles()).items ?? [];
        });
      case 'skills':
        return this.run('skills', async () => {
          this.skills = (await this.client.listSkills()).items ?? [];
        });
      case 'providers':
        return this.run('providers', async () => {
          this.providers = (await this.client.listProviders()).items ?? [];
        });
      case 'deployments':
        return this.run('deployments', async () => {
          this.deployments = (await this.client.listDeployments()).items ?? [];
        });
      case 'operations':
        return this.activeMachine ? this.loadMachine(this.activeMachine) : Promise.resolve();
      default:
        return Promise.resolve();
    }
  }

  async loadMachine(name: string): Promise<void> {
    const prev = this.details[name];
    this.details[name] = { ...(prev ?? { operations: [] }), loading: true, error: undefined };
    try {
      const [machine, drift, ops] = await Promise.all([
        this.client.getMachine(name),
        this.client.getDrift(name).catch((err: unknown) => {
          // drift 视图失败不应吞掉机器本身；把原因留在详情里。
          if (err instanceof ApiError && err.status === 404) throw err;
          return undefined;
        }),
        this.client.listOperations(name),
      ]);
      const operations = ops.items ?? [];
      this.rememberOperationOwners(name, operations);
      this.details[name] = { machine, drift, operations, loading: false };
      delete this.errors[`machine:${name}`];
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        // 删除事件后重取得到 404：把该机从列表与详情中移除。
        this.machines = this.machines.filter((m) => m.metadata.name !== name);
        delete this.details[name];
        return;
      }
      this.details[name] = {
        ...(this.details[name] ?? { operations: [] }),
        loading: false,
        error: err instanceof Error ? err.message : String(err),
      };
      this.errors[`machine:${name}`] = err instanceof Error ? err.message : String(err);
    }
  }

  async loadDeployment(name: string): Promise<void> {
    try {
      const dep = await this.client.getDeployment(name);
      this.deployments = this.deployments.map((d) => (d.metadata.name === name ? dep : d));
      if (!this.deployments.some((d) => d.metadata.name === name)) this.deployments = [...this.deployments, dep];
      delete this.errors[`deployment:${name}`];
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        this.deployments = this.deployments.filter((d) => d.metadata.name !== name);
        return;
      }
      this.errors[`deployment:${name}`] = err instanceof Error ? err.message : String(err);
    }
  }

  /** 写操作后的统一收敛入口：无论事件是否及时到达，都主动重取一次。 */
  async afterMutation(...resources: ResourceType[]): Promise<void> {
    this.tracker.reset();
    await Promise.all(resources.map((r) => this.refreshList(r)));
    if (this.activeMachine) await this.loadMachine(this.activeMachine);
  }

  /**
   * Overview 需要的"最近操作"。
   *
   * 契约缺口：§23 只提供 `GET /machines/{name}/operations`，没有全局操作列表端点，
   * 因此这里对每台机器逐机取（并发上限 6，10 秒内不重复取）。规模是 MVP 的几十台，
   * 代价可接受；缺口已在 KM-25 交付评论中登记，后续若新增全局端点则应替换本实现。
   */
  async loadRecentOperations(force = false): Promise<void> {
    const now = Date.now();
    if (!force && now - this.recentOperationsFetchedAt < 10_000) return;
    this.recentOperationsFetchedAt = now;
    const names = this.machines.map((m) => m.metadata.name);
    const collected: Operation[] = [];
    const queue = [...names];
    const workers = Array.from({ length: Math.min(6, queue.length) }, async () => {
      for (;;) {
        const name = queue.shift();
        if (!name) return;
        try {
          const ops = await this.client.listOperations(name);
          for (const op of ops.items ?? []) {
            collected.push(op);
            if (op.metadata?.name) this.operationOwners.set(op.metadata.name, name);
          }
        } catch {
          // 单机读取失败不影响聚合结果；错误已由该机详情的加载路径暴露。
        }
      }
    });
    await Promise.all(workers);
    collected.sort((a, b) =>
      (b.metadata?.creationTimestamp ?? '').localeCompare(a.metadata?.creationTimestamp ?? ''),
    );
    this.recentOperations = collected;
  }

  // ---------- 内部 ----------

  private rememberOperationOwners(machine: string, ops: Operation[]): void {
    for (const op of ops) {
      const id = op.metadata?.name;
      if (id) this.operationOwners.set(id, machine);
    }
  }

  private async run(key: string, fn: () => Promise<void>): Promise<void> {
    this.loading[key] = true;
    try {
      await fn();
      delete this.errors[key];
    } catch (err) {
      this.errors[key] = err instanceof ApiError ? `${err.reason}: ${err.message}` : String(err);
    } finally {
      this.loading[key] = false;
    }
  }

  // ---------- 便利读取 ----------

  machineByName(name?: string | null): Machine | undefined {
    if (!name) return undefined;
    return this.machines.find((m) => m.metadata.name === name);
  }

  deploymentByName(name?: string | null): Deployment | undefined {
    if (!name) return undefined;
    return this.deployments.find((d) => d.metadata.name === name);
  }

  /** 消费某台机器期望状态的 profile 列表（Profiles 页"消费机器"）。 */
  machinesUsingProfile(profileName: string): Machine[] {
    return this.machines.filter((m) => m.spec?.profileRef === profileName);
  }

  machinesUsingSkill(skillName: string): Machine[] {
    const names = new Set<string>();
    for (const p of this.profiles) {
      const refs = p.spec?.skills ?? [];
      if (refs.some((r) => r.skill === skillName || r.name === skillName)) {
        for (const m of this.machinesUsingProfile(p.metadata.name)) names.add(m.metadata.name);
      }
    }
    return this.machines.filter((m) => names.has(m.metadata.name));
  }
}
