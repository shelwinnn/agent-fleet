package httpapi

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// spaHandler 返回 Web UI 静态产物目录的处理器（§3.6/§4.1：127.0.0.1:7788 =
// Web + REST + SSE + 静态 SPA；架构图中 SPA 标注"由 server 托管"）。
//
// 设计约束（KM-25 核查必改 M3）：
//   - **不用 go:embed**：web/dist 是 gitignore 的构建产物，embed 会让
//     `go build ./...` 在没有前端产物的干净树上直接失败（CI 就是这么跑的）；
//   - 目录不存在时**不注册**静态托管，只保留 API（并打日志说明），
//     因此 Go 构建与前端产物互不耦合；
//   - UI 用 hash 路由，深链接不需要服务端 rewrite 回退。
func spaHandler(dir string, log *slog.Logger) (http.Handler, bool) {
	if dir == "" {
		log.Info("spa static serving disabled (no --spa-dir)")
		return nil, false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		log.Info("spa static serving disabled (directory not found)", "dir", dir)
		return nil, false
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		log.Info("spa static serving disabled (index.html not found)", "dir", dir)
		return nil, false
	}
	log.Info("spa static serving enabled", "dir", dir)
	return http.FileServer(http.Dir(dir)), true
}

// isAPIPath 判定该请求是否属于 REST/探针命名空间：这些路径**不**回落到 SPA，
// 未匹配时仍返回 §6.4 错误体（KM-21 核查既有行为，不得回退成 HTML 404）。
func isAPIPath(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}
