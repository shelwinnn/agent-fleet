// agent-fleet-server 控制面入口（架构 v1.1.2 §3.6）：只做装配，禁止业务逻辑。
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
	dbPath := fs.String("db", defaultDBPath(), "SQLite 库文件路径（§4.9）")
	adminTokenEnv := fs.String("admin-token-env", "",
		"存放 admin token 的环境变量名；非回环监听时必须配置（§29.14）")
	fs.Parse(os.Args[1:]) //nolint:errcheck // ExitOnError

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

	db, err := sqlite.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx, log); err != nil {
		return err
	}

	api := httpapi.New(httpapi.Config{AdminToken: adminToken},
		sqlite.NewMachineStore(db), sqlite.NewProfileStore(db),
		db.PingContext, log)

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
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// defaultDBPath 按架构 §4.9/§10.2 返回 ~/.local/share/agent-fleet/fleet.db。
func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "fleet.db"
	}
	return filepath.Join(home, ".local", "share", "agent-fleet", "fleet.db")
}

// checkBindSecurity 落实 §29.14：绑定非回环地址时必须配置 admin token。
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
