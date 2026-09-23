package enrollment

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

// testCA 建立临时 CA 与配套存储。
func testCA(t *testing.T) (*CA, *sqlite.DB, *TokenService) {
	t.Helper()
	ca, err := EnsureCA(filepath.Join(t.TempDir(), "pki"))
	if err != nil {
		t.Fatalf("ensure ca: %v", err)
	}
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), testLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tokens := NewTokenService(sqlite.NewEnrollmentStore(db))
	return ca, db, tokens
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeCSR 生成测试 CSR 并返回 PEM 与公钥指纹。
func makeCSR(t *testing.T, cn string) ([]byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: cn},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tpl, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	fp, err := PublicKeyFingerprint(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return csrPEM, fp
}

func TestEnsureCACreatesSecretsWith0600AndReloads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	ca1, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	for _, f := range []string{caKeyFile, serverKey} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s mode = %o, want 600", f, perm)
		}
	}
	ca2, err := EnsureCA(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if string(ca1.caPEM) != string(ca2.caPEM) {
		t.Fatal("reload must reuse existing CA material (§4.6: CA 不重复生成)")
	}
	// 服务端证书由本 CA 签发且 EKU 匹配。
	if err := ca1.verifyOwn(ca1.server.Leaf, x509.ExtKeyUsageServerAuth); err != nil {
		t.Fatalf("server cert not issued by CA: %v", err)
	}
	// 篡改：证书在、私钥缺失 → 报错并提示恢复手册（材料不完整不静默重建）。
	if err := os.Remove(filepath.Join(dir, caKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCA(dir); err == nil {
		t.Fatal("incomplete CA material must fail loudly")
	}
}

func TestIssueClientCertificateForcesMachineIdentity(t *testing.T) {
	ca, db, _ := testCA(t)
	certs := sqlite.NewAgentCertificateStore(db)
	csrPEM, _ := makeCSR(t, "ATTACKER-CN")

	certPEM, serial, notAfter, err := ca.IssueClientCertificate(csrPEM, "workstation-1", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// CN 由服务端认证的 machine 决定，不信任 CSR 自报 Subject（§7.5 身份绑定）。
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "workstation-1" {
		t.Fatalf("cert CN = %q, want machine id", cert.Subject.CommonName)
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 || len(cert.ExtKeyUsage) != 1 ||
		cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("client cert EKU wrong: %+v", cert.ExtKeyUsage)
	}
	if serial == "" || !notAfter.After(time.Now()) {
		t.Fatalf("serial/notAfter invalid: %q %v", serial, notAfter)
	}
	if err := certs.Record(context.Background(), &domain.AgentCertificate{
		Serial: serial, Machine: "workstation-1",
		NotBefore: cert.NotBefore, NotAfter: cert.NotAfter, IssuedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
}

func TestTokenServiceIssueAndRedeem(t *testing.T) {
	_, db, tokens := testCA(t)
	machines := sqlite.NewMachineStore(db)
	ctx := context.Background()
	if err := machines.Create(ctx, &domain.Machine{Metadata: domain.ObjectMeta{Name: "m1"}}); err != nil {
		t.Fatal(err)
	}
	csrPEM, fp := makeCSR(t, "m1")
	_ = csrPEM

	token, expiresAt, err := tokens.Issue(ctx, "m1", fp, time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(token) < 32 {
		t.Fatalf("token entropy too small: %d chars", len(token))
	}
	if !expiresAt.After(time.Now()) {
		t.Fatal("expiresAt must be in the future")
	}
	if err := tokens.Redeem(ctx, "m1", token, fp); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	// 重放被拒（一次性）。
	if err := tokens.Redeem(ctx, "m1", token, fp); err == nil {
		t.Fatal("replayed token must be rejected")
	}
}

func TestIPLimiterAllowsConfiguredFailures(t *testing.T) {
	l := NewIPLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.Check("10.0.0.1") {
			t.Fatalf("attempt %d should pass", i+1)
		}
		l.RecordFailure("10.0.0.1")
	}
	if l.Check("10.0.0.1") {
		t.Fatal("4th failure over limit must be blocked")
	}
	if !l.Check("10.0.0.2") {
		t.Fatal("other source IP unaffected")
	}
}
