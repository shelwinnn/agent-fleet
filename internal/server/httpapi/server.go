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

	// 动作端点（§8.1；KM-23）。confirm/cancel/skip 是机器级互斥的三个合法例外。
	mux.Handle("POST /api/v1/machines/{name}/reconcile", s.auth(http.HandlerFunc(s.handleReconcile)))
	mux.Handle("POST /api/v1/machines/{name}/rollback", s.auth(http.HandlerFunc(s.handleRollbackMachine)))
	mux.Handle("POST /api/v1/machines/{name}/operations/{opId}/cancel", s.auth(http.HandlerFunc(s.handleCancelOperation)))
	mux.Handle("POST /api/v1/machines/{name}/operations/{opId}/skip", s.auth(http.HandlerFunc(s.handleSkipOperation)))
	mux.Handle("GET /api/v1/machines/{name}/operations", s.auth(http.HandlerFunc(s.handleListOperations)))
	mux.Handle("GET /api/v1/machines/{name}/drift", s.auth(http.HandlerFunc(s.handleDrift)))

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
	}
	mux.Handle("/api/v1/deployments", s.auth(deployments.collection()))
	mux.Handle("/api/v1/deployments/{name}", s.auth(deployments.item()))
	mux.Handle("POST /api/v1/deployments/{name}/rollback", s.auth(http.HandlerFunc(s.handleDeploymentRollback)))
	mux.Handle("POST /api/v1/deployments/{name}/targets/{machine}/skip", s.auth(http.HandlerFunc(s.handleSkipDeploymentTarget)))

	// catch-all：未匹配路径统一走 §6.4 错误体（KM-21 核查发现 #3：
	// 不再回落 net/http 纯文本 "404 page not found"）。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
