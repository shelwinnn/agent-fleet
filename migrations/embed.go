// Package migrations 嵌入 SQLite 迁移脚本（架构 v1.1.2 §3.6：migrations/ 于仓库根）。
// 脚本按文件名顺序应用，已应用脚本禁止修改（§10.1 / NFR-5）。
package migrations

import "embed"

// FS 持有仓库 migrations/ 目录下的全部 *.sql 脚本。
//
//go:embed *.sql
var FS embed.FS
