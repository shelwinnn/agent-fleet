-- 0001_init.sql — agent-fleet MVP 骨架表（架构 v1.1.2 §10.1 最小子集）。
-- 资源统一 metadata/spec/status JSON 形态（§6.1），spec/status 落 JSON 文本列，
-- 只为控制面需要查询的字段建独立列。名称在类型内唯一（§6.1）。

CREATE TABLE IF NOT EXISTS machines (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    management_mode  TEXT NOT NULL DEFAULT '',  -- 查询列，源自 spec.managementMode（agentd|ssh）
    profile_ref      TEXT NOT NULL DEFAULT '',  -- 查询列，源自 spec.profileRef
    spec             TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT '{}',-- 控制器写入，API 创建时初始化为 {}
    resource_version INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL,             -- RFC3339Nano UTC
    updated_at       TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_machines_name ON machines(name);

CREATE TABLE IF NOT EXISTS profiles (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    spec             TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT '{}',
    resource_version INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_profiles_name ON profiles(name);

CREATE TABLE IF NOT EXISTS providers (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    spec             TEXT NOT NULL DEFAULT '{}', -- 不含秘密值，仅 apiKeyEnv 等引用（§10.1）
    status           TEXT NOT NULL DEFAULT '{}',
    resource_version INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_providers_name ON providers(name);

CREATE TABLE IF NOT EXISTS skills (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    spec             TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT '{}', -- 指向最新解析（resolvedRevision/contentDigest）
    resource_version INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_skills_name ON skills(name);

CREATE TABLE IF NOT EXISTS deployments (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    spec             TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT '{}',
    resource_version INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_deployments_name ON deployments(name);

-- Operation 为不可变审计单元（§6.4）；MVP 骨架仅落最小字段，
-- steps/verify 证据等列由后续切片的迁移补充。
CREATE TABLE IF NOT EXISTS operations (
    id                 TEXT PRIMARY KEY,
    machine_id         TEXT NOT NULL,            -- 目标机器的 name
    op_type            TEXT NOT NULL,            -- Reconcile|Repair|Bootstrap|Rollback|AutoPlan
    transport          TEXT NOT NULL DEFAULT '', -- agentd|ssh
    desired_generation INTEGER NOT NULL DEFAULT 0,
    phase              TEXT NOT NULL DEFAULT 'Pending',
    spec               TEXT NOT NULL DEFAULT '{}',
    status             TEXT NOT NULL DEFAULT '{}',
    resource_version   INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_operations_machine ON operations(machine_id, created_at);

-- 架构 §4.3/§4.9：机器级互斥以持久层表达——每台机器至多一个未决操作。
-- 唯一约束冲突即该机已有未决操作（409 MachineBusy）；终态（Succeeded/Failed）不占用。
CREATE UNIQUE INDEX IF NOT EXISTS ux_operations_machine_unresolved
    ON operations(machine_id)
    WHERE phase IN ('Pending','Running','AwaitingConfirmation','CancelRequested','Unknown');
