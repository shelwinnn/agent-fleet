package machine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

func newController(t *testing.T) (*Controller, domain.MachineRepository) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), discardLog()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	machines := sqlite.NewMachineStore(db)
	c := NewController(machines,
		sqlite.NewMachineStatusStore(db),
		sqlite.NewObservedStateStore(db),
		sqlite.NewAgentCertificateStore(db),
		discardLog())
	return c, machines
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

func mustStatus(t *testing.T, machines domain.MachineRepository, name string) domain.MachineStatus {
	t.Helper()
	m, err := machines.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// 条件写入纪律：重复写同一状态不推进 lastTransitionTime；状态变化才推进（§22）。
func TestSetConditionTransitionSemantics(t *testing.T) {
	c, machines := newController(t)
	ctx := context.Background()
	if err := machines.Create(ctx, &domain.Machine{Metadata: domain.ObjectMeta{Name: "m1"}}); err != nil {
		t.Fatal(err)
	}
	t1 := time.Now().UTC().Add(-time.Hour)
	if err := c.OnConnected(ctx, "m1", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	// 心跳（不改变条件状态）。
	if err := c.OnHeartbeat(ctx, "m1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	st := mustStatus(t, machines, "m1")
	cond, _ := st.GetCondition(domain.ConditionAgentConnected)
	firstTransition := cond.LastTransitionTime
	if !firstTransition.After(t1) {
		t.Fatal("transition time should be set on first True")
	}
	// 重复 True 写入（OnConnected 再触发）不推进时间。
	time.Sleep(5 * time.Millisecond)
	if err := c.OnConnected(ctx, "m1", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	st = mustStatus(t, machines, "m1")
	cond, _ = st.GetCondition(domain.ConditionAgentConnected)
	if !cond.LastTransitionTime.Equal(firstTransition) {
		t.Fatal("same-status rewrite must not bump lastTransitionTime")
	}
}

// 离线扫描：agentd 机超时置离线；SSH-only 机从不被扫描触碰（§6.2 Unknown 语义）。
func TestScanOfflineOnlyTouchesAgentdMachines(t *testing.T) {
	c, machines := newController(t)
	ctx := context.Background()
	for _, m := range []struct {
		name, mode string
	}{
		{"agentd-1", domain.ManagementModeAgentd},
		{"ssh-1", domain.ManagementModeSSH},
	} {
		spec := `{"managementMode":"` + m.mode + `"}`
		if err := machines.Create(ctx, &domain.Machine{
			Metadata: domain.ObjectMeta{Name: m.name},
			Spec:     jsonRaw(spec),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 两台都曾连接过（写 True 与 lastHeartbeatAt）。
	for _, name := range []string{"agentd-1", "ssh-1"} {
		if err := c.OnConnected(ctx, name, "0.2.0"); err != nil {
			t.Fatal(err)
		}
	}
	// 时间前进超过阈值后扫描。
	err := c.ScanOffline(ctx, time.Nanosecond, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, machines, "agentd-1"); func() bool {
		cond, ok := st.GetCondition(domain.ConditionAgentConnected)
		return !ok || cond.Status != domain.ConditionFalse
	}() {
		t.Fatal("agentd machine should be marked offline")
	}
	// SSH-only 机的 AgentConnected 不该被写 False（保持 True，因为它"从不建立流"，
	// 但本例它被显式连接过——扫描只看 lastHeartbeat；SSH 机不该进入该路径）。
	// 架构语义上 SSH-only 机不会被扫描翻转：这里验证扫描确实跳过了它。
	if st := mustStatus(t, machines, "ssh-1"); func() bool {
		cond, ok := st.GetCondition(domain.ConditionAgentConnected)
		return ok && cond.Status == domain.ConditionFalse
	}() {
		t.Fatal("ssh-only machine must not be flipped offline by heartbeat scanner")
	}
}
