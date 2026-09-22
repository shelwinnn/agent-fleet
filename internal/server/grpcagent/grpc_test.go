package grpcagent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"path/filepath"
	"testing"
	"time"

	fleetv1 "github.com/shelwinnn/agent-fleet/api/proto/fleet/v1"
	"github.com/shelwinnn/agent-fleet/internal/controller/machine"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/enrollment"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// harness 是一套真实 TLS 监听下的完整控制面栈。
type harness struct {
	t          *testing.T
	addr       string // gRPC 监听地址（127.0.0.1:port）
	ca         *enrollment.CA
	caPath     string
	tokens     *enrollment.TokenService
	tokenStore domain.EnrollmentTokenRepository
	machines   domain.MachineRepository
	certs      domain.AgentCertificateRepository
	observed   domain.ObservedStateRepository
	status     domain.MachineStatusRepository
	ctrl       *machine.Controller
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	dir := t.TempDir()
	ca, err := enrollment.EnsureCA(filepath.Join(dir, "pki"))
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	caPath := filepath.Join(dir, "pki", "ca.crt")
	db, err := sqlite.Open(filepath.Join(dir, "fleet.db"))
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), quietLog()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	machines := sqlite.NewMachineStore(db)
	status := sqlite.NewMachineStatusStore(db)
	certs := sqlite.NewAgentCertificateStore(db)
	observed := sqlite.NewObservedStateStore(db)
	ctrl := machine.NewController(machines, status, observed, certs, quietLog())
	go machine.RunOfflineScanner(context.Background(), ctrl, cfg.scanEvery(), cfg.offlineAfter(), quietLog())

	tokens := enrollment.NewTokenService(sqlite.NewEnrollmentStore(db))
	srv := New(cfg, ca, tokens, ctrl, machines, certs, observed, quietLog())

	g := srv.GRPCServer()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = g.Serve(ln) }()
	t.Cleanup(g.Stop)

	return &harness{
		t: t, addr: ln.Addr().String(), ca: ca, caPath: caPath, tokens: tokens,
		tokenStore: sqlite.NewEnrollmentStore(db),
		machines:   machines, certs: certs, observed: observed, status: status, ctrl: ctrl,
	}
}

// agentCert 模拟 agentd 侧：本地私钥 + CSR + 指纹。
type agentCert struct {
	csrPEM      []byte
	fingerprint string
	priv        *ecdsa.PrivateKey
}

func newAgentCert(t *testing.T, cn string) *agentCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: cn},
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
	return &agentCert{
		csrPEM:      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}),
		fingerprint: fp,
		priv:        key,
	}
}

// enrollToken 走 REST 同源的签发路径（TokenService）拿一次性 token。
func (h *harness) enrollToken(machine string, ac *agentCert, ttl time.Duration) string {
	h.t.Helper()
	tok, _, err := h.tokens.Issue(context.Background(), machine, ac.fingerprint, ttl)
	if err != nil {
		h.t.Fatalf("issue token: %v", err)
	}
	return tok
}

// enrollClient 建立仅校验服务端证书的连接（enroll 阶段，无客户端证书，FR-11.4）。
func (h *harness) enrollClient() *grpc.ClientConn {
	h.t.Helper()
	pool := x509.NewCertPool()
	caPEM := h.ca.CACertPEM()
	if !pool.AppendCertsFromPEM(caPEM) {
		h.t.Fatal("bad ca pem")
	}
	conn, err := grpc.NewClient(h.addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	})))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { conn.Close() })
	return conn
}

// enroll 走完整 enroll RPC（含 token）。
func (h *harness) enroll(machine, token string, ac *agentCert) (*fleetv1.EnrollResponse, error) {
	conn := h.enrollClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := fleetv1.NewFleetEnrollmentServiceClient(conn).Enroll(ctx, &fleetv1.EnrollRequest{
		MachineId: machine, Token: token, CsrPem: ac.csrPEM,
	})
	return resp, err
}

// connectClient 用已签发证书材料建立 mTLS Connect 客户端（agentd 常驻路径）。
func (h *harness) connectClient(certPEM []byte, priv *ecdsa.PrivateKey) (fleetv1.FleetAgentServiceClient, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(h.ca.CACertPEM()) {
		return nil, errors.New("bad ca pem")
	}
	leafDER, _ := pem.Decode(certPEM)
	if leafDER == nil {
		return nil, errors.New("bad cert pem")
	}
	cert := tls.Certificate{
		Certificate: [][]byte{leafDER.Bytes},
		PrivateKey:  priv,
		Leaf:        nil,
	}
	conn, err := grpc.NewClient(h.addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
	})))
	if err != nil {
		return nil, err
	}
	h.t.Cleanup(func() { conn.Close() })
	return fleetv1.NewFleetAgentServiceClient(conn), nil
}

// connectStream 打开 Connect 流并完成 Hello→Welcome 握手。
func (h *harness) connectStream(client fleetv1.FleetAgentServiceClient, machineID, version string,
) (grpc.BidiStreamingClient[fleetv1.AgentToServer, fleetv1.ServerToAgent], *fleetv1.Welcome, error) {
	stream, err := client.Connect(context.Background())
	if err != nil {
		return nil, nil, err
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_Hello{
		Hello: &fleetv1.Hello{MachineId: machineID, AgentdVersion: version, ProtocolVersion: ProtocolVersionV1},
	}}); err != nil {
		return nil, nil, err
	}
	first, err := stream.Recv()
	if err != nil {
		return nil, nil, err
	}
	w := first.GetWelcome()
	if w == nil {
		return nil, nil, errors.New("first server message is not Welcome")
	}
	return stream, w, nil
}

func (h *harness) machineStatus(name string) domain.MachineStatus {
	h.t.Helper()
	m, err := h.machines.Get(context.Background(), name)
	if err != nil {
		h.t.Fatalf("get machine: %v", err)
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) mustCondition(st domain.MachineStatus, condType, wantStatus, wantReason string) {
	h.t.Helper()
	cond, ok := st.GetCondition(condType)
	if !ok {
		h.t.Fatalf("condition %s missing; status=%+v", condType, st)
	}
	if cond.Status != wantStatus {
		h.t.Fatalf("condition %s = %s (%s), want %s (%s)", condType, cond.Status, cond.Reason, wantStatus, wantReason)
	}
	if wantReason != "" && cond.Reason != wantReason {
		h.t.Fatalf("condition %s reason = %s, want %s", condType, cond.Reason, wantReason)
	}
}

func obsFor(seq int64, hostname string) *fleetv1.ObservedState {
	return &fleetv1.ObservedState{
		InventorySeq: seq,
		Full:         true,
		Machine:      &fleetv1.MachineInfo{Os: "linux", Arch: "amd64", Hostname: hostname, HomeDir: "/home/u", Kernel: "6.1.0"},
		Agents:       []*fleetv1.AgentInstance{{Family: "agentd", Version: "0.2.0", Enabled: true}},
	}
}

// ---- 用例 ----

// enroll 成功 → 证书落库 → mTLS Connect → Hello/Welcome → 心跳 → inventory，
// Machine 条件按架构语义置位（§4.2/§6.2）。
func TestEnrollConnectHeartbeatInventoryHappyPath(t *testing.T) {
	h := newHarness(t, Config{OfflineAfter: time.Minute})
	ctx := context.Background()
	if err := h.machines.Create(ctx, &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-1")

	// Enroll 阶段无需客户端证书，但连接是 TLS（由监听配置保证）。
	resp, err := h.enroll("ws-1", h.enrollToken("ws-1", ac, time.Minute), ac)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if len(resp.GetCertPem()) == 0 {
		t.Fatal("empty cert pem")
	}

	client, err := h.connectClient(resp.GetCertPem(), ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	stream, welcome, err := h.connectStream(client, "ws-1", "0.2.0")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if welcome.GetProtocolVersion() != ProtocolVersionV1 || welcome.GetHeartbeatIntervalSeconds() == 0 {
		t.Fatalf("welcome incomplete: %+v", welcome)
	}
	st := h.machineStatus("ws-1")
	h.mustCondition(st, domain.ConditionAgentConnected, domain.ConditionTrue, "Connected")

	// 心跳 → lastHeartbeatAt 刷新。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_Heartbeat{
		Heartbeat: &fleetv1.Heartbeat{Sequence: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	// 全量 inventory → InventoryReady=True + os/arch 写入 status。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
		ObservedState: obsFor(1, "ws-1-host"),
	}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st = h.machineStatus("ws-1")
		if _, ok := st.GetCondition(domain.ConditionInventoryReady); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inventory condition not set in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.mustCondition(st, domain.ConditionInventoryReady, domain.ConditionTrue, "InventoryReceived")
	if st.OS != "linux" || st.Arch != "amd64" || st.Hostname != "ws-1-host" || st.InventorySeq != 1 {
		t.Fatalf("machine status fields not updated: %+v", st)
	}

	// 断开流 → AgentConnected=False(AgentDisconnected)。
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		st = h.machineStatus("ws-1")
		cond, _ := st.GetCondition(domain.ConditionAgentConnected)
		if cond.Status == domain.ConditionFalse {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disconnect not observed; got %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.mustCondition(st, domain.ConditionAgentConnected, domain.ConditionFalse, "AgentDisconnected")
}

// 重复 token：一次性消费，第二次 enroll 被拒（FR-11.5）。
func TestEnrollRejectsReplayedToken(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.machines.Create(context.Background(), &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-1")
	token := h.enrollToken("ws-1", ac, time.Minute)

	if _, err := h.enroll("ws-1", token, ac); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	ac2 := newAgentCert(t, "ws-1")
	_, err := h.enroll("ws-1", token, ac2)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("replayed enroll: want PermissionDenied, got %v", err)
	}
}

// 过期 token 拒绝（§4.6：短时效）。直接登记一条已过期的 token 行
// （TokenService.Issue 对非正 TTL 取默认值，故此处绕过签发 API）。
func TestEnrollRejectsExpiredToken(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.machines.Create(context.Background(), &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-1")
	token := "expired-token-0123456789abcdef"
	now := time.Now().UTC()
	if err := h.tokenStore.Create(context.Background(), &domain.EnrollmentToken{
		TokenHash:      enrollment.TokenHash(token),
		Machine:        "ws-1",
		CSRFingerprint: ac.fingerprint,
		CreatedAt:      now.Add(-2 * time.Minute),
		ExpiresAt:      now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	_, err := h.enroll("ws-1", token, ac)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expired enroll: want PermissionDenied, got %v", err)
	}
}

// 错误证书拒绝：由另一 CA 签发的客户端证书无法通过 TLS 校验（mTLS 信任根）。
func TestConnectRejectsForeignCACertificate(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.machines.Create(context.Background(), &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	// 伪造 CA：不在服务端 ClientCAs 内。
	foreignKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	foreignTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Foreign CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	foreignDER, err := x509.CreateCertificate(rand.Reader, foreignTpl, foreignTpl, &foreignKey.PublicKey, foreignKey)
	if err != nil {
		t.Fatal(err)
	}
	foreignCA, err := x509.ParseCertificate(foreignDER)
	if err != nil {
		t.Fatal(err)
	}

	ac := newAgentCert(t, "ws-1")
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "ws-1"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	badDER, err := x509.CreateCertificate(rand.Reader, tpl, foreignCA, &ac.priv.PublicKey, foreignKey)
	if err != nil {
		t.Fatal(err)
	}
	badPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: badDER})

	client, err := h.connectClient(badPEM, ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.connectStream(client, "ws-1", "0.2.0")
	if err == nil {
		t.Fatal("connect with foreign-CA certificate must fail")
	}
}

// 已删除机器的证书被拒（§4.6 v1.1.2 取舍 (a)：删除即拒绝，不是"证书未到期就放行"）。
func TestConnectRejectsDeletedMachineCertificate(t *testing.T) {
	h := newHarness(t, Config{})
	ctx := context.Background()
	if err := h.machines.Create(ctx, &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-1")
	resp, err := h.enroll("ws-1", h.enrollToken("ws-1", ac, time.Minute), ac)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// 删除机器（走控制器的删除后钩子：退役证书）。
	if err := h.machines.Delete(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	if err := h.ctrl.OnMachineDeleted(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}

	client, err := h.connectClient(resp.GetCertPem(), ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = h.connectStream(client, "ws-1", "0.2.0")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("connect after machine delete: want PermissionDenied, got %v", err)
	}
}

// 同一 machineId 的第二条 Connect 流被拒并告警（FR-11.6）。
func TestConnectRejectsDuplicateStream(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.machines.Create(context.Background(), &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-1")
	resp, err := h.enroll("ws-1", h.enrollToken("ws-1", ac, time.Minute), ac)
	if err != nil {
		t.Fatal(err)
	}
	client, err := h.connectClient(resp.GetCertPem(), ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := h.connectStream(client, "ws-1", "0.2.0")
	if err != nil {
		t.Fatalf("first stream: %v", err)
	}
	defer first.CloseSend()

	// 同证书的第二条流（模拟证书被复用/抢注）。
	second, _, err := h.connectStream(client, "ws-1", "0.2.0")
	if err == nil {
		second.CloseSend()
		t.Fatal("second concurrent stream must be rejected")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("second stream: want FailedPrecondition, got %v", err)
	}
}

// 心跳超时置离线并可恢复（issue 验收第 6 条；§4.2）。
func TestHeartbeatTimeoutMarksOfflineThenRecovers(t *testing.T) {
	h := newHarness(t, Config{OfflineAfter: 300 * time.Millisecond, OfflineScanEvery: 100 * time.Millisecond})
	if err := h.machines.Create(context.Background(), &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-1")
	resp, err := h.enroll("ws-1", h.enrollToken("ws-1", ac, time.Minute), ac)
	if err != nil {
		t.Fatal(err)
	}
	client, err := h.connectClient(resp.GetCertPem(), ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err := h.connectStream(client, "ws-1", "0.2.0")
	if err != nil {
		t.Fatal(err)
	}

	// 不发心跳，等待扫描器置离线。
	waitFor(t, 5*time.Second, func() bool {
		st := h.machineStatus("ws-1")
		cond, ok := st.GetCondition(domain.ConditionAgentConnected)
		return ok && cond.Status == domain.ConditionFalse && cond.Reason == "AgentDisconnected"
	}, "AgentConnected should flip False(AgentDisconnected) after offlineAfter")

	// 恢复：心跳重新到达 → True。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_Heartbeat{
		Heartbeat: &fleetv1.Heartbeat{Sequence: 1},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool {
		st := h.machineStatus("ws-1")
		cond, ok := st.GetCondition(domain.ConditionAgentConnected)
		return ok && cond.Status == domain.ConditionTrue
	}, "AgentConnected should recover to True on heartbeat")
}

// 观测有序性（FR-8.7）：落后 seq 的报文不改写条件与 status。
func TestStaleInventoryDoesNotRewriteStatus(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.machines.Create(context.Background(), &domain.Machine{Metadata: domain.ObjectMeta{Name: "ws-1"}}); err != nil {
		t.Fatal(err)
	}
	ac := newAgentCert(t, "ws-1")
	resp, err := h.enroll("ws-1", h.enrollToken("ws-1", ac, time.Minute), ac)
	if err != nil {
		t.Fatal(err)
	}
	client, err := h.connectClient(resp.GetCertPem(), ac.priv)
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err := h.connectStream(client, "ws-1", "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
		ObservedState: obsFor(7, "new-host"),
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool {
		return h.machineStatus("ws-1").InventorySeq == 7
	}, "seq 7 should be recorded")
	// 迟到的旧报文（seq 3）不得改写（FR-8.7）。
	if err := stream.Send(&fleetv1.AgentToServer{Payload: &fleetv1.AgentToServer_ObservedState{
		ObservedState: obsFor(3, "old-host"),
	}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	st := h.machineStatus("ws-1")
	if st.InventorySeq != 7 || st.Hostname != "new-host" {
		t.Fatalf("stale observation rewrote status: seq=%d host=%s", st.InventorySeq, st.Hostname)
	}
}

func waitFor(t *testing.T, within time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
