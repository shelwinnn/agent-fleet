package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// createDeployment 把 REST 的 POST /api/v1/deployments 接到 Deployment 控制器创建路径。
// 未装配控制器时（部分单元测试）退回直接落库，保持装配前的行为。
func (s *Server) createDeployment(ctx context.Context, d *domain.Deployment) error {
	if s.deploys == nil {
		s.log.Warn("deployment controller not wired; creating deployment without targets")
		return s.deployments.Create(ctx, d)
	}
	return s.deploys.Create(ctx, d)
}

// attachDeploymentTargets 把 deployment_targets 的逐机推进状态并入
// Deployment.status.targets（§6.1 的 DeploymentStatus 定义、FR-14.5 第 3 组：
// Superseded/Skipped 必须与 Failed 区分显示且各带原因）。
//
// 为什么在 HTTP 层补齐：目标的权威存储在 deployment_targets 表（§10.1），控制器
// 逐机更新它；而上层只需要 §6.1 规定的资源形态。放在读取路径上，既不改存储模型，
// 也不引入"用户能写 status"的旁路（PATCH/PUT 仍以 spec 为限）。
func (s *Server) attachDeploymentTargets(ctx context.Context, d *domain.Deployment) error {
	if s.cfg.DeploymentTargets == nil {
		return nil
	}
	targets, err := s.cfg.DeploymentTargets(ctx, d.Metadata.Name)
	if err != nil {
		return err
	}
	status := domain.DeploymentStatus{}
	if raw := d.StatusJSON(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &status); err != nil {
			return fmt.Errorf("%w: deployment status: %v", domain.ErrInvalid, err)
		}
	}
	if targets == nil {
		targets = []domain.DeploymentTargetStatus{}
	}
	status.Targets = targets
	out, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("httpapi: marshal deployment status: %w", err)
	}
	d.SetStatusJSON(out)
	return nil
}
