package grpcagent

import (
	"context"
	"errors"
	"strings"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// enroll 与 agent 服务的方法前缀（拦截器按方法分级要求身份）。
const (
	prefixAgentService  = "/fleet.v1.FleetAgentService/"
	fullMethodEnroll    = "/fleet.v1.FleetEnrollmentService/Enroll"
	fullMethodRenewCert = "/fleet.v1.FleetEnrollmentService/RenewCertificate"
)

// unaryInterceptor：RenewCertificate 强制 mTLS 身份；Enroll 施加按源 IP
// 失败限速（§11 S3 强制）。机器删除检查在需要身份的方法上一并完成。
func (s *Server) unaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		switch info.FullMethod {
		case fullMethodEnroll:
			ip := peerIP(ctx)
			if !s.limiter.Check(ip) {
				s.log.Warn("enroll rate limited", "method", info.FullMethod,
					"event", "enroll_rate_limited", "source_ip", ip)
				return nil, status.Error(codes.ResourceExhausted, "too many enrollment attempts; try later")
			}
			resp, err := handler(ctx, req)
			if err != nil {
				s.limiter.RecordFailure(ip)
			}
			return resp, err
		case fullMethodRenewCert:
			id, err := s.requireIdentity(ctx, info.FullMethod)
			if err != nil {
				return nil, status.Error(codes.PermissionDenied, "valid client certificate required")
			}
			// 身份必须注入 ctx：RenewCertificate 的续期主体取自 mTLS 通道而非 CSR。
			return handler(WithIdentity(ctx, id), req)
		default:
			return handler(ctx, req)
		}
	}
}

// streamInterceptor：Connect 强制 mTLS 身份 + 机器存在（删除即拒绝），
// 并把身份注入流上下文。
func (s *Server) streamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !strings.HasPrefix(info.FullMethod, prefixAgentService) {
			return handler(srv, ss)
		}
		id, err := s.requireIdentity(ss.Context(), info.FullMethod)
		if err != nil {
			return status.Error(codes.PermissionDenied, "valid client certificate required")
		}
		return handler(srv, &identityStream{ServerStream: ss, id: id})
	}
}

// identityStream 把认证身份带入流上下文。
type identityStream struct {
	grpc.ServerStream
	id Identity
}

func (s *identityStream) Context() context.Context {
	return WithIdentity(s.ServerStream.Context(), s.id)
}

// mapStoreErr 把仓储错误统一映射为 gRPC 状态。
func mapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrEnrollmentRejected):
		return status.Error(codes.PermissionDenied, domain.ReasonEnrollmentRejected)
	case errors.Is(err, domain.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
