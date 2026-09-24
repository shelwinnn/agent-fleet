package main

// KM-26 核查回归（必改 3/4）：
//   - 节点执行权失败必须带 §30 reason（NodeBusy / StaleExecutionLock），不得退化成 Internal；
//   - 失败/拒绝路径只能向 stdout 写**一份** JSON 文档（控制面按单文档解析）；
//   - 陈旧锁必须有可执行的人工恢复入口（doctor --recover-lock）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/reconciler"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func TestClassifyOneshotLockReasons(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		wantExit int
	}{
		{"lock-held", &reconciler.LockHeldError{Held: reconciler.LockFile{OwnerPID: 1, Channel: "daemon", OperationID: "op-1"}},
			domain.ReasonNodeBusy, exitExecution},
		{"lock-stale", &reconciler.StaleLockError{Reason: "stale", Stale: reconciler.LockFile{OwnerPID: 999999}},
			domain.ReasonStaleExecutionLock, exitExecution},
		{"bundle", domain.Coded(domain.ReasonSkillDigestMismatch, "digest mismatch"), domain.ReasonSkillDigestMismatch, exitBundle},
		{"other", errors.New("boom"), domain.ReasonInternal, exitInfra},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, exit := classifyOneshot(tc.err)
			if code != tc.wantCode || exit != tc.wantExit {
				t.Fatalf("got (%s, %d), want (%s, %d)", code, exit, tc.wantCode, tc.wantExit)
			}
		})
	}
}

func TestLockReasonIsActionable(t *testing.T) {
	stale := &reconciler.StaleLockError{Reason: "stale lock held by dead pid 999999",
		Stale: reconciler.LockFile{OwnerPID: 999999, OperationID: "op-1"}}
	wrapped := lockReason(stale)
	if got := domain.ReasonOf(wrapped); got != domain.ReasonStaleExecutionLock {
		t.Fatalf("reason = %s, want %s", got, domain.ReasonStaleExecutionLock)
	}
	if !strings.Contains(wrapped.Error(), "doctor --recover-lock") {
		t.Fatalf("stale-lock error must tell the operator how to recover: %v", wrapped)
	}
	held := &reconciler.LockHeldError{Held: reconciler.LockFile{OwnerPID: 1, Channel: "daemon", OperationID: "op-1"}}
	if got := domain.ReasonOf(lockReason(held)); got != domain.ReasonNodeBusy {
		t.Fatalf("held-lock reason = %s, want %s", got, domain.ReasonNodeBusy)
	}
}

// captureStdout 捕获一次调用的 stdout（writeJSON 直接写 os.Stdout）。
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := fn()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out), ferr
}

// decodeSingleJSON 断言 buffer 里**恰好**一个 JSON 文档（没有第二份、没有尾随内容）。
func decodeSingleJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(out)))
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s", err, out)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout carries more than one JSON document (err=%v):\n%s", err, out)
	}
	return first
}

func TestOneshotFailEmitsSingleJSONWithReason(t *testing.T) {
	held := &reconciler.LockHeldError{Held: reconciler.LockFile{OwnerPID: 4014, Channel: "daemon", OperationID: "op-live"}}
	out, err := captureStdout(t, func() error { return oneshotFail("op-x", held) })
	if err == nil {
		t.Fatal("oneshotFail must return an error (non-zero exit)")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitExecution {
		t.Fatalf("exit code = %v, want %d", err, exitExecution)
	}
	doc := decodeSingleJSON(t, out)
	if doc["reason"] != domain.ReasonNodeBusy {
		t.Fatalf("reason = %v, want %s (full: %s)", doc["reason"], domain.ReasonNodeBusy, out)
	}
	if doc["phase"] != domain.OperationPhaseFailed {
		t.Fatalf("phase = %v, want Failed", doc["phase"])
	}
}

func TestOneshotRefuseEmitsSingleJSON(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return oneshotRefuse("op-x", domain.ReasonReplanRequired, "baseline changed")
	})
	if err != nil {
		t.Fatalf("refusal must exit 0 (it is a node verdict, not an infra error): %v", err)
	}
	doc := decodeSingleJSON(t, out)
	if doc["reason"] != domain.ReasonReplanRequired {
		t.Fatalf("reason = %v, want %s", doc["reason"], domain.ReasonReplanRequired)
	}
}

// TestDoctorRecoverLock：陈旧锁可由 doctor --recover-lock 清理；持有者存活时拒绝。
func TestDoctorRecoverLock(t *testing.T) {
	dataDir := t.TempDir()
	lockPath := filepath.Join(dataDir, "state", "execution.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := reconciler.LockFile{OwnerPID: 999999, Channel: "oneshot", OperationID: "op-dead"}
	raw, _ := json.Marshal(stale)
	if err := os.WriteFile(lockPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return cmdDoctor([]string{"--data-dir", dataDir, "--recover-lock", "--json"})
	})
	if err != nil {
		t.Fatalf("doctor --recover-lock: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("stale lock was not cleared (err=%v)", err)
	}
	doc := decodeSingleJSON(t, out)
	if doc["recoveredLock"] != true {
		t.Fatalf("report must record the recovery: %s", out)
	}

	// 持有者存活（本进程）：必须拒绝清理，锁保持原样。
	live := reconciler.LockFile{OwnerPID: os.Getpid(), Channel: "daemon", OperationID: "op-live"}
	raw, _ = json.Marshal(live)
	if err := os.WriteFile(lockPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error {
		return cmdDoctor([]string{"--data-dir", dataDir, "--recover-lock", "--json"})
	}); err == nil {
		t.Fatal("doctor must refuse to clear a lock whose owner is alive")
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("a live lock must be preserved: %v", err)
	}
}

// TestDoctorReportsLockState：只读诊断报告（不改锁状态）。
func TestDoctorReportsLockState(t *testing.T) {
	dataDir := t.TempDir()
	out, err := captureStdout(t, func() error {
		return cmdDoctor([]string{"--data-dir", dataDir, "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeSingleJSON(t, out)
	if doc["executionLock"] != "free" {
		t.Fatalf("lock state = %v, want free (%s)", doc["executionLock"], out)
	}
}
