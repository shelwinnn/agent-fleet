package domain

// ObservedState 是节点上报的观测状态（§8.2/§5.2：机器信息 + 家族实例 +
// ADR-1 双侧投影摘要 + 适配器健康）。
// 领域层中性类型：由 server/grpcagent 从 fleetv1 proto 转换而来，
// 以 JSON 存入 observed_states.payload 并驱动 Machine status 更新。
type ObservedState struct {
	InventorySeq int64  `json:"inventorySeq"`
	Full         bool   `json:"full"`
	OperationID  string `json:"operationId,omitempty"`

	Machine *MachineObservation `json:"machine,omitempty"`
	Agents  []AgentObservation  `json:"agents,omitempty"`

	// 投影规范化版本与双侧投影摘要（ADR-1；第 4 片起由家族适配器产出）。
	CanonicalizationVersion  string `json:"canonicalizationVersion,omitempty"`
	DesiredProjectionDigest  string `json:"desiredProjectionDigest,omitempty"`
	ObservedProjectionDigest string `json:"observedProjectionDigest,omitempty"`
	// AdapterHealth 是同一份观测的适配器健康结果（passed|failed|skipped）：
	// 门禁条件 4 接受"同一份观测（或该操作 verify 证据）"的健康结果（§4.4）。
	AdapterHealth string `json:"adapterHealth,omitempty"`
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

// AgentObservation 是节点上一个 Agent 实例（agentd 自身 + 各家族探测结果）。
type AgentObservation struct {
	Family     string `json:"family"`
	Version    string `json:"version,omitempty"`
	Enabled    bool   `json:"enabled"`
	ConfigPath string `json:"configPath,omitempty"`
	// Installed 区分"未安装"与"已安装但版本不可解析"（矩阵验收 1）。
	Installed bool `json:"installed,omitempty"`
	// VersionError 非空表示已安装但版本探测/解析失败（如实上报，不伪装成未安装）。
	VersionError string `json:"versionError,omitempty"`
}
