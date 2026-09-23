package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/adapter/fixture"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/inventory"
	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestApplyWelcomeIntervals 固定 applyWelcome 的优先级：本地显式配置 >
// 服务端 Welcome 回传 > 默认值；服务端未给时不改动。
func TestApplyWelcomeIntervals(t *testing.T) {
	heartbeat := time.NewTicker(time.Second)
	defer heartbeat.Stop()
	full := time.NewTicker(time.Minute)
	defer full.Stop()
	log := quietLogger()

	// 本地未配置 → 采纳 Welcome 回传值。
	cfg := &Config{}
	hb, inv := applyWelcome(&fleetv1.Welcome{
		HeartbeatIntervalSeconds: 7, InventoryIntervalSeconds: 300,
	}, cfg, defaultHeartbeatInterval, defaultInventoryInterval, heartbeat, full, log)
	if hb != 7*time.Second || inv != 300*time.Second {
		t.Fatalf("welcome intervals not adopted: hb=%v inv=%v", hb, inv)
	}

	// 本地显式配置 → 忽略 Welcome。
	cfg = &Config{HeartbeatIntervalSeconds: 10, InventoryIntervalSeconds: 60}
	hb, inv = applyWelcome(&fleetv1.Welcome{
		HeartbeatIntervalSeconds: 7, InventoryIntervalSeconds: 300,
	}, cfg, 10*time.Second, 60*time.Second, heartbeat, full, log)
	if hb != 10*time.Second || inv != 60*time.Second {
		t.Fatalf("local override clobbered by welcome: hb=%v inv=%v", hb, inv)
	}

	// 服务端未给（≤0）→ 保持不变。
	hb, inv = applyWelcome(&fleetv1.Welcome{}, cfg, 10*time.Second, 60*time.Second, heartbeat, full, log)
	if hb != 10*time.Second || inv != 60*time.Second {
		t.Fatalf("empty welcome changed intervals: hb=%v inv=%v", hb, inv)
	}
}

// fakeAgentServer 模拟服务端下行：Hello 后回 Welcome，并可下发 RequestInventory；
// 收到的心跳与 inventory 记入带缓冲 channel 供断言（缓冲耗尽即丢弃并计数，
// 保证服务端 handler 永不阻塞测试）。
type fakeAgentServer struct {
	fleetv1.UnimplementedFleetAgentServiceServer
	welcome     *fleetv1.Welcome
	afterHello  func(stream grpc.BidiStreamingServer[fleetv1.AgentToServer, fleetv1.ServerToAgent]) error
	heartbeats  chan *fleetv1.Heartbeat
	inventories chan *fleetv1.ObservedState
	results     chan *fleetv1.OperationResult
	dropped     int
}

func (f *fakeAgentServer) Connect(stream grpc.BidiStreamingServer[fleetv1.AgentToServer, fleetv1.ServerToAgent]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetHello() == nil {
		return status.Error(codes.InvalidArgument, "first message must be Hello")
	}
	if err := stream.Send(&fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_Welcome{Welcome: f.welcome}}); err != nil {
		return err
	}
	if f.afterHello != nil {
		if err := f.afterHello(stream); err != nil {
			return err
		}
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil // 流结束即正常退出
		}
		switch payload := msg.GetPayload().(type) {
		case *fleetv1.AgentToServer_Heartbeat:
			select {
			case f.heartbeats <- payload.Heartbeat:
			default:
				f.dropped++
			}
		case *fleetv1.AgentToServer_ObservedState:
			select {
			case f.inventories <- payload.ObservedState:
			default:
				f.dropped++
			}
		case *fleetv1.AgentToServer_OperationResult:
			if f.results != nil {
				select {
				case f.results <- payload.OperationResult:
				default:
					f.dropped++
				}
			}
		}
	}
}

// TestRunStreamRequestInventoryAndWelcomeHeartbeat 驱动真实 runStream（-race 下）：
//   - 服务端 RequestInventory 触发一次新的全量 inventory（seq 单调递增），
//     且发送全部收敛在主循环（单写者，修复双 goroutine 并发 Send）；
//   - 本地未配置周期时，Welcome 回传的心跳周期被落实，不再固定用本地默认 15s。
func TestRunStreamRequestInventoryAndWelcomeHeartbeat(t *testing.T) {
	fs := &fakeAgentServer{
		welcome:     &fleetv1.Welcome{ProtocolVersion: "1", HeartbeatIntervalSeconds: 1},
		heartbeats:  make(chan *fleetv1.Heartbeat, 32),
		inventories: make(chan *fleetv1.ObservedState, 32),
	}
	fs.afterHello = func(stream grpc.BidiStreamingServer[fleetv1.AgentToServer, fleetv1.ServerToAgent]) error {
		return stream.Send(&fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_RequestInventory{
			RequestInventory: &fleetv1.RequestInventory{},
		}})
	}
	srv := grpc.NewServer()
	fleetv1.RegisterFleetAgentServiceServer(srv, fs)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	dir := t.TempDir()
	cfg := &Config{MachineID: "ws-1", DataDir: dir} // 不配本地间隔 → 应采纳 Welcome
	collector := &inventory.Collector{DataDir: filepath.Join(dir, "state"), AgentdVersion: agentdVersion}

	ctx, cancel := context.WithCancel(context.Background())
	home := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(fixture.New())
	exec := newExecutor(home, cfg.DataDir, reg, collector.NextSeq, quietLogger())
	workerMsgs := make(chan workerMsg, 32)
	go exec.RunWorker(ctx, workerMsgs)
	done := make(chan error, 1)
	go func() { done <- runStream(ctx, cfg, conn, collector, exec, workerMsgs, quietLogger()) }()

	// 初始全量 inventory（seq 1）+ RequestInventory 触发的第二次（seq 2）。
	for want := int64(1); want <= 2; want++ {
		select {
		case obs := <-fs.inventories:
			if obs.GetInventorySeq() != want {
				t.Fatalf("inventory seq = %d, want %d", obs.GetInventorySeq(), want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("inventory %d not received in time", want)
		}
	}

	// Welcome 周期 1s 生效：3s 内应收到 ≥2 次心跳（本地默认 15s 则为 0 次）。
	deadline := time.After(3 * time.Second)
	beats := 0
	for beats < 2 {
		select {
		case <-fs.heartbeats:
			beats++
		case <-deadline:
			t.Fatalf("expected >=2 heartbeats under welcome interval, got %d", beats)
		}
	}

	// 退出路径：ctx 取消后 runStream 返回且不留悬挂 goroutine（-race 守护）。
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runStream did not return after cancel")
	}
}

// 节点算出 verify 证据后必须真的发出去（§4.4 门禁四条件与 FR-15.5 审计证据都
// 靠它；此前 resultToProto 丢掉了 res.Verify，服务端因此永远拿不到证据、
// Reconciled 永远不置 True）。本用例走真实 gRPC 流 + 真实流水线，并覆盖 outbox
// 重发同一条转换路径。
func TestRunStreamReportsVerifyEvidence(t *testing.T) {
	fs := &fakeAgentServer{
		welcome:     &fleetv1.Welcome{ProtocolVersion: "1"},
		heartbeats:  make(chan *fleetv1.Heartbeat, 32),
		inventories: make(chan *fleetv1.ObservedState, 32),
		results:     make(chan *fleetv1.OperationResult, 8),
	}
	srv := grpc.NewServer()
	fleetv1.RegisterFleetAgentServiceServer(srv, fs)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	dir := t.TempDir()
	cfg := &Config{MachineID: "ws-1", DataDir: dir}
	collector := &inventory.Collector{DataDir: filepath.Join(dir, "state"), AgentdVersion: agentdVersion}
	home := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(fixture.New())
	exec := newExecutor(home, cfg.DataDir, reg, collector.NextSeq, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	workerMsgs := make(chan workerMsg, 32)
	go exec.RunWorker(ctx, workerMsgs)
	done := make(chan error, 1)
	go func() { done <- runStream(ctx, cfg, conn, collector, exec, workerMsgs, quietLogger()) }()

	// 真实快照（fixture 家族，受管键可收敛）。
	snap, err := desiredstate.BuildSnapshot("ws-1", 1, domain.DesiredState{
		SchemaVersion: "fixture/v1",
		Agents: map[string]domain.AgentDesired{fixture.ID: {
			Version: "1.0.0",
			Config:  json.RawMessage(`{"model":"m1","providerEndpoint":"http://ep"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := snap.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if replayed := exec.Enqueue(&fleetv1.ExecuteOperation{
		OperationId: "op-verify-1", DesiredGeneration: 1,
		Snapshot: &fleetv1.DesiredStateSnapshot{Generation: 1, Digest: snap.Digest, SnapshotJson: raw},
	}); replayed != nil {
		t.Fatalf("first enqueue unexpectedly replayed a terminal result: %+v", replayed)
	}

	var got *fleetv1.OperationResult
	select {
	case got = <-fs.results:
	case <-time.After(10 * time.Second):
		t.Fatal("operation result not received on the wire")
	}
	if got.GetPhase() != "Succeeded" || got.GetDesiredGeneration() != 1 {
		t.Fatalf("result = %s@gen%d", got.GetPhase(), got.GetDesiredGeneration())
	}
	v := got.GetVerify()
	if v == nil {
		t.Fatal("verify evidence missing from the reported OperationResult (FR-15.5)")
	}
	if v.GetDesiredProjectionDigest() == "" || v.GetDesiredProjectionDigest() != v.GetObservedProjectionDigest() {
		t.Fatalf("verify digests unusable: %+v", v)
	}
	if v.GetCanonicalizationVersion() != fixture.CanonicalizationVersion {
		t.Fatalf("canonicalizationVersion = %q", v.GetCanonicalizationVersion())
	}
	if v.GetInventorySeq() <= 0 || v.GetAdapterHealth() != domain.AdapterHealthPassed {
		t.Fatalf("verify evidence incomplete: %+v", v)
	}

	// outbox 重发路径（服务端未 Ack → 条目仍在）必须携带同一份证据。
	pend := exec.PendingResults()
	if len(pend) != 1 {
		t.Fatalf("outbox entries = %d, want 1", len(pend))
	}
	replayed := resultToProto(pend[0].OperationID, &pend[0].Result)
	if replayed.GetOperationId() != "op-verify-1" || replayed.GetVerify() == nil ||
		replayed.GetVerify().GetObservedProjectionDigest() != v.GetObservedProjectionDigest() ||
		replayed.GetDesiredGeneration() != 1 {
		t.Fatalf("outbox replay lost evidence: %+v", replayed)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runStream did not return after cancel")
	}
}
