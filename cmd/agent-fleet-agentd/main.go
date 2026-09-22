// agent-fleet-agentd 是节点侧 agent（架构 v1.1.2 §5.1/§3.6 cmd 布局）。
// 本片实现子命令：daemon（常驻 mTLS 长连接 + 心跳 + 全量 inventory）、
// enroll（一次性 enrollment）、oneshot inventory（输出观测 JSON）、version。
// oneshot plan|apply、doctor 随第 3/4/6 片引入。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/inventory"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)

	var err error
	switch os.Args[1] {
	case "daemon":
		err = cmdDaemon(os.Args[2:], log)
	case "enroll":
		err = cmdEnroll(os.Args[2:], log)
	case "oneshot":
		err = cmdOneshot(os.Args[2:])
	case "version":
		fmt.Println("agent-fleet-agentd " + agentdVersion)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("agent-fleet-agentd exited", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  agent-fleet-agentd enroll    [--config P] [--server A] [--machine M] [--ca P] [--data-dir P] [--token T]
  agent-fleet-agentd daemon    [--config P] [--server A] [--machine M] [--ca P] [--data-dir P]
  agent-fleet-agentd oneshot inventory
  agent-fleet-agentd version
`)
}

func commonFlags(fs *flag.FlagSet) (configPath, server, machine, caCert, dataDir, token *string) {
	configPath = fs.String("config", DefaultConfigPath(), "agentd.yaml 路径（默认 ~/.config/agent-fleet/agentd.yaml）")
	server = fs.String("server", "", "控制面 gRPC 地址 host:port（覆盖配置）")
	machine = fs.String("machine", "", "Machine 名称（覆盖配置）")
	caCert = fs.String("ca", "", "Fleet CA 证书路径（覆盖配置；用于校验服务端证书）")
	dataDir = fs.String("data-dir", "", "节点数据目录（覆盖配置）")
	token = fs.String("token", "", "enrollment token（仅 enroll；不写入日志）")
	return
}

func cmdEnroll(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	configPath, server, machine, caCert, dataDir, token := commonFlags(fs)
	fs.Parse(args) //nolint:errcheck // ExitOnError
	cfg, err := LoadConfig(*configPath, *server, *machine, *caCert, *dataDir, *token)
	if err != nil {
		return err
	}
	return runEnroll(cfg, log)
}

func cmdDaemon(args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath, server, machine, caCert, dataDir, _ := commonFlags(fs)
	fs.Parse(args) //nolint:errcheck // ExitOnError
	cfg, err := LoadConfig(*configPath, *server, *machine, *caCert, *dataDir, "")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemon(ctx, cfg, log)
}

// cmdOneshot 本片仅支持 inventory：输出观测状态 JSON（§5.1）。
func cmdOneshot(args []string) error {
	if len(args) != 1 || args[0] != "inventory" {
		return fmt.Errorf("oneshot: only `inventory` is implemented in this slice (plan/apply land with slice 3)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	collector := &inventory.Collector{DataDir: home + "/.local/share/agent-fleet", AgentdVersion: agentdVersion}
	obs, err := collector.Collect()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(obs)
}
