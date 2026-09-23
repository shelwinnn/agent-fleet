package reconciler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// 节点本地跨进程执行权（架构 v1.1.2 §5.6）：daemon 与 oneshot 共享同一把锁，
// 是"任一时刻某台机器至多一条变更流水线"的双重表达之一（FR-9.9，与服务端
// 持久化互斥缺一不可）。锁文件 O_CREAT|O_EXCL 原子创建，内容为持有者元数据。

// LockFile 是执行权锁文件的 JSON 内容（§5.6）。
type LockFile struct {
	OwnerPID       int       `json:"ownerPid"`
	Channel        string    `json:"channel"` // daemon | oneshot
	OperationID    string    `json:"operationId"`
	AcquiredAt     time.Time `json:"acquiredAt"`
	StagingCleaned bool      `json:"stagingCleaned,omitempty"`
}

// ExecutionLock 管理锁文件的取得/释放/陈旧判定。
type ExecutionLock struct {
	// Path 是锁文件绝对路径（<data>/state/execution.lock，§5.6）。
	Path string
}

func NewExecutionLock(dataDir string) *ExecutionLock {
	return &ExecutionLock{Path: filepath.Join(dataDir, "state", "execution.lock")}
}

// Acquire 取得执行权。锁被他人持有时返回 *LockHeldError（daemon 据此排队、
// oneshot 据此以 NodeBusy 失败，§5.6 规则 3/4）；陈旧锁不静默夺取——返回
// *StaleLockError，要求显式处理（规则 5）。
func (l *ExecutionLock) Acquire(channel, operationID string) error {
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return fmt.Errorf("reconciler: create state dir: %w", err)
	}
	lf := LockFile{
		OwnerPID:    os.Getpid(),
		Channel:     channel,
		OperationID: operationID,
		AcquiredAt:  time.Now().UTC(),
	}
	b, _ := json.Marshal(lf)
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		if _, err := f.Write(b); err != nil {
			f.Close()
			return fmt.Errorf("reconciler: write lock file: %w", err)
		}
		return f.Close()
	}
	if !os.IsExist(err) {
		return fmt.Errorf("reconciler: create lock file: %w", err)
	}
	// 锁已存在：判定持有者是否存活（规则 5/6）。
	cur, rerr := l.load()
	if rerr != nil {
		// 锁文件不可解析：无法判定，按不可静默夺取处理。
		return &StaleLockError{Reason: fmt.Sprintf("lock file unparseable: %v", rerr)}
	}
	if cur.OwnerPID == os.Getpid() {
		// 同进程重复取得：单 worker 串行下不应发生，防御性拒绝。
		return &LockHeldError{Held: *cur}
	}
	if processAlive(cur.OwnerPID) {
		return &LockHeldError{Held: *cur}
	}
	// 持有者已死 → 陈旧锁。但陈旧 ≠ 可立即夺取：只有确认"无未清理暂存产物"
	// （这里以 StagingCleaned 标记表达）才可安全夺取，否则要求显式处理。
	if !cur.StagingCleaned {
		return &StaleLockError{Reason: fmt.Sprintf(
			"stale lock held by dead pid %d (op %s) with unverified staging; explicit recovery required",
			cur.OwnerPID, cur.OperationID), Stale: *cur}
	}
	// 可安全夺锁（记录一条告警语义由调用方日志承载）。
	if err := os.Remove(l.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reconciler: remove stale lock: %w", err)
	}
	f, err = os.OpenFile(l.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		// 并发夺取失败：另一个进程抢先，按锁被持有处理。
		if cur2, rerr := l.load(); rerr == nil {
			return &LockHeldError{Held: *cur2}
		}
		return fmt.Errorf("reconciler: re-create lock file: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("reconciler: write lock file: %w", err)
	}
	return f.Close()
}

// Release 释放执行权（§5.6 规则 2：流水线终结且结果已记录后释放）。
func (l *ExecutionLock) Release() error {
	cur, err := l.load()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur.OwnerPID != os.Getpid() {
		return fmt.Errorf("reconciler: not lock owner (held by pid %d)", cur.OwnerPID)
	}
	err = os.Remove(l.Path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Recover 显式清理陈旧锁（§5.6 规则 5 的操作者显式处理路径；对应
// `agentd doctor --recover-lock` 的核心动作，MVP 以库函数形态提供）。
func (l *ExecutionLock) Recover() error {
	cur, err := l.load()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if processAlive(cur.OwnerPID) {
		return fmt.Errorf("reconciler: lock holder pid %d is alive; refuse to recover", cur.OwnerPID)
	}
	if err := os.Remove(l.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *ExecutionLock) load() (*LockFile, error) {
	b, err := os.ReadFile(l.Path)
	if err != nil {
		return nil, err
	}
	var lf LockFile
	if err := json.Unmarshal(b, &lf); err != nil {
		return nil, err
	}
	if lf.OwnerPID == 0 {
		return nil, fmt.Errorf("lock file missing ownerPid")
	}
	return &lf, nil
}

// LockHeldError 表示执行权被存活进程持有（§5.6：daemon 排队 / oneshot NodeBusy）。
type LockHeldError struct{ Held LockFile }

func (e *LockHeldError) Error() string {
	return fmt.Sprintf("execution lock held by pid %d (%s, op %s)",
		e.Held.OwnerPID, e.Held.Channel, e.Held.OperationID)
}

// StaleLockError 表示陈旧锁且不可静默夺取（§5.6 规则 5：要求显式处理）。
type StaleLockError struct {
	Reason string
	Stale  LockFile
}

func (e *StaleLockError) Error() string { return e.Reason }

// processAlive 用 kill(pid, 0) 判定进程存活（ESRCH=不存在，EPERM=存在但无权限）。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Windows 无 syscall.Signal；本仓库目标平台为 Linux/macOS（spec 运行假设）。
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
