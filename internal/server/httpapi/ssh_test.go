package httpapi

// KM-26 核查回归：本片新增的 4 条路由必须有 handler 级用例
//（未装配 501 / 未注册 404 / 安装必须显式确认且不落盘 / 未知机器 404）。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/controller/sshops"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/sshtransport"
)

type fakeSSHAPI struct {
	probes []string
	err    error
}

func (f *fakeSSHAPI) ProbeAndRecord(_ context.Context, machine string) error {
	f.probes = append(f.probes, machine)
	return f.err
}

type fakeIncludeAPI struct {
	installs int
	path     string
}

func (f *fakeIncludeAPI) RenderInclude(context.Context) (*sshtransport.IncludeRender, error) {
	return &sshtransport.IncludeRender{
		Content:  "# generated\n\nHost devbox-01\n  HostName devbox.internal\n",
		Included: []string{"devbox-01"},
	}, nil
}

func (f *fakeIncludeAPI) InstallInclude(_ context.Context, confirm bool) (string, error) {
	if !confirm {
		return "", domain.Coded(domain.ReasonInvalid, "confirmation required")
	}
	f.installs++
	return f.path, nil
}

func TestSSHRoutesWithoutWiring(t *testing.T) {
	f := newAPIFixture(t)
	// 未装配 SSH 通道 → 501 + §6.4 错误体（不是 panic、不是 404 掩盖）。
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/machines/ws-1/ssh/probe"},
		{"GET", "/api/v1/ssh/include"},
	} {
		status, body := f.do(tc.method, tc.path, "")
		if status != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d, want 501 (%v)", tc.method, tc.path, status, body)
		}
		if body["reason"] != domain.ReasonNotImplemented {
			t.Fatalf("%s %s reason = %v, want %s", tc.method, tc.path, body["reason"], domain.ReasonNotImplemented)
		}
	}
}

// 未实现的动作端点必须走 §6.4 JSON 404（而不是 HTML 404，也不是静默成功）。
func TestUnimplementedSSHActionsAreJSON404(t *testing.T) {
	f := newAPIFixture(t)
	for _, path := range []string{
		"/api/v1/machines/ws-1/ssh/bootstrap",
		"/api/v1/machines/ws-1/ssh/repair-agentd",
	} {
		status, body := f.do("POST", path, "{}")
		if status != http.StatusNotFound {
			t.Fatalf("POST %s = %d, want 404 (%v)", path, status, body)
		}
		if body["reason"] != domain.ReasonNotFound {
			t.Fatalf("POST %s reason = %v, want %s", path, body["reason"], domain.ReasonNotFound)
		}
	}
}

func TestSSHProbeUnknownMachineIs404AndWiredProbeSucceeds(t *testing.T) {
	f := newAPIFixture(t)
	fake := &fakeSSHAPI{}
	f.api.SetSSH(fake)

	status, body := f.do("POST", "/api/v1/machines/ghost/ssh/probe", "")
	if status != http.StatusNotFound {
		t.Fatalf("probe unknown machine = %d, want 404 (%v)", status, body)
	}
	if len(fake.probes) != 0 {
		t.Fatalf("probe must not run for an unknown machine: %v", fake.probes)
	}

	f.seed()
	status, body = f.do("POST", "/api/v1/machines/ws-1/ssh/probe", "")
	if status != http.StatusOK {
		t.Fatalf("probe = %d, want 200 (%v)", status, body)
	}
	if len(fake.probes) != 1 || fake.probes[0] != "ws-1" {
		t.Fatalf("probe calls = %v", fake.probes)
	}

	// 探测失败必须把 §30.1 的 reason 透出（host-key 失败 → 502，不静默降级）。
	fake.err = domain.Coded(domain.ReasonHostKeyVerificationFailed, "Host key verification failed.")
	status, body = f.do("POST", "/api/v1/machines/ws-1/ssh/probe", "")
	if status != http.StatusBadGateway {
		t.Fatalf("failed probe = %d, want 502 (%v)", status, body)
	}
	if body["reason"] != domain.ReasonHostKeyVerificationFailed {
		t.Fatalf("reason = %v, want %s", body["reason"], domain.ReasonHostKeyVerificationFailed)
	}
}

// 安装 include 必须显式确认：缺 confirm 时 400，且**不落盘**。
func TestIncludeInstallRequiresExplicitConfirmation(t *testing.T) {
	f := newAPIFixture(t)
	fake := &fakeIncludeAPI{path: filepath.Join(t.TempDir(), "agent-fleet.conf")}
	f.api.SetInclude(fake)

	for _, body := range []string{`{}`, `{"action":"install"}`, `{"action":"install","confirm":false}`} {
		status, resp := f.do("POST", "/api/v1/ssh/include", body)
		if status != http.StatusBadRequest {
			t.Fatalf("install %s = %d, want 400 (%v)", body, status, resp)
		}
	}
	if fake.installs != 0 {
		t.Fatalf("no install may happen without confirmation, got %d", fake.installs)
	}
	if _, err := os.Stat(fake.path); !os.IsNotExist(err) {
		t.Fatalf("nothing may be written without confirmation (err=%v)", err)
	}

	status, resp := f.do("POST", "/api/v1/ssh/include", `{"action":"install","confirm":true}`)
	if status != http.StatusOK {
		t.Fatalf("confirmed install = %d, want 200 (%v)", status, resp)
	}
	if fake.installs != 1 {
		t.Fatalf("install calls = %d, want 1", fake.installs)
	}
	if resp["includeDirective"] != sshtransport.IncludeDirective {
		t.Fatalf("response must echo the Include directive: %v", resp)
	}

	// preview：text/plain，且带 Included 计数（FR-12.6 的第一个动作）。
	req, err := http.NewRequest("GET", f.srv.URL+"/api/v1/ssh/include", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("preview = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("preview content-type = %q", ct)
	}
	if got := res.Header.Get("X-Agent-Fleet-Included-Hosts"); got != "1" {
		t.Fatalf("included hosts header = %q, want 1", got)
	}
}

// KM-29 回归：include 渲染拒绝（FR-12.6）此前落进 writeSSHError 的默认分支
// → 502 Internal，与 §6.4 的 400 Invalid 相反。
//
// 装配的是**真实**链路（真实机器存储 → real orchestrator → sshtransport.RenderInclude
// → handler），因为这条路径的 bug 恰恰在"渲染器返回的哨兵错误 vs HTTP 层只认
// reason code"之间；手搓一个错误只会重复实现者的假设。
func TestIncludeRenderRejectionIs400Invalid(t *testing.T) {
	f := newAPIFixture(t)
	// install 的成功路径会写文件：指向临时目录，测试不触碰真实 home。
	dir := t.TempDir()
	f.api.SetInclude(sshops.New(sshops.Config{
		Machines: f.machines, OperatorHome: dir, GeneratedDir: dir,
	}))

	// 对照：一份合法的 ssh.user 能正常渲染（证明后面的 400 来自渲染拒绝本身）。
	code, obj := f.do(http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"devbox-01"},"spec":{"managementMode":"ssh","ssh":{"hostName":"devbox.internal","user":"op"}}}`)
	if code != http.StatusCreated {
		t.Fatalf("machine create = %d (%v)", code, obj)
	}
	// 成功路径是 text/plain，不走 f.do（它按 JSON 解码）：直接看状态码。
	if res := f.get("/api/v1/ssh/include"); res != http.StatusOK {
		t.Fatalf("preview with a renderable machine = %d, want 200", res)
	}
	// 复现夹具：一台 ssh.user = "o p" 的机器（空白会渲染出无法解析的 include 行）。
	code, obj = f.do(http.MethodPost, "/api/v1/machines",
		`{"metadata":{"name":"devbox-02"},"spec":{"managementMode":"ssh","ssh":{"hostName":"devbox.internal","user":"o p"}}}`)
	if code != http.StatusCreated {
		t.Fatalf("machine create = %d (%v)", code, obj)
	}

	const wantMessage = `resource invalid: ssh.user "o p" contains whitespace; refusing to render an unparseable OpenSSH include line (FR-12.6)`
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/ssh/include", ""},
		{http.MethodPost, "/api/v1/ssh/include", `{"action":"install","confirm":true}`},
	} {
		status, body := f.do(tc.method, tc.path, tc.body)
		if status != http.StatusBadRequest {
			t.Fatalf("%s %s = %d, want 400 (%v)", tc.method, tc.path, status, body)
		}
		if body["reason"] != domain.ReasonInvalid {
			t.Fatalf("%s %s reason = %v, want %s", tc.method, tc.path, body["reason"], domain.ReasonInvalid)
		}
		if body["message"] != wantMessage {
			t.Fatalf("%s %s message = %q, want %q", tc.method, tc.path, body["message"], wantMessage)
		}
	}
	// 渲染拒绝发生在任何写入之前：include 文件与生成产物都不得落盘。
	for _, p := range []string{
		filepath.Join(dir, ".ssh", "agent-fleet.conf"),
		filepath.Join(dir, "ssh", "agent-fleet.conf"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("render rejection must not write %s (err=%v)", p, err)
		}
	}
}

// 未知机器的 inventory 也要 404（route 存在但机器不存在）。
func TestSSHInventoryUnknownMachine(t *testing.T) {
	f := newAPIFixture(t)
	status, body := f.do("POST", "/api/v1/machines/ghost/ssh/inventory", "")
	if status != http.StatusNotFound {
		t.Fatalf("inventory unknown machine = %d, want 404 (%v)", status, body)
	}
}

// TestDriftEndpointReportsPostEvaluationStatus：drift 端点求值会写条件，响应体必须用
// **求值后**的 status——否则会出现"响应说 Unknown(StaleObservation)、库里是 False"
// （KM-26 核查必改 2）。
func TestDriftEndpointReportsPostEvaluationStatus(t *testing.T) {
	f := newAPIFixture(t)
	f.seed()
	ctx := context.Background()

	// 先物化一代期望（否则 drift 未定义，求值不会写条件）。
	if _, _, err := f.rec.EnsureSnapshot(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	// 一份"观测与期望不一致"的观测 → 先给出确定判决 Drifted=True。
	payload, err := json.Marshal(domain.ObservedState{
		InventorySeq: 4, Full: true, CanonicalizationVersion: "v1",
		DesiredProjectionDigest: "sha256:wanted", ObservedProjectionDigest: "sha256:actual",
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &domain.ObservedStateRecord{Machine: "ws-1", InventorySeq: 4, Payload: payload}
	f.rec.AssignObservationGeneration(ctx, rec)
	if _, err := f.observed.Store(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.EvaluateDrift(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	// 把 lastInventoryAt 拨到窗口之外（模拟时间流逝）。
	old := time.Now().UTC().Add(-2 * time.Hour)
	if err := f.mstatus.UpdateStatus(ctx, "ws-1", func(st *domain.MachineStatus) error {
		st.LastInventoryAt = &old
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	code, body := f.do(http.MethodGet, "/api/v1/machines/ws-1/drift", "")
	if code != http.StatusOK {
		t.Fatalf("drift = %d (%v)", code, body)
	}
	drifted, _ := body["drifted"].(map[string]any)
	if drifted == nil || drifted["status"] != domain.ConditionUnknown || drifted["reason"] != domain.DriftReasonStaleObservation {
		t.Fatalf("response drifted = %v, want Unknown(%s)", body["drifted"], domain.DriftReasonStaleObservation)
	}
	// 库里也必须是一致的 Unknown（这正是核查发现的"响应与库不一致"）。
	m, err := f.api.machines.Get(ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		t.Fatal(err)
	}
	c, ok := st.GetCondition(domain.ConditionDrifted)
	if !ok || c.Status != domain.ConditionUnknown || c.Reason != domain.DriftReasonStaleObservation {
		t.Fatalf("stored condition = %+v, want Unknown(%s)", c, domain.DriftReasonStaleObservation)
	}
}
