package domain

import "context"

// Repository 是名称在类型内唯一的资源的仓储接口（§4.9：接口定义于领域层，SQLite 实现可替换）。
// T 为资源指针类型（如 *Machine），满足 ResourceObject。
type Repository[T ResourceObject] interface {
	Create(ctx context.Context, obj T) error
	Get(ctx context.Context, name string) (T, error)
	List(ctx context.Context) ([]T, error)
	Update(ctx context.Context, obj T) error
	Delete(ctx context.Context, name string) error
}

// MachineRepository、ProfileRepository 为本切片 API 实际消费的两个仓储；
// Provider/Skill/Deployment 的仓储随对应切片的 API 一同启用。
type (
	MachineRepository    Repository[*Machine]
	ProfileRepository    Repository[*AgentProfile]
	ProviderRepository   Repository[*ModelProvider]
	SkillRepository      Repository[*Skill]
	DeploymentRepository Repository[*Deployment]
)

// OperationRepository 是 Operation 审计单元的最小仓储（本切片供互斥索引测试使用；
// 完整状态机与派发语义在 reconcile 切片实现）。
type OperationRepository interface {
	Create(ctx context.Context, op *Operation) error
	ListByMachine(ctx context.Context, machine string) ([]*Operation, error)
	// UpdatePhase 迁移相位并递增 resource_version。相位合法性由调用方保证。
	UpdatePhase(ctx context.Context, id, phase string) error
}
