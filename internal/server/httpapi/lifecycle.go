// 生命周期 REST 端点（KM-49；契约 api/openapi/fleet-v1.yaml 的
// /api/v1/lifecycle/* 与 /api/v1/machines/{name}/lifecycle/desired，KM-47 落地）。
// 参数校验 → 编排控制器 → §6.4 错误体；本层无业务语义。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// LifecycleAPI 是 httpapi 对生命周期编排控制器的窄依赖（与 ReconcileAPI 同模）。
type LifecycleAPI interface {
	CreateOperation(ctx context.Context, spec domain.LifecycleOperationSpec) (*domain.LifecycleOperation, bool, error)
	GetOperation(ctx context.Context, id string) (*domain.LifecycleOperation, error)
	Confirm(ctx context.Context, id string, at time.Time) error
	Retry(ctx context.Context, originalID string, targets []domain.LifecycleTarget, createdBy string) (*domain.LifecycleOperation, error)
	GetDesired(ctx context.Context, machine string) (*domain.LifecycleDesiredRecord, error)
	PutDesired(ctx context.Context, machine string, update domain.LifecycleDesired) (*domain.LifecycleDesiredRecord, bool, error)
	Capabilities() []domain.LifecycleCapability
}

// SetLifecycle 装配生命周期编排控制器。nil 时端点返回 501。
func (s *Server) SetLifecycle(api LifecycleAPI) { s.lifecycle = api }

// lifecycleOperationView 是操作响应体：LifecycleOperation 原样 + duplicate 标记
// （POST /operations 幂等复用时 200 + duplicate=true；新建 201 + duplicate=false。
// 契约描述与实现的一致性由 api/openapi/contract_test.go 锁定）。
type lifecycleOperationView struct {
	domain.LifecycleOperation
	Duplicate bool `json:"duplicate"`
}

func (s *Server) handleLifecycleCreateOperation(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"lifecycle orchestration is not wired", nil)
		return
	}
	var req struct {
		Type           string                  `json:"type"`
		IdempotencyKey string                  `json:"idempotencyKey"`
		Version        string                  `json:"version"`
		Targets        []domain.LifecycleTarget `json:"targets"`
		CreatedBy      string                  `json:"createdBy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, "invalid JSON body: "+err.Error(), nil)
		return
	}
	op, created, err := s.lifecycle.CreateOperation(r.Context(), domain.LifecycleOperationSpec{
		Type: req.Type, IdempotencyKey: req.IdempotencyKey, Version: req.Version,
		Targets: req.Targets, CreatedBy: req.CreatedBy,
	})
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, lifecycleOperationView{LifecycleOperation: *op, Duplicate: !created})
}

func (s *Server) handleLifecycleGetOperation(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"lifecycle orchestration is not wired", nil)
		return
	}
	op, err := s.lifecycle.GetOperation(r.Context(), r.PathValue("opId"))
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lifecycleOperationView{LifecycleOperation: *op})
}

// handleLifecycleConfirmOperation 落操作者确认时刻（未确认不得派发；影响变化
// 后必须重新确认才能继续——重试是同一已批准意图，不重复确认）。
func (s *Server) handleLifecycleConfirmOperation(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"lifecycle orchestration is not wired", nil)
		return
	}
	id := r.PathValue("opId")
	if err := s.lifecycle.Confirm(r.Context(), id, time.Now()); err != nil {
		writeLifecycleError(w, err)
		return
	}
	op, err := s.lifecycle.GetOperation(r.Context(), id)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lifecycleOperationView{LifecycleOperation: *op})
}

func (s *Server) handleLifecycleRetryOperation(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"lifecycle orchestration is not wired", nil)
		return
	}
	var req struct {
		Targets   []domain.LifecycleTarget `json:"targets"`
		CreatedBy string                   `json:"createdBy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, "invalid JSON body: "+err.Error(), nil)
		return
	}
	op, err := s.lifecycle.Retry(r.Context(), r.PathValue("opId"), req.Targets, req.CreatedBy)
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, lifecycleOperationView{LifecycleOperation: *op})
}

func (s *Server) handleLifecycleGetDesired(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"lifecycle orchestration is not wired", nil)
		return
	}
	rec, err := s.lifecycle.GetDesired(r.Context(), r.PathValue("name"))
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lifecycleDesiredView(rec))
}

func (s *Server) handleLifecyclePutDesired(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"lifecycle orchestration is not wired", nil)
		return
	}
	var req struct {
		Entries map[string]domain.LifecycleEntry `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, "invalid JSON body: "+err.Error(), nil)
		return
	}
	rec, _, err := s.lifecycle.PutDesired(r.Context(), r.PathValue("name"), domain.LifecycleDesired{
		SchemaVersion: domain.LifecycleDesiredSchemaVersion,
		Entries:       req.Entries,
	})
	if err != nil {
		writeLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lifecycleDesiredView(rec))
}

func (s *Server) handleLifecycleCapabilities(w http.ResponseWriter, _ *http.Request) {
	if s.lifecycle == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"lifecycle orchestration is not wired", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.lifecycle.Capabilities()})
}

// lifecycleDesiredView 按 LifecycleDesiredDocument 契约形状输出。
func lifecycleDesiredView(rec *domain.LifecycleDesiredRecord) map[string]any {
	if rec == nil {
		return map[string]any{}
	}
	entries := rec.Desired.Entries
	if entries == nil {
		entries = map[string]domain.LifecycleEntry{}
	}
	return map[string]any{
		"schemaVersion": rec.Desired.SchemaVersion,
		"entries":       entries,
		"machine":       rec.Machine,
		"revision":      rec.Revision,
		"digest":        rec.Digest,
		"createdAt":     rec.CreatedAt,
	}
}

// writeLifecycleError 把生命周期哨兵错误映射为 §6.4 错误体
//（幂等冲突/不可重试 → 409；能力阻塞 → 422；其余走通用映射）。
func writeLifecycleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, domain.ReasonIdempotencyConflict, err.Error(), nil)
	case errors.Is(err, domain.ErrRetryNotAllowed):
		writeError(w, http.StatusConflict, domain.ReasonRetryNotAllowed, err.Error(), nil)
	case errors.Is(err, domain.ErrUnsupportedCombo):
		// 能力矩阵阻塞的组合：创建即拒（422 Unprocessable Entity——请求本身
		// 语法正确，但该目标组合被禁止交付，禁止静默替换）。
		writeError(w, http.StatusUnprocessableEntity, domain.ReasonCapabilityBlocked, err.Error(), nil)
	default:
		writeStoreError(w, err)
	}
}
