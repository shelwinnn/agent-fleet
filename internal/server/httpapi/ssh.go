// SSH-only 路径的 HTTP 端点（架构 v1.1.2 §8.1、FR-12.6）：
//
//	POST /api/v1/machines/{name}/ssh/probe        SSH 探测（ssh -G + 远端平台采集）
//	POST /api/v1/machines/{name}/ssh/inventory    经 SSH 采集观测（readOnly 操作）
//	GET  /api/v1/ssh/include                      preview：渲染 ~/.ssh/agent-fleet.conf
//	GET  /api/v1/ssh/include?download=1           export：下载该文件
//	POST /api/v1/ssh/include                      显式确认后安装/更新该 include 文件
//
// 绝不静默改写操作者的主 SSH 配置：安装动作必须带 `{"action":"install","confirm":true}`。
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/sshtransport"
)

// SSHMachineAPI 是 httpapi 对 SSH 路径的窄依赖（由 controller/sshops 实现）。
type SSHMachineAPI interface {
	// ProbeAndRecord 执行探测并写入 Machine status（成功写 SSHReachable=True 与
	// 静态信息，失败写 SSHReachable=False + §30.1 reason）。返回的错误仅供 HTTP 呈现。
	ProbeAndRecord(ctx context.Context, machine string) error
}

// IncludeAPI 是 OpenSSH include 导出的窄依赖（由 controller/sshops 实现）。
type IncludeAPI interface {
	RenderInclude(ctx context.Context) (*sshtransport.IncludeRender, error)
	// InstallInclude 在**显式确认**后写入 ~/.ssh/agent-fleet.conf（且同时在
	// data-dir 的 generated/ssh/ 下留一份产物，便于审计与 diff）。
	InstallInclude(ctx context.Context, confirm bool) (string, error)
}

// handleSSHProbe 落实 FR-12.1/§4.2：成功写 SSHReachable=True 与静态信息，
// 失败写 SSHReachable=False + §30.1 的 reason（不坍缩为 unreachable）。
func (s *Server) handleSSHProbe(w http.ResponseWriter, r *http.Request) {
	if s.ssh == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"ssh transport is not wired on this control plane", nil)
		return
	}
	name := r.PathValue("name")
	if _, err := s.machines.Get(r.Context(), name); err != nil {
		writeStoreError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.ssh.ProbeAndRecord(ctx, name); err != nil {
		writeSSHError(w, err)
		return
	}
	m, err := s.machines.Get(r.Context(), name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleSSHInventory 落实 §8.1 的 POST /machines/{id}/inventory：返回 Operation
// 资源（202 语义），readOnly=true（不产生任何变更）。
func (s *Server) handleSSHInventory(w http.ResponseWriter, r *http.Request) {
	if s.reconcile == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"reconcile controller is not wired", nil)
		return
	}
	op, err := s.reconcile.RequestInventory(r.Context(), r.PathValue("name"))
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

// handleSSHIncludePreview 渲染 include 文件内容：默认 text/plain 预览，
// `?download=1` 时以附件形式导出（FR-12.6 的 preview / download 两个动作）。
func (s *Server) handleSSHIncludePreview(w http.ResponseWriter, r *http.Request) {
	if s.include == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"openssh include export is not wired on this control plane", nil)
		return
	}
	render, err := s.include.RenderInclude(r.Context())
	if err != nil {
		writeSSHError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Agent-Fleet-Included-Hosts", fmt.Sprint(len(render.Included)))
	w.Header().Set("X-Agent-Fleet-Skipped-Hosts", fmt.Sprint(len(render.Skipped)))
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="agent-fleet.conf"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(render.Content))
}

// includeInstallRequest 是安装动作的载荷：必须显式确认（FR-12.6：绝不静默
// 改写用户主 SSH 配置）。
type includeInstallRequest struct {
	Action  string `json:"action"`
	Confirm bool   `json:"confirm"`
}

// handleSSHIncludeInstall 是 FR-12.6 的第三个动作：**显式确认后**安装/更新
// include 文件。它只写 `~/.ssh/agent-fleet.conf`，绝不触碰主配置——主配置里的
// `Include ~/.ssh/agent-fleet.conf` 由操作者自己加（响应里回显该指令）。
func (s *Server) handleSSHIncludeInstall(w http.ResponseWriter, r *http.Request) {
	if s.include == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"openssh include export is not wired on this control plane", nil)
		return
	}
	var req includeInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, "invalid JSON body: "+err.Error(), nil)
		return
	}
	if req.Action != "" && req.Action != "install" {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid,
			`action must be "install" (preview/download use GET)`, nil)
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid,
			"install requires explicit confirmation: send {\"action\":\"install\",\"confirm\":true}", nil)
		return
	}
	path, err := s.include.InstallInclude(r.Context(), true)
	if err != nil {
		writeSSHError(w, err)
		return
	}
	render, err := s.include.RenderInclude(r.Context())
	if err != nil {
		writeSSHError(w, err)
		return
	}
	sum := sha256.Sum256([]byte(render.Content))
	writeJSON(w, http.StatusOK, map[string]any{
		"path":              path,
		"includeDirective":  sshtransport.IncludeDirective,
		"contentDigest":     "sha256:" + hex.EncodeToString(sum[:]),
		"includedHosts":     render.Included,
		"skippedHosts":      render.Skipped,
		"primaryConfigHint": "add `" + sshtransport.IncludeDirective + "` to your ~/.ssh/config (fleet never edits it)",
	})
}

// writeSSHError 把 §30.1 六类 reason 映射为 HTTP 状态码（错误体仍是 §6.4 五要素）。
//
// 除带码错误（domain.CodedError / ReasonCoder）外还必须认哨兵 domain.ErrInvalid：
// 渲染器（internal/sshtransport）与各控制器用 `fmt.Errorf("%w: …", domain.ErrInvalid)`
// 表达"入参/规格非法"——它既不带 §30.1 的 reason code，也不是上游节点的故障。
// 漏认会落进默认分支 → 502 Internal，把客户端的输入问题报成上游故障
// （KM-29：include 渲染拒绝 {"reason":"Internal"} 与 §6.4 的 400 Invalid 相反）。
// writeStoreError 是同一条契约的先例；显式 reason code 优先，哨兵只在回退
// Internal 时兜底。
func writeSSHError(w http.ResponseWriter, err error) {
	reason := domain.ReasonOf(err)
	if reason == domain.ReasonInternal && errors.Is(err, domain.ErrInvalid) {
		reason = domain.ReasonInvalid
	}
	status := http.StatusBadGateway
	switch reason {
	case domain.ReasonInvalid, domain.ReasonDesiredStateInvalid, domain.ReasonSkillPathRejected,
		domain.ReasonSkillDigestMismatch, domain.ReasonBundleTooLarge, domain.ReasonArtifactTooLarge:
		status = http.StatusBadRequest
	case domain.ReasonUnsupportedPlatform, domain.ReasonNotImplemented:
		status = http.StatusNotImplemented
	case domain.ReasonAuthenticationFailed, domain.ReasonHostKeyVerificationFailed,
		domain.ReasonDNSResolveFailed, domain.ReasonConnectionTimeout:
		status = http.StatusBadGateway // 上游（被管节点）不可达/不可信：不是客户端的错
	case domain.ReasonMachineBusy, domain.ReasonNodeBusy, domain.ReasonStaleExecutionLock:
		status = http.StatusConflict
	case domain.ReasonNotFound:
		status = http.StatusNotFound
	}
	writeError(w, status, reason, err.Error(), nil)
}

// writeActionError 映射动作端点错误（复用 reconcile 的 409 语义）。
func writeActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrMachineBusy):
		writeError(w, http.StatusConflict, domain.ReasonMachineBusy, err.Error(), nil)
	case errors.Is(err, domain.ErrOpState), errors.Is(err, domain.ErrReplanRequired):
		writeError(w, http.StatusConflict, domain.ReasonOf(err), err.Error(), nil)
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, domain.ReasonNotFound, err.Error(), nil)
	case errors.Is(err, domain.ErrInvalid):
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid, err.Error(), nil)
	default:
		writeSSHError(w, err)
	}
}
