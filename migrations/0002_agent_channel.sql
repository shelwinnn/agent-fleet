-- 0002_agent_channel.sql — enrollment/mTLS 与 inventory 上报所需表
-- （架构 v1.1.2 §4.6/§10.1；KM-22 第 2 片）。

-- enrollment token：一次性、短时效（§4.6）。
-- 只存 SHA-256(token) 哈希，不存明文（token 不得进日志，也不得持久化明文）；
-- csr_fingerprint 绑定 CSR 公钥指纹（"sha256:<hex>"）；
-- used_at 非空即已消费；一次性由条件更新（WHERE used_at IS NULL ...）保证（FR-11.5）。
CREATE TABLE IF NOT EXISTS enrollment_tokens (
    token_hash      TEXT PRIMARY KEY,
    machine_id      TEXT NOT NULL,             -- machines.name（沿用 operations.machine_id 键约定）
    csr_fingerprint TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    expires_at      TEXT NOT NULL,
    used_at         TEXT
);

CREATE INDEX IF NOT EXISTS ix_enrollment_tokens_machine ON enrollment_tokens(machine_id);

-- 已签发客户端证书：machine ↔ 证书序列号/有效期，不含私钥（§10.1）。
-- retired_at 承载"机器删除后其证书的后续 Connect 被拒"的审计事实
-- （v1.1.2 用户已确认取舍 (a)：MVP 无 CRL/OCSP，删除即拒绝）。
CREATE TABLE IF NOT EXISTS agent_certificates (
    serial      TEXT PRIMARY KEY,              -- 证书序列号（hex）
    machine_id  TEXT NOT NULL,
    not_before  TEXT NOT NULL,
    not_after   TEXT NOT NULL,
    issued_at   TEXT NOT NULL,
    retired_at  TEXT
);

CREATE INDEX IF NOT EXISTS ix_agent_certificates_machine ON agent_certificates(machine_id);

-- 节点观测状态：每机最新一份全量 inventory（§10.1 observed_states 最小子集）。
-- machine_id 为 machines.name；inventory_seq 高水位在写入时判定（FR-8.7：
-- 落后报文不得改写条件）。滚动清理与受保护引用集合随第 3 片细化。
CREATE TABLE IF NOT EXISTS observed_states (
    machine_id    TEXT PRIMARY KEY,
    inventory_seq INTEGER NOT NULL,
    operation_id  TEXT NOT NULL DEFAULT '',
    payload       TEXT NOT NULL,
    recorded_at   TEXT NOT NULL
);
