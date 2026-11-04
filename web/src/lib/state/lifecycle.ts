/**
 * 生命周期维护的纯视图模型（KM-50；无 Svelte、无 fetch，便于单测）。
 *
 * 语义边界（与 T2 域模型一致，不得在 UI 里改写）：
 *  - 逐目标四态各带原因；unknown 不计成功、不自动重试——呈现上永远与成功区分；
 *  - "尚未安装"是显式状态；未验证/阻塞组合如实展示状态与原因；
 *  - 上游更新与期望状态偏差是两回事，分开展示、各有解释；
 *  - "停止管理"保留程序，与"卸载"严格区分；
 *  - 上游查询失败时缓存只作参考：未知 ≠ 最新。
 */
import { translate, type Locale } from '../i18n/messages.js';
import type {
  LifecycleCapability,
  LifecycleDesiredDocument,
  LifecycleDesiredUpdate,
  LifecycleEntry,
  LifecycleOperation,
  LifecycleOperationCreateRequest,
  LifecycleOperationType,
  LifecycleTarget,
  LifecycleTargetResult,
  LifecycleTargetState,
  UpstreamSourceResult,
} from '../api/types.js';
import type { AgentInstance, AgentInstanceInventory } from './lifecycleGateway.js';

export const CODEX_AGENT = 'codex';
export const CODEX_PACKAGE = '@openai/codex';

/** 契约允许的精确固定版本：x.y.z[-pre][+build]；拒绝 latest/范围/空白。 */
const EXACT_VERSION_RE = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

export function isExactVersion(value: string): boolean {
  return EXACT_VERSION_RE.test(value.trim());
}

// ---------- 能力矩阵 ----------

export function capabilityFor(
  capabilities: LifecycleCapability[],
  os: string | undefined,
  arch: string | undefined,
  method: string,
): LifecycleCapability | undefined {
  return capabilities.find((c) => c.os === os && c.arch === arch && c.method === method);
}

export interface MethodOffering {
  method: string;
  capability?: LifecycleCapability;
  /** blocked 组合不提供操作入口，但状态与原因要如实展示。 */
  offered: boolean;
}

/** 按能力矩阵列出某平台可表达的安装方式：blocked 也列出（展示原因）但不提供入口。 */
export function methodOfferings(
  capabilities: LifecycleCapability[],
  os: string | undefined,
  arch: string | undefined,
): MethodOffering[] {
  const methods = new Map<string, LifecycleCapability | undefined>();
  for (const c of capabilities) {
    if (c.os !== os || c.arch !== arch) continue;
    methods.set(c.method, c);
  }
  return [...methods.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([method, capability]) => ({
      method,
      capability,
      offered: capability?.state !== 'blocked',
    }));
}

/** 能力状态的展示基调：supported=ok；unverified=warn（未验证不标支持）；blocked=danger。 */
export function capabilityTone(state: LifecycleCapability['state'] | undefined): 'ok' | 'warn' | 'danger' | 'muted' {
  if (state === 'supported') return 'ok';
  if (state === 'unverified') return 'warn';
  if (state === 'blocked') return 'danger';
  return 'muted';
}

// ---------- 目标与选择 ----------

export function envLabel(env: string | undefined, locale: Locale = 'zh-CN'): string {
  if (!env) return translate(locale, 'lc.env.native');
  return env;
}

export function instanceKey(instance: AgentInstance): string {
  return `${instance.env || 'native'}|${instance.user}|${instance.method}|${instance.package}`;
}

export function targetIdentityKey(t: LifecycleTarget): string {
  return `${t.machine}|${t.env || 'native'}|${t.user}|${t.agent}|${t.method}|${t.package}`;
}

/** 目标的一行身份描述：主机 · 环境 · 用户 · 来源/包（路径单独展示）。 */
export function targetLabel(t: LifecycleTarget, locale: Locale = 'zh-CN'): string {
  return [
    t.machine,
    envLabel(t.env, locale),
    t.user,
    `${t.method} ${t.package}`,
  ].join(' · ');
}

/** 从选择与机器台账快照构造目标（能力判定快照由调用方传入）。 */
export function buildTarget(input: {
  machine: string;
  env: string;
  user: string;
  method: string;
  pkg?: string;
  os?: string;
  arch?: string;
  capability?: LifecycleCapability;
}): LifecycleTarget {
  return {
    machine: input.machine,
    env: input.env || undefined,
    user: input.user,
    agent: CODEX_AGENT,
    method: input.method,
    package: input.pkg ?? CODEX_PACKAGE,
    os: input.os,
    arch: input.arch,
    capabilityState: input.capability?.state,
    capabilityReason: input.capability?.reason,
  };
}

/** 幂等键：优先 crypto.randomUUID，测试/旧环境回退到随机串。 */
export function newIdempotencyKey(): string {
  const c = globalThis.crypto as Crypto | undefined;
  if (c && typeof c.randomUUID === 'function') return c.randomUUID();
  return `idem-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

// ---------- 意图文档 ----------

export function desiredEntryFor(
  doc: LifecycleDesiredDocument | null | undefined,
  agent: string = CODEX_AGENT,
): LifecycleEntry | undefined {
  return doc?.entries?.[agent];
}

export function desiredPresentUpdate(version: string, method: string, pkg: string): LifecycleDesiredUpdate {
  return { entries: { [CODEX_AGENT]: { intent: 'present', version, method, package: pkg } } };
}

export function desiredAbsentUpdate(method: string, pkg: string): LifecycleDesiredUpdate {
  return { entries: { [CODEX_AGENT]: { intent: 'absent', method, package: pkg } } };
}

/** 停止管理：仅解除纳管、保留程序——与卸载严格区分（条目不携带卸载语义）。 */
export function desiredUnmanagedUpdate(): LifecycleDesiredUpdate {
  return { entries: { [CODEX_AGENT]: { intent: 'unmanaged' } } };
}

// ---------- 逐目标状态 ----------

const TARGET_STATES: LifecycleTargetState[] = ['pending', 'running', 'succeeded', 'failed', 'not_executed', 'unknown'];

export function lifecycleStateLabel(state: string, locale: Locale = 'zh-CN'): string {
  return TARGET_STATES.includes(state as LifecycleTargetState) ? translate(locale, `lc.state.${state}`) : state;
}

/** 状态基调：unknown 永远走 warn，绝不与成功共用外观；not_executed 是中性事实。 */
export function lifecycleStateTone(state: string): 'ok' | 'warn' | 'danger' | 'muted' | 'info' {
  switch (state) {
    case 'succeeded':
      return 'ok';
    case 'failed':
      return 'danger';
    case 'unknown':
      return 'warn';
    case 'running':
      return 'info';
    case 'pending':
    case 'not_executed':
      return 'muted';
    default:
      return 'muted';
  }
}

/** 契约白名单内的状态-原因配对；未知原因码原样保留（不臆造解释）。 */
const KNOWN_REASONS = new Set([
  'verified-present',
  'verified-absent',
  'version-mismatch',
  'discovery-failed',
  'op-failed',
  'unsupported-combo',
  'target-offline',
  'dispatch-failed',
  'result-lost',
  'node-disconnected',
  'timeout-no-result',
]);

export function targetReasonLabel(reason: string | undefined, locale: Locale = 'zh-CN'): string | undefined {
  if (!reason) return undefined;
  return KNOWN_REASONS.has(reason) ? translate(locale, `lc.reason.${reason}`) : reason;
}

/** 逐目标的任务导向下一步：四态各有走向，未知永远先核对、不计成功。 */
export function nextStepLabel(state: string, reason: string | undefined, locale: Locale = 'zh-CN'): string {
  if (state === 'succeeded') return translate(locale, `lc.next.${reason === 'verified-absent' ? 'verified-absent' : 'verified-present'}`);
  if (state === 'failed' && reason && KNOWN_REASONS.has(reason)) return translate(locale, `lc.next.${reason}`);
  if (state === 'not_executed' && reason && KNOWN_REASONS.has(reason)) return translate(locale, `lc.next.${reason}`);
  if (state === 'unknown') return translate(locale, 'lc.next.unknown');
  if (state === 'pending' || state === 'running') return translate(locale, `lc.next.${state}`);
  return '';
}

export interface LifecycleTargetResultView {
  result: LifecycleTargetResult;
  targetText: string;
  path?: string;
  stateText: string;
  tone: 'ok' | 'warn' | 'danger' | 'muted' | 'info';
  reasonText?: string;
  nextStep: string;
  observedVersion?: string;
  message?: string;
}

export function targetResultViews(op: LifecycleOperation, locale: Locale = 'zh-CN'): LifecycleTargetResultView[] {
  return (op.status.results ?? []).map((result) => ({
    result,
    targetText: result.target ? targetLabel(result.target, locale) : '—',
    path: result.evidence?.executablePath,
    stateText: lifecycleStateLabel(result.state, locale),
    tone: lifecycleStateTone(result.state),
    reasonText: targetReasonLabel(result.reason, locale),
    nextStep: nextStepLabel(result.state, result.reason, locale),
    observedVersion: result.evidence?.observedVersion,
    message: result.message,
  }));
}

/** 仅确定失败（failed）的目标可重试；其余状态一律不可。 */
export function failedResults(op: LifecycleOperation): LifecycleTargetResult[] {
  return (op.status.results ?? []).filter((r) => r.state === 'failed' && r.target);
}

/** 操作是否已到逐目标终局（没有 pending/running 目标）。 */
export function isOperationSettled(op: LifecycleOperation): boolean {
  return !(op.status.results ?? []).some((r) => r.state === 'pending' || r.state === 'running');
}

export interface OperationProgress {
  total: number;
  succeeded: number;
  failed: number;
  notExecuted: number;
  unknown: number;
  active: number;
}

export function operationProgress(op: LifecycleOperation): OperationProgress {
  const p: OperationProgress = { total: 0, succeeded: 0, failed: 0, notExecuted: 0, unknown: 0, active: 0 };
  for (const r of op.status.results ?? []) {
    p.total += 1;
    if (r.state === 'succeeded') p.succeeded += 1;
    else if (r.state === 'failed') p.failed += 1;
    else if (r.state === 'not_executed') p.notExecuted += 1;
    else if (r.state === 'unknown') p.unknown += 1;
    else p.active += 1;
  }
  return p;
}

/** 操作标题：类型 · 固定版本 · 操作 id（版本与 id 原样保留）。 */
export function formatOpTitle(op: LifecycleOperation, locale: Locale = 'zh-CN'): string {
  return translate(locale, 'lc.results.opTitle', {
    type: translate(locale, `lc.optype.${op.spec.type}`),
    version: op.spec.version ?? '',
    id: op.metadata.name,
  });
}

/** 重试被 needsReconfirmation 暂停时的呈现文本（原因如实带上）。 */
export function needsReconfirmationView(op: LifecycleOperation, locale: Locale = 'zh-CN'): { text: string } | null {
  if (!op.status.needsReconfirmation) return null;
  return {
    text: translate(locale, 'lc.results.needsReconfirmationDetail', {
      reason: targetReasonLabel(op.status.reconfirmReason, locale) ?? (op.status.reconfirmReason ?? '—'),
    }),
  };
}

/** 页面动作 → 契约操作类型（单一映射，页面与预览卡共用）。 */
export function opTypeOf(kind: 'install' | 'upgrade' | 'uninstall'): LifecycleOperationType {
  return kind === 'install' ? 'Install' : kind === 'upgrade' ? 'Upgrade' : 'Uninstall';
}

// ---------- 影响指纹与重新确认 ----------

/**
 * 预览影响的稳定指纹（类型 + 固定版本 + 目标身份集）。
 * 确认后指纹变化 → 影响变化，必须重新确认；同一已批准操作的重试指纹不变 → 不重复确认。
 */
export function impactFingerprint(
  type: LifecycleOperationType,
  version: string | undefined,
  targets: LifecycleTarget[],
): string {
  const ids = targets.map(targetIdentityKey).sort();
  return JSON.stringify([type, version ?? '', ids]);
}

// ---------- 上游版本发现 ----------

export type UpstreamRowView =
  | {
      kind: 'ok';
      method: string;
      pkg?: string;
      channel: string;
      version: string;
      scheme: string;
      queriedAt: string;
      platforms: { platform: string; arch: string; status: string }[];
    }
  | {
      kind: 'failed';
      method: string;
      pkg?: string;
      channel: string;
      attempts?: number;
      reasonText: string;
      detail?: string;
      /** 最近一次成功缓存（可能没有：从未成功查询过）。 */
      cacheVersion?: string;
      cacheQueriedAt?: string;
      /** failed 且无缓存：可用版本未知——绝不能显示为"最新"。 */
      unknown: boolean;
    }
  | {
      kind: 'unsupported';
      method: string;
      pkg?: string;
      channel: string;
      reasonText: string;
      detail?: string;
    };

const UPSTREAM_FAILURE_REASONS = new Set([
  'query_failed',
  'timeout',
  'canceled',
  'source_unavailable',
  'version_parse_failed',
  'method_unsupported',
  'channel_unsupported',
]);

export function upstreamFailureLabel(reason: string | undefined, locale: Locale = 'zh-CN'): string {
  return reason && UPSTREAM_FAILURE_REASONS.has(reason)
    ? translate(locale, `lc.upstreamReason.${reason}`)
    : (reason ?? translate(locale, 'lc.upstreamReason.none'));
}

export function upstreamRowView(src: UpstreamSourceResult, locale: Locale = 'zh-CN'): UpstreamRowView {
  if (src.status === 'ok' && src.version) {
    return {
      kind: 'ok',
      method: src.method,
      pkg: src.package,
      channel: src.channel,
      version: src.version.raw,
      scheme: src.version.scheme,
      queriedAt: src.queriedAt ?? '',
      platforms: (src.platforms ?? []).map((p) => ({ platform: p.platform, arch: p.arch, status: p.status })),
    };
  }
  const failure = src.failure;
  if (src.status === 'unsupported') {
    return {
      kind: 'unsupported',
      method: src.method,
      pkg: src.package,
      channel: src.channel,
      reasonText: upstreamFailureLabel(failure?.reason, locale),
      detail: failure?.detail,
    };
  }
  return {
    kind: 'failed',
    method: src.method,
    pkg: src.package,
    channel: src.channel,
    attempts: src.attempts,
    reasonText: upstreamFailureLabel(failure?.reason, locale),
    detail: failure?.detail,
    cacheVersion: src.lastSuccess?.version.raw,
    cacheQueriedAt: src.lastSuccess?.queriedAt,
    unknown: !src.lastSuccess,
  };
}

// ---------- 期望状态偏差（与上游更新分开展示） ----------

export interface DeviationView {
  /** ok=一致；deviate=偏差；info=未纳管/停止管理等中性说明。 */
  level: 'ok' | 'deviate' | 'info';
  text: string;
  detail?: string;
}

/**
 * 期望意图 vs 实际观测的偏差。实际观测来自实例发现：installedVersion 为
 * undefined 且无实例 = 尚未安装；versionError = 已装但版本不可解析（未知 ≠ 一致）。
 */
export function deviationView(
  entry: LifecycleEntry | undefined,
  installed: { version?: string; versionError?: string; present: boolean },
  locale: Locale = 'zh-CN',
): DeviationView {
  if (!entry) {
    return {
      level: 'info',
      text: translate(locale, 'lc.deviation.noIntent'),
      detail: translate(locale, 'lc.deviation.noIntentDetail'),
    };
  }
  if (entry.intent === 'unmanaged') {
    return {
      level: 'info',
      text: translate(locale, 'lc.deviation.unmanaged'),
      detail: translate(locale, 'lc.deviation.unmanagedDetail'),
    };
  }
  if (entry.intent === 'absent') {
    if (!installed.present) {
      return { level: 'ok', text: translate(locale, 'lc.deviation.absentMatched') };
    }
    return {
      level: 'deviate',
      text: translate(locale, 'lc.deviation.absentButPresent'),
      detail: translate(locale, 'lc.deviation.absentButPresentDetail'),
    };
  }
  // present
  if (!installed.present) {
    return {
      level: 'deviate',
      text: translate(locale, 'lc.deviation.presentButMissing', { version: entry.version ?? '—' }),
      detail: translate(locale, 'lc.deviation.presentButMissingDetail'),
    };
  }
  if (installed.versionError) {
    return {
      level: 'deviate',
      text: translate(locale, 'lc.deviation.versionUnknown'),
      detail: translate(locale, 'lc.deviation.versionUnknownDetail'),
    };
  }
  if (installed.version === entry.version) {
    return { level: 'ok', text: translate(locale, 'lc.deviation.matched', { version: entry.version ?? '—' }) };
  }
  return {
    level: 'deviate',
    text: translate(locale, 'lc.deviation.versionDiffers', {
      expected: entry.version ?? '—',
      actual: installed.version ?? '—',
    }),
    detail: translate(locale, 'lc.deviation.versionDiffersDetail'),
  };
}

// ---------- 实例选择的辅助 ----------

/** 某（环境, 用户）下的实例列表；空列表 = 显式"尚未安装"状态。 */
export function instancesFor(
  inventory: AgentInstanceInventory | undefined,
  env: string,
  user: string,
  agent: string = CODEX_AGENT,
): AgentInstance[] {
  return (inventory?.instances ?? []).filter((i) => (i.env || '') === env && i.user === user && i.agent === agent);
}

/** 实例的一行展示身份：来源/包 + 版本（或版本不可解析原因）+ 路径。 */
export function instanceTitle(instance: AgentInstance, locale: Locale = 'zh-CN'): string {
  const base = `${instance.method} ${instance.package}`;
  if (instance.versionError) return `${base} · ${translate(locale, 'lc.instance.versionUnresolved')}`;
  if (instance.version) return `${base} · ${instance.version}`;
  return base;
}
