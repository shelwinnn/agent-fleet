package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/rand"
	"os"
	"time"
)

// x509Pool 从 PEM 内容构建证书池（无效内容返回 nil）。
func x509Pool(caPEM []byte) *x509.CertPool {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil
	}
	return pool
}

// certNotAfter 读取 PEM 证书的到期时刻。
func certNotAfter(certPath string) (time.Time, error) {
	b, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return time.Time{}, fmt.Errorf("no PEM block in %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

// renewalDelay 返回距下一次续期触发的随机抖动延迟（§4.6：续期窗口加随机抖动，
// 避免全机群续期对齐到同一时刻，S4）。取点在**续期窗口内部** [notAfter-window,
// notAfter)，而不是"进入窗口之前"：
//   - 尚未进入窗口（now < notAfter-window）：先等到窗口开始，再在窗口内均匀取点；
//   - 已在窗口内：返回一个小抖动延迟，仍明显早于到期；
//   - 已到期/已过期：返回 0（立即续期，不得再等）。
//
// 结果恒满足 now+delay < notAfter（绝不晚于到期时刻）。now/rnd 由调用方注入，
// 便于测试；不使用 math/rand 全局源。
func renewalDelay(now, notAfter time.Time, window time.Duration, rnd *rand.Rand) time.Duration {
	if !now.Before(notAfter) {
		return 0 // 已到期：立即续期
	}
	remaining := notAfter.Sub(now)
	if window <= 0 {
		// 窗口未配置：退化为剩余有效期内取点（保留一半余量），仍不晚于到期。
		return randBelow(rnd, remaining/2)
	}
	start := notAfter.Add(-window)
	if now.Before(start) {
		// 未进入窗口：等到窗口开始，再在窗口内部均匀取点。
		return start.Sub(now) + randBelow(rnd, window)
	}
	// 已在窗口内：只做小抖动（窗口的 1/10，且最多消耗剩余有效期的一半），
	// 保证续期有足够余量、且严格早于到期。
	jitter := window / 10
	if half := remaining / 2; jitter > half {
		jitter = half
	}
	return randBelow(rnd, jitter)
}

// randBelow 返回 [0, n) 内的均匀随机时长（n ≤ 0 或 rnd 为空时返回 0）。
func randBelow(rnd *rand.Rand, n time.Duration) time.Duration {
	if rnd == nil || n <= 0 {
		return 0
	}
	return time.Duration(rnd.Int63n(int64(n)))
}
