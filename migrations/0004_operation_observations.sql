-- 0004_operation_observations.sql
-- 「每操作最近一份绑定观测」（架构 v1.1.2 §4.4 门禁因果绑定 / FR-10.3）。
--
-- 裁决（KM-24，第 4 片）：门禁条件 2/3/4 的证据只能来自**与操作同 operationId
-- 绑定**的 apply 后观测。若沿用 observed_states 单行 UPSERT，一份更高 inventorySeq
-- 的周期 inventory（无 operationId）会在几秒内覆盖并清空该绑定，绑定观测的半衰期
-- 极短、门禁在生产必然失败。
--
-- 因此：observed_states 继续表示"每机最新一份观测"（机器条件与 drift 新鲜度用），
-- 绑定观测另存本表，按 (machine_id, operation_id) 保留最近一份，周期报文永不触碰。
-- 未绑定报文不覆盖既有绑定（结构性保证，非约定）。
CREATE TABLE IF NOT EXISTS operation_observations (
    machine_id          TEXT NOT NULL,
    operation_id        TEXT NOT NULL,
    inventory_seq       INTEGER NOT NULL,
    observed_generation INTEGER NOT NULL DEFAULT 0,
    payload             TEXT NOT NULL,
    recorded_at         TEXT NOT NULL,
    PRIMARY KEY (machine_id, operation_id)
);

CREATE INDEX IF NOT EXISTS ix_operation_observations_machine
    ON operation_observations(machine_id, recorded_at);
