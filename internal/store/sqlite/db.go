// Package sqlite 是仓储接口的 SQLite 实现（架构 v1.1.2 §4.9）：
// 单文件库 + 显式迁移 + WAL；不引入 ORM。
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，驱动名 "sqlite"
)

// DB 包装 *sql.DB，是各仓储的宿主。
type DB struct {
	sql *sql.DB
	// changes 是"写入成功后发布变更"的通知槽（SSE 事件枢纽的数据源，见 change.go）。
	changes *changeNotifier
}

// Open 打开（必要时创建目录）单文件 SQLite 库，并按 §4.9/§12.1 显式设置
// WAL 与 busy_timeout（后者必须显式配置，不能依赖驱动默认）。
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("sqlite: create data dir: %w", err)
		}
	}
	// pragma 经 DSN 逐连接生效：busy_timeout 5s（§12.1 建议）、WAL（§4.9）、
	// 事务以 IMMEDIATE 起始——写锁在事务开始即取得，避免"读事务升级写事务"
	// 时 busy_timeout 不生效的 SQLITE_BUSY_SNAPSHOT（KM-21 核查发现 #2；
	// §12.1：写串行化，避免多连接互相制造 SQLITE_BUSY）。
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	// 保险起见再对连接池整体确认一次 WAL（幂等）。
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	return &DB{sql: sqlDB, changes: &changeNotifier{}}, nil
}

// Ping 探活，供 /readyz 使用。
func (d *DB) Ping() error { return d.sql.Ping() }

// PingContext 带超时上下文的探活。
func (d *DB) PingContext(ctx context.Context) error { return d.sql.PingContext(ctx) }

// Close 关闭底层连接池。
func (d *DB) Close() error { return d.sql.Close() }
