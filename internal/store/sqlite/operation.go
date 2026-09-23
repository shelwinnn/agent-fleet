package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/ids"
)

// operationStore 实现 Operation 仓储与状态机迁移（架构 §4.3/§6.4/FR-15.5）。
// 机器级互斥以 operations 表的部分唯一索引表达（0001 迁移，谓词含
// AwaitingConfirmation/Unknown）：并发创建第二个未决操作在库层被拒绝；
// 迁移到终态即释放互斥。重启自愈（Running→Unknown，不移出未决集合）由
// reconcile 控制器经 ListUnresolved + Transition 完成——互斥随库恢复。
type operationStore struct {
	db *sql.DB
}

func NewOperationStore(db *DB) domain.OperationRepository {
	return &operationStore{db: db.sql}
}

func (s *operationStore) Create(ctx context.Context, op *domain.Operation) error {
	if op.Spec.Machine == "" {
		return fmt.Errorf("%w: spec.machine is required", domain.ErrInvalid)
	}
	switch op.Spec.Type {
	case domain.OperationTypeReconcile, domain.OperationTypeRepair, domain.OperationTypeBootstrap,
		domain.OperationTypeRollback, domain.OperationTypeAutoPlan:
	default:
		return fmt.Errorf("%w: unknown operation type %q", domain.ErrInvalid, op.Spec.Type)
	}
	if op.Status.Phase == "" {
		op.Status.Phase = domain.OperationPhasePending
	}
	meta := &op.Metadata
	meta.UID = ids.NewUID()
	// Operation 以生成的 id 为名称（API 客户端经 metadata.name 引用操作）。
	meta.Name = meta.UID
	meta.ResourceVersion = 1
	now := time.Now().UTC()
	meta.CreationTimestamp = now
	stamp := now.Format(time.RFC3339Nano)
	specJSON, err := json.Marshal(op.Spec)
	if err != nil {
		return fmt.Errorf("sqlite: marshal operation spec: %w", err)
	}
	statusJSON, err := json.Marshal(op.Status)
	if err != nil {
		return fmt.Errorf("sqlite: marshal operation status: %w", err)
	}
	readOnly := 0
	if op.Spec.ReadOnly {
		readOnly = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO operations (id, machine_id, op_type, transport, desired_generation, phase,
		     spec, status, read_only, plan_digest, terminal_modifier, verify, plan_expires_at,
		     resource_version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.UID, op.Spec.Machine, op.Spec.Type, op.Spec.Transport, op.Spec.DesiredGeneration,
		op.Status.Phase, string(specJSON), string(statusJSON), readOnly, op.Spec.PlanDigest,
		op.Status.TerminalModifier, marshalVerify(op.Status.Verify), marshalTime(op.Status.PlanExpiresAt),
		meta.ResourceVersion, stamp, stamp)
	if err != nil {
		// 唯一约束涉及 machine_id 的只有 §4.3 机器级互斥部分索引
		//（SQLite 对部分唯一索引冲突按列名报告）。
		if strings.Contains(err.Error(), "UNIQUE constraint failed: operations.machine_id") ||
			strings.Contains(err.Error(), "ux_operations_machine_unresolved") {
			return fmt.Errorf("%w: %s", domain.ErrMachineBusy, op.Spec.Machine)
		}
		return mapConstraintErr(err, "operations")
	}
	return nil
}

func (s *operationStore) Get(ctx context.Context, id string) (*domain.Operation, error) {
	row := s.db.QueryRowContext(ctx, operationSelect+` WHERE id = ?`, id)
	return scanOperation(row.Scan)
}

func (s *operationStore) ListByMachine(ctx context.Context, machine string) ([]*domain.Operation, error) {
	return s.list(ctx, operationSelect+` WHERE machine_id = ? ORDER BY created_at`, machine)
}

func (s *operationStore) ListUnresolved(ctx context.Context) ([]*domain.Operation, error) {
	phases := make([]string, 0, len(domain.UnresolvedPhases))
	args := make([]any, 0, len(domain.UnresolvedPhases))
	for _, p := range domain.UnresolvedPhases {
		phases = append(phases, "?")
		args = append(args, p)
	}
	query := operationSelect + ` WHERE phase IN (` + strings.Join(phases, ",") + `) ORDER BY created_at`
	return s.list(ctx, query, args...)
}

func (s *operationStore) list(ctx context.Context, query string, args ...any) ([]*domain.Operation, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list operations: %w", err)
	}
	defer rows.Close()
	var out []*domain.Operation
	for rows.Next() {
		op, err := scanOperation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

const operationSelect = `SELECT id, machine_id, op_type, transport, desired_generation, phase,
	spec, status, read_only, plan_digest, terminal_modifier, verify, plan_expires_at,
	resource_version, created_at, updated_at FROM operations`

func scanOperation(scan func(dest ...any) error) (*domain.Operation, error) {
	var id, machineID, opType, transport, phase, specJSON, statusJSON string
	var planDigest, terminalModifier, verifyJSON string
	var generation, readOnly, rv int64
	var planExpiresAt sql.NullString
	var createdAt, updatedAt string
	if err := scan(&id, &machineID, &opType, &transport, &generation, &phase,
		&specJSON, &statusJSON, &readOnly, &planDigest, &terminalModifier, &verifyJSON, &planExpiresAt,
		&rv, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: operation", domain.ErrNotFound)
		}
		return nil, fmt.Errorf("sqlite: scan operation: %w", err)
	}
	op := &domain.Operation{}
	// metadata.name = 操作 id（Create 时与 UID 一致）。
	if err := fillMeta(&op.Metadata, id, id, rv, createdAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(specJSON), &op.Spec); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal operation spec: %w", err)
	}
	status, err := domain.ParseOperationStatus(statusJSON)
	if err != nil {
		return nil, err
	}
	// 列为准：权威相位与扩展列以独立列存储，JSON 仅做镜像。
	status.Phase = phase
	status.TerminalModifier = terminalModifier
	if verifyJSON != "" {
		ve := &domain.VerifyEvidence{}
		if err := json.Unmarshal([]byte(verifyJSON), ve); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal verify evidence: %w", err)
		}
		status.Verify = ve
	}
	if planExpiresAt.Valid {
		t, err := parseTime(planExpiresAt.String, "operation.plan_expires_at")
		if err != nil {
			return nil, err
		}
		status.PlanExpiresAt = &t
	}
	op.Spec.DesiredGeneration = generation
	op.Spec.ReadOnly = readOnly == 1
	op.Spec.PlanDigest = planDigest
	op.Status = status
	return op, nil
}

// Transition 在单事务内做 CAS 相位迁移（§6.4 状态机）。from 为空表示任意未决
// 相位均可；迁移到终态后部分唯一索引不再命中，机器级互斥随之释放。
func (s *operationStore) Transition(ctx context.Context, id, from, to string, mutate func(*domain.Operation) error) error {
	return withBusyRetry(ctx, "transition_operation:"+id, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin transition %s: %w", id, err)
		}
		defer func() { _ = tx.Rollback() }()

		op, err := scanOperation(tx.QueryRow(operationSelect+` WHERE id = ?`, id).Scan)
		if err != nil {
			return err
		}
		if from != "" && op.Status.Phase != from {
			return fmt.Errorf("%w: operation %s is %s, want %s", domain.ErrOpState, id, op.Status.Phase, from)
		}
		if from == "" && domain.IsTerminal(op.Status.Phase) {
			return fmt.Errorf("%w: operation %s already terminal (%s)", domain.ErrOpState, id, op.Status.Phase)
		}
		op.Status.Phase = to
		if mutate != nil {
			if err := mutate(op); err != nil {
				return err
			}
		}
		if err := writeOperationRow(ctx, tx, op); err != nil {
			return err
		}
		return tx.Commit()
	})
}

func writeOperationRow(ctx context.Context, tx *sql.Tx, op *domain.Operation) error {
	specJSON, err := json.Marshal(op.Spec)
	if err != nil {
		return fmt.Errorf("sqlite: marshal operation spec: %w", err)
	}
	statusJSON, err := json.Marshal(op.Status)
	if err != nil {
		return fmt.Errorf("sqlite: marshal operation status: %w", err)
	}
	readOnly := 0
	if op.Spec.ReadOnly {
		readOnly = 1
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE operations SET machine_id = ?, op_type = ?, transport = ?, desired_generation = ?,
		   phase = ?, spec = ?, status = ?, read_only = ?, plan_digest = ?,
		   terminal_modifier = ?, verify = ?, plan_expires_at = ?,
		   resource_version = resource_version + 1, updated_at = ?
		 WHERE id = ?`,
		op.Spec.Machine, op.Spec.Type, op.Spec.Transport, op.Spec.DesiredGeneration,
		op.Status.Phase, string(specJSON), string(statusJSON), readOnly, op.Spec.PlanDigest,
		op.Status.TerminalModifier, marshalVerify(op.Status.Verify), marshalTime(op.Status.PlanExpiresAt),
		nowStamp(), op.Metadata.Name)
	if err != nil {
		return fmt.Errorf("sqlite: update operation %s: %w", op.Metadata.Name, err)
	}
	return nil
}

func (s *operationStore) ReplaceSteps(ctx context.Context, id string, steps []domain.OperationStepRecord) error {
	return withBusyRetry(ctx, "replace_steps:"+id, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin replace steps %s: %w", id, err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `DELETE FROM operation_steps WHERE op_id = ?`, id); err != nil {
			return fmt.Errorf("sqlite: clear steps %s: %w", id, err)
		}
		for _, st := range steps {
			if st.RecordedAt.IsZero() {
				st.RecordedAt = time.Now().UTC()
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO operation_steps (op_id, seq, name, phase, output, recorded_at)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				id, st.Seq, st.Name, st.Phase, st.Output, st.RecordedAt.Format(time.RFC3339Nano)); err != nil {
				return fmt.Errorf("sqlite: insert step %s/%d: %w", id, st.Seq, err)
			}
		}
		return tx.Commit()
	})
}

func (s *operationStore) ListSteps(ctx context.Context, id string) ([]domain.OperationStepRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, name, phase, output, recorded_at FROM operation_steps WHERE op_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list steps %s: %w", id, err)
	}
	defer rows.Close()
	var out []domain.OperationStepRecord
	for rows.Next() {
		var st domain.OperationStepRecord
		var recordedAt string
		if err := rows.Scan(&st.Seq, &st.Name, &st.Phase, &st.Output, &recordedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan steps: %w", err)
		}
		if st.RecordedAt, err = parseTime(recordedAt, "step.recorded_at"); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func marshalVerify(v *domain.VerifyEvidence) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func marshalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339Nano)
}
