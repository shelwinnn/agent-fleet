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

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// Config 为服务器装配参数。
type Config struct {
	// AdminToken 非空时要求 /api/v1 全部请求携带 Authorization: Bearer <token>（§29.14）。
	AdminToken string
}

type Server struct {
	cfg      Config
	log      *slog.Logger
	machines domain.MachineRepository
	profiles domain.ProfileRepository
	ping     func(ctx context.Context) error
}

// New 装配服务器。ping 供 /readyz 探测存储可用性。
func New(cfg Config, machines domain.MachineRepository, profiles domain.ProfileRepository,
	ping func(ctx context.Context) error, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, log: log, machines: machines, profiles: profiles, ping: ping}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	machines := &resourceAPI[domain.Machine, *domain.Machine]{
		resource: "machines",
		repo:     s.machines,
		validate: func(m *domain.Machine) error { return m.Validate() },
	}
	mux.Handle("/api/v1/machines", s.auth(machines.collection()))
	mux.Handle("/api/v1/machines/{name}", s.auth(machines.item()))

	profiles := &resourceAPI[domain.AgentProfile, *domain.AgentProfile]{
		resource: "profiles",
		repo:     s.profiles,
		validate: func(p *domain.AgentProfile) error { return p.Validate() },
	}
	mux.Handle("/api/v1/profiles", s.auth(profiles.collection()))
	mux.Handle("/api/v1/profiles/{name}", s.auth(profiles.item()))

	// 骨架路由：资源归属后续切片，未实现返回明确错误（501 NotImplemented）。
	for _, name := range []string{"skills", "providers", "deployments"} {
		mux.Handle("/api/v1/"+name, s.auth(notImplemented(name)))
		mux.Handle("/api/v1/"+name+"/{name}", s.auth(notImplemented(name)))
	}

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
