package sqlite

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func snapOf(t *testing.T, machine string, gen int64, digest, schema string) *domain.DesiredStateSnapshot {
	t.Helper()
	return &domain.DesiredStateSnapshot{
		Machine: machine, Generation: gen, Digest: digest,
		Protocol: domain.SnapshotProtocolVersion, CanonicalizationVersion: "snapshot-json-v1",
		Desired: domain.DesiredState{
			SchemaVersion: schema,
			Agents: map[string]domain.AgentDesired{
				"fixture": {Version: "1.0.0", Config: json.RawMessage(`{"model":"m1"}`)},
			},
		},
	}
}

// 代身份与内容身份分离（FR-7.5/ADR-2）：digest 不变不增代；变化增代；
// A→B→A 时同 digest 可出现在多代，按代引用可解析（T16 单元部分）。
func TestSnapshotMaterializeGenerations(t *testing.T) {
	db := openTestDB(t)
	store := NewSnapshotStore(db)
	ctx := context.Background()

	a1, changed, err := store.Materialize(ctx, snapOf(t, "ws-1", 0, "sha256:A", "fixture/v1"))
	if err != nil || !changed || a1.Generation != 1 {
		t.Fatalf("first materialize: gen=%d changed=%v err=%v", a1.Generation, changed, err)
	}
	// 同内容再次物化 → 不新增代（FR-7.2：仅有效期望变化时递增）。
	a2, changed, err := store.Materialize(ctx, snapOf(t, "ws-1", 0, "sha256:A", "fixture/v1"))
	if err != nil || changed || a2.Generation != 1 {
		t.Fatalf("same-content materialize: gen=%d changed=%v err=%v", a2.Generation, changed, err)
	}
	// 内容变化 → gen2。
	b, changed, err := store.Materialize(ctx, snapOf(t, "ws-1", 0, "sha256:B", "fixture/v2"))
	if err != nil || !changed || b.Generation != 2 {
		t.Fatalf("changed materialize: gen=%d changed=%v err=%v", b.Generation, changed, err)
	}
	// A→B→A：内容回到 A → gen3，与 gen1 同 digest（两行）。
	a3, changed, err := store.Materialize(ctx, snapOf(t, "ws-1", 0, "sha256:A", "fixture/v1"))
	if err != nil || !changed || a3.Generation != 3 {
		t.Fatalf("A→B→A materialize: gen=%d changed=%v err=%v", a3.Generation, changed, err)
	}
	if a3.Digest != a1.Digest {
		t.Fatalf("A→B→A digest mismatch: %s vs %s", a3.Digest, a1.Digest)
	}
	// "回滚到第 1 代"按代解析可达，内容完整（FR-7.5）。
	g1, err := store.Get(ctx, "ws-1", 1)
	if err != nil {
		t.Fatalf("get gen1: %v", err)
	}
	if g1.Digest != "sha256:A" || g1.Desired.Agents["fixture"].Version != "1.0.0" {
		t.Fatalf("gen1 content wrong: %+v", g1.Desired)
	}
	if cur, err := store.Current(ctx, "ws-1"); err != nil || cur.Generation != 3 {
		t.Fatalf("current: %+v err=%v", cur, err)
	}
	// 不同机器的代空间独立。
	other, changed, err := store.Materialize(ctx, snapOf(t, "ws-2", 0, "sha256:A", "fixture/v1"))
	if err != nil || !changed || other.Generation != 1 {
		t.Fatalf("independent machine gen: gen=%d changed=%v err=%v", other.Generation, changed, err)
	}
}

// 并发物化的持久层不变式：同内容并发物化恰好一代；(machine, generation) 不重复、
// 每代内容完整（FR-7.2/ADR-2。交替内容并发时"相邻 digest 变化即增代"的语义
// 本身允许多代——那是两种 racing 的有效期望，非重复分配）。
func TestSnapshotMaterializeConcurrent(t *testing.T) {
	db := openTestDB(t)
	store := NewSnapshotStore(db)
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.Materialize(ctx, snapOf(t, "ws-1", 0, "sha256:same", "fixture/v1"))
			if err != nil {
				t.Errorf("materialize: %v", err)
			}
		}()
	}
	wg.Wait()
	cur, err := store.Current(ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Generation != 1 || cur.Digest != "sha256:same" {
		t.Fatalf("concurrent same-content materialize produced gen %d (%s), want exactly 1", cur.Generation, cur.Digest)
	}
}
