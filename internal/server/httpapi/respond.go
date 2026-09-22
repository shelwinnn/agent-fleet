package httpapi

import (
	"net/http"
	"time"
)

// errorBody 是统一错误响应（架构 v1.1.2 §6.4/§8.1 子集）：
// {reason, message, operationId?, timestamp, diagnostics?}。
// operationId 由动作类端点（reconcile 等，后续切片）填充。
type errorBody struct {
	Reason      string            `json:"reason"`
	Message     string            `json:"message"`
	OperationID string            `json:"operationId,omitempty"`
	Timestamp   string            `json:"timestamp"`
	Diagnostics map[string]string `json:"diagnostics,omitempty"`
}

func writeError(w http.ResponseWriter, status int, reason, message string, diagnostics map[string]string) {
	writeJSON(w, status, errorBody{
		Reason:      reason,
		Message:     message,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Diagnostics: diagnostics,
	})
}
