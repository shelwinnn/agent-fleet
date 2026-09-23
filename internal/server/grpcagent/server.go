package grpcagent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/controller/machine"
	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
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

// dispatchQueueLen 是单连接下行派发队列长度。控制面互斥保证每机至多一个
// 未决操作，正常深度为 1；满即拒绝派发（上游写 Operation.Failed，不静默丢）。
const dispatchQueueLen = 32

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

// ResultSink 是控制面对节点上报的处理入口（由 reconcile.Controller 实现）。
type ResultSink interface {
	OnStarted(ctx context.Context, machine, opID string) error
	OnProgress(ctx context.Context, machine, opID, step, phase, message string) error
	OnResult(ctx context.Context, machine string, res reconcile.OperationResultMsg) (reconcile.ResultOutcome, error)
	// AssignObservationGeneration 为观测做因果归属代标注（§4.4/FR-8.7）。
	AssignObservationGeneration(ctx context.Context, rec *domain.ObservedStateRecord)
	// EvaluateDrift 在观测落库后按当前代求值 drift 三态（§4.2）。
	EvaluateDrift(ctx context.Context, machine string) (*domain.DriftEvaluation, error)
}

// Server 是 FleetAgentService / FleetEnrollmentService 的宿主，
// 同时实现 reconcile.Dispatcher（控制面 → 节点的下行派发通道）。
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
	sink     ResultSink

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

// SetResultSink 装配结果回灌入口（main 装配：reconcile.Controller；
// 与 SetDispatcher 成对出现，打破构造环）。
func (s *Server) SetResultSink(sink ResultSink) { s.sink = sink }

// ExecuteOperation 实现 reconcile.Dispatcher：经该机活跃 Connect 流下发
// ExecuteOperation（一律携带完整快照，FR-13.6）。无活跃流返回 ErrAgentDisconnected。
func (s *Server) ExecuteOperation(_ context.Context, machine string, op *domain.Operation, snap *domain.DesiredStateSnapshot) error {
	raw, err := snap.JSON()
	if err != nil {
		return err
	}
	msg := &fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_ExecuteOperation{
		ExecuteOperation: &fleetv1.ExecuteOperation{
			OperationId:       op.Metadata.Name,
			DesiredGeneration: snap.Generation,
			Snapshot: &fleetv1.DesiredStateSnapshot{
				Generation:   snap.Generation,
				Digest:       snap.Digest,
				SnapshotJson: raw,
			},
		},
	}}
	return s.dispatch(machine, msg)
}

// CancelOperation 实现 reconcile.Dispatcher：下发取消（节点在阶段边界生效，
// FR-13.9）。
func (s *Server) CancelOperation(_ context.Context, machine, operationID string) error {
	return s.dispatch(machine, &fleetv1.ServerToAgent{Payload: &fleetv1.ServerToAgent_CancelOperation{
		CancelOperation: &fleetv1.CancelOperation{OperationId: operationID},
	}})
}

func (s *Server) dispatch(machine string, msg *fleetv1.ServerToAgent) error {
	ch, ok := s.registry.channel(machine)
	if !ok {
		return fmt.Errorf("%w: %s", domain.ErrAgentDisconnected, machine)
	}
	select {
	case ch <- msg:
		return nil
	default:
		return fmt.Errorf("dispatch queue full for machine %q", machine)
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
// 第二条流默认拒绝并告警，不做"新连接顶掉旧流"）与下行派发队列。
type connRegistry struct {
	mu     sync.Mutex
	active map[string]*streamHandle // machine -> handle
}

type streamHandle struct {
	serial string
	// dispatch 是下行消息队列；发送只发生在 Connect 主循环（单写者，P1-2）。
	dispatch chan *fleetv1.ServerToAgent
}

func newConnRegistry() *connRegistry {
	return &connRegistry{active: map[string]*streamHandle{}}
}

// tryRegister 注册成功返回 handle；该机已有活跃流返回 false。
func (r *connRegistry) tryRegister(machine, serial string) (*streamHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.active[machine]; busy {
		return nil, false
	}
	h := &streamHandle{serial: serial, dispatch: make(chan *fleetv1.ServerToAgent, dispatchQueueLen)}
	r.active[machine] = h
	return h, true
}

func (r *connRegistry) channel(machine string) (chan *fleetv1.ServerToAgent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.active[machine]
	if !ok {
		return nil, false
	}
	return h.dispatch, true
}

// unregister 按 serial 注销：旧流退出不会误删持不同证书的新流。
func (r *connRegistry) unregister(machine, serial string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.active[machine]; ok && cur.serial == serial {
		delete(r.active, machine)
	}
}
