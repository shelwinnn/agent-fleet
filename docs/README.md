# 文档索引

本目录收录 KM-11 及关联任务已交付的 Markdown 文档。首次入库保留了原附件；2026-09-20 按用户截图同步修订需求与当前架构的 Agent 支持范围，历史版本和评审原文保留。本索引区分当前版本、评审依据和历史版本。文档版本不是软件发布版本。

## 当前阅读顺序

1. [MVP spec v0.1 + Agent 范围补充](agent-fleet-mvp-spec.md)：需求基准。
2. [中文架构 v1.1.1](agent-fleet-architecture-v1.1.1.md)：最新完整架构，含用户确认的 CA 备份决定。
3. [人工 CA 备份恢复手册](manual-ca-backup-recovery.md)：人工备份、恢复和演练方案。
4. [v1.1 评审问题处置与文档验证](architecture-revision-resolution.md)：两轮评审的修订落点。
5. [v1.1.1 决策补充表](architecture-revision-resolution-v1.1.1-supplement.md)：与上一份配套，记录 CA 决策落地及待验证事项。

[Agent 支持矩阵与验收要求](agent-support-matrix.md)：截图七种 Agent 加原有 OpenCode，共八个支持目标；包含待验证身份与能力边界。

## 评审依据

- [第一性原理评审原文](first-principles-review.md)
- [KM-12 对抗式评审报告](KM-12-对抗式评审报告.md)
- [两轮评审汇总](agent-fleet-final-summary.md)：针对 v1.0 的汇总与修订要求，不是 v1.1.1 已验收的证明。

## 历史版本

- [架构 v1.0](agent-fleet-architecture-design.md)
- [架构 v1.1](agent-fleet-architecture-v1.1.md)

## 决策与验证状态

用户已确认：**MVP 不做内置 CA 备份导出，补齐人工备份恢复方案。** 不做内置导出不等于不备份。该决定已经纳入 v1.1.1；历史文档中的旧口径仅供追溯。

当前文档已交付，但整份架构的独立复核和最终验收尚未完成。已有检查为文档级；未执行产品测试、容量压测、真实备份恢复或演练。手册中待实现验证的步骤不能当作已运行成功的命令。

来源分工：原架构由贾维斯交付；第一性原理评审由资深架构专家完成；对抗式评审由资深后端工程师完成。v1.1 由资深架构专家起草、后端工程师接续；v1.1.1 与人工备份手册由文档专员交付；首席调度官负责汇总与本次入库。
