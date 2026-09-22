package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

type fixture struct {
	srv *httptest.Server
	db  *sqlite.DB
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := New(cfg, sqlite.NewMachineStore(db), sqlite.NewProfileStore(db), db.PingContext,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &fixture{srv: srv, db: db}
}

func (f *fixture) do(t *testing.T, method, path, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("%s %s: decode response: %v", method, path, err)
	}
	return resp.StatusCode, out
}

// TestMachineCRUDLifecycle 走一遍 machines 的完整 CRUD 与错误路径。
func TestMachineCRUDLifecycle(t *testing.T) {
	f := newFixture(t, Config{})

	// 创建：服务端补齐 metadata，status 初始化为 {}。
	body := `{"metadata":{"name":"node-1","uid":"client-should-ignore"},"spec":{"managementMode":"agentd","profileRef":"default"}}`
	code, obj := f.do(t, http.MethodPost, "/api/v1/machines", body, nil)
	if code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %v", code, obj)
	}
	meta := obj["metadata"].(map[string]any)
	if meta["uid"] == "client-should-ignore" || meta["uid"] == "" {
		t.Fatalf("uid not server-assigned: %v", meta)
	}
	if meta["resourceVersion"].(float64) != 1 {
		t.Fatalf("resourceVersion = %v, want 1", meta["resourceVersion"])
	}
	if got, ok := obj["status"].(map[string]any); !ok || len(got) != 0 {
		t.Fatalf("status = %v, want empty object", obj["status"])
	}

	// 重复创建 → 409 AlreadyExists。
	code, errBody := f.do(t, http.MethodPost, "/api/v1/machines", body, nil)
	if code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", code)
	}
	if errBody["reason"] != domain.ReasonAlreadyExists {
		t.Fatalf("reason = %v, want AlreadyExists", errBody["reason"])
	}

	// 读取。
	code, obj = f.do(t, http.MethodGet, "/api/v1/machines/node-1", "", nil)
	if code != http.StatusOK || obj["metadata"].(map[string]any)["name"] != "node-1" {
		t.Fatalf("get status = %d obj = %v", code, obj)
	}

	// 更新 spec：status 不被客户端改写，resource_version 递增。
	code, obj = f.do(t, http.MethodPut, "/api/v1/machines/node-1",
		`{"metadata":{"name":"node-1"},"spec":{"managementMode":"ssh","profileRef":"p2"},"status":{"hacked":true}}`, nil)
	if code != http.StatusOK {
		t.Fatalf("update status = %d: %v", code, obj)
	}
	if obj["metadata"].(map[string]any)["resourceVersion"].(float64) != 2 {
		t.Fatalf("resourceVersion after update = %v, want 2", obj["metadata"].(map[string]any)["resourceVersion"])
	}
	if status := obj["status"].(map[string]any); len(status) != 0 {
		t.Fatalf("status after update = %v, want preserved empty object", status)
	}
	var stored domain.Machine
	if err := json.Unmarshal(mustJSON(t, obj), &stored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// 列表 + name 过滤。
	_, _ = f.do(t, http.MethodPost, "/api/v1/machines", `{"metadata":{"name":"node-2"}}`, nil)
	code, list := f.do(t, http.MethodGet, "/api/v1/machines", "", nil)
	if code != http.StatusOK || len(list["items"].([]any)) != 2 {
		t.Fatalf("list status = %d items = %v", code, list["items"])
	}
	code, list = f.do(t, http.MethodGet, "/api/v1/machines?name=node-2", "", nil)
	if got := len(list["items"].([]any)); code != http.StatusOK || got != 1 {
		t.Fatalf("filtered list status = %d len = %d, want 200/1", code, got)
	}

	// 删除后 404，错误体含时间戳。
	code, _ = f.do(t, http.MethodDelete, "/api/v1/machines/node-1", "", nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", code)
	}
	code, errBody = f.do(t, http.MethodGet, "/api/v1/machines/node-1", "", nil)
	if code != http.StatusNotFound || errBody["reason"] != domain.ReasonNotFound {
		t.Fatalf("get after delete: status = %d reason = %v", code, errBody["reason"])
	}
	if errBody["timestamp"] == "" {
		t.Fatal("error body missing timestamp")
	}
}

func TestValidationErrors(t *testing.T) {
	f := newFixture(t, Config{})

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"bad name", http.MethodPost, "/api/v1/machines", `{"metadata":{"name":"-bad-"},"spec":{}}`},
		{"missing name", http.MethodPost, "/api/v1/machines", `{"spec":{}}`},
		{"bad managementMode", http.MethodPost, "/api/v1/machines", `{"metadata":{"name":"x"},"spec":{"managementMode":"teleport"}}`},
		{"spec not object", http.MethodPost, "/api/v1/machines", `{"metadata":{"name":"x"},"spec":[1,2]}`},
		{"unknown top field", http.MethodPost, "/api/v1/machines", `{"metadata":{"name":"x"},"spec":{},"specx":1}`},
		{"name mismatch on update", http.MethodPut, "/api/v1/machines/x", `{"metadata":{"name":"y"},"spec":{}}`},
		{"profile spec not object", http.MethodPost, "/api/v1/profiles", `{"metadata":{"name":"p"},"spec":"oops"}`},
	}
	for _, tc := range cases {
		code, errBody := f.do(t, tc.method, tc.path, tc.body, nil)
		if code != http.StatusBadRequest || errBody["reason"] != domain.ReasonInvalid {
			t.Fatalf("%s: status = %d reason = %v, want 400 Invalid", tc.name, code, errBody["reason"])
		}
	}
}

func TestProfileCRUDAndSkeletons(t *testing.T) {
	f := newFixture(t, Config{})

	code, obj := f.do(t, http.MethodPost, "/api/v1/profiles",
		`{"metadata":{"name":"default"},"spec":{"agents":{"codex":{"enabled":true}}}}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("profile create status = %d: %v", code, obj)
	}
	code, errBody := f.do(t, http.MethodPost, "/api/v1/profiles",
		`{"metadata":{"name":"default"},"spec":{}}`, nil)
	if code != http.StatusConflict || errBody["reason"] != domain.ReasonAlreadyExists {
		t.Fatalf("profile duplicate: status = %d reason = %v", code, errBody["reason"])
	}

	for _, path := range []string{
		"/api/v1/skills", "/api/v1/skills/x",
		"/api/v1/providers", "/api/v1/providers/x",
		"/api/v1/deployments", "/api/v1/deployments/x",
	} {
		code, errBody := f.do(t, http.MethodGet, path, "", nil)
		if code != http.StatusNotImplemented || errBody["reason"] != domain.ReasonNotImplemented {
			t.Fatalf("GET %s: status = %d reason = %v, want 501 NotImplemented", path, code, errBody["reason"])
		}
	}
}

func TestHealthAndMethodNotAllowed(t *testing.T) {
	f := newFixture(t, Config{})

	code, body := f.do(t, http.MethodGet, "/healthz", "", nil)
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz: status = %d body = %v", code, body)
	}
	code, body = f.do(t, http.MethodGet, "/readyz", "", nil)
	if code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("readyz: status = %d body = %v", code, body)
	}

	code, errBody := f.do(t, http.MethodPatch, "/api/v1/machines", "", nil)
	if code != http.StatusMethodNotAllowed || errBody["reason"] != domain.ReasonMethodNotAllowed {
		t.Fatalf("patch machines: status = %d reason = %v", code, errBody["reason"])
	}
}

func TestAdminToken(t *testing.T) {
	f := newFixture(t, Config{AdminToken: "sekret"})

	code, errBody := f.do(t, http.MethodGet, "/api/v1/machines", "", nil)
	if code != http.StatusUnauthorized || errBody["reason"] != domain.ReasonUnauthorized {
		t.Fatalf("no token: status = %d reason = %v", code, errBody["reason"])
	}
	code, errBody = f.do(t, http.MethodGet, "/api/v1/machines", "",
		map[string]string{"Authorization": "Bearer wrong"})
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d", code)
	}
	// healthz/readyz 不做鉴权（仅 /api/v1 受保护）。
	code, _ = f.do(t, http.MethodGet, "/healthz", "", nil)
	if code != http.StatusOK {
		t.Fatalf("healthz with token required = %d, want 200", code)
	}
	code, _ = f.do(t, http.MethodGet, "/api/v1/machines", "",
		map[string]string{"Authorization": "Bearer sekret"})
	if code != http.StatusOK {
		t.Fatalf("correct token: status = %d, want 200", code)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
