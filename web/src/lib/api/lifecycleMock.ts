/**
 * 契约形状的生命周期 mock 后端（KM-50 验收："后端未就绪部分用 T2 契约的 mock 验证"）。
 *
 * 只服务于测试：形状与取值逐字段对齐 T2（KM-47）与 T3（KM-48）落地的
 * OpenAPI 契约（api/openapi/fleet-v1.yaml），并复刻服务端的关键判定——
 * 幂等去重、blocked 组合创建即拒（422）、精确版本校验（400）、仅 failed
 * 目标可重试、retryOf 追溯、needsReconfirmation 阻断——让 UI 的失败与
 * 边界路径得到真实形状的验证。生产代码绝不 import 本模块（浏览器里没有
 * 任何入口）；真实链路由 T4 的 REST 处理器与 T6 端到端收口。
 */
import { ApiError } from './client';
import type {
  LifecycleCapability,
  LifecycleDesiredDocument,
  LifecycleDesiredUpdate,
  LifecycleOperation,
  LifecycleOperationCreateRequest,
  LifecycleRetryRequest,
  LifecycleTarget,
  LifecycleTargetResult,
  UpstreamVersionReport,
} from './types';
import type { AgentInstanceInventory, LifecycleGateway } from '../state/lifecycleGateway';
import { CODEX_PACKAGE, desiredAbsentUpdate, desiredPresentUpdate, targetIdentityKey } from '../state/lifecycle';

function apiError(status: number, reason: string, message: string): ApiError {
  return new ApiError(status, { reason, message, timestamp: '2026-10-29T00:00:00Z' });
}

export const NOW_STR = '2026-10-29T08:00:00Z';

/** 能力矩阵夹具：linux npm 建模为 supported（真机证据在案）以验证放行路径；
 *  darwin 保持 unverified、windows 全部 blocked，用于验证入口关闭与如实展示。 */
export const MOCK_CAPABILITIES: LifecycleCapability[] = [
  { os: 'linux', arch: 'amd64', method: 'npm', state: 'supported', evidenceLevel: 'real_machine', reason: '真机验收通过' },
  { os: 'linux', arch: 'amd64', method: 'binary', state: 'blocked', evidenceLevel: 'none', reason: 'binary 通道不支持固定版本升级' },
  { os: 'windows', arch: 'amd64', method: 'npm', state: 'blocked', evidenceLevel: 'none', reason: 'Windows npm/.cmd shim 待实测' },
  { os: 'windows', arch: 'amd64', method: 'binary', state: 'blocked', evidenceLevel: 'none', reason: 'binary 通道阻塞' },
  { os: 'darwin', arch: 'arm64', method: 'npm', state: 'unverified', evidenceLevel: 'isolated', reason: '隔离环境已验证，真机待验收' },
  { os: 'darwin', arch: 'arm64', method: 'brew', state: 'unverified', evidenceLevel: 'none', reason: 'brew 安装器副作用未验证' },
];

export function mockInventory(): Record<string, AgentInstanceInventory> {
  return {
    'win-prod': {
      machine: 'win-prod',
      os: 'windows',
      arch: 'amd64',
      environments: [
        { env: '', users: ['ops'] },
        { env: 'wsl:ubuntu', users: ['dev'] },
      ],
      instances: [
        { env: '', user: 'ops', agent: 'codex', method: 'npm', package: CODEX_PACKAGE, version: '0.55.0', executablePath: 'C:\\Users\\ops\\AppData\\Roaming\\npm\\codex.cmd' },
        { env: 'wsl:ubuntu', user: 'dev', agent: 'codex', method: 'npm', package: CODEX_PACKAGE, version: '0.54.2', executablePath: '/home/dev/.npm-global/bin/codex' },
      ],
      inventorySeq: 41,
      discoveredAt: NOW_STR,
    },
    // darwin 无实例：安装视图里 unverified 方式如实禁用（P7）。
    'mac-ci': {
      machine: 'mac-ci',
      os: 'darwin',
      arch: 'arm64',
      environments: [{ env: '', users: ['ci2'] }],
      instances: [],
      inventorySeq: 9,
      discoveredAt: NOW_STR,
    },
    // 单用户无实例：显式"尚未安装"路径。
    'linux-build': {
      machine: 'linux-build',
      os: 'linux',
      arch: 'amd64',
      environments: [{ env: '', users: ['ci'] }],
      instances: [],
      inventorySeq: 87,
      discoveredAt: NOW_STR,
    },
    // 同一用户多实例：必须显式选择。
    'mac-studio': {
      machine: 'mac-studio',
      os: 'darwin',
      arch: 'arm64',
      environments: [{ env: '', users: ['designer'] }],
      instances: [
        { env: '', user: 'designer', agent: 'codex', method: 'npm', package: CODEX_PACKAGE, version: '0.54.0', executablePath: '/Users/designer/.npm-global/bin/codex' },
        { env: '', user: 'designer', agent: 'codex', method: 'brew', package: 'codex', version: '0.55.0', executablePath: '/opt/homebrew/bin/codex' },
      ],
      inventorySeq: 12,
      discoveredAt: NOW_STR,
      stale: true,
    },
  };
}

export function mockUpstreamReport(): UpstreamVersionReport {
  return {
    agent: 'codex',
    checkedAt: NOW_STR,
    sources: [
      { agentId: 'codex', method: 'npm', package: CODEX_PACKAGE, channel: 'latest', status: 'ok', version: { raw: '0.56.1', scheme: 'semver' }, queriedAt: NOW_STR, attempts: 1 },
      {
        agentId: 'codex',
        method: 'brew',
        channel: 'stable',
        status: 'failed',
        attempts: 3,
        failure: { reason: 'timeout', detail: 'upstream registry did not respond in time' },
        lastSuccess: { version: { raw: '0.55.0', scheme: 'source_rule' }, queriedAt: '2026-10-28T09:00:00Z' },
      },
      {
        agentId: 'codex',
        method: 'binary',
        channel: 'latest',
        status: 'failed',
        attempts: 1,
        failure: { reason: 'method_unsupported', detail: 'binary channel has no queryable registry' },
        // 无 lastSuccess：从未成功查询——必须显示"未知"，不得显示"最新"。
      },
    ],
  };
}

export class MockLifecycleBackend implements LifecycleGateway {
  inventory: Record<string, AgentInstanceInventory>;
  capabilities: LifecycleCapability[];
  desired: Record<string, LifecycleDesiredDocument | undefined> = {};
  operations = new Map<string, LifecycleOperation>();
  upstreamReport: UpstreamVersionReport;
  /** 置位后 checkUpstream 抛错（模拟后端不可达）。 */
  upstreamError: Error | null = null;
  /** 置位后 retry 一律判 needsReconfirmation（模拟重发现发现影响变化）。 */
  retryNeedsReconfirmation = false;
  /** 置位后 retry 拒绝非 failed 目标（默认开启，可关闭以测异常）。 */
  enforceRetryValidation = true;
  /** 置位后 createOperation 一律失败（模拟后端拒绝，验证 UI 不产生部分变更）。 */
  createError: Error | null = null;
  /** 置位后 confirmOperation 一律失败（模拟确认请求失败，验证恢复路径）。 */
  confirmError: Error | null = null;
  /** 置位后 createOperation 抛出一次（模拟响应丢失的网络错误），随后自动清除。 */
  transientCreateError: Error | null = null;
  /** 每次 createOperation 尝试的幂等键记录（验证 P6 键保留语义）。 */
  createKeyLog: string[] = [];
  private seq = 0;

  constructor() {
    this.inventory = mockInventory();
    this.capabilities = MOCK_CAPABILITIES.map((c) => ({ ...c }));
    this.upstreamReport = mockUpstreamReport();
    // 预置两份意图文档：win-prod=present@0.55.0（与 native 实例一致）；mac-studio 从未纳管。
    this.desired['win-prod'] = {
      schemaVersion: 'lifecycle-v1',
      machine: 'win-prod',
      entries: { codex: { intent: 'present', version: '0.55.0', method: 'npm', package: CODEX_PACKAGE } },
      revision: 3,
      digest: 'sha256:seed-win',
      createdAt: NOW_STR,
    };
  }

  private opId(): string {
    this.seq += 1;
    return `lc-op-${String(this.seq).padStart(3, '0')}`;
  }

  async getInstanceInventory(machine: string): Promise<AgentInstanceInventory> {
    const inv = this.inventory[machine];
    if (!inv) throw apiError(404, 'NotFound', `machine ${machine} has no instance inventory`);
    return JSON.parse(JSON.stringify(inv)) as AgentInstanceInventory;
  }

  async getDesired(machine: string): Promise<LifecycleDesiredDocument | null> {
    return this.desired[machine] ?? null;
  }

  async putDesired(machine: string, update: LifecycleDesiredUpdate): Promise<LifecycleDesiredDocument> {
    const cur = this.desired[machine];
    const entries = { ...(cur?.entries ?? {}) };
    let changed = false;
    for (const [agent, entry] of Object.entries(update.entries)) {
      const prev = entries[agent];
      if (!prev || prev.intent !== entry.intent || prev.version !== entry.version || prev.method !== entry.method || prev.package !== entry.package) {
        changed = true;
      }
      entries[agent] = { ...entry };
    }
    const revision = (cur?.revision ?? 0) + (changed ? 1 : 0);
    const doc: LifecycleDesiredDocument = {
      schemaVersion: 'lifecycle-v1',
      machine,
      entries,
      revision,
      digest: changed ? `sha256:${machine}-r${revision}` : (cur?.digest ?? `sha256:${machine}-r0`),
      createdAt: cur?.createdAt ?? NOW_STR,
    };
    this.desired[machine] = doc;
    return JSON.parse(JSON.stringify(doc)) as LifecycleDesiredDocument;
  }

  async getCapabilities(): Promise<LifecycleCapability[]> {
    return this.capabilities.map((c) => ({ ...c }));
  }

  async createOperation(req: LifecycleOperationCreateRequest): Promise<{ operation: LifecycleOperation; reused: boolean }> {
    this.createKeyLog.push(req.idempotencyKey);
    if (this.transientCreateError) {
      const err = this.transientCreateError;
      this.transientCreateError = null;
      throw err;
    }
    if (this.createError) throw this.createError;
    const existing = [...this.operations.values()].find((o) => o.spec.idempotencyKey === req.idempotencyKey);
    if (existing) {
      const same = existing.spec.type === req.type && existing.spec.version === req.version
        && JSON.stringify(existing.spec.targets.map(targetIdentityKey).sort())
          === JSON.stringify(req.targets.map(targetIdentityKey).sort());
      if (!same) throw apiError(409, 'IdempotencyConflict', 'same key with different content');
      return { operation: JSON.parse(JSON.stringify(existing)) as LifecycleOperation, reused: true };
    }
    if ((req.type === 'Install' || req.type === 'Upgrade') && !/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$/.test(req.version ?? '')) {
      throw apiError(400, 'Invalid', 'Install/Upgrade require an exact version');
    }
    if (req.type === 'Uninstall' && req.targets.some((t) => !t.method || !t.package)) {
      throw apiError(400, 'Invalid', 'Uninstall targets must have an identified source');
    }
    for (const t of req.targets) {
      const cap = this.capabilities.find((c) => c.os === t.os && c.arch === t.arch && c.method === t.method);
      if (cap?.state === 'blocked') {
        throw apiError(422, 'UnsupportedCombination', `blocked combo ${t.os}/${t.arch}/${t.method}: ${cap.reason ?? ''}`);
      }
    }
    const op: LifecycleOperation = {
      metadata: { name: this.opId(), creationTimestamp: NOW_STR, resourceVersion: 1 },
      spec: {
        type: req.type,
        idempotencyKey: req.idempotencyKey,
        version: req.type === 'Install' || req.type === 'Upgrade' ? req.version : undefined,
        targets: JSON.parse(JSON.stringify(req.targets)) as LifecycleTarget[],
      },
      status: {
        phase: 'Pending',
        results: req.targets.map((t) => ({ target: t, state: 'pending' }) as LifecycleTargetResult),
      },
    };
    this.operations.set(op.metadata.name, op);
    return { operation: JSON.parse(JSON.stringify(op)) as LifecycleOperation, reused: false };
  }

  async getOperation(opId: string): Promise<LifecycleOperation> {
    const op = this.operations.get(opId);
    if (!op) throw apiError(404, 'NotFound', `operation ${opId} not found`);
    return JSON.parse(JSON.stringify(op)) as LifecycleOperation;
  }

  /**
   * 记录操作者确认（对齐 T4 控制器 Confirm/MarkConfirmed）：幂等置确认时刻并
   * 清除 needsReconfirmation；重复确认不产生重复变更。
   */
  async confirmOperation(opId: string): Promise<LifecycleOperation> {
    if (this.confirmError) throw this.confirmError;
    const op = this.operations.get(opId);
    if (!op) throw apiError(404, 'NotFound', `operation ${opId} not found`);
    op.status.confirmedAt = NOW_STR;
    op.status.needsReconfirmation = false;
    op.status.reconfirmReason = undefined;
    op.metadata.resourceVersion = (op.metadata.resourceVersion ?? 0) + 1;
    return JSON.parse(JSON.stringify(op)) as LifecycleOperation;
  }

  /** 该操作是否可派发（对齐控制器闸门：confirmedAt 为空或 needsReconfirmation 时不派发）。 */
  dispatchable(opId: string): boolean {
    const op = this.operations.get(opId);
    return !!op && op.status.confirmedAt != null && !op.status.needsReconfirmation;
  }

  /** 测试推进逐目标状态：模拟执行结果（真实执行由 T4 引擎承担）。未确认的操作不派发。 */
  settleOperation(opId: string, results: { targetKey: (t: LifecycleTarget) => boolean; state: LifecycleTargetResult['state']; reason?: string; message?: string; observedVersion?: string }[]) {
    const op = this.operations.get(opId);
    if (!op) throw new Error(`unknown op ${opId}`);
    if (op.status.confirmedAt == null || op.status.needsReconfirmation) {
      throw new Error(`operation ${opId} is not dispatchable: unconfirmed or needs reconfirmation`);
    }
    op.status.results = (op.status.results ?? []).map((r) => {
      const hit = results.find((x) => x.targetKey(r.target as LifecycleTarget));
      if (!hit) return r;
      return {
        ...r,
        state: hit.state,
        reason: hit.reason,
        message: hit.message,
        evidence: hit.observedVersion !== undefined || hit.state === 'succeeded'
          ? { observedVersion: hit.observedVersion, method: r.target?.method, recordedAt: NOW_STR }
          : r.evidence,
      } as LifecycleTargetResult;
    });
    op.status.phase = 'Completed';
    op.status.completedAt = NOW_STR;
  }

  async retryOperation(opId: string, req: LifecycleRetryRequest): Promise<LifecycleOperation> {
    const original = this.operations.get(opId);
    if (!original) throw apiError(404, 'NotFound', `operation ${opId} not found`);
    if (this.enforceRetryValidation) {
      const failedIds = new Set(
        (original.status.results ?? []).filter((r) => r.state === 'failed').map((r) => targetIdentityKey(r.target as LifecycleTarget)),
      );
      if (req.targets.some((t) => !failedIds.has(targetIdentityKey(t)))) {
        throw apiError(409, 'RetryNotAllowed', 'only failed targets can be retried');
      }
    }
    const op: LifecycleOperation = {
      metadata: { name: this.opId(), creationTimestamp: NOW_STR, resourceVersion: 1 },
      spec: {
        type: original.spec.type,
        idempotencyKey: `retry-${original.spec.idempotencyKey}-${this.seq + 1}`,
        version: original.spec.version,
        targets: JSON.parse(JSON.stringify(req.targets)) as LifecycleTarget[],
      },
      status: {
        phase: 'Pending',
        results: req.targets.map((t) => ({ target: t, state: 'pending' }) as LifecycleTargetResult),
        // 同一已批准操作的重试不重复确认：仅在原操作已确认时继承
        //（对齐 internal/controller/lifecycle/controller.go——未确认原操作的重试保持未确认）。
        confirmedAt: original.status.confirmedAt ?? undefined,
        ...(this.retryNeedsReconfirmation
          ? { needsReconfirmation: true, reconfirmReason: 'rediscovered target state differs from the approved premise' }
          : {}),
      },
      retryOf: opId,
    };
    this.seq += 1;
    this.operations.set(op.metadata.name, op);
    return JSON.parse(JSON.stringify(op)) as LifecycleOperation;
  }

  async checkUpstream(): Promise<UpstreamVersionReport> {
    if (this.upstreamError) throw this.upstreamError;
    return JSON.parse(JSON.stringify(this.upstreamReport)) as UpstreamVersionReport;
  }

  /** 便捷：把意图设为 present（安装预演用）。 */
  seedPresent(machine: string, version: string, method = 'npm'): void {
    void this.putDesired(machine, desiredPresentUpdate(version, method, CODEX_PACKAGE));
  }

  /** 便捷：把意图设为 absent。 */
  seedAbsent(machine: string, method = 'npm'): void {
    void this.putDesired(machine, desiredAbsentUpdate(method, CODEX_PACKAGE));
  }
}
