package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"

	deployctl "github.com/shelwinnn/agent-fleet/internal/controller/deployment"
	reconcilectl "github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
)

// stackFixture 是与 cmd/agent-fleet-server 同形的**全栈**装配（真实 reconcile 与
// deployment 控制器、真实 SQLite）：M2 的问题只在"REST 创建 → 控制器推进"这条
// 链路上暴露，桩掉控制器就测不出来。
type stackFixture struct {
	*fixture
	reconcile  *reconcilectl.Controller
	deployment *deployctl.Controller
}

func newStackFixture(t *testing.T, cfg Config) *stackFixture {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := openTestDB(t, log)

	machines := sqlite.NewMachineStore(db)
	status := sqlite.NewMachineStatusStore(db)
	profiles := sqlite.NewProfileStore(db)
	skills := sqlite.NewSkillStore(db)
	providers := sqlite.NewProviderStore(db)
	snapshots := sqlite.NewSnapshotStore(db)
	operations := sqlite.NewOperationStore(db)
	observed := sqlite.NewObservedStateStore(db)
	deployments := sqlite.NewDeploymentStore(db)
	targets := sqlite.NewDeploymentTargetStore(db)

	render := reconcilectl.NewRenderer(machines, profiles, skills, providers, "fixture/v1")
	rec := reconcilectl.NewController(machines, status, snapshots, operations, observed, render,
		reconcilectl.Config{PlanTimeout: time.Minute, FreshnessWindow: time.Hour}, log)
	// 刻意不装配 Dispatcher：无活跃 agentd 连接，与联调环境一致（派发即
	// ErrAgentDisconnected，操作为 Failed）。
	dep := deployctl.NewController(deployments, targets, machines, snapshots, operations, observed, rec, log)

	if cfg.DeploymentTargets == nil {
		cfg.DeploymentTargets = targets.List
	}
	api := New(cfg, machines, profiles, skills, providers, deployments, operations,
		rec, dep, "fixture/v1", db.PingContext, log)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &stackFixture{fixture: &fixture{srv: srv, db: db}, reconcile: rec, deployment: dep}
}

// seedMachineWithSnapshot 建 profile + machine 并物化第 1 代期望快照
// （发布的目标代必须可解析，FR-10.1）。
func (s *stackFixture) seedMachineWithSnapshot(t *testing.T, name string) {
	t.Helper()
	ctx := context.Background()
	code, body := s.do(t, http.MethodPost, "/api/v1/profiles",
		`{"metadata":{"name":"default"},"spec":{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create profile = %d: %v", code, body)
	}
	code, body = s.do(t, http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"`+name+`"},"spec":{"managementMode":"agentd","profileRef":"default"}}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create machine = %d: %v", code, body)
	}
	if _, _, err := s.reconcile.EnsureSnapshot(ctx, name); err != nil {
		t.Fatalf("ensure snapshot: %v", err)
	}
}

// TestRESTCreateDeploymentMaterializesTargets 锁定 M2：经 REST 创建的发布必须
// 物化 deployment_targets 并能被控制器按批次推进——而不是"0 个目标 →
// done==total → Failed / no target succeeded"。
func TestRESTCreateDeploymentMaterializesTargets(t *testing.T) {
	s := newStackFixture(t, Config{})
	s.seedMachineWithSnapshot(t, "ws-a")

	code, body := s.do(t, http.MethodPost, "/api/v1/deployments",
		`{"metadata":{"name":"rollout-1"},"spec":{"machineNames":["ws-a"],"targetGeneration":1,`+
			`"strategy":{"canary":1,"batchSize":1,"maxUnavailable":1,"pauseOnFailure":true}}}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create deployment = %d: %v", code, body)
	}

	// 创建响应即带逐机目标行（FR-14.5 第 3 组的数据来源）与初始相位。
	targets := targetsOf(t, body)
	if len(targets) != 1 {
		t.Fatalf("创建响应的 status.targets = %v, want 1 条", targets)
	}
	first := targets[0].(map[string]any)
	if first["machine"] != "ws-a" || first["phase"] != "Pending" {
		t.Fatalf("目标行 = %v, want ws-a/Pending", first)
	}
	if status, _ := body["status"].(map[string]any); status["phase"] != "Pending" {
		t.Fatalf("创建后的 phase = %v, want Pending", status["phase"])
	}

	// 控制器按批次推进（扫描循环的单步）：无 agent 连接 → 该目标最终
	// Failed(AgentDisconnected)，发布级 reason 必须是"目标失败"，
	// 而不是 M2 的病灶"没有目标成功"。
	ctx := context.Background()
	var status map[string]any
	var rows []any
	for i := 0; i < 6; i++ {
		if err := s.deployment.Advance(ctx, "rollout-1"); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
		code, got := s.do(t, http.MethodGet, "/api/v1/deployments/rollout-1", "", nil)
		if code != http.StatusOK {
			t.Fatalf("get deployment = %d: %v", code, got)
		}
		status, _ = got["status"].(map[string]any)
		if status["reason"] == "no target succeeded" {
			t.Fatalf("发布以 no target succeeded 终结（M2 回归）：%v", status)
		}
		rows = targetsOf(t, got)
		if len(rows) == 1 {
			if phase, _ := rows[0].(map[string]any)["phase"].(string); phase == "Failed" {
				break
			}
		}
	}

	if len(rows) != 1 {
		t.Fatalf("推进后 targets = %v, want 1 条（目标行必须由创建路径物化）", rows)
	}
	target := rows[0].(map[string]any)
	if target["phase"] != "Failed" {
		t.Fatalf("推进后目标相位 = %v, want Failed", target["phase"])
	}
	if reason, _ := target["reason"].(string); reason == "" {
		t.Fatalf("Failed 目标必须带原因：%v", target)
	} else {
		t.Logf("目标失败原因：%s；发布 reason=%v phase=%v", reason, status["reason"], status["phase"])
	}
	if phase, _ := status["phase"].(string); phase != "Failed" && phase != "Paused" {
		t.Fatalf("发布相位 = %v, want Failed 或 Paused（失败后按策略暂停）", status["phase"])
	}
}

// TestRESTCreateDeploymentRejectsInvalidSpec 锁定入口校验：显式 machineNames 与
// 可解析的 targetGeneration（FR-10.1）——失败要在入口以 §6.4 错误体报出，
// 不能落库成一条注定失败的发布。
func TestRESTCreateDeploymentRejectsInvalidSpec(t *testing.T) {
	s := newStackFixture(t, Config{})
	s.seedMachineWithSnapshot(t, "ws-a")

	code, body := s.do(t, http.MethodPost, "/api/v1/deployments",
		`{"metadata":{"name":"no-targets"},"spec":{"targetGeneration":1}}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("缺 machineNames = %d (%v), want 400", code, body)
	}
	if body["reason"] != "Invalid" {
		t.Fatalf("reason = %v, want Invalid", body["reason"])
	}

	code, body = s.do(t, http.MethodPost, "/api/v1/deployments",
		`{"metadata":{"name":"bad-gen"},"spec":{"machineNames":["ws-a"],"targetGeneration":99}}`, nil)
	if code != http.StatusConflict {
		t.Fatalf("不可解析的目标代 = %d (%v), want 409", code, body)
	}
	if body["reason"] != "RollbackUnsupported" {
		t.Fatalf("reason = %v, want RollbackUnsupported", body["reason"])
	}

	// 被拒绝的发布不得留下任何行。
	if code, _ := s.do(t, http.MethodGet, "/api/v1/deployments/no-targets", "", nil); code != http.StatusNotFound {
		t.Fatalf("被拒绝的发布仍存在（status=%d）", code)
	}
}

// TestSPAServing 锁定 M3：控制面进程托管 Web UI 静态产物（§3.6/§4.1），
// 同时 **/api 命名空间未匹配时仍返回 §6.4 JSON 404**（KM-21 核查的既有行为，
// 不得回退成 HTML 404）。
func TestSPAServing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<!doctype html><title>fleet-ui</title><div id=\"app\"></div>")
	writeFile(t, filepath.Join(dir, "assets/app.js"), "console.log('ui')")

	s := newStackFixture(t, Config{SPADir: dir})

	code, raw, ctype := s.getRaw(t, "/")
	if code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", code)
	}
	if !contains(raw, "fleet-ui") || !contains(ctype, "text/html") {
		t.Fatalf("GET / 未返回 SPA 首页：ctype=%q body=%q", ctype, raw)
	}

	code, raw, _ = s.getRaw(t, "/assets/app.js")
	if code != http.StatusOK || !contains(raw, "console.log") {
		t.Fatalf("GET /assets/app.js = %d body=%q", code, raw)
	}

	code, raw, ctype = s.getRaw(t, "/api/v1/nope")
	if code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/nope = %d, want 404", code)
	}
	if !contains(ctype, "application/json") || !contains(raw, "NotFound") {
		t.Fatalf("/api 未匹配路径必须仍是 §6.4 JSON 404：ctype=%q body=%q", ctype, raw)
	}
	if contains(raw, "<div id=\"app\"") {
		t.Fatalf("/api 未匹配路径回落到了 SPA：%q", raw)
	}
}

// TestSPAServingDisabledWithoutDir 断言目录缺失时不托管静态资源、行为与装配前一致
// （Go 构建与前端产物解耦：CI 的干净树上 web/dist 不存在）。
func TestSPAServingDisabledWithoutDir(t *testing.T) {
	s := newStackFixture(t, Config{SPADir: filepath.Join(t.TempDir(), "missing")})
	code, raw, ctype := s.getRaw(t, "/")
	if code != http.StatusNotFound {
		t.Fatalf("GET / = %d, want 404（未配置静态目录）", code)
	}
	if !contains(ctype, "application/json") || !contains(raw, "NotFound") {
		t.Fatalf("应为 §6.4 JSON 404：ctype=%q body=%q", ctype, raw)
	}
}

// openTestDB 打开临时库并迁移（httpapi 侧的统一入口）。
func openTestDB(t *testing.T, log *slog.Logger) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// getRaw 取原始响应体与 Content-Type（静态资源不是 JSON，不能用 do 解 JSON）。
func (f *fixture) getRaw(t *testing.T, path string) (int, string, string) {
	t.Helper()
	resp, err := f.srv.Client().Get(f.srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body), resp.Header.Get("Content-Type")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
