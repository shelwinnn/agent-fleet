package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// MachineStatus 是 machines.status JSON 的类型化视图（架构 v1.1.2 §6.1/§6.2，
// 本片维护的字段子集）。status 由 Machine 控制器独占写入；REST 更新不改写。
type MachineStatus struct {
	Conditions []Condition `json:"conditions,omitempty"`

	// agentd 通道与 inventory 观测（§6.1 Machine status 关键字段的本片子集）。
	ObservedGeneration int64      `json:"observedGeneration,omitempty"`
	OS                 string     `json:"os,omitempty"`
	Arch               string     `json:"arch,omitempty"`
	Hostname           string     `json:"hostname,omitempty"`
	HomeDir            string     `json:"homeDir,omitempty"`
	KernelVersion      string     `json:"kernelVersion,omitempty"`
	AgentdVersion      string     `json:"agentdVersion,omitempty"`
	LastHeartbeatAt    *time.Time `json:"lastHeartbeatAt,omitempty"`
	LastInventoryAt    *time.Time `json:"lastInventoryAt,omitempty"`
	InventorySeq       int64      `json:"inventorySeq,omitempty"`
}

// ParseMachineStatus 解析 status JSON（空/未初始化视为零值）。
func ParseMachineStatus(raw json.RawMessage) (MachineStatus, error) {
	var s MachineStatus
	if len(raw) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%w: status: %v", ErrInvalid, err)
	}
	return s, nil
}

// JSON 序列化为 status 列存储值。
func (s *MachineStatus) JSON() (json.RawMessage, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("domain: marshal machine status: %w", err)
	}
	return b, nil
}

// GetCondition 按类型取条件（不存在时 ok=false）。
func (s *MachineStatus) GetCondition(condType string) (Condition, bool) {
	for _, c := range s.Conditions {
		if c.Type == condType {
			return c, true
		}
	}
	return Condition{}, false
}

// SetCondition 写入一条完整条件记录（§22：status/reason/message/lastTransitionTime）。
// 仅当 status 取值变化时更新 lastTransitionTime；同一条件重复写入同一状态只刷新
// reason/message。条件间不互相推导，写入方由 §6.2 归属表约束。
func (s *MachineStatus) SetCondition(condType, status, reason, message string, now time.Time) {
	for i := range s.Conditions {
		if s.Conditions[i].Type != condType {
			continue
		}
		if s.Conditions[i].Status != status {
			s.Conditions[i].LastTransitionTime = now
		}
		s.Conditions[i].Status = status
		s.Conditions[i].Reason = reason
		s.Conditions[i].Message = message
		return
	}
	s.Conditions = append(s.Conditions, Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}
