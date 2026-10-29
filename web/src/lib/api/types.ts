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
  | 'operations'
  /** KM-47 起：lifecycle-desired 按机器名发事件、lifecycle-operations 按操作 id 发事件。 */
  | 'lifecycle-desired'
  | 'lifecycle-operations';

export const RESOURCE_TYPES: ResourceType[] = [
  'machines',
  'profiles',
  'skills',
  'providers',
  'deployments',
  'operations',
  'lifecycle-desired',
  'lifecycle-operations',
];

// ===== 生命周期（KM-47 契约：api/openapi/fleet-v1.yaml 的 /api/v1/lifecycle/* =====
// ===== 与 /machines/{name}/lifecycle/desired；T3 上游发现为 Upstream* 前缀）=====

/** 显式三态意图。没有"未指定"值：文档中未出现的 agent 不携带任何意图，尤其不得解释为 absent。 */
export type LifecycleIntent = 'present' | 'absent' | 'unmanaged';

/** 逐 agent 的意图条目：present 必须带精确固定版本；absent/unmanaged 必须带已识别来源。 */
export interface LifecycleEntry {
  intent: LifecycleIntent;
  /** 仅 present 允许；创建时固定的精确版本。 */
  version?: string;
  /** 安装方式/来源（能力矩阵合法取值）。 */
  method?: string;
  /** 安装包名（如 "@openai/codex"）。 */
  package?: string;
}

/** PUT 请求体：合并语义——提及的 agent 显式替换；未提及的原样保留（省略 ≠ 卸载）。 */
export interface LifecycleDesiredUpdate {
  entries: Record<string, LifecycleEntry>;
}

/** 机器的显式意图文档（最高 revision 代；404 = 从未纳管，是可查询的显式状态）。 */
export interface LifecycleDesiredDocument {
  schemaVersion: 'lifecycle-v1';
  entries: Record<string, LifecycleEntry>;
  machine?: string;
  /** 不可变代次（身份）；内容无变化不新增代。 */
  revision: number;
  /** 内容指纹（"sha256:<hex>"）。 */
  digest: string;
  createdAt?: string;
}

export type LifecycleOperationType = 'Install' | 'Upgrade' | 'Uninstall' | 'CheckUpdate';

/** 最小目标身份：主机、OS 环境、受管用户、明确安装实例。Windows 原生与 WSL 以 env 区分（空 = native）。 */
export interface LifecycleTarget {
  machine: string;
  env?: string;
  user: string;
  agent: string;
  method: string;
  package: string;
  /** 创建时从机器台账快照的平台事实（不参与身份）。 */
  os?: string;
  arch?: string;
  capabilityState?: LifecycleCapabilityState;
  capabilityReason?: string;
}

export type LifecycleCapabilityState = 'supported' | 'unverified' | 'blocked';

/** 逐目标证据（不含秘密）。observedVersion 是操作后重新发现并独立核验的实测版本。 */
export interface LifecycleEvidence {
  /** 卸载成功时为空且实例确已消失。 */
  observedVersion?: string;
  executablePath?: string;
  method?: string;
  inventorySeq?: number;
  message?: string;
  recordedAt?: string;
}

/**
 * 逐目标状态机。not_executed（提交前离线）与 unknown（结果丢失）没有出边：
 * 上线不自动执行旧意图；未知不计成功、不自动重试，只能经新操作离开。
 */
export type LifecycleTargetState = 'pending' | 'running' | 'succeeded' | 'failed' | 'not_executed' | 'unknown';

/**
 * 状态-原因配对（域模型白名单）：
 * succeeded: verified-present|verified-absent；failed: version-mismatch|discovery-failed|op-failed|unsupported-combo；
 * not_executed: target-offline|dispatch-failed；unknown: result-lost|node-disconnected|timeout-no-result。
 */
export interface LifecycleTargetResult {
  target?: LifecycleTarget;
  state: LifecycleTargetState;
  reason?: string;
  message?: string;
  evidence?: LifecycleEvidence;
}

export interface LifecycleOperationSpec {
  type: LifecycleOperationType;
  /** 调用方生成（建议 UUID）；同一键重复提交复用同一操作。 */
  idempotencyKey: string;
  /** Install/Upgrade 创建时固定的精确版本；其余类型为空。 */
  version?: string;
  targets: LifecycleTarget[];
  createdBy?: string;
}

export interface LifecycleOperationStatus {
  phase?: 'Pending' | 'Running' | 'Completed';
  results?: LifecycleTargetResult[];
  /** 操作者确认时刻；未确认不得派发。 */
  confirmedAt?: string;
  /** 重试前重发现影响变化时置位；重新确认前不得派发。 */
  needsReconfirmation?: boolean;
  reconfirmReason?: string;
  startedAt?: string;
  completedAt?: string;
}

/** 生命周期持久操作（幂等键 + 固定版本 + 逐目标状态机 + 重试追溯链）。记录不含秘密。 */
export interface LifecycleOperation {
  metadata: ObjectMeta;
  spec: LifecycleOperationSpec;
  status: LifecycleOperationStatus;
  /** 重试关联的原操作 id（追溯链）；空表示首次创建。 */
  retryOf?: string;
}

export interface LifecycleOperationCreateRequest {
  type: LifecycleOperationType;
  idempotencyKey: string;
  version?: string;
  targets: LifecycleTarget[];
}

/** 仅列出原操作中确定失败（failed）的目标；类型与版本继承原操作，不可更改。 */
export interface LifecycleRetryRequest {
  targets: LifecycleTarget[];
}

/** OS × 架构 × 安装方式的能力记录（KM-46 §7）。supported 必须有真机证据（T8 之前为空集）。 */
export interface LifecycleCapability {
  os: string;
  arch: string;
  method: string;
  state: LifecycleCapabilityState;
  /** none=无证据；isolated=隔离环境已验证（真机待验收）；real_machine=真机证据。 */
  evidenceLevel?: 'none' | 'isolated' | 'real_machine';
  reason?: string;
}

export interface LifecycleCapabilityList {
  items: LifecycleCapability[];
}

// ===== 上游版本发现（T3/KM-48：POST /api/v1/agents/{agent}/upstream-versions/check）=====

/** 上游版本及其解释方式。不假定 SemVer；scheme 显式标注，调用方不得自行猜测。 */
export interface UpstreamVersion {
  raw: string;
  scheme: 'semver' | 'source_rule' | 'unparsed';
}

export interface UpstreamFailure {
  reason:
    | 'query_failed'
    | 'timeout'
    | 'canceled'
    | 'source_unavailable'
    | 'version_parse_failed'
    | 'method_unsupported'
    | 'channel_unsupported';
  detail?: string;
}

export interface UpstreamCachedVersion {
  version: UpstreamVersion;
  queriedAt: string;
}

/** 某来源在一个 OS×架构组合上的适用性与证据（未验证不标支持）。 */
export interface UpstreamPlatformApplicability {
  platform: string;
  arch: string;
  status: 'verified_isolated' | 'unverified' | 'blocked_pending_windows' | 'blocked_upstream';
  evidence?: string;
}

/** 一个安装来源的一次检查记录。只有 status=ok 携带 version/queriedAt。 */
export interface UpstreamSourceResult {
  agentId: string;
  method: string;
  package?: string;
  /** 版本渠道；上游只查默认 latest。 */
  channel: string;
  platforms?: UpstreamPlatformApplicability[];
  status: 'ok' | 'failed' | 'unsupported';
  version?: UpstreamVersion;
  /** 本次成功查询的完成时间；仅 status=ok 时出现。 */
  queriedAt?: string;
  attempts?: number;
  failure?: UpstreamFailure;
  /** 仅 failed 时可能出现：最近一次成功缓存。缺省 = 从未成功查询过——必须显示"未知"。 */
  lastSuccess?: UpstreamCachedVersion;
}

export interface UpstreamVersionReport {
  agent: string;
  checkedAt: string;
  sources: UpstreamSourceResult[];
}
