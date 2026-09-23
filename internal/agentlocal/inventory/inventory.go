// Package inventory 实现节点观测采集（架构 v1.1.2 §3.6 agentlocal/inventory/）。
// 本片为最小实现：机器信息 + agentd 自身实例占位（真实家族适配器随第 4 片接入，
// 双侧投影摘要同样留待该切片）。inventorySeq 为节点本地单调序（FR-8.7），
// 持久化于 state/last-observed.json，重启后不回退。
package inventory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// lastObservedFile 记录最近观测的 inventorySeq（§5.2：用于断线恢复与单调性，
// 不是权威证据——权威观测由服务端绑定 operationId 保存）。
const lastObservedFile = "state/last-observed.json"

// Collector 采集本机观测。
type Collector struct {
	// DataDir 是节点数据目录（~/.local/share/agent-fleet）。
	DataDir string
	// AgentdVersion 上报于占位 Agent 实例。
	AgentdVersion string

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

// Collect 产出一份全量观测（full=true）并递增持久化的 inventorySeq。
func (c *Collector) Collect() (domain.ObservedState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	seq, err := c.nextSeq()
	if err != nil {
		return domain.ObservedState{}, err
	}
	obs := domain.ObservedState{
		InventorySeq: seq,
		Full:         true,
		Machine:      c.machineInfo(),
		Agents:       c.agentInstances(),
		// canonicalizationVersion 本片占位：无受管投影可比（第 4 片引入真实值）。
		CanonicalizationVersion: "0",
	}
	return obs, nil
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

// agentInstances 上报占位实例：agentd 自身。真实家族（codex/omp/opencode/zcode）
// 的探测与实例上报随第 4 片适配器接入（KM-22 范围第 4 条）。
func (c *Collector) agentInstances() []domain.AgentObservation {
	return []domain.AgentObservation{{
		Family:  "agentd",
		Version: c.AgentdVersion,
		Enabled: true,
	}}
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
