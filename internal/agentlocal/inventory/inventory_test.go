package inventory

import (
	"context"
	"path/filepath"
	"testing"
)

// 采集最小契约：seq 单调递增且持久化；机器信息完整；Agent 实例为占位（agentd 自身）。
func TestCollectIncrementsPersistedSeq(t *testing.T) {
	c := &Collector{DataDir: t.TempDir(), AgentdVersion: "0.2.0"}

	first, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	second, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if second.InventorySeq != first.InventorySeq+1 {
		t.Fatalf("seq not monotonic: %d -> %d", first.InventorySeq, second.InventorySeq)
	}
	if !first.Full || !second.Full {
		t.Fatal("full flag must be true")
	}
	if first.Machine == nil || first.Machine.OS == "" || first.Machine.Arch == "" {
		t.Fatalf("machine info incomplete: %+v", first.Machine)
	}
	if first.Machine.Hostname == "" {
		t.Fatal("hostname should be reported")
	}
	if len(first.Agents) != 1 || first.Agents[0].Family != "agentd" || first.Agents[0].Version != "0.2.0" {
		t.Fatalf("placeholder agent instance wrong: %+v", first.Agents)
	}
}

// 重启不回退：新建 Collector（同 DataDir）从持久化序号继续。
func TestSeqSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	c1 := &Collector{DataDir: dir, AgentdVersion: "0.2.0"}
	if _, err := c1.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	c2 := &Collector{DataDir: filepath.Join(dir), AgentdVersion: "0.2.0"}
	obs, err := c2.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if obs.InventorySeq != 2 {
		t.Fatalf("seq after restart = %d, want 2", obs.InventorySeq)
	}
}
