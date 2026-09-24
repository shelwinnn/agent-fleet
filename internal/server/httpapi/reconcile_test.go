package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

// apiFixture 装配完整 HTTP 栈（真实 SQLite + 控制器 + fake 派发）。
type apiFixture struct {
	t        *testing.T
	srv      *httptest.Server
	api      *Server
	rec      *reconcile.Controller
	ops      domain.OperationRepository
	observed domain.ObservedStateRepository
	mstatus  domain.MachineStatusRepository
	disp     *fakeDispatch
}

type fakeDispatch struct{ fail bool }

func (d *fakeDispatch) ExecuteOperation(_ context.Context, _ string, _ *domain.Operation, _ *domain.DesiredStateSnapshot) error {
	if d.fail {
		return domain.ErrAgentDisconnected
	}
	return nil
}
func (d *fakeDispatch) CancelOperation(_ context.Context, _, _ string) error { return nil }

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), log); err != nil {
		t.Fatal(err)
	}
	machines := sqlite.NewMachineStore(db)
	profiles := sqlite.NewProfileStore(db)
	snapshots := sqlite.NewSnapshotStore(db)
	ops := sqlite.NewOperationStore(db)
	observed := sqlite.NewObservedStateStore(db)
	disp := &fakeDispatch{}
	render := reconcile.NewRenderer(machines, profiles, sqlite.NewSkillStore(db),
		sqlite.NewProviderStore(db), "fixture/v1")
	rec := reconcile.NewController(machines, sqlite.NewMachineStatusStore(db), snapshots,
		ops, observed, render,
		reconcile.Config{PlanTimeout: 30 * time.Minute, FreshnessWindow: time.Hour}, log)
	rec.SetDispatcher(disp)
	dep := newNoopDeploymentAPI()
	api := New(Config{}, machines, profiles, sqlite.NewSkillStore(db),
		sqlite.NewProviderStore(db), sqlite.NewDeploymentStore(db), ops,
		rec, dep, "fixture/v1", db.PingContext, log)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &apiFixture{t: t, srv: srv, api: api, rec: rec, ops: ops,
		observed: observed, mstatus: sqlite.NewMachineStatusStore(db), disp: disp}
}

type noopDeploymentAPI struct{}

func newNoopDeploymentAPI() DeploymentAPI { return &noopDeploymentAPI{} }

func (n *noopDeploymentAPI) Create(_ context.Context, _ *domain.Deployment) error {
	return domain.ErrNotFound
}
func (n *noopDeploymentAPI) CreateRollback(_ context.Context, name string, gen int64) (*domain.Deployment, error) {
	return nil, domain.ErrNotFound
}
func (n *noopDeploymentAPI) SkipTarget(_ context.Context, _, _, _ string) error {
	return domain.ErrNotFound
}

func (f *apiFixture) do(method, path, body string) (int, map[string]any) {
	f.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		f.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var obj map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil && err != io.EOF {
		f.t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return resp.StatusCode, obj
}

func (f *apiFixture) seed() {
	f.t.Helper()
	code, obj := f.do(http.MethodPost, "/api/v1/profiles",
		`{"metadata":{"name":"default"},"spec":{"agents":{"fixture":{"enabled":true,"version":"1.0.0"}}}}`)
	if code != http.StatusCreated {
		f.t.Fatalf("profile create: %d %v", code, obj)
	}
	code, obj = f.do(http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"ws-1"},"spec":{"managementMode":"agentd","profileRef":"default"}}`)
	if code != http.StatusCreated {
		f.t.Fatalf("machine create: %d %v", code, obj)
	}
}

// 动作端点：202 + Operation；409 MachineBusy 带 diagnostics；确认/取消/跳过/回滚。
func TestReconcileEndpoints(t *testing.T) {
	f := newAPIFixture(t)
	f.seed()

	// 202 语义：返回 Operation 资源（§8.1 约定）。
	code, obj := f.do(http.MethodPost, "/api/v1/machines/ws-1/reconcile", `{}`)
	if code != http.StatusAccepted {
		t.Fatalf("reconcile status = %d: %v", code, obj)
	}
	if obj["status"].(map[string]any)["phase"] != domain.OperationPhasePending {
		t.Fatalf("phase = %v", obj["status"])
	}
	// 未决期间 → 409 MachineBusy + diagnostics（FR-1.10/§8.1）。
	code, obj = f.do(http.MethodPost, "/api/v1/machines/ws-1/reconcile", `{}`)
	if code != http.StatusConflict || obj["reason"] != domain.ReasonMachineBusy {
		t.Fatalf("busy status = %d reason = %v", code, obj["reason"])
	}
	diag := obj["diagnostics"].(map[string]any)
	if diag["phase"] == nil || diag["operationId"] == nil {
		t.Fatalf("diagnostics missing unresolved op ref: %v", diag)
	}
	// 操作列表可查。
	code, obj = f.do(http.MethodGet, "/api/v1/machines/ws-1/operations", "")
	if code != http.StatusOK {
		t.Fatalf("operations list = %d", code)
	}
	if n := len(obj["items"].([]any)); n != 1 {
		t.Fatalf("operations count = %d, want 1", n)
	}
	// 未知机器动作 → 404。
	code, _ = f.do(http.MethodPost, "/api/v1/machines/nope/reconcile", `{}`)
	if code != http.StatusNotFound {
		t.Fatalf("unknown machine = %d, want 404", code)
	}
}

func TestConfirmAndRollbackEndpoints(t *testing.T) {
	f := newAPIFixture(t)
	f.seed()

	// 确认窗口：构造 AwaitingConfirmation。
	op, err := f.rec.RequestPlanConfirmation(context.Background(), "ws-1", "sha256:plan-x", false)
	if err != nil {
		t.Fatal(err)
	}
	// 错误 digest → 409 ReplanRequired。
	code, obj := f.do(http.MethodPost, "/api/v1/machines/ws-1/reconcile",
		`{"confirmPlanDigest":"sha256:wrong"}`)
	if code != http.StatusConflict || obj["reason"] != domain.ReasonReplanRequired {
		t.Fatalf("wrong confirm = %d %v", code, obj["reason"])
	}
	// 正确确认 → 202 + Running。
	code, obj = f.do(http.MethodPost, "/api/v1/machines/ws-1/reconcile",
		`{"confirmPlanDigest":"sha256:plan-x"}`)
	if code != http.StatusAccepted {
		t.Fatalf("confirm = %d %v", code, obj)
	}
	if obj["status"].(map[string]any)["phase"] != domain.OperationPhaseRunning {
		t.Fatalf("confirm phase = %v", obj["status"])
	}
	_ = op

	// 取消与跳过端点。
	code, obj = f.do(http.MethodPost, "/api/v1/machines/ws-1/operations/"+op.Metadata.Name+"/cancel", `{}`)
	if code != http.StatusOK {
		t.Fatalf("cancel = %d %v", code, obj)
	}
	code, obj = f.do(http.MethodPost, "/api/v1/machines/ws-1/operations/"+op.Metadata.Name+"/skip",
		`{"reason":"operator override"}`)
	if code != http.StatusOK {
		t.Fatalf("skip = %d %v", code, obj)
	}
	if obj["status"].(map[string]any)["terminalModifier"] != domain.ModifierSkipped {
		t.Fatalf("skip modifier = %v", obj["status"])
	}
	// 无原因跳过被拒（FR-15.4：不允许静默跳过）。
	code, obj = f.do(http.MethodPost, "/api/v1/machines/ws-1/operations/"+op.Metadata.Name+"/skip", `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("reasonless skip = %d", code)
	}
}

func TestRollbackAndDriftAndRenderEndpoints(t *testing.T) {
	f := newAPIFixture(t)
	f.seed()

	// 渲染预览（FR-7.4）。
	code, obj := f.do(http.MethodGet, "/api/v1/profiles/default/render?machine=ws-1", "")
	if code != http.StatusOK {
		t.Fatalf("render = %d %v", code, obj)
	}
	if obj["digest"] == nil || obj["desired"] == nil {
		t.Fatalf("render payload incomplete: %v", obj)
	}
	// drift 视图：从未采集 → Unknown(NeverInventoried)（T19/FR-14.5）。
	code, obj = f.do(http.MethodGet, "/api/v1/machines/ws-1/drift", "")
	if code != http.StatusOK {
		t.Fatalf("drift = %d", code)
	}
	drifted := obj["drifted"].(map[string]any)
	if drifted["status"] != domain.ConditionUnknown ||
		drifted["reason"] != domain.DriftReasonNeverInventoried {
		t.Fatalf("drift view = %v", drifted)
	}
	// 回滚：目标代不存在 → 409 RollbackUnsupported。
	code, obj = f.do(http.MethodPost, "/api/v1/machines/ws-1/rollback", `{"targetGeneration":42}`)
	if code != http.StatusConflict || obj["reason"] != domain.ReasonRollbackUnsupported {
		t.Fatalf("rollback unknown gen = %d %v", code, obj["reason"])
	}
	// 回滚成功路径：先物化 gen1。
	if _, _, err := f.rec.EnsureSnapshot(context.Background(), "ws-1"); err != nil {
		t.Fatal(err)
	}
	code, obj = f.do(http.MethodPost, "/api/v1/machines/ws-1/rollback", `{"targetGeneration":1}`)
	if code != http.StatusAccepted {
		t.Fatalf("rollback = %d %v", code, obj)
	}
	if obj["spec"].(map[string]any)["type"] != domain.OperationTypeRollback {
		t.Fatalf("rollback op type = %v", obj["spec"])
	}
}
