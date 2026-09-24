/**
 * SSH 路径的状态呈现（FR-14.5 第 4 组、FR-12.3、FR-12.7、§9.3、§7.6）。
 *
 * 覆盖三件事：
 *  1. `AwaitingConfirmation`：显示"等待确认（剩余 T）"，给出确认/取消入口；
 *  2. **plan 基线失效**：确认后 apply 仍会重算 plan 并比对基线，不一致则拒绝执行
 *     并报 `ReplanRequired`（零变更），UI 必须把这条提示放在确认入口旁边；
 *  3. host-key 失败与"需升级 agentd"必须显式呈现，禁止静默降级（FR-12.3/§7.6）。
 */
import type { ApiError } from '../api/client';
import type { Machine, Operation } from '../api/types';
import { formatDuration } from './format';
import { viewOperations, type OperationView } from './operations';

export interface AwaitingConfirmationView {
  operation: OperationView;
  remainingMs: number;
  remainingText: string;
  expired: boolean;
  planDigest?: string;
  /** 确认入口旁的强制提示（§9.3 第 3 条：确认不等于计划仍有效）。 */
  baselineNotice: string;
}

export const CONFIRM_BASELINE_NOTICE =
  '确认只表示"确认的是这份计划"：apply 阶段仍会重取观测、重算 plan 并比对基线，基线不一致将拒绝执行、零变更，并报 ReplanRequired（需重新 plan 与重新确认）。';

export function awaitingConfirmations(ops: Operation[], now: Date = new Date()): AwaitingConfirmationView[] {
  return viewOperations(ops, now)
    .filter((v) => v.phase === 'AwaitingConfirmation')
    .map((v) => {
      const remainingMs = v.planRemainingMs ?? 0;
      return {
        operation: v,
        remainingMs,
        remainingText: v.planExpiresAt ? formatDuration(remainingMs) : '未报告截止时间',
        expired: v.planExpired,
        planDigest: v.planDigest,
        baselineNotice: CONFIRM_BASELINE_NOTICE,
      };
    });
}

/** 计划超时提示（§9.3 第 4 条：超时后操作以 Failed 终结、互斥释放）。 */
export const PLAN_TIMEOUT_NOTICE =
  '计划确认窗口超时后，该操作以 Failed 终结并释放互斥（审计保留原 planDigest 与超时时刻）；超时计划不会被继续推进，需要重新 plan。';

export interface SshNotice {
  level: 'info' | 'warn' | 'danger';
  title: string;
  detail: string;
}

/** host-key 失败（FR-12.3）：标准校验保留，禁止 StrictHostKeyChecking=no。 */
export function hostKeyNotice(machine: Machine): SshNotice | null {
  const cond = machine.status?.conditions?.find((c) => c.type === 'SSHReachable');
  if (!cond || cond.status !== 'False') return null;
  const reason = cond.reason ?? '';
  const hostKeyRelated = /hostkey|host_key|knownhost|fingerprint/i.test(reason) || /host.?key/i.test(cond.message ?? '');
  if (!hostKeyRelated && cond.reason !== 'SSHUnreachable') return null;
  return {
    level: 'danger',
    title: hostKeyRelated ? 'SSH host-key 校验失败' : 'SSH 不可达',
    detail: `${cond.message ?? ''}${cond.reason ? `（reason=${cond.reason}）` : ''}${
      hostKeyRelated
        ? '——保留标准 host-key 校验，不使用 StrictHostKeyChecking=no；请人工核对目标机指纹后重试。'
        : '——请检查网络、端口与凭据配置。'
    }`,
  };
}

/** agentd 版本协商被拒（§7.6）：条件保持 False，绝不下发期望状态。 */
export function agentVersionNotice(machine: Machine): SshNotice | null {
  const cond = machine.status?.conditions?.find((c) => c.type === 'AgentConnected');
  if (!cond || cond.status !== 'Unknown') return null;
  const reason = cond.reason ?? '';
  if (!/version|upgrade|incompatible/i.test(reason)) return null;
  return {
    level: 'warn',
    title: '需升级 agentd',
    detail: `${cond.message ?? ''}（reason=${reason}）——服务端拒绝不支持的 daemon 主版本，不会向其下发期望状态。`,
  };
}

/** 动作失败后的提示映射（409 三种 reason + 400 无效期望状态）。 */
export interface ActionFailure {
  title: string;
  detail: string;
  /** 是否提供"重新 plan"之类的后续入口提示。 */
  replanHint?: string;
  unresolvedOperationId?: string;
  unresolvedPhase?: string;
}

export function describeActionFailure(err: unknown): ActionFailure {
  const api = err as ApiError;
  if (api && typeof api === 'object' && 'reason' in api) {
    switch (api.reason) {
      case 'MachineBusy':
        return {
          title: '409 MachineBusy：该机存在未决操作',
          detail: `${api.message}。未决操作占用机器级互斥，新的变更类操作会被拒绝，Deployment 该目标保持阻塞；可用入口只有对同一操作的确认/取消/跳过。`,
          unresolvedOperationId: api.unresolvedOperationId,
          unresolvedPhase: api.unresolvedPhase,
        };
      case 'ReplanRequired':
        return {
          title: '409 ReplanRequired：计划基线已失效',
          detail: `${api.message}。确认后本地受管文件发生变化，apply 重算的基线与之不一致，因此拒绝执行且零变更。`,
          replanHint: '重新 plan 并重新确认后才会执行。',
        };
      case 'RollbackUnsupported':
        return { title: '409 RollbackUnsupported：回滚目标不可用', detail: api.message };
      case 'DesiredStateInvalid':
        return {
          title: '400 DesiredStateInvalid：期望状态不可渲染',
          detail: `${api.message}。节点阶段 1 会就此显式失败；未注册的家族（如 zcode）按范围决策原样报错，UI 不做特例。`,
        };
      case 'NotFound':
        return { title: '404 NotFound：对象不存在', detail: api.message };
      case 'Invalid':
        return {
          title: '400 Invalid：请求被服务端拒绝',
          detail: `${api.message}（§6.4 错误体原文，UI 不改写服务端拒绝理由。）`,
        };
      default:
        return { title: `${api.reason}`, detail: api.message };
    }
  }
  return { title: '请求失败', detail: err instanceof Error ? err.message : String(err) };
}

/**
 * 仍未实现的 SSH 路径动作端点（登记见 docs/ssh-only-oneshot.md）——UI 禁用入口并
 * 如实标注，不假装可用，也不暗示后续切片会自动有。
 */
export const UNIMPLEMENTED_SSH_ENDPOINTS = [
  'POST /machines/{name}/ssh/bootstrap',
  'POST /machines/{name}/ssh/repair-agentd',
];

/** SSH 路径未实现禁用入口的统一口径。 */
export const UNIMPLEMENTED_NOTICE = '未实现，见 docs/ssh-only-oneshot.md';

/**
 * 未实现的 Skill 端点（POST /skills/{name}/resolve）的口径：它不在
 * docs/ssh-only-oneshot.md 的登记范围，UI 侧的 doc-of-record 是 docs/web-ui.md §6。
 */
export const UNIMPLEMENTED_SKILL_NOTICE = '未实现，见 docs/web-ui.md';

/**
 * install 结果里 skippedHosts（服务端 SkippedHost：{Name,Reason}，JSON 键为 Go
 * 字段名）的呈现：`Name（Reason）`——绝不把对象直接 join 成 [object Object]。
 */
export function formatSkippedHosts(skipped: { Name: string; Reason: string }[]): string {
  return skipped
    .map((s) => (s.Reason ? `${s.Name}（${s.Reason}）` : s.Name))
    .join('、');
}
