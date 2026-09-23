// Package grpcagent 实现 agent gRPC 端点（架构 v1.1.2 §3.6 server/grpcagent/）：
// mTLS 身份拦截器、连接注册表（同一 machineId 至多一条活跃 Connect 流）、
// enrollment 处理器与 Connect 流处理。
package grpcagent

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// ProtocolVersionV1 是本片协议主版本（§7.6：服务端拒绝不支持的主版本）。
const ProtocolVersionV1 = "1"

type identityKey struct{}

// Identity 是经 mTLS 客户端证书认证的 agent 身份（§7.5 身份绑定：
// machine 取自证书 CN，serial 为证书序列号）。
type Identity struct {
	Machine string
	Serial  string
}

// WithIdentity 把认证身份注入上下文。
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext 取出认证身份（未认证时 ok=false）。
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// peerIdentity 从 gRPC peer 中提取已验证的客户端证书身份。
// 服务端 TLS 配置为 VerifyClientCertIfGiven：VerifiedChains 非空即表示
// 客户端证书存在且通过 Fleet CA 校验。
func peerIdentity(ctx context.Context) (Identity, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return Identity{}, errors.New("no peer info")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return Identity{}, errors.New("connection is not TLS")
	}
	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return Identity{}, errors.New("no verified client certificate")
	}
	leaf := tlsInfo.State.VerifiedChains[0][0]
	return Identity{Machine: leaf.Subject.CommonName, Serial: serialHex(leaf)}, nil
}

func serialHex(cert *x509.Certificate) string {
	return fmt.Sprintf("%x", cert.SerialNumber)
}

// peerIP 从 peer 地址提取源 IP（限速键）。
func peerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
		return host
	}
	return p.Addr.String()
}

// requireIdentity 校验 mTLS 身份并确认机器未被删除（§4.6 v1.1.2 取舍 (a)：
// 机器删除后必须拒绝其已签发证书的后续 Connect，按库中删除事实判定）。
func (s *Server) requireIdentity(ctx context.Context, fullMethod string) (Identity, error) {
	id, err := peerIdentity(ctx)
	if err != nil {
		s.log.Warn("agent rpc rejected: no client identity", "method", fullMethod,
			"event", "agent_rpc_rejected", "reason_code", domain.ReasonUnauthorized)
		return Identity{}, err
	}
	if _, err := s.machines.Get(ctx, id.Machine); err != nil {
		// 含"机器已删除"的情况：记录 machineId、证书序列号与时间（§4.6）。
		s.log.Warn("agent rpc rejected: machine unknown or deleted", "method", fullMethod,
			"event", "agent_rpc_rejected", "reason_code", domain.ReasonEnrollmentRejected,
			"machine_id", id.Machine, "cert_serial", id.Serial)
		return Identity{}, fmt.Errorf("machine %q unknown or deleted", id.Machine)
	}
	return id, nil
}
