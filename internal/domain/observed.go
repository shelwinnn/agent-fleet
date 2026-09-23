package domain

// ObservedState 是节点上报的观测状态（§8.2/§5.2；本片：机器信息 + Agent 实例占位）。
// 领域层中性类型：由 server/grpcagent 从 fleetv1 proto 转换而来，
// 以 JSON 存入 observed_states.payload 并驱动 Machine status 更新。
type ObservedState struct {
	InventorySeq int64  `json:"inventorySeq"`
	Full         bool   `json:"full"`
	OperationID  string `json:"operationId,omitempty"`

	Machine *MachineObservation `json:"machine,omitempty"`
	Agents  []AgentObservation  `json:"agents,omitempty"`

	// 投影规范化版本与双侧投影摘要（ADR-1；本片占位，真实值随第 4 片适配器）。
	CanonicalizationVersion  string `json:"canonicalizationVersion,omitempty"`
	DesiredProjectionDigest  string `json:"desiredProjectionDigest,omitempty"`
	ObservedProjectionDigest string `json:"observedProjectionDigest,omitempty"`
}

// MachineObservation 是节点机器信息（§6.1 Machine status 的 os/arch/hostname/homeDir）。
type MachineObservation struct {
	OS            string `json:"os,omitempty"`
	Arch          string `json:"arch,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	HomeDir       string `json:"homeDir,omitempty"`
	KernelVersion string `json:"kernelVersion,omitempty"`
	CPUCores      int    `json:"cpuCores,omitempty"`
	TotalMemBytes int64  `json:"totalMemBytes,omitempty"`
}

// AgentObservation 是节点上一个 Agent 实例（本片为占位：仅 agentd 自身；
// 真实家族实例随第 4 片适配器接入）。
type AgentObservation struct {
	Family     string `json:"family"`
	Version    string `json:"version,omitempty"`
	Enabled    bool   `json:"enabled"`
	ConfigPath string `json:"configPath,omitempty"`
}
