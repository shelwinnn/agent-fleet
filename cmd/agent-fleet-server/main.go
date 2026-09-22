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

	"github.com/shelwinnn/agent-fleet/internal/controller/machine"
	"github.com/shelwinnn/agent-fleet/internal/enrollment"
	grpcagent "github.com/shelwinnn/agent-fleet/internal/server/grpcagent"
	"github.com/shelwinnn/agent-fleet/internal/server/httpapi"
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

	machineStore := sqlite.NewMachineStore(db)
	statusStore := sqlite.NewMachineStatusStore(db)
	certStore := sqlite.NewAgentCertificateStore(db)
	observedStore := sqlite.NewObservedStateStore(db)

	ctrl := machine.NewController(machineStore, statusStore, observedStore, certStore, log)
	go machine.RunOfflineScanner(ctx, ctrl, *offlineScanEvery, *offlineAfter, log)

	tokens := enrollment.NewTokenService(sqlite.NewEnrollmentStore(db))
	agentSrv := grpcagent.New(grpcagent.Config{
		HeartbeatInterval:  *heartbeatInterval,
		InventoryInterval:  *inventoryInterval,
		OfflineAfter:       *offlineAfter,
		ClientCertValidity: *clientCertValidity,
	}, ca, tokens, ctrl, machineStore, certStore, observedStore, log)

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
		AdminToken:   adminToken,
		EnrollTokens: &tokenIssuer{tokens: tokens, ttl: *tokenTTL},
		OnMachineDelete: func(ctx context.Context, name string) error {
			return ctrl.OnMachineDeleted(ctx, name)
		},
	},
		machineStore, sqlite.NewProfileStore(db), db.PingContext, log)

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
