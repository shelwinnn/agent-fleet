package sqlite

import (
	"fmt"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// mapConstraintErr 把 SQLite 唯一约束错误映射为领域哨兵错误。
// 各表的 name 唯一索引冲突 → ErrAlreadyExists；
// operations 的机器级互斥索引冲突 → ErrMachineBusy（§4.3，API 层后续映射 409 MachineBusy）。
func mapConstraintErr(err error, table string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed") {
		if strings.Contains(msg, "ux_operations_machine_unresolved") {
			return domain.ErrMachineBusy
		}
		return fmt.Errorf("%w: %s name already exists", domain.ErrAlreadyExists, table)
	}
	return fmt.Errorf("sqlite: write %s: %w", table, err)
}
