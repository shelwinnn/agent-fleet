// Package machine 实现 Machine 控制器的本片职责（架构 v1.1.2 §4.2）：
// 条件写入纪律（每个 condition 恰有一个置位方；本控制器写
// SSHReachable 之外的 AgentConnected / InventoryReady——SSHReachable 属 SSH 探测编排，
// 随 SSH 切片引入）、心跳与离线阈值扫描、inventory 观测落库与条件置位。
package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// Controller 是 Machine 控制器（本片子集）。
type Controller struct {
	machines domain.MachineRepository
	status   domain.MachineStatusRepository
	observed domain.ObservedStateRepository
	certs    domain.AgentCertificateRepository
	log      *slog.Logger
}

func NewController(
	machines domain.MachineRepository,
	status domain.MachineStatusRepository,
	observed domain.ObservedStateRepository,
	certs domain.AgentCertificateRepository,
	log *slog.Logger,
) *Controller {
	if log == nil {
		log = slog.Default()
	}
	return &Controller{machines: machines, status: status, observed: observed, certs: certs, log: log}
}

// OnConnected 在 Connect 流建立时调用（§4.2 条件表：
// "Connect 流建立 → AgentConnected=True"，由 gRPC 连接注册表驱动）。
func (c *Controller) OnConnected(ctx context.Context, machineName, agentdVersion string) error {
	now := time.Now().UTC()
	return c.update(ctx, machineName, "on_connected", func(st *domain.MachineStatus) {
		if agentdVersion != "" {
			st.AgentdVersion = agentdVersion
		}
		st.LastHeartbeatAt = &now
		st.SetCondition(domain.ConditionAgentConnected, domain.ConditionTrue, "Connected",
			"agentd connect stream established", now)
	})
}

// OnDisconnected 在 Connect 流断开时调用（§4.2 条件表：
// "Connect 流断开 → AgentConnected=False，原因 AgentDisconnected"）。
func (c *Controller) OnDisconnected(ctx context.Context, machineName string) error {
	now := time.Now().UTC()
	return c.update(ctx, machineName, "on_disconnected", func(st *domain.MachineStatus) {
		st.SetCondition(domain.ConditionAgentConnected, domain.ConditionFalse, "AgentDisconnected",
			"agentd connect stream closed", now)
	})
}

// OnHeartbeat 处理心跳：刷新 lastHeartbeatAt，并在离线扫描误报/网络抖动恢复后
// 把 AgentConnected 拉回 True（§4.2：心跳超时置 False 可由后续心跳恢复）。
func (c *Controller) OnHeartbeat(ctx context.Context, machineName string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return c.update(ctx, machineName, "on_heartbeat", func(st *domain.MachineStatus) {
		st.LastHeartbeatAt = &at
		if cond, ok := st.GetCondition(domain.ConditionAgentConnected); !ok || cond.Status != domain.ConditionTrue {
			st.SetCondition(domain.ConditionAgentConnected, domain.ConditionTrue, "Heartbeat",
				"heartbeat received after disconnect", at)
		}
	})
}

// OnInventory 落库一份观测并维护 Machine status（§4.2 条件表：
// 收到有效 inventory → InventoryReady=True、lastInventoryAt/inventorySeq）。
// 观测有序性（FR-8.7）：seq 落后于已处理高水位的报文不改写任何条件。
func (c *Controller) OnInventory(ctx context.Context, rec domain.ObservedStateRecord, obs domain.ObservedState) error {
	accepted, err := c.observed.Store(ctx, &rec)
	if err != nil {
		return err
	}
	if !accepted {
		c.log.Warn("stale observed state dropped", "machine_id", rec.Machine,
			"seq", rec.InventorySeq, "reason_code", "StaleObservation")
		return nil
	}
	now := rec.RecordedAt
	return c.update(ctx, rec.Machine, "on_inventory", func(st *domain.MachineStatus) {
		st.InventorySeq = obs.InventorySeq
		st.LastInventoryAt = &now
		if obs.Machine != nil {
			st.OS = obs.Machine.OS
			st.Arch = obs.Machine.Arch
			st.Hostname = obs.Machine.Hostname
			st.HomeDir = obs.Machine.HomeDir
			st.KernelVersion = obs.Machine.KernelVersion
		}
		if _, ok := st.GetCondition(domain.ConditionInventoryReady); !ok {
			st.SetCondition(domain.ConditionInventoryReady, domain.ConditionTrue, "InventoryReceived",
				"first valid inventory received", now)
		}
	})
}

// ScanOffline 按 offlineAfter 扫描心跳超时（§4.2 职责 2：
// lastHeartbeatAt + offlineAfter(默认 45s) 过期 → AgentConnected=False(AgentDisconnected)）。
// 只处理 managementMode=agentd 的机器；SSH-only 机器从不建立 daemon 流，
// AgentConnected 保持 Unknown/未写入（§6.2）。
func (c *Controller) ScanOffline(ctx context.Context, offlineAfter time.Duration, now time.Time) error {
	list, err := c.machines.List(ctx)
	if err != nil {
		return fmt.Errorf("machine controller: list machines: %w", err)
	}
	for _, m := range list {
		var core domain.MachineCoreSpec
		if len(m.SpecJSON()) != 0 {
			if err := json.Unmarshal(m.SpecJSON(), &core); err != nil {
				c.log.Error("machine spec unparseable", "machine_id", m.Metadata.Name, "err", err)
				continue
			}
		}
		// spec.managementMode 缺省按 agentd（§6.1/MachineCoreSpec 语义），
		// 仅 SSH-only 机器不参与心跳超时扫描。
		if core.ManagementMode != "" && core.ManagementMode != domain.ManagementModeAgentd {
			continue
		}
		st, err := domain.ParseMachineStatus(m.StatusJSON())
		if err != nil {
			c.log.Error("machine status unparseable", "machine_id", m.Metadata.Name, "err", err)
			continue
		}
		if st.LastHeartbeatAt == nil || now.Sub(*st.LastHeartbeatAt) <= offlineAfter {
			continue
		}
		cond, ok := st.GetCondition(domain.ConditionAgentConnected)
		if !ok || cond.Status != domain.ConditionTrue {
			continue // 已离线/从未连接，不重复写。
		}
		err = c.update(ctx, m.Metadata.Name, "scan_offline", func(st *domain.MachineStatus) {
			st.SetCondition(domain.ConditionAgentConnected, domain.ConditionFalse, "AgentDisconnected",
				fmt.Sprintf("no heartbeat for %s (offlineAfter=%s)",
					now.Sub(*st.LastHeartbeatAt).Round(time.Second), offlineAfter), now)
		})
		if err != nil {
			c.log.Error("offline scan update failed", "machine_id", m.Metadata.Name, "err", err)
		} else {
			c.log.Info("machine marked offline", "machine_id", m.Metadata.Name,
				"condition", domain.ConditionAgentConnected, "status", domain.ConditionFalse,
				"reason", "AgentDisconnected", "last_heartbeat_at", st.LastHeartbeatAt.Format(time.RFC3339))
		}
	}
	return nil
}

// OnMachineDeleted 在机器删除时退役其全部证书（§4.6 v1.1.2 取舍 (a)：
// 删除机器即拒绝其证书的后续 Connect；MVP 无 CRL/OCSP，retired_at 是审计事实）。
func (c *Controller) OnMachineDeleted(ctx context.Context, machineName string) error {
	if err := c.certs.RetireByMachine(ctx, machineName, time.Now().UTC()); err != nil {
		return fmt.Errorf("machine controller: retire certificates for %q: %w", machineName, err)
	}
	c.log.Info("machine deleted; certificates retired", "machine_id", machineName,
		"event", "machine_deleted_certificates_retired")
	return nil
}

func (c *Controller) update(ctx context.Context, machineName, op string, mutate func(*domain.MachineStatus)) error {
	err := c.status.UpdateStatus(ctx, machineName, func(st *domain.MachineStatus) error {
		mutate(st)
		return nil
	})
	if err != nil {
		c.log.Error("machine status update failed", "op", op, "machine_id", machineName, "err", err)
	}
	return err
}
