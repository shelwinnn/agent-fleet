// Package enrollment 实现 Fleet CA 与 enrollment token 生命周期
// （架构 v1.1.2 §3.6 enrollment/、§4.6、§7.5）：
//   - CA（自签根 + 服务端/客户端证书签发，首启本地生成，私钥 0600）；
//   - token 签发与原子消费（一次性、短时效、绑定 machineId 与 CSR 指纹）；
//   - CSR 校验与公钥指纹；
//   - enroll 端点的按源 IP 失败限速（S3 升为强制）。
//
// gRPC 处理器（FleetEnrollmentService/FleetAgentService）在 server/grpcagent 包，
// 本包只承载可独立测试的核心逻辑。
package enrollment

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
)

// CSR 相关错误（服务端映射 gRPC InvalidArgument / PermissionDenied）。
var (
	ErrInvalidCSR     = errors.New("invalid csr")
	ErrUnsupportedKey = errors.New("unsupported public key algorithm")
)

// FingerprintPrefix 是公钥指纹的前缀（§4.7 digest 风格）。
const FingerprintPrefix = "sha256:"

// PublicKeyFingerprint 计算公钥指纹 "sha256:<hex(SHA-256(SPKI DER)>)"。
// token 绑定与 agentd 引导输出共用同一实现（§4.6：token 绑定 CSR 公钥指纹）。
func PublicKeyFingerprint(pub any) (string, error) {
	if pub == nil {
		return "", fmt.Errorf("%w: nil public key", ErrInvalidCSR)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("%w: marshal public key: %v", ErrInvalidCSR, err)
	}
	sum := sha256.Sum256(der)
	return FingerprintPrefix + hex.EncodeToString(sum[:]), nil
}

// ParseCSR 解析 PEM 编码的 PKCS#10 CSR 并校验自签名有效（§4.6：CSR 校验）。
func ParseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("%w: expect PEM block CERTIFICATE REQUEST", ErrInvalidCSR)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidCSR, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: signature: %v", ErrInvalidCSR, err)
	}
	return csr, nil
}

// CSRFingerprint 解析 CSR 并返回其公钥指纹。
func CSRFingerprint(csrPEM []byte) (string, error) {
	csr, err := ParseCSR(csrPEM)
	if err != nil {
		return "", err
	}
	return PublicKeyFingerprint(csr.PublicKey)
}

// randomToken 生成熵 ≥128-bit 的 URL 安全 token（§4.6；32 字节 = 256-bit）。
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("enrollment: rand: %w", err)
	}
	const urlSafe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = urlSafe[int(v)%len(urlSafe)]
	}
	return string(out), nil
}

// newKey 生成 CA/服务端密钥（ECDSA P-256：MVP 单操作者规模下的性能与安全折中）。
func newKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}
