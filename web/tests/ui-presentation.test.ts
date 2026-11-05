// @vitest-environment happy-dom
/**
 * 穷举生命周期展示：未知不算成功，查不到的版本不算已经一致。
 *
 * 这是 verification/ui-presentation/check.sh 要跑的校验，不是抽样单测。
 * 范围内的每个输入都要跑到。不要把循环收成几个例子。
 *
 * 函数的范围是契约里的状态、原因和上游查询结果，再加一个契约外的原因码。
 * 组件只在会改成功外观的分支上渲染：StateBadge 的色、ResultsPanel 的重试按钮、
 * UpstreamCard 按 kind 和有没有缓存切换的那几支。原因文案不改变这三处外观。
 */
import { describe, it } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import { setLocale } from '../src/lib/i18n/index.js';
import { translate, type Locale } from '../src/lib/i18n/messages.js';
import type { LifecycleOperation, LifecycleTargetState, UpstreamSourceResult } from '../src/lib/api/types.js';
import {
  buildTarget,
  deviationView,
  failedResults,
  lifecycleStateLabel,
  lifecycleStateTone,
  nextStepLabel,
  operationProgress,
  targetResultViews,
  upstreamRowView,
} from '../src/lib/state/lifecycle.js';
import StateBadge from '../src/lib/components/StateBadge.svelte';
import ResultsPanel from '../src/lib/components/lifecycle/ResultsPanel.svelte';
import UpstreamCard from '../src/lib/components/lifecycle/UpstreamCard.svelte';

const LOCALES: Locale[] = ['zh-CN', 'en'];

/** 与 lifecycle.ts 的 KNOWN_REASONS 相同，再加上缺省、空串，以及一个契约外的码。 */
const REASONS: (string | undefined)[] = [
  undefined,
  '',
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
  'not-in-contract',
];

/** 与 UpstreamFailure.reason 相同，再加上缺省和一个契约外的码。 */
const FAILURE_REASONS: (UpstreamSourceResult['failure'] extends infer F
  ? F extends { reason: infer R }
    ? R
    : never
  : never)[] = [
  'query_failed',
  'timeout',
  'canceled',
  'source_unavailable',
  'version_parse_failed',
  'method_unsupported',
  'channel_unsupported',
];

const VERSION_ERRORS = ['unparsed', 'not-in-contract'];

function opOf(state: LifecycleTargetState, reason: string | undefined): LifecycleOperation {
  return {
    metadata: { name: 'op-1' },
    spec: { type: 'Upgrade', idempotencyKey: 'k', version: '1.2.3', targets: [] },
    status: {
      phase: 'Completed',
      results: [
        {
          target: buildTarget({ machine: 'nuc', env: '', user: 'shelwin', method: 'npm' }),
          state,
          reason,
        },
      ],
    },
  };
}

function source(partial: Partial<UpstreamSourceResult> & Pick<UpstreamSourceResult, 'status'>): UpstreamSourceResult {
  return {
    agentId: 'codex',
    method: 'npm',
    package: '@openai/codex',
    channel: 'latest',
    ...partial,
  };
}

function render(component: unknown, props: Record<string, unknown>): { text: string; html: string } {
  document.body.replaceChildren();
  const app = mount(component as never, { target: document.body, props: props as never });
  flushSync();
  const text = document.body.textContent ?? '';
  const html = document.body.innerHTML;
  unmount(app);
  document.body.replaceChildren();
  return { text, html };
}

const failures: string[] = [];
let cases = 0;

function observe(ok: boolean, sentence: string) {
  cases += 1;
  if (!ok) failures.push(sentence);
}

function reasonWord(reason: string | undefined): string {
  return reason === undefined ? '缺省' : reason === '' ? '空串' : reason;
}

describe('未知不算成功，也不算已经一致', () => {
  it('穷举展示函数，并渲染会改外观的分支', () => {
    const unknownTone = lifecycleStateTone('unknown');
    observe(unknownTone !== 'ok', `lifecycleStateTone 收到 unknown，返回 ${unknownTone}。结果行会用成功的颜色。`);
    observe(unknownTone === 'warn', `lifecycleStateTone 收到 unknown，返回 ${unknownTone}。未知走 warn，不换别的色。`);
    observe(
      lifecycleStateTone('succeeded') === 'ok',
      `lifecycleStateTone('succeeded') 不是 ok。成功行的颜色被改掉了，这套对照失效。`,
    );

    for (const locale of LOCALES) {
      setLocale(locale);
      const successLabel = lifecycleStateLabel('succeeded', locale);
      const successNext = new Set([
        nextStepLabel('succeeded', 'verified-present', locale),
        nextStepLabel('succeeded', 'verified-absent', locale),
      ]);

      const rowTone = targetResultViews(opOf('unknown', 'result-lost'), locale)[0].tone;
      observe(
        rowTone !== 'ok',
        `targetResultViews 在 ${locale} 给 unknown 的外观是 ${rowTone}。结果行会用成功的颜色。`,
      );
      const succeeded = operationProgress(opOf('succeeded', 'verified-present'));
      observe(
        succeeded.succeeded === 1 && succeeded.unknown === 0,
        `operationProgress 没有把 succeeded 计入成功。`,
      );

      for (const reason of REASONS) {
        const label = lifecycleStateLabel('unknown', locale);
        observe(
          label !== successLabel && label.length > 0,
          `lifecycleStateLabel 在 ${locale} 把 unknown 显示成「${label}」，和成功是同一句。`,
        );
        const next = nextStepLabel('unknown', reason, locale);
        observe(
          !successNext.has(next) && next.length > 0,
          `nextStepLabel 在 ${locale} 收到 unknown 和原因 ${reasonWord(reason)}，下一步写成成功后的那一句。`,
        );
        const op = opOf('unknown', reason);
        const progress = operationProgress(op);
        observe(
          progress.total === 1 && progress.succeeded === 0 && progress.unknown === 1,
          `operationProgress 在原因 ${reasonWord(reason)} 时把 unknown 计入了成功。进度会把这台机器算作成功。`,
        );
        observe(
          failedResults(op).length === 0,
          `failedResults 在原因 ${reasonWord(reason)} 时把 unknown 放进可重试列表。未知会被再执行一次。`,
        );
        const view = targetResultViews(op, locale)[0];
        observe(
          view.stateText === label && view.nextStep === next,
          `targetResultViews 在 ${locale}、原因 ${reasonWord(reason)} 时把 unknown 写成「${view.stateText}」，下一步是「${view.nextStep}」。`,
        );
      }

      const badge = render(StateBadge, { tone: lifecycleStateTone('unknown'), label: lifecycleStateLabel('unknown', locale) });
      observe(
        badge.html.includes('tone-warn') && !badge.html.includes('tone-ok'),
        `StateBadge 收到 unknown 的色调后标了 tone-ok。这一行和成功用同一套外观。`,
      );
      const panel = render(ResultsPanel, {
        operations: [opOf('unknown', 'result-lost')],
        onretry: () => {},
        onlookup: () => {},
      });
      const retryLabel = translate(locale, 'lc.results.retry');
      observe(
        !panel.html.includes('tone-ok'),
        `ResultsPanel 在 ${locale} 给 unknown 画了 tone-ok。这一行和成功用同一套外观。`,
      );
      observe(
        !panel.text.includes(retryLabel),
        `ResultsPanel 在 ${locale} 画出了「${retryLabel}」。未知会被再执行一次。`,
      );
      observe(
        panel.text.includes(lifecycleStateLabel('unknown', locale)),
        `ResultsPanel 在 ${locale} 没有写出未知的状态文案。`,
      );

      for (const failureReason of [undefined, ...FAILURE_REASONS, 'not-in-contract' as const]) {
        for (const withCache of [false, true]) {
          for (const withVersion of [false, true]) {
            for (const status of ['failed', 'unsupported'] as const) {
              const src = source({
                status,
                version: withVersion ? { raw: '9.9.9', scheme: 'semver' } : undefined,
                failure: failureReason ? { reason: failureReason as never, detail: 'down' } : undefined,
                lastSuccess: withCache
                  ? { version: { raw: '0.1.0', scheme: 'semver' }, queriedAt: '2026-10-01T00:00:00Z' }
                  : undefined,
                attempts: 2,
              });
              const row = upstreamRowView(src, locale);
              observe(
                row.kind !== 'ok',
                `upstreamRowView 在 ${locale} 收到 status ${status}、原因 ${reasonWord(failureReason)}、${withCache ? '有' : '无'}缓存、${withVersion ? '有' : '无'}版本字段，返回 kind ok。卡片会把这次查询显示成可用版本。`,
              );
              if (row.kind === 'failed' && !withCache) {
                observe(row.unknown === true, `upstreamRowView 在查询失败且没有缓存时没有标 unknown。`);
              }
              if (row.kind === 'failed' && withCache) {
                observe(
                  row.unknown === false && row.cacheVersion === '0.1.0',
                  `upstreamRowView 把缓存版本 0.1.0 当成了这次查询的可用版本。`,
                );
              }
            }
          }
        }
      }

      const missingVersion = upstreamRowView(source({ status: 'ok' }), locale);
      observe(
        missingVersion.kind !== 'ok',
        `upstreamRowView 在 ${locale} 收到 status ok 但没有 version，返回 kind ${missingVersion.kind}。没有带回的版本会被写成可用版本。`,
      );

      for (const scheme of ['semver', 'source_rule', 'unparsed'] as const) {
        const row = upstreamRowView(
          source({ status: 'ok', version: { raw: '1.2.3', scheme }, queriedAt: '2026-11-05T00:00:00Z' }),
          locale,
        );
        observe(
          row.kind === 'ok' && row.version === '1.2.3',
          `upstreamRowView 在 ${locale} 收到成功查询 1.2.3（${scheme}），没有把它显示成可用版本。`,
        );
      }

      const available = translate(locale, 'lc.upstream.available');
      const failedCard = render(UpstreamCard, {
        state: 'ok',
        rows: [upstreamRowView(source({ status: 'failed', failure: { reason: 'query_failed' } }), locale)],
        oncheck: () => {},
        now: new Date('2026-11-05T09:00:00Z'),
      });
      observe(
        !failedCard.text.includes(available) && failedCard.text.includes(translate(locale, 'lc.upstream.unknown')),
        `UpstreamCard 在 ${locale} 把没有缓存的失败查询画成了可用版本。`,
      );
      const cachedCard = render(UpstreamCard, {
        state: 'ok',
        rows: [
          upstreamRowView(
            source({
              status: 'failed',
              failure: { reason: 'timeout' },
              lastSuccess: { version: { raw: '0.1.0', scheme: 'semver' }, queriedAt: '2026-10-01T00:00:00Z' },
            }),
            locale,
          ),
        ],
        oncheck: () => {},
        now: new Date('2026-11-05T09:00:00Z'),
      });
      observe(
        !cachedCard.text.includes(available) && cachedCard.text.includes('0.1.0'),
        `UpstreamCard 在 ${locale} 把缓存版本 0.1.0 画成了这次的可用版本。`,
      );
      const unsupportedCard = render(UpstreamCard, {
        state: 'ok',
        rows: [upstreamRowView(source({ status: 'unsupported', failure: { reason: 'method_unsupported' } }), locale)],
        oncheck: () => {},
        now: new Date('2026-11-05T09:00:00Z'),
      });
      observe(!unsupportedCard.text.includes(available), `UpstreamCard 在 ${locale} 把不受支持的来源画成了可用版本。`);
      const okCard = render(UpstreamCard, {
        state: 'ok',
        rows: [
          upstreamRowView(
            source({ status: 'ok', version: { raw: '1.2.3', scheme: 'semver' }, queriedAt: '2026-11-05T00:00:00Z' }),
            locale,
          ),
        ],
        oncheck: () => {},
        now: new Date('2026-11-05T09:00:00Z'),
      });
      observe(
        okCard.text.includes(available) && okCard.text.includes('1.2.3'),
        `UpstreamCard 在 ${locale} 没有把成功查询到的 1.2.3 画成可用版本。对照失效。`,
      );

      const entry = { intent: 'present' as const, version: '1.2.3', method: 'npm', package: '@openai/codex' };
      const matched = translate(locale, 'lc.deviation.matched', { version: '1.2.3' });
      for (const versionError of VERSION_ERRORS) {
        for (const version of ['1.2.3', '9.9.9']) {
          const shown = deviationView(entry, { present: true, version, versionError }, locale);
          observe(
            shown.level !== 'ok' && shown.text !== matched,
            `deviationView 在 ${locale} 收到 versionError ${versionError}、版本字符串 ${version}，level 是 ${shown.level}。页面会写成与期望一致。`,
          );
        }
      }
      const aligned = deviationView(entry, { present: true, version: '1.2.3' }, locale);
      observe(
        aligned.level === 'ok',
        `deviationView 在 ${locale} 收到已解析且与期望相同的 1.2.3，没有写成一致。对照失效。`,
      );
    }

    if (failures.length > 0) {
      throw new Error(['unknown-not-success: FAIL', ...failures].join('\n'));
    }
    console.log('unknown-not-success: PASS');
    console.log(`cases: ${cases}`);
  });
});
