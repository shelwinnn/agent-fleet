---
name: verify-ui-presentation
description: >-
  Check that a lifecycle result shown as unknown is never painted or counted
  as success, and that a failed or version-less upstream query is never shown
  as the available version. Use when editing lifecycleStateTone,
  lifecycleStateLabel, nextStepLabel, operationProgress, failedResults,
  targetResultViews, upstreamRowView, deviationView, StateBadge, UpstreamCard,
  or ResultsPanel.
---

# 校验生命周期界面的未知

一次安装、升级或卸载的结果是未知，一次上游查询没有带着版本成功返回，或者已安装实例标了 versionError 时，页面不会把它显示成成功或已经一致。

破坏时的损害：操作者会以为这台机器已经做完，或以为可用版本已经核对过，于是不再检查。

这条校验在当前产品代码上通过。`check.sh` 退出码 0 的意思是：范围内每种输入都没有被显示成成功或已经一致。不要为了让它继续通过而放宽断言。

## 何时要跑

改了下面任一处，或改了调用它们的方式，就跑。同步提交是产品代码提交 `5ef6242df82683481e32e89c449163bdd148c6fa`。先看这个提交之后，下表里的文件有没有变化：

```sh
git diff 5ef6242df82683481e32e89c449163bdd148c6fa -- \
  web/src/lib/state/lifecycle.ts \
  web/src/lib/components/StateBadge.svelte \
  web/src/lib/components/lifecycle/UpstreamCard.svelte \
  web/src/lib/components/lifecycle/ResultsPanel.svelte \
  web/src/lib/i18n/messages.ts \
  web/src/lib/api/types.ts
```

有变化就先读对应函数，再决定校验还否描述现在的代码。只有重新读过、确认 `web/tests/ui-presentation.test.ts` 仍然描述那个新提交上的代码，并且 `check.sh` 的结果与下面的记录一致之后，才把同步提交往前移。同时改技能里的 cases 数。产品代码故意把未知画成成功时，不要改校验，也不要前移提交。

| 代码 | 校验在看什么 |
|---|---|
| `lifecycleStateTone` | `unknown` 返回 `warn`，不是 `ok` |
| `lifecycleStateLabel` | `unknown` 的文案不是 `succeeded` 的文案 |
| `nextStepLabel` | `unknown` 的下一步不是 `verified-present` 或 `verified-absent` 那两句 |
| `operationProgress` | 一条 `unknown` 记在 `unknown` 上，`succeeded` 仍是 0 |
| `failedResults` | `unknown` 不进可重试列表 |
| `targetResultViews` | 上面四项反映到一行结果上 |
| `upstreamRowView` | `status` 不是带着 `version` 的 `ok` 时，`kind` 不是 `ok`。失败且没有 `lastSuccess` 时 `unknown` 为真。有缓存时缓存版本留在 `cacheVersion`，不成为这次的可用版本 |
| `deviationView` | `intent` 为 `present` 且 `versionError` 有值时，`level` 不是 `ok`，哪怕版本字符串和期望相同 |
| `StateBadge.svelte` | 把 `unknown` 的色调画成 `tone-warn`，不画成 `tone-ok` |
| `UpstreamCard.svelte` | 只有 `row.kind === 'ok'` 时写出 `lc.upstream.available` |
| `ResultsPanel.svelte` | 只有 `failedResults` 非空时画出 `lc.results.retry` |
| `messages.ts` 的 `lc.state.unknown`、`lc.state.succeeded`、`lc.next.unknown`、`lc.next.verified-present`、`lc.next.verified-absent`、`lc.upstream.available`、`lc.upstream.unknown`、`lc.results.retry`、`lc.deviation.matched` | 未知文案和成功文案不是同一句 |

`LifecycleTargetState` 的六个值在 `web/src/lib/api/types.ts`。`upstreamRowView` 只把 `status === 'ok'` 且 `version` 存在的来源当成可用版本，其余落到 `failed` 或 `unsupported`。

## 怎么跑

在仓库根目录：

```sh
verification/ui-presentation/check.sh
```

脚本在 `web/node_modules/.bin/vitest` 不存在时，用 pnpm 10 执行 `pnpm install --frozen-lockfile`。PATH 里的 pnpm 如果是不能运行的空壳，脚本会退出 2，并要求先在 `web/` 装好依赖。Vitest 不是 5.0.1 也退出 2。退出 2 是工具问题，不是这条承诺被打破。

确认这次描述的是上面那个提交时，用的是 Node v26.4.0。脚本不锁定 Node 的补丁版本。Vitest 锁定为 `web/pnpm-lock.yaml` 里的 5.0.1。一次跑完大约两秒。

通过时输出里有这几行：

```
node v26.4.0
vitest 5.0.1
unknown-not-success: PASS
cases: 397
```

`node` 那一行随机器变化。`cases: 397` 在这个同步提交上必须不变。变少说明有输入没跑到。

## 范围

问的是这些函数对每一个输入是否都成立。状态、原因、语言、上游查询结果都是有限集合。仓库里没有能直接验证 TypeScript 的证明器，所以校验导入真实模块，把范围内的输入逐个送进去，并渲染真正会改成功外观的组件分支。不另写一份手写模型。

范围内的输入：

- 语言：`zh-CN` 和 `en`，也就是 `Locale` 的全部取值。
- 目标状态：`unknown` 与作为对照的 `succeeded`。原因是 `lifecycle.ts` 里 `KNOWN_REASONS` 的全部 11 个，再加上缺省、空串，以及一个契约外的码 `not-in-contract`。
- 上游：`status` 为 `failed` 或 `unsupported`。失败原因是 `UpstreamFailure.reason` 的 7 个，再加上缺省和 `not-in-contract`。缓存有或无，版本字段有或无。另加 `status` 为 `ok` 但没有 `version`，以及 `ok` 且版本 `1.2.3`、scheme 分别为 `semver`、`source_rule`、`unparsed`。
- 偏差：期望 `present`、版本 `1.2.3`。`versionError` 为 `unparsed` 或 `not-in-contract`，版本字符串为 `1.2.3` 或 `9.9.9`。对照是没有 `versionError` 且版本相同。
- 组件只渲染会改成功外观的分支，每种语言一次：`StateBadge` 接收 `unknown` 的色调，`ResultsPanel` 接收一条 `unknown` 结果，`UpstreamCard` 接收无缓存的失败、有缓存的失败、不受支持、以及一次成功查询。原因文案不改变这三处分支。

不在范围内：`tone-ok` 的像素和 CSS，`.scratch/` 里的原型，控制器的派发顺序（那是 `verify-lifecycle-results`），操作者能否选中已经一致的机器，除上述两个组件以外的页面，以及上面那个契约外原因码之外的其他自由字符串。`unknown` 的下一步不读取原因，所以一个契约外的码代表其余字符串。通过只说明这个范围，不说明整份界面。

## 失败时怎么读

失败的第一行是 `unknown-not-success: FAIL`。后面每一句都点了函数或组件的名字，以及送进去的状态、原因或语言。按那一句打开表里的代码。句子和代码返回值一致，就是产品缺陷，留着，不要改断言，也不要改产品代码，除非用户要求修。

句子里的输入是函数根本收不到的值，或 Vitest 无法导入模块，才是校验本身写错。改 `web/tests/ui-presentation.test.ts` 或 `check.sh`，不要改产品代码来迁就它。

对照句（成功行不是 `ok`、成功查询没有显示版本、已解析且版本相同却不是一致）失败时，先看函数签名是不是变了、夹具是不是过期。签名变了就更新夹具。函数不再把一次真正的成功显示出来，按产品变化报告，不要删掉对照。

故意改动了呈现，例如给未知增加一种仍然不是 `ok` 的色，先改校验让它描述新代码，跑通，再按上面的规则前移同步提交。覆盖的代码变了却没有重跑时，用 `/maintain-formal-verification-skill` 把校验和代码重新对齐。
