package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/claude"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/codex"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/grok"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/omp"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/opencode"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/inventory"
	"github.com/shelwinnn/agent-fleet/internal/domain"
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

// renewRetryBackoff 是续期失败/未生效后的重试退避（§4.6：续期失败不终止 daemon，
// 旧证书在有效期内仍可用；退避避免忙等）。
const renewRetryBackoff = time.Minute

// renewTimeout 是单次 RenewCertificate RPC 的超时。
const renewTimeout = 30 * time.Second

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

	// HOME 根可注入（护栏 #12）。
	home := cfg.Home
	if home == "" {
		home = defaultHome()
	}
	// 批次一家族适配器注册表（§5.4：新家族 = 新增 adapter/<family> + 注册，
	// 控制面零改动）。ZCode 未注册：运行时来源为第三方非官方分发，等待范围决策
	// （见 docs/adapters-batch-1.md「ZCode 范围决策」）——未注册的家族会在
	// 流水线阶段 1 显式失败，不会被静默跳过。
	reg := adapter.NewRegistry()
	reg.Register(claude.New())
	reg.Register(codex.New())
	reg.Register(grok.New())
	reg.Register(omp.New())
	reg.Register(opencode.New())

	collector := &inventory.Collector{
		DataDir:       cfg.DataDir,
		AgentdVersion: agentdVersion,
		Registry:      reg,
		Home:          home,
	}
	// 证书续期循环（§4.6 续期窗口抖动、FR-11.2）：仅经既有有效 mTLS 通道；
	// 每次尝试后按磁盘上 pki/client.crt 的到期时刻重排；失败不终止 daemon；
	// ctx 取消即退出（runDaemon 返回前等它收尾，不泄漏 goroutine）。
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		runRenewLoop(ctx, cfg, conn, log)
	}()
	defer func() { <-renewDone }()
	exec := newExecutor(home, cfg.DataDir, reg, collector, log)
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
			Adapters:        collector.AdapterFamilies(),                  // 能力协商：已注册家族（FR-13.4）
			Capabilities:    capabilityDecls(collector.AdapterRegistry()), // 能力声明（矩阵）
		},
	}}); err != nil {
		return err
	}

	// 2) 全量 ObservedState（§5.2：紧随 Hello）。
	if err := sendInventory(streamCtx, stream, collector, log); err != nil {
		return err
	}

	// 3) outbox 重发（FR-13.7：紧随 Hello + 全量观测之后，未确认终态按序重发；
	// 服务端按 operationId 幂等去重，Ack 后淘汰）。
	for _, pr := range exec.PendingResults() {
		res := pr.Result
		if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationResult{
			OperationResult: resultToProto(pr.OperationID, &res),
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
				if err := sendInventory(streamCtx, stream, collector, log); err != nil {
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
						OperationResult: resultToProto(ev.execute.GetOperationId(), replayed),
					}}); err != nil {
						return err
					}
				}
			}
			if ev.cancel != "" {
				// FR-13.9：取消在阶段边界生效；仅入队未开始的直接终结。
				if res := exec.Cancel(ev.cancel); res != nil {
					if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_OperationResult{
						OperationResult: resultToProto(ev.cancel, res),
					}}); err != nil {
						return err
					}
				}
			}
		case wm := <-workerMsgs:
			// worker 上行（进度/终态）同样只在主循环发送（单写者）。
			var out *fleetv1.AgentToServer
			if wm.observation != nil {
				out = &fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
					ObservedState: wm.observation}}
			} else if wm.progress != nil {
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
			if err := sendInventory(streamCtx, stream, collector, log); err != nil {
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

func sendInventory(ctx context.Context, stream grpc.BidiStreamingClient[fleetv1.AgentToServer, fleetv1.ServerToAgent],
	collector *inventory.Collector, log *slog.Logger) error {
	obs, err := collector.Collect(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
		ObservedState: observedToProto(obs),
	}}); err != nil {
		return err
	}
	log.Info("observed state reported", "inventory_seq", obs.InventorySeq, "full", obs.Full,
		"canonicalization_version", obs.CanonicalizationVersion)
	return nil
}

// capabilityDecls 汇总已注册家族的逐能力声明（矩阵「统一适配契约」：
// 支持/不支持/未验证都必须上行，控制面只读取声明）。
func capabilityDecls(reg *adapter.Registry) []*fleetv1.AdapterCapability {
	if reg == nil {
		return nil
	}
	var out []*fleetv1.AdapterCapability
	for _, family := range reg.Families() {
		ad, err := reg.Get(family)
		if err != nil {
			continue
		}
		for _, d := range ad.Capabilities() {
			out = append(out, &fleetv1.AdapterCapability{
				Family:           family,
				Capability:       string(d.Capability),
				State:            string(d.State),
				VerifiedVersions: d.VerifiedVersions,
				Reason:           d.Reason,
			})
		}
	}
	return out
}

// observedToProto 映射观测状态（含 ADR-1 双侧投影摘要、绑定 operationId 与
// 适配器健康；规范版本随每次上报携带，§7.1）。
func observedToProto(obs domain.ObservedState) *fleetv1.ObservedState {
	m := &fleetv1.ObservedState{
		InventorySeq:             obs.InventorySeq,
		Full:                     obs.Full,
		OperationId:              obs.OperationID,
		CanonicalizationVersion:  obs.CanonicalizationVersion,
		DesiredProjectionDigest:  obs.DesiredProjectionDigest,
		ObservedProjectionDigest: obs.ObservedProjectionDigest,
		AdapterHealth:            obs.AdapterHealth,
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
			Installed: a.Installed, VersionError: a.VersionError,
		})
	}
	return m
}

// renewDeps 是续期循环的可注入依赖（默认值见 withDefaults；测试用假实现替换
// 磁盘/网络/时间，覆盖"续期后重排"与"ctx 取消退出"）。
type renewDeps struct {
	log          *slog.Logger
	window       time.Duration
	retryBackoff time.Duration
	// notAfter 读取磁盘上 pki/client.crt 的到期时刻；每次续期尝试后都会重新读取，
	// 作为下一次排期的依据（一次性的旧实现不重排 → 证书最终会过期）。
	notAfter func() (time.Time, error)
	// renew 经既有有效 mTLS 通道执行一次续期（§4.6：RenewCertificate 仅接受既有
	// 有效 mTLS 通道上的请求，spec §12.2）。
	renew func(ctx context.Context) error
	now   func() time.Time
	rnd   *rand.Rand
	// wait 等待给定延迟；ctx 取消时返回 false（循环据此退出）。
	wait func(ctx context.Context, d time.Duration) bool
}

// withDefaults 补齐未注入的依赖；生产路径只提供 notAfter/renew。
func (d renewDeps) withDefaults() renewDeps {
	if d.log == nil {
		d.log = slog.Default()
	}
	if d.window <= 0 {
		d.window = renewWindow
	}
	if d.retryBackoff <= 0 {
		d.retryBackoff = renewRetryBackoff
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.rnd == nil {
		// 局部随机源：抖动可注入、可测，不使用 math/rand 全局源。
		d.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if d.wait == nil {
		d.wait = waitContext
	}
	return d
}

// waitContext 等待 d，或在 ctx 取消时提前返回（返回 false 表示已取消）。
func waitContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// run 是证书续期循环（§4.6 续期窗口抖动、§5.2 daemon 运行时）：
//  1. 按磁盘上 pki/client.crt 的到期时刻在续期窗口内随机排期；
//  2. 到点后经既有 mTLS 通道续期一次（RenewCertificate，spec §12.2）；
//  3. 每次尝试结束后重新读取磁盘到期时刻，据此排下一次（成功=新证书，失败=旧证书）。
//     续期失败不终止 daemon（旧证书在有效期内仍可用）；未取得进展时退避重试，
//     绝不忙等；
//  4. ctx 取消立即退出，不泄漏 goroutine。
func (d renewDeps) run(ctx context.Context) {
	d = d.withDefaults()
	for {
		if ctx.Err() != nil {
			return
		}
		notAfter, err := d.notAfter()
		if err != nil {
			d.log.Warn("read client cert expiry failed; retrying", "err", err,
				"retry_in", d.retryBackoff.String())
			if !d.wait(ctx, d.retryBackoff) {
				return
			}
			continue
		}

		if delay := renewalDelay(d.now(), notAfter, d.window, d.rnd); delay > 0 {
			d.log.Info("certificate renewal scheduled", "not_after", notAfter.Format(time.RFC3339),
				"renew_in", delay.Round(time.Minute).String())
			if !d.wait(ctx, delay) {
				return
			}
		}

		renewErr := d.renew(ctx)
		if renewErr != nil {
			// 失败不终止 daemon：旧证书在有效期内仍可用。
			d.log.Warn("certificate renewal failed (old certificate remains in use)", "err", renewErr)
		}

		// 下一轮排期只看磁盘上的实际到期时刻：续期成功即新证书，失败则仍是旧证书。
		next, err := d.notAfter()
		if err != nil {
			d.log.Warn("read client cert expiry after renewal failed; retrying", "err", err,
				"retry_in", d.retryBackoff.String())
			if !d.wait(ctx, d.retryBackoff) {
				return
			}
			continue
		}
		if !next.After(notAfter) {
			// 续期未生效（失败或磁盘证书未更新）：退避后重试。
			d.log.Warn("certificate not renewed; retrying after backoff", "renew_err", renewErr,
				"not_after", next.Format(time.RFC3339), "retry_in", d.retryBackoff.String())
			if !d.wait(ctx, d.retryBackoff) {
				return
			}
			continue
		}
		if renewErr == nil {
			d.log.Info("client certificate renewed", "not_after", next.Format(time.RFC3339))
		}
	}
}

// runRenewLoop 启动 daemon 的证书续期循环：续期只走既有有效 mTLS 通道（§4.6），
// 循环按磁盘证书到期时刻重排（§5.2）；失败不终止 daemon，ctx 取消即退出。
func runRenewLoop(ctx context.Context, cfg *Config, conn *grpc.ClientConn, log *slog.Logger) {
	deps := renewDeps{
		log:      log,
		notAfter: func() (time.Time, error) { return certNotAfter(cfg.clientCertPath()) },
		renew: func(ctx context.Context) error {
			return renewCertificate(ctx, cfg, conn)
		},
	}
	deps.run(ctx)
}

// renewCertificate 经既有 mTLS 通道调用 RenewCertificate 并把签发结果落盘
// （§4.6、spec §12.2：私钥不轮换，复用本地已有密钥与 CSR）。返回错误供循环退避重试。
func renewCertificate(ctx context.Context, cfg *Config, conn *grpc.ClientConn) error {
	csrPEM, _, err := ensureKeyAndCSR(cfg)
	if err != nil {
		return fmt.Errorf("prepare csr: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, renewTimeout)
	defer cancel()
	client := fleetv1.NewFleetEnrollmentServiceClient(conn)
	resp, err := client.RenewCertificate(ctx, &fleetv1.RenewCertificateRequest{CsrPem: csrPEM})
	if err != nil {
		return fmt.Errorf("renew certificate: %w", err)
	}
	if err := storeClientCert(cfg, resp.GetCertPem()); err != nil {
		return fmt.Errorf("store certificate: %w", err)
	}
	return nil
}
