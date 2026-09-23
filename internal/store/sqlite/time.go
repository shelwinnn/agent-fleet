package sqlite

import "time"

// nowStamp 返回统一的 RFC3339Nano UTC 时间戳（与既有各仓储一致）。
func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
