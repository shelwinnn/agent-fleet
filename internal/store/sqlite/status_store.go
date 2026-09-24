package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// machineStatusStore 是 Machine 控制器写 machines.status 的专用通道
// （架构 §4.2：Machine 控制器维护 conditions 与观测字段）。读改写处于同一
// IMMEDIATE 事务，配合有界重试规避写竞争（KM-21 核查发现 #2/§12.1）。
type machineStatusStore struct {
	db *sql.DB
	// changes 写成功后发布变更（§23.6 SSE 事件源）。
	changes *changeNotifier
}

func NewMachineStatusStore(db *DB) domain.MachineStatusRepository {
	return &machineStatusStore{db: db.sql, changes: db.changes}
}

func (s *machineStatusStore) UpdateStatus(ctx context.Context, machine string, mutate func(*domain.MachineStatus) error) error {
	return withBusyRetry(ctx, "update_machine_status:"+machine, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("sqlite: begin status update %q: %w", machine, err)
		}
		defer func() { _ = tx.Rollback() }()

		var statusRaw string
		var rv int64
		err = tx.QueryRowContext(ctx,
			`SELECT status, resource_version FROM machines WHERE name = ?`, machine).
			Scan(&statusRaw, &rv)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: machine %q", domain.ErrNotFound, machine)
		}
		if err != nil {
			return fmt.Errorf("sqlite: lock machine %q status: %w", machine, err)
		}
		st, err := domain.ParseMachineStatus([]byte(statusRaw))
		if err != nil {
			return err
		}
		if err := mutate(&st); err != nil {
			return err
		}
		newJSON, err := st.JSON()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE machines SET status = ?, resource_version = resource_version + 1, updated_at = ?
			 WHERE name = ?`,
			string(newJSON), nowStamp(), machine); err != nil {
			return fmt.Errorf("sqlite: update machine %q status: %w", machine, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		// 状态写入同样是一次可见变更（conditions/新鲜度/未决操作都走这里），
		// 事件版本 = 事务内读到的 rv + 1（与上面的 UPDATE 一致）。
		s.changes.emit(resourceMachines, machine, rv+1)
		return nil
	})
}
