# 文档索引

本目录收录 KM-11 及关联任务已交付的 Markdown 文档。首次入库保留了原附件；2026-09-20 按用户截图同步修订需求与当前架构的 Agent 支持范围；2026-09-21 完成独立复核结论的闭合修订（v1.1.2）。历史版本和评审原文保留。本索引区分当前版本、评审依据和历史版本。文档版本不是软件发布版本。

## 当前阅读顺序

1. [MVP spec v0.1 + Agent 范围补充 + 前端栈修订记录](agent-fleet-mvp-spec.md)：需求基准。前端栈一处（§24）按用户 2026-09-20 决定回写并附修订记录，其余不动。
2. [中文架构 v1.1.2](agent-fleet-architecture-v1.1.2.md)：**当前版本**。在 v1.1.1 基础上闭合 KM-17 独立复核全部发现（P-1～P-15），落地 Svelte + shadcn-svelte 前端栈（含用户已授权的显式 FR 例外）与用户 2026-09-21 的两项取舍、八家族分两批决定。含 §1.7 全量变更清单与附录 B 触及位置索引。
3. [人工 CA 备份恢复手册](manual-ca-backup-recovery.md)：人工备份、恢复和演练方案。
4. [v1.1 评审问题处置与文档验证](architecture-revision-resolution.md)：两轮评审的修订落点；**§9 为 v1.1.2 追加的四处失实声明更正**。
5. [v1.1.1 决策补充表](architecture-revision-resolution-v1.1.1-supplement.md)：记录 CA 备份决策落地及待验证事项。
6. [v1.1.2 复核闭合处置表](architecture-revision-resolution-v1.1.2-supplement.md)：KM-17 发现 P-1～P-15 的逐项处置、用户决策清单、前端栈变更与 FR 例外登记、文档检查记录、待确认与未验证清单。
7. [Agent 支持矩阵与验收要求](agent-support-matrix.md)：八个支持目标的身份与能力边界；本版补充**批次一 / 批次二**的接入顺序。
8. [批次一适配器：接入前确认、逐家族验收与范围决策](adapters-batch-1.md)：KM-24 交付记录——Codex / OMP / OpenCode 的接入前确认与验收 5 项、ZCode 范围决策、未验证清单、契约缺口。
9. [批次二适配器：四家族接入前确认与切片规划](adapters-batch-2.md)：KM-32 交付记录（本片不改产品代码）——Claude / DeepSeek Harness / Grok / Hermes 的接入前确认 7 项与逐家族证据、受管字段与所有权边界、接入可行性结论（可接入 / 需用户裁决）、切片结构与建议顺序、未验证清单、契约缺口。
10. [Web UI：路由、状态呈现与契约缺口](web-ui.md)：KM-25 交付记录——`web/` 工程（Svelte + TypeScript + Vite + shadcn-svelte，FR-14.4）、SSE 事件枢纽（`GET /api/v1/events`，FR-14.1/14.2）、FR-14.5 四组状态呈现的落点、本片发现的后端契约缺口与未验证清单。

统计断言脚本：[`tools/verify-architecture-stats.py`](tools/verify-architecture-stats.py)（文档级，无外部依赖）：校验 FR 计数（95 条 / P0 94 / P1 1）、§2.8 每行 15 列合计与逐列相加、§18 与附录声明一致、以及"96/70/+26"无现行残留。运行方式：`python3 docs/tools/verify-architecture-stats.py docs/agent-fleet-architecture-v1.1.2.md`。

### 八家族接入路线（用户 2026-09-21 决定）

| 批次 | 家族 | 说明 |
|---|---|---|
| **批次一（MVP 范围）** | Codex、OMP、OpenCode、ZCode | 三个已有适配契约的家族加一个新家族，先验证适配器抽象站得住 |
| **批次二（批次一打通后接入）** | Claude、DeepSeek Harness、Grok、Hermes | 运行时要先实测确认，**不能按同名模型推定**；未确认不得进入实现 |

批次归属只表示范围与接入顺序，**不改变任何家族的验收要求**，也**不改动 FR 计数与统计口径**（95 条，P0 94 / P1 1）。

## 评审依据

- [第一性原理评审原文](first-principles-review.md)
- [KM-12 对抗式评审报告](KM-12-对抗式评审报告.md)
- [两轮评审汇总](agent-fleet-final-summary.md)：针对 v1.0 的汇总与修订要求，不是 v1.1.2 已验收的证明。
- KM-17 独立复核报告（`KM-17-独立复核报告.md`，KM-17 交付评论附件 `01a0bee0-e898-7e46-97ce-03acf66dc17e`）：针对 v1.1.1 的独立复核，发现 P-1～P-15；本版处置见第 6 项。

## 历史版本

- [架构 v1.0](agent-fleet-architecture-design.md)
- [架构 v1.1](agent-fleet-architecture-v1.1.md)
- [架构 v1.1.1](agent-fleet-architecture-v1.1.1.md)：CA 备份决策版；原件保留，作为 v1.1.2 的前序对照基准。

## 决策与验证状态

**用户已确认的决定**（均可追溯到评论）：

1. **MVP 不做内置 CA 备份导出，补齐人工备份恢复方案**（2026-09-20，纳入 v1.1.1）。不做内置导出不等于不备份。
2. **前端技术栈 = Svelte + TypeScript + Vite + shadcn-svelte**（2026-09-20，KM-15 评论 `01a0bec5-3837-75bc-b322-1a6140fd8a83`）；FR-14.4 的改写按"**用户已授权的显式 FR 例外**"登记（2026-09-21，KM-19 评论 `01a0c3e9-bec5-716d-93e4-46c8ddf897c3`）。
3. **两项取舍接受并带边界**（2026-09-21，同上评论）：(a) MVP 不做证书吊销，但**删除机器即拒绝其证书**，不引入完整 PKI/HSM/CRL/OCSP，UI 标注「当前限制：无证书吊销」；(b) Unknown 目标允许人工跳过，但**必须留审计记录并在 UI 显式标出**，不允许静默跳过，且不计入成功。
4. **八家族分两批**（2026-09-21，同上评论）：批次一 Codex/OMP/OpenCode/ZCode；批次二 Claude/DeepSeek Harness/Grok/Hermes。

**验证状态**：**独立复核结论（KM-17 的 P-1～P-15）已落地**（逐项处置见第 6 项；FR 计数与统计断言由脚本校验通过）；**最终验收待用户确认**。已有检查**全部为文档级**：未执行产品测试（T1–T22 及 v1.1.2 的 T2/T10/T16 扩展）、未做容量压测（H1–H3）、未执行真实备份恢复或 CA 演练（DR-1～DR-8）、未部署、未写产品代码。手册与文档中"待实现验证"的步骤不能当作已运行成功的命令。

来源分工：原架构由贾维斯交付；第一性原理评审由资深架构专家完成；对抗式评审由资深后端工程师完成；v1.1 由资深架构专家起草、后端工程师接续；v1.1.1 与人工备份手册由文档专员交付；v1.1.2 复核闭合与用户裁决落地由文档专员交付（KM-18）；KM-17 独立复核由部署专员完成；首席调度官负责汇总、入库与收口。
