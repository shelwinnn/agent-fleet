// 动作端点（架构 v1.1.2 §8.1）：reconcile/确认、取消、跳过、回滚、操作列表、
// drift 视图与 profile 渲染预览。变更类动作在机器存在未决操作时返回
// 409 MachineBusy，diagnostics 携带未决操作 id 与 phase（FR-1.10）。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/desiredstate"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// maxActionBodyBytes 限制动作端点请求体（载荷都很小）。
const maxActionBodyBytes = 64 << 10

// maxOperationList 不适用；操作列表全量返回（审计视图，MVP 规模可承受）。

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req reconcile.ReconcileRequest
	if r.ContentLength > 0 {
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, domain.ReasonInvalid, err.Error(), nil)
			return
		}
	}
	op, err := s.reconcile.Reconcile(r.Context(), name, req)
	if err != nil {
		s.writeActionError(w, r, name, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *Server) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	opID := r.PathValue("opId")
	op, err := s.reconcile.Cancel(r.Context(), name, opID)
	if err != nil {
		s.writeActionError(w, r, name, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleSkipOperation(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	opID := r.PathValue("opId")
	var req reconcile.SkipRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, err.Error(), nil)
		return
	}
	op, err := s.reconcile.Skip(r.Context(), name, opID, req)
	if err != nil {
		s.writeActionError(w, r, name, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleRollbackMachine(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		TargetGeneration int64 `json:"targetGeneration"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, err.Error(), nil)
		return
	}
	if req.TargetGeneration <= 0 {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, "targetGeneration must be a positive integer", nil)
		return
	}
	op, err := s.reconcile.Rollback(r.Context(), name, req.TargetGeneration)
	if err != nil {
		s.writeActionError(w, r, name, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.machines.Get(r.Context(), name); err != nil {
		writeStoreError(w, err)
		return
	}
	ops, err := s.operations.ListByMachine(r.Context(), name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if ops == nil {
		ops = []*domain.Operation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": ops})
}

// handleDrift 返回机器的 drift 三态视图（FR-14.5：一致 / 未知或过期 / 漂移，
// Unknown 必须携带 reason）。
func (s *Server) handleDrift(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.machines.Get(r.Context(), name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		writeError(w, http.StatusInternalServerError, domain.ReasonInternal, err.Error(), nil)
		return
	}
	eval, err := s.reconcile.EvaluateDrift(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, domain.ReasonInternal, err.Error(), nil)
		return
	}
	// 求值会写条件（例如把超窗观测置回 Unknown(StaleObservation)）：响应必须用
	// **求值后**的 status，否则会出现"响应说 Unknown、库里是 False"的自相矛盾。
	if fresh, ferr := s.machines.Get(r.Context(), name); ferr == nil {
		if parsed, perr := domain.ParseMachineStatus(fresh.StatusJSON()); perr == nil {
			st = parsed
		}
	}
	drift, _ := st.GetCondition(domain.ConditionDrifted)
	reconciled, _ := st.GetCondition(domain.ConditionReconciled)
	writeJSON(w, http.StatusOK, map[string]any{
		"machine":                  name,
		"drifted":                  conditionView(drift, okOf(drift)),
		"reconciled":               conditionView(reconciled, okOf(reconciled)),
		"desiredGeneration":        st.DesiredGeneration,
		"observedGeneration":       st.ObservedGeneration,
		"lastInventoryAt":          st.LastInventoryAt,
		"inventorySeq":             st.InventorySeq,
		"desiredProjectionDigest":  st.DesiredProjectionDigest,
		"observedProjectionDigest": st.ObservedProjectionDigest,
		"unresolvedOperation":      st.UnresolvedOperation,
		"evaluation":               eval,
	})
}

func okOf(c domain.Condition) bool { return c.Type != "" }

func conditionView(c domain.Condition, ok bool) map[string]any {
	if !ok {
		return map[string]any{"status": domain.ConditionUnknown, "reason": domain.DriftReasonNeverInventoried,
			"message": "no condition recorded yet"}
	}
	return map[string]any{"status": c.Status, "reason": c.Reason, "message": c.Message,
		"lastTransitionTime": c.LastTransitionTime}
}

// handleRenderPreview 实现 FR-7.4：GET /profiles/{id}/render?machine=<id>。
func (s *Server) handleRenderPreview(w http.ResponseWriter, r *http.Request) {
	profileName := r.PathValue("name")
	machineName := r.URL.Query().Get("machine")
	if machineName == "" {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, "machine query parameter is required", nil)
		return
	}
	m, err := s.machines.Get(r.Context(), machineName)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	profile, err := s.profiles.Get(r.Context(), profileName)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	skillMap := map[string]*domain.Skill{}
	if list, err := s.skills.List(r.Context()); err == nil {
		for _, sk := range list {
			skillMap[sk.Metadata.Name] = sk
		}
	}
	providerMap := map[string]*domain.ModelProvider{}
	if list, err := s.providers.List(r.Context()); err == nil {
		for _, p := range list {
			providerMap[p.Metadata.Name] = p
		}
	}
	state, err := desiredstate.Render(desiredstate.RenderInputs{
		Machine: m, Profile: profile, Skills: skillMap, Providers: providerMap,
		SchemaVersion: s.schemaVersion,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonDesiredStateInvalid, err.Error(), nil)
		return
	}
	digest, err := desiredstate.SnapshotDigest(state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, domain.ReasonInternal, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile": profileName, "machine": machineName,
		"digest": digest, "canonicalizationVersion": desiredstate.SnapshotCanonicalizationVersion,
		"desired": state,
	})
}

// handleDeploymentRollback 实现 POST /deployments/{id}/rollback（§8.1/FR-10.4）。
func (s *Server) handleDeploymentRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req struct {
		TargetGeneration int64 `json:"targetGeneration"`
	}
	if r.ContentLength > 0 {
		if err := decodeBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, domain.ReasonInvalid, err.Error(), nil)
			return
		}
	}
	dep, err := s.deploys.CreateRollback(r.Context(), name, req.TargetGeneration)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.attachDeploymentTargets(r.Context(), dep); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, dep)
}

// handleSkipDeploymentTarget 实现 FR-10.7 的显式跳过（留审计，不计入成功）。
func (s *Server) handleSkipDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	depName := r.PathValue("name")
	machine := r.PathValue("machine")
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, err.Error(), nil)
		return
	}
	if err := s.deploys.SkipTarget(r.Context(), depName, machine, req.Reason); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "skipped", "deployment": depName, "machine": machine})
}

// writeActionError 把动作端点错误映射为 §6.4 五要素错误体；409 MachineBusy
// 的 diagnostics 携带未决操作引用（§8.1 约定）。
func (s *Server) writeActionError(w http.ResponseWriter, r *http.Request, machine string, err error) {
	switch {
	case errors.Is(err, domain.ErrMachineBusy):
		diag := map[string]string{}
		if ref := s.reconcile.UnresolvedRef(r.Context(), machine); ref != nil {
			diag["operationId"] = ref.ID
			diag["phase"] = ref.Phase
			if ref.Type != "" {
				diag["type"] = ref.Type
			}
		}
		writeError(w, http.StatusConflict, domain.ReasonMachineBusy, err.Error(), diag)
		return
	case errors.Is(err, domain.ErrRollbackUnsupported):
		writeError(w, http.StatusConflict, domain.ReasonRollbackUnsupported, err.Error(), nil)
		return
	case errors.Is(err, domain.ErrReplanRequired):
		writeError(w, http.StatusConflict, domain.ReasonReplanRequired, err.Error(), nil)
		return
	case errors.Is(err, domain.ErrOpState):
		writeError(w, http.StatusConflict, domain.ReasonInvalid, err.Error(), nil)
		return
	}
	writeStoreError(w, err)
}

func decodeBody(r *http.Request, v any) error {
	defer func() { _, _ = io.Copy(io.Discard, r.Body) }()
	dec := json.NewDecoder(io.LimitReader(r.Body, maxActionBodyBytes))
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}

// DeploymentAPI 是 httpapi 对 Deployment 控制器的窄依赖。
type DeploymentAPI interface {
	// Create 是发布的唯一创建入口（FR-10.1/§4.4）：校验显式 machineNames 与
	// 可解析的 targetGeneration，落库并物化 deployment_targets 与初始 status。
	Create(ctx context.Context, d *domain.Deployment) error
	CreateRollback(ctx context.Context, sourceName string, targetGeneration int64) (*domain.Deployment, error)
	SkipTarget(ctx context.Context, deploymentName, machine, reason string) error
}
