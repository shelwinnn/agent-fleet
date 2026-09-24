// agent-fleet-server 控制面入口（架构 v1.1.2 §3.6）：只做装配，禁止业务逻辑。
// KM-22 起新增：Fleet CA、agent gRPC 端点（mTLS）、enrollment、离线扫描。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/bundle"
	deployctl "github.com/shelwinnn/agent-fleet/internal/controller/deployment"
	machine "github.com/shelwinnn/agent-fleet/internal/controller/machine"
	reconcilectl "github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/controller/sshops"
	"github.com/shelwinnn/agent-fleet/internal/enrollment"
	grpcagent "github.com/shelwinnn/agent-fleet/internal/server/grpcagent"
	"github.com/shelwinnn/agent-fleet/internal/server/httpapi"
	"github.com/shelwinnn/agent-fleet/internal/sshtransport"
	"github.com/shelwinnn/agent-fleet/internal/server/sse"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

func main() {
	if err := run(); err != nil {
		slog.Error("agent-fleet-server exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)

	fs := flag.NewFlagSet("agent-fleet-server", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7788", "HTTP 监听地址（§4.1 默认仅回环）")
	grpcAddr := fs.String("grpc-addr", "0.0.0.0:7789", "agent gRPC 监听地址（mTLS，§4.1 默认 0.0.0.0:7789）")
	dbPath := fs.String("db", "", "SQLite 库文件路径（§4.9；默认 <data-dir>/fleet.db）")
	dataDir := fs.String("data-dir", defaultDataDir(), "控制面数据目录（pki 等，§10.2）")
	adminTokenEnv := fs.String("admin-token-env", "",
		"存放 admin token 的环境变量名；HTTP 非回环监听时必须配置（§29.14）")
	tokenTTL := fs.Duration("token-ttl", enrollment.DefaultTokenTTL,
		"enrollment token 有效期（§4.6 默认 10 分钟）")
	clientCertValidity := fs.Duration("client-cert-validity", 90*24*time.Hour,
		"签发的客户端证书有效期（有限期，spec §12.2）")
	heartbeatInterval := fs.Duration("heartbeat-interval", grpcagent.DefaultHeartbeatInterval,
		"期望心跳周期（§5.2 默认 15s，随 Welcome 回传）")
	inventoryInterval := fs.Duration("inventory-interval", grpcagent.DefaultInventoryInterval,
		"期望全量 inventory 周期（§5.2 默认 5min，随 Welcome 回传）")
	offlineAfter := fs.Duration("offline-after", grpcagent.DefaultOfflineAfter,
		"心跳超时置离线阈值（§4.2 默认 45s）")
	offlineScanEvery := fs.Duration("offline-scan-every", grpcagent.DefaultOfflineScanEvery,
		"离线扫描周期（默认 5s）")
	planTimeout := fs.Duration("plan-timeout", 30*time.Minute,
		"计划确认窗口（§9.3 默认 30 分钟，可配置）")
	freshnessWindow := fs.Duration("freshness-window", 15*time.Minute,
		"观测新鲜度阈值（§6.2 默认 3×inventory 间隔 = 15min）")
	deployScanEvery := fs.Duration("deploy-scan-every", 2*time.Second,
		"Deployment 推进循环周期")
	spaDir := fs.String("spa-dir", "web/dist",
		"Web UI 静态产物目录（§3.6：控制面同时托管 SPA；目录不存在则只提供 API）")
	adapterSchemaVersion := fs.String("adapter-schema-version", "fixture/v1",
		"适配器 schema 版本（渲染输入五要素之一，§9；随适配器切片对齐）")
	// —— SSH-only 通道（§4.5/§7.4，KM-26）——
	sshBinary := fs.String("ssh-binary", "ssh", "系统 OpenSSH 客户端路径（FR-12.1）")
	scpBinary := fs.String("scp-binary", "scp", "系统 scp 路径（FR-12.1）")
	sshClientConfig := fs.String("ssh-client-config", "",
		"传给 ssh/scp 的 -F 客户端配置；为空时沿用系统默认（保留既有 Host/Include/known_hosts）")
	sshConnectTimeout := fs.Duration("ssh-connect-timeout", sshtransport.DefaultConnectTimeout,
		"SSH 连接超时（§4.5 默认 10s）")
	sshCommandTimeout := fs.Duration("ssh-command-timeout", sshtransport.DefaultCommandTimeout,
		"SSH 命令超时（§4.5 默认 60s）")
	agentdBinDir := fs.String("agentd-bin-dir", filepath.Join(*dataDir, "agentd"),
		"按平台预构建的 agentd 二进制目录（agentd-<goos>-<goarch>，§7.4 临时二进制上传）")
	artifactDir := fs.String("skill-artifact-dir", filepath.Join(*dataDir, "artifacts", "skills"),
		"Skill 工件缓存目录（§4.7；bundle 只打包被引用的工件）")
	fs.Parse(os.Args[1:]) //nolint:errcheck // ExitOnError

	if *dbPath == "" {
		*dbPath = filepath.Join(*dataDir, "fleet.db")
	}

	adminToken := ""
	if *adminTokenEnv != "" {
		adminToken = os.Getenv(*adminTokenEnv)
		if adminToken == "" {
			return fmt.Errorf("environment variable %s is set as admin-token-env but empty", *adminTokenEnv)
		}
	}
	if err := checkBindSecurity(*addr, adminToken); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Fleet CA（§4.6）：首启本地生成；ca.key/server.key 0600 并启动自检。
	ca, err := enrollment.EnsureCA(filepath.Join(*dataDir, "pki"))
	if err != nil {
		return err
	}
	log.Info("fleet ca ready", "pki_dir", filepath.Join(*dataDir, "pki"))

	db, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx, log); err != nil {
		return err
	}

	// SSE 事件枢纽（§8.1 `GET /api/v1/events`，§23.6）：接到持久层的写成功钩子上，
	// 因此 REST、控制器与 agent 上报三条写入来源都自动产生事件（KM-25）。
	events := sse.NewHub(log)
	db.SetChangeSink(events.Publish)

	machineStore := sqlite.NewMachineStore(db)
	statusStore := sqlite.NewMachineStatusStore(db)
	certStore := sqlite.NewAgentCertificateStore(db)
	observedStore := sqlite.NewObservedStateStore(db)
	snapshotStore := sqlite.NewSnapshotStore(db)
	operationStore := sqlite.NewOperationStore(db)
	profileStore := sqlite.NewProfileStore(db)
	skillStore := sqlite.NewSkillStore(db)
	providerStore := sqlite.NewProviderStore(db)
	deploymentStore := sqlite.NewDeploymentStore(db)
	targetStore := sqlite.NewDeploymentTargetStore(db)

	ctrl := machine.NewController(machineStore, statusStore, observedStore, certStore, log)
	go machine.RunOfflineScanner(ctx, ctrl, *offlineScanEvery, *offlineAfter, log)

	// Reconcile 控制器（§4.3/§4.8/FR-9.x）：期望渲染 + 快照物化 + 派发 +
	// 状态机 + drift 三态。渲染纯函数经 NewRenderer 注入各仓储。
	render := reconcilectl.NewRenderer(machineStore, profileStore, skillStore,
		providerStore, *adapterSchemaVersion)
	recCtrl := reconcilectl.NewController(machineStore, statusStore, snapshotStore,
		operationStore, observedStore, render, reconcilectl.Config{
			PlanTimeout:     *planTimeout,
			FreshnessWindow: *freshnessWindow,
		}, log)

	// Deployment 控制器（§4.4/FR-10.x）与周期推进循环。
	depCtrl := deployctl.NewController(deploymentStore, targetStore, machineStore,
		snapshotStore, operationStore, observedStore, recCtrl, log)
	go depCtrl.RunLoop(ctx, *deployScanEvery)

	tokens := enrollment.NewTokenService(sqlite.NewEnrollmentStore(db))
	agentSrv := grpcagent.New(grpcagent.Config{
		HeartbeatInterval:  *heartbeatInterval,
		InventoryInterval:  *inventoryInterval,
		OfflineAfter:       *offlineAfter,
		ClientCertValidity: *clientCertValidity,
	}, ca, tokens, ctrl, machineStore, certStore, observedStore, log)
	// 双向接线：gRPC 端点是下行派发通道（Dispatcher），Reconcile 控制器是
	// 上行结果回灌入口（ResultSink）。
	recCtrl.SetDispatcher(agentSrv)
	agentSrv.SetResultSink(recCtrl)

	// SSH-only 通道（§4.5/§7.4/§9.3）：受限执行器 + 操作编排 + OpenSSH include 导出。
	sshTransport := sshtransport.New(sshtransport.Config{
		SSHBinary:      *sshBinary,
		SCPBinary:      *scpBinary,
		ClientConfig:   *sshClientConfig,
		ConnectTimeout: *sshConnectTimeout,
		CommandTimeout: *sshCommandTimeout,
		Log:            log,
	})
	sshOrch := sshops.New(sshops.Config{
		Transport:    sshTransport,
		Machines:     machineStore,
		Status:       statusStore,
		Ops:          operationStore,
		Inventory:    ctrl,
		Artifacts:    bundleLocalSource(*artifactDir),
		AgentdBinDir: *agentdBinDir,
		OperatorHome: operatorHome(),
		GeneratedDir: filepath.Join(*dataDir, "generated"),
		Log:          log,
	})
	recCtrl.SetSSHPlanner(sshOrch)

	// 重启自愈（§4.3 第 4 条/FR-15.3）：Pending/Running → Unknown（不移出未决集合）。
	if err := recCtrl.RecoverInflight(ctx); err != nil {
		return fmt.Errorf("recover in-flight operations: %w", err)
	}

	// 计划超时与观测新鲜度扫描（§9.3 第 4 条/§6.2 契约）。
	go func() {
		ticker := time.NewTicker(*offlineScanEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := recCtrl.ScanPlanTimeout(ctx, now.UTC()); err != nil {
					log.Error("plan timeout scan failed", "err", err)
				}
				if err := recCtrl.ScanFreshness(ctx, now.UTC()); err != nil {
					log.Error("freshness scan failed", "err", err)
				}
			}
		}
	}()

	// agent gRPC 端点（§4.1）：TLS 强制 + 服务端证书；Connect/Renew 强制客户端
	// 证书（mTLS），Enroll 免客户端证书但经 TLS + token + 按源 IP 失败限速。
	grpcListener, err := net.Listen("tcp", *grpcAddr)
	if err != nil {
		return fmt.Errorf("listen grpc %s: %w", *grpcAddr, err)
	}
	g := agentSrv.GRPCServer()
	grpcDone := make(chan error, 1)
	go func() {
		log.Info("agent grpc listening", "addr", *grpcAddr, "tls", true,
			"heartbeat_interval", heartbeatInterval.String(),
			"inventory_interval", inventoryInterval.String(),
			"offline_after", offlineAfter.String())
		grpcDone <- g.Serve(grpcListener)
	}()

	api := httpapi.New(httpapi.Config{
		AdminToken:        adminToken,
		SPADir:            *spaDir,
		Events:            events.Handler(),
		DeploymentTargets: targetStore.List,
		EnrollTokens:      &tokenIssuer{tokens: tokens, ttl: *tokenTTL},
		OnMachineDelete: func(ctx context.Context, name string) error {
			return ctrl.OnMachineDeleted(ctx, name)
		},
	},
		machineStore, profileStore, skillStore, providerStore, deploymentStore,
		operationStore, recCtrl, depCtrl, *adapterSchemaVersion, db.PingContext, log)

	api.SetSSH(sshOrch)
	api.SetInclude(sshOrch)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("agent-fleet-server listening", "addr", *addr, "db", *dbPath)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		g.Stop()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-grpcDone:
		_ = srv.Close()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		g.GracefulStop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// tokenIssuer 把 enrollment.TokenService 适配为 httpapi.EnrollTokenIssuer，
// 使 token TTL 由服务端 flag 统一决定。
type tokenIssuer struct {
	tokens *enrollment.TokenService
	ttl    time.Duration
}

func (t *tokenIssuer) Issue(ctx context.Context, machine, csrFingerprint string, _ time.Duration) (string, time.Time, error) {
	return t.tokens.Issue(ctx, machine, csrFingerprint, t.ttl)
}

// defaultDataDir 按架构 §4.9/§10.2 返回 ~/.local/share/agent-fleet。
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "agent-fleet-data"
	}
	return filepath.Join(home, ".local", "share", "agent-fleet")
}

// checkBindSecurity 落实 §29.14：HTTP 绑定非回环地址时必须配置 admin token。
// bundleLocalSource 是控制面工件缓存作为 bundle 工件来源（§4.7 的
// <data-dir>/artifacts/skills/<digest>/ 布局）。
func bundleLocalSource(root string) bundle.ArtifactSource {
	return bundle.LocalArtifactSource{Root: root}
}

// operatorHome 是 include 安装目标所在的操作者 home。
func operatorHome() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

func checkBindSecurity(addr, adminToken string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid addr %q: %w", addr, err)
	}
	if net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if adminToken == "" {
		return fmt.Errorf("binding to non-loopback address %q requires an admin token: "+
			"configure --admin-token-env (§29.14)", addr)
	}
	return nil
}
