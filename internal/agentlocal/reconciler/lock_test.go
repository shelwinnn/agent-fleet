package reconciler

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// T1（§5.6/§13.1）：执行权互斥、陈旧锁不静默夺取、显式恢复。
// "跨进程"以真实子进程承载：持锁 pid 是独立进程，杀掉后观察陈旧锁判定。
func TestExecutionLockCrossProcessSemantics(t *testing.T) {
	dir := t.TempDir()
	lock := NewExecutionLock(filepath.Join(dir, "data"))

	// 1) 取得后释放，可再取得。
	if err := lock.Acquire("daemon", "op-a"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// 2) 锁被另一个存活进程持有 → LockHeldError（oneshot 据此 NodeBusy 失败）。
	externalPID := startExternalHolder(t, filepath.Join(dir, "data", "state", "execution.lock"))
	var heldErr *LockHeldError
	err := lock.Acquire("oneshot", "op-c")
	if !errors.As(err, &heldErr) {
		t.Fatalf("acquire with live external holder = %v, want LockHeldError", err)
	}
	if heldErr.Held.OwnerPID != externalPID {
		t.Fatalf("held pid = %d, want %d", heldErr.Held.OwnerPID, externalPID)
	}

	// 3) 持有者死亡且 staging 未确认清理 → 陈旧锁，拒绝静默夺取（规则 5）。
	if err := exec.Command("kill", "-9", strconv.Itoa(externalPID)).Run(); err != nil {
		t.Fatalf("kill external holder: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(externalPID) {
		if time.Now().After(deadline) {
			t.Fatal("external holder pid still alive")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var staleErr *StaleLockError
	err = lock.Acquire("oneshot", "op-d")
	if !errors.As(err, &staleErr) {
		t.Fatalf("acquire with dead holder = %v, want StaleLockError", err)
	}

	// 4) 显式恢复（doctor --recover-lock 的核心动作）后可重新取得。
	if err := lock.Recover(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if err := lock.Acquire("oneshot", "op-e"); err != nil {
		t.Fatalf("acquire after recover: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

// startExternalHolder 启动一个存活的外部进程，让它以一个仍存活的 pid 写合法锁
// 文件（无 StagingCleaned 标记 → 视为存在未清理暂存产物）。
func startExternalHolder(t *testing.T, lockPath string) int {
	t.Helper()
	script := `sleep 30 & p=$!; ` +
		`printf '{"ownerPid":%d,"channel":"oneshot","operationId":"op-ext","acquiredAt":"2026-01-01T00:00:00Z"}' $p > ` + lockPath + `; ` +
		`echo $p; wait $p`
	cmd := exec.Command("sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start external holder: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// 读回 sh 打印的内部 pid（即锁文件 ownerPid）。
	buf := make([]byte, 32)
	n, err := stdout.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("read external pid: %v", err)
	}
	pid, convErr := strconv.Atoi(trimSpace(string(buf[:n])))
	if convErr != nil {
		t.Fatalf("parse external pid %q: %v", string(buf[:n]), convErr)
	}
	// 等锁文件就位。
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(lockPath)
		if err == nil && contains(string(b), `"ownerPid"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("external holder did not write lock file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pid
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// 防御：并发 Acquire 恰有一个赢家（O_CREAT|O_EXCL 原子性）。
func TestExecutionLockConcurrentAcquire(t *testing.T) {
	dir := t.TempDir()
	lock := NewExecutionLock(filepath.Join(dir, "data"))
	const n = 8
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- lock.Acquire("daemon", "op-race")
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}
