package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// deploymentTargetStore 实现 deployment_targets（架构 §10.1）：每机推进状态、
// 门禁绑定的 operationId、回滚物化的有效目标代与 Superseded/Skipped 原因。
//
// 目标行写入同时递增所属 deployment 的 resource_version 并发变更事件：目标的
// phase/reason 是 deployments 资源的对外状态（§6.1），UI 需要它才能按 revision
// 选择性重取（§23.6）——否则目标行变化对 SSE 订阅者不可见。
type deploymentTargetStore struct {
	db      *sql.DB
	changes *changeNotifier
}

func NewDeploymentTargetStore(db *DB) domain.DeploymentTargetRepository {
	return &deploymentTargetStore{db: db.sql, changes: db.changes}
}

// bumpDeployment 在目标行写入的同一事务内递增所属 deployment 的版本，返回新版本
// 供事件发布（deployment 不存在时返回 0，不发布）。
func bumpDeployment(ctx context.Context, tx *sql.Tx, deployment string) (int64, error) {
	var rv int64
	err := tx.QueryRowContext(ctx, `SELECT resource_version FROM deployments WHERE name = ?`, deployment).Scan(&rv)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("sqlite: lock deployment %q: %w", deployment, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE deployments SET resource_version = ?, updated_at = ? WHERE name = ?`,
		rv+1, nowStamp(), deployment); err != nil {
		return 0, fmt.Errorf("sqlite: bump deployment %q: %w", deployment, err)
	}
	return rv + 1, nil
}

func (s *deploymentTargetStore) Replace(ctx context.Context, deployment string, targets []domain.DeploymentTargetStatus) error {
	return withBusyRetry(ctx, "replace_targets:"+deployment, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin replace targets %s: %w", deployment, err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `DELETE FROM deployment_targets WHERE deployment_id = ?`, deployment); err != nil {
			return fmt.Errorf("sqlite: clear targets %s: %w", deployment, err)
		}
		for _, t := range targets {
			if err := insertTarget(ctx, tx, deployment, t); err != nil {
				return err
			}
		}
		rv, err := bumpDeployment(ctx, tx, deployment)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if rv > 0 {
			s.changes.emit(resourceDeployments, deployment, rv)
		}
		return nil
	})
}

func insertTarget(ctx context.Context, tx *sql.Tx, deployment string, t domain.DeploymentTargetStatus) error {
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO deployment_targets (deployment_id, machine_id, phase, reason, operation_id, effective_generation, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		deployment, t.Machine, t.Phase, t.Reason, t.OperationID, t.EffectiveGeneration,
		t.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("sqlite: insert target %s/%s: %w", deployment, t.Machine, err)
	}
	return nil
}

func (s *deploymentTargetStore) List(ctx context.Context, deployment string) ([]domain.DeploymentTargetStatus, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT machine_id, phase, reason, operation_id, effective_generation, updated_at
		 FROM deployment_targets WHERE deployment_id = ? ORDER BY machine_id`, deployment)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list targets %s: %w", deployment, err)
	}
	defer rows.Close()
	var out []domain.DeploymentTargetStatus
	for rows.Next() {
		var t domain.DeploymentTargetStatus
		var updatedAt string
		if err := rows.Scan(&t.Machine, &t.Phase, &t.Reason, &t.OperationID, &t.EffectiveGeneration, &updatedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan targets: %w", err)
		}
		if t.UpdatedAt, err = parseTime(updatedAt, "target.updated_at"); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *deploymentTargetStore) Update(ctx context.Context, deployment string, t domain.DeploymentTargetStatus) error {
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin update target %s/%s: %w", deployment, t.Machine, err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`UPDATE deployment_targets SET phase = ?, reason = ?, operation_id = ?, effective_generation = ?, updated_at = ?
		 WHERE deployment_id = ? AND machine_id = ?`,
		t.Phase, t.Reason, t.OperationID, t.EffectiveGeneration,
		t.UpdatedAt.Format(time.RFC3339Nano), deployment, t.Machine)
	if err != nil {
		return fmt.Errorf("sqlite: update target %s/%s: %w", deployment, t.Machine, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: deployment target %s/%s", domain.ErrNotFound, deployment, t.Machine)
	}
	rv, err := bumpDeployment(ctx, tx, deployment)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if rv > 0 {
		s.changes.emit(resourceDeployments, deployment, rv)
	}
	return nil
}
