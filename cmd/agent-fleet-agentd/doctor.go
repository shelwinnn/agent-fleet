package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/claude"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/codex"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/grok"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/omp"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/adapter/opencode"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/reconciler"
)

// doctor 是节点本地诊断（架构 v1.1.2 §5.1、§5.6 规则 5）：
//
//	agent-fleet-agentd doctor                 # 只读诊断报告（平台/配置/执行权/暂存）
//	agent-fleet-agentd doctor --recover-lock  # 显式清理陈旧执行权锁（唯一的人工恢复入口）
//
// `--recover-lock` 是 §5.6 规则 5 "陈旧锁不得静默夺取" 的操作者显式处理路径：
// 持有者进程仍存活时**拒绝**清理；只有持有者已死才删除锁文件，并打印它删了什么。
func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	home := fs.String("home", "", "受管内容解析根（护栏 #12；默认 $HOME）")
	dataDir := fs.String("data-dir", "", "节点数据目录（默认 <home>/.local/share/agent-fleet）")
	recoverLock := fs.Bool("recover-lock", false,
		"显式清理陈旧执行权锁（持有者仍存活时拒绝；§5.6 规则 5）")
	jsonOut := fs.Bool("json", false, "以 JSON 输出诊断报告")
	fs.Parse(args) //nolint:errcheck // ExitOnError

	h := *home
	if h == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		h = hd
	}
	dd := *dataDir
	if dd == "" {
		dd = filepath.Join(h, ".local", "share", "agent-fleet")
	}
	lock := reconciler.NewExecutionLock(dd)

	report := map[string]any{
		"agentdVersion": agentdVersion,
		"home":          h,
		"dataDir":       dd,
		"lockPath":      lock.Path,
		"checkedAt":     time.Now().UTC().Format(time.RFC3339),
	}
	state, holder, err := describeLock(lock)
	report["executionLock"] = state
	if holder != nil {
		report["executionLockHolder"] = holder
	}
	if err != nil {
		report["executionLockError"] = err.Error()
	}
	if entries, derr := os.ReadDir(filepath.Join(dd, "staging")); derr == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		report["stagingEntries"] = names
	}
	report["adapters"] = adapterFamilies()

	if *recoverLock {
		if err := lock.Recover(); err != nil {
			return fmt.Errorf("recover execution lock: %w", err)
		}
		report["recoveredLock"] = true
		fmt.Fprintln(os.Stderr, "execution lock cleared (only if its owner was not alive)")
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Printf("agent-fleet-agentd %s\n", agentdVersion)
	fmt.Printf("home        : %s\n", h)
	fmt.Printf("data dir    : %s\n", dd)
	fmt.Printf("adapters    : %v\n", report["adapters"])
	fmt.Printf("exec-lock   : %s (%s)\n", state, lock.Path)
	if holder != nil {
		fmt.Printf("lock holder : pid %v channel %v op %v acquiredAt %v\n",
			holder["ownerPid"], holder["channel"], holder["operationId"], holder["acquiredAt"])
	}
	if err != nil {
		fmt.Printf("lock error  : %v\n", err)
	}
	if v, ok := report["recoveredLock"]; ok {
		fmt.Printf("recovered   : %v\n", v)
	}
	fmt.Println("hint        : stale lock recovery = `agent-fleet-agentd doctor --recover-lock` " +
		"(confirm no change pipeline is running first)")
	return nil
}

// describeLock 返回锁状态：free / held / stale（陈旧=持有者已死）/ unparseable。
func describeLock(lock *reconciler.ExecutionLock) (string, map[string]any, error) {
	lf, alive, err := lock.Holder()
	if err != nil {
		return "unparseable", nil, err
	}
	if lf == nil {
		return "free", nil, nil
	}
	holder := map[string]any{
		"ownerPid": lf.OwnerPID, "channel": lf.Channel, "operationId": lf.OperationID,
		"acquiredAt": lf.AcquiredAt.Format(time.RFC3339), "stagingCleaned": lf.StagingCleaned,
	}
	state := "stale"
	if alive {
		state = "held"
	}
	return state, holder, nil
}

// adapterFamilies 列出本地已注册家族（诊断用；控制面零改动的注册表）。
func adapterFamilies() []string {
	reg := adapter.NewRegistry()
	reg.Register(claude.New())
	reg.Register(codex.New())
	reg.Register(grok.New())
	reg.Register(omp.New())
	reg.Register(opencode.New())
	return reg.Families()
}
