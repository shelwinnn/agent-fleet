package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/shelwinnn/agent-fleet/internal/enrollment"
)

// 节点侧证书材料管理（§10.2 节点布局：pki/{client.crt, client.key(0600), ca.crt}）。
// 私钥只在节点本地生成与保存（spec §12.1 第 5/9 步）。

// ensureKeyAndCSR 确保存在本地私钥（0600）与对应 CSR；返回 CSR PEM 与公钥指纹。
// 幂等：材料已存在时直接复用。
func ensureKeyAndCSR(cfg *Config) (csrPEM []byte, fingerprint string, err error) {
	keyPath, csrPath := cfg.clientKeyPath(), cfg.csrPath()
	if err := os.MkdirAll(cfg.pkiDir(), 0o755); err != nil {
		return nil, "", fmt.Errorf("create pki dir: %w", err)
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, "", err
	}
	if b, err := os.ReadFile(csrPath); err == nil {
		if fp, fpErr := enrollment.CSRFingerprint(b); fpErr == nil {
			return b, fp, nil
		}
		// CSR 损坏则重建。
	}
	tpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: cfg.MachineID, Organization: []string{"agent-fleet"}},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tpl, key)
	if err != nil {
		return nil, "", fmt.Errorf("create csr: %w", err)
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	if err := os.WriteFile(csrPath, csrPEM, 0o644); err != nil {
		return nil, "", fmt.Errorf("write %s: %w", csrPath, err)
	}
	fp, err := enrollment.PublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		return nil, "", err
	}
	return csrPEM, fp, nil
}

func loadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("parse %s: no PEM block", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return key, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil { // spec §12.1 第 9 步：0600
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// storeClientCert 把签发的客户端证书写入 pki/client.crt（0600）。
func storeClientCert(cfg *Config, certPEM []byte) error {
	path := cfg.clientCertPath()
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

// loadClientTLS 装载客户端证书材料并构建出站 TLS 配置
// （mTLS：客户端证书 + 校验服务端证书的 CA 池；不放宽校验，FR-11.4）。
func loadClientTLS(cfg *Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.clientCertPath(), cfg.clientKeyPath())
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.CACert)
	if err != nil {
		return nil, fmt.Errorf("read ca cert %s: %w", cfg.CACert, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no valid certificate in %s", cfg.CACert)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   cfg.ServerName,
	}, nil
}
