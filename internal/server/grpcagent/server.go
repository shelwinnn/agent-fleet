package grpcagent

import (
	"log/slog"
	"sync"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/controller/machine"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/enrollment"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// 默认周期（§5.2/§4.2：心跳 15s、全量 inventory 5min、离线阈值 45s；均可配置）。
const (
	DefaultHeartbeatInterval = 15 * time.Second
	DefaultInventoryInterval = 5 * time.Minute
	DefaultOfflineAfter      = 45 * time.Second
	DefaultOfflineScanEvery  = 5 * time.Second
)

// Config 是 agent gRPC 端点的装配参数。
type Config struct {
	HeartbeatInterval  time.Duration // 随 Welcome 回传 agentd 的期望周期
	InventoryInterval  time.Duration // 同上
	OfflineAfter       time.Duration // 心跳超时置离线阈值（§4.2）
	OfflineScanEvery   time.Duration // 离线扫描周期（默认 5s）
	ClientCertValidity time.Duration // 签发的客户端证书有效期（默认 90d）
	EnrollRateLimit    int           // 每源 IP 每窗口失败上限（默认 10）
	EnrollRateWindow   time.Duration // 默认 1min
}

func (c Config) heartbeatInterval() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return DefaultHeartbeatInterval
}

func (c Config) inventoryInterval() time.Duration {
	if c.InventoryInterval > 0 {
		return c.InventoryInterval
	}
	return DefaultInventoryInterval
}

func (c Config) offlineAfter() time.Duration {
	if c.OfflineAfter > 0 {
		return c.OfflineAfter
	}
	return DefaultOfflineAfter
}

func (c Config) scanEvery() time.Duration {
	if c.OfflineScanEvery > 0 {
		return c.OfflineScanEvery
	}
	return DefaultOfflineScanEvery
}

// Server 是 FleetAgentService / FleetEnrollmentService 的宿主。
type Server struct {
	fleetv1.UnimplementedFleetAgentServiceServer
	fleetv1.UnimplementedFleetEnrollmentServiceServer

	cfg      Config
	log      *slog.Logger
	ca       *enrollment.CA
	tokens   *enrollment.TokenService
	limiter  *enrollment.IPLimiter
	ctrl     *machine.Controller
	machines domain.MachineRepository
	certs    domain.AgentCertificateRepository
	observed domain.ObservedStateRepository

	registry *connRegistry
}

// New 装配 agent gRPC 端点。
func New(cfg Config, ca *enrollment.CA, tokens *enrollment.TokenService, ctrl *machine.Controller,
	machines domain.MachineRepository, certs domain.AgentCertificateRepository,
	observed domain.ObservedStateRepository, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg:      cfg,
		log:      log,
		ca:       ca,
		tokens:   tokens,
		limiter:  enrollment.NewIPLimiter(cfg.EnrollRateLimit, cfg.EnrollRateWindow),
		ctrl:     ctrl,
		machines: machines,
		certs:    certs,
		observed: observed,
		registry: newConnRegistry(),
	}
}

// OfflineAfter 暴露离线阈值供扫描器装配。
func (s *Server) OfflineAfter() time.Duration { return s.cfg.offlineAfter() }

// GRPCServer 构建 gRPC 服务器：TLS（强制 + 服务端证书）+ 拦截器 + 两个服务。
func (s *Server) GRPCServer() *grpc.Server {
	tlsCfg := s.ca.ServerTLSConfig()
	g := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.ChainUnaryInterceptor(s.unaryInterceptor()),
		grpc.ChainStreamInterceptor(s.streamInterceptor()),
	)
	s.RegisterGRPC(g)
	return g
}

// RegisterGRPC 把两个服务注册到既有 gRPC 服务器（测试可复用）。
func (s *Server) RegisterGRPC(g *grpc.Server) {
	fleetv1.RegisterFleetAgentServiceServer(g, s)
	fleetv1.RegisterFleetEnrollmentServiceServer(g, s)
}

// connRegistry 记录每台机器的活跃 Connect 流（§4.6：同一 machineId 已有活跃流时
// 第二条流默认拒绝并告警，不做"新连接顶掉旧流"）。
type connRegistry struct {
	mu     sync.Mutex
	active map[string]string // machine -> cert serial
}

func newConnRegistry() *connRegistry {
	return &connRegistry{active: map[string]string{}}
}

// tryRegister 注册成功返回 true；该机已有活跃流返回 false。
func (r *connRegistry) tryRegister(machine, serial string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.active[machine]; busy {
		return false
	}
	r.active[machine] = serial
	return true
}

// unregister 按 serial 注销：旧流退出不会误删持不同证书的新流。
// （serial 相同则视为同一流族，正常移除。）
func (r *connRegistry) unregister(machine, serial string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.active[machine]; ok && cur == serial {
		delete(r.active, machine)
	}
}
