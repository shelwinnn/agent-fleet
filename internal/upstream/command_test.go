package upstream

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle/testenv"
)

// 受控版本源端到端：CommandQuerier 生产接线 —— fake npm / fake brew 可执行
// 回放受控注册表应答。与 KM-46 同一隔离纪律：临时 HOME、PATH 首位放替身，
// 不触宿主包管理器；只读查询，无任何安装/卸载路径。

func newIsolatedQuerier(t *testing.T) (*CommandQuerier, *testenv.Env) {
	t.Helper()
	e, err := testenv.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, prefix, registry, path := e.Environ()
	t.Setenv("HOME", home)
	t.Setenv("NPM_PREFIX", prefix)
	t.Setenv("FAKE_NPM_REGISTRY", registry)
	t.Setenv("PATH", path)
	return NewCommandQuerier(), e
}

func TestCommandQuerierNpmOk(t *testing.T) {
	q, e := newIsolatedQuerier(t)
	if err := e.Publish("@openai/codex", "0.45.0", "codex", "codex-cli 0.45.0"); err != nil {
		t.Fatal(err)
	}
	raw, err := q.LatestVersion(context.Background(), npmDef())
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if raw != "0.45.0" {
		t.Fatalf("raw = %q, want 0.45.0", raw)
	}
}

func TestCommandQuerierNpmQueryFailure(t *testing.T) {
	q, e := newIsolatedQuerier(t)
	if err := e.Publish("@openai/codex", "0.45.0", "codex", "codex-cli 0.45.0"); err != nil {
		t.Fatal(err)
	}
	// 受控源注入查询失败（.fail-view → npm view 非零退出）。
	if err := e.SetFlag(".fail-view", true); err != nil {
		t.Fatal(err)
	}
	if _, err := q.LatestVersion(context.Background(), npmDef()); err == nil {
		t.Fatal("expected error on injected view failure")
	} else if reason, retryable := classifyError(err); reason != ReasonQueryFailed || !retryable {
		t.Fatalf("classified = (%s, %v), want (query_failed, true)", reason, retryable)
	}
}

func TestCommandQuerierNpmMissingRegistryEntry(t *testing.T) {
	q, _ := newIsolatedQuerier(t)
	// 注册表里没有 @openai/codex：等价真实 npm 对不存在包的非零退出。
	if _, err := q.LatestVersion(context.Background(), npmDef()); err == nil {
		t.Fatal("expected error for missing registry entry")
	}
}

func TestCommandQuerierNpmNotInPath(t *testing.T) {
	q, _ := newIsolatedQuerier(t)
	t.Setenv("PATH", t.TempDir()) // 无 npm 的 PATH
	_, err := q.LatestVersion(context.Background(), npmDef())
	if err == nil {
		t.Fatal("expected error without npm in PATH")
	}
	if reason, retryable := classifyError(err); reason != ReasonSourceUnavailable || retryable {
		t.Fatalf("classified = (%s, %v), want (source_unavailable, false)", reason, retryable)
	}
}

func TestCommandQuerierBrewCaskAndFormula(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		wantErr bool
	}{
		{"cask version", `{"casks":[{"version":"1.2.3_1"}]}`, "1.2.3_1", false},
		{"formula fallback", `{"formulae":[{"versions":{"stable":"2024.11"}}]}`, "2024.11", false},
		{"no version", `{"casks":[],"formulae":[]}`, "", true},
		{"malformed json", `not-json`, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := &CommandQuerier{brewOutput: func(context.Context, []string) ([]byte, error) {
				return []byte(tc.payload), nil
			}}
			got, err := q.LatestVersion(context.Background(), CodexSources()[1]) // brew
			if tc.wantErr {
				if err == nil {
					t.Fatalf("raw = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("LatestVersion: %v", err)
			}
			if got != tc.want {
				t.Fatalf("raw = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommandQuerierBrewFakeExecutable(t *testing.T) {
	// 走真实 exec 路径：PATH 首位放 fake brew（POSIX sh 替身），验证
	// lookPath → exec → 解析整链。macOS 真机行为仍属未验证（T1 矩阵），
	// 本测试只证明本层对受控应答的处理。
	bin := t.TempDir()
	script := `#!/bin/sh
if [ -n "${FAKE_BREW_FAIL:-}" ]; then echo "brew: injected failure" >&2; exit 1; fi
if [ "${1:-}" != "info" ] || [ "${2:-}" != "--json=v2" ]; then echo "usage" >&2; exit 64; fi
printf '{"casks":[{"version":"%s"}]}\n' "${FAKE_BREW_VERSION:-}"
`
	if err := os.WriteFile(filepath.Join(bin, "brew"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_BREW_VERSION", "2.3.1")

	q := NewCommandQuerier()
	raw, err := q.LatestVersion(context.Background(), CodexSources()[1])
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if raw != "2.3.1" {
		t.Fatalf("raw = %q, want 2.3.1", raw)
	}

	// 注入 brew 失败 → 可重试的 query_failed。
	t.Setenv("FAKE_BREW_FAIL", "1")
	if _, err := q.LatestVersion(context.Background(), CodexSources()[1]); err == nil {
		t.Fatal("expected error on injected brew failure")
	} else if reason, retryable := classifyError(err); reason != ReasonQueryFailed || !retryable {
		t.Fatalf("classified = (%s, %v), want (query_failed, true)", reason, retryable)
	}
}

func TestServiceEndToEndWithControlledSource(t *testing.T) {
	// Service 全链路（能力门 → 查询 → 解释 → 缓存回退）在受控源上的表现。
	q, e := newIsolatedQuerier(t)
	if err := e.Publish("@openai/codex", "0.45.0", "codex", "codex-cli 0.45.0"); err != nil {
		t.Fatal(err)
	}
	svc := New(Config{}, q)

	first, err := svc.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	npm := mustResult(t, first, "npm")
	if npm.Status != StatusOK || npm.Version == nil || npm.Version.Raw != "0.45.0" {
		t.Fatalf("npm result = %+v, want ok 0.45.0", npm)
	}

	if err := e.SetFlag(".fail-view", true); err != nil {
		t.Fatal(err)
	}
	second, err := svc.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	npm2 := mustResult(t, second, "npm")
	if npm2.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", npm2.Status)
	}
	if npm2.Failure == nil || npm2.Failure.Reason != ReasonQueryFailed {
		t.Fatalf("failure = %+v, want query_failed", npm2.Failure)
	}
	if npm2.LastSuccess == nil || npm2.LastSuccess.Version.Raw != "0.45.0" {
		t.Fatalf("lastSuccess = %+v, want cached 0.45.0", npm2.LastSuccess)
	}
	if !npm2.LastSuccess.QueriedAt.Equal(*npm.QueriedAt) {
		t.Errorf("lastSuccess.queriedAt = %v, want首次成功的 %v（缓存不伪装成新查询）",
			npm2.LastSuccess.QueriedAt, npm.QueriedAt)
	}
	// 未知 ≠ 最新：失败结果必须不携带本次查询到的版本。
	if npm2.Version != nil {
		t.Errorf("failed result carries version %+v", npm2.Version)
	}
}

func TestServiceEndToEndParseFailureWithControlledSource(t *testing.T) {
	// 受控源发布非版本串 latest：上游 ParseVersion 同款语义下该串不可解释，
	// 本服务必须显式上报 version_parse_failed（验收：不静默降级）。
	q, e := newIsolatedQuerier(t)
	if err := e.Publish("@openai/codex", "0.45.0", "codex", "codex-cli 0.45.0"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetLatest("@openai/codex", "cheese"); err != nil {
		t.Fatal(err)
	}
	svc := New(Config{}, q)

	rep, err := svc.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Failure == nil || got.Failure.Reason != ReasonParseFailed {
		t.Fatalf("failure = %+v, want version_parse_failed", got.Failure)
	}
	if got.Version != nil {
		t.Errorf("parse failure returned version %+v", got.Version)
	}
}
