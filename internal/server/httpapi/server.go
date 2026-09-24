// Package httpapi 实现 REST 处理器（架构 v1.1.2 §4.1/§8.1）：
// 参数校验 → 调用仓储 → 错误映射为 §6.4 五要素错误体。
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// ReconcileAPI 是 httpapi 对 Reconcile 控制器的窄依赖（§8.1 动作端点）。
type ReconcileAPI interface {
	Reconcile(ctx context.Context, machine string, req reconcile.ReconcileRequest) (*domain.Operation, error)
	Cancel(ctx context.Context, machine, opID string) (*domain.Operation, error)
	Skip(ctx context.Context, machine, opID string, req reconcile.SkipRequest) (*domain.Operation, error)
	Rollback(ctx context.Context, machine string, targetGeneration int64) (*domain.Operation, error)
	UnresolvedRef(ctx context.Context, machine string) *domain.UnresolvedOperationRef
	EvaluateDrift(ctx context.Context, machine string) (*domain.DriftEvaluation, error)
	// RequestInventory 是 POST /machines/{name}/inventory（§8.1）：记录一条
	// readOnly 操作并采集一次观测（SSH-only 机器只能经 SSH 采集）。
	RequestInventory(ctx context.Context, machine string) (*domain.Operation, error)
}

// Config 为服务器装配参数。
type Config struct {
	// AdminToken 非空时要求 /api/v1 全部请求携带 Authorization: Bearer <token>（§29.14）。
	AdminToken string
	// EnrollTokens 签发一次性 enrollment token（KM-22）；nil 时端点返回 501。
	EnrollTokens EnrollTokenIssuer
	// OnMachineDelete 在机器删除成功后调用（KM-22：退役该机证书，§4.6）。
	// 失败只记日志，不影响 204——"删除即拒绝"由 Connect 的机器存在性检查兜底。
	OnMachineDelete func(ctx context.Context, name string) error
	// Events 是 SSE 事件枢纽处理器（KM-25：§8.1 `GET /api/v1/events`，格式见 §23.6）；
	// nil 时该路由按骨架语义返回 501。
	Events http.Handler
	// SPADir 是 Web UI 静态产物目录（默认 web/dist，见 cmd/agent-fleet-server）。
	// 目录不存在时不注册静态托管（Go 构建与前端产物解耦），只保留 API。
	SPADir string
	// DeploymentTargets 读取某 Deployment 的逐机推进状态（deployment_targets 表）。
	// 非 nil 时，GET /api/v1/deployments 与 /{name} 会把结果并入 status.targets
	// （§6.1 已规定的字段；FR-14.5 第 3 组的数据来源）。
	DeploymentTargets func(ctx context.Context, deployment string) ([]domain.DeploymentTargetStatus, error)
}

type Server struct {
	cfg           Config
	log           *slog.Logger
	machines      domain.MachineRepository
	profiles      domain.ProfileRepository
	skills        domain.SkillRepository
	providers     domain.ProviderRepository
	deployments   domain.DeploymentRepository
	operations    domain.OperationRepository
	reconcile     ReconcileAPI
	ssh           SSHMachineAPI
	include       IncludeAPI
	deploys       DeploymentAPI
	schemaVersion string
	ping          func(ctx context.Context) error
}

// New 装配服务器。ping 供 /readyz 探测存储可用性。
// KM-23 起新增：skills/providers/deployments 仓储、operations 审计仓储、
// Reconcile/Deployment 控制器与渲染 schema 版本（动作端点，§8.1）。
func New(cfg Config, machines domain.MachineRepository, profiles domain.ProfileRepository,
	skills domain.SkillRepository, providers domain.ProviderRepository,
	deploys domain.DeploymentRepository, operations domain.OperationRepository,
	reconcile ReconcileAPI, deploymentCtl DeploymentAPI, schemaVersion string,
	ping func(ctx context.Context) error, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		cfg: cfg, log: log, machines: machines, profiles: profiles,
		skills: skills, providers: providers, deployments: deploys,
		operations: operations, reconcile: reconcile, deploys: deploymentCtl,
		schemaVersion: schemaVersion, ping: ping,
	}
}

// SetSSH 装配 SSH-only 通道（probe/inventory）。nil 时端点返回 501。
func (s *Server) SetSSH(api SSHMachineAPI) { s.ssh = api }

// SetInclude 装配 OpenSSH include 导出。nil 时端点返回 501。
func (s *Server) SetInclude(api IncludeAPI) { s.include = api }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	machines := &resourceAPI[domain.Machine, *domain.Machine]{
		resource: "machines",
		repo:     s.machines,
		validate: func(m *domain.Machine) error { return m.Validate() },
		onDelete: s.cfg.OnMachineDelete,
	}
	mux.Handle("/api/v1/machines", s.auth(machines.collection()))
	mux.Handle("/api/v1/machines/{name}", s.auth(machines.item()))
	mux.Handle("POST /api/v1/machines/{name}/enroll-token", s.auth(http.HandlerFunc(s.handleEnrollToken)))

	// SSE 事件枢纽（§8.1 `GET /events`，§23.6 事件格式；KM-25）。
	// 路径按 §8.1 的 /api/v1 前缀约定解析为 /api/v1/events；鉴权与其余端点同一
	// 中间件（§29.14），浏览器端用 fetch 流式读取以携带 Authorization 头。
	events := s.cfg.Events
	if events == nil {
		events = notImplemented("events")
	}
	mux.Handle("GET /api/v1/events", s.auth(events))

	// 动作端点（§8.1；KM-23）。confirm/cancel/skip 是机器级互斥的三个合法例外。
	mux.Handle("POST /api/v1/machines/{name}/reconcile", s.auth(http.HandlerFunc(s.handleReconcile)))
	mux.Handle("POST /api/v1/machines/{name}/rollback", s.auth(http.HandlerFunc(s.handleRollbackMachine)))
	mux.Handle("POST /api/v1/machines/{name}/operations/{opId}/cancel", s.auth(http.HandlerFunc(s.handleCancelOperation)))
	mux.Handle("POST /api/v1/machines/{name}/operations/{opId}/skip", s.auth(http.HandlerFunc(s.handleSkipOperation)))
	mux.Handle("GET /api/v1/machines/{name}/operations", s.auth(http.HandlerFunc(s.handleListOperations)))
	mux.Handle("GET /api/v1/machines/{name}/drift", s.auth(http.HandlerFunc(s.handleDrift)))
	// SSH-only 通道（§8.1；KM-26）：probe 与 inventory。bootstrap / repair-agentd
	// 未在本片实现（见交付说明的遗留项），路由不注册 → 走 §6.4 JSON 404。
	mux.Handle("POST /api/v1/machines/{name}/ssh/probe", s.auth(http.HandlerFunc(s.handleSSHProbe)))
	mux.Handle("POST /api/v1/machines/{name}/ssh/inventory", s.auth(http.HandlerFunc(s.handleSSHInventory)))
	// OpenSSH include 导出（FR-12.6）：preview / export(GET) / install(POST，须显式确认)。
	mux.Handle("GET /api/v1/ssh/include", s.auth(http.HandlerFunc(s.handleSSHIncludePreview)))
	mux.Handle("POST /api/v1/ssh/include", s.auth(http.HandlerFunc(s.handleSSHIncludeInstall)))

	profiles := &resourceAPI[domain.AgentProfile, *domain.AgentProfile]{
		resource: "profiles",
		repo:     s.profiles,
		validate: func(p *domain.AgentProfile) error { return p.Validate() },
	}
	mux.Handle("/api/v1/profiles", s.auth(profiles.collection()))
	mux.Handle("/api/v1/profiles/{name}", s.auth(profiles.item()))
	mux.Handle("GET /api/v1/profiles/{name}/render", s.auth(http.HandlerFunc(s.handleRenderPreview)))

	skills := &resourceAPI[domain.Skill, *domain.Skill]{
		resource: "skills",
		repo:     s.skills,
		validate: func(k *domain.Skill) error { return k.Validate() },
	}
	mux.Handle("/api/v1/skills", s.auth(skills.collection()))
	mux.Handle("/api/v1/skills/{name}", s.auth(skills.item()))

	providers := &resourceAPI[domain.ModelProvider, *domain.ModelProvider]{
		resource: "providers",
		repo:     s.providers,
		validate: func(p *domain.ModelProvider) error { return p.Validate() },
	}
	mux.Handle("/api/v1/providers", s.auth(providers.collection()))
	mux.Handle("/api/v1/providers/{name}", s.auth(providers.item()))

	deployments := &resourceAPI[domain.Deployment, *domain.Deployment]{
		resource: "deployments",
		repo:     s.deployments,
		validate: func(d *domain.Deployment) error { return d.Validate() },
		enrich:   s.attachDeploymentTargets,
		onCreate: s.createDeployment,
	}
	mux.Handle("/api/v1/deployments", s.auth(deployments.collection()))
	mux.Handle("/api/v1/deployments/{name}", s.auth(deployments.item()))
	mux.Handle("POST /api/v1/deployments/{name}/rollback", s.auth(http.HandlerFunc(s.handleDeploymentRollback)))
	mux.Handle("POST /api/v1/deployments/{name}/targets/{machine}/skip", s.auth(http.HandlerFunc(s.handleSkipDeploymentTarget)))

	// catch-all：/api 与探针命名空间未匹配时走 §6.4 错误体（KM-21 核查发现 #3：
	// 不再回落 net/http 纯文本 "404 page not found"）；其余路径交给静态 SPA
	// （§3.6/§4.1：控制面进程同时托管 Web UI），未配置或目录缺失时维持原 404 行为。
	spa, spaEnabled := spaHandler(s.cfg.SPADir, s.log)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			writeError(w, http.StatusNotFound, domain.ReasonNotFound,
				"no route for "+r.Method+" "+r.URL.Path, nil)
			return
		}
		if spaEnabled {
			spa.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusNotFound, domain.ReasonNotFound,
			"no route for "+r.Method+" "+r.URL.Path, nil)
	})

	return s.logRequests(mux)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.ping(ctx); err != nil {
		s.log.Error("readiness check failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, domain.ReasonNotReady, "storage is not ready", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// auth 校验 admin token（§29.14：非回环部署必须配置）。常量时间比较。
func (s *Server) auth(next http.Handler) http.Handler {
	if s.cfg.AdminToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.AdminToken)) != 1 {
			writeError(w, http.StatusUnauthorized, domain.ReasonUnauthorized, "missing or invalid admin token", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests 输出结构化访问日志。
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Info("http", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start).Round(time.Microsecond).String())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush 透传 http.Flusher：SSE 处理器（/api/v1/events）经本中间件时，若这里
// 不实现 Flusher，事件会被缓冲到连接结束（KM-25）。底层不支持 Flush 时是空操作。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// notImplemented 返回骨架路由的 501 响应。
func notImplemented(resource string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			(&domain.NotImplemented{Resource: resource}).Error(), nil)
	}
}

// writeStoreError 把存储/校验层错误映射为 HTTP 状态码与 reason code。
func writeStoreError(w http.ResponseWriter, err error) {
	var reason string
	var status int
	switch {
	case errors.Is(err, domain.ErrNotFound):
		status, reason = http.StatusNotFound, domain.ReasonNotFound
	case errors.Is(err, domain.ErrAlreadyExists):
		status, reason = http.StatusConflict, domain.ReasonAlreadyExists
	case errors.Is(err, domain.ErrMachineBusy):
		status, reason = http.StatusConflict, domain.ReasonMachineBusy
	case errors.Is(err, domain.ErrRollbackUnsupported):
		// 创建发布时目标代不可解析（控制器创建路径的入参校验，FR-10.1）。
		status, reason = http.StatusConflict, domain.ReasonRollbackUnsupported
	case errors.Is(err, domain.ErrReplanRequired):
		status, reason = http.StatusConflict, domain.ReasonReplanRequired
	case errors.Is(err, domain.ErrOpState):
		status, reason = http.StatusConflict, domain.ReasonInvalid
	case errors.Is(err, domain.ErrInvalid):
		status, reason = http.StatusBadRequest, domain.ReasonInvalid
	default:
		status, reason = http.StatusInternalServerError, domain.ReasonInternal
	}
	if status == http.StatusInternalServerError {
		slog.Error("internal error", "err", err)
	}
	writeError(w, status, reason, err.Error(), nil)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}
