package sse

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testHub() *Hub { return NewHub(slog.New(slog.NewTextHandler(io.Discard, nil))) }

// readRecord 读取一条 SSE 记录（以空行结束），返回原始文本。
func readRecord(t *testing.T, r *bufio.Reader) string {
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

// TestHandlerStreamsSpecFormat 断言 §8.1/§23.6 的事件形状：
// `event: <resource-type>` + `data: {id, revision}`。
func TestHandlerStreamsSpecFormat(t *testing.T) {
	hub := testHub()
	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(resp.Body)
	if rec := readRecord(t, reader); !strings.HasPrefix(rec, "retry: ") {
		t.Fatalf("first record = %q, want retry hint", rec)
	}
	waitForSubscribers(t, hub, 1)

	hub.Publish(ResourceMachines, "gpu-home", 7)

	rec := readRecord(t, reader)
	want := "event: machines\ndata: {\"id\":\"gpu-home\",\"revision\":7}"
	if rec != want {
		t.Fatalf("event record =\n%q\nwant\n%q", rec, want)
	}

	// 同一连接上第二条事件（含 operations 类型）也必须按序到达。
	hub.Publish(ResourceOperations, "op-1", 2)
	rec = readRecord(t, reader)
	want = "event: operations\ndata: {\"id\":\"op-1\",\"revision\":2}"
	if rec != want {
		t.Fatalf("second event record =\n%q\nwant\n%q", rec, want)
	}
}

// TestPublishReachesAllSubscribers 断言广播语义（多订阅者各自收到同一事件）。
func TestPublishReachesAllSubscribers(t *testing.T) {
	hub := testHub()
	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()

	const n = 3
	readers := make([]*bufio.Reader, 0, n)
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
		defer resp.Body.Close()
		r := bufio.NewReader(resp.Body)
		readRecord(t, r) // retry 提示
		readers = append(readers, r)
	}
	waitForSubscribers(t, hub, n)

	hub.Publish(ResourceProfiles, "default-dev", 4)
	for i, r := range readers {
		rec := readRecord(t, r)
		if !strings.Contains(rec, "event: profiles") || !strings.Contains(rec, `"revision":4`) {
			t.Fatalf("subscriber %d got %q", i, rec)
		}
	}
}

// TestSlowSubscriberEvicted 断言缓冲溢出时摘除连接（而非静默丢事件）：
// 客户端重连后会做全量重取，UI 不会停在过期视图上。
func TestSlowSubscriberEvicted(t *testing.T) {
	hub := testHub()
	hub.buffer = 2 // 测试用极小缓冲
	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	readRecord(t, r)
	waitForSubscribers(t, hub, 1)

	for i := 0; i < 5; i++ { // 不读取，撑爆缓冲
		hub.Publish(ResourceMachines, "m", int64(i))
	}
	waitForSubscribers(t, hub, 0)
	if _, evicted, _ := hub.Stats(); evicted == 0 {
		t.Fatal("evicted counter = 0, want >= 1")
	}
	// 被摘除的连接应被关闭：读到 EOF（已缓冲的 2 条之后）。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
	}
	t.Fatal("evicted subscriber connection still open")
}

// TestHandlerRequiresFlusher 断言不可流式的 ResponseWriter 返回 §6.4 形状错误体。
func TestHandlerRequiresFlusher(t *testing.T) {
	hub := testHub()
	rec := httptest.NewRecorder() // httptest.ResponseRecorder 实现 Flusher；用包装类型去掉它
	w := struct{ http.ResponseWriter }{ResponseWriter: rec}
	hub.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body["reason"] != "Internal" || body["timestamp"] == "" {
		t.Fatalf("error body = %v, want reason=Internal + timestamp", body)
	}
}

// TestPublishIgnoresEmptyIdentity 断言空 resource/id 不产生事件（防御性）。
func TestPublishIgnoresEmptyIdentity(t *testing.T) {
	hub := testHub()
	hub.Publish("", "m", 1)
	hub.Publish(ResourceMachines, "", 1)
	if published, _, _ := hub.Stats(); published != 0 {
		t.Fatalf("published = %d, want 0", published)
	}
}

func waitForSubscribers(t *testing.T, hub *Hub, want int) {
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
