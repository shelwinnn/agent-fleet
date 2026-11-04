package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/ids"
)

// lifecycleStore 实现 LifecycleRepository（KM-47）。
//
// 两个库层不变量：
//   - 幂等：idempotency_key 上的唯一索引（0005）承载 get-or-create——并发重复
//     提交只有一个 INSERT 成功，其余读到同一行并复用同一操作，不产生重复变更；
//     同键不同内容由 request_digest 判为客户端缺陷（ErrIdempotencyConflict）。
//   - 逐目标状态机：白名单迁移在单事务内做 CAS（domain.TargetTransitionAllowed），
//     not_executed 与 unknown 无出边——目标上线不自动执行旧意图，未知不原地改写。
type lifecycleStore struct {
	db      *sql.DB
	changes *changeNotifier
}

// NewLifecycleStore 构造生命周期仓储。
func NewLifecycleStore(db *DB) domain.LifecycleRepository {
	return &lifecycleStore{db: db.sql, changes: db.changes}
}

// —— 期望意图文档（显式三态）——

func (s *lifecycleStore) CurrentDesired(ctx context.Context, machine string) (*domain.LifecycleDesiredRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT revision, digest, entries, created_at
		FROM lifecycle_desired WHERE machine_id = ? ORDER BY revision DESC LIMIT 1`, machine)
	return scanDesiredRecord(machine, row.Scan)
}

func (s *lifecycleStore) PutDesired(ctx context.Context, machine string, update domain.LifecycleDesired) (*domain.LifecycleDesiredRecord, bool, error) {
	if strings.TrimSpace(machine) == "" {
		return nil, false, fmt.Errorf("%w: machine is required", domain.ErrInvalid)
	}
	var out *domain.LifecycleDesiredRecord
	changed := false
	err := withBusyRetry(ctx, "put_lifecycle_desired:"+machine, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin put desired %s: %w", machine, err)
		}
		defer func() { _ = tx.Rollback() }()

		current, err := scanDesiredRecord(machine, tx.QueryRow(`SELECT revision, digest, entries, created_at
			FROM lifecycle_desired WHERE machine_id = ? ORDER BY revision DESC LIMIT 1`, machine).Scan)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		curDesired := domain.LifecycleDesired{}
		rev := int64(0)
		if current != nil {
			curDesired = current.Desired
			rev = current.Revision
		}
		merged, ch, err := domain.MergeLifecycleDesired(curDesired, update)
		if err != nil {
			return err
		}
		if !ch {
			if current == nil {
				// 空更新落在从未纳管的机器上：不落库，但返回非 nil 的"零代"文档
				// 表示当前无任何生命周期管理（调用方不必对 changed=false 做nil 分支）。
				out = &domain.LifecycleDesiredRecord{Machine: machine, Revision: 0,
					Desired: domain.LifecycleDesired{SchemaVersion: domain.LifecycleDesiredSchemaVersion,
						Entries: map[string]domain.LifecycleEntry{}}}
			} else {
				out = current
			}
			changed = false
			return nil
		}
		entriesJSON, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("sqlite: marshal lifecycle desired: %w", err)
		}
		sum := fmt.Sprintf("sha256:%x", sha256.Sum256(entriesJSON))
		rev++
		stamp := nowStamp()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO lifecycle_desired (machine_id, revision, digest, entries, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			machine, rev, sum, string(entriesJSON), stamp); err != nil {
			return fmt.Errorf("sqlite: insert lifecycle desired %s: %w", machine, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		record := &domain.LifecycleDesiredRecord{Machine: machine, Revision: rev,
			Digest: sum, Desired: merged}
		if t, perr := parseTime(stamp, "lifecycle_desired.created_at"); perr == nil {
			record.CreatedAt = t
		}
		out, changed = record, true
		s.changes.emit(resourceLifecycleDesired, machine, rev)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

func scanDesiredRecord(machine string, scan func(dest ...any) error) (*domain.LifecycleDesiredRecord, error) {
	var revision int64
	var digest, entries, createdAt string
	if err := scan(&revision, &digest, &entries, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: lifecycle desired for %s", domain.ErrNotFound, machine)
		}
		return nil, fmt.Errorf("sqlite: scan lifecycle desired: %w", err)
	}
	rec := &domain.LifecycleDesiredRecord{Machine: machine, Revision: revision, Digest: digest}
	if err := json.Unmarshal([]byte(entries), &rec.Desired); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal lifecycle desired: %w", err)
	}
	t, err := parseTime(createdAt, "lifecycle_desired.created_at")
	if err != nil {
		return nil, err
	}
	rec.CreatedAt = t
	return rec, nil
}

// —— 生命周期操作 ——

// lifecycleRequestFingerprint 是幂等键复用检测的摘要输入：同键必须同内容。
// Targets 已归一化并按身份键排序（顺序不改变意图）。
type lifecycleRequestFingerprint struct {
	Type    string                   `json:"type"`
	Version string                   `json:"version,omitempty"`
	RetryOf string                   `json:"retryOf,omitempty"`
	Targets []domain.LifecycleTarget `json:"targets"`
}

func lifecycleRequestDigest(op *domain.LifecycleOperation) (string, error) {
	fp := lifecycleRequestFingerprint{Type: op.Spec.Type, Version: op.Spec.Version, RetryOf: op.RetryOf}
	fp.Targets = make([]domain.LifecycleTarget, len(op.Spec.Targets))
	for i, t := range op.Spec.Targets {
		fp.Targets[i] = t.Normalize()
	}
	sort.Slice(fp.Targets, func(i, j int) bool { return fp.Targets[i].Key() < fp.Targets[j].Key() })
	b, err := json.Marshal(fp)
	if err != nil {
		return "", fmt.Errorf("sqlite: marshal lifecycle fingerprint: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

func (s *lifecycleStore) CreateOperation(ctx context.Context, op *domain.LifecycleOperation) (*domain.LifecycleOperation, bool, error) {
	if err := domain.ValidateLifecycleOperation(op); err != nil {
		return nil, false, err
	}
	// 归一化目标并快照能力判定（操作记录携带"创建时的矩阵判定"）。
	normalized := make([]domain.LifecycleTarget, len(op.Spec.Targets))
	for i, t := range op.Spec.Targets {
		normalized[i] = domain.SnapshotCapability(t.Normalize())
	}
	op.Spec.Targets = normalized
	op.Spec.IdempotencyKey = strings.TrimSpace(op.Spec.IdempotencyKey)
	if op.Status.Phase == "" {
		op.Status.Phase = domain.OperationPhasePending
	}
	// 创建态一律从 pending 起步：调用方携带的任何预置结果/证据都被丢弃——
	// 状态与证据只能经 TransitionTarget 白名单进入，不存在"生而成功"的目标。
	op.Status.Results = make([]domain.LifecycleTargetResult, len(op.Spec.Targets))
	for i := range op.Spec.Targets {
		op.Status.Results[i] = domain.LifecycleTargetResult{Target: op.Spec.Targets[i], State: domain.TargetPending}
	}
	meta := &op.Metadata
	if meta.Name == "" {
		meta.UID = ids.NewUID()
		meta.Name = meta.UID
	}
	meta.ResourceVersion = 1
	digest, err := lifecycleRequestDigest(op)
	if err != nil {
		return nil, false, err
	}

	var stored *domain.LifecycleOperation
	created := false
	err = withBusyRetry(ctx, "create_lifecycle_op:"+op.Spec.IdempotencyKey, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin create lifecycle op: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		// 快路径：键已存在则复用（同键同内容 → 同一操作；不同内容 → 冲突）。
		existing, existingDigest, err := scanLifecycleOpWithDigest(tx.QueryRow(lifecycleOpSelect+` WHERE idempotency_key = ?`,
			op.Spec.IdempotencyKey).Scan, opTargetsLoader(ctx, tx))
		if err == nil {
			if existingDigest != digest {
				return fmt.Errorf("%w: key %s", domain.ErrIdempotencyConflict, op.Spec.IdempotencyKey)
			}
			stored, created = existing, false
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		// 重试追溯校验与插入同事务：仅确定失败目标、意图必须与原操作一致。
		if op.RetryOf != "" {
			original, err := scanLifecycleOp(tx.QueryRow(lifecycleOpSelect+` WHERE id = ?`, op.RetryOf).Scan, opTargetsLoader(ctx, tx))
			if errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("%w: retry original %s not found", domain.ErrInvalid, op.RetryOf)
			}
			if err != nil {
				return err
			}
			if err := domain.ValidateLifecycleRetry(op, original); err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		meta.CreationTimestamp = now
		stamp := now.Format(time.RFC3339Nano)
		var confirmedAt any
		if op.Status.ConfirmedAt != nil {
			confirmedAt = op.Status.ConfirmedAt.Format(time.RFC3339Nano)
		}
		needsReconfirm := 0
		if op.Status.NeedsReconfirmation {
			needsReconfirm = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO lifecycle_operations (id, idempotency_key, request_digest, op_type, version,
			     retry_of, created_by, phase, needs_reconfirm, reconfirm_reason, confirmed_at,
			     resource_version, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			meta.Name, op.Spec.IdempotencyKey, digest, op.Spec.Type, op.Spec.Version,
			op.RetryOf, op.Spec.CreatedBy, op.Status.Phase, needsReconfirm, op.Status.ReconfirmReason, confirmedAt,
			meta.ResourceVersion, stamp, stamp); err != nil {
			// 并发竞争：另一请求抢先插入同一幂等键——回读复用。
			if strings.Contains(err.Error(), "ux_lifecycle_operations_idem") ||
				strings.Contains(err.Error(), "UNIQUE constraint failed: lifecycle_operations.idempotency_key") {
				existing, existingDigest, gerr := scanLifecycleOpWithDigest(tx.QueryRow(lifecycleOpSelect+` WHERE idempotency_key = ?`,
					op.Spec.IdempotencyKey).Scan, opTargetsLoader(ctx, tx))
				if gerr != nil {
					return gerr
				}
				if existingDigest != digest {
					return fmt.Errorf("%w: key %s", domain.ErrIdempotencyConflict, op.Spec.IdempotencyKey)
				}
				stored, created = existing, false
				return nil
			}
			return fmt.Errorf("sqlite: insert lifecycle operation: %w", err)
		}
		if err := insertLifecycleTargets(ctx, tx, meta.Name, op.Status.Results); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		stored, created = op, true
		s.changes.emit(resourceLifecycleOperations, meta.Name, meta.ResourceVersion)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return stored, created, nil
}

func insertLifecycleTargets(ctx context.Context, tx *sql.Tx, opID string, results []domain.LifecycleTargetResult) error {
	stamp := nowStamp()
	for _, r := range results {
		t := r.Target.Normalize()
		evJSON := ""
		if r.Evidence != nil {
			b, err := json.Marshal(r.Evidence)
			if err != nil {
				return fmt.Errorf("sqlite: marshal lifecycle evidence: %w", err)
			}
			evJSON = string(b)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO lifecycle_operation_targets (op_id, target_key, machine_id, env, username, agent,
			     method, package, os, arch, capability_state, capability_reason,
			     state, reason, message, evidence, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			opID, t.Key(), t.Machine, t.Env, t.User, t.Agent, t.Method, t.Package,
			t.OS, t.Arch, t.CapabilityState, t.CapabilityReason,
			r.State, r.Reason, r.Message, evJSON, stamp); err != nil {
			return fmt.Errorf("sqlite: insert lifecycle target %s: %w", t.Key(), err)
		}
	}
	return nil
}

const lifecycleOpSelect = `SELECT id, idempotency_key, request_digest, op_type, version, retry_of,
	created_by, phase, needs_reconfirm, reconfirm_reason, confirmed_at, started_at, completed_at,
	resource_version, created_at, updated_at
	FROM lifecycle_operations`

const lifecycleTargetSelect = `SELECT target_key, machine_id, env, username, agent, method, package,
	os, arch, capability_state, capability_reason, state, reason, message, evidence, updated_at
	FROM lifecycle_operation_targets`

// rowSetQuerier 由 *sql.DB 与 *sql.Tx 共同满足（snapshot.go 的 rowQuerier 是
// QueryRowContext 版，返回单行，语义不同故另起其名）：目标集装载在事务内外
// 都可用。
type rowSetQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// opTargetsLoader 返回从 q 加载某操作全部目标结果的闭包。
func opTargetsLoader(ctx context.Context, q rowSetQuerier) func(opID string) ([]domain.LifecycleTargetResult, error) {
	return func(opID string) ([]domain.LifecycleTargetResult, error) {
		rows, err := q.QueryContext(ctx, lifecycleTargetSelect+` WHERE op_id = ? ORDER BY target_key`, opID)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list lifecycle targets: %w", err)
		}
		defer rows.Close()
		return scanLifecycleTargets(rows)
	}
}

func scanLifecycleTargets(rows *sql.Rows) ([]domain.LifecycleTargetResult, error) {
	var out []domain.LifecycleTargetResult
	for rows.Next() {
		var key, env, user, agent, method, pkg, os, arch, capState, capReason, state, reason, message, evidence, updatedAt string
		var machine string
		if err := rows.Scan(&key, &machine, &env, &user, &agent, &method, &pkg,
			&os, &arch, &capState, &capReason, &state, &reason, &message, &evidence, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan lifecycle target: %w", err)
		}
		r := domain.LifecycleTargetResult{
			Target: domain.LifecycleTarget{
				Machine: machine, Env: env, User: user, Agent: agent, Method: method, Package: pkg,
				OS: os, Arch: arch, CapabilityState: capState, CapabilityReason: capReason,
			},
			State: state, Reason: reason, Message: message,
		}
		if evidence != "" {
			ev := &domain.LifecycleEvidence{}
			if err := json.Unmarshal([]byte(evidence), ev); err != nil {
				return nil, fmt.Errorf("sqlite: unmarshal lifecycle evidence: %w", err)
			}
			r.Evidence = ev
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanLifecycleOp 组装一行操作记录 + 逐目标结果（含 request_digest 供幂等键
// 复用检测）。loadTargets 为 nil 时跳过目标装载（仅校验存在性的内部路径）。
func scanLifecycleOpWithDigest(scan func(dest ...any) error, loadTargets func(opID string) ([]domain.LifecycleTargetResult, error)) (*domain.LifecycleOperation, string, error) {
	var id, idemKey, digest, opType, version, retryOf, createdBy, phase, reconfirmReason string
	var needsReconfirm, rv int64
	var confirmedAt, startedAt, completedAt sql.NullString
	var createdAt, updatedAt string
	if err := scan(&id, &idemKey, &digest, &opType, &version, &retryOf, &createdBy, &phase,
		&needsReconfirm, &reconfirmReason, &confirmedAt, &startedAt, &completedAt, &rv, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("%w: lifecycle operation", domain.ErrNotFound)
		}
		return nil, "", fmt.Errorf("sqlite: scan lifecycle operation: %w", err)
	}
	op := &domain.LifecycleOperation{
		Metadata: domain.ObjectMeta{Name: id, UID: id, ResourceVersion: rv},
		Spec: domain.LifecycleOperationSpec{
			Type: opType, IdempotencyKey: idemKey, Version: version, CreatedBy: createdBy,
		},
		Status:  domain.LifecycleOperationStatus{Phase: phase},
		RetryOf: retryOf,
	}
	op.Status.NeedsReconfirmation = needsReconfirm == 1
	op.Status.ReconfirmReason = reconfirmReason
	if confirmedAt.Valid {
		t, err := parseTime(confirmedAt.String, "lifecycle_operation.confirmed_at")
		if err != nil {
			return nil, "", err
		}
		op.Status.ConfirmedAt = &t
	}
	if startedAt.Valid {
		t, err := parseTime(startedAt.String, "lifecycle_operation.started_at")
		if err != nil {
			return nil, "", err
		}
		op.Status.StartedAt = &t
	}
	if completedAt.Valid {
		t, err := parseTime(completedAt.String, "lifecycle_operation.completed_at")
		if err != nil {
			return nil, "", err
		}
		op.Status.CompletedAt = &t
	}
	ct, err := parseTime(createdAt, "lifecycle_operation.created_at")
	if err != nil {
		return nil, "", err
	}
	op.Metadata.CreationTimestamp = ct
	if loadTargets != nil {
		results, err := loadTargets(id)
		if err != nil {
			return nil, "", err
		}
		op.Status.Results = results
		// 目标身份回填 spec.Targets（列为准），保持 Spec/Status 一致。
		op.Spec.Targets = make([]domain.LifecycleTarget, len(results))
		for i := range results {
			op.Spec.Targets[i] = results[i].Target
		}
	}
	return op, digest, nil
}

// scanLifecycleOp 是不需要 digest 的便捷封装。
func scanLifecycleOp(scan func(dest ...any) error, loadTargets func(opID string) ([]domain.LifecycleTargetResult, error)) (*domain.LifecycleOperation, error) {
	op, _, err := scanLifecycleOpWithDigest(scan, loadTargets)
	return op, err
}

func (s *lifecycleStore) GetOperation(ctx context.Context, id string) (*domain.LifecycleOperation, error) {
	return scanLifecycleOp(s.db.QueryRowContext(ctx, lifecycleOpSelect+` WHERE id = ?`, id).Scan,
		opTargetsLoader(ctx, s.db))
}

func (s *lifecycleStore) GetOperationByIdempotencyKey(ctx context.Context, key string) (*domain.LifecycleOperation, error) {
	return scanLifecycleOp(s.db.QueryRowContext(ctx, lifecycleOpSelect+` WHERE idempotency_key = ?`, key).Scan,
		opTargetsLoader(ctx, s.db))
}

func (s *lifecycleStore) ListOperationsByMachine(ctx context.Context, machine string) ([]*domain.LifecycleOperation, error) {
	return s.listByOpIDs(ctx, `SELECT DISTINCT op_id FROM lifecycle_operation_targets
		WHERE machine_id = ?`, machine)
}

func (s *lifecycleStore) ListRetryChildren(ctx context.Context, id string) ([]*domain.LifecycleOperation, error) {
	return s.listByOpIDs(ctx, `SELECT id FROM lifecycle_operations WHERE retry_of = ? ORDER BY created_at, id`, id)
}

// ListUnfinishedOperations 列出相位未到 Completed 的操作（KM-49 执行编排的
// 派发与超时扫描输入）。phase 列由 TransitionTarget 按逐目标状态推导维护。
func (s *lifecycleStore) ListUnfinishedOperations(ctx context.Context) ([]*domain.LifecycleOperation, error) {
	return s.listByOpIDs(ctx, `SELECT id FROM lifecycle_operations
		WHERE phase <> ? ORDER BY created_at, id`, domain.OperationPhaseCompleted)
}

// listByOpIDs 先收集操作 id（游标关闭后再逐个取全量），与既有仓储的列表
// 形状一致：行遍历期间不发起嵌套查询。
func (s *lifecycleStore) listByOpIDs(ctx context.Context, idQuery string, args ...any) ([]*domain.LifecycleOperation, error) {
	rows, err := s.db.QueryContext(ctx, idQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list lifecycle ops: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sqlite: scan lifecycle op id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("sqlite: list lifecycle ops: %w", err)
	}
	rows.Close()

	var out []*domain.LifecycleOperation
	for _, id := range ids {
		op, err := s.GetOperation(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Metadata.CreationTimestamp.Equal(out[j].Metadata.CreationTimestamp) {
			return out[i].Metadata.CreationTimestamp.Before(out[j].Metadata.CreationTimestamp)
		}
		return out[i].Metadata.Name < out[j].Metadata.Name
	})
	return out, nil
}

// TransitionTarget 在单事务内做逐目标白名单迁移，重算操作相位并维护起止时间。
func (s *lifecycleStore) TransitionTarget(ctx context.Context, opID, targetKey, to, reason, message string, ev *domain.LifecycleEvidence) error {
	if len(message) > domain.MaxEvidenceMessageLen {
		return fmt.Errorf("%w: message exceeds %d chars", domain.ErrInvalid, domain.MaxEvidenceMessageLen)
	}
	if ev != nil && len(ev.Message) > domain.MaxEvidenceMessageLen {
		return fmt.Errorf("%w: evidence message exceeds %d chars", domain.ErrInvalid, domain.MaxEvidenceMessageLen)
	}
	return withBusyRetry(ctx, "transition_lifecycle_target:"+opID, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin lifecycle transition: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		op, err := scanLifecycleOp(tx.QueryRow(lifecycleOpSelect+` WHERE id = ?`, opID).Scan, nil)
		if err != nil {
			return err
		}
		results, err := opTargetsLoader(ctx, tx)(opID)
		if err != nil {
			return err
		}
		idx := -1
		for i := range results {
			if results[i].Target.Key() == targetKey {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("%w: lifecycle target %s in %s", domain.ErrNotFound, targetKey, opID)
		}
		from := results[idx].State
		if !domain.TargetTransitionAllowed(from, to) {
			return fmt.Errorf("%w: lifecycle target in %s cannot move %s → %s", domain.ErrOpState, opID, from, to)
		}
		if err := domain.ValidateTargetResult(to, reason); err != nil {
			return err
		}

		if ev != nil {
			merged := *ev
			if merged.RecordedAt.IsZero() {
				merged.RecordedAt = time.Now().UTC()
			}
			ev = &merged
		}
		results[idx].State = to
		results[idx].Reason = reason
		results[idx].Message = message
		results[idx].Evidence = ev

		evJSON := ""
		if ev != nil {
			b, err := json.Marshal(ev)
			if err != nil {
				return fmt.Errorf("sqlite: marshal lifecycle evidence: %w", err)
			}
			evJSON = string(b)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE lifecycle_operation_targets SET state = ?, reason = ?, message = ?, evidence = ?, updated_at = ?
			 WHERE op_id = ? AND target_key = ?`,
			to, reason, message, evJSON, nowStamp(), opID, targetKey); err != nil {
			return fmt.Errorf("sqlite: update lifecycle target: %w", err)
		}

		// 重算操作相位并维护起止时间（首次 running 记 StartedAt；全目标落定记 CompletedAt）。
		phase := domain.RecomputeOpPhase(results)
		startedAt, completedAt := op.Status.StartedAt, op.Status.CompletedAt
		nowTS := time.Now().UTC()
		if phase == domain.OperationPhaseRunning && startedAt == nil {
			startedAt = &nowTS
		}
		if phase == domain.OperationPhaseCompleted && completedAt == nil {
			completedAt = &nowTS
		}
		var startStamp, completeStamp any
		if startedAt != nil {
			startStamp = startedAt.Format(time.RFC3339Nano)
		}
		if completedAt != nil {
			completeStamp = completedAt.Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE lifecycle_operations SET phase = ?, started_at = COALESCE(?, started_at),
			    completed_at = COALESCE(?, completed_at), resource_version = resource_version + 1, updated_at = ?
			 WHERE id = ?`,
			phase, startStamp, completeStamp, nowStamp(), opID); err != nil {
			return fmt.Errorf("sqlite: update lifecycle operation phase: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.changes.emit(resourceLifecycleOperations, opID, op.Metadata.ResourceVersion+1)
		return nil
	})
}

// SetNeedsReconfirmation 置位/清除重试需重新确认标记（重发现影响变化时置位）。
// 事件携带落库后的真实版本（KM-25 核查必改 M1：版本从未在库里出现过的事件
// 会被前端判重丢弃）。
func (s *lifecycleStore) SetNeedsReconfirmation(ctx context.Context, opID string, needs bool, reason string) error {
	return withBusyRetry(ctx, "set_lifecycle_reconfirm:"+opID, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin set lifecycle reconfirm: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		var rv int64
		if err := tx.QueryRowContext(ctx,
			`SELECT resource_version FROM lifecycle_operations WHERE id = ?`, opID).Scan(&rv); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: lifecycle operation", domain.ErrNotFound)
			}
			return fmt.Errorf("sqlite: lock lifecycle operation %s: %w", opID, err)
		}
		flag := 0
		if needs {
			flag = 1
		}
		if !needs {
			reason = ""
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE lifecycle_operations SET needs_reconfirm = ?, reconfirm_reason = ?,
			    resource_version = ?, updated_at = ?
			 WHERE id = ?`,
			flag, reason, rv+1, nowStamp(), opID); err != nil {
			return fmt.Errorf("sqlite: set lifecycle reconfirm: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.changes.emit(resourceLifecycleOperations, opID, rv+1)
		return nil
	})
}

// MarkConfirmed 记录操作者确认时刻并清除需重新确认标记（重新确认即放行）。
// 事件版本纪律同 SetNeedsReconfirmation。
func (s *lifecycleStore) MarkConfirmed(ctx context.Context, opID string, at time.Time) error {
	return withBusyRetry(ctx, "confirm_lifecycle_op:"+opID, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin confirm lifecycle op: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		var rv int64
		if err := tx.QueryRowContext(ctx,
			`SELECT resource_version FROM lifecycle_operations WHERE id = ?`, opID).Scan(&rv); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: lifecycle operation", domain.ErrNotFound)
			}
			return fmt.Errorf("sqlite: lock lifecycle operation %s: %w", opID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE lifecycle_operations SET confirmed_at = ?, needs_reconfirm = 0, reconfirm_reason = '',
			    resource_version = ?, updated_at = ?
			 WHERE id = ?`,
			at.UTC().Format(time.RFC3339Nano), rv+1, nowStamp(), opID); err != nil {
			return fmt.Errorf("sqlite: confirm lifecycle operation: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.changes.emit(resourceLifecycleOperations, opID, rv+1)
		return nil
	})
}
