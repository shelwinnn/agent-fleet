-- 0005_lifecycle.sql — 生命周期操作模型（KM-47）：显式三态意图文档与逐目标
-- 幂等操作（依据 docs/superpowers/specs/2026-10-27-codex-lifecycle-spec.md）。
-- 与配置期望快照（0003 desired_snapshots）独立演进：生命周期只动程序本体，
-- 不触发现有配置/MCP/rules/skill 收敛。

-- 生命周期意图文档（按代不可变，revision 是身份、digest 是内容指纹；
-- 与 desired_snapshots 同一代次模式）。"未纳管" = 无任何代，可显式查询，
-- 不是 absent——省略绝不构成隐式卸载。
CREATE TABLE IF NOT EXISTS lifecycle_desired (
    machine_id  TEXT NOT NULL,             -- machines.name
    revision    INTEGER NOT NULL,
    digest      TEXT NOT NULL,             -- "sha256:<hex>"（意图条目规范化 JSON）
    entries     TEXT NOT NULL,             -- 归一化意图条目 JSON（agent → 三态条目）
    created_at  TEXT NOT NULL,
    PRIMARY KEY (machine_id, revision)
);

CREATE INDEX IF NOT EXISTS ix_lifecycle_desired_machine_rev
    ON lifecycle_desired(machine_id, revision DESC);

-- 生命周期操作（install/upgrade/uninstall/check-update）：幂等键唯一索引在
-- 库层承载去重——并发重复提交同一请求只有一个 INSERT 成功，其余读到同一行。
CREATE TABLE IF NOT EXISTS lifecycle_operations (
    id              TEXT PRIMARY KEY,      -- 操作 id（= metadata.name）
    idempotency_key TEXT NOT NULL,
    request_digest  TEXT NOT NULL,         -- 幂等键复用检测：同键不同内容 = 客户端缺陷
    op_type         TEXT NOT NULL,         -- Install | Upgrade | Uninstall | CheckUpdate
    version         TEXT NOT NULL DEFAULT '', -- Install/Upgrade：创建时固定的精确版本
    retry_of        TEXT NOT NULL DEFAULT '', -- 重试关联的原操作 id（追溯链）
    created_by      TEXT NOT NULL DEFAULT '',
    phase           TEXT NOT NULL DEFAULT 'Pending', -- Pending | Running | Completed
    needs_reconfirm INTEGER NOT NULL DEFAULT 0,       -- 重发现影响变化：需重新确认
    reconfirm_reason TEXT NOT NULL DEFAULT '',
    confirmed_at    TEXT,                  -- 操作者确认时刻（重试不重复确认；影响变化后必须重新确认）
    started_at      TEXT,                  -- 首个目标进入 running 的时刻
    completed_at    TEXT,                  -- 全部目标落定（结果态）的时刻
    resource_version INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_lifecycle_operations_idem
    ON lifecycle_operations(idempotency_key);
CREATE INDEX IF NOT EXISTS ix_lifecycle_operations_retry
    ON lifecycle_operations(retry_of);

-- 逐目标状态与证据（spec：逐目标持久化状态和证据，至少四态各带原因）。
-- 目标身份 (machine,env,user,agent,method,package)；os/arch 与能力判定是创建时
-- 快照（审计"当时的矩阵判定"），不参与身份。
CREATE TABLE IF NOT EXISTS lifecycle_operation_targets (
    op_id       TEXT NOT NULL,
    target_key  TEXT NOT NULL,             -- \x1f 分隔的目标身份键
    machine_id  TEXT NOT NULL,
    env         TEXT NOT NULL DEFAULT 'native',
    username    TEXT NOT NULL,
    agent       TEXT NOT NULL,
    method      TEXT NOT NULL,
    package     TEXT NOT NULL,
    os          TEXT NOT NULL DEFAULT '',
    arch        TEXT NOT NULL DEFAULT '',
    capability_state  TEXT NOT NULL DEFAULT '', -- 创建时能力矩阵判定快照
    capability_reason TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'pending', -- 白名单状态机（domain/lifecycle.go）
    reason      TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL DEFAULT '',
    evidence    TEXT NOT NULL DEFAULT '',  -- LifecycleEvidence JSON（不含秘密）
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (op_id, target_key)
);

CREATE INDEX IF NOT EXISTS ix_lifecycle_targets_machine
    ON lifecycle_operation_targets(machine_id);
