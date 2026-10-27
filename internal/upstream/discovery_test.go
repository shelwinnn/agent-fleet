package upstream

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// fakeQuerier 是受控版本源：按调用序回放预置结果，不发起任何外部命令。
type fakeQuerier struct {
	mu     sync.Mutex
	resps  []fakeResp // 逐次消费；耗尽后复用最后一个
	calls  []SourceDef
	nowIdx int
}

type fakeResp struct {
	raw string
	err error
}

func (f *fakeQuerier) LatestVersion(_ context.Context, def SourceDef) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, def)
	if len(f.resps) == 0 {
		return "", errors.New("fakeQuerier: no scripted response")
	}
	if f.nowIdx >= len(f.resps) {
		f.nowIdx = len(f.resps) - 1
	}
	r := f.resps[f.nowIdx]
	f.nowIdx++
	return r.raw, r.err
}

func newFakeService(t *testing.T, q *fakeQuerier) *Service {
	t.Helper()
	return New(Config{
		QueryTimeout: 50 * time.Millisecond,
		MaxAttempts:  2,
		RetryBackoff: time.Millisecond,
		Now:          func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) },
	}, q)
}

func npmDef() SourceDef { return CodexSources()[0] }

// callsFor 统计 fake 收到的指定方式调用数——Check 遍历全部来源，npm/brew
// 都会各查各的，计数必须按方式分桶。
func (f *fakeQuerier) callsFor(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func mustResult(t *testing.T, r Report, method string) SourceResult {
	t.Helper()
	for _, s := range r.Sources {
		if s.Method == method {
			return s
		}
	}
	t.Fatalf("report has no %q source: %+v", method, r.Sources)
	return SourceResult{}
}

func TestCheckOKRecordsSourceChannelPlatformsTime(t *testing.T) {
	q := &fakeQuerier{resps: []fakeResp{{raw: "0.45.0"}}}
	s := newFakeService(t, q)

	rep, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusOK {
		t.Fatalf("status = %q (%+v), want ok", got.Status, got.Failure)
	}
	if got.Package != "@openai/codex" || got.Channel != "latest" || got.AgentID != "codex" {
		t.Errorf("record = %s/%s/%s, want codex/@openai/codex/latest", got.AgentID, got.Package, got.Channel)
	}
	if got.Version == nil || got.Version.Raw != "0.45.0" || got.Version.Scheme != SchemeSemver {
		t.Errorf("version = %+v, want raw 0.45.0 semver", got.Version)
	}
	if got.QueriedAt == nil || !got.QueriedAt.Equal(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("queriedAt = %v, want注入时钟值", got.QueriedAt)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", got.Attempts)
	}
	if len(got.Platforms) == 0 {
		t.Error("platforms applicability missing")
	}
	// 全部来源都在报告里（含不可用的 binary）——「存在但不可用」不消失。
	if len(rep.Sources) != 3 {
		t.Errorf("sources = %d, want 3 (npm/brew/binary)", len(rep.Sources))
	}
}

func TestCheckFailureFallsBackToLastSuccessCache(t *testing.T) {
	q := &fakeQuerier{resps: []fakeResp{{raw: "0.45.0"}, {err: exec.ErrNotFound}}}
	s := newFakeService(t, q)

	first, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if st := mustResult(t, first, "npm").Status; st != StatusOK {
		t.Fatalf("first check status = %q, want ok", st)
	}

	second, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, second, "npm")
	if got.Status != StatusFailed {
		t.Fatalf("second check status = %q, want failed", got.Status)
	}
	if got.Failure == nil || got.Failure.Reason != ReasonQueryFailed {
		t.Fatalf("failure = %+v, want query_failed", got.Failure)
	}
	if got.Version != nil || got.QueriedAt != nil {
		t.Errorf("failed result must not carry a version: %+v / %v", got.Version, got.QueriedAt)
	}
	if got.LastSuccess == nil {
		t.Fatal("lastSuccess cache fallback missing")
	}
	if got.LastSuccess.Version.Raw != "0.45.0" {
		t.Errorf("lastSuccess version = %q, want 0.45.0", got.LastSuccess.Version.Raw)
	}
	if !got.LastSuccess.QueriedAt.Equal(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("lastSuccess queriedAt = %v, want首次成功的时间（不是本次时间）", got.LastSuccess.QueriedAt)
	}

	// 从未成功过的来源失败：没有缓存可回退，必须保持为空（前端显示未知）。
	q2 := &fakeQuerier{resps: []fakeResp{{err: exec.ErrNotFound}}}
	s2 := newFakeService(t, q2)
	rep, err := s2.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if ls := mustResult(t, rep, "npm").LastSuccess; ls != nil {
		t.Errorf("never-successful source must have no lastSuccess, got %+v", ls)
	}
}

func TestCheckNonSemVerExpressibleAndComparable(t *testing.T) {
	q := &fakeQuerier{resps: []fakeResp{{raw: "1.2.3_1"}}}
	s := newFakeService(t, q)

	rep, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusOK {
		t.Fatalf("status = %q (%+v), want ok——非 SemVer 不等于失败", got.Status, got.Failure)
	}
	if got.Version == nil || got.Version.Scheme != SchemeSourceRule || got.Version.Raw != "1.2.3_1" {
		t.Fatalf("version = %+v, want source_rule 1.2.3_1", got.Version)
	}
	// 可按来源规则比较：与缓存中同 scheme 版本比较不报 incomparable。
	cmp, err := Compare(*got.Version, ParseVersion("1.2.2_9"))
	if err != nil || cmp <= 0 {
		t.Errorf("Compare(1.2.3_1, 1.2.2_9) = (%d, %v), want positive", cmp, err)
	}
}

func TestCheckParseFailureIsExplicit(t *testing.T) {
	q := &fakeQuerier{resps: []fakeResp{{raw: "cheese"}}}
	s := newFakeService(t, q)

	rep, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed（解析失败不是成功）", got.Status)
	}
	if got.Failure == nil || got.Failure.Reason != ReasonParseFailed {
		t.Fatalf("failure = %+v, want version_parse_failed", got.Failure)
	}
	if got.Version != nil {
		t.Errorf("parse failure must not return a version, got %+v", got.Version)
	}
	// 解析失败的结果不得进入成功缓存。
	q2 := &fakeQuerier{resps: []fakeResp{{raw: "cheese"}, {err: exec.ErrNotFound}}}
	s2 := newFakeService(t, q2)
	if _, err := s2.Check(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	rep2, err := s2.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if ls := mustResult(t, rep2, "npm").LastSuccess; ls != nil {
		t.Errorf("parse failure poisoned the success cache: %+v", ls)
	}
}

func TestCheckUnsupportedMethodNeverQueries(t *testing.T) {
	q := &fakeQuerier{}
	s := newFakeService(t, q)

	rep, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "binary")
	if got.Status != StatusUnsupported {
		t.Fatalf("binary status = %q, want unsupported", got.Status)
	}
	if got.Failure == nil || got.Failure.Reason != ReasonMethodUnsupported {
		t.Fatalf("failure = %+v, want method_unsupported", got.Failure)
	}
	if got.Attempts != 0 || got.Version != nil {
		t.Errorf("unsupported must not query: attempts=%d version=%+v", got.Attempts, got.Version)
	}
	if n := q.callsFor("binary"); n != 0 {
		t.Errorf("binary queried %d times, want 0（能力门拦截，不发起查询）", n)
	}
	if len(got.Platforms) == 0 {
		t.Error("unsupported source must still declare platform applicability")
	}
}

func TestCheckRetriesRetryableThenSucceeds(t *testing.T) {
	q := &fakeQuerier{resps: []fakeResp{
		{err: &exec.ExitError{}}, // 第 1 次：可重试失败
		{raw: "0.45.0"},          // 第 2 次：成功
	}}
	s := newFakeService(t, q)

	rep, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusOK || got.Attempts != 2 {
		t.Fatalf("status=%q attempts=%d, want ok/2", got.Status, got.Attempts)
	}
}

func TestCheckRetryBudgetExhaustedReportsFailure(t *testing.T) {
	q := &fakeQuerier{resps: []fakeResp{
		{err: context.DeadlineExceeded},
		{err: context.DeadlineExceeded},
	}}
	s := newFakeService(t, q)

	rep, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Failure == nil || got.Failure.Reason != ReasonTimeout || got.Failure.Attempts != 2 {
		t.Fatalf("failure = %+v, want timeout with 2 attempts", got.Failure)
	}
	if n := q.callsFor("npm"); n != 2 {
		t.Errorf("npm queried %d times, want exactly 2（有限重试）", n)
	}
}

func TestCheckSourceUnavailableNotRetried(t *testing.T) {
	q := &fakeQuerier{resps: []fakeResp{{err: errors.New("npm is not available")}}}
	s := newFakeService(t, q)

	rep, err := s.Check(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Failure == nil || got.Failure.Reason != ReasonSourceUnavailable {
		t.Fatalf("failure = %+v, want source_unavailable", got.Failure)
	}
	if n := q.callsFor("npm"); n != 1 {
		t.Errorf("npm queried %d times, want 1（不可用不重试）", n)
	}
}

func TestCheckParentDeadlineOverridesPerAttempt(t *testing.T) {
	// 父 context 已超时：不再发起查询，结果为 failed/timeout 且无缓存伪装。
	q := &fakeQuerier{resps: []fakeResp{{raw: "0.45.0"}}}
	s := newFakeService(t, q)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	rep, err := s.Check(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	got := mustResult(t, rep, "npm")
	if got.Status != StatusFailed || got.Failure == nil || got.Failure.Reason != ReasonTimeout {
		t.Fatalf("result = %+v, want failed/timeout", got)
	}
	if n := q.callsFor("npm"); n != 0 {
		t.Errorf("npm queried %d times with dead parent, want 0", n)
	}
}

func TestCheckUnknownAgent(t *testing.T) {
	s := newFakeService(t, &fakeQuerier{})
	if _, err := s.Check(context.Background(), "claude"); err == nil {
		t.Fatal("unknown agent must error")
	}
}

func TestCheckNonDefaultChannelUnsupported(t *testing.T) {
	// 渠道能力边界：直接构造非默认渠道声明验证门逻辑。
	def := npmDef()
	def.DefaultChannel = false
	def.Channel = "beta"
	s := newFakeService(t, &fakeQuerier{})

	got := s.checkSource(context.Background(), def)
	if got.Status != StatusUnsupported {
		t.Fatalf("status = %q, want unsupported", got.Status)
	}
	if got.Failure == nil || got.Failure.Reason != ReasonChannelUnsupported {
		t.Fatalf("failure = %+v, want channel_unsupported", got.Failure)
	}
}
