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
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/enrollment"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

// enrollFixture：真实存储 + 真实 TokenService 的独立夹具（KM-22 端点用）。
type enrollFixtureT struct {
	srv     *httptest.Server
	deleted map[string]bool
}

func enrollFixture(t *testing.T, withIssuer bool) *enrollFixtureT {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), discardLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := Config{}
	f := &enrollFixtureT{deleted: map[string]bool{}}
	if withIssuer {
		tokens := enrollment.NewTokenService(sqlite.NewEnrollmentStore(db))
		cfg.EnrollTokens = issuerFunc(tokens.Issue)
		cfg.OnMachineDelete = func(ctx context.Context, name string) error {
			f.deleted[name] = true
			return nil
		}
	}
	api := New(cfg, sqlite.NewMachineStore(db), sqlite.NewProfileStore(db), db.PingContext, discardLogger())
	f.srv = httptest.NewServer(api.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type issuerFunc func(ctx context.Context, m, fp string, ttl time.Duration) (string, time.Time, error)

func (f issuerFunc) Issue(ctx context.Context, m, fp string, ttl time.Duration) (string, time.Time, error) {
	return f(ctx, m, fp, ttl)
}

func (f *enrollFixtureT) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("%s %s: decode: %v", method, path, err)
	}
	return resp.StatusCode, out
}

func TestEnrollTokenEndpoint(t *testing.T) {
	f := enrollFixture(t, true)
	f.do(t, "POST", "/api/v1/machines", `{"metadata":{"name":"ws-1"},"spec":{"managementMode":"agentd"}}`)

	fp := "sha256:" + strings.Repeat("ab", 32)
	code, body := f.do(t, "POST", "/api/v1/machines/ws-1/enroll-token",
		`{"csrPubKeySha256":"`+fp+`"}`)
	if code != 201 {
		t.Fatalf("enroll-token = %d, want 201: %v", code, body)
	}
	if tok, _ := body["token"].(string); len(tok) < 32 {
		t.Fatalf("token missing or too short: %v", body)
	}
	if body["machineId"] != "ws-1" {
		t.Fatalf("machineId = %v", body["machineId"])
	}
	// 未知机器 → 404。
	code, _ = f.do(t, "POST", "/api/v1/machines/nope/enroll-token", `{"csrPubKeySha256":"`+fp+`"}`)
	if code != 404 {
		t.Fatalf("unknown machine = %d, want 404", code)
	}
	// 指纹形态非法 → 400。
	code, _ = f.do(t, "POST", "/api/v1/machines/ws-1/enroll-token", `{"csrPubKeySha256":"sha256:xyz"}`)
	if code != 400 {
		t.Fatalf("bad fingerprint = %d, want 400", code)
	}
	// 未配置签发器 → 501。
	f2 := enrollFixture(t, false)
	f2.do(t, "POST", "/api/v1/machines", `{"metadata":{"name":"ws-1"}}`)
	code, _ = f2.do(t, "POST", "/api/v1/machines/ws-1/enroll-token", `{"csrPubKeySha256":"`+fp+`"}`)
	if code != 501 {
		t.Fatalf("no issuer = %d, want 501", code)
	}
}

func TestMachineDeleteInvokesHook(t *testing.T) {
	f := enrollFixture(t, true)
	f.do(t, "POST", "/api/v1/machines", `{"metadata":{"name":"ws-1"}}`)
	code, _ := f.do(t, "DELETE", "/api/v1/machines/ws-1", "")
	if code != 204 {
		t.Fatalf("delete = %d, want 204", code)
	}
	if !f.deleted["ws-1"] {
		t.Fatal("OnMachineDelete hook not invoked")
	}
}

func TestCatchAllReturnsErrorBody(t *testing.T) {
	// KM-21 核查发现 #3：未匹配路径统一走 §6.4 错误体，不再回落纯文本 404。
	f := enrollFixture(t, false)
	code, body := f.do(t, "GET", "/api/v1/machines/", "")
	if code != 404 {
		t.Fatalf("unmatched path = %d, want 404", code)
	}
	if body["reason"] != domain.ReasonNotFound {
		t.Fatalf("reason = %v, want %s", body["reason"], domain.ReasonNotFound)
	}
	if _, ok := body["timestamp"]; !ok {
		t.Fatal("error body must carry timestamp (§6.4 五要素)")
	}
	// 已注册路由不受 catch-all 影响。
	code, _ = f.do(t, "GET", "/healthz", "")
	if code != 200 {
		t.Fatalf("healthz = %d, want 200", code)
	}
}
