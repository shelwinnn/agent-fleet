package httpapi

// KM-48：上游版本发现端点的 handler 级用例——未装配 501、未知 agent 404、
// 正常报告透传、查询失败仍 200（「没查到」是业务结果，不是传输错误）。

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/upstream"
	"github.com/shelwinnn/agent-fleet/internal/domain"
)

type fakeUpstreamDiscovery struct {
	report upstream.Report
	err    error
	called []string
}

func (f *fakeUpstreamDiscovery) Check(_ context.Context, agentID string) (upstream.Report, error) {
	f.called = append(f.called, agentID)
	return f.report, f.err
}

func newFakeUpstreamDiscovery() *fakeUpstreamDiscovery {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	v := upstream.ParseVersion("0.45.0")
	return &fakeUpstreamDiscovery{
		report: upstream.Report{
			AgentID:   "codex",
			CheckedAt: now,
			Sources: []upstream.SourceResult{
				{
					AgentID: "codex", Method: "npm", Package: "@openai/codex",
					Channel: "latest", Status: upstream.StatusOK,
					Version: &v, QueriedAt: &now, Attempts: 1,
					Platforms: []upstream.PlatformApplicability{
						{Platform: "linux", Arch: "amd64", Status: upstream.VerifiedIsolated,
							Evidence: "KM-46 §5"},
					},
				},
				{
					AgentID: "codex", Method: "brew", Package: "codex",
					Channel: "latest", Status: upstream.StatusFailed, Attempts: 2,
					Failure: &upstream.Failure{
						Reason: upstream.ReasonSourceUnavailable,
						Detail: "brew is not available in PATH", Attempts: 1,
					},
				},
			},
		},
	}
}

func TestUpstreamCheckWithoutWiring(t *testing.T) {
	f := newAPIFixture(t)
	// 未装配发现服务 → 501 + §6.4 错误体（与 SSH 通道同一骨架语义）。
	status, body := f.do("POST", "/api/v1/agents/codex/upstream-versions/check", "")
	if status != http.StatusNotImplemented {
		t.Fatalf("check = %d, want 501 (%v)", status, body)
	}
	if body["reason"] != domain.ReasonNotImplemented {
		t.Fatalf("reason = %v, want NotImplemented", body["reason"])
	}
}

func TestUpstreamCheckUnknownAgentIs404(t *testing.T) {
	f := newAPIFixture(t)
	fake := newFakeUpstreamDiscovery()
	fake.err = errors.New(`upstream: unknown agent "claude"`)
	f.api.upstreamDiscovery = fake

	status, body := f.do("POST", "/api/v1/agents/claude/upstream-versions/check", "")
	if status != http.StatusNotFound {
		t.Fatalf("unknown agent = %d, want 404 (%v)", status, body)
	}
	if body["reason"] != domain.ReasonNotFound {
		t.Fatalf("reason = %v, want NotFound", body["reason"])
	}
}

func TestUpstreamCheckReturnsReport(t *testing.T) {
	f := newAPIFixture(t)
	fake := newFakeUpstreamDiscovery()
	f.api.upstreamDiscovery = fake

	status, body := f.do("POST", "/api/v1/agents/codex/upstream-versions/check", "")
	if status != http.StatusOK {
		t.Fatalf("check = %d, want 200 (%v)", status, body)
	}
	if fake.called[len(fake.called)-1] != "codex" {
		t.Fatalf("Check called with %v", fake.called)
	}
	if body["agent"] != "codex" {
		t.Fatalf("report agent = %v", body["agent"])
	}
	sources, ok := body["sources"].([]any)
	if !ok || len(sources) != 2 {
		t.Fatalf("sources = %v, want 2 entries", body["sources"])
	}
	npm := sources[0].(map[string]any)
	if npm["method"] != "npm" || npm["channel"] != "latest" || npm["status"] != "ok" {
		t.Fatalf("npm entry = %v", npm)
	}
	ver := npm["version"].(map[string]any)
	if ver["raw"] != "0.45.0" || ver["scheme"] != "semver" {
		t.Fatalf("version = %v, want raw+scheme 显式标注", ver)
	}
	if npm["queriedAt"] == nil || npm["platforms"] == nil {
		t.Fatalf("queriedAt/platforms must be recorded: %v", npm)
	}
	// 查询失败的来源同样在报告里，带原因——HTTP 仍是 200。
	brew := sources[1].(map[string]any)
	if brew["status"] != "failed" {
		t.Fatalf("brew status = %v, want failed", brew["status"])
	}
	failure := brew["failure"].(map[string]any)
	if failure["reason"] != "source_unavailable" {
		t.Fatalf("failure = %v, want source_unavailable", failure)
	}
}

func TestUpstreamCheckWrongMethodIsJSON404(t *testing.T) {
	f := newAPIFixture(t)
	f.api.upstreamDiscovery = newFakeUpstreamDiscovery()
	// 本仓约定：仅 POST 注册；其他方法落 catch-all → §6.4 JSON 404。
	status, body := f.do("GET", "/api/v1/agents/codex/upstream-versions/check", "")
	if status != http.StatusNotFound {
		t.Fatalf("GET check = %d, want 404 (%v)", status, body)
	}
	if body["reason"] != domain.ReasonNotFound {
		t.Fatalf("reason = %v, want NotFound", body["reason"])
	}
}
