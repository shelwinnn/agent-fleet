package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
