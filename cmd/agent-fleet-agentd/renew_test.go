package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math"
	"math/big"
	mrand "math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖 §4.6 证书生命周期的三处缺陷回归：
//  1. 续期必须能重排（每次尝试后按磁盘证书到期时刻排下一次）；
//  2. 出站 mTLS 凭据必须能在续期替换文件后按需重载（GetClientCertificate）；
//  3. 续期抖动的取点必须落在续期窗口内部，且绝不晚于到期时刻。

// testClientCert 是测试用自签客户端证书的标识（用于区分"续期前/后"的证书）。
type testClientCert struct {
	serial   int64
	notAfter time.Time
}

// writeTestClientCert 用 crypto/x509 + crypto/ecdsa 生成一对自签客户端证书/私钥
// （不引入新依赖）并写入给定路径；返回落盘证书的序列号与到期时刻供断言。
func writeTestClientCert(t *testing.T, certPath, keyPath string, serial int64, notAfter time.Time) testClientCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "test-node", Organization: []string{"agent-fleet"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(crand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	leaf, err := x509.ParseCertificate(der) // 取编码后的真实值（ASN.1 时间精度到秒）
	if err != nil {
		t.Fatalf("parse generated certificate: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return testClientCert{serial: serial, notAfter: leaf.NotAfter}
}

// clientCertFromTLS 经 tls.Config.GetClientCertificate 取客户端证书并解析叶证书
// （出站 mTLS 的客户端证书只应经该回调按需提供）。
func clientCertFromTLS(t *testing.T, tlsCfg *tls.Config) (*tls.Certificate, *x509.Certificate) {
	t.Helper()
	if tlsCfg.GetClientCertificate == nil {
		t.Fatal("tls.Config.GetClientCertificate is not set")
	}
	got, err := tlsCfg.GetClientCertificate(&tls.CertificateRequestInfo{})
	if err != nil {
		t.Fatalf("GetClientCertificate: %v", err)
	}
	if got == nil || len(got.Certificate) == 0 {
		t.Fatal("GetClientCertificate returned no certificate")
	}
	leaf, err := x509.ParseCertificate(got.Certificate[0])
	if err != nil {
		t.Fatalf("parse client certificate: %v", err)
	}
	return got, leaf
}

// TestRenewalDelaySamplesInsideRenewWindow 断言抖动取点落在续期窗口内部
// [notAfter-window, notAfter)：旧实现在 [now, notAfter-window] 取点（进入窗口之前，
// 往往过早续期），本用例的最小延迟断言会直接抓住它。
func TestRenewalDelaySamplesInsideRenewWindow(t *testing.T) {
	now := time.Now()
	const window = 7 * 24 * time.Hour
	notAfter := now.Add(90 * 24 * time.Hour)
	windowStart := notAfter.Add(-window)
	rnd := mrand.New(mrand.NewSource(20260923))

	minOffset, maxOffset := time.Duration(math.MaxInt64), time.Duration(0)
	toWindowStart := windowStart.Sub(now) // 距窗口开始的固定偏移，抖动是它之上的增量
	const samples = 200
	for i := 0; i < samples; i++ {
		d := renewalDelay(now, notAfter, window, rnd)
		at := now.Add(d)
		if at.Before(windowStart) {
			t.Fatalf("sample %d renews before the window opens: now+%v < window start (old buggy range)", i, d)
		}
		if !at.Before(notAfter) {
			t.Fatalf("sample %d lands at/after expiry: now+%v, remaining %v", i, d, notAfter.Sub(now))
		}
		offset := d - toWindowStart // 窗口内部的取点位置 ∈ [0, window)
		minOffset = min(minOffset, offset)
		maxOffset = max(maxOffset, offset)
	}
	// 抖动要真的散开在窗口内部，而不是固定点。
	if minOffset >= window/10 {
		t.Fatalf("min in-window offset %v: jitter never reaches the start of the window", minOffset)
	}
	if maxOffset <= window*9/10 {
		t.Fatalf("max in-window offset %v: jitter never reaches the end of the window", maxOffset)
	}
}

// TestRenewalDelayInWindowKeepsMargin 断言已进入窗口时返回小抖动延迟，且仍明显
// 早于到期（保留余量，绝不晚于到期）。
func TestRenewalDelayInWindowKeepsMargin(t *testing.T) {
	now := time.Now()
	const window = 7 * 24 * time.Hour
	notAfter := now.Add(2 * time.Hour) // 已在窗口内，只剩 2h
	remaining := notAfter.Sub(now)
	rnd := mrand.New(mrand.NewSource(7))

	for i := 0; i < 200; i++ {
		d := renewalDelay(now, notAfter, window, rnd)
		if d < 0 || d >= remaining {
			t.Fatalf("in-window delay %v must stay strictly before expiry (remaining %v)", d, remaining)
		}
		if d >= remaining/2 {
			t.Fatalf("in-window delay %v must keep a margin before expiry (remaining %v)", d, remaining)
		}
	}
}

// TestRenewalDelayBoundaries 固定窗口边界行为：刚进窗口、窗口之前、到期时刻、
// 已过期、窗口未配置、临近到期。
func TestRenewalDelayBoundaries(t *testing.T) {
	now := time.Now()
	const window = 7 * 24 * time.Hour
	notAfter := now.Add(90 * 24 * time.Hour)
	windowStart := notAfter.Add(-window)
	rnd := mrand.New(mrand.NewSource(3))

	// 刚进入窗口（now == 窗口开始）：小抖动，仍早于到期。
	at := windowStart.Add(renewalDelay(windowStart, notAfter, window, rnd))
	if !at.After(windowStart) || !at.Before(notAfter) {
		t.Fatalf("window-start schedule %v outside (%v, %v)", at, windowStart, notAfter)
	}

	// 窗口之前：不得提前到窗口开始之前续期。
	at = now.Add(renewalDelay(now, notAfter, window, rnd))
	if at.Before(windowStart) || !at.Before(notAfter) {
		t.Fatalf("pre-window schedule %v outside [%v, %v)", at, windowStart, notAfter)
	}

	// 到期时刻与已过期：立即续期（0），不得再等。
	for _, expiredAt := range []time.Time{notAfter, notAfter.Add(time.Hour)} {
		if got := renewalDelay(expiredAt, notAfter, window, rnd); got != 0 {
			t.Fatalf("delay at/after expiry = %v, want 0 (renew immediately)", got)
		}
	}

	// 窗口未配置（≤0）不得 panic，且结果仍早于到期。
	if got := renewalDelay(now, notAfter, 0, rnd); !now.Add(got).Before(notAfter) {
		t.Fatalf("zero-window delay %v must stay before expiry", got)
	}

	// 临近到期（剩 1ns）：必须为 0，绝不越过到期时刻。
	if got := renewalDelay(notAfter.Add(-time.Nanosecond), notAfter, window, rnd); got != 0 {
		t.Fatalf("near-expiry delay = %v, want 0", got)
	}
}

// TestLoadClientTLSReloadsRenewedCertificate 覆盖缺陷 2：启动时一次性装载
// （Certificates 字段）会让续期后的旧证书一直被 gRPC 使用；改为
// GetClientCertificate 按需重载后，磁盘替换（=续期落盘）必须立刻生效。
func TestLoadClientTLSReloadsRenewedCertificate(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{DataDir: dir, ServerName: "fleet.test", CACert: filepath.Join(dir, "ca.crt")}
	first := writeTestClientCert(t, cfg.clientCertPath(), cfg.clientKeyPath(), 1, time.Now().Add(24*time.Hour))
	// CA 文件用同一份自签证书（只需可解析：用于校验服务端证书，FR-11.4）。
	caPEM, err := os.ReadFile(cfg.clientCertPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.CACert, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	tlsCfg, err := loadClientTLS(cfg)
	if err != nil {
		t.Fatalf("loadClientTLS: %v", err)
	}
	// 硬约束：服务端校验语义不得放宽。
	if tlsCfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must stay false (FR-11.4)")
	}
	if tlsCfg.RootCAs == nil || tlsCfg.MinVersion != tls.VersionTLS12 || tlsCfg.ServerName != "fleet.test" {
		t.Fatalf("server verification semantics changed: %+v", tlsCfg)
	}
	if tlsCfg.GetClientCertificate == nil || len(tlsCfg.Certificates) != 0 {
		t.Fatal("client certificate must be loaded on demand via GetClientCertificate")
	}

	_, leaf := clientCertFromTLS(t, tlsCfg)
	if leaf.SerialNumber.Int64() != first.serial || !leaf.NotAfter.Equal(first.notAfter) {
		t.Fatalf("initial client cert = serial %v notAfter %v, want serial %d notAfter %v",
			leaf.SerialNumber, leaf.NotAfter, first.serial, first.notAfter)
	}

	// 模拟续期落盘：直接替换证书与私钥（缓存判据是文件内容，不依赖 mtime 粒度）。
	second := writeTestClientCert(t, cfg.clientCertPath(), cfg.clientKeyPath(), 2, time.Now().Add(48*time.Hour))
	reloaded, leaf2 := clientCertFromTLS(t, tlsCfg)
	if leaf2.SerialNumber.Int64() != second.serial {
		t.Fatalf("renewed client cert not picked up: serial %v, want %d", leaf2.SerialNumber, second.serial)
	}
	if !leaf2.NotAfter.Equal(second.notAfter) || leaf2.NotAfter.Equal(first.notAfter) {
		t.Fatalf("renewed client cert notAfter %v, want %v (old %v)", leaf2.NotAfter, second.notAfter, first.notAfter)
	}

	// 文件未变化时复用已解析的证书（缓存生效，不重复解析）。
	again, _ := clientCertFromTLS(t, tlsCfg)
	if again != reloaded {
		t.Fatal("unchanged files must reuse the cached client certificate")
	}

	// 材料缺失必须直接失败，不得回退到旧证书或放宽校验。
	if err := os.Remove(cfg.clientCertPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsCfg.GetClientCertificate(&tls.CertificateRequestInfo{}); err == nil {
		t.Fatal("missing client certificate must fail, not silently fall back")
	}
}

// TestLoadClientTLSFailsOnMissingKeyPair 固定启动期校验：材料缺失时 loadClientTLS
// 必须报错（而不是构建出一个没有客户端证书的配置）。
func TestLoadClientTLSFailsOnMissingKeyPair(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{DataDir: dir, ServerName: "fleet.test", CACert: filepath.Join(dir, "ca.crt")}
	writeTestClientCert(t, filepath.Join(dir, "tmp.crt"), filepath.Join(dir, "tmp.key"), 9, time.Now().Add(time.Hour))
	caPEM, err := os.ReadFile(filepath.Join(dir, "tmp.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.CACert, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadClientTLS(cfg); err == nil {
		t.Fatal("loadClientTLS succeeded without pki/client.crt (must fail)")
	}
}

// TestRenewLoopReschedulesFromDiskExpiry 覆盖缺陷 1 的回归：一次续期尝试结束后必须
// 重新读取磁盘上 pki/client.crt 的到期时刻并据此排下一次（旧的"一次性定时器"不重排，
// 证书最终会过期）。
func TestRenewLoopReschedulesFromDiskExpiry(t *testing.T) {
	const window = 7 * 24 * time.Hour
	now := time.Now()
	var mu sync.Mutex
	expiry := now.Add(2 * time.Hour) // 已在窗口内 → 首轮排期是小抖动
	renewals := 0
	rescheduled := make(chan time.Duration, 4)

	deps := renewDeps{
		log:    quietLogger(),
		window: window,
		notAfter: func() (time.Time, error) {
			mu.Lock()
			defer mu.Unlock()
			return expiry, nil
		},
		renew: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			renewals++
			// 模拟服务端签发：新到期时刻 = 旧值 + 90d（磁盘文件已被续期替换）。
			expiry = expiry.Add(90 * 24 * time.Hour)
			return nil
		},
		now: func() time.Time { return now },
		rnd: mrand.New(mrand.NewSource(11)),
		wait: func(_ context.Context, d time.Duration) bool {
			if d >= 80*24*time.Hour { // 只有按"新证书"排期才可能这么大
				select {
				case rescheduled <- d:
				default:
				}
				return false // 模拟 ctx 取消：循环必须退出
			}
			return true // 窗口内抖动：不真等，直接推进
		},
	}

	done := make(chan struct{})
	go func() { deps.run(context.Background()); close(done) }()

	var next time.Duration
	select {
	case next = <-rescheduled:
	case <-time.After(2 * time.Second):
		t.Fatal("no renewal scheduled from the renewed certificate's expiry (loop did not reschedule)")
	}
	// 新证书（now+2h+90d）的窗口内：延迟 ∈ [83d+2h, 90d+2h)。
	if next < 83*24*time.Hour || next >= 90*24*time.Hour+2*time.Hour {
		t.Fatalf("next renewal delay %v is not inside the renewed certificate's window", next)
	}
	mu.Lock()
	got := renewals
	mu.Unlock()
	if got < 1 {
		t.Fatalf("renew called %d times, want >= 1", got)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("renew loop did not exit after wait signalled cancellation")
	}
}

// TestRenewLoopRetriesAfterFailureWithBackoff 覆盖"续期失败不得终止 daemon"：
// 失败后循环仍继续，并以退避重试（不忙等）。
func TestRenewLoopRetriesAfterFailureWithBackoff(t *testing.T) {
	now := time.Now()
	expiry := now.Add(-time.Minute) // 已过期：抖动延迟恒为 0，只能靠退避避免忙等
	renewals := 0
	var waits []time.Duration

	deps := renewDeps{
		log:    quietLogger(),
		window: renewWindow,
		notAfter: func() (time.Time, error) {
			return expiry, nil
		},
		renew: func(context.Context) error {
			renewals++
			return errors.New("control plane unreachable")
		},
		now: func() time.Time { return now },
		rnd: mrand.New(mrand.NewSource(5)),
		wait: func(_ context.Context, d time.Duration) bool {
			waits = append(waits, d)
			return len(waits) < 2 // 第二次退避后模拟 ctx 取消：循环退出
		},
	}
	deps.run(context.Background())

	if renewals < 2 {
		t.Fatalf("renew called %d times after a failure, want >= 2 (failure must not stop the daemon)", renewals)
	}
	if len(waits) != 2 {
		t.Fatalf("waits = %v, want exactly 2 retry backoffs", waits)
	}
	for i, d := range waits {
		if d != renewRetryBackoff {
			t.Fatalf("wait %d = %v, want retry backoff %v (no busy loop)", i, d, renewRetryBackoff)
		}
	}
}

// TestRenewLoopExitsOnContextCancel 覆盖"ctx 取消后循环退出"：真实等待实现必须被
// ctx 取消打断，daemon 退出不泄漏 goroutine。
func TestRenewLoopExitsOnContextCancel(t *testing.T) {
	now := time.Now()
	expiry := now.Add(90 * 24 * time.Hour) // 远在窗口之外 → 循环进入长等待
	enteredWait := make(chan struct{})
	var once sync.Once

	deps := renewDeps{
		log:      quietLogger(),
		notAfter: func() (time.Time, error) { return expiry, nil },
		renew: func(context.Context) error {
			t.Error("renew must not run before the renewal window opens")
			return nil
		},
		now: func() time.Time { return now },
		rnd: mrand.New(mrand.NewSource(13)),
		wait: func(ctx context.Context, d time.Duration) bool {
			once.Do(func() { close(enteredWait) })
			return waitContext(ctx, d) // 真实等待：只能被 ctx 取消打断
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { deps.run(ctx); close(done) }()

	select {
	case <-enteredWait:
	case <-time.After(2 * time.Second):
		t.Fatal("renew loop did not schedule the first renewal")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("renew loop did not exit after ctx cancel (goroutine leak)")
	}
}
