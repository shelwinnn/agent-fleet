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

// machineStore 实现 Machine 仓储：machines 表额外维护 management_mode / profile_ref
// 查询列（架构 §10.1），取自 spec JSON。
type machineStore struct {
	db *sql.DB
}

func (s *machineStore) core(spec json.RawMessage) (domain.MachineCoreSpec, error) {
	var core domain.MachineCoreSpec
	if len(spec) != 0 {
		if err := json.Unmarshal(spec, &core); err != nil {
			return core, fmt.Errorf("%w: spec: %v", domain.ErrInvalid, err)
		}
	}
	if core.ManagementMode == "" {
		core.ManagementMode = domain.ManagementModeAgentd
	}
	return core, nil
}

func (s *machineStore) Create(ctx context.Context, m *domain.Machine) error {
	meta := &m.Metadata
	if meta.Name == "" {
		return fmt.Errorf("%w: metadata.name is required", domain.ErrInvalid)
	}
	if len(m.Spec) == 0 {
		m.Spec = json.RawMessage("{}")
	}
	if len(m.Status) == 0 {
		m.Status = json.RawMessage("{}")
	}
	core, err := s.core(m.Spec)
	if err != nil {
		return err
	}
	meta.UID = ids.NewUID()
	meta.ResourceVersion = 1
	now := time.Now().UTC()
	meta.CreationTimestamp = now
	stamp := now.Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO machines (id, name, management_mode, profile_ref, spec, status, resource_version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.UID, meta.Name, core.ManagementMode, core.ProfileRef,
		string(m.Spec), string(m.Status), meta.ResourceVersion, stamp, stamp)
	return mapConstraintErr(err, "machines")
}

func (s *machineStore) Get(ctx context.Context, name string) (*domain.Machine, error) {
	var id, resourceName, mode, profileRef, spec, status, createdAt, updatedAt string
	var rv int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, management_mode, profile_ref, spec, status, resource_version, created_at, updated_at
		 FROM machines WHERE name = ?`, name).
		Scan(&id, &resourceName, &mode, &profileRef, &spec, &status, &rv, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: machine %q", domain.ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: get machine %q: %w", name, err)
	}
	m := &domain.Machine{}
	if err := fillMeta(&m.Metadata, id, resourceName, rv, createdAt); err != nil {
		return nil, err
	}
	m.Spec, m.Status = json.RawMessage(spec), json.RawMessage(status)
	return m, nil
}

func (s *machineStore) List(ctx context.Context) ([]*domain.Machine, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, management_mode, profile_ref, spec, status, resource_version, created_at, updated_at
		 FROM machines ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list machines: %w", err)
	}
	defer rows.Close()

	var out []*domain.Machine
	for rows.Next() {
		var id, name, mode, profileRef, spec, status, createdAt, updatedAt string
		var rv int64
		if err := rows.Scan(&id, &name, &mode, &profileRef, &spec, &status, &rv, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan machines: %w", err)
		}
		m := &domain.Machine{}
		if err := fillMeta(&m.Metadata, id, name, rv, createdAt); err != nil {
			return nil, err
		}
		m.Spec, m.Status = json.RawMessage(spec), json.RawMessage(status)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *machineStore) Update(ctx context.Context, m *domain.Machine) error {
	meta := &m.Metadata
	if meta.Name == "" {
		return fmt.Errorf("%w: metadata.name is required", domain.ErrInvalid)
	}
	if len(m.Spec) == 0 {
		m.Spec = json.RawMessage("{}")
	}
	if len(m.Status) == 0 {
		m.Status = json.RawMessage("{}")
	}
	core, err := s.core(m.Spec)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin update machine: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var rv int64
	err = tx.QueryRowContext(ctx,
		`SELECT resource_version FROM machines WHERE name = ?`, meta.Name).Scan(&rv)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: machine %q", domain.ErrNotFound, meta.Name)
	}
	if err != nil {
		return fmt.Errorf("sqlite: lock machine %q: %w", meta.Name, err)
	}
	meta.ResourceVersion = rv + 1
	if _, err := tx.ExecContext(ctx,
		`UPDATE machines
		 SET management_mode = ?, profile_ref = ?, spec = ?, status = ?, resource_version = ?, updated_at = ?
		 WHERE name = ?`,
		core.ManagementMode, core.ProfileRef, string(m.Spec), string(m.Status),
		meta.ResourceVersion, time.Now().UTC().Format(time.RFC3339Nano), meta.Name); err != nil {
		return fmt.Errorf("sqlite: update machine %q: %w", meta.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit update machine: %w", err)
	}
	return nil
}

func (s *machineStore) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM machines WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("sqlite: delete machine %q: %w", name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: machine %q", domain.ErrNotFound, name)
	}
	return nil
}

// operationStore 实现 Operation 的最小仓储。互斥以 operations 表的部分唯一索引
// 表达（架构 §4.3/§4.9）：并发创建同一机器的第二个未决操作在库层被拒绝。
type operationStore struct {
	db *sql.DB
}

func (s *operationStore) Create(ctx context.Context, op *domain.Operation) error {
	meta := &op.Metadata
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
	meta.UID = ids.NewUID()
	meta.ResourceVersion = 1
	now := time.Now().UTC()
	meta.CreationTimestamp = now
	stamp := now.Format(time.RFC3339Nano)
	specJSON, _ := json.Marshal(op.Spec)
	statusJSON, _ := json.Marshal(op.Status)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO operations (id, machine_id, op_type, transport, desired_generation, phase, spec, status, resource_version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.UID, op.Spec.Machine, op.Spec.Type, op.Spec.Transport, op.Spec.DesiredGeneration,
		op.Status.Phase, string(specJSON), string(statusJSON), meta.ResourceVersion, stamp, stamp)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: operations.machine_id") {
		// operations 表唯一涉及 machine_id 的约束只有 §4.3 的机器级互斥部分索引，
		// SQLite 对部分唯一索引冲突按列名报告，故以此识别。
		return fmt.Errorf("%w: %s", domain.ErrMachineBusy, op.Spec.Machine)
	}
	return mapConstraintErr(err, "operations")
}

func (s *operationStore) ListByMachine(ctx context.Context, machine string) ([]*domain.Operation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, machine_id, op_type, transport, desired_generation, phase, spec, status, resource_version, created_at, updated_at
		 FROM operations WHERE machine_id = ? ORDER BY created_at`, machine)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list operations for %q: %w", machine, err)
	}
	defer rows.Close()

	var out []*domain.Operation
	for rows.Next() {
		var id, machineID, opType, transport, phase, specJSON, statusJSON, createdAt, updatedAt string
		var generation, rv int64
		if err := rows.Scan(&id, &machineID, &opType, &transport, &generation, &phase,
			&specJSON, &statusJSON, &rv, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan operations: %w", err)
		}
		op := &domain.Operation{}
		if err := fillMeta(&op.Metadata, id, machineID, rv, createdAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(specJSON), &op.Spec); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal operation spec: %w", err)
		}
		if err := json.Unmarshal([]byte(statusJSON), &op.Status); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal operation status: %w", err)
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

func (s *operationStore) UpdatePhase(ctx context.Context, id, phase string) error {
	statusJSON, _ := json.Marshal(domain.OperationStatus{Phase: phase})
	res, err := s.db.ExecContext(ctx,
		`UPDATE operations SET phase = ?, status = ?, resource_version = resource_version + 1, updated_at = ?
		 WHERE id = ?`, phase, string(statusJSON), time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return fmt.Errorf("sqlite: update operation %s phase: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: operation %q", domain.ErrNotFound, id)
	}
	return nil
}
