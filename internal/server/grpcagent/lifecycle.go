// 生命周期通道接线（KM-49）：控制面 ↔ 节点的生命周期消息双向转换与派发。
// 协议契约见 api/proto/fleet/v1/lifecycle.proto（KM-47）；本文件只做转换，
// 编排语义在 internal/controller/lifecycle，执行语义在 agentd 侧。
package grpcagent

import (
	"context"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	lifecyclectl "github.com/shelwinnn/agent-fleet/internal/controller/lifecycle"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// LifecycleResultSink 是控制面对节点生命周期结果上报的处理入口
//（由 lifecycle.Controller 实现；与 ResultSink 同一双接线模式）。
type LifecycleResultSink interface {
	OnLifecycleResult(ctx context.Context, msg lifecyclectl.ResultMsg) error
}

// SetLifecycleSink 装配生命周期结果回灌入口（main 装配；与 SetResultSink 成对）。
func (s *Server) SetLifecycleSink(sink LifecycleResultSink) { s.lifecycleSink = sink }

// DispatchLifecycle 实现 lifecyclectl.Dispatcher：经该机活跃 Connect 流下发
// ExecuteLifecycleOperation（确认后的固定版本意图）。无活跃流返回
// ErrAgentDisconnected（编排层据此标记 not_executed(dispatch-failed)）。
func (s *Server) DispatchLifecycle(_ context.Context, machine string, req lifecyclectl.DispatchRequest) error {
	targets := make([]*fleetv1.LifecycleTarget, len(req.Targets))
	for i, t := range req.Targets {
		targets[i] = lifecycleTargetToProto(t)
	}
	msg := &fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_ExecuteLifecycleOperation{
		ExecuteLifecycleOperation: &fleetv1.ExecuteLifecycleOperation{
			Spec: &fleetv1.LifecycleOperationSpec{
				OpId:           req.OpID,
				Type:           req.Type,
				IdempotencyKey: req.IdempotencyKey,
				Version:        req.Version,
				Targets:        targets,
				CreatedBy:      req.CreatedBy,
			},
			RediscoveryRequired: req.RediscoveryRequired,
			DeadlineUnix:        req.Deadline.Unix(),
		},
	}}
	return s.dispatch(machine, msg)
}

func lifecycleTargetToProto(t domain.LifecycleTarget) *fleetv1.LifecycleTarget {
	return &fleetv1.LifecycleTarget{
		MachineId:        t.Machine,
		Env:              t.Env,
		User:             t.User,
		Agent:            t.Agent,
		Method:           t.Method,
		Package:          t.Package,
		Os:               t.OS,
		Arch:             t.Arch,
		CapabilityState:  t.CapabilityState,
		CapabilityReason: t.CapabilityReason,
	}
}

// lifecycleResultFromProto 把节点上报转换为控制器载荷（目标身份逐段还原，
// 键由域模型重建——两端同构，防漂移）。
func lifecycleResultFromProto(machine string, res *fleetv1.LifecycleOperationResult) lifecyclectl.ResultMsg {
	msg := lifecyclectl.ResultMsg{
		Machine:             machine,
		OpID:                res.GetOpId(),
		IdempotencyKey:      res.GetIdempotencyKey(),
		NeedsReconfirmation: res.GetNeedsReconfirmation(),
		ReconfirmReason:     res.GetReconfirmReason(),
	}
	if ts := res.GetFinishedAtUnix(); ts > 0 {
		msg.FinishedAt = time.Unix(ts, 0).UTC()
	}
	for _, r := range res.GetResults() {
		out := domain.LifecycleTargetResult{
			Target: domain.LifecycleTarget{
				Machine: r.GetTarget().GetMachineId(),
				Env:     r.GetTarget().GetEnv(),
				User:    r.GetTarget().GetUser(),
				Agent:   r.GetTarget().GetAgent(),
				Method:  r.GetTarget().GetMethod(),
				Package: r.GetTarget().GetPackage(),
			},
			State:   r.GetState(),
			Reason:  r.GetReason(),
			Message: r.GetMessage(),
		}
		if ev := r.GetEvidence(); ev != nil {
			out.Evidence = &domain.LifecycleEvidence{
				ObservedVersion: ev.GetObservedVersion(),
				ExecutablePath:  ev.GetExecutablePath(),
				Method:          ev.GetMethod(),
				InventorySeq:    ev.GetInventorySeq(),
				Message:         ev.GetMessage(),
			}
			if ts := ev.GetRecordedAtUnix(); ts > 0 {
				out.Evidence.RecordedAt = time.Unix(ts, 0).UTC()
			}
		}
		msg.Results = append(msg.Results, out)
	}
	return msg
}
