/**
 * 控制面 REST 类型（依据 api/openapi/fleet-v1.yaml，架构 v1.1.2 §8.1/§6.x）。
 *
 * 约定：类型是契约的**手写投影**，字段名与 OpenAPI 属性一一对应；不做任何
 * 框架相关的包装（NFR-9：API 契约不依赖前端框架）。契约以服务端 YAML 为准，
 * 本文件只做类型化读取——变更契约时先改 YAML 再改这里。
 */

export type ConditionStatus = 'True' | 'False' | 'Unknown';

export type ConditionType =
  | 'SSHReachable'
  | 'AgentConnected'
  | 'InventoryReady'
  | 'Drifted'
  | 'Reconciled'
  | 'Degraded';

export interface ObjectMeta {
  name: string;
  uid?: string;
  resourceVersion?: number;
  creationTimestamp?: string;
}

export interface Condition {
  type: ConditionType;
  status: ConditionStatus;
  reason?: string;
  message?: string;
  lastTransitionTime: string;
}

/** §6.1/§6.2：控制器维护的 Machine status。 */
export interface MachineStatus {
  conditions?: Condition[];
  observedGeneration?: number;
  desiredGeneration?: number;
  os?: string;
  arch?: string;
  hostname?: string;
  homeDir?: string;
  kernelVersion?: string;
  agentdVersion?: string;
  lastHeartbeatAt?: string;
  lastInventoryAt?: string;
  inventorySeq?: number;
  /** 最近一次 drift 比较使用的投影规范化版本（跨版本不沿用旧判定，§7.1）。 */
  desiredProjectionDigest?: string;
  observedProjectionDigest?: string;
  canonicalizationVersion?: string;
  /** FR-1.10：占用机器级互斥的操作引用。 */
  unresolvedOperation?: UnresolvedOperationRef;
  /** KM-24：节点上报的逐家族能力声明；控制面只读，UI 只如实转述。 */
  adapterCapabilities?: AdapterCapability[];
  [key: string]: unknown;
}

export interface AdapterCapability {
  family: string;
  capability: string;
  state: string;
  verifiedVersions?: string;
  reason?: string;
}

export interface UnresolvedOperationRef {
  id: string;
  phase: OperationPhase;
  type?: string;
}

export interface MachineCoreSpec {
  managementMode?: 'agentd' | 'ssh';
  profileRef?: string;
  ssh?: Record<string, unknown>;
  overrides?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface Machine {
  metadata: ObjectMeta;
  spec?: MachineCoreSpec;
  status?: MachineStatus;
}

export interface MachineList {
  items: Machine[];
}

/** AgentProfile.spec（归一化解释见 internal/desiredstate/render.go）。 */
export interface ProfileSpec {
  agents?: Record<string, ProfileAgent>;
  skills?: ProfileSkillRef[];
  mcp?: Record<string, unknown>;
  rules?: Record<string, unknown>;
  [key: string]: unknown;
}

export interface ProfileAgent {
  enabled?: boolean;
  version?: string;
  provider?: string;
  config?: unknown;
}

export interface ProfileSkillRef {
  name?: string;
  skill?: string;
}

export interface AgentProfile {
  metadata: ObjectMeta;
  spec?: ProfileSpec;
  status?: Record<string, unknown>;
}

export interface AgentProfileList {
  items: AgentProfile[];
}

export interface SkillSpec {
  [key: string]: unknown;
}

export interface SkillStatus {
  resolvedRevision?: string;
  contentDigest?: string;
  artifactDigest?: string;
  [key: string]: unknown;
}

export interface Skill {
  metadata: ObjectMeta;
  spec?: SkillSpec;
  status?: SkillStatus;
}

export interface SkillList {
  items: Skill[];
}

export interface ModelProvider {
  metadata: ObjectMeta;
  spec?: Record<string, unknown>;
  status?: Record<string, unknown>;
}

export interface ModelProviderList {
  items: ModelProvider[];
}

/** §6.4 操作相位。Unknown 不是终态：它占用机器级互斥（FR-15.4）。 */
export type OperationPhase =
  | 'Pending'
  | 'Running'
  | 'AwaitingConfirmation'
  | 'CancelRequested'
  | 'Succeeded'
  | 'Failed'
  | 'Unknown';

export type OperationType = 'Reconcile' | 'Repair' | 'Bootstrap' | 'Rollback' | 'AutoPlan';

/** §6.4：仅终态可非空。Superseded/Cancelled/Skipped 带修饰的成功不满足健康门禁。 */
export type TerminalModifier = 'Superseded' | 'Cancelled' | 'Skipped';

export interface VerifyEvidence {
  desiredProjectionDigest?: string;
  observedProjectionDigest?: string;
  canonicalizationVersion?: string;
  inventorySeq?: number;
  adapterHealth?: 'passed' | 'failed' | 'skipped';
}

export interface OperationSpec {
  machine: string;
  type: OperationType;
  transport?: 'agentd' | 'ssh';
  desiredGeneration?: number;
  readOnly?: boolean;
  planDigest?: string;
}

export interface OperationStatus {
  phase: OperationPhase;
  startedAt?: string;
  finishedAt?: string;
  terminalModifier?: TerminalModifier;
  verify?: VerifyEvidence;
  planExpiresAt?: string;
}

export interface Operation {
  metadata: ObjectMeta;
  spec: OperationSpec;
  status: OperationStatus;
}

export interface OperationList {
  items: Operation[];
}

export type DriftStatusView = 'in-sync' | 'unknown' | 'drifted';

/** GET /machines/{name}/drift 的条件视图（未记录时为 Unknown + NeverInventoried）。 */
export interface ConditionView {
  status: ConditionStatus;
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

export interface DriftEvaluation {
  DriftStatus?: ConditionStatus;
  DriftReason?: string;
  DriftMessage?: string;
  ReconciledStatus?: ConditionStatus;
  ReconciledReason?: string;
  DesiredProjectionDigest?: string;
  ObservedProjectionDigest?: string;
  CanonicalizationVersion?: string;
}

export interface MachineDrift {
  machine: string;
  drifted: ConditionView;
  reconciled: ConditionView;
  desiredGeneration?: number;
  observedGeneration?: number;
  lastInventoryAt?: string;
  inventorySeq?: number;
  desiredProjectionDigest?: string;
  observedProjectionDigest?: string;
  unresolvedOperation?: UnresolvedOperationRef;
  evaluation?: DriftEvaluation;
}

export interface RenderPreview {
  profile: string;
  machine: string;
  digest: string;
  canonicalizationVersion: string;
  desired?: {
    schemaVersion?: string;
    agents?: Record<string, { version?: string; config?: unknown }>;
    skills?: Record<string, { revision?: string; contentDigest?: string; artifactDigest?: string }>;
    mcp?: Record<string, unknown>;
    rules?: Record<string, unknown>;
  };
}

export type DeploymentPhase =
  | 'Pending'
  | 'Canary'
  | 'RollingOut'
  | 'Paused'
  | 'Succeeded'
  | 'Failed';

export type DeploymentTargetPhase =
  | 'Pending'
  | 'Running'
  | 'Succeeded'
  | 'Failed'
  | 'Superseded'
  | 'Skipped'
  | 'Blocked';

export interface DeploymentTarget {
  machine: string;
  phase: DeploymentTargetPhase;
  /** Superseded/Skipped/Failed 的原因码（FR-14.5：必须与 Failed 区分显示）。 */
  reason?: string;
  operationId?: string;
  effectiveGeneration?: number;
  updatedAt?: string;
}

export interface DeploymentStatus {
  phase?: DeploymentPhase;
  reason?: string;
  targets?: DeploymentTarget[];
}

export interface DeploymentStrategy {
  canary?: number;
  batchSize?: number;
  maxUnavailable?: number;
  pauseOnFailure?: boolean;
}

export interface DeploymentSpec {
  machineNames?: string[];
  targetGeneration?: number;
  rollbackOf?: number;
  strategy?: DeploymentStrategy;
  [key: string]: unknown;
}

export interface Deployment {
  metadata: ObjectMeta;
  spec?: DeploymentSpec;
  status?: DeploymentStatus;
}

export interface DeploymentList {
  items: Deployment[];
}

/**
 * GET /api/v1/ssh/include 的预览/导出结果（FR-12.6）：服务端渲染的 include
 * 全文（text/plain）加上 included/skipped 计数响应头。前端不自拼 include 内容，
 * 以服务端渲染为唯一权威（架构 §7.7）。
 */
export interface IncludePreview {
  content: string;
  includedHosts: number;
  skippedHosts: number;
}

/**
 * 未被导出到 include 文件的机器及原因（服务端 internal/sshtransport.SkippedHost：
 * 无 json 标签，JSON 键与 Go 字段名一致）。
 */
export interface SkippedHostEntry {
  Name: string;
  Reason: string;
}

/**
 * POST /api/v1/ssh/include（显式确认安装）的响应（FR-12.6）：只写
 * `~/.ssh/agent-fleet.conf`，绝不改写主配置——`includeDirective` 由操作者自己加。
 */
export interface IncludeInstallResult {
  path: string;
  includeDirective: string;
  contentDigest: string;
  includedHosts: string[];
  skippedHosts: SkippedHostEntry[];
  primaryConfigHint: string;
}

/** §6.4 五要素错误体。 */
export interface ApiErrorBody {
  reason: string;
  message: string;
  operationId?: string;
  timestamp: string;
  diagnostics?: Record<string, string>;
}

/** SSE 事件载荷（§8.1/§23.6）：`event: <resource-type>` + `data: {id, revision}`。 */
export interface ResourceEvent {
  /** 资源类型：事件名（= SSE 的 event 字段）。 */
  resource: ResourceType;
  /** 资源名（operations 为操作 id，等于 metadata.name）。 */
  id: string;
  /** 写入后的 metadata.resourceVersion；删除事件为删除前版本 +1。 */
  revision: number;
}

export type ResourceType =
  | 'machines'
  | 'profiles'
  | 'skills'
  | 'providers'
  | 'deployments'
  | 'operations';

export const RESOURCE_TYPES: ResourceType[] = [
  'machines',
  'profiles',
  'skills',
  'providers',
  'deployments',
  'operations',
];
