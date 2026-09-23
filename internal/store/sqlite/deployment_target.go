package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// deploymentTargetStore 实现 deployment_targets（架构 §10.1）：每机推进状态、
// 门禁绑定的 operationId、回滚物化的有效目标代与 Superseded/Skipped 原因。
type deploymentTargetStore struct {
	db *sql.DB
}

func NewDeploymentTargetStore(db *DB) domain.DeploymentTargetRepository {
	return &deploymentTargetStore{db: db.sql}
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
		return tx.Commit()
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
	res, err := s.db.ExecContext(ctx,
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
	return nil
}
