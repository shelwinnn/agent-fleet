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

	lifecyclectl "github.com/shelwinnn/agent-fleet/internal/controller/lifecycle"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

// lifecycleFixture：REST 端点测试环境（真实 SQLite + 真实编排控制器，
// Dispatcher 不接线——REST 层语义与派发无关）。
type lifecycleFixture struct {
	t    *testing.T
	srv  *httptest.Server
	ctrl *lifecyclectl.Controller
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	machines := sqlite.NewMachineStore(db)
	observed := sqlite.NewObservedStateStore(db)
	lcRepo := sqlite.NewLifecycleStore(db)
	ctrl := lifecyclectl.New(lcRepo, machines, observed, lifecyclectl.Config{}, quietLogger())
	api := New(Config{}, machines, nil, nil, nil, nil,
		sqlite.NewOperationStore(db), nil, nil, "fixture/v1", db.PingContext,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.SetLifecycle(ctrl)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &lifecycleFixture{t: t, srv: srv, ctrl: ctrl}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (f *lifecycleFixture) do(method, path, body string) (int, map[string]any) {
	f.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		f.t.Fatalf("new request: %v", err)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && err != io.EOF {
		f.t.Fatalf("%s %s: decode response: %v", method, path, err)
	}
	return resp.StatusCode, out
}

func TestLifecycleOperationsEndpointContract(t *testing.T) {
	f := newLifecycleFixture(t)
	// 机器必须已在台账。
	code, obj := f.do(http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"m1"},"spec":{"managementMode":"agentd"}}`)
	if code != http.StatusCreated {
		t.Fatalf("seed machine: %d %v", code, obj)
	}

	body := `{"type":"Install","idempotencyKey":"rest-1","version":"0.44.0",
		"targets":[{"machine":"m1","user":"alice","agent":"codex","method":"npm","package":"@openai/codex"}]}`
	code, obj = f.do(http.MethodPost, "/api/v1/lifecycle/operations", body)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, obj)
	}
	if obj["duplicate"] != false {
		t.Fatalf("duplicate flag = %v, want false", obj["duplicate"])
	}
	opID, _ := obj["metadata"].(map[string]any)["name"].(string)
	if opID == "" {
		t.Fatalf("operation id missing: %v", obj)
	}

	// 幂等复用：同键同内容 → 200 + duplicate=true。
	code, obj = f.do(http.MethodPost, "/api/v1/lifecycle/operations", body)
	if code != http.StatusOK || obj["duplicate"] != true {
		t.Fatalf("idempotent replay: %d duplicate=%v", code, obj["duplicate"])
	}

	// 未确认不可派发：查询应看到 confirmedAt 缺省。
	code, obj = f.do(http.MethodGet, "/api/v1/lifecycle/operations/"+opID, "")
	if code != http.StatusOK {
		t.Fatalf("get: %d", code)
	}
	if status := obj["status"].(map[string]any); status["confirmedAt"] != nil {
		t.Fatalf("confirmedAt should be empty before confirm: %v", status["confirmedAt"])
	}

	// 确认 → confirmedAt 落库。
	code, obj = f.do(http.MethodPost, "/api/v1/lifecycle/operations/"+opID+"/confirm", "")
	if code != http.StatusOK {
		t.Fatalf("confirm: %d %v", code, obj)
	}
	if status := obj["status"].(map[string]any); status["confirmedAt"] == nil {
		t.Fatal("confirmedAt not recorded")
	}

	// 幂等键冲突：同键不同内容 → 409。
	code, obj = f.do(http.MethodPost, "/api/v1/lifecycle/operations",
		`{"type":"Install","idempotencyKey":"rest-1","version":"0.45.0",
		"targets":[{"machine":"m1","user":"alice","agent":"codex","method":"npm","package":"@openai/codex"}]}`)
	if code != http.StatusConflict {
		t.Fatalf("conflict: %d %v", code, obj)
	}
	if obj["reason"] != domain.ReasonIdempotencyConflict {
		t.Fatalf("reason = %v, want IdempotencyConflict", obj["reason"])
	}

	// 阻塞组合 → 422（binary 通道跨平台阻塞，创建即拒）。
	code, obj = f.do(http.MethodPost, "/api/v1/lifecycle/operations",
		`{"type":"Install","idempotencyKey":"rest-2","version":"0.44.0",
		"targets":[{"machine":"m1","user":"alice","agent":"codex","method":"binary","package":"codex"}]}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("blocked combo: %d %v", code, obj)
	}
	if obj["reason"] != domain.ReasonCapabilityBlocked {
		t.Fatalf("reason = %v, want CapabilityBlocked", obj["reason"])
	}

	// 未知操作 → 404。
	code, _ = f.do(http.MethodGet, "/api/v1/lifecycle/operations/no-such-op", "")
	if code != http.StatusNotFound {
		t.Fatalf("get unknown: %d", code)
	}
	code, _ = f.do(http.MethodPost, "/api/v1/lifecycle/operations/no-such-op/confirm", "")
	if code != http.StatusNotFound {
		t.Fatalf("confirm unknown: %d", code)
	}
}

func TestLifecycleRetryEndpointRejectsNonFailed(t *testing.T) {
	f := newLifecycleFixture(t)
	f.do(http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"m1"},"spec":{"managementMode":"agentd"}}`)
	code, obj := f.do(http.MethodPost, "/api/v1/lifecycle/operations",
		`{"type":"Install","idempotencyKey":"retry-e2e","version":"0.44.0",
		"targets":[{"machine":"m1","user":"alice","agent":"codex","method":"npm","package":"@openai/codex"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, obj)
	}
	opID := obj["metadata"].(map[string]any)["name"].(string)
	// 目标仍 pending（未失败）：重试被拒（仅确定失败目标可重试）。
	code, obj = f.do(http.MethodPost, "/api/v1/lifecycle/operations/"+opID+"/retry",
		`{"targets":[{"machine":"m1","user":"alice","agent":"codex","method":"npm","package":"@openai/codex"}]}`)
	if code != http.StatusConflict {
		t.Fatalf("retry non-failed: %d %v", code, obj)
	}
	if obj["reason"] != domain.ReasonRetryNotAllowed && obj["reason"] != domain.ReasonInvalid {
		t.Fatalf("reason = %v", obj["reason"])
	}
}

func TestLifecycleDesiredAndCapabilitiesEndpoints(t *testing.T) {
	f := newLifecycleFixture(t)
	f.do(http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"m1"},"spec":{"managementMode":"agentd"}}`)

	// 从未纳管 → 404（"未纳管"是可查询的显式状态，不是 absent）。
	code, _ := f.do(http.MethodGet, "/api/v1/machines/m1/lifecycle/desired", "")
	if code != http.StatusNotFound {
		t.Fatalf("never-managed desired: %d, want 404", code)
	}

	// PUT 合并语义：显式条目进入。
	code, obj := f.do(http.MethodPut, "/api/v1/machines/m1/lifecycle/desired",
		`{"entries":{"codex":{"intent":"present","version":"0.44.0","method":"npm","package":"@openai/codex"}}}`)
	if code != http.StatusOK {
		t.Fatalf("put desired: %d %v", code, obj)
	}
	if obj["revision"].(float64) != 1 {
		t.Fatalf("revision = %v", obj["revision"])
	}
	// 再 PUT 未提及 codex 的更新：原样保留（省略 ≠ 卸载）且内容无变化不新增代。
	code, obj = f.do(http.MethodPut, "/api/v1/machines/m1/lifecycle/desired", `{"entries":{}}`)
	if code != http.StatusOK {
		t.Fatalf("put empty: %d %v", code, obj)
	}
	if rev := obj["revision"].(float64); rev != 1 {
		t.Fatalf("unchanged content must not create a new revision: %v", rev)
	}
	entries := obj["entries"].(map[string]any)
	if _, ok := entries["codex"]; !ok {
		t.Fatalf("omitted agent dropped from desired (implicit uninstall!): %v", entries)
	}

	// 能力矩阵：blocked/unverified 如实展示，supported 为空集（T8 前）。
	code, obj = f.do(http.MethodGet, "/api/v1/lifecycle/capabilities", "")
	if code != http.StatusOK {
		t.Fatalf("capabilities: %d", code)
	}
	items := obj["items"].([]any)
	if len(items) == 0 {
		t.Fatal("capability registry empty")
	}
	for _, it := range items {
		if it.(map[string]any)["state"] == domain.CapabilitySupported {
			t.Fatal("supported entries must not exist before T8 real-machine evidence")
		}
	}
}
