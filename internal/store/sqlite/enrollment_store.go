package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// enrollmentStore 实现 token / 证书 / 观测状态的仓储（架构 §4.6/§10.1）。
// machine_id 均为 machines.name（沿用第 1 片的机器键约定，KM-21 交接第 4 条）。
type enrollmentStore struct {
	db *sql.DB
}

func NewEnrollmentStore(db *DB) domain.EnrollmentTokenRepository {
	return &enrollmentStore{db: db.sql}
}

// NewAgentCertificateStore / NewObservedStateStore 返回同一具体实现的其他仓储视图。
func NewAgentCertificateStore(db *DB) domain.AgentCertificateRepository {
	return &enrollmentStore{db: db.sql}
}

func NewObservedStateStore(db *DB) domain.ObservedStateRepository {
	return &enrollmentStore{db: db.sql}
}

func (s *enrollmentStore) Create(ctx context.Context, tok *domain.EnrollmentToken) error {
	if tok.TokenHash == "" || tok.Machine == "" || tok.CSRFingerprint == "" {
		return fmt.Errorf("%w: enrollment token requires hash/machine/csr fingerprint", domain.ErrInvalid)
	}
	if tok.CreatedAt.IsZero() {
		tok.CreatedAt = time.Now().UTC()
	}
	if tok.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: enrollment token requires expiresAt", domain.ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO enrollment_tokens (token_hash, machine_id, csr_fingerprint, created_at, expires_at, used_at)
		 VALUES (?, ?, ?, ?, ?, NULL)`,
		tok.TokenHash, tok.Machine, tok.CSRFingerprint,
		tok.CreatedAt.Format(time.RFC3339Nano), tok.ExpiresAt.Format(time.RFC3339Nano))
	if err != nil {
		return mapConstraintErr(err, "enrollment_tokens")
	}
	return nil
}

// Get 返回 token 登记记录（供拒绝后做服务端审计分类，不改变一次性语义）。
func (s *enrollmentStore) Get(ctx context.Context, tokenHash string) (*domain.EnrollmentToken, error) {
	var t domain.EnrollmentToken
	var createdAt, expiresAt string
	var usedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT token_hash, machine_id, csr_fingerprint, created_at, expires_at, used_at
		 FROM enrollment_tokens WHERE token_hash = ?`, tokenHash).
		Scan(&t.TokenHash, &t.Machine, &t.CSRFingerprint, &createdAt, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: token", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get enrollment token: %w", err)
	}
	if t.CreatedAt, err = parseTime(createdAt, "token.created_at"); err != nil {
		return nil, err
	}
	if t.ExpiresAt, err = parseTime(expiresAt, "token.expires_at"); err != nil {
		return nil, err
	}
	if usedAt.Valid {
		u, err := parseTime(usedAt.String, "token.used_at")
		if err != nil {
			return nil, err
		}
		t.UsedAt = &u
	}
	return &t, nil
}

// Consume 落实 §4.6 原子消费契约：校验与作废处于同一条条件 UPDATE，
// 影响行数为 0 即拒绝（无效/已用/过期/绑定不符统一拒绝，细节由调用方
// 经 Get 做服务端审计分类）。并发抢注由 SQLite 单写 + IMMEDIATE 事务序列化。
func (s *enrollmentStore) Consume(ctx context.Context, tokenHash, machine, csrfingerprint string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE enrollment_tokens SET used_at = ?
		 WHERE token_hash = ? AND machine_id = ? AND csr_fingerprint = ?
		   AND used_at IS NULL AND expires_at > ?`,
		now.Format(time.RFC3339Nano), tokenHash, machine, csrfingerprint,
		now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("sqlite: consume enrollment token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: token is invalid, expired, already used, or bound to another machine/csr", domain.ErrEnrollmentRejected)
	}
	return nil
}

// RetireByMachine 把某机全部未退役证书打上退役时刻（§4.6：删除即拒绝，
// 保留审计事实）。机器删除路径（HTTP DELETE）由控制器调用。
func (s *enrollmentStore) RetireByMachine(ctx context.Context, machine string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_certificates SET retired_at = ?
		 WHERE machine_id = ? AND retired_at IS NULL`,
		now.Format(time.RFC3339Nano), machine)
	if err != nil {
		return fmt.Errorf("sqlite: retire certificates for %q: %w", machine, err)
	}
	return nil
}

// ---- agent_certificates ----

func (s *enrollmentStore) Record(ctx context.Context, c *domain.AgentCertificate) error {
	if c.Serial == "" || c.Machine == "" {
		return fmt.Errorf("%w: certificate record requires serial/machine", domain.ErrInvalid)
	}
	if c.IssuedAt.IsZero() {
		c.IssuedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_certificates (serial, machine_id, not_before, not_after, issued_at, retired_at)
		 VALUES (?, ?, ?, ?, ?, NULL)`,
		c.Serial, c.Machine,
		c.NotBefore.Format(time.RFC3339Nano), c.NotAfter.Format(time.RFC3339Nano),
		c.IssuedAt.Format(time.RFC3339Nano))
	if err != nil {
		return mapConstraintErr(err, "agent_certificates")
	}
	return nil
}

// ---- observed_states ----

func (s *enrollmentStore) Store(ctx context.Context, rec *domain.ObservedStateRecord) (bool, error) {
	if rec.Machine == "" {
		return false, fmt.Errorf("%w: observed state requires machine", domain.ErrInvalid)
	}
	if rec.RecordedAt.IsZero() {
		rec.RecordedAt = time.Now().UTC()
	}
	// 主行 + 绑定表在同一事务内写入：门禁证据（绑定观测）与机器条件
	// （最新观测）必须一致地推进，不能出现"条件已推进但证据丢失"的中间态。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("sqlite: begin observed state tx for %q: %w", rec.Machine, err)
	}
	defer func() { _ = tx.Rollback() }()

	// UPSERT 带 seq 条件：落后报文（inventory_seq < 已存高水位）不落主行、
	// 不改写条件（FR-8.7 观测有序性）。影响行数为 0 即被拒绝。
	res, err := tx.ExecContext(ctx,
		`INSERT INTO observed_states (machine_id, inventory_seq, operation_id, observed_generation, payload, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(machine_id) DO UPDATE SET
		   inventory_seq = excluded.inventory_seq,
		   operation_id  = excluded.operation_id,
		   observed_generation = excluded.observed_generation,
		   payload       = excluded.payload,
		   recorded_at   = excluded.recorded_at
		 WHERE excluded.inventory_seq >= observed_states.inventory_seq`,
		rec.Machine, rec.InventorySeq, rec.OperationID, rec.ObservedGeneration,
		string(rec.Payload), rec.RecordedAt.Format(time.RFC3339Nano))
	if err != nil {
		return false, fmt.Errorf("sqlite: store observed state for %q: %w", rec.Machine, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil // 落后报文：主行与绑定行都不改写
	}
	// 绑定观测：每 (machine, operation) 保留最近一份，带 seq 高水位判定；
	// 周期报文（OperationID == ""）不写本表，因此永不覆盖既有绑定。
	if rec.OperationID != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO operation_observations
			   (machine_id, operation_id, inventory_seq, observed_generation, payload, recorded_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(machine_id, operation_id) DO UPDATE SET
			   inventory_seq = excluded.inventory_seq,
			   observed_generation = excluded.observed_generation,
			   payload = excluded.payload,
			   recorded_at = excluded.recorded_at
			 WHERE excluded.inventory_seq >= operation_observations.inventory_seq`,
			rec.Machine, rec.OperationID, rec.InventorySeq, rec.ObservedGeneration,
			string(rec.Payload), rec.RecordedAt.Format(time.RFC3339Nano)); err != nil {
			return false, fmt.Errorf("sqlite: store operation observation for %q/%q: %w",
				rec.Machine, rec.OperationID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("sqlite: commit observed state for %q: %w", rec.Machine, err)
	}
	return true, nil
}

// LatestBound 取某操作最近一份绑定观测（§4.4 门禁条件 2 的唯一证据来源）。
func (s *enrollmentStore) LatestBound(ctx context.Context, machine, operationID string) (*domain.ObservedStateRecord, error) {
	var rec domain.ObservedStateRecord
	var payload, recordedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT machine_id, inventory_seq, operation_id, observed_generation, payload, recorded_at
		 FROM operation_observations WHERE machine_id = ? AND operation_id = ?`, machine, operationID).
		Scan(&rec.Machine, &rec.InventorySeq, &rec.OperationID, &rec.ObservedGeneration, &payload, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: observation bound to operation %q on machine %q",
			domain.ErrNotFound, operationID, machine)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get bound observation for %q/%q: %w", machine, operationID, err)
	}
	rec.Payload = json.RawMessage(payload)
	if rec.RecordedAt, err = parseTime(recordedAt, "operation_observations.recorded_at"); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *enrollmentStore) Latest(ctx context.Context, machine string) (*domain.ObservedStateRecord, error) {
	var rec domain.ObservedStateRecord
	var payload, recordedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT machine_id, inventory_seq, operation_id, observed_generation, payload, recorded_at
		 FROM observed_states WHERE machine_id = ?`, machine).
		Scan(&rec.Machine, &rec.InventorySeq, &rec.OperationID, &rec.ObservedGeneration, &payload, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: observed state for machine %q", domain.ErrNotFound, machine)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get observed state for %q: %w", machine, err)
	}
	rec.Payload = json.RawMessage(payload)
	if rec.RecordedAt, err = parseTime(recordedAt, "observed.recorded_at"); err != nil {
		return nil, err
	}
	return &rec, nil
}

func parseTime(v, field string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: parse %s %q: %w", field, v, err)
	}
	return t, nil
}
