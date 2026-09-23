// agent-fleet-agentd 是节点侧 agent（架构 v1.1.2 §5.1/§3.6 cmd 布局）。
// 子命令：daemon（常驻 mTLS 长连接 + 心跳 + 全量 inventory）、enroll、
// oneshot inventory|plan|apply（SSH-only 路径，第 6 片）、version。
// doctor 尚未实现（见交付说明的遗留项）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
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
		// oneshot 需要把"结果已产出但操作失败"与"基础设施错误"用退出码区分开
		// （控制面据此决定是写终态还是报错），见 oneshot.go 的退出码约定。
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  agent-fleet-agentd enroll    [--config P] [--server A] [--machine M] [--ca P] [--data-dir P] [--token T]
  agent-fleet-agentd daemon    [--config P] [--server A] [--machine M] [--ca P] [--data-dir P]
  agent-fleet-agentd oneshot inventory [--home P] [--data-dir P]
  agent-fleet-agentd oneshot plan      --bundle P --operation-id ID [--staging P]
  agent-fleet-agentd oneshot apply     --bundle P --operation-id ID [--plan-digest D]
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
	home := fs.String("home", "", "受管内容解析根目录（覆盖配置；默认 $HOME，护栏 #12）")
	fs.Parse(args) //nolint:errcheck // ExitOnError
	cfg, err := LoadConfig(*configPath, *server, *machine, *caCert, *dataDir, "")
	if err != nil {
		return err
	}
	if *home != "" {
		cfg.Home = *home
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemon(ctx, cfg, log)
}

// cmdOneshot 分发 oneshot 子命令（§5.1）：inventory / plan / apply。
// 三者与控制面共用同一 reconciler 与同一把节点执行权锁（AD-5/§5.6）。
func cmdOneshot(args []string) error {
	if len(args) == 0 {
		return errors.New("oneshot: subcommand required (inventory|plan|apply)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "inventory":
		return cmdOneshotInventory(rest)
	case "plan":
		return cmdOneshotPlan(rest)
	case "apply":
		return cmdOneshotApply(rest)
	default:
		return fmt.Errorf("oneshot: unknown subcommand %q (want inventory|plan|apply)", sub)
	}
}
