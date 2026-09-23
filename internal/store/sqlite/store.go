package sqlite

import (
	"database/sql"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 各仓储构造器：接口在 domain 定义，SQLite 实现可替换（§4.9 / AD-2）。
// changes 为写成功后的变更通知槽（SSE 事件枢纽，§8.1/§23.6）。

func NewMachineStore(db *DB) domain.MachineRepository {
	return &machineStore{db: db.sql, changes: db.changes}
}

func NewProfileStore(db *DB) domain.ProfileRepository {
	return newResourceStore[domain.AgentProfile](db.sql, "profiles", db.changes)
}

func NewProviderStore(db *DB) domain.ProviderRepository {
	return newResourceStore[domain.ModelProvider](db.sql, "providers", db.changes)
}

func NewSkillStore(db *DB) domain.SkillRepository {
	return newResourceStore[domain.Skill](db.sql, "skills", db.changes)
}

func NewDeploymentStore(db *DB) domain.DeploymentRepository {
	return newResourceStore[domain.Deployment](db.sql, "deployments", db.changes)
}

// newResourceStore 具象化泛型仓储（T 为资源结构体，PT 为其指针类型）。
func newResourceStore[T any, PT objectOf[T]](db *sql.DB, table string, changes *changeNotifier) *resourceStore[T, PT] {
	return &resourceStore[T, PT]{db: db, table: table, changes: changes}
}
