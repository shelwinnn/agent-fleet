package sshtransport

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// ---- QuoteArgs：引用必须让远端 shell 还原出原始 argv（防注入的核心） ----

func TestQuoteArgsRoundTrip(t *testing.T) {
	cases := [][]string{
		{"echo", "hello"},
		{"echo", "a b"},
		{"echo", "; rm -rf /"},
		{"echo", "$(id)"},
		{"echo", "`id`"},
		{"echo", "it's"},
		{"echo", ""},
		{"echo", "a|b>c<d&e"},
		{"agent-fleet-agentd", "oneshot", "plan", "--bundle", "/home/u/.local/share/agent-fleet/staging/bundle"},
		{"echo", "*", "?"},
	}
	for _, argv := range cases {
		quoted := QuoteArgs(argv)
		// 用 POSIX sh 还原：set -- <quoted>; printf '%s\n' "$@"
		cmd := exec.Command("/bin/sh", "-c", `set -- `+quoted+`; printf '%s\n' "$@"`)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("sh -c %q: %v", quoted, err)
		}
		got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(argv) == 1 && argv[0] == "" {
			got = []string{""}
		}
		if len(got) != len(argv) {
			t.Fatalf("argv %q round-trip gave %q", argv, got)
		}
		for i := range argv {
			if got[i] != argv[i] {
				t.Fatalf("argv[%d] %q round-trip gave %q (quoted=%s)", i, argv[i], got[i], quoted)
			}
		}
	}
}

func TestQuoteArgsNeverLeavesMetacharactersBare(t *testing.T) {
	quoted := QuoteArgs([]string{"echo", "a;b"})
	if !strings.Contains(quoted, `'a;b'`) {
		t.Fatalf("metacharacters must be quoted, got %q", quoted)
	}
}

// ---- 受限执行器：本地不经 shell、argv 结构化、BatchMode、host-key 绝不放宽 ----

// fakeSSH 生成一个假的 ssh 可执行文件：-G 时输出预置生效配置，其余情况记录
// 收到的 argv 并按配置的退出码/stderr 结束。
func fakeSSH(t *testing.T, gOutput string) (bin string, argvFile string, stderrFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "ssh")
	argvFile = filepath.Join(dir, "argv.txt")
	stderrFile = filepath.Join(dir, "stderr.txt")
	gFile := filepath.Join(dir, "g.txt")
	if err := os.WriteFile(gFile, []byte(gOutput), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
: > ` + argvFile + `
for a in "$@"; do printf '%s\n' "$a" >> ` + argvFile + `; done
case "$*" in
  *-G*) cat ` + gFile + ` ;;
esac
if [ -f ` + stderrFile + ` ]; then cat ` + stderrFile + ` >&2; fi
if [ -f ` + dir + `/exit ]; then exit "$(cat ` + dir + `/exit)"; fi
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile, stderrFile
}

func TestResolveParsesSSHConfig(t *testing.T) {
	g := strings.Join([]string{
		"host node-a",
		"user deploy",
		"hostname 10.0.0.5",
		"port 2222",
		"proxyjump bastion",
		"identityfile /home/deploy/.ssh/id_ed25519",
		"identityfile none",
		"batchmode yes",
	}, "\n")
	bin, _, _ := fakeSSH(t, g)
	tr := New(Config{SSHBinary: bin})
	target, err := tr.Resolve(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if target.HostName != "10.0.0.5" || target.User != "deploy" || target.Port != 2222 ||
		target.ProxyJump != "bastion" || len(target.IdentityFile) != 1 {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestRunUsesStructuredArgvWithoutShell(t *testing.T) {
	bin, argvFile, _ := fakeSSH(t, "host node-a\nhostname node-a\n")
	tr := New(Config{SSHBinary: bin, ClientConfig: "/tmp/client.conf"})
	remoteArg := "a b; rm -rf /"
	if _, err := tr.Run(context.Background(), "node-a", []string{"echo", remoteArg}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	want := []string{"-F", "/tmp/client.conf", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		"node-a", "--", "echo 'a b; rm -rf /'"}
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full: %q)", i, got[i], want[i], got)
		}
	}
	// 远端命令必须是**单个** argv 元素（一旦拆开就不可能被本地 shell 或 ssh 误解析）。
	if strings.Count(strings.Join(got[8:], " "), " ") == 0 {
		t.Fatal("remote command must be one argv element")
	}
	// 硬边界：绝不出现放宽 host-key 校验的选项（§13.3/FR-12.3）。
	joined := strings.Join(got, " ")
	for _, forbidden := range []string{"StrictHostKeyChecking", "no", "-oUserKnownHostsFile=/dev/null"} {
		if forbidden == "no" {
			continue
		}
		if strings.Contains(joined, forbidden) {
			t.Fatalf("argv must not contain %q: %q", forbidden, joined)
		}
	}
}

func TestBaseArgsNeverDisableHostKeyChecking(t *testing.T) {
	tr := New(Config{})
	args := strings.Join(tr.baseArgs(), " ")
	if strings.Contains(args, "StrictHostKeyChecking") || strings.Contains(args, "UserKnownHostsFile") {
		t.Fatalf("transport must not touch host-key policy: %q", args)
	}
}

func TestClassifySSHFailures(t *testing.T) {
	cases := []struct {
		name    string
		stderr  string
		code    int
		timeout bool
		want    string
	}{
		{"dns", "ssh: Could not resolve hostname nope: Name or service not known", 255, false, domain.ReasonDNSResolveFailed},
		{"hostkey", "Host key verification failed.", 255, false, domain.ReasonHostKeyVerificationFailed},
		{"hostkey-changed", "@@@@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!", 255, false, domain.ReasonHostKeyVerificationFailed},
		{"auth", "deploy@10.0.0.5: Permission denied (publickey).", 255, false, domain.ReasonAuthenticationFailed},
		{"conn", "ssh: connect to host 10.0.0.5 port 22: Connection timed out", 255, false, domain.ReasonConnectionTimeout},
		{"refused", "ssh: connect to host 10.0.0.5 port 22: Connection refused", 255, false, domain.ReasonConnectionTimeout},
		{"remote", "agent-fleet-agentd: bundle verify failed", 3, false, domain.ReasonRemoteCommandFailed},
		{"timeout-no-output", "", 0, true, domain.ReasonConnectionTimeout},
		{"timeout-with-output", "", 0, true, domain.ReasonRemoteCommandFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Result{Stderr: []byte(tc.stderr), ExitCode: tc.code, Stdout: nil}
			if tc.name == "timeout-with-output" {
				res.Stdout = []byte("partial")
			}
			err := classify(res, tc.timeout)
			if got := domain.ReasonOf(err); got != tc.want {
				t.Fatalf("want %s, got %s (%v)", tc.want, got, err)
			}
		})
	}
}

func TestRunPropagatesClassifiedError(t *testing.T) {
	bin, _, stderrFile := fakeSSH(t, "")
	if err := os.WriteFile(stderrFile, []byte("Host key verification failed.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(bin), "exit"), []byte("255"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := New(Config{SSHBinary: bin})
	_, err := tr.Run(context.Background(), "node-a", []string{"true"})
	if got := domain.ReasonOf(err); got != domain.ReasonHostKeyVerificationFailed {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonHostKeyVerificationFailed, got, err)
	}
}

func TestCommandTimeoutIsEnforced(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ssh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := New(Config{SSHBinary: bin, CommandTimeout: 200 * time.Millisecond})
	start := time.Now()
	_, err := tr.Run(context.Background(), "node-a", []string{"true"})
	if err == nil {
		t.Fatal("timeout must produce an error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not enforced: %s", time.Since(start))
	}
	if got := domain.ReasonOf(err); got != domain.ReasonConnectionTimeout {
		t.Fatalf("want %s, got %s (%v)", domain.ReasonConnectionTimeout, got, err)
	}
}

func TestAliasAndOperandValidation(t *testing.T) {
	tr := New(Config{})
	for _, bad := range []string{"-oProxyCommand=evil", "a b", "a\nb", ""} {
		if _, err := tr.Run(context.Background(), bad, []string{"true"}); err == nil {
			t.Errorf("alias %q must be rejected", bad)
		}
	}
	if err := validateScpOperand("-oProxyCommand=evil"); err == nil {
		t.Error("scp operand starting with '-' must be rejected")
	}
	if err := validateScpOperand("/ok/path"); err != nil {
		t.Errorf("absolute operand rejected: %v", err)
	}
}

func TestUnsupportedPlatformError(t *testing.T) {
	err := UnsupportedPlatformError("plan9", "mips", "no prebuilt agentd")
	if got := domain.ReasonOf(err); got != domain.ReasonUnsupportedPlatform {
		t.Fatalf("want %s, got %s", domain.ReasonUnsupportedPlatform, got)
	}
	var ce *domain.CodedError
	if !errors.As(err, &ce) {
		t.Fatalf("want *domain.CodedError, got %T", err)
	}
}

// ---- staging 布局与清理规则（§4.5 A10） ----

func TestRemoteLayoutPaths(t *testing.T) {
	l, err := NewRemoteLayout("/home/deploy")
	if err != nil {
		t.Fatal(err)
	}
	if l.Root() != "/home/deploy/.local/share/agent-fleet/staging" {
		t.Fatalf("root = %s", l.Root())
	}
	if l.BundleDir() != l.Root()+"/bundle" || l.BinDir() != l.Root()+"/bin" {
		t.Fatalf("fixed subdirs wrong: %s %s", l.BundleDir(), l.BinDir())
	}
	if l.PlanFile("op-1") != l.Root()+"/plan-op-1.json" {
		t.Fatalf("plan file = %s", l.PlanFile("op-1"))
	}
	if !strings.HasSuffix(l.AgentdBinary("sha256:abc"), "/bin/agentd-abc") {
		t.Fatalf("agentd path = %s", l.AgentdBinary("sha256:abc"))
	}
	if _, err := NewRemoteLayout("relative/home"); err == nil {
		t.Fatal("relative homeDir must be rejected")
	}
}

func TestDecideCleanup(t *testing.T) {
	cases := []struct {
		name string
		prev *domain.Operation
		want bool
	}{
		{"nil", nil, true},
		{"unresolved-pending", opWithPhase(domain.OperationPhasePending, ""), false},
		{"unresolved-unknown", opWithPhase(domain.OperationPhaseUnknown, ""), false},
		{"unresolved-awaiting", opWithPhase(domain.OperationPhaseAwaitingConfirmation, ""), false},
		{"terminal-succeeded", opWithPhase(domain.OperationPhaseSucceeded, ""), true},
		{"terminal-failed", opWithPhase(domain.OperationPhaseFailed, ""), true},
		{"terminal-cancelled", opWithPhase(domain.OperationPhaseFailed, domain.ModifierCancelled), true},
		{"terminal-skipped", opWithPhase(domain.OperationPhaseSucceeded, domain.ModifierSkipped), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideCleanup(tc.prev)
			if got.Clean != tc.want {
				t.Fatalf("Clean = %v, want %v (%s)", got.Clean, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Fatal("decision must carry a human-readable reason")
			}
		})
	}
}

func TestPostRunCleanup(t *testing.T) {
	if d := PostRunCleanup(true); !d.Clean {
		t.Fatalf("node-confirmed run must allow cleanup: %s", d.Reason)
	}
	if d := PostRunCleanup(false); d.Clean {
		t.Fatal("unconfirmed node stop must keep inputs")
	}
}

func opWithPhase(phase, modifier string) *domain.Operation {
	op := &domain.Operation{Status: domain.OperationStatus{Phase: phase, TerminalModifier: modifier}}
	op.Metadata.Name = "op-1"
	return op
}

// ---- KM-26 核查回归：scp 方向、连接前分类的远端输出守卫、操作数校验 ----

// fakeSCP 生成一个假 scp，记录收到的 argv。
func fakeSCP(t *testing.T) (bin, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "scp")
	argvFile = filepath.Join(dir, "scp-argv.txt")
	script := "#!/bin/sh\n: > " + argvFile + "\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + argvFile + "; done\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

func TestScpUploadAndDownloadDirections(t *testing.T) {
	bin, argvFile := fakeSCP(t)
	tr := New(Config{SCPBinary: bin, ClientConfig: "/tmp/c.conf"})
	ctx := context.Background()

	if err := tr.Upload(ctx, "node-a", "/local/bundle", "/remote/staging/bundle"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	up := readArgv(t, argvFile)
	if up[len(up)-2] != "/local/bundle" || up[len(up)-1] != "node-a:/remote/staging/bundle" {
		t.Fatalf("Upload operands = %v, want [<local> node-a:<remote>]", up[len(up)-2:])
	}

	if err := tr.Download(ctx, "node-a", "/remote/staging/result.json", "/local/result.json"); err != nil {
		t.Fatalf("Download: %v", err)
	}
	down := readArgv(t, argvFile)
	if down[len(down)-2] != "node-a:/remote/staging/result.json" || down[len(down)-1] != "/local/result.json" {
		t.Fatalf("Download operands = %v, want [node-a:<remote> <local>]", down[len(down)-2:])
	}
}

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func TestScpRejectsRelativeRemotePath(t *testing.T) {
	bin, _ := fakeSCP(t)
	tr := New(Config{SCPBinary: bin})
	if err := tr.Upload(context.Background(), "node-a", "/local/x", "relative/path"); err == nil {
		t.Fatal("relative remote path must be rejected (only absolute staging paths are used)")
	}
}

// TestClassifyRemoteOutputIsNotConnectionFailure：远端进程自己打印
// "Host key verification failed." 并退 255 时，命令**已经启动**，不能当成连接前失败
// （否则会把 Unknown 误判成 Failed 并提前释放机器级互斥，§6.4/FR-15.4）。
func TestClassifyRemoteOutputIsNotConnectionFailure(t *testing.T) {
	res := Result{
		Stdout:   []byte(`{"phase":"Failed","reason":"RemoteCommandFailed"}`),
		Stderr:   []byte("Host key verification failed.\n"),
		ExitCode: 255,
	}
	err := classify(res, false)
	if got := domain.ReasonOf(err); got != domain.ReasonRemoteCommandFailed {
		t.Fatalf("reason = %s, want %s (%v)", got, domain.ReasonRemoteCommandFailed, err)
	}
	// 反过来：远端没有任何输出 + 同样的 stderr → 连接前失败（host-key 校验失败）。
	res.Stdout = nil
	err = classify(res, false)
	if got := domain.ReasonOf(err); got != domain.ReasonHostKeyVerificationFailed {
		t.Fatalf("reason = %s, want %s (%v)", got, domain.ReasonHostKeyVerificationFailed, err)
	}
}
