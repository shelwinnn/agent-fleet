package grpcagent

import (
	"context"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/enrollment"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Enroll 实现 bootstrap 签发（§7.5 十步中的 6–8 步）：
// TLS + 服务端证书校验由传输层保证；token 是唯一身份证明。
// 服务端校验顺序：请求形态 → CSR 合法性与指纹 → token 原子消费（绑定校验在同一条
// 条件 UPDATE 内）→ CA 签发 → 记录审计。失败一律 EnrollmentRejected，细节只进日志。
func (s *Server) Enroll(ctx context.Context, req *fleetv1.EnrollRequest) (*fleetv1.EnrollResponse, error) {
	if req.GetMachineId() == "" || req.GetToken() == "" || len(req.GetCsrPem()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "machineId, token and csrPem are required")
	}
	machine := req.GetMachineId()
	csrFingerprint, err := enrollment.CSRFingerprint(req.GetCsrPem())
	if err != nil {
		s.limiter.RecordFailure(peerIP(ctx))
		s.log.Warn("enroll rejected", "event", "enroll_rejected",
			"reason_code", domain.ReasonEnrollmentRejected, "machine_id", machine, "detail", "invalid_csr")
		return nil, status.Error(codes.InvalidArgument, "invalid csr")
	}
	if err := s.tokens.Redeem(ctx, machine, req.GetToken(), csrFingerprint); err != nil {
		return nil, mapStoreErr(err)
	}
	validity := s.cfg.ClientCertValidity
	certPEM, serial, notAfter, err := s.ca.IssueClientCertificate(req.GetCsrPem(), machine, validity)
	if err != nil {
		s.log.Error("client certificate issue failed", "machine_id", machine, "err", err)
		return nil, status.Error(codes.Internal, "certificate issue failed")
	}
	notBefore := time.Now().UTC().Add(-time.Minute)
	if err := s.certs.Record(ctx, &domain.AgentCertificate{
		Serial:    serial,
		Machine:   machine,
		NotBefore: notBefore,
		NotAfter:  notAfter,
		IssuedAt:  time.Now().UTC(),
	}); err != nil {
		s.log.Error("certificate record failed", "machine_id", machine, "serial", serial, "err", err)
		return nil, status.Error(codes.Internal, "certificate record failed")
	}
	s.log.Info("client certificate issued", "event", "client_cert_issued",
		"machine_id", machine, "cert_serial", serial, "not_after", notAfter.Format(time.RFC3339))
	return &fleetv1.EnrollResponse{
		CertPem:      certPEM,
		CaPem:        s.ca.CACertPEM(),
		NotAfterUnix: notAfter.Unix(),
	}, nil
}

// RenewCertificate 实现证书续期（spec §12.2：仅接受既有有效 mTLS 通道上的请求——
// mTLS 要求由拦截器完成；签名新证书沿用认证身份，不信任 CSR 自报 Subject）。
func (s *Server) RenewCertificate(ctx context.Context, req *fleetv1.RenewCertificateRequest) (*fleetv1.RenewCertificateResponse, error) {
	id, _ := IdentityFromContext(ctx) // 拦截器保证存在
	if len(req.GetCsrPem()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "csrPem is required")
	}
	csrFingerprint, err := enrollment.CSRFingerprint(req.GetCsrPem())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid csr")
	}
	_ = csrFingerprint // 续期身份来自通道，指纹仅校验 CSR 合法性
	validity := s.cfg.ClientCertValidity
	certPEM, serial, notAfter, err := s.ca.IssueClientCertificate(req.GetCsrPem(), id.Machine, validity)
	if err != nil {
		s.log.Error("certificate renewal failed", "machine_id", id.Machine, "err", err)
		return nil, status.Error(codes.Internal, "certificate renewal failed")
	}
	if err := s.certs.Record(ctx, &domain.AgentCertificate{
		Serial:    serial,
		Machine:   id.Machine,
		NotBefore: time.Now().UTC().Add(-time.Minute),
		NotAfter:  notAfter,
		IssuedAt:  time.Now().UTC(),
	}); err != nil {
		s.log.Error("certificate record failed", "machine_id", id.Machine, "serial", serial, "err", err)
		return nil, status.Error(codes.Internal, "certificate record failed")
	}
	s.log.Info("client certificate renewed", "event", "client_cert_renewed",
		"machine_id", id.Machine, "old_serial", id.Serial, "new_serial", serial,
		"not_after", notAfter.Format(time.RFC3339))
	return &fleetv1.RenewCertificateResponse{
		CertPem:      certPEM,
		NotAfterUnix: notAfter.Unix(),
	}, nil
}
