/**
 * 生命周期页面的数据通道（KM-50）。
 *
 * 接口按"T2 落地契约（api/openapi/fleet-v1.yaml 的 /api/v1/lifecycle/* 与
 * /machines/{name}/lifecycle/desired）+ T3 上游发现 + 实例发现"划分。
 * 生产实现 RestLifecycleGateway 直连 FleetClient；REST 处理器属 T4/后端切片，
 * 未就绪时调用会得到 ApiError（如 404/501），页面如实呈现失败原因——绝不 mock 兜底。
 *
 * 实例发现（受管环境、用户与已装实例的路径/来源/版本）尚无落地契约：为不编造
 * 端点，RestLifecycleGateway.getInstanceInventory 显式抛 InventoryNotWiredError，
 * 页面呈现"实例发现尚未接入"的诚实状态；测试用契约形状的 mock（lifecycleMock）
 * 验证完整选择流程，浏览器发起的真实链路由 T6 收口。
 */
import { ApiError, type FleetClient } from '../api/client';
import type {
  LifecycleCapability,
  LifecycleDesiredDocument,
  LifecycleDesiredUpdate,
  LifecycleOperation,
  LifecycleOperationCreateRequest,
  LifecycleRetryRequest,
  UpstreamVersionReport,
} from '../api/types';

/** 一个已发现的 agent 安装实例（路径、来源、版本——spec User Story 4）。 */
export interface AgentInstance {
  /** 执行环境：空 = 原生；如 "wsl:ubuntu"。同一主机的 Windows 原生与 WSL 独立。 */
  env: string;
  user: string;
  agent: string;
  /** 安装方式/来源（能力矩阵合法取值）；卸载按已识别来源执行。 */
  method: string;
  package: string;
  /** 已安装但版本不可解析时为空且 versionError 非空（如实区分，不伪装成未安装）。 */
  version?: string;
  versionError?: string;
  executablePath?: string;
}

/** 一台机器的实例发现结果：受管环境（含用户）+ 已装实例 + 观测新鲜度。 */
export interface AgentInstanceInventory {
  machine: string;
  os?: string;
  arch?: string;
  /** 逐环境列出受管用户——即使该用户尚未安装 Codex，也要给出"尚未安装"状态。 */
  environments: { env: string; users: string[] }[];
  instances: AgentInstance[];
  /** 观测新鲜度（inventorySeq / 采集时间 / 是否过期）：缺失或过期时 UI 必须标注，未知 ≠ 一切。 */
  inventorySeq?: number;
  discoveredAt?: string;
  stale?: boolean;
}

/** 生产通道尚未接通实例发现时的显式错误（页面据此呈现诚实状态，不是静默空数据）。 */
export class InventoryNotWiredError extends Error {
  constructor() {
    super('instance inventory endpoint is not wired yet');
    this.name = 'InventoryNotWiredError';
  }
}

export interface LifecycleGateway {
  getInstanceInventory(machine: string): Promise<AgentInstanceInventory>;
  /** 404（从未纳管）映射为 null——"未纳管"是可查询的显式状态。 */
  getDesired(machine: string): Promise<LifecycleDesiredDocument | null>;
  putDesired(machine: string, update: LifecycleDesiredUpdate): Promise<LifecycleDesiredDocument>;
  getCapabilities(): Promise<LifecycleCapability[]>;
  createOperation(req: LifecycleOperationCreateRequest): Promise<{ operation: LifecycleOperation; reused: boolean }>;
  getOperation(opId: string): Promise<LifecycleOperation>;
  retryOperation(opId: string, req: LifecycleRetryRequest): Promise<LifecycleOperation>;
  checkUpstream(agent: string): Promise<UpstreamVersionReport>;
}

export class RestLifecycleGateway implements LifecycleGateway {
  constructor(private readonly client: FleetClient) {}

  getInstanceInventory(_machine: string): Promise<AgentInstanceInventory> {
    // 实例发现尚无落地契约：显式失败，让页面呈现诚实状态；不编造端点、不返回假数据。
    return Promise.reject(new InventoryNotWiredError());
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

  retryOperation(opId: string, req: LifecycleRetryRequest) {
    return this.client.retryLifecycleOperation(opId, req);
  }

  checkUpstream(agent: string) {
    return this.client.checkUpstreamVersions(agent);
  }
}
