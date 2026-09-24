package httpapi

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/server/sse"
)

// TestEventsStreamDeliversRESTWrites 断言 §8.1/§23.6：
// `GET /api/v1/events` 以 `event: <resource-type>` + `data: {id, revision}` 推送，
// 且事件由 REST 写入触发、经访问日志中间件即时 flush。
func TestEventsStreamDeliversRESTWrites(t *testing.T) {
	f, hub := eventsFixtureWithHub(t, "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	reader := bufio.NewReader(resp.Body)
	readSSERecord(t, reader) // retry 提示
	waitHubSubscribers(t, hub, 1)

	// 经 REST 创建 profile → 存储层写成功 → 事件到达已建立的流。
	code, body := f.do(t, http.MethodPost, "/api/v1/profiles",
		`{"metadata":{"name":"default-dev"},"spec":{"agents":{}}}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create profile = %d: %v", code, body)
	}
	rec := readSSERecord(t, reader)
	if rec != "event: profiles\ndata: {\"id\":\"default-dev\",\"revision\":1}" {
		t.Fatalf("event record = %q", rec)
	}

	// 机器写入（同一存储层钩子：控制器与 REST 共用）同样产生事件。
	if code, body := f.do(t, http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"ws-1"},"spec":{"managementMode":"agentd"}}`, nil); code != http.StatusCreated {
		t.Fatalf("create machine = %d: %v", code, body)
	}
	rec = readSSERecord(t, reader)
	if rec != "event: machines\ndata: {\"id\":\"ws-1\",\"revision\":1}" {
		t.Fatalf("machine event record = %q", rec)
	}
}

// TestEventsRequiresAdminToken 断言 SSE 与其余 /api/v1 端点同一鉴权模型（§29.14）。
func TestEventsRequiresAdminToken(t *testing.T) {
	f, _ := eventsFixtureWithHub(t, "s3cret")

	code, body := f.do(t, http.MethodGet, "/api/v1/events", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("no token = %d (%v), want 401", code, body)
	}
	if body["reason"] != domain.ReasonUnauthorized {
		t.Fatalf("reason = %v, want Unauthorized", body["reason"])
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect with token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with token = %d, want 200", resp.StatusCode)
	}
}

// TestEventsNotImplementedWithoutHub 断言未装配枢纽时该路由保持骨架语义（501）。
func TestEventsNotImplementedWithoutHub(t *testing.T) {
	f := newFixture(t, Config{})
	code, body := f.do(t, http.MethodGet, "/api/v1/events", "", nil)
	if code != http.StatusNotImplemented {
		t.Fatalf("status = %d (%v), want 501", code, body)
	}
	if body["reason"] != domain.ReasonNotImplemented {
		t.Fatalf("reason = %v, want NotImplemented", body["reason"])
	}
}

// eventsFixtureWithHub 装配 cfg.Events = hub.Handler() 的 fixture。
func eventsFixtureWithHub(t *testing.T, adminToken string) (*fixture, *sse.Hub) {
	t.Helper()
	hub := sse.NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := newFixture(t, Config{AdminToken: adminToken, Events: hub.Handler()})
	f.db.SetChangeSink(hub.Publish)
	return f, hub
}

func readSSERecord(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v (got %q)", err, strings.Join(lines, ""))
		}
		line = strings.TrimRight(line, "\n")
		if line == "" {
			return strings.Join(lines, "\n")
		}
		lines = append(lines, line)
	}
}

func waitHubSubscribers(t *testing.T, hub *sse.Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hub.Subscribers() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscribers = %d, want %d", hub.Subscribers(), want)
}
