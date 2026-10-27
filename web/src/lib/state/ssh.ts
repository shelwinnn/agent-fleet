/**
 * SSH 路径的状态呈现。
 *
 * 覆盖三件事：
 *  1. `AwaitingConfirmation`：显示"等待确认（剩余 T）"，给出确认/取消入口；
 *  2. **plan 基线失效**：确认后 apply 仍会重算 plan 并比对基线，不一致则拒绝执行
 *     并报 `ReplanRequired`（零变更），UI 必须把这条提示放在确认入口旁边；
 *  3. host-key 失败与"需升级 agentd"必须显式呈现，禁止静默降级。
 *
 * 全部面向用户的文案经 i18n 取值（locale 尾参，默认 zh-CN）；后端 reason code
 * 与服务端 message 原样保留（定位价值），未知失败给出准确的通用说明。
 */
import type { ApiError } from '../api/client';
import type { Machine, Operation } from '../api/types';
import { translate, type Locale } from '../i18n/messages.js';
import { formatDuration } from './format';
import { operationPhaseLabel, viewOperations, type OperationView } from './operations';

export interface AwaitingConfirmationView {
  operation: OperationView;
  remainingMs: number;
  remainingText: string;
  expired: boolean;
  planDigest?: string;
  /** 确认入口旁的强制提示（确认不等于计划仍有效）。 */
  baselineNotice: string;
}

export function confirmBaselineNotice(locale: Locale = 'zh-CN'): string {
  return translate(locale, 'ops.baselineNotice');
}

export function awaitingConfirmations(ops: Operation[], now: Date = new Date(), locale: Locale = 'zh-CN'): AwaitingConfirmationView[] {
  return viewOperations(ops, now, locale)
    .filter((v) => v.phase === 'AwaitingConfirmation')
    .map((v) => {
      const remainingMs = v.planRemainingMs ?? 0;
      return {
        operation: v,
        remainingMs,
        remainingText: v.planExpiresAt ? formatDuration(remainingMs, locale) : translate(locale, 'ops.noDeadline'),
        expired: v.planExpired,
        planDigest: v.planDigest,
        baselineNotice: confirmBaselineNotice(locale),
      };
    });
}

/** 计划超时提示（超时后操作以 Failed 终结、互斥释放）。 */
export function planTimeoutNotice(locale: Locale = 'zh-CN'): string {
  return translate(locale, 'ops.timeoutNotice');
}

export interface SshNotice {
  level: 'info' | 'warn' | 'danger';
  title: string;
  detail: string;
}

/** host-key 失败：标准校验保留，不出现 StrictHostKeyChecking=no。 */
export function hostKeyNotice(machine: Machine, locale: Locale = 'zh-CN'): SshNotice | null {
  const cond = machine.status?.conditions?.find((c) => c.type === 'SSHReachable');
  if (!cond || cond.status !== 'False') return null;
  const reason = cond.reason ?? '';
  const hostKeyRelated = /hostkey|host_key|knownhost|fingerprint/i.test(reason) || /host.?key/i.test(cond.message ?? '');
  if (!hostKeyRelated && cond.reason !== 'SSHUnreachable') return null;
  const open = locale === 'zh-CN' ? '（' : ' (';
  const close = locale === 'zh-CN' ? '）' : ')';
  const reasonText = reason ? `${open}reason=${reason}${close}` : '';
  return {
    level: 'danger',
    title: translate(locale, hostKeyRelated ? 'ssh.hostKey.title' : 'ssh.unreachable.title'),
    detail: translate(locale, hostKeyRelated ? 'ssh.hostKey.detail' : 'ssh.unreachable.detail', {
      message: cond.message ?? '',
      reason: reasonText,
    }),
  };
}

/** agentd 版本协商被拒：条件保持 False，绝不下发期望状态。 */
export function agentVersionNotice(machine: Machine, locale: Locale = 'zh-CN'): SshNotice | null {
  const cond = machine.status?.conditions?.find((c) => c.type === 'AgentConnected');
  if (!cond || cond.status !== 'Unknown') return null;
  const reason = cond.reason ?? '';
  if (!/version|upgrade|incompatible/i.test(reason)) return null;
  return {
    level: 'warn',
    title: translate(locale, 'ssh.agentVersion.title'),
    detail: translate(locale, 'ssh.agentVersion.detail', { message: cond.message ?? '', reason }),
  };
}

/** 动作失败后的提示映射（409 三种 reason + 400 Invalid 等）。 */
export interface ActionFailure {
  /** rejected = 服务端明确拒绝（4xx/业务 409）；failed = 未完成（网络/5xx）。 */
  kind: 'rejected' | 'failed';
  title: string;
  detail: string;
  /** 是否提供"重新 plan"之类的后续入口提示。 */
  replanHint?: string;
  unresolvedOperationId?: string;
  unresolvedPhase?: string;
}

export function describeActionFailure(err: unknown, locale: Locale = 'zh-CN'): ActionFailure {
  const api = err as ApiError;
  if (api && typeof api === 'object' && 'reason' in api) {
    const kind: ActionFailure['kind'] = api.status > 0 && api.status < 500 ? 'rejected' : 'failed';
    switch (api.reason) {
      case 'MachineBusy':
        return {
          kind,
          title: translate(locale, 'error.busy.title'),
          detail: translate(locale, 'error.busy.detail', { message: api.message }),
          unresolvedOperationId: api.unresolvedOperationId,
          unresolvedPhase: api.unresolvedPhase,
        };
      case 'ReplanRequired':
        return {
          kind,
          title: translate(locale, 'error.replan.title'),
          detail: translate(locale, 'error.replan.detail', { message: api.message }),
          replanHint: translate(locale, 'error.replan.hint'),
        };
      case 'RollbackUnsupported':
        return { kind, title: translate(locale, 'error.rollbackUnsupported.title'), detail: api.message };
      case 'DesiredStateInvalid':
        return {
          kind,
          title: translate(locale, 'error.desiredInvalid.title'),
          detail: translate(locale, 'error.desiredInvalid.detail', { message: api.message }),
        };
      case 'NotFound':
        return {
          kind,
          title: translate(locale, 'error.notFound.title'),
          detail: translate(locale, 'error.notFound.detail', { message: api.message }),
        };
      case 'Invalid':
        return {
          kind,
          title: translate(locale, 'error.invalid.title'),
          detail: translate(locale, 'error.invalid.detail', { message: api.message }),
        };
      default:
        // 未识别的业务 reason：保留原码定位，说明用准确的通用文案。
        return {
          kind,
          title: translate(locale, 'error.knownReason.title', { reason: api.reason }),
          detail: api.message,
        };
    }
  }
  // 未知原因（网络断开等）：通用说明，不把原始异常当作默认提示。
  return { kind: 'failed', title: translate(locale, 'error.requestFailed'), detail: translate(locale, 'error.unexpected') };
}

/** 错误提示的标题词：业务拒绝与未完成分开表述（网络失败不是"被拒绝"）。 */
export function actionOutcomeTitle(err: unknown, locale: Locale = 'zh-CN'): string {
  return translate(locale, describeActionFailure(err, locale).kind === 'rejected' ? 'common.actionRejected' : 'common.actionFailed');
}

/** `标题。详情` 的一行式组合（页面提示正文用）；标点随 locale。 */
export function failureText(err: unknown, locale: Locale = 'zh-CN'): string {
  const f = describeActionFailure(err, locale);
  return locale === 'zh-CN' ? `${f.title}。${f.detail}` : `${f.title}. ${f.detail}`;
}

/** 列表/详情加载失败的完整提示：对象 + 原因 + 下一步。 */
export function loadErrorText(err: unknown, locale: Locale = 'zh-CN'): string {
  const f = describeActionFailure(err, locale);
  return locale === 'zh-CN' ? `（${f.title}）${f.detail}` : ` (${f.title}) ${f.detail}`;
}

/** 尚未交付功能的统一口径：禁用入口如实标注，不假装可用。 */
export function featureUnavailableNotice(locale: Locale = 'zh-CN'): string {
  return translate(locale, 'ssh.featureUnavailable');
}

/**
 * install 结果里 skippedHosts（服务端 SkippedHost：{Name,Reason}，JSON 键为 Go
 * 字段名）的呈现：`Name（Reason）`——绝不把对象直接 join 成 [object Object]。
 */
export function formatSkippedHosts(skipped: { Name: string; Reason: string }[], locale: Locale = 'zh-CN'): string {
  const joiner = locale === 'zh-CN' ? '、' : ', ';
  const open = locale === 'zh-CN' ? '（' : ' (';
  const close = locale === 'zh-CN' ? '）' : ')';
  return skipped
    .map((s) => (s.Reason ? `${s.Name}${open}${s.Reason}${close}` : s.Name))
    .join(joiner);
}

/** 供页面提示引用的相位展示名（转发 operations 的映射，减少页面导入面）。 */
export function sshPhaseLabel(phase: string, locale: Locale = 'zh-CN'): string {
  return operationPhaseLabel(phase as Parameters<typeof operationPhaseLabel>[0], locale);
}
