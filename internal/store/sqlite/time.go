package sqlite

import "time"

// nowStamp 返回统一的 RFC3339Nano UTC 时间戳（与既有各仓储一致）。
func nowStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// timeNowUTC 返回当前 UTC 时间（time.Now().UTC() 的具名包装，便于阅读）。
func timeNowUTC() time.Time { return time.Now().UTC() }
