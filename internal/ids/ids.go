// Package ids 生成服务端管理的资源标识（RFC 4122 v4，crypto/rand）。
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// NewUID 返回一个随机 UUIDv4 字符串。
func NewUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
