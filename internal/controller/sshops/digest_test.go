package sshops

// KM-26 核查回归：节点侧 agentd 二进制摘要校验必须是**真**校验。
// 核查复现过两处空操作：错误摘要被 Contains 子串匹配接受、`test -x` 复用分支
// 完全不校验（FR-12.8 要求经 SSH 上传的临时二进制携带并校验摘要方可执行）。

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/sshtransport"
)

func digestTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeTransport 生成一个假 ssh 可执行文件：把每次调用的参数追加到 argv.log，
// 并按 responder（命令键 → (stdout, 退出码)）输出；responder 为 nil 表示"命令不存在"。
func fakeTransport(t *testing.T, dir string, respond func(key string) (stdout string, exit int)) (*sshtransport.Transport, string) {
	t.Helper()
	logPath := filepath.Join(dir, "argv.log")
	script := filepath.Join(dir, "fake-ssh")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("printf '%s\\n' \"$*\" >> " + logPath + "\n")
	sb.WriteString("case \"$*\" in\n")
	// shell case 的模式里不能出现未转义的空白，因此"模式"与"responder 键"分开。
	for _, probe := range []struct{ pattern, key string }{
		{"*version*", "agent-fleet-agentd version"},
		{"*test*-x*", "test -x"},
		{"*sha256sum*", "sha256sum"},
		{"*shasum*", "shasum"},
		{"*openssl*", "openssl"},
		{"*install*", "install"},
		{"*uname*", "uname"},
	} {
		stdout, exit := "", 1
		if respond != nil {
			stdout, exit = respond(probe.key)
		}
		sb.WriteString("  " + probe.pattern + ") printf '%s\\n' " + shellQuote(stdout) +
			"; exit " + strconv.Itoa(exit) + " ;;\n")
	}
	sb.WriteString("  *) exit 1 ;;\n")
	sb.WriteString("esac\n")
	if err := os.WriteFile(script, []byte(sb.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return sshtransport.New(sshtransport.Config{SSHBinary: script, Log: digestTestLogger()}), logPath
}

// fakeSSHPath 取得假 ssh 的路径（fakeTransport 未导出脚本路径，这里按约定拼）。
func fakeSSHPath(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "fake-ssh")
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestVerifyRemoteDigestRejectsWrongHash：sha256sum / shasum / openssl 三种输出形态
// 下的错误摘要都必须被拒绝（不匹配即失败，不 fallback 到下一个工具）。
func TestVerifyRemoteDigestRejectsWrongHash(t *testing.T) {
	wantHex := strings.Repeat("a", 64)
	wrongHex := strings.Repeat("b", 64)
	remote := "/home/node/.local/share/agent-fleet/staging/bin/agentd-" + wantHex

	cases := []struct {
		name   string
		stdout string
		tool   string
	}{
		{"sha256sum-style", wrongHex + "  " + remote, "sha256sum"},
		{"shasum-style", wrongHex + "  " + remote, "shasum"},
		{"openssl-style", "SHA256(" + remote + ")= " + wrongHex, "openssl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tr, logPath := fakeTransport(t, dir, func(key string) (string, int) {
				if key == tc.tool {
					return tc.stdout, 0
				}
				return "", 1 // 其它工具"不存在"
			})
			o := New(Config{Transport: tr, Log: digestTestLogger()})
			err := o.verifyRemoteDigest(context.Background(), "node-1", remote, "sha256:"+wantHex)
			if err == nil {
				raw, _ := os.ReadFile(logPath)
				t.Fatalf("wrong digest accepted (FR-12.8 violated); remote calls:\n%s", raw)
			}
			if got := domain.ReasonOf(err); got != domain.ReasonInstallerFailed {
				t.Fatalf("reason = %s, want %s (%v)", got, domain.ReasonInstallerFailed, err)
			}
		})
	}
}

// TestVerifyRemoteDigestAcceptsExactMatch：正确摘要（三种形态）都必须通过，
// 证明拒绝不是因为"永远失败"。
func TestVerifyRemoteDigestAcceptsExactMatch(t *testing.T) {
	wantHex := strings.Repeat("c", 64)
	remote := "/home/node/.local/share/agent-fleet/staging/bin/agentd-" + wantHex
	cases := []struct {
		name   string
		stdout string
		match  string
	}{
		{"sha256sum", wantHex + "  " + remote, "sha256sum"},
		{"shasum", wantHex + "  " + remote, "shasum"},
		{"openssl", "SHA256(" + remote + ")= " + wantHex, "openssl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tr, _ := fakeTransport(t, dir, func(key string) (string, int) {
				if key == tc.match {
					return tc.stdout, 0
				}
				return "", 1
			})
			o := New(Config{Transport: tr, Log: digestTestLogger()})
			if err := o.verifyRemoteDigest(context.Background(), "node-1", remote, "sha256:"+wantHex); err != nil {
				t.Fatalf("correct digest rejected: %v", err)
			}
		})
	}
}

// TestVerifyRemoteDigestRejectsGarbage：不可解析输出、缺失工具、畸形期望值都必须失败
// （绝不"无法校验就当通过"）。
func TestVerifyRemoteDigestRejectsGarbage(t *testing.T) {
	wantHex := strings.Repeat("d", 64)
	remote := "/home/node/staging/bin/agentd-" + wantHex

	t.Run("truncated-token", func(t *testing.T) {
		dir := t.TempDir()
		tr, _ := fakeTransport(t, dir, func(key string) (string, int) {
			if key == "sha256sum" {
				return wantHex[:40] + "  " + remote, 0
			}
			return "", 1
		})
		o := New(Config{Transport: tr, Log: digestTestLogger()})
		if err := o.verifyRemoteDigest(context.Background(), "node-1", remote, "sha256:"+wantHex); err == nil {
			t.Fatal("truncated digest token accepted")
		}
	})
	t.Run("no-tool", func(t *testing.T) {
		dir := t.TempDir()
		tr, _ := fakeTransport(t, dir, nil)
		o := New(Config{Transport: tr, Log: digestTestLogger()})
		err := o.verifyRemoteDigest(context.Background(), "node-1", remote, "sha256:"+wantHex)
		if err == nil {
			t.Fatal("missing digest tools must fail closed")
		}
		if !strings.Contains(err.Error(), "could not verify") {
			t.Fatalf("unexpected message: %v", err)
		}
	})
	t.Run("malformed-expected", func(t *testing.T) {
		dir := t.TempDir()
		tr, _ := fakeTransport(t, dir, func(key string) (string, int) {
			if key == "sha256sum" {
				return wantHex + "  x", 0
			}
			return "", 1
		})
		o := New(Config{Transport: tr, Log: digestTestLogger()})
		if err := o.verifyRemoteDigest(context.Background(), "node-1", remote, "sha256:nothex"); err == nil {
			t.Fatal("malformed expected digest must be refused")
		}
	})
}

// TestParseDigestOutput 锁定三种工具的解析规则（只认唯一摘要 token）。
func TestParseDigestOutput(t *testing.T) {
	want := strings.Repeat("e", 64)
	cases := []struct {
		tool, stdout, want string
		ok                 bool
	}{
		{"sha256sum", want + "  /path/to/file\n", want, true},
		{"shasum", want + "  /path/to/file\n", want, true},
		{"openssl", "SHA256(/path/to/file)= " + want + "\n", want, true},
		{"sha256sum", "not-a-digest  /path\n", "", false},
		{"openssl", "garbage", "", false},
		{"sha256sum", "", "", false},
	}
	for _, tc := range cases {
		got, err := parseDigestOutput(tc.tool, tc.stdout)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("%s %q: got %q err=%v, want %q", tc.tool, tc.stdout, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s %q: expected error, got %q", tc.tool, tc.stdout, got)
		}
	}
}

// TestEnsureAgentdReusePathVerifiesDigest：`test -x` 复用分支也必须校验摘要；
// 摘要不可验证时必须 fail-closed（绝不把未经校验的二进制交出去执行）。
func TestEnsureAgentdReusePathVerifiesDigest(t *testing.T) {
	wantHex := strings.Repeat("f", 64)
	remote := "/home/node/.local/share/agent-fleet/staging/bin/agentd-" + wantHex
	dir := t.TempDir()
	_, logPath := fakeTransport(t, dir, func(key string) (string, int) {
		switch key {
		case "agent-fleet-agentd version":
			return "", 1 // 节点没装 agentd
		case "test -x":
			return "", 0 // 临时二进制"存在"
		case "sha256sum":
			return strings.Repeat("0", 64) + "  " + remote, 0 // 节点报的摘要与期望不符
		case "install":
			return "", 0
		}
		return "", 1
	})
	binDir := filepath.Join(dir, "agentd")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(binDir, "agentd-linux-amd64")
	if err := os.WriteFile(local, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// scp 用"永远成功"的假实现：本用例只关心 digest 校验，不关心传输本身。
	scp := filepath.Join(dir, "fake-scp")
	if err := os.WriteFile(scp, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := New(Config{
		Transport: sshtransport.New(sshtransport.Config{
			SSHBinary: fakeSSHPath(t, dir), SCPBinary: scp, Log: digestTestLogger(),
		}),
		AgentdBinDir: binDir, Log: digestTestLogger(),
	})
	got, err := o.ensureAgentd(context.Background(), "node-1", "/home/node", "linux", "amd64")
	raw, _ := os.ReadFile(logPath)
	if !strings.Contains(string(raw), "sha256sum") {
		t.Fatalf("reuse path did not verify the digest; remote calls:\n%s", raw)
	}
	if err == nil {
		t.Fatalf("a binary whose digest cannot be verified must not be handed out (got %s)", got)
	}
	if code := domain.ReasonOf(err); code != domain.ReasonInstallerFailed {
		t.Fatalf("reason = %s, want %s (%v)", code, domain.ReasonInstallerFailed, err)
	}
}
