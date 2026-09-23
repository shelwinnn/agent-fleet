package main

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/adapter/fixture"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/inventory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
)

// agentdVersion 是 agentd 的版本号（§7.6 版本协商随 Hello 上报）。
const agentdVersion = "0.2.0"

// 本地未配置时的默认周期（§5.2：15s 心跳 / 5min 全量 inventory；
// 服务端 Welcome 回传值可覆盖，本地显式配置优先级最高）。
const (
	defaultHeartbeatInterval = 15 * time.Second
	defaultInventoryInterval = 5 * time.Minute
)

// renewWindow 是证书续期窗口（到期前该时长内触发；窗口带随机抖动，§4.6）。
const renewWindow = 7 * 24 * time.Hour

// runDaemon 实现常驻模式（§5.2 本片子集）：出站 mTLS gRPC Connect 长连接，
// 指数退避 + 抖动重连（spec §11.5）；每次（重）连接先 Hello + 全量 ObservedState；
// 心跳 15s、全量 inventory 5min（服务端 Welcome 可覆盖）；结果 outbox 与操作
// 执行随第 3 片接入。ctx 取消即退出。
func runDaemon(ctx context.Context, cfg *Config, log *slog.Logger) error {
	if err := cfg.ValidateForDaemon(); err != nil {
		return err
	}
	tlsCfg, err := loadClientTLS(cfg)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(cfg.Server,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay:  1 * time.Second,
				Multiplier: 1.6,
				Jitter:     0.2, // 抖动重连（spec §11.5）
				MaxDelay:   30 * time.Second,
			},
			MinConnectTimeout: 5 * time.Second,
		}))
	if err != nil {
		return err
	}
	defer conn.Close()

	collector := &inventory.Collector{DataDir: cfg.DataDir, AgentdVersion: agentdVersion}
	maybeRenewCertificate(cfg, conn, log)

	// 适配器注册表与执行宿主（本片注册 fixture 适配器验证语义；真实家族适配器
	// 随第 4 片接入）。HOME 根可注入（护栏 #12）。
	home := cfg.Home
	if home == "" {
		home = defaultHome()
	}
	reg := adapter.NewRegistry()
	reg.Register(fixture.New())
	exec := newExecutor(home, cfg.DataDir, reg, collector.NextSeq, log)
	workerMsgs := make(chan workerMsg, 32)
	go exec.RunWorker(ctx, workerMsgs)

	log.Info("agentd daemon starting", "server", cfg.Server, "machine_id", cfg.MachineID,
		"version", agentdVersion, "home", home)
	for {
		if err := runStream(ctx, cfg, conn, collector, exec, workerMsgs, log); err != nil {
			log.Warn("connect stream ended; reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
			log.Info("agentd daemon stopped")
			return nil
		case <-time.After(reconnectDelay()):
		}
	}
}

// reconnectDelay 提供 gRPC 内置退避之外的兜底间隔（流级失败重连）。
func reconnectDelay() time.Duration {
	return 2*time.Second + time.Duration(rand.Int63n(int64(2*time.Second)))
}

// runStream 建立一次 Connect 流并服务到断开。
func runStream(ctx context.Context, cfg *Config, conn *grpc.ClientConn,
	collector *inventory.Collector, exec *executor, workerMsgs <-chan workerMsg,
	log *slog.Logger) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	client := fleetv1.NewFleetAgentServiceClient(conn)
	stream, err := client.Connect(streamCtx)
	if err != nil {
		return err
	}

	// 1) Hello（§5.2：每次（重）连接必以 Hello 开始）。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_Hello{
		Hello: &fleetv1.Hello{
			MachineId:       cfg.MachineID,
			AgentdVersion:   agentdVersion,
			ProtocolVersion: "1",
			Adapters:        []string{}, // 家族适配器随第 4 片注册
		},
	}}); err != nil {
		return err
	}

	// 2) 全量 ObservedState（§5.2：紧随 Hello）。
	if err := sendInventory(stream, collector, log); err != nil {
		return err
	}

	// 3) outbox 重发（FR-13.7：紧随 Hello + 全量观测之后，未确认终态按序重发；
	// 服务端按 operationId 幂等去重，Ack 后淘汰）。
	for _, pr := range exec.PendingResults() {
		res := pr.Result
		if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationResult{
			OperationResult: &fleetv1.OperationResult{
				OperationId:      pr.OperationID,
				Phase:            res.Phase,
				Reason:           res.Reason,
				Message:          res.Message,
				TerminalModifier: res.TerminalModifier,
				StartedAtUnix:    res.StartedAt.Unix(),
				FinishedAtUnix:   res.FinishedAt.Unix(),
			},
		}}); err != nil {
			return err
		}
		log.Info("outbox result resent", "operation_id", pr.OperationID, "phase", res.Phase)
	}

	heartbeatEvery := cfg.heartbeatInterval()
	inventoryEvery := cfg.inventoryInterval()
	if heartbeatEvery <= 0 {
		heartbeatEvery = defaultHeartbeatInterval
	}
	if inventoryEvery <= 0 {
		inventoryEvery = defaultInventoryInterval
	}
	hbSeq := int64(0)
	heartbeat := time.NewTicker(heartbeatEvery)
	defer heartbeat.Stop()
	full := time.NewTicker(inventoryEvery)
	defer full.Stop()

	// serverEvents 把服务端下行事件转交主循环处理（含由此触发的所有发送）。
	type serverEvent struct {
		welcome       *fleetv1.Welcome
		wantInventory bool
		ack           string
		execute       *fleetv1.ExecuteOperation
		cancel        string
	}
	serverEvents := make(chan serverEvent, 8)
	recvErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			switch payload := msg.GetPayload().(type) {
			case *fleetv1.ServerToAgent_Welcome:
				select {
				case serverEvents <- serverEvent{welcome: payload.Welcome}:
				case <-streamCtx.Done():
					return
				}
			case *fleetv1.ServerToAgent_RequestInventory:
				log.Info("inventory requested by server")
				select {
				case serverEvents <- serverEvent{wantInventory: true}:
				case <-streamCtx.Done():
					return
				}
			case *fleetv1.ServerToAgent_OperationResultAck:
				select {
				case serverEvents <- serverEvent{ack: payload.OperationResultAck.GetOperationId()}:
				case <-streamCtx.Done():
					return
				}
			case *fleetv1.ServerToAgent_ExecuteOperation:
				select {
				case serverEvents <- serverEvent{execute: payload.ExecuteOperation}:
				case <-streamCtx.Done():
					return
				}
			case *fleetv1.ServerToAgent_CancelOperation:
				log.Info("cancel operation received", "operation_id", payload.CancelOperation.GetOperationId())
				select {
				case serverEvents <- serverEvent{cancel: payload.CancelOperation.GetOperationId()}:
				case <-streamCtx.Done():
					return
				}
			case *fleetv1.ServerToAgent_DesiredStateChanged:
				log.Info("desired state changed", "generation", payload.DesiredStateChanged.GetGeneration())
			default:
				log.Warn("unsupported server message ignored")
			}
		}
	}()

	for {
		select {
		case err := <-recvErr:
			return err
		case ev := <-serverEvents:
			// 发送单写者：下行事件触发的发送同样只在主循环执行
			// （gRPC 禁止不同 goroutine 并发 Send 同一 stream）。
			if w := ev.welcome; w != nil {
				heartbeatEvery, inventoryEvery = applyWelcome(w, cfg, heartbeatEvery, inventoryEvery, heartbeat, full, log)
			}
			if ev.wantInventory {
				if err := sendInventory(stream, collector, log); err != nil {
					return err
				}
			}
			if ev.ack != "" {
				// Ack 是 outbox 唯一淘汰依据（§8.2/FR-13.7）。
				exec.Forget(ev.ack)
			}
			if ev.execute != nil {
				// FR-13.8 重复派发幂等：执行中/已入队忽略（outbox 命中则重发终态）。
				if replayed := exec.Enqueue(ev.execute); replayed != nil {
					if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationResult{
						OperationResult: &fleetv1.OperationResult{
							OperationId:       ev.execute.GetOperationId(),
							Phase:             replayed.Phase,
							Reason:            replayed.Reason,
							Message:           replayed.Message,
							TerminalModifier:  replayed.TerminalModifier,
							DesiredGeneration: ev.execute.GetDesiredGeneration(),
							StartedAtUnix:     replayed.StartedAt.Unix(),
							FinishedAtUnix:    replayed.FinishedAt.Unix(),
						},
					}}); err != nil {
						return err
					}
				}
			}
			if ev.cancel != "" {
				// FR-13.9：取消在阶段边界生效；仅入队未开始的直接终结。
				if res := exec.Cancel(ev.cancel); res != nil {
					if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationResult{
						OperationResult: &fleetv1.OperationResult{
							OperationId:      ev.cancel,
							Phase:            res.Phase,
							Reason:           res.Reason,
							Message:          res.Message,
							TerminalModifier: res.TerminalModifier,
							StartedAtUnix:    res.StartedAt.Unix(),
							FinishedAtUnix:   res.FinishedAt.Unix(),
						},
					}}); err != nil {
						return err
					}
				}
			}
		case wm := <-workerMsgs:
			// worker 上行（进度/终态）同样只在主循环发送（单写者）。
			var out *fleetv1.AgentToServer
			if wm.progress != nil {
				out = &fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationProgress{
					OperationProgress: wm.progress}}
			} else if wm.result != nil {
				out = &fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationResult{
					OperationResult: wm.result}}
			}
			if out != nil {
				if err := stream.Send(out); err != nil {
					return err
				}
			}
		case <-heartbeat.C:
			hbSeq++
			if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_Heartbeat{
				Heartbeat: &fleetv1.Heartbeat{Sequence: hbSeq, SentAtUnix: time.Now().Unix()},
			}}); err != nil {
				return err
			}
			log.Debug("heartbeat sent", "sequence", hbSeq)
		case <-full.C:
			if err := sendInventory(stream, collector, log); err != nil {
				return err
			}
		case <-streamCtx.Done():
			return streamCtx.Err()
		}
	}
}

// applyWelcome 把 Welcome 回传的周期落实到 ticker（§5.2 服务端可覆盖节点周期）。
// 优先级：本地显式配置（>0）> 服务端回传（>0）> 默认值；服务端未给（≤0）或与
// 当前一致时不重排，避免无谓打断心跳节拍。
func applyWelcome(w *fleetv1.Welcome, cfg *Config, hbEvery, invEvery time.Duration,
	heartbeat, full *time.Ticker, log *slog.Logger) (time.Duration, time.Duration) {
	if s := w.GetHeartbeatIntervalSeconds(); s > 0 && cfg.heartbeatInterval() <= 0 {
		if d := time.Duration(s) * time.Second; d != hbEvery {
			heartbeat.Reset(d)
			hbEvery = d
		}
	}
	if s := w.GetInventoryIntervalSeconds(); s > 0 && cfg.inventoryInterval() <= 0 {
		if d := time.Duration(s) * time.Second; d != invEvery {
			full.Reset(d)
			invEvery = d
		}
	}
	log.Info("report intervals in effect", "heartbeat_every", hbEvery.String(),
		"inventory_every", invEvery.String())
	return hbEvery, invEvery
}

func sendInventory(stream grpc.BidiStreamingClient[fleetv1.AgentToServer, fleetv1.ServerToAgent],
	collector *inventory.Collector, log *slog.Logger) error {
	obs, err := collector.Collect()
	if err != nil {
		return err
	}
	m := &fleetv1.ObservedState{
		InventorySeq:            obs.InventorySeq,
		Full:                    obs.Full,
		CanonicalizationVersion: obs.CanonicalizationVersion,
	}
	if obs.Machine != nil {
		m.Machine = &fleetv1.MachineInfo{
			Os:            obs.Machine.OS,
			Arch:          obs.Machine.Arch,
			Hostname:      obs.Machine.Hostname,
			HomeDir:       obs.Machine.HomeDir,
			Kernel:        obs.Machine.KernelVersion,
			CpuCount:      int32(obs.Machine.CPUCores),
			TotalMemBytes: obs.Machine.TotalMemBytes,
		}
	}
	for _, a := range obs.Agents {
		m.Agents = append(m.Agents, &fleetv1.AgentInstance{
			Family: a.Family, Version: a.Version, Enabled: a.Enabled, ConfigPath: a.ConfigPath,
		})
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
		ObservedState: m,
	}}); err != nil {
		return err
	}
	log.Info("observed state reported", "inventory_seq", obs.InventorySeq, "full", obs.Full)
	return nil
}

// maybeRenewCertificate 在证书进入续期窗口时经既有 mTLS 通道续期（spec §12.2），
// 窗口内随机取触发时刻（§4.6 抖动）。续期失败不阻断 daemon（旧证书在有效期内仍可用）。
func maybeRenewCertificate(cfg *Config, conn *grpc.ClientConn, log *slog.Logger) {
	notAfter, err := certNotAfter(cfg.clientCertPath())
	if err != nil {
		log.Warn("read client cert expiry failed", "err", err)
		return
	}
	delay := renewalDelay(notAfter, renewWindow)
	if delay > 0 {
		log.Info("certificate renewal scheduled", "not_after", notAfter.Format(time.RFC3339),
			"renew_in", delay.Round(time.Minute).String())
		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			<-timer.C
			renewCertificate(cfg, conn, log)
		}()
		return
	}
	renewCertificate(cfg, conn, log)
}

func renewCertificate(cfg *Config, conn *grpc.ClientConn, log *slog.Logger) {
	csrPEM, _, err := ensureKeyAndCSR(cfg)
	if err != nil {
		log.Warn("renew: prepare csr failed", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := fleetv1.NewFleetEnrollmentServiceClient(conn)
	resp, err := client.RenewCertificate(ctx, &fleetv1.RenewCertificateRequest{CsrPem: csrPEM})
	if err != nil {
		log.Warn("certificate renewal failed (old certificate remains in use)", "err", err)
		return
	}
	if err := storeClientCert(cfg, resp.GetCertPem()); err != nil {
		log.Warn("renew: store certificate failed", "err", err)
		return
	}
	log.Info("client certificate renewed", "not_after_unix", resp.GetNotAfterUnix())
}
