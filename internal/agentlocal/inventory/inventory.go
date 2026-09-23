// Package inventory 实现节点观测采集（架构 v1.1.2 §3.6 agentlocal/inventory/、
// §7.1/ADR-1）。第 4 片起：真实家族适配器接入后，周期 inventory 必须对**当前期望
// 快照**同时计算期望侧与观测侧受管投影摘要，并随 ObservedState 一并上报；服务端
// 只存储与比较两个数。inventorySeq 为节点本地单调序（FR-8.7），持久化于
// state/last-observed.json，重启后不回退。
package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// lastObservedFile 记录最近观测的 inventorySeq（§5.2：用于断线恢复与单调性，
// 不是权威证据——权威观测由服务端绑定 operationId 保存）。
const lastObservedFile = "state/last-observed.json"

// desiredFile 保存最近一次下发的期望快照（§7.1：inventory 以"当前快照"为期望侧
// 输入；快照由 ExecuteOperation 自包含下发，FR-13.6）。
const desiredFile = "state/desired.json"

// Collector 采集本机观测。
type Collector struct {
	// DataDir 是节点数据目录（~/.local/share/agent-fleet）。
	DataDir string
	// AgentdVersion 上报于 agentd 自身实例。
	AgentdVersion string
	// Registry 是家族适配器注册表（nil 时只上报 agentd 自身，投影为空）。
	Registry *adapter.Registry
	// Home 是注入的 HOME 根（护栏 #12）。
	Home string

	// mu 串行化 Collect：inventorySeq 的推进是读-改-写（FR-8.7 单调性前提），
	// 并发采集会导致 seq 重复或回退。
	mu sync.Mutex
}

// NextSeq 暴露节点本地单调序（FR-8.7）给 reconciler：操作流水线的投影采集与
// 周期上报共用同一序列，保证服务端看到的 seq 全局单调不回退。
func (c *Collector) NextSeq() (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextSeq()
}

// AdapterFamilies 返回已注册家族（Hello 的能力协商字段，FR-13.4）。
func (c *Collector) AdapterFamilies() []string {
	if c.Registry == nil {
		return nil
	}
	return c.Registry.Families()
}

// SaveDesired 持久化最近一次收到的期望快照（ExecuteOperation 携带的完整快照）。
func (c *Collector) SaveDesired(snapshotJSON []byte) error {
	if len(snapshotJSON) == 0 {
		return nil
	}
	path := filepath.Join(c.DataDir, desiredFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("inventory: create state dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, snapshotJSON, 0o600); err != nil {
		return fmt.Errorf("inventory: write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

// LoadDesired 读取最近一次期望快照（无则 ok=false）。
func (c *Collector) LoadDesired() ([]byte, bool) {
	b, err := os.ReadFile(filepath.Join(c.DataDir, desiredFile))
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// Collect 产出一份周期全量观测（full=true）并递增持久化的 inventorySeq。
// 投影按最近一次期望快照计算；没有快照时双侧摘要为空（服务端据此置 Unknown，
// 不会把"从未采集到期望"误判为一致）。
func (c *Collector) Collect(ctx context.Context) (domain.ObservedState, error) {
	c.mu.Lock()
	seq, err := c.nextSeq()
	c.mu.Unlock()
	if err != nil {
		return domain.ObservedState{}, err
	}
	obs := domain.ObservedState{
		InventorySeq: seq,
		Full:         true,
		Machine:      c.machineInfo(),
		Agents:       c.agentInstances(ctx),
	}
	version, desired, observed := c.project(ctx)
	obs.CanonicalizationVersion = version
	obs.DesiredProjectionDigest = desired
	obs.ObservedProjectionDigest = observed
	return obs, nil
}

// CollectForOperation 产出与某操作绑定的 apply 后观测（§4.4 门禁条件 2/3/4 的
// 唯一可接受证据形态）：投影/序/健康直接取自该操作流水线的 verify 证据，
// 保证证据与操作同源。
func (c *Collector) CollectForOperation(opID string, ev *domain.VerifyEvidence) domain.ObservedState {
	obs := domain.ObservedState{
		InventorySeq: 0,
		Full:         true,
		OperationID:  opID,
		Machine:      c.machineInfo(),
	}
	if ev != nil {
		obs.InventorySeq = ev.InventorySeq
		obs.CanonicalizationVersion = ev.CanonicalizationVersion
		obs.DesiredProjectionDigest = ev.DesiredProjectionDigest
		obs.ObservedProjectionDigest = ev.ObservedProjectionDigest
		obs.AdapterHealth = ev.AdapterHealth
	}
	return obs
}

// project 对最近一次期望快照计算多家族聚合投影（ADR-1：单机单判据）。
// 返回 (canonicalizationVersion, desiredDigest, observedDigest)。
func (c *Collector) project(ctx context.Context) (string, string, string) {
	if c.Registry == nil || len(c.Registry.Families()) == 0 {
		return "", "", ""
	}
	raw, ok := c.LoadDesired()
	if !ok {
		return adapter.ProjectionCanonicalizationVersion, "", ""
	}
	snap, err := domain.ParseSnapshot(raw)
	if err != nil {
		return adapter.ProjectionCanonicalizationVersion, "", ""
	}
	states, err := adapter.DesiredStatesFromSnapshot(snap)
	if err != nil {
		return adapter.ProjectionCanonicalizationVersion, "", ""
	}
	desiredSet := map[string]string{}
	observedSet := map[string]string{}
	for _, family := range c.Registry.Families() {
		desired, ok := states[family]
		if !ok {
			continue // 该家族不在当前期望快照内：无期望侧投影，不参与判据
		}
		ad, err := c.Registry.Get(family)
		if err != nil {
			continue
		}
		obs, err := ad.Inventory(ctx, c.Home, desired)
		if err != nil {
			// 单家族采集失败不得伪装成"一致"：该家族两侧摘要都置为不可比标记，
			// 聚合摘要随之不等（服务端置 Drifted/Reconciled Unknown）。
			desiredSet[family] = "error"
			observedSet[family] = "error:" + err.Error()
			continue
		}
		desiredSet[family] = obs.DesiredProjectionDigest
		observedSet[family] = obs.ObservedProjectionDigest
	}
	if len(desiredSet) == 0 {
		return adapter.ProjectionCanonicalizationVersion, "", ""
	}
	return adapter.ProjectionCanonicalizationVersion,
		adapter.CombineProjectionDigests(desiredSet), adapter.CombineProjectionDigests(observedSet)
}

// agentInstances 上报 agentd 自身与各家族探测结果（矩阵验收 1：已装/未装、
// 版本可解析/不可解析都必须如实呈现）。
func (c *Collector) agentInstances(ctx context.Context) []domain.AgentObservation {
	out := []domain.AgentObservation{{Family: "agentd", Version: c.AgentdVersion, Enabled: true}}
	if c.Registry == nil {
		return out
	}
	for _, family := range c.Registry.Families() {
		ad, err := c.Registry.Get(family)
		if err != nil {
			continue
		}
		inst := domain.AgentObservation{Family: family, Enabled: true}
		if snap, ok := c.LoadDesired(); ok {
			if parsed, err := domain.ParseSnapshot(snap); err == nil {
				_, configured := parsed.Desired.Agents[family]
				inst.Enabled = configured
			}
		}
		d, err := ad.Detect(ctx, c.Home)
		switch {
		case err != nil:
			// 版本不可解析：明确记录原因，不伪装成未安装（矩阵验收 1）。
			inst.Installed = true
			inst.VersionError = err.Error()
		case d.Installed:
			inst.Installed = true
			inst.Version = d.Version
			inst.ConfigPath = d.ConfigPath
		}
		out = append(out, inst)
	}
	return out
}

func (c *Collector) machineInfo() *domain.MachineObservation {
	m := &domain.MachineObservation{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
	}
	if h, err := os.Hostname(); err == nil {
		m.Hostname = h
	}
	if home, err := os.UserHomeDir(); err == nil {
		m.HomeDir = home
	}
	m.KernelVersion = kernelRelease()
	m.TotalMemBytes = totalMem()
	return m
}

func (c *Collector) nextSeq() (int64, error) {
	path := filepath.Join(c.DataDir, lastObservedFile)
	var last int64
	if b, err := os.ReadFile(path); err == nil {
		var rec struct {
			InventorySeq int64 `json:"inventorySeq"`
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			return 0, fmt.Errorf("inventory: parse %s: %w", path, err)
		}
		last = rec.InventorySeq
	}
	seq := last + 1
	rec, _ := json.MarshalIndent(map[string]any{"inventorySeq": seq}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("inventory: create state dir: %w", err)
	}
	// 原子写（临时文件 + rename），避免中断留下截断文件。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, rec, 0o644); err != nil {
		return 0, fmt.Errorf("inventory: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, fmt.Errorf("inventory: rename %s: %w", path, err)
	}
	return seq, nil
}

// kernelRelease 读取内核版本；仅 Linux 支持（其余平台返回空）。
func kernelRelease() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// totalMem 读取物理内存（Linux /proc/meminfo，KB 单位）；其余平台返回 0。
func totalMem() int64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					return kb * 1024
				}
			}
		}
	}
	return 0
}
