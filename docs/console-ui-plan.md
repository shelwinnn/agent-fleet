# 控制台界面计划

`web/` 里的控制台改成 `.scratch/ui-directions/index.html` 已定稿的外壳和六个画面。操作者看到这套控制台。下一任维护者继续用现有的生命周期函数上色。未知不用成功色。失败的上游查询不当成可用版本。PR 顺序是 ui-shell，然后 ui-overview、ui-devices、ui-host、ui-apps、ui-agent、ui-skills。

## How to read this

一格是一件事。格子写明用什么证据打勾。嵌在里面的格子是上面那格的子步骤。证据要是文件、日志行、截图、一次测试或一个 SHA，才打勾。正文是操作步骤。附录解释和记录。

检查脚本要在这一节里匹配到下面这几句原文，不要改字。

One box is one unit of work. Every box names the evidence that checks it. A nested box is a sub-step of the box above it. Check a box only when its evidence exists, a file, a log line, a screenshot, a test run, or a SHA. The body is a how-to. The appendices explain and record.

程序按 `pstack/skills/poteto-mode/playbooks/autopilot-stack.md` 跑。没有人代为合并。审查关过了之后，由操作者落地每一份 PR。ui-shell、ui-overview、ui-devices、ui-host、ui-apps、ui-agent、ui-skills 都停在可合并。

Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

## Program checklist

### Arm the program

- [ ] 把这份计划和执行方式告诉操作者，然后停下。只有操作者明确说开始，才执行。
- [ ] 确认所有者拉到的分支上有 `verification/ui-presentation/check.sh`、`web/tests/ui-presentation.test.ts` 和 `.cursor/skills/verify-ui-presentation/SKILL.md`。还是未跟踪文件就停下并告诉操作者。不要改写这个校验。
- [ ] 程序开始时从主干读下面这些文件。每一拍都重读。
  - [ ] `git show origin/main:pstack/skills/poteto-mode/playbooks/autopilot-stack.md`
  - [ ] `git show origin/main:pstack/skills/swarm/SKILL.md`
  - [ ] `git show origin/main:pstack/skills/control-ui/SKILL.md`
  - [ ] `git show origin/main:pstack/skills/poteto-mode/playbooks/opening-a-pr.md`
  - [ ] `git show origin/main:pstack/skills/principle-sequence-verifiable-units/SKILL.md`
  - [ ] `git show origin/main:pstack/skills/principle-prove-it-works/SKILL.md`
  - [ ] `git show origin/main:pstack/skills/unslop/SKILL.md`
- [ ] 操作者说开始之后，用下面的提示词武装审计节拍 `/loop 1h`。不要靠记忆记间隔。
- [ ] 原样使用这句提示词。"Re-read the execution playbook from trunk. Audit the operation against it and fix drift in this tick. Probe every active lane and judge progress by side effects only. Stand down a stuck lane and dispatch its replacement now. Then post a short status message to the operator in chat only when the audit found a tracked change that no earlier status message reported, such as a PR opened, a code-ready head, a round launched or closed, a verdict, a merge, a stuck agent and the action taken, a blocker added or cleared, or a decision only the operator can make. Name every such change and nothing else. Do not repeat a table, the merged list, or an unchanged blocker. If the audit found none, end the turn with no reply text. Either way, log this tick's row in your decision trail. The row names the items reported, or none."
- [ ] 操作者说暂停或停止时，立刻给每个所有者发零写入命令。

### Spawn owners

- [ ] 按执行手册给每份 PR 派一个所有者，走完它写的生命周期。
- [ ] 按下面的依赖图做。父 PR 合并之后才开始依赖它的工作。执行手册要求叠放时，子 PR 基于父分支。
  - [ ] ui-shell 最先，从 `main` 拉出。
  - [ ] ui-overview、ui-devices、ui-host、ui-apps、ui-agent、ui-skills 彼此独立，都基于 ui-shell。
- [ ] 守住文件边界。ui-shell 只动 `web/index.html`、`web/src/App.svelte`、`web/src/app.css`、`web/src/lib/i18n/messages.ts`、`web/src/lib/components/shell/LogoMark.svelte`、`web/src/routes/Apps.svelte`、`web/tests/i18n.test.ts`、`web/tests/shell.test.ts`。ui-overview 只动 `web/src/routes/Overview.svelte`、`web/tests/overview.test.ts`，以及在 `web/src/lib/i18n/messages.ts` 里追加的键。ui-devices 只动 `web/src/routes/Machines.svelte`、`web/tests/devices.test.ts`，以及 `messages.ts` 里追加的键。ui-host 只动 `web/src/routes/MachineDetail.svelte`、`web/tests/host.test.ts`，以及 `messages.ts` 里追加的键。ui-apps 只动 `web/src/routes/Apps.svelte`、`web/tests/apps.test.ts`，以及 `messages.ts` 里追加的键。ui-agent 只动 `web/src/routes/Lifecycle.svelte`、`web/tests/agent.test.ts`，以及 `messages.ts` 里追加的键。ui-skills 只动 `web/src/routes/Skills.svelte`、`web/tests/skills-page.test.ts`，以及 `messages.ts` 里追加的键。任何 PR 都不改 `web/src/lib/state/lifecycle.ts`、`web/src/lib/components/StateBadge.svelte`、`web/src/lib/components/lifecycle/UpstreamCard.svelte`、`web/src/lib/components/lifecycle/ResultsPanel.svelte`。任何 PR 都不改 `web/src/app.css` 里的 `.tone-ok`、`.tone-warn`、`.tone-danger`、`.tone-muted`、`.tone-info`。任何 PR 都不改 `messages.ts` 里名字以 `lc.` 开头的键。任何 PR 都不改 `web/src/lib/components/ui/`。
- [ ] 守住审查关。ui-shell、ui-overview、ui-devices、ui-host、ui-apps、ui-agent、ui-skills 都改了交互。合并前要等操作者在聊天里看过截图和视频。

### PR mechanics, for every PR

- [ ] 只选定一次代码托管。默认用 `gh`。若 `command -v origin` 成功且 Origin 能解析这个仓库，则所有 PR 操作用 `origin pr`。退回 `gh` 时记下来。不要依赖 `gt`。
- [ ] 按 **Opening a PR** 直接打开可审查的 PR，不要开草稿。运行环境自带 PR 工具就用它，否则按选定的托管用 `origin pr create --status open --base <base-branch>` 或 `gh pr create --base <base-branch>`。叠放的子 PR 以父分支为基。
- [ ] 面向 PR 的推送之前，在 `web/` 跑一次 `pnpm check`。推送时保留钩子。
- [ ] 每次提交前跑 `/deslop`。审查前跑 `/no-comments`。
- [ ] 按 `../references/bugbot-triage.md` 处理每条 Bugbot 和安全审查评论。
- [ ] 代码就绪报告和后续看护之前，变基到父分支。ui-shell 变基到当前主干。修复轮次保持这个合并基。只在合并准备、与父分支 `git merge-tree` 冲突、父分支改动导致 CI 失败、或已知的同一批文件并发改动落地时，才再次变基。

### Verdict and merge, for every PR

- [ ] 完整的一组核对只在代码就绪的那个提交上跑一次。只有新行为、重做或很大的新差异才再跑一整组。变基、解冲突或小修复只在新提交上派一个核对者。只改措辞则不派。按 `pstack/skills/swarm/SKILL.md` 跑完整组。一条门禁。本 PR **Verify, live** 里的十条现场记录。**Verify, perf** 里的性能记录。至少两条审计，各看一个重点，读差异和收据，不信 PR 正文。根在给出结论前审计合并就绪报告里的收据。
- [ ] 按执行手册的阻塞规则判断每条发现。没有阻塞项才算干净。标了 `Regression lane against trunk.` 的记录若失败在主干已有的行为上，记在主干名下，不阻塞这份 PR。新提交要新结论。`playbooks/shipping.md` 里 patch-id 规则仍然有效的结果除外。
- [ ] 停在已验证。由操作者落地每份 PR。交出之后，没有操作者的允许就不要再推。变基后 `git patch-id` 不变的，结论保留，见 `playbooks/shipping.md`。

### Boot recipe, for every live lane

每条现场记录在 PR 头的一台云虚拟机上跑。这个检出里没有 `control-ui`。用 `cursor-ide-browser` 驱动浏览器。

- [ ] `git fetch origin <head-branch> && git checkout <head SHA>`。
- [ ] 在 `web/` 里先 `pnpm install --frozen-lockfile`，再 `pnpm exec vite --host 127.0.0.1 --port 4173`。等到 `http://127.0.0.1:4173/` 有响应。
- [ ] 只通过 `browser_navigate`、`browser_lock`、`browser_click`、`browser_press_key`、`browser_fill` 输入。只读诊断是 `browser_snapshot` 和 `browser_take_screenshot`。
- [ ] 每张截图存到 `/tmp/swarm-<pr-id>/worker-<n>/<slug>.png`，并在报告里带回路径。

## 装上控制台外壳 (ui-shell)

**Depends on.** 无。

**Files.**

- [ ] 改 `web/index.html`。
- [ ] 改 `web/src/App.svelte`。
- [ ] 改 `web/src/app.css`。
- [ ] 改 `web/src/lib/i18n/messages.ts`。
- [ ] 改 `web/tests/i18n.test.ts`。
- [ ] 新建 `web/src/lib/components/shell/LogoMark.svelte`。
- [ ] 新建 `web/src/routes/Apps.svelte`。
- [ ] 新建 `web/tests/shell.test.ts`。

**Build.**

- [ ] 把定稿的页眉和导航放进 `App.svelte`。锁头是 `LogoMark.svelte` 加文字 agent-fleet。SVG 里没有文字。页眉顺序是锁头、范围「我的 N 台」、搜索、铃铛「要处理」、帮助、账号标记。导航分组是总览，资源下的设备，软件下的应用和技能。收起按钮在导航底部，展开时写收起菜单，收成轨时写展开菜单。
- [ ] 应用指向 `#/apps`。`Apps.svelte` 先只放标题「应用」。表格留给 ui-apps。
- [ ] `zh-CN` 和 `en` 的 `app.title` 都改成 agent-fleet。`web/index.html` 的 `<title>` 用同一个词。`messages.ts` 只改 `app.title` 和 `nav.*`。不要改名字以 `lc.` 开头的键。
- [ ] 在 `app.css` 里另加控制台外壳的颜色。`.tone-ok`、`.tone-warn`、`.tone-danger`、`.tone-muted`、`.tone-info` 逐字节保持原样。路由表留在 `web/src/lib/state/router.svelte.ts`。导航里不加配置档、发布、SSH 清单。这些路由仍能用 hash 打开。

**You see.**

- [ ] `#/` 能看到锁头文字 agent-fleet、分组导航，以及导航底部的收起菜单。
- [ ] 点收起菜单后，文字标签消失，按钮变成展开菜单。
- [ ] `#/lifecycle` 仍画在新外壳里。Codex 维护不是顶层导航项。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `web/tests/shell.test.ts` 断言锁头文字是 agent-fleet，导航有总览、设备、应用、技能，以及点击前后页脚按钮的文字。`web/tests/i18n.test.ts` 断言两种语言的 `app.title` 都是 agent-fleet。在仓库根运行 `verification/ui-presentation/check.sh`。在 `web/` 运行 `pnpm exec vitest run tests/shell.test.ts tests/i18n.test.ts`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `grok-4.7-high` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在主干和 head 上打开 `#/`。Save `shell-regression.png`. Pass when head 的锁头是 agent-fleet，并且记录引用了主干页眉的文字。
- [ ] Lane 2. 打开展开的 `#/`。Save `shell-nav.png`. Pass when 能看到总览、资源、设备、软件、应用、技能和收起菜单，并且 Codex 维护不是顶层项。
- [ ] Lane 3. 点收起菜单。Save `shell-rail.png`. Pass when 按钮是展开菜单，导航标签消失。
- [ ] Lane 4. 点展开菜单。Save `shell-expanded.png`. Pass when 收起菜单和导航标签回来。
- [ ] Lane 5. 打开范围。Save `shell-scope.png`. Pass when 「我的 N 台」展开，并且能看到全部设备。
- [ ] Lane 6. 打开铃铛。Save `shell-notify.png`. Pass when 控件名里有要处理。
- [ ] Lane 7. 打开帮助。Save `shell-help.png`. Pass when 成功、失败、未执行、未知都可见。
- [ ] Lane 8. 打开账号。Save `shell-account.png`. Pass when 账号标记可见，并且 logo 的 SVG 没有文本节点。
- [ ] Lane 9. 按 `/`。Save `shell-search.png`. Pass when 搜索框获得焦点。
- [ ] Lane 10. 打开 `#/lifecycle` 和 `#/apps`。Save `shell-routes.png`. Pass when 两个地址上都还有锁头，并且 `#/apps` 的标题是应用。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. 从打开页面到页眉文字出现的毫秒数。主干和 head 都量。
- [ ] Probe. 主干和 head 交替各打开一次 `#/`。从浏览器性能时间线读数。两边都要有毫秒数。
- [ ] Baseline. 先记下主干的毫秒数。
- [ ] Rule. head 比主干慢过 300 毫秒则失败。2000 毫秒还没有页眉也失败。这两个数是本计划设的预算。这次没有量过页面加载。

**Review gate.** 合并前由操作者审查。

- [ ] 把 lane 2 和 lane 3 的 screenshot 复制到 `/Users/shelwin/Library/Application Support/Cursor/AgentStores/cursor_agent_stores/779c368a-b954-43ef-8a88-dc4caf1e264a/files/docs/media/ui-shell-review-nav.png`，以及同一目录的 `ui-shell-review-rail.png`。
- [ ] 录一段 30 到 60 秒的 video，内容是收起菜单、帮助气泡，以及外壳里的 `#/lifecycle`。存为同一目录的 `ui-shell-review.mp4`。
- [ ] 把 screenshot 和 video 发到聊天里。停在可合并。等 operator 点头。

**Merge.**

- [ ] 根在这个确切的 head SHA 上给出干净结论。
- [ ] Bugbot 评论已分流。
- [ ] 结论之后变基到当前主干，patch-id 不变。
- [ ] 根停在已验证。操作者落地 ui-shell。

## 显示总览 (ui-overview)

**Depends on.** ui-shell.

**Files.**

- [ ] 改 `web/src/routes/Overview.svelte`。
- [ ] 改 `web/src/lib/i18n/messages.ts`。
- [ ] 新建 `web/tests/overview.test.ts`。

**Build.**

- [ ] 在 `Overview.svelte` 画出定稿总览。四个计数是设备、在线、失败、版本未知。两张表是要处理和可以更新。主按钮是打开 Codex。导语是「先处理失败和未知。可以更新的列在下面。」
- [ ] 未知的颜色继续读现有的展示函数。不要改那些函数。

**You see.**

- [ ] `#/` 能看到四个计数、两张表和打开 Codex。
- [ ] 接口没有数据时，标题仍在。不要把表藏掉。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `web/tests/overview.test.ts` 断言四个计数标签、两个表题和打开 Codex。在 `web/` 运行 `pnpm exec vitest run tests/overview.test.ts`。在仓库根运行 `verification/ui-presentation/check.sh`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `grok-4.7-high` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在主干和 head 上打开 `#/`。Save `overview-regression.png`. Pass when head 能看到要处理、可以更新和打开 Codex，并且记录引用了主干的标题。
- [ ] Lane 2. 读四个计数。Save `overview-counts.png`. Pass when 设备、在线、失败、版本未知可见。
- [ ] Lane 3. 读要处理表。Save `overview-attention.png`. Pass when 标题要处理和列机器、代理、说明、状态可见。
- [ ] Lane 4. 读可以更新表。Save `overview-updates.png`. Pass when 标题可以更新和列机器、变化可见。
- [ ] Lane 5. 点设备。Save `overview-jump-devices.png`. Pass when hash 是 `#/machines`。
- [ ] Lane 6. 回到 `#/` 再点打开 Codex。Save `overview-open-codex.png`. Pass when hash 进入代理页，锁头还在。
- [ ] Lane 7. 在总览打开帮助。Save `overview-help.png`. Pass when 未知可见，且没有带 `tone-ok` 的元素包含文字未知。
- [ ] Lane 8. 在总览收起导航。Save `overview-rail.png`. Pass when 展开菜单可见，总览标题还在。
- [ ] Lane 9. 若能强制接口不可达，就在那种状态下重载 `#/`。Save `overview-offline.png`. Pass when 四个计数标签和两个表题还在。若不能强制断开，记下这一点，并只在接口可用且标题仍在时通过。
- [ ] Lane 10. 若语言开关还在，就切到 `en` 再切回 `zh-CN`。Save `overview-locale.png`. Pass when 中文标题回来。若这份 PR 去掉了语言开关，记下这一点，并在屏幕上仍是中文标题时通过。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. 从打开页面到页眉文字出现的毫秒数。主干和 head 都量。
- [ ] Probe. 主干和 head 交替各打开一次 `#/`。从浏览器性能时间线读数。两边都要有毫秒数。
- [ ] Baseline. 先记下主干的毫秒数。
- [ ] Rule. head 比主干慢过 300 毫秒则失败。2000 毫秒还没有页眉也失败。这两个数是本计划设的预算。这次没有量过页面加载。

**Review gate.** 合并前由操作者审查。

- [ ] 把 lane 2 和 lane 3 的 screenshot 复制到媒体目录，文件名 `ui-overview-review-counts.png` 和 `ui-overview-review-attention.png`。
- [ ] 录一段 30 到 60 秒的 video，内容是 `#/`、两张表，以及跳到机器列表。存为同一媒体目录的 `ui-overview-review.mp4`。
- [ ] 把 screenshot 和 video 发到聊天里。停在可合并。等 operator 点头。

**Merge.**

- [ ] 根在这个确切的 head SHA 上给出干净结论。
- [ ] Bugbot 评论已分流。
- [ ] 结论之后变基到 ui-shell，patch-id 不变。
- [ ] 根停在已验证。ui-shell 落地之后，操作者落地 ui-overview。

## 显示设备列表 (ui-devices)

**Depends on.** ui-shell.

**Files.**

- [ ] 改 `web/src/routes/Machines.svelte`。
- [ ] 改 `web/src/lib/i18n/messages.ts`。
- [ ] 新建 `web/tests/devices.test.ts`。

**Build.**

- [ ] 在 `Machines.svelte` 画出定稿设备页。标题是设备。导语是「选择一台机器，查看已安装的代理和技能。」芯片是全部、在线、要处理、离线。列是名称、状态、系统、用户、软件、要处理。主按钮是接入机器。
- [ ] 页眉搜索在这一页过滤行。没有匹配时显示没有匹配的机器。

**You see.**

- [ ] `#/machines` 能看到标题、四枚芯片、表格和接入机器。
- [ ] 点芯片会改变可见的行。搜索框按输入的文字过滤。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `web/tests/devices.test.ts` 断言标题、四枚芯片、六列名、接入机器，以及查无结果时的没有匹配的机器。在 `web/` 运行 `pnpm exec vitest run tests/devices.test.ts`。在仓库根运行 `verification/ui-presentation/check.sh`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `grok-4.7-high` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在主干和 head 上打开 `#/machines`。Save `devices-regression.png`. Pass when head 能看到接入机器和四枚芯片，并且记录引用了主干标题。
- [ ] Lane 2. 读表格。Save `devices-table.png`. Pass when 名称、状态、系统、用户、软件、要处理可见。
- [ ] Lane 3. 点在线。Save `devices-online.png`. Pass when 在线是选中的芯片。
- [ ] Lane 4. 点要处理。Save `devices-attention.png`. Pass when 要处理是选中的芯片。
- [ ] Lane 5. 点离线。Save `devices-offline.png`. Pass when 离线是选中的芯片。
- [ ] Lane 6. 点全部。Save `devices-all.png`. Pass when 全部是选中的芯片。
- [ ] Lane 7. 输入一个匹配不到机器的字符串。Save `devices-empty.png`. Pass when 没有匹配的机器可见。
- [ ] Lane 8. 清空搜索。若有第一行就打开它。Save `devices-open-host.png`. Pass when hash 以 `#/machines/` 开头并且比 `#/machines` 更长。若接口没有机器，记下这一点，并在空表和接入机器仍在时通过。
- [ ] Lane 9. 点接入机器。Save `devices-enroll.png`. Pass when 接入对话框可见。空名字提交后，能看到先写一个称呼。
- [ ] Lane 10. 在这一页按 `/` 聚焦搜索。Save `devices-search.png`. Pass when 搜索框有焦点，并且输入一个字符会过滤表格。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. 从打开页面到页眉文字出现的毫秒数。主干和 head 都量。
- [ ] Probe. 主干和 head 交替各打开一次 `#/machines`。从浏览器性能时间线读数。两边都要有毫秒数。
- [ ] Baseline. 先记下主干的毫秒数。
- [ ] Rule. head 比主干慢过 300 毫秒则失败。2000 毫秒还没有页眉也失败。这两个数是本计划设的预算。这次没有量过页面加载。

**Review gate.** 合并前由操作者审查。

- [ ] 把 lane 2 和 lane 9 的 screenshot 复制到媒体目录，文件名 `ui-devices-review-table.png` 和 `ui-devices-review-enroll.png`。
- [ ] 录一段 30 到 60 秒的 video，内容是芯片、空搜索，以及空名字的接入警告。存为同一目录的 `ui-devices-review.mp4`。
- [ ] 把 screenshot 和 video 发到聊天里。停在可合并。等 operator 点头。

**Merge.**

- [ ] 根在这个确切的 head SHA 上给出干净结论。
- [ ] Bugbot 评论已分流。
- [ ] 结论之后变基到 ui-shell，patch-id 不变。
- [ ] 根停在已验证。ui-shell 落地之后，操作者落地 ui-devices。

## 显示一台机器 (ui-host)

**Depends on.** ui-shell.

**Files.**

- [ ] 改 `web/src/routes/MachineDetail.svelte`。
- [ ] 改 `web/src/lib/i18n/messages.ts`。
- [ ] 新建 `web/tests/host.test.ts`。

**Build.**

- [ ] 在 `MachineDetail.svelte` 画出定稿的单机页。面包屑从设备开始。元信息行显示在线或离线、操作系统、架构和用户。两节是代理和技能。这台机器没有技能时显示这台机器上还没有技能。
- [ ] 失败或未安装、没有版本时显示无。未知版本保留上次看到，不用成功色。

**You see.**

- [ ] `#/machines/<name>` 能看到面包屑、元信息、代理和技能。
- [ ] 这一页上的未知不在带 `tone-ok` 的元素里。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `web/tests/host.test.ts` 断言面包屑设备、标题代理和技能、没有技能时的那句，以及夹具里的未知状态不在 `tone-ok` 元素里。在 `web/` 运行 `pnpm exec vitest run tests/host.test.ts`。在仓库根运行 `verification/ui-presentation/check.sh`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `grok-4.7-high` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在主干和 head 上打开同一台机器的 hash。Save `host-regression.png`. Pass when head 能看到代理和技能，并且记录引用了主干标题。
- [ ] Lane 2. 读面包屑。Save `host-crumb.png`. Pass when 设备是回到 `#/machines` 的链接。
- [ ] Lane 3. 读元信息。Save `host-meta.png`. Pass when 在线或离线可见，并且有操作系统字符串。
- [ ] Lane 4. 读代理表。Save `host-agents.png`. Pass when 列名称、版本、状态、说明可见。
- [ ] Lane 5. 读技能区。Save `host-skills.png`. Pass when 技能可见。若没有技能，则这台机器上还没有技能可见。
- [ ] Lane 6. 点面包屑里的设备。Save `host-back.png`. Pass when hash 是 `#/machines`。
- [ ] Lane 7. 若接口有离线机器，就打开它。Save `host-offline.png`. Pass when 离线可见。若没有，记下这一点，并在在线机器仍显示在线时通过。
- [ ] Lane 8. 找一个版本格。Save `host-version.png`. Pass when 失败或未安装显示无，或者记录写明接口没有这样的行。
- [ ] Lane 9. 若有一行是未知，就找到文字未知。Save `host-unknown.png`. Pass when 这段文字不在 `tone-ok` 里。若没有未知行，记下这一点。空页面不算通过。这条记录保持阻塞。
- [ ] Lane 10. 在这一页收起导航。Save `host-rail.png`. Pass when 展开菜单可见，机器名还在。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. 从打开页面到页眉文字出现的毫秒数。主干和 head 都量。
- [ ] Probe. 主干和 head 交替各打开一次同一个 `#/machines/<name>`。从浏览器性能时间线读数。两边都要有毫秒数。
- [ ] Baseline. 先记下主干的毫秒数。
- [ ] Rule. head 比主干慢过 300 毫秒则失败。2000 毫秒还没有页眉也失败。这两个数是本计划设的预算。这次没有量过页面加载。

**Review gate.** 合并前由操作者审查。

- [ ] 把 lane 4 和 lane 5 的 screenshot 复制到媒体目录，文件名 `ui-host-review-agents.png` 和 `ui-host-review-skills.png`。
- [ ] 录一段 30 到 60 秒的 video，内容是面包屑、代理表和技能区。存为同一目录的 `ui-host-review.mp4`。
- [ ] 把 screenshot 和 video 发到聊天里。停在可合并。等 operator 点头。

**Merge.**

- [ ] 根在这个确切的 head SHA 上给出干净结论。
- [ ] Bugbot 评论已分流。
- [ ] 结论之后变基到 ui-shell，patch-id 不变。
- [ ] 根停在已验证。ui-shell 落地之后，操作者落地 ui-host。

## 显示应用列表 (ui-apps)

**Depends on.** ui-shell.

**Files.**

- [ ] 改 `web/src/routes/Apps.svelte`。
- [ ] 改 `web/src/lib/i18n/messages.ts`。
- [ ] 新建 `web/tests/apps.test.ts`。

**Build.**

- [ ] 用定稿的应用表替换 `Apps.svelte` 里的占位。标题是应用。导语是「要安装到机器上的代理。」列是名称、可用版本、覆盖、状态。
- [ ] 点一行打开该代理的路由。不要改 `upstreamRowView`。

**You see.**

- [ ] `#/apps` 能看到标题、导语和四列。
- [ ] 点一行离开 `#/apps` 进入代理 hash。锁头还在。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `web/tests/apps.test.ts` 断言标题应用、导语和四列名。在 `web/` 运行 `pnpm exec vitest run tests/apps.test.ts`。在仓库根运行 `verification/ui-presentation/check.sh`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `grok-4.7-high` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在主干和 head 上打开 `#/apps`。Save `apps-regression.png`. Pass when head 能看到「要安装到机器上的代理。」并且记录写明主干没有这个标题。
- [ ] Lane 2. 读表格。Save `apps-table.png`. Pass when 名称、可用版本、覆盖、状态可见。
- [ ] Lane 3. 若有第一行就点它。Save `apps-open-agent.png`. Pass when hash 变了且锁头还在。若目录是空的，记下这一点，并在标题和列还在时通过。
- [ ] Lane 4. 用浏览器后退。Save `apps-back.png`. Pass when hash 是 `#/apps`。
- [ ] Lane 5. 在这一页打开帮助。Save `apps-help.png`. Pass when 未知可见，且不在 `tone-ok` 里。
- [ ] Lane 6. 收起导航。Save `apps-rail.png`. Pass when 展开菜单可见，应用还在。
- [ ] Lane 7. 若能强制接口不可达，就重载。Save `apps-offline.png`. Pass when 标题和四列还在。
- [ ] Lane 8. 核对失败查询的可用版本没有画成成功行的可用版本。Save `apps-version.png`. Pass when 失败来源不显示成功时的可用版本行。若目录里没有失败来源，记下这一点，这条记录保持阻塞。
- [ ] Lane 9. 用 hash 打开 `#/profiles`。Save `apps-profiles-url.png`. Pass when 旧的配置档路由仍能画出来，新导航里没有配置档。
- [ ] Lane 10. 用 hash 打开 `#/ssh-inventory`。Save `apps-ssh-url.png`. Pass when 该路由仍能画出来，新导航里没有 SSH 清单。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. 从打开页面到页眉文字出现的毫秒数。主干和 head 都量。再量 head 上标题「应用」出现的毫秒数。主干没有这一页。
- [ ] Probe. 交替打开。主干没有 `#/apps` 时，改量主干 `#/` 的页眉时间，并记下缺页。head 量到标题应用出现。两边都要有毫秒数。
- [ ] Baseline. 先记下主干页眉的毫秒数。
- [ ] Rule. 不要把主干没有的页面和 head 的页面做成比值。页眉比主干页眉慢过 300 毫秒则失败。2000 毫秒还没有应用也失败。这两个数是本计划设的预算。

**Review gate.** 合并前由操作者审查。

- [ ] 把 lane 2 和 lane 3 的 screenshot 复制到媒体目录，文件名 `ui-apps-review-table.png` 和 `ui-apps-review-open.png`。
- [ ] 录一段 30 到 60 秒的 video，内容是表格和一次点行。存为同一目录的 `ui-apps-review.mp4`。
- [ ] 把 screenshot 和 video 发到聊天里。停在可合并。等 operator 点头。

**Merge.**

- [ ] 根在这个确切的 head SHA 上给出干净结论。
- [ ] Bugbot 评论已分流。
- [ ] 结论之后变基到 ui-shell，patch-id 不变。
- [ ] 根停在已验证。ui-shell 落地之后，操作者落地 ui-apps。

## 显示一个代理 (ui-agent)

**Depends on.** ui-shell.

**Files.**

- [ ] 改 `web/src/routes/Lifecycle.svelte`。
- [ ] 改 `web/src/lib/i18n/messages.ts`。
- [ ] 新建 `web/tests/agent.test.ts`。

**Build.**

- [ ] 在 `Lifecycle.svelte` 按定稿排布代理页。面包屑从应用开始。动作是检查更新、安装或升级到加上固定版本、卸载。没有安装时卸载保持禁用。四个计数是成功、失败、未执行、未知。表题是每台的结果。
- [ ] 照原样调用 `lifecycleStateTone`、`operationProgress`、`upstreamRowView`、`failedResults`。不要改它们所在的文件。已经是这个固定版本的机器不能再选。预览不执行安装。

**You see.**

- [ ] 代理路由能看到四个计数和三个动作。
- [ ] 未知不在 `tone-ok` 里。失败的上游行不显示成功时的可用版本行。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `web/tests/agent.test.ts` 断言检查更新、卸载、四个计数标签和每台的结果。夹具没有安装时卸载为禁用。未知夹具行不在 `tone-ok` 里。在 `web/` 运行 `pnpm exec vitest run tests/agent.test.ts tests/ui-presentation.test.ts`。在仓库根运行 `verification/ui-presentation/check.sh`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `grok-4.7-high` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在主干和 head 上打开 `#/lifecycle`。Save `agent-regression.png`. Pass when head 能看到检查更新和每台的结果，并且记录引用了主干标题。
- [ ] Lane 2. 读动作。Save `agent-actions.png`. Pass when 检查更新和卸载可见，升级按钮文字以安装或升级到开头。
- [ ] Lane 3. 读四个计数。Save `agent-counts.png`. Pass when 成功、失败、未执行、未知可见，且未知不在 `tone-ok` 里。
- [ ] Lane 4. 读每台的结果。Save `agent-results.png`. Pass when 这个标题可见。
- [ ] Lane 5. 点检查更新。Save `agent-check.png`. Pass when 页面没有把失败的上游行换成成功时的可用版本行。
- [ ] Lane 6. 打开安装或升级。Save `agent-upgrade.png`. Pass when 已经是固定版本的机器不能选，并且对话框写着已是这个版本的机器不能再选。
- [ ] Lane 7. 一台都不选。Save `agent-preview-disabled.png`. Pass when 预览按钮禁用。
- [ ] Lane 8. 选一台可以升级的机器并打开预览。Save `agent-preview.png`. Pass when 能看到所选的那一行，并且没有发出安装请求。返回后，对话框回到选择列表。
- [ ] Lane 9. 若当前什么都没装，就看卸载。Save `agent-uninstall-disabled.png`. Pass when 卸载禁用。若已经装了，打开卸载后对话框可见，确认它不会拆掉外壳。
- [ ] Lane 10. 点面包屑里的应用。Save `agent-back.png`. Pass when 分支上已有 ui-apps 时 hash 是 `#/apps`。若这条分支没有 ui-apps，则面包屑文字应用可见即通过。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. 从打开页面到页眉文字出现的毫秒数。主干和 head 都量页眉。再量 head 上检查更新出现的毫秒数。主干已有生命周期页，所以两边都量页眉。多出来的 head 时间是这份差异加上的工作。
- [ ] Probe. 主干和 head 交替各打开一次 `#/lifecycle`。从浏览器性能时间线读两个时间。
- [ ] Baseline. 先记下主干页眉的毫秒数。
- [ ] Rule. 页眉比主干页眉慢过 300 毫秒则失败。2000 毫秒还没有检查更新也失败。这两个数是本计划设的预算。

**Review gate.** 合并前由操作者审查。

- [ ] 把 lane 3 和 lane 6 的 screenshot 复制到媒体目录，文件名 `ui-agent-review-counts.png` 和 `ui-agent-review-upgrade.png`。
- [ ] 录一段 30 到 60 秒的 video，内容是四个计数、禁用的预览，以及一次打开预览再返回。存为同一目录的 `ui-agent-review.mp4`。
- [ ] 把 screenshot 和 video 发到聊天里。停在可合并。等 operator 点头。

**Merge.**

- [ ] 根在这个确切的 head SHA 上给出干净结论。
- [ ] Bugbot 评论已分流。
- [ ] 结论之后变基到 ui-shell，patch-id 不变。
- [ ] 根停在已验证。ui-shell 落地之后，操作者落地 ui-agent。

## 显示技能 (ui-skills)

**Depends on.** ui-shell.

**Files.**

- [ ] 改 `web/src/routes/Skills.svelte`。
- [ ] 改 `web/src/lib/i18n/messages.ts`。
- [ ] 新建 `web/tests/skills-page.test.ts`。

**Build.**

- [ ] 在 `Skills.svelte` 画出定稿技能页。标题是技能。导语是「技能装在某台机器的某个代理上。」主按钮是添加来源。已是当前版本的技能显示已是当前。有更新的技能显示可升到和升级技能。
- [ ] 添加来源对话框的标题是添加来源。空名字显示先写来源的名字。升级技能对话框有关闭，并且不执行安装。

**You see.**

- [ ] `#/skills` 能看到标题、导语和添加来源。
- [ ] 已是当前和可升到不是同一句。有可升到的技能不用已是当前来标。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `web/tests/skills-page.test.ts` 断言标题、导语、添加来源、当前夹具的已是当前，以及落后夹具的可升到。在 `web/` 运行 `pnpm exec vitest run tests/skills-page.test.ts`。在仓库根运行 `verification/ui-presentation/check.sh`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `grok-4.7-high` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在主干和 head 上打开 `#/skills`。Save `skills-regression.png`. Pass when head 能看到添加来源，并且记录引用了主干标题。
- [ ] Lane 2. 读页头。Save `skills-head.png`. Pass when 技能和「技能装在某台机器的某个代理上。」可见。
- [ ] Lane 3. 若有已是当前的技能，就读它。Save `skills-current.png`. Pass when 已是当前可见，且该行没有可升到。
- [ ] Lane 4. 若有可更新的技能，就读它。Save `skills-update.png`. Pass when 可升到可见，且该行不是只标已是当前。若没有这样的行，记下这一点，这条记录保持阻塞。
- [ ] Lane 5. 点添加来源。Save `skills-source.png`. Pass when 对话框标题是添加来源。
- [ ] Lane 6. 提交空的来源名。Save `skills-source-empty.png`. Pass when 先写来源的名字可见。
- [ ] Lane 7. 关闭对话框。Save `skills-source-closed.png`. Pass when 标题技能可见，对话框消失。
- [ ] Lane 8. 在有升级技能的一行上打开它。Save `skills-upgrade.png`. Pass when 关闭可见，并且确认不会发出安装请求。若没有这样的行，记下这一点，这条记录保持阻塞。
- [ ] Lane 9. 收起导航。Save `skills-rail.png`. Pass when 展开菜单可见，技能还在。
- [ ] Lane 10. 用 hash 打开 `#/deployments`。Save `skills-deployments-url.png`. Pass when 该路由仍能画出来，导航里没有发布。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. 从打开页面到页眉文字出现的毫秒数。主干和 head 都量。
- [ ] Probe. 主干和 head 交替各打开一次 `#/skills`。从浏览器性能时间线读数。两边都要有毫秒数。
- [ ] Baseline. 先记下主干的毫秒数。
- [ ] Rule. head 比主干慢过 300 毫秒则失败。2000 毫秒还没有页眉也失败。这两个数是本计划设的预算。这次没有量过页面加载。

**Review gate.** 合并前由操作者审查。

- [ ] 把 lane 2 和 lane 5 的 screenshot 复制到媒体目录，文件名 `ui-skills-review-head.png` 和 `ui-skills-review-source.png`。
- [ ] 录一段 30 到 60 秒的 video，内容是列表、空来源警告，以及有升级行时的一次升级对话框。存为同一目录的 `ui-skills-review.mp4`。
- [ ] 把 screenshot 和 video 发到聊天里。停在可合并。等 operator 点头。

**Merge.**

- [ ] 根在这个确切的 head SHA 上给出干净结论。
- [ ] Bugbot 评论已分流。
- [ ] 结论之后变基到 ui-shell，patch-id 不变。
- [ ] 根停在已验证。ui-shell 落地之后，操作者落地 ui-skills。

## Close the program

- [ ] 上面每一格都有证据才打勾。
- [ ] 按执行手册回复操作者。列出每份 PR 的结论。不要声称已经合并。

## Appendix A. Prototype evidence

视觉问题在这份计划之前已经定了。没有再做新原型。定稿文件是 `.scratch/ui-directions/index.html`，50430 字节。`git check-ignore -v` 对这个路径报告 `.scratch/.gitignore:1:*`。这个路径上 `git log -1` 没有输出。没有分支，也没有 SHA。写计划时的主干是 `5ef6242`。

采用的截图由 `ls` 量过，都在 `.scratch/ui-directions/shots/`。`cloud-home.png` 95752 字节，`cloud-rail.png` 88931 字节，`cloud-devices.png` 91802 字节，`cloud-host.png` 71581 字节，`cloud-codex.png` 108009 字节，`cloud-scope.png` 102246 字节，`cloud-notify.png` 104802 字节。`agent-*`、`app-*`、`asset-*`、`retro-*` 是操作者已经放弃的方向。

页面名和对话框名来自对这个文件的阅读，以及 [Map locked UI prototype](6dad05c4-5697-4761-ad6d-ae455da68212) 的清单。计划里的字符串随后又从同一文件读过。

有两处选择没有运行能替操作者做。默认写在上面的 PR 格子里。开始之前可以用自己的话改掉。

定稿页眉没有管理令牌框，也没有语言开关。默认是新页眉里这两样都不要。若要留，就说留令牌框、留语言开关，或两样都留。

定稿导航没有配置档、发布、SSH 清单。默认是这些路由留在原来的 hash 上，不进导航。若要把其中某一个放回导航，就直接说。

控制面若没有一条未知结果，现场就看不到那一行。这时 ui-host 的第 9 条保持阻塞。单测仍然必须通过。这个缺口不算通过。

## Appendix B. Alternatives rejected

整站做成一份 PR 被否掉。代理页上的错误会压在一个还没证明的外壳上，审查视频会把七处改动混在一起。

把原型的 hash 记号抄进 `web/src/lib/state/router.svelte.ts` 被否掉。生产环境已经用 `#/machines` 和 `#/lifecycle` 这样的 hash。画面不依赖原型里的 `home` 和 `hosts`。

改 `lifecycle.ts` 去凑原型里的样例机器名被否掉。那些名字是样例。展示函数已经有一次通过的校验。页面调用它们，不重写它们。

把旧导航项放回定稿的三组旁边被否掉。操作者锁定的导航就是这三组。多出来的路由留在原地址上，书签仍能打开。后来的文件调查把 `Profiles.svelte`、`Deployments.svelte`、`SshInventory.svelte` 列成后续页面 PR。这三份不在本程序里。定稿原型没有它们的画面。

## Appendix C. Risks

这个检出没有安装 `control-ui`。每条现场记录改用 `cursor-ide-browser`。所有者要留意记录点到了编辑器而不是页面。锁不住浏览器标签就停下并报告阻塞。

这个仓库的 `origin/main` 上没有 `pstack/`。`git show origin/main:pstack/...` 失败时，到插件缓存读同一文件，路径是 `/Users/shelwin/.cursor/plugins/cache/cursor-public/pstack/ae5650febf4320793844c12c4adaa937f417f8da/skills/`。不要跳过阅读。

ui-shell 之后的每份 PR 都会往 `web/src/lib/i18n/messages.ts` 追加键。变基时两边的追加都留着。谁删了另一份 PR 的键，审查就不通过。

写计划时，展示校验还是未跟踪文件。虚拟机看不到 `verification/ui-presentation/check.sh` 就不能通过单测格。启动步骤会把程序停到这些文件出现在拉取的分支上。

这台机器上的 `pnpm` 可能是一个跑不起来的空壳。启动步骤要用能读 `web/pnpm-lock.yaml` 的 pnpm 10。Vitest 必须是 5.0.1，否则校验脚本以 2 退出。退出码 2 是工具问题，不是产品问题。

开发服务器把 `/api` 代理到 `127.0.0.1:7788`。现场记录核对结构，不核对原型里的机器名。标成阻塞的记录不算通过。

ui-agent 和 ui-apps 都在告诉操作者未知是什么意思，位置不同。ui-agent 不能为了截图好看去改 `StateBadge`。文字未知落在 `tone-ok` 上，这条记录失败。

`web/tests/ui-presentation.test.ts` 检查的是类名 `tone-warn` 和 `tone-ok`。它不读算出来的颜色。上色的是 `web/src/app.css` 第 150 到 164 行。外壳若把 `.tone-ok` 改成警告色，校验仍会通过，未知看起来却像成功。ui-shell 不动这一段。`docs/web-ui.md` 写的开发地址是 `http://localhost:5173`。启动步骤固定用 4173，避免接到已经占着 5173 的服务。

## Appendix D. Links and reading list

每份 PR 之前读 `.cursor/skills/verify-ui-presentation/SKILL.md`。记录里点名的字符串到 `.scratch/ui-directions/index.html` 核对。做 ui-shell 之前读 `web/src/App.svelte` 和 `web/src/lib/state/router.svelte.ts`。

ui-shell 和 ui-agent 写代码之前读 `pstack/skills/how/SKILL.md`。ui-shell 跨过页眉、导航和路由出口。ui-agent 坐在生命周期页上，不能改展示函数。没有 PR 读 `pstack/skills/interrogate/SKILL.md`。设计已经锁定，不再重开布局争论。

决策记录按 `pstack/skills/show-me-your-work/SKILL.md`。根把它放在这个 agent store。这次会话的现场记录用 `grok-4.7-high`，因为本会话的模型列表里没有 `grok-4.7-xhigh-fast`。

审查文件的媒体目录是 `/Users/shelwin/Library/Application Support/Cursor/AgentStores/cursor_agent_stores/779c368a-b954-43ef-8a88-dc4caf1e264a/files/docs/media`。
