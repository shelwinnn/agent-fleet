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

// 各资源仓储：接口在领域层，SQLite 实现可替换（§4.9 / AD-2）。
type (
	MachineRepository    Repository[*Machine]
	ProfileRepository    Repository[*AgentProfile]
	ProviderRepository   Repository[*ModelProvider]
	SkillRepository      Repository[*Skill]
	DeploymentRepository Repository[*Deployment]
)
