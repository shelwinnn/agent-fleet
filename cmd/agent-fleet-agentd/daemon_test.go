package main

import (
	"context"
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
