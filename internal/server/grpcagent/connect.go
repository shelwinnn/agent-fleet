package grpcagent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Connect 是唯一的逻辑长连接（§5.2）：
//   - 首消息必须是 Hello（身份与协议版本校验），服务端回 Welcome；
//   - 随后处理 Heartbeat / ObservedState / OperationResult 等上报；
//   - 同一 machineId 的第二条流被拒绝并产生告警事件（FR-11.6）；
//   - 流断开时 AgentConnected=False(AgentDisconnected)。
//
// 重复 ExecuteOperation 幂等、outbox 重发等随第 3 片的执行流水线接入。
func (s *Server) Connect(stream grpc.BidiStreamingServer[fleetv1.AgentToServer, fleetv1.ServerToAgent]) error {
	ctx := stream.Context()
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return status.Error(codes.PermissionDenied, "client certificate required")
	}
	machine := id.Machine

	if !s.registry.tryRegister(machine, id.Serial) {
		// FR-11.6：拒绝并告警——顶替语义会把安全事件伪装成网络抖动。
		s.log.Warn("duplicate connect stream rejected", "event", "duplicate_connect_rejected",
			"machine_id", machine, "reason_code", "AgentDisconnected")
		return status.Error(codes.FailedPrecondition,
			"another active Connect stream exists for this machine")
	}
	s.log.Info("agent connect stream opened", "machine_id", machine, "cert_serial", id.Serial)
	defer func() {
		s.registry.unregister(machine, id.Serial)
		if err := s.ctrl.OnDisconnected(context.WithoutCancel(ctx), machine); err != nil {
			s.log.Error("mark disconnected failed", "machine_id", machine, "err", err)
		}
		s.log.Info("agent connect stream closed", "machine_id", machine)
	}()

	// 首消息必须是 Hello（§5.2：重连必以 Hello 开始）。
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be Hello")
	}
	if hello.GetMachineId() != machine {
		return status.Error(codes.PermissionDenied, "hello machineId does not match certificate identity")
	}
	if hello.GetProtocolVersion() != ProtocolVersionV1 {
		// §7.6：拒绝不支持的主版本；AgentConnected 保持 False，不下发期望状态。
		s.log.Warn("protocol version mismatch", "machine_id", machine,
			"got", hello.GetProtocolVersion(), "want", ProtocolVersionV1,
			"reason_code", "ProtocolVersionMismatch")
		return status.Error(codes.FailedPrecondition, "unsupported protocol version")
	}
	if err := s.ctrl.OnConnected(ctx, machine, hello.GetAgentdVersion()); err != nil {
		return mapStoreErr(err)
	}
	if err := stream.Send(&fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_Welcome{
		Welcome: &fleetv1.Welcome{
			ProtocolVersion:          ProtocolVersionV1,
			HeartbeatIntervalSeconds: int32(s.cfg.heartbeatInterval().Seconds()),
			InventoryIntervalSeconds: int32(s.cfg.inventoryInterval().Seconds()),
		},
	}}); err != nil {
		return err
	}

	// 消息循环：仅处理本片范围内的上报；执行流水线随第 3 片接入。
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err // EOF/取消即正常断开；其余原样返回（agentd 指数退避重连）。
		}
		switch payload := msg.GetPayload().(type) {
		case *fleetv1.AgentToServer_Heartbeat:
			if err := s.ctrl.OnHeartbeat(ctx, machine, time.Now().UTC()); err != nil {
				s.log.Error("heartbeat update failed", "machine_id", machine, "err", err)
			}
		case *fleetv1.AgentToServer_ObservedState:
			obs, rec := observedFromProto(machine, payload.ObservedState)
			if err := s.ctrl.OnInventory(ctx, rec, obs); err != nil {
				s.log.Error("inventory update failed", "machine_id", machine, "seq", obs.InventorySeq, "err", err)
			} else {
				logAttrs := []any{"machine_id", machine, "inventory_seq", obs.InventorySeq, "full", obs.Full}
				if obs.Machine != nil {
					logAttrs = append(logAttrs, "os", obs.Machine.OS, "arch", obs.Machine.Arch)
				}
				s.log.Info("inventory received", logAttrs...)
			}
		case *fleetv1.AgentToServer_OperationResult:
			// §8.2：服务端幂等处理重复结果并回 Ack；结果记录与状态机随第 3 片。
			res := payload.OperationResult
			s.log.Info("operation result received (recording lands with slice 3)", "machine_id", machine,
				"operation_id", res.GetOperationId(), "phase", res.GetPhase())
			if err := stream.Send(&fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_OperationResultAck{
				OperationResultAck: &fleetv1.OperationResultAck{OperationId: res.GetOperationId()},
			}}); err != nil {
				return err
			}
		case *fleetv1.AgentToServer_OperationStarted:
			s.log.Info("operation started", "machine_id", machine,
				"operation_id", payload.OperationStarted.GetOperationId())
		case *fleetv1.AgentToServer_OperationProgress:
			p := payload.OperationProgress
			s.log.Info("operation progress", "machine_id", machine,
				"operation_id", p.GetOperationId(), "step", p.GetStep(), "phase", p.GetPhase())
		case *fleetv1.AgentToServer_LogEvent:
			// 仅运维日志（spec §11.3）；限长与脱敏在输出侧统一处理。
			s.log.Info("agent log", "machine_id", machine,
				"level", payload.LogEvent.GetLevel(), "message", truncate(payload.LogEvent.GetMessage(), 512))
		default:
			s.log.Warn("unknown agent message ignored", "machine_id", machine)
		}
	}
}

// observedFromProto 把 proto 观测转换为领域类型与入库记录。
func observedFromProto(machine string, m *fleetv1.ObservedState) (domain.ObservedState, domain.ObservedStateRecord) {
	obs := domain.ObservedState{
		InventorySeq:             m.GetInventorySeq(),
		Full:                     m.GetFull(),
		OperationID:              m.GetOperationId(),
		CanonicalizationVersion:  m.GetCanonicalizationVersion(),
		DesiredProjectionDigest:  m.GetDesiredProjectionDigest(),
		ObservedProjectionDigest: m.GetObservedProjectionDigest(),
	}
	if mi := m.GetMachine(); mi != nil {
		obs.Machine = &domain.MachineObservation{
			OS:            mi.GetOs(),
			Arch:          mi.GetArch(),
			Hostname:      mi.GetHostname(),
			HomeDir:       mi.GetHomeDir(),
			KernelVersion: mi.GetKernel(),
			CPUCores:      int(mi.GetCpuCount()),
			TotalMemBytes: mi.GetTotalMemBytes(),
		}
	}
	for _, a := range m.GetAgents() {
		obs.Agents = append(obs.Agents, domain.AgentObservation{
			Family:     a.GetFamily(),
			Version:    a.GetVersion(),
			Enabled:    a.GetEnabled(),
			ConfigPath: a.GetConfigPath(),
		})
	}
	payload, _ := json.Marshal(obs)
	rec := domain.ObservedStateRecord{
		Machine:      machine,
		InventorySeq: obs.InventorySeq,
		OperationID:  obs.OperationID,
		Payload:      payload,
		RecordedAt:   time.Now().UTC(),
	}
	return obs, rec
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
