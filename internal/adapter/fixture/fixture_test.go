package fixture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/adapter"
)

// 范围项 2（FR-8.2/FR-8.3 + ADR-1）在适配器契约层的最小锁定：
//   - 权威判据是"期望侧受管投影摘要 vs 观测侧受管投影摘要"，两侧同
//     canonicalizationVersion；
//   - 受管键（model/providerEndpoint）被本地改动 → 摘要不等，必须产生 drift；
//   - 仅未托管键改动 → 摘要相等，不得误报 drift、不得产生计划。
func TestInventoryProjectionManagedVsUnmanagedEdits(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	a := New()
	desired := adapter.AgentDesiredState{
		Family: ID, Version: "1.0.0",
		Config: json.RawMessage(`{"model":"m1","providerEndpoint":"http://ep"}`),
	}

	// 与本机一致：两侧摘要相等（同一规范化版本下可比）。
	writeConfig(t, home, `{"model":"m1","providerEndpoint":"http://ep","userCustom":"keep-me"}`)
	same, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if same.CanonicalizationVersion != CanonicalizationVersion {
		t.Fatalf("canonicalizationVersion = %q, want %q",
			same.CanonicalizationVersion, CanonicalizationVersion)
	}
	if same.DesiredProjectionDigest != same.ObservedProjectionDigest {
		t.Fatalf("consistent config reported as drift: desired=%s observed=%s",
			same.DesiredProjectionDigest, same.ObservedProjectionDigest)
	}
	if got := configChanges(t, a, home, desired, same); len(got) != 0 {
		t.Fatalf("consistent config produced plan: %+v", got)
	}

	// 受管键被改动 → 观测摘要变化，drift 必须成立，计划指向该受管键。
	writeConfig(t, home, `{"model":"hand-edited","providerEndpoint":"http://ep","userCustom":"keep-me"}`)
	drifted, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatalf("inventory after managed edit: %v", err)
	}
	if drifted.ObservedProjectionDigest == drifted.DesiredProjectionDigest {
		t.Fatal("managed-key edit did not change the observed projection digest (drift missed)")
	}
	changes := configChanges(t, a, home, desired, drifted)
	if len(changes) != 1 || changes[0].Key != "config.model" {
		t.Fatalf("managed edit plan = %+v, want a single config.model change", changes)
	}

	// 仅未托管键被改动 → 摘要不变（FR-8.2：只哈希受管投影）。
	writeConfig(t, home, `{"model":"m1","providerEndpoint":"http://ep","userCustom":"something-else"}`)
	clean, err := a.Inventory(ctx, home, desired)
	if err != nil {
		t.Fatalf("inventory after unmanaged edit: %v", err)
	}
	if clean.ObservedProjectionDigest != clean.DesiredProjectionDigest {
		t.Fatalf("unmanaged edit produced drift: desired=%s observed=%s",
			clean.DesiredProjectionDigest, clean.ObservedProjectionDigest)
	}
	if got := configChanges(t, a, home, desired, clean); len(got) != 0 {
		t.Fatalf("unmanaged edit produced plan: %+v", got)
	}
}

func configChanges(t *testing.T, a *Adapter, home string, desired adapter.AgentDesiredState,
	observed adapter.AgentObservedState) []adapter.Change {
	t.Helper()
	all, err := a.Plan(context.Background(), home, desired, observed)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var out []adapter.Change
	for _, c := range all {
		if c.Step == "config" {
			out = append(out, c)
		}
	}
	return out
}

func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ID, "config.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
