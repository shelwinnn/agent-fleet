// 上游版本发现端点（KM-48，T3）：POST /api/v1/agents/{agent}/upstream-versions/check。
//
// 与 T2 契约的对齐状态：T2（KM-47，生命周期操作模型）正并行落地 proto/OpenAPI
// 契约；本端点先挂在现有 fleet-v1.yaml 上，路径与响应形状遵循本文件既有约定
// （动作动词后缀、§6.4 错误体、UTC RFC3339 时间）。T2 契约落地评审时，
// 由 T2 把本端点收编或更名——本 handler 只依赖 internal/upstream
// 的窄接口（UpstreamDiscoveryAPI），迁移不动实现。
package httpapi

import (
	"context"
	"net/http"

	"github.com/shelwinnn/agent-fleet/internal/upstream"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// UpstreamDiscoveryAPI 是 httpapi 对上游版本发现服务的窄依赖。
type UpstreamDiscoveryAPI interface {
	// Check 对指定 agent 的全部已声明来源各做一次上游查询。
	// 未知 agent 返回错误（映射 404）。
	Check(ctx context.Context, agentID string) (upstream.Report, error)
}

func (s *Server) handleUpstreamVersionCheck(w http.ResponseWriter, r *http.Request) {
	if s.upstreamDiscovery == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"upstream version discovery is not wired", nil)
		return
	}
	report, err := s.upstreamDiscovery.Check(r.Context(), r.PathValue("agent"))
	if err != nil {
		// Check 只有未知 agent 一种错误；服务内部的查询失败都进
		// Report（逐源 failed + 原因 + 缓存回退），不走 HTTP 错误体——
		// 「没查到」是正常的业务结果，不是传输层错误。
		writeError(w, http.StatusNotFound, domain.ReasonNotFound, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
