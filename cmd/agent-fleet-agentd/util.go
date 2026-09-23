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

// renewalDelay 返回续期触发前的随机抖动延迟（§4.6：续期窗口加随机抖动，
// 避免全机群续期对齐到同一时刻）。窗口内均匀取点。
func renewalDelay(notAfter time.Time, window time.Duration) time.Duration {
	deadline := notAfter.Add(-window)
	d := time.Until(deadline)
	if d <= 0 {
		return 0 // 已进入窗口，立即续期
	}
	return time.Duration(rand.Int63n(int64(d)))
}
