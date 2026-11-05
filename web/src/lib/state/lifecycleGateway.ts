/**
 * 生命周期页面的数据通道（KM-50）。
 *
 * 接口按"T2/T4 落地契约（api/openapi/fleet-v1.yaml 的 /api/v1/lifecycle/* 与
 * /machines/{name}/lifecycle/desired，含 confirm 确认端点）+ T3 上游发现 + 实例
 * 发现查询（KM-49 修复波/T7 P5 契约侧）"划分。生产实现 RestLifecycleGateway
 * 直连 FleetClient；端点未装配或资源缺失时得到 ApiError（如 404/501），页面
 * 如实呈现失败原因——绝不 mock 兜底。
 *
 * 实例发现的契约形状是机器级 Codex 实例观测（路径/版本/安装态），不含受管
 * 用户/执行环境/安装来源归属。映射把这些缺口如实保留为空（不猜测）：页面据
 * "environments 为空"进入操作者声明用户的模式，来源未上报的实例不给升级/卸载
 * 入口（spec User Story 22）；notInstalled / inventoryMissing 两种显式状态
 * （"尚未安装" ≠ "未知"）原样透传给页面区分呈现。
 */
import { ApiError, type FleetClient } from '../api/client';
import type {
  LifecycleCapability,
  LifecycleDesiredDocument,
  LifecycleDesiredUpdate,
  LifecycleInstanceList,
  LifecycleOperation,
  LifecycleOperationCreateRequest,
  LifecycleRetryRequest,
  UpstreamVersionReport,
} from '../api/types';

/** 一个已发现的 agent 安装实例（路径、来源、版本——spec User Story 4）。 */
export interface AgentInstance {
  /** 执行环境：空 = 原生；如 "wsl:ubuntu"。同一主机的 Windows 原生与 WSL 独立。 */
  env: string;
  /** 归属受管用户；观测未上报归属时为空串（如实缺省，不猜测）。 */
  user: string;
  agent: string;
  /** 安装方式/来源（能力矩阵合法取值）；观测未上报时为空串——卸载/升级按已识别来源执行，来源未知即拒绝。 */
  method: string;
  /** 安装包名；观测未上报时为空串。 */
  package: string;
  /** 已安装但版本不可解析时为空且 versionError 非空（如实区分，不伪装成未安装）。 */
  version?: string;
  versionError?: string;
  /** 实例路径（程序入口或配置位置）；多实例区分与目标身份绑定（instancePath）都依赖它。 */
  executablePath?: string;
}

/** 一台机器的实例发现结果：受管环境（含用户）+ 已装实例 + 观测新鲜度。 */
export interface AgentInstanceInventory {
  machine: string;
  os?: string;
  arch?: string;
  /**
   * 逐环境列出受管用户——即使该用户尚未安装 Codex，也要给出"尚未安装"状态。
   * 契约不提供用户/环境枚举时为空数组：页面进入操作者声明用户的模式。
   */
  environments: { env: string; users: string[] }[];
  instances: AgentInstance[];
  /** 尚未安装的显式状态（inventory 在场但没有任何 codex 条目）——不是错误。 */
  notInstalled?: boolean;
  /** 节点从未上报观测：实例状态未知——未知 ≠ 尚未安装，页面必须区分呈现。 */
  inventoryMissing?: boolean;
  /** 观测新鲜度（inventorySeq / 采集时间 / 是否过期）：缺失或过期时 UI 必须标注，未知 ≠ 一切。 */
  inventorySeq?: number;
  discoveredAt?: string;
  stale?: boolean;
}

/**
 * 契约形状 → 页面模型的实例发现映射（KM-50 第二波接线；对照 OpenAPI
 * LifecycleInstanceList）。缺口如实保留：观测不含 env/user/method/package
 * 归属，映射置空不猜测（contract："当前节点采集不区分安装来源，不猜测"）；
 * notInstalled / inventoryMissing 两种显式状态原样透传。
 */
export function instanceInventoryFromContract(list: LifecycleInstanceList): AgentInstanceInventory {
  const instances: AgentInstance[] = (list.instances ?? [])
    // installed=false 的条目不是实例（不冒充已装）；versionError 只在 installed 条目上
    // 表达"已装但版本不可解析"（后端不会给出 installed=false + versionError 的矛盾组合）。
    .filter((i) => i.installed)
    .map((i) => ({
      env: '',
      user: '',
      agent: i.agent ?? '',
      method: '',
      package: '',
      ...(i.version ? { version: i.version } : {}),
      ...(i.versionError ? { versionError: i.versionError } : {}),
      ...(i.path ? { executablePath: i.path } : {}),
    }));
  return {
    machine: list.machine,
    environments: [],
    instances,
    ...(list.notInstalled ? { notInstalled: true } : {}),
    ...(list.inventoryMissing ? { inventoryMissing: true } : {}),
  };
}

export interface LifecycleGateway {
  getInstanceInventory(machine: string): Promise<AgentInstanceInventory>;
  /** 404（从未纳管）映射为 null——"未纳管"是可查询的显式状态。 */
  getDesired(machine: string): Promise<LifecycleDesiredDocument | null>;
  putDesired(machine: string, update: LifecycleDesiredUpdate): Promise<LifecycleDesiredDocument>;
  getCapabilities(): Promise<LifecycleCapability[]>;
  createOperation(req: LifecycleOperationCreateRequest): Promise<{ operation: LifecycleOperation; reused: boolean }>;
  getOperation(opId: string): Promise<LifecycleOperation>;
  /** 记录操作者确认；未确认不派发，needsReconfirmation 经重新确认清除（幂等）。 */
  confirmOperation(opId: string): Promise<LifecycleOperation>;
  retryOperation(opId: string, req: LifecycleRetryRequest): Promise<LifecycleOperation>;
  checkUpstream(agent: string): Promise<UpstreamVersionReport>;
}

export class RestLifecycleGateway implements LifecycleGateway {
  constructor(private readonly client: FleetClient) {}

  async getInstanceInventory(machine: string): Promise<AgentInstanceInventory> {
    return instanceInventoryFromContract(await this.client.getLifecycleInstances(machine));
  }

  async getDesired(machine: string): Promise<LifecycleDesiredDocument | null> {
    try {
      return await this.client.getLifecycleDesired(machine);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return null; // 从未纳管
      throw err;
    }
  }

  putDesired(machine: string, update: LifecycleDesiredUpdate) {
    return this.client.putLifecycleDesired(machine, update);
  }

  async getCapabilities(): Promise<LifecycleCapability[]> {
    return (await this.client.getLifecycleCapabilities()).items ?? [];
  }

  createOperation(req: LifecycleOperationCreateRequest) {
    return this.client.createLifecycleOperation(req);
  }

  getOperation(opId: string) {
    return this.client.getLifecycleOperation(opId);
  }

  confirmOperation(opId: string) {
    return this.client.confirmLifecycleOperation(opId);
  }

  retryOperation(opId: string, req: LifecycleRetryRequest) {
    return this.client.retryLifecycleOperation(opId, req);
  }

  checkUpstream(agent: string) {
    return this.client.checkUpstreamVersions(agent);
  }
}
