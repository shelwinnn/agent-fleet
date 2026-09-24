/**
 * 类型化 REST 客户端（依据 api/openapi/fleet-v1.yaml；架构 v1.1.2 §8.1）。
 *
 * 只做三件事：拼路径、带 `Authorization: Bearer <adminToken>`（§29.14）、把
 * §6.4 五要素错误体映射为 ApiError。不含框架依赖（NFR-9），因此可在 Node 中
 * 直接用于联调脚本（scripts/integration-check.ts）。
 */
import type {
  AgentProfile,
  AgentProfileList,
  ApiErrorBody,
  Deployment,
  DeploymentList,
  Machine,
  MachineDrift,
  MachineList,
  ModelProvider,
  ModelProviderList,
  Operation,
  OperationList,
  RenderPreview,
  Skill,
  SkillList,
} from './types';

/** §6.4 错误体映射后的错误对象。 */
export class ApiError extends Error {
  readonly status: number;
  readonly reason: string;
  readonly operationId?: string;
  readonly diagnostics?: Record<string, string>;

  constructor(status: number, body: Partial<ApiErrorBody>) {
    super(body.message || `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.reason = body.reason || 'Unknown';
    this.operationId = body.operationId;
    this.diagnostics = body.diagnostics;
  }

  /** 409 MachineBusy：诊断里带未决操作引用（§8.1），UI 直接呈现处置入口。 */
  get isMachineBusy(): boolean {
    return this.status === 409 && this.reason === 'MachineBusy';
  }

  /** 409 ReplanRequired：确认时计划基线已失效，需重新 plan 与重新确认（FR-12.7）。 */
  get isReplanRequired(): boolean {
    return this.status === 409 && this.reason === 'ReplanRequired';
  }

  get unresolvedOperationId(): string | undefined {
    return this.diagnostics?.operationId;
  }

  get unresolvedPhase(): string | undefined {
    return this.diagnostics?.phase;
  }
}

export interface ClientOptions {
  /** 控制面地址；默认同源（Vite dev 经 proxy 转发到 127.0.0.1:7788）。 */
  baseUrl?: string;
  /** admin token（§29.14）；未配置时服务端也不校验（回环默认）。 */
  token?: string;
  /** 注入 fetch（测试与 Node 联调脚本用）。 */
  fetch?: typeof fetch;
}

export interface RequestOptions {
  signal?: AbortSignal;
}

async function parseError(resp: Response): Promise<ApiError> {
  let body: Partial<ApiErrorBody> = {};
  try {
    body = (await resp.json()) as ApiErrorBody;
  } catch {
    body = { reason: 'Unknown', message: `HTTP ${resp.status} ${resp.statusText}` };
  }
  return new ApiError(resp.status, body);
}

export class FleetClient {
  private readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;
  token: string;

  constructor(opts: ClientOptions = {}) {
    this.baseUrl = (opts.baseUrl ?? '').replace(/\/$/, '');
    this.token = opts.token ?? '';
    this.fetchImpl = opts.fetch ?? ((...args) => fetch(...args));
  }

  /** 底层请求：返回 Response（SSE 订阅需要拿到未解析的流）。 */
  async request(
    method: string,
    path: string,
    body?: unknown,
    opts: RequestOptions = {},
  ): Promise<Response> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (this.token) headers['Authorization'] = `Bearer ${this.token}`;
    const resp = await this.fetchImpl(`${this.baseUrl}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: opts.signal,
    });
    if (!resp.ok) throw await parseError(resp);
    return resp;
  }

  private async json<T>(method: string, path: string, body?: unknown, opts?: RequestOptions): Promise<T> {
    const resp = await this.request(method, path, body, opts);
    if (resp.status === 204) return undefined as T;
    return (await resp.json()) as T;
  }

  // ---- system ----
  healthz = (opts?: RequestOptions) => this.json<{ status: string }>('GET', '/healthz', undefined, opts);
  readyz = (opts?: RequestOptions) => this.json<{ status: string }>('GET', '/readyz', undefined, opts);

  // ---- machines ----
  listMachines = (opts?: RequestOptions) => this.json<MachineList>('GET', '/api/v1/machines', undefined, opts);
  getMachine = (name: string, opts?: RequestOptions) =>
    this.json<Machine>('GET', `/api/v1/machines/${encodeURIComponent(name)}`, undefined, opts);
  createMachine = (machine: Machine, opts?: RequestOptions) =>
    this.json<Machine>('POST', '/api/v1/machines', machine, opts);
  updateMachine = (name: string, machine: Machine, opts?: RequestOptions) =>
    this.json<Machine>('PUT', `/api/v1/machines/${encodeURIComponent(name)}`, machine, opts);
  deleteMachine = (name: string, opts?: RequestOptions) =>
    this.json<void>('DELETE', `/api/v1/machines/${encodeURIComponent(name)}`, undefined, opts);

  /** drfit 三态视图（FR-14.5 的权威输入）。 */
  getDrift = (name: string, opts?: RequestOptions) =>
    this.json<MachineDrift>('GET', `/api/v1/machines/${encodeURIComponent(name)}/drift`, undefined, opts);
  listOperations = (name: string, opts?: RequestOptions) =>
    this.json<OperationList>('GET', `/api/v1/machines/${encodeURIComponent(name)}/operations`, undefined, opts);

  /** 触发收敛；携带 confirmPlanDigest 即"确认该操作自己的计划"（§4.3 第 6 条）。 */
  reconcile = (name: string, confirmPlanDigest?: string, opts?: RequestOptions) =>
    this.json<Operation>(
      'POST',
      `/api/v1/machines/${encodeURIComponent(name)}/reconcile`,
      confirmPlanDigest ? { confirmPlanDigest } : {},
      opts,
    );
  rollbackMachine = (name: string, targetGeneration: number, opts?: RequestOptions) =>
    this.json<Operation>('POST', `/api/v1/machines/${encodeURIComponent(name)}/rollback`, { targetGeneration }, opts);
  cancelOperation = (name: string, opId: string, opts?: RequestOptions) =>
    this.json<Operation>(
      'POST',
      `/api/v1/machines/${encodeURIComponent(name)}/operations/${encodeURIComponent(opId)}/cancel`,
      undefined,
      opts,
    );
  /** 显式跳过：必须带原因（审计，§9.3 第 5 条）；不计入成功。 */
  skipOperation = (name: string, opId: string, reason: string, operator?: string, opts?: RequestOptions) =>
    this.json<Operation>(
      'POST',
      `/api/v1/machines/${encodeURIComponent(name)}/operations/${encodeURIComponent(opId)}/skip`,
      { reason, operator },
      opts,
    );

  // ---- profiles ----
  listProfiles = (opts?: RequestOptions) => this.json<AgentProfileList>('GET', '/api/v1/profiles', undefined, opts);
  getProfile = (name: string, opts?: RequestOptions) =>
    this.json<AgentProfile>('GET', `/api/v1/profiles/${encodeURIComponent(name)}`, undefined, opts);
  createProfile = (profile: AgentProfile, opts?: RequestOptions) =>
    this.json<AgentProfile>('POST', '/api/v1/profiles', profile, opts);
  updateProfile = (name: string, profile: AgentProfile, opts?: RequestOptions) =>
    this.json<AgentProfile>('PUT', `/api/v1/profiles/${encodeURIComponent(name)}`, profile, opts);
  deleteProfile = (name: string, opts?: RequestOptions) =>
    this.json<void>('DELETE', `/api/v1/profiles/${encodeURIComponent(name)}`, undefined, opts);
  /** 渲染期望状态预览（FR-7.4：不落库、不增代）；未解析引用返回 400。 */
  renderProfile = (name: string, machine: string, opts?: RequestOptions) =>
    this.json<RenderPreview>(
      'GET',
      `/api/v1/profiles/${encodeURIComponent(name)}/render?machine=${encodeURIComponent(machine)}`,
      undefined,
      opts,
    );

  // ---- skills ----
  listSkills = (opts?: RequestOptions) => this.json<SkillList>('GET', '/api/v1/skills', undefined, opts);
  getSkill = (name: string, opts?: RequestOptions) =>
    this.json<Skill>('GET', `/api/v1/skills/${encodeURIComponent(name)}`, undefined, opts);
  createSkill = (skill: Skill, opts?: RequestOptions) => this.json<Skill>('POST', '/api/v1/skills', skill, opts);
  updateSkill = (name: string, skill: Skill, opts?: RequestOptions) =>
    this.json<Skill>('PUT', `/api/v1/skills/${encodeURIComponent(name)}`, skill, opts);
  deleteSkill = (name: string, opts?: RequestOptions) =>
    this.json<void>('DELETE', `/api/v1/skills/${encodeURIComponent(name)}`, undefined, opts);

  // ---- providers ----
  listProviders = (opts?: RequestOptions) =>
    this.json<ModelProviderList>('GET', '/api/v1/providers', undefined, opts);
  getProvider = (name: string, opts?: RequestOptions) =>
    this.json<ModelProvider>('GET', `/api/v1/providers/${encodeURIComponent(name)}`, undefined, opts);
  createProvider = (provider: ModelProvider, opts?: RequestOptions) =>
    this.json<ModelProvider>('POST', '/api/v1/providers', provider, opts);
  updateProvider = (name: string, provider: ModelProvider, opts?: RequestOptions) =>
    this.json<ModelProvider>('PUT', `/api/v1/providers/${encodeURIComponent(name)}`, provider, opts);
  deleteProvider = (name: string, opts?: RequestOptions) =>
    this.json<void>('DELETE', `/api/v1/providers/${encodeURIComponent(name)}`, undefined, opts);

  // ---- deployments ----
  listDeployments = (opts?: RequestOptions) =>
    this.json<DeploymentList>('GET', '/api/v1/deployments', undefined, opts);
  getDeployment = (name: string, opts?: RequestOptions) =>
    this.json<Deployment>('GET', `/api/v1/deployments/${encodeURIComponent(name)}`, undefined, opts);
  createDeployment = (deployment: Deployment, opts?: RequestOptions) =>
    this.json<Deployment>('POST', '/api/v1/deployments', deployment, opts);
  deleteDeployment = (name: string, opts?: RequestOptions) =>
    this.json<void>('DELETE', `/api/v1/deployments/${encodeURIComponent(name)}`, undefined, opts);
  rollbackDeployment = (name: string, targetGeneration?: number, opts?: RequestOptions) =>
    this.json<Deployment>(
      'POST',
      `/api/v1/deployments/${encodeURIComponent(name)}/rollback`,
      targetGeneration ? { targetGeneration } : {},
      opts,
    );
  /** 显式跳过被阻塞的发布目标（FR-10.7）：必须带原因，记 Skipped 且不计入成功。 */
  skipDeploymentTarget = (name: string, machine: string, reason: string, opts?: RequestOptions) =>
    this.json<Record<string, unknown>>(
      'POST',
      `/api/v1/deployments/${encodeURIComponent(name)}/targets/${encodeURIComponent(machine)}/skip`,
      { reason },
      opts,
    );
}
