package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	agentlc "github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle"
	"github.com/shelwinnn/agent-fleet/internal/agentlocal/lifecycle/testenv"
	lifecyclectl "github.com/shelwinnn/agent-fleet/internal/controller/lifecycle"
	"github.com/shelwinnn/agent-fleet/internal/controller/machine"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/enrollment"
	grpcagent "github.com/shelwinnn/agent-fleet/internal/server/grpcagent"
	"github.com/shelwinnn/agent-fleet/internal/server/httpapi"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
	"github.com/shelwinnn/agent-fleet/internal/upstream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// 本文件是链路测试的装配层：与 cmd/agent-fleet-server 同形的控制面 + 与
// cmd/agent-fleet-agentd 同构的节点泵 + KM-46 隔离环境。断言一律走 REST
// （页面渲染的数据来源）与目标环境文件系统（实际结果）。

const (
	codexPackage = "@openai/codex"
	altPackage   = "@openai/codex-alt" // 部分失败场景的第二实例包名
	waitDeadline = 10 * time.Second
)

// quietLog 让链路测试输出不淹没 go test 结果。
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// e2eEnv 创建 KM-46 隔离环境并把 HOME/NPM_PREFIX/FAKE_NPM_REGISTRY/PATH 指进
// 去（与 agentlocal/lifecycle 的 newEnv 同一纪律：fake npm 子命令与引擎探测
// 都落在替身里，宿主不被触碰）。非 linux 跳过：替身是 POSIX sh。
func e2eEnv(t *testing.T) *testenv.Env {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("e2e chain runs on linux only; fake npm is a POSIX sh double (see KM-46 report)")
	}
	e, err := testenv.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, prefix, registry, path := e.Environ()
	t.Setenv("HOME", home)
	t.Setenv("NPM_PREFIX", prefix)
	t.Setenv("FAKE_NPM_REGISTRY", registry)
	t.Setenv("PATH", path)
	return e
}

// harness 是与 cmd/agent-fleet-server 同形的控制面栈（生命周期路径全量真实）。
type harness struct {
	t        *testing.T
	env      *testenv.Env
	rest     *httptest.Server
	lc       *lifecyclectl.Controller
	machines domain.MachineRepository
	observed domain.ObservedStateRepository
	tokens   *enrollment.TokenService
	ca       *enrollment.CA
	caPath   string
	grpcAddr string
}

// harnessOptions 允许单测调整控制面参数。
type harnessOptions struct {
	// clock 注入控制器时钟（超时扫描用可推进的假时钟保持确定性；派发给
	// 节点的 deadline 由 proto 的秒级 unix 承载，delta 从 0 起步，节点执行
	// 窗口始终是真实的默认值，不受假时钟挤压）。
	clock func() time.Time
}

func newHarness(t *testing.T, env *testenv.Env, opts harnessOptions) *harness {
	t.Helper()
	dir := t.TempDir()
	log := quietLog()
	ctx := context.Background()

	ca, err := enrollment.EnsureCA(filepath.Join(dir, "pki"))
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	db, err := sqlite.Open(filepath.Join(dir, "fleet.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	machines := sqlite.NewMachineStore(db)
	status := sqlite.NewMachineStatusStore(db)
	certs := sqlite.NewAgentCertificateStore(db)
	observed := sqlite.NewObservedStateStore(db)
	lcRepo := sqlite.NewLifecycleStore(db)

	ctrl := machine.NewController(machines, status, observed, certs, log)
	// 与 main 装配同形：离线扫描在位，但阈值放宽到小时级——测试时长内不会
	// 把在线节点误判离线（离线场景由用例显式构造：不连节点即离线）。
	go machine.RunOfflineScanner(ctx, ctrl, time.Hour, time.Hour, log)

	tokens := enrollment.NewTokenService(sqlite.NewEnrollmentStore(db))
	agentSrv := grpcagent.New(grpcagent.Config{
		HeartbeatInterval:  time.Second,
		InventoryInterval:  time.Hour,
		OfflineAfter:       time.Hour,
		OfflineScanEvery:   time.Hour,
		ClientCertValidity: time.Hour,
	}, ca, tokens, ctrl, machines, certs, observed, log)

	// 上游发现服务（T3 真实现）：CommandQuerier 经 PATH 命中 fake npm，
	// 查询落在受控注册表上——"受控版本源"。
	discovery := upstream.New(upstream.Config{}, upstream.NewCommandQuerier())

	lcCfg := lifecyclectl.Config{UpstreamQuery: discovery}
	if opts.clock != nil {
		lcCfg.Now = opts.clock
	}
	lc := lifecyclectl.New(lcRepo, machines, observed, lcCfg, log)
	lc.SetDispatcher(agentSrv)
	agentSrv.SetLifecycleSink(lc)

	g := agentSrv.GRPCServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen grpc: %v", err)
	}
	go func() { _ = g.Serve(ln) }()
	t.Cleanup(g.Stop)

	api := httpapi.New(httpapi.Config{
		UpstreamDiscovery: discovery,
		EnrollTokens:      &tokenIssuer{tokens: tokens, ttl: time.Minute},
	}, machines, nil, nil, nil, nil, sqlite.NewOperationStore(db), nil, nil,
		"e2e/v1", db.PingContext, log)
	api.SetLifecycle(lc)
	rest := httptest.NewServer(api.Handler())
	t.Cleanup(rest.Close)

	return &harness{
		t: t, env: env, rest: rest, lc: lc,
		machines: machines, observed: observed,
		tokens: tokens, ca: ca, caPath: filepath.Join(dir, "pki", "ca.crt"),
		grpcAddr: ln.Addr().String(),
	}
}

// tokenIssuer 与 cmd/agent-fleet-server 的同名适配器同形：REST 层的 TTL 由
// 装配统一决定。
type tokenIssuer struct {
	tokens *enrollment.TokenService
	ttl    time.Duration
}

func (x *tokenIssuer) Issue(ctx context.Context, machine, csrFingerprint string, _ time.Duration) (string, time.Time, error) {
	return x.tokens.Issue(ctx, machine, csrFingerprint, x.ttl)
}

// ---- REST 助手（断言面 = 页面渲染的数据来源）----

// restDo 发起页面会发起的同款 REST 调用并解 JSON。
func (h *harness) restDo(method, path, body string) (int, map[string]any) {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.rest.URL+path, reader)
	if err != nil {
		h.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	resp, err := h.rest.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, out
}

// seedMachine 走 REST 建 Machine（页面的第一步：接入主机）。
func (h *harness) seedMachine(name string) {
	h.t.Helper()
	code, body := h.restDo(http.MethodPost, "/api/v1/machines",
		fmt.Sprintf(`{"metadata":{"name":%q},"spec":{"managementMode":"agentd"}}`, name))
	if code != http.StatusCreated {
		h.t.Fatalf("create machine %s = %d: %v", name, code, body)
	}
}

// createOperation / confirmOperation / getOperation / retryOperation 覆盖页面
// 会发起的动作端点。保真度说明：confirm 是预览确认对话框的后端契约端点
// （KM-49；未确认不得派发），已交付的 T5 页面尚未接线该调用——集成缝隙，
// 见包文档与 KM-51 回报。
func (h *harness) createOperation(opType, key, version string, targets []domain.LifecycleTarget) (int, map[string]any) {
	h.t.Helper()
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		h.t.Fatal(err)
	}
	body := fmt.Sprintf(`{"type":%q,"idempotencyKey":%q,`, opType, key)
	if version != "" {
		body += fmt.Sprintf(`"version":%q,`, version)
	}
	body += fmt.Sprintf(`"targets":%s,"createdBy":"e2e"}`, targetJSON)
	return h.restDo(http.MethodPost, "/api/v1/lifecycle/operations", body)
}

func (h *harness) confirmOperation(opID string) (int, map[string]any) {
	h.t.Helper()
	return h.restDo(http.MethodPost, "/api/v1/lifecycle/operations/"+opID+"/confirm", "")
}

func (h *harness) getOperation(opID string) (int, map[string]any) {
	h.t.Helper()
	return h.restDo(http.MethodGet, "/api/v1/lifecycle/operations/"+opID, "")
}

func (h *harness) retryOperation(opID string, targets []domain.LifecycleTarget) (int, map[string]any) {
	h.t.Helper()
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.restDo(http.MethodPost, "/api/v1/lifecycle/operations/"+opID+"/retry",
		fmt.Sprintf(`{"targets":%s,"createdBy":"e2e"}`, targetJSON))
}

// opID 提取创建响应里的操作 id（metadata.name）。
func opIDOf(t *testing.T, body map[string]any) string {
	t.Helper()
	meta, _ := body["metadata"].(map[string]any)
	id, _ := meta["name"].(string)
	if id == "" {
		t.Fatalf("operation id missing in response: %v", body)
	}
	return id
}

// waitOperation 轮询 GET 操作直到全部目标离开 pending/running（页面轮询的
// 服务端等价物），返回终态响应。
func (h *harness) waitOperation(opID string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(waitDeadline)
	for {
		code, body := h.getOperation(opID)
		if code != http.StatusOK {
			h.t.Fatalf("get operation %s = %d: %v", opID, code, body)
		}
		if status, _ := body["status"].(map[string]any); status != nil {
			if phase, _ := status["phase"].(string); phase == domain.OperationPhaseCompleted {
				return body
			}
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("operation %s not completed within %s: %v", opID, waitDeadline, body)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// firstResult 取指定目标的结果对象（页面逐目标渲染的数据行）。
func firstResult(t *testing.T, body map[string]any, pkg string) map[string]any {
	t.Helper()
	status, _ := body["status"].(map[string]any)
	results, _ := status["results"].([]any)
	for _, r := range results {
		res, _ := r.(map[string]any)
		tgt, _ := res["target"].(map[string]any)
		if tgt["package"] == pkg {
			return res
		}
	}
	t.Fatalf("target %s missing in operation results: %v", pkg, body)
	return nil
}

// targetState 取指定目标的 state/reason（页面逐目标渲染的字段）。
func targetState(t *testing.T, body map[string]any, pkg string) (state, reason string) {
	t.Helper()
	res := firstResult(t, body, pkg)
	state, _ = res["state"].(string)
	reason, _ = res["reason"].(string)
	return state, reason
}

// evidenceVersion 取指定目标证据里的实测版本（页面"实际版本"列的数据来源）。
func evidenceVersion(t *testing.T, body map[string]any, pkg string) string {
	t.Helper()
	ev, _ := firstResult(t, body, pkg)["evidence"].(map[string]any)
	v, _ := ev["observedVersion"].(string)
	return v
}

// codexTarget 构造页面上会选择的标准目标（native 环境的 codex npm 实例）。
func codexTarget(machine, pkg string) domain.LifecycleTarget {
	return domain.LifecycleTarget{
		Machine: machine, Env: "native", User: "alice",
		Agent: "codex", Method: "npm", Package: pkg,
	}
}

// ---- 目标环境断言助手（实际结果，不是退出码）----

// installedVersion 读受控前缀里某包安装后的版本（node_modules 的 package.json）。
// 未安装返回空串。
func installedVersion(t *testing.T, prefix, pkg string) string {
	t.Helper()
	p := filepath.Join(prefix, "lib", "node_modules", pkg, "package.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	var pkgJSON struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkgJSON); err != nil {
		t.Fatalf("read installed package.json %s: %v", p, err)
	}
	return pkgJSON.Version
}

func shimPath(prefix, bin string) string { return filepath.Join(prefix, "bin", bin) }

// snapshotOf 是 SnapshotDir 的测试包装（路径进失败信息）。
func snapshotOf(t *testing.T, dir string) agentlc.Snapshot {
	t.Helper()
	s, err := agentlc.SnapshotDir(dir)
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return s
}

func assertNoDiff(t *testing.T, what string, before, after agentlc.Snapshot) {
	t.Helper()
	if diffs := before.Diff(after); len(diffs) != 0 {
		t.Fatalf("%s changed across the full chain:\n%s", what, before.Report(after))
	}
}

// ---- 节点侧：真实 enroll + mTLS Connect + 真实执行引擎的最小泵 ----

// nodeCert 模拟 agentd 侧的本地身份（私钥 + CSR + 指纹）。
type nodeCert struct {
	priv        *ecdsa.PrivateKey
	csrPEM      []byte
	fingerprint string
}

func newNodeCert(t *testing.T, machine string) *nodeCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: machine},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tpl, key)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := enrollment.PublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return &nodeCert{priv: key, csrPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), fingerprint: fp}
}

// enrollNode 走完整真实链路：REST 签发一次性 token（页面动作）→ gRPC Enroll →
// mTLS 客户端就绪。
func (h *harness) enrollNode(machineName string, nc *nodeCert) fleetv1.FleetAgentServiceClient {
	h.t.Helper()
	code, body := h.restDo(http.MethodPost, "/api/v1/machines/"+machineName+"/enroll-token",
		fmt.Sprintf(`{"csrPubKeySha256":%q,"ttlSeconds":60}`, nc.fingerprint))
	if code != http.StatusCreated {
		h.t.Fatalf("issue enroll token = %d: %v", code, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		h.t.Fatalf("enroll token missing: %v", body)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(h.ca.CACertPEM()) {
		h.t.Fatal("bad ca pem")
	}
	enrollConn, err := grpc.NewClient(h.grpcAddr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, RootCAs: pool,
	})))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { enrollConn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := fleetv1.NewFleetEnrollmentServiceClient(enrollConn).Enroll(ctx, &fleetv1.EnrollRequest{
		MachineId: machineName, Token: token, CsrPem: nc.csrPEM,
	})
	if err != nil {
		h.t.Fatalf("enroll: %v", err)
	}

	leafDER, _ := pem.Decode(resp.GetCertPem())
	if leafDER == nil {
		h.t.Fatal("bad cert pem from enroll")
	}
	conn, err := grpc.NewClient(h.grpcAddr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      pool,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER.Bytes}, PrivateKey: nc.priv}},
	})))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { conn.Close() })
	return fleetv1.NewFleetAgentServiceClient(conn)
}

// pumpNode 是节点的最小泵（与 cmd/agent-fleet-agentd 的 lifecycleExecutor 同构：
// Recv → Execute → Send → Ack），挂真实 NodeExecutor + 真实 NpmEngine。
// 故障注入面：dropResults（结果丢失）、hookedEngine（包级失败/执行中漂移）、
// 不连接（提交前离线）。
type pumpNode struct {
	t        *testing.T
	machine  string
	engine   *hookedEngine
	executor *agentlc.NodeExecutor
	stream   grpc.BidiStreamingClient[fleetv1.AgentToServer, fleetv1.ServerToAgent]

	dropResults atomic.Bool
	dispatches  atomic.Int32
	acks        atomic.Int32
	done        chan struct{}
	startOnce   sync.Once
}

// connectNode 让节点上线：Connect 流 + Hello/Welcome + 心跳 + 全量观测（平台
// 事实与控制面台账一致），随后泵在后台运行。等待 AgentConnected 条件落库后返回。
func (h *harness) connectNode(machineName string) *pumpNode {
	h.t.Helper()
	nc := newNodeCert(h.t, machineName)
	client := h.enrollNode(machineName, nc)

	stream, err := client.Connect(context.Background())
	if err != nil {
		h.t.Fatalf("connect: %v", err)
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_Hello{
		Hello: &fleetv1.Hello{MachineId: machineName, AgentdVersion: "e2e-0.0.0", ProtocolVersion: grpcagent.ProtocolVersionV1},
	}}); err != nil {
		h.t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil {
		h.t.Fatalf("welcome: %v", err)
	}
	if first.GetWelcome() == nil {
		h.t.Fatalf("first server message is not Welcome: %T", first.GetPayload())
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_Heartbeat{
		Heartbeat: &fleetv1.Heartbeat{Sequence: 1},
	}}); err != nil {
		h.t.Fatal(err)
	}
	// 全量观测：平台事实 = 本进程运行时（节点闸要求台账与运行时一致）。
	obs := &fleetv1.ObservedState{
		InventorySeq: 1, Full: true,
		Machine: &fleetv1.MachineInfo{
			Os: runtime.GOOS, Arch: runtime.GOARCH,
			Hostname: machineName + "-host", HomeDir: h.env.Home, Kernel: "e2e",
		},
		Agents: []*fleetv1.AgentInstance{{Family: "agentd", Version: "e2e-0.0.0", Enabled: true}},
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{ObservedState: obs}}); err != nil {
		h.t.Fatal(err)
	}

	// 隔离验证配置：allowPolicy 替身表示"该组合已在隔离环境验证"（生产装配
	// 固定 RegistryPolicy，T8 前 supported 为空集——见包文档）。
	engine := &hookedEngine{inner: agentlc.NewNpmEngine(nil), calls: map[string]int{}}
	executor := agentlc.NewNodeExecutor(agentlc.NodeExecutorConfig{
		Engines: map[string]agentlc.Engine{"npm": engine},
		Policy:  allowPolicy{},
		Timeout: 2 * time.Minute,
		Log:     silentLogger{},
	})
	p := &pumpNode{t: h.t, machine: machineName, engine: engine, executor: executor, stream: stream, done: make(chan struct{})}
	p.startOnce.Do(func() { go p.run() })

	// 等待 AgentConnected 与全量观测都落库：前者是 dispatchPending 的判据，
	// 后者是 CreateOperation 回填目标 OS/Arch 的数据源——两者都就绪后创建
	// 操作才是确定性的（否则前后两次创建的请求摘要会因回填差异而漂移）。
	deadline := time.Now().Add(waitDeadline)
	for {
		connected, observed := false, false
		m, err := h.machines.Get(context.Background(), machineName)
		if err == nil {
			if st, err := domain.ParseMachineStatus(m.StatusJSON()); err == nil {
				if cond, ok := st.GetCondition(domain.ConditionAgentConnected); ok && cond.Status == domain.ConditionTrue {
					connected = true
				}
			}
		}
		if obs, err := h.observed.Latest(context.Background(), machineName); err == nil && obs != nil {
			observed = true
		}
		if connected && observed {
			return p
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("node readiness not reached in time (connected=%v observed=%v)", connected, observed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// run 是泵主循环（gRPC 单写者：Recv/Send 全在本 goroutine）。
func (p *pumpNode) run() {
	defer close(p.done)
	for {
		msg, err := p.stream.Recv()
		if err != nil {
			return
		}
		switch payload := msg.GetPayload().(type) {
		case *fleetv1.ServerToAgent_ExecuteLifecycleOperation:
			ex := payload.ExecuteLifecycleOperation
			p.dispatches.Add(1)
			rep := p.executeOp(ex)
			if p.dropResults.Load() {
				continue // 故障注入：执行了但结果回不来（结果丢失）
			}
			if err := p.stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_LifecycleOperationResult{
				LifecycleOperationResult: reportToProto(rep, ex.GetSpec().GetIdempotencyKey()),
			}}); err != nil {
				return
			}
		case *fleetv1.ServerToAgent_OperationResultAck:
			p.acks.Add(1) // §8.2：Ack 是 outbox 淘汰依据；泵内仅计数
		default:
			// Welcome 之外的下行消息本场景不涉及；静默忽略。
		}
	}
}

// executeOp 把协议载荷交给真实 NodeExecutor（目标转换与
// cmd/agent-fleet-agentd/lifecycle.go 的 lifecycleTargetsOf 同构）。
func (p *pumpNode) executeOp(ex *fleetv1.ExecuteLifecycleOperation) *agentlc.Report {
	spec := ex.GetSpec()
	targets := make([]agentlc.TargetSpec, 0, len(spec.GetTargets()))
	for _, tgt := range spec.GetTargets() {
		executables := []string(nil)
		if tgt.GetAgent() == "codex" {
			executables = []string{"codex"}
		}
		targets = append(targets, agentlc.TargetSpec{
			Machine: tgt.GetMachineId(), Type: spec.GetType(),
			User: tgt.GetUser(), Agent: tgt.GetAgent(),
			Method: tgt.GetMethod(), Package: tgt.GetPackage(),
			Version: spec.GetVersion(), Env: tgt.GetEnv(),
			OS: tgt.GetOs(), Arch: tgt.GetArch(),
			Executables: executables,
		})
	}
	var deadline time.Time
	if ts := ex.GetDeadlineUnix(); ts > 0 {
		deadline = time.Unix(ts, 0)
	}
	rep := p.executor.Execute(context.Background(), agentlc.OpRequest{
		OpID:                spec.GetOpId(),
		Type:                spec.GetType(),
		Version:             spec.GetVersion(),
		RediscoveryRequired: ex.GetRediscoveryRequired(),
		Deadline:            deadline,
		Targets:             targets,
	})
	return &rep
}

// reportToProto 与 cmd/agent-fleet-agentd/lifecycle.go 的 lifecycleReportToProto
// 同构（同一协议契约的两端；KM-51 修复后 daemon 侧同样回带幂等键与逐目标
// 身份——本泵的字段集与 daemon 侧逐字段对齐）。
func reportToProto(rep *agentlc.Report, idempotencyKey string) *fleetv1.LifecycleOperationResult {
	out := &fleetv1.LifecycleOperationResult{
		OpId:                rep.OpID,
		IdempotencyKey:      idempotencyKey,
		NeedsReconfirmation: rep.NeedsReconfirmation,
		ReconfirmReason:     rep.ReconfirmReason,
		FinishedAtUnix:      rep.FinishedAt.Unix(),
	}
	for _, r := range rep.Results {
		item := &fleetv1.LifecycleTargetResult{
			State:   r.State,
			Reason:  r.Reason,
			Message: r.Message,
		}
		// 目标身份逐段回带（键由控制面域模型重建，两端同构防漂移）。
		segments := strings.Split(r.TargetKey, "\x1f")
		if len(segments) == 6 {
			item.Target = &fleetv1.LifecycleTarget{
				MachineId: segments[0], Env: segments[1], User: segments[2],
				Agent: segments[3], Method: segments[4], Package: segments[5],
			}
		}
		item.Evidence = &fleetv1.LifecycleEvidence{
			ObservedVersion: r.Evidence.ObservedVersion,
			ExecutablePath:  r.Evidence.ExecutablePath,
			Method:          r.Evidence.Method,
			InventorySeq:    r.Evidence.InventorySeq,
			Message:         r.Evidence.Message,
			RecordedAtUnix:  r.Evidence.RecordedAt.Unix(),
		}
		out.Results = append(out.Results, item)
	}
	return out
}

// hookedEngine 是真实 NpmEngine 的测试装饰器（故障注入面）：
//   - failInstall：指定包的安装/升级在引擎入口失败（部分目标失败）；
//   - onInstall：安装/升级进入引擎前的钩子（执行中上游漂移注入）；
//   - calls：逐包安装/升级调用计数（断言"成功目标不因重试而再执行"）。
type hookedEngine struct {
	inner       agentlc.Engine
	mu          sync.Mutex
	calls       map[string]int
	failInstall map[string]string
	onInstall   func(pkg string)
}

func (e *hookedEngine) countAndGate(pkg string) error {
	e.mu.Lock()
	e.calls[pkg]++
	fail, injected := e.failInstall[pkg]
	hook := e.onInstall
	e.mu.Unlock()
	if injected {
		return errors.New(fail)
	}
	if hook != nil {
		hook(pkg)
	}
	return nil
}

func (e *hookedEngine) installCalls(pkg string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[pkg]
}

// setFailInstall 注入"指定包的安装/升级在引擎入口失败"（部分目标失败）。
func (e *hookedEngine) setFailInstall(fail map[string]string) {
	e.mu.Lock()
	e.failInstall = fail
	e.mu.Unlock()
}

// setOnInstall 注入"安装/升级进入引擎前"的钩子（执行中上游漂移）。
func (e *hookedEngine) setOnInstall(hook func(pkg string)) {
	e.mu.Lock()
	e.onInstall = hook
	e.mu.Unlock()
}

func (e *hookedEngine) Detect(ctx context.Context, req agentlc.Request) (agentlc.Detected, error) {
	return e.inner.Detect(ctx, req)
}

func (e *hookedEngine) InstallExact(ctx context.Context, req agentlc.Request) (agentlc.Outcome, error) {
	if err := e.countAndGate(req.Package); err != nil {
		return agentlc.Outcome{}, err
	}
	return e.inner.InstallExact(ctx, req)
}

func (e *hookedEngine) UpgradeToExact(ctx context.Context, req agentlc.Request) (agentlc.Outcome, error) {
	if err := e.countAndGate(req.Package); err != nil {
		return agentlc.Outcome{}, err
	}
	return e.inner.UpgradeToExact(ctx, req)
}

func (e *hookedEngine) Uninstall(ctx context.Context, req agentlc.UninstallRequest) (agentlc.Outcome, error) {
	return e.inner.Uninstall(ctx, req)
}

// allowPolicy 是隔离验证配置的准入替身（与 agentlocal/lifecycle 测试同名同义）：
// 放行一切组合。生产装配没有旁路——T8 登记前真实主机一律拒绝。
type allowPolicy struct{}

func (allowPolicy) Check(string, string, string) error { return nil }

// silentLogger 是执行引擎的最小日志接口的静默替身（节点侧命名沿用
// lifecycle.Logger 语义，避免与 daemon 测试里的 quietLogger 函数同形异义）。
type silentLogger struct{}

func (silentLogger) Info(string, ...any) {}
func (silentLogger) Warn(string, ...any) {}
