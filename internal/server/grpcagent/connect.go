package grpcagent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Connect 是唯一的逻辑长连接（§5.2）：
//   - 首消息必须是 Hello（身份与协议版本校验），服务端回 Welcome；
//   - 随后处理 Heartbeat / ObservedState / OperationStarted/Progress/Result 等上报；
//   - 下行派发（ExecuteOperation/CancelOperation）经注册表队列进入同一主循环
//     发送（单写者约束，P1-2 修复保持）；
//   - 同一 machineId 的第二条流被拒绝并产生告警事件（FR-11.6）；
//   - 流断开时 AgentConnected=False(AgentDisconnected)。
func (s *Server) Connect(stream grpc.BidiStreamingServer[fleetv1.AgentToServer, fleetv1.ServerToAgent]) error {
	ctx := stream.Context()
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return status.Error(codes.PermissionDenied, "client certificate required")
	}
	machine := id.Machine

	handle, ok := s.registry.tryRegister(machine, id.Serial)
	if !ok {
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

	// 接收 goroutine 只把上行消息投递到 channel；全部发送收敛在主循环
	//（gRPC 禁止不同 goroutine 并发 Send 同一 stream，P1-2）。
	type inbound struct {
		msg *fleetv1.AgentToServer
		err error
	}
	inboundCh := make(chan inbound, 16)
	go func() {
		defer close(inboundCh)
		for {
			msg, err := stream.Recv()
			if err != nil {
				select {
				case inboundCh <- inbound{err: err}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case inboundCh <- inbound{msg: msg}:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case in, open := <-inboundCh:
			if !open {
				return nil
			}
			if in.err != nil {
				// EOF/取消即正常断开；其余原样返回（agentd 指数退避重连）。
				if errors.Is(in.err, context.Canceled) {
					return nil
				}
				return in.err
			}
			if err := s.handleUpstream(ctx, machine, in.msg, stream); err != nil {
				return err
			}
		case msg := <-handle.dispatch:
			// 下行派发同样只在主循环发送（单写者）。
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// handleUpstream 处理一条上行消息。返回错误表示流必须终止（协议违规/发送失败）；
// 处理类错误只记日志，不打断流。
func (s *Server) handleUpstream(ctx context.Context, machine string, msg *fleetv1.AgentToServer,
	stream grpc.BidiStreamingServer[fleetv1.AgentToServer, fleetv1.ServerToAgent]) error {
	switch payload := msg.GetPayload().(type) {
	case *fleetv1.AgentToServer_Heartbeat:
		if err := s.ctrl.OnHeartbeat(ctx, machine, time.Now().UTC()); err != nil {
			s.log.Error("heartbeat update failed", "machine_id", machine, "err", err)
		}
	case *fleetv1.AgentToServer_ObservedState:
		obs, rec := observedFromProto(machine, payload.ObservedState)
		// 因果归属（§4.4/FR-8.7）：携带 operationId 的观测按该操作的目标代归属。
		if s.sink != nil {
			s.sink.AssignObservationGeneration(ctx, &rec)
		}
		if err := s.ctrl.OnInventory(ctx, rec, obs); err != nil {
			s.log.Error("inventory update failed", "machine_id", machine, "seq", obs.InventorySeq, "err", err)
		} else {
			logAttrs := []any{"machine_id", machine, "inventory_seq", obs.InventorySeq, "full", obs.Full}
			if obs.Machine != nil {
				logAttrs = append(logAttrs, "os", obs.Machine.OS, "arch", obs.Machine.Arch)
			}
			if obs.OperationID != "" {
				logAttrs = append(logAttrs, "operation_id", obs.OperationID)
			}
			s.log.Info("inventory received", logAttrs...)
			// 观测落库后按当前代求值 drift 三态（§4.2：Reconcile 控制器写
			// Drifted/Reconciled）。
			if s.sink != nil {
				if _, err := s.sink.EvaluateDrift(ctx, machine); err != nil {
					s.log.Error("drift evaluation failed", "machine_id", machine, "err", err)
				}
			}
		}
	case *fleetv1.AgentToServer_OperationStarted:
		opID := payload.OperationStarted.GetOperationId()
		if s.sink != nil {
			if err := s.sink.OnStarted(ctx, machine, opID); err != nil {
				s.log.Error("operation started handling failed", "operation_id", opID, "err", err)
			}
		}
	case *fleetv1.AgentToServer_OperationProgress:
		p := payload.OperationProgress
		if s.sink != nil {
			if err := s.sink.OnProgress(ctx, machine, p.GetOperationId(), p.GetStep(), p.GetPhase(), p.GetMessage()); err != nil {
				s.log.Error("operation progress handling failed", "operation_id", p.GetOperationId(), "err", err)
			}
		}
	case *fleetv1.AgentToServer_OperationResult:
		res := payload.OperationResult
		if s.sink == nil {
			s.log.Warn("operation result received but no result sink wired",
				"machine_id", machine, "operation_id", res.GetOperationId())
		} else {
			outcome, err := s.sink.OnResult(ctx, machine, OperationResultFromProto(res))
			if err != nil {
				s.log.Error("operation result handling failed", "machine_id", machine,
					"operation_id", res.GetOperationId(), "err", err)
			} else if outcome.Duplicate {
				s.log.Info("duplicate result deduped", "machine_id", machine, "operation_id", res.GetOperationId())
			}
		}
		// §8.2：处理完成后回 Ack；节点收到 Ack 才淘汰 outbox 项（FR-13.7）。
		if err := stream.Send(&fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_OperationResultAck{
			OperationResultAck: &fleetv1.OperationResultAck{OperationId: res.GetOperationId()},
		}}); err != nil {
			return err
		}
	case *fleetv1.AgentToServer_LogEvent:
		// 仅运维日志（spec §11.3）；限长与脱敏在输出侧统一处理。
		s.log.Info("agent log", "machine_id", machine,
			"level", payload.LogEvent.GetLevel(), "message", truncate(payload.LogEvent.GetMessage(), 512))
	default:
		s.log.Warn("unknown agent message ignored", "machine_id", machine)
	}
	return nil
}

// OperationResultFromProto 把 proto 结果转换为控制器载荷。
func OperationResultFromProto(res *fleetv1.OperationResult) reconcile.OperationResultMsg {
	msg := reconcile.OperationResultMsg{
		OperationID:      res.GetOperationId(),
		Phase:            res.GetPhase(),
		Reason:           res.GetReason(),
		Message:          res.GetMessage(),
		TerminalModifier: res.GetTerminalModifier(),
	}
	if v := res.GetVerify(); v != nil {
		msg.Verify = &domain.VerifyEvidence{
			DesiredProjectionDigest:  v.GetDesiredProjectionDigest(),
			ObservedProjectionDigest: v.GetObservedProjectionDigest(),
			CanonicalizationVersion:  v.GetCanonicalizationVersion(),
			InventorySeq:             v.GetInventorySeq(),
			AdapterHealth:            v.GetAdapterHealth(),
		}
	}
	if res.GetStartedAtUnix() > 0 {
		msg.StartedAt = time.Unix(res.GetStartedAtUnix(), 0).UTC()
	}
	if res.GetFinishedAtUnix() > 0 {
		msg.FinishedAt = time.Unix(res.GetFinishedAtUnix(), 0).UTC()
	}
	return msg
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
		AdapterHealth:            m.GetAdapterHealth(),
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
			Family:       a.GetFamily(),
			Version:      a.GetVersion(),
			Enabled:      a.GetEnabled(),
			ConfigPath:   a.GetConfigPath(),
			Installed:    a.GetInstalled(),
			VersionError: a.GetVersionError(),
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
