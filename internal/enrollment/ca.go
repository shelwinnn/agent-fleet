package enrollment

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA 是 Fleet CA 及其签发的服务端证书（§4.6：首启本地生成自签根；
// CA 私钥由产品全程留在控制面主机，文件权限 0600，§29.7）。
type CA struct {
	mu sync.Mutex

	dir      string // pki 目录（默认 <data-dir>/pki）
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	caPEM    []byte
	server   tls.Certificate
	serverCN string
}

// 文件名遵循 §10.2 布局：pki/{ca.crt, ca.key(0600), server.crt, server.key(0600)}。
const (
	caCertFile   = "ca.crt"
	caKeyFile    = "ca.key"
	serverCert   = "server.crt"
	serverKey    = "server.key"
	secretMode   = 0o600 // spec §12.2/§29.7：私钥文件 0600
	CNPrefix     = "agent-fleet"
	ServerCN     = CNPrefix + "-server"
	CertValidity = 365 * 24 * time.Hour // 服务端证书有效期
	clientCertTL = 90 * 24 * time.Hour  // 客户端证书默认有效期（有限期，spec §12.2）
)

// EnsureCA 加载既有 CA 材料，或在首启时生成本地 Fleet CA 与服务端证书。
// 私钥文件以 0600 写入，并对既有文件做启动自检（§29.7：0600）。
func EnsureCA(pkiDir string) (*CA, error) {
	if err := os.MkdirAll(pkiDir, 0o755); err != nil {
		return nil, fmt.Errorf("enrollment: create pki dir: %w", err)
	}
	ca := &CA{dir: pkiDir, serverCN: ServerCN}
	if err := ca.loadOrCreateRoot(); err != nil {
		return nil, err
	}
	if err := ca.loadOrCreateServerCert(); err != nil {
		return nil, err
	}
	return ca, nil
}

func (c *CA) loadOrCreateRoot() error {
	certPath := filepath.Join(c.dir, caCertFile)
	keyPath := filepath.Join(c.dir, caKeyFile)
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	switch {
	case certErr == nil && keyErr == nil:
		// 既有材料：校验配对与权限（§4.6 恢复前检查的常态子集）。
		cert, err := parseCertPEM(certPEM)
		if err != nil {
			return fmt.Errorf("enrollment: parse %s: %w", certPath, err)
		}
		key, err := parseKeyPEM(keyPEM)
		if err != nil {
			return fmt.Errorf("enrollment: parse %s: %w", keyPath, err)
		}
		if cert.KeyUsage&x509.KeyUsageCertSign == 0 || !cert.IsCA {
			return fmt.Errorf("enrollment: %s is not a CA certificate", certPath)
		}
		if err := checkKeyMatches(cert, key); err != nil {
			return fmt.Errorf("enrollment: CA cert/key mismatch in %s: %w", certPath, err)
		}
		if err := checkSecretPerm(keyPath); err != nil {
			return err
		}
		c.caCert, c.caKey, c.caPEM = cert, key, certPEM
		return nil
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		// 首启：生成本地自签 CA（§4.6）。
		return c.createRoot(certPath, keyPath)
	default:
		return fmt.Errorf("enrollment: CA material incomplete in %s: cert err=%v key err=%v "+
			"(see docs/manual-ca-backup-recovery.md for recovery)",
			c.dir, certErr, keyErr)
	}
}

func (c *CA) createRoot(certPath, keyPath string) error {
	key, err := newKey()
	if err != nil {
		return fmt.Errorf("enrollment: generate CA key: %w", err)
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: CNPrefix + " Fleet CA", Organization: []string{CNPrefix}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(randReader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("enrollment: create CA cert: %w", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := marshalKey(key)
	if err != nil {
		return err
	}
	if err := writeSecret(keyPath, keyPEM); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, caPEM, 0o644); err != nil {
		return fmt.Errorf("enrollment: write %s: %w", certPath, err)
	}
	c.caCert, _ = x509.ParseCertificate(der)
	c.caKey, c.caPEM = key, caPEM
	return nil
}

func (c *CA) loadOrCreateServerCert() error {
	certPath := filepath.Join(c.dir, serverCert)
	keyPath := filepath.Join(c.dir, serverKey)
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)

	switch {
	case certErr == nil && keyErr == nil:
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("enrollment: parse %s/%s: %w", certPath, keyPath, err)
		}
		if err := checkSecretPerm(keyPath); err != nil {
			return err
		}
		if err := c.verifyOwn(cert.Leaf, x509.ExtKeyUsageServerAuth); err != nil {
			return fmt.Errorf("enrollment: server certificate not issued by fleet CA: %w", err)
		}
		c.server = cert
		return nil
	case errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist):
		return c.createServerCert(certPath, keyPath)
	default:
		return fmt.Errorf("enrollment: server cert material incomplete in %s: cert err=%v key err=%v",
			c.dir, certErr, keyErr)
	}
}

func (c *CA) createServerCert(certPath, keyPath string) error {
	key, err := newKey()
	if err != nil {
		return fmt.Errorf("enrollment: generate server key: %w", err)
	}
	now := time.Now()
	host, _ := os.Hostname()
	dnsNames := []string{"localhost"}
	if host != "" {
		dnsNames = append(dnsNames, host)
	}
	tpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: ServerCN, Organization: []string{CNPrefix}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(CertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  loopbackIPs(),
	}
	der, err := x509.CreateCertificate(randReader, tpl, c.caCert, &key.PublicKey, c.caKey)
	if err != nil {
		return fmt.Errorf("enrollment: create server cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := marshalKey(key)
	if err != nil {
		return err
	}
	if err := writeSecret(keyPath, keyPEM); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("enrollment: write %s: %w", certPath, err)
	}
	leaf, _ := x509.ParseCertificate(der)
	c.server = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	return nil
}

// IssueClientCertificate 校验 CSR（自签名有效）并以 Fleet CA 签发客户端证书。
// 证书 CN 一律取服务端认证的 machine 身份，不信任 CSR 自报的 Subject（§7.5 身份绑定）。
func (c *CA) IssueClientCertificate(csrPEM []byte, machine string, validity time.Duration) (certPEM []byte, serial string, notAfter time.Time, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	csr, err := ParseCSR(csrPEM)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if machine == "" {
		return nil, "", time.Time{}, fmt.Errorf("%w: machine id is empty", ErrInvalidCSR)
	}
	if validity <= 0 {
		validity = clientCertTL
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: machine, Organization: []string{CNPrefix}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(randReader, tpl, c.caCert, csr.PublicKey, c.caKey)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("enrollment: issue client cert: %w", err)
	}
	serial = fmt.Sprintf("%x", tpl.SerialNumber)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), serial, tpl.NotAfter, nil
}

// CACertPEM 返回 CA 证书 PEM（下发 agentd 冗余持有）。
func (c *CA) CACertPEM() []byte { return c.caPEM }

// CACertPool 返回以 Fleet CA 为根的校验池（服务端校验客户端证书）。
func (c *CA) CACertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.caCert)
	return pool
}

// ServerTLSConfig 返回 gRPC 监听用的 TLS 配置：强制 TLS + 服务端证书，
// 客户端证书"有则必须有效"——Enroll 不要求客户端证书（§7.5），而
// FleetAgentService/RenewCertificate 在拦截器层强制要求（mTLS）。
func (c *CA) ServerTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{c.server},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    c.CACertPool(),
	}
}

// verifyOwn 校验 leaf 由本 CA 签发且 EKU 匹配（服务端证书自检）。
func (c *CA) verifyOwn(leaf *x509.Certificate, eku x509.ExtKeyUsage) error {
	if leaf == nil {
		return errors.New("missing leaf")
	}
	chains, err := leaf.Verify(x509.VerifyOptions{
		Roots:     c.CACertPool(),
		KeyUsages: []x509.ExtKeyUsage{eku},
	})
	if err != nil {
		return err
	}
	if len(chains) == 0 {
		return errors.New("no valid chain")
	}
	return nil
}

// ---- 小工具 ----

// writeSecret 以 0600 写入私钥材料（写入时显式 chmod，§29.7）。
func writeSecret(path string, pemBytes []byte) error {
	if err := os.WriteFile(path, pemBytes, secretMode); err != nil {
		return fmt.Errorf("enrollment: write %s: %w", path, err)
	}
	if err := os.Chmod(path, secretMode); err != nil {
		return fmt.Errorf("enrollment: chmod %s: %w", path, err)
	}
	return nil
}

// checkSecretPerm 启动自检：私钥文件必须 0600（§29.7）。
func checkSecretPerm(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("enrollment: stat %s: %w", path, err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		// 自愈为 0600 并继续（防止 umask 变更导致的意外放宽）。
		if err := os.Chmod(path, secretMode); err != nil {
			return fmt.Errorf("enrollment: %s has mode %o, want 0600 (chmod failed: %v)", path, perm, err)
		}
	}
	return nil
}

func parseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("expect PEM CERTIFICATE")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("expect PEM private key")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		// 兼容 PKCS#8。
		if k8, err8 := x509.ParsePKCS8PrivateKey(block.Bytes); err8 == nil {
			if ec, ok := k8.(*ecdsa.PrivateKey); ok {
				return ec, nil
			}
		}
		return nil, err
	}
	return key, nil
}

func marshalKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("enrollment: marshal key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// checkKeyMatches 校验证书与私钥配对（§4.6 恢复前检查项之一）。
func checkKeyMatches(cert *x509.Certificate, key *ecdsa.PrivateKey) error {
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return ErrUnsupportedKey
	}
	if pub.X.Cmp(key.X) != 0 || pub.Y.Cmp(key.Y) != 0 {
		return errors.New("public key mismatch")
	}
	return nil
}
