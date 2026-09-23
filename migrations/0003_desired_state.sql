-- 0003_desired_state.sql — 期望状态、drift 与操作状态机所需表
-- （架构 v1.1.2 §6.3/§10.1/§4.3；KM-23 第 3 片）。

-- 期望快照（§6.3/ADR-2 单表按代唯一）：generation 是身份、digest 是内容指纹。
-- (machine_id, generation) 唯一；digest 只建普通索引——同一内容可在多代出现
-- （A→B→A 时 genN 与 genN+2 同 digest，两行内容相同），回滚到第 N 代是一次主键查找。
CREATE TABLE IF NOT EXISTS desired_snapshots (
    machine_id    TEXT NOT NULL,             -- machines.name
    generation    INTEGER NOT NULL,
    digest        TEXT NOT NULL,             -- "sha256:<hex>"（snapshotDigest，仅内容寻址）
    canonicalization_version TEXT NOT NULL DEFAULT '',
    snapshot      TEXT NOT NULL,             -- 自包含快照 JSON（FR-13.6）
    created_at    TEXT NOT NULL,
    PRIMARY KEY (machine_id, generation)
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_desired_snapshots_machine_gen
    ON desired_snapshots(machine_id, generation);
CREATE INDEX IF NOT EXISTS ix_desired_snapshots_machine_digest
    ON desired_snapshots(machine_id, digest);

-- Operation 状态机扩展（§6.4/FR-15.5）：只读标记、planDigest、终态修饰、
-- verify 证据与确认窗口。互斥仍由 0001 的 ux_operations_machine_unresolved
-- 部分唯一索引承载（谓词含 AwaitingConfirmation）。
ALTER TABLE operations ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE operations ADD COLUMN plan_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN terminal_modifier TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN verify TEXT NOT NULL DEFAULT '';          -- VerifyEvidence JSON
ALTER TABLE operations ADD COLUMN plan_expires_at TEXT;                     -- 确认窗口截止（RFC3339）

-- 操作步骤审计（§10.1 operation_steps：steps 含限长脱敏输出）。
CREATE TABLE IF NOT EXISTS operation_steps (
    op_id       TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    name        TEXT NOT NULL,
    phase       TEXT NOT NULL DEFAULT '',
    output      TEXT NOT NULL DEFAULT '',
    recorded_at TEXT NOT NULL,
    PRIMARY KEY (op_id, seq)
);

-- Deployment 目标推进（§10.1 deployment_targets：每机 phase/result/operation_id
-- 与 superseded/skipped 原因；effective_generation 承载回滚型发布"每机物化新代"）。
CREATE TABLE IF NOT EXISTS deployment_targets (
    deployment_id  TEXT NOT NULL,
    machine_id     TEXT NOT NULL,
    phase          TEXT NOT NULL DEFAULT 'Pending',
    reason         TEXT NOT NULL DEFAULT '',
    operation_id   TEXT NOT NULL DEFAULT '',
    effective_generation INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT NOT NULL,
    PRIMARY KEY (deployment_id, machine_id)
);

-- observed_states 增加因果标注列（§10.1：含关联 operationId 与归属代）。
ALTER TABLE observed_states ADD COLUMN observed_generation INTEGER NOT NULL DEFAULT 0;
