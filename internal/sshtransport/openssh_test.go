package sshtransport

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// sshTestTime 固定生成时间：产物内容不含时间，渲染应当与它无关（确定性）。
var sshTestTime = time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

// sshTestMachine 直接用 json.RawMessage 构造 Machine，不依赖存储层。
func sshTestMachine(name, spec string) *domain.Machine {
	return &domain.Machine{
		Metadata: domain.ObjectMeta{Name: name},
		Spec:     json.RawMessage(spec),
	}
}

// 有显式 hostName 的机器被导出：字段齐全、顺序固定、Port 正确（FR-12.6、§13.5）。
func TestRenderIncludeExportsExplicitHost(t *testing.T) {
	m := sshTestMachine("devbox-01", `{
		"managementMode": "ssh",
		"ssh": {
			"hostName": "devbox.internal",
			"user": "dev",
			"port": 2222,
			"proxyJump": "bastion",
			"identityFile": "/home/dev/.ssh/id_ed25519"
		}
	}`)
	r, err := RenderInclude([]*domain.Machine{m}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	want := "# 由 agent-fleet 生成，请勿手工编辑（FR-12.6）\n" +
		"# 主配置需包含一行：" + IncludeDirective + "\n" +
		"Host devbox-01\n" +
		"  HostName devbox.internal\n" +
		"  User dev\n" +
		"  Port 2222\n" +
		"  ProxyJump bastion\n" +
		"  IdentityFile /home/dev/.ssh/id_ed25519\n"
	if r.Content != want {
		t.Fatalf("content mismatch:\n--- got ---\n%s\n--- want ---\n%s", r.Content, want)
	}
	if len(r.Included) != 1 || r.Included[0] != "devbox-01" {
		t.Fatalf("Included = %v, want [devbox-01]", r.Included)
	}
	if len(r.Skipped) != 0 {
		t.Fatalf("Skipped = %v, want empty", r.Skipped)
	}
	if !r.GeneratedAt.Equal(sshTestTime) {
		t.Fatalf("GeneratedAt = %v, want %v", r.GeneratedAt, sshTestTime)
	}
	if !strings.HasSuffix(r.Content, "\n") || strings.HasSuffix(r.Content, "\n\n") {
		t.Fatalf("content must end with exactly one newline: %q", r.Content)
	}
	// 绝不输出私钥内容（FR-12.2、§29.2）：只有路径字符串。
	if strings.Contains(r.Content, "PRIVATE KEY") {
		t.Fatalf("content must never carry private key material:\n%s", r.Content)
	}
}

// 可选字段缺省时不输出；Port=0 视为未设置（FR-12.6）。
func TestRenderIncludeOmitsUnsetFields(t *testing.T) {
	m := sshTestMachine("solo", `{"ssh":{"hostName":"solo.internal"}}`)
	r, err := RenderInclude([]*domain.Machine{m}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	want := "# 由 agent-fleet 生成，请勿手工编辑（FR-12.6）\n" +
		"# 主配置需包含一行：" + IncludeDirective + "\n" +
		"Host solo\n" +
		"  HostName solo.internal\n"
	if r.Content != want {
		t.Fatalf("content mismatch:\n--- got ---\n%s\n--- want ---\n%s", r.Content, want)
	}
	// port 显式为 0 时同样不输出。
	m0 := sshTestMachine("zero-port", `{"ssh":{"hostName":"z.internal","port":0}}`)
	r0, err := RenderInclude([]*domain.Machine{m0}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	if strings.Contains(r0.Content, "Port") {
		t.Fatalf("port 0 must be treated as unset:\n%s", r0.Content)
	}
}

// 仅经 hostAlias 导入的机器不重复导出，且在 Skipped 里带非空原因（FR-12.6、§7.7）。
func TestRenderIncludeSkipsHostAliasOnly(t *testing.T) {
	aliasOnly := sshTestMachine("wsl-gpu", `{"managementMode":"ssh","ssh":{"hostAlias":"wsl-gpu"}}`)
	explicit := sshTestMachine("devbox-01", `{"managementMode":"ssh","ssh":{"hostName":"devbox.internal"}}`)
	r, err := RenderInclude([]*domain.Machine{aliasOnly, explicit}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	if strings.Contains(r.Content, "wsl-gpu") {
		t.Fatalf("hostAlias-only machine must not appear in content:\n%s", r.Content)
	}
	if len(r.Included) != 1 || r.Included[0] != "devbox-01" {
		t.Fatalf("Included = %v, want [devbox-01]", r.Included)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Name != "wsl-gpu" {
		t.Fatalf("Skipped = %v, want one entry for wsl-gpu", r.Skipped)
	}
	if r.Skipped[0].Reason == "" {
		t.Fatal("skipped reason must not be empty")
	}
	// 有显式字段但缺 hostName：同样跳过并记原因，不产出坏配置。
	noHost := sshTestMachine("partial", `{"ssh":{"user":"dev","port":22}}`)
	r2, err := RenderInclude([]*domain.Machine{noHost}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	if len(r2.Included) != 0 {
		t.Fatalf("Included = %v, want empty", r2.Included)
	}
	if len(r2.Skipped) != 1 || r2.Skipped[0].Name != "partial" || r2.Skipped[0].Reason == "" {
		t.Fatalf("Skipped = %v, want one reasoned entry for partial", r2.Skipped)
	}
}

// 多台机器按名字排序，且与输入顺序无关：连续两次渲染逐字节一致（§32.1 确定性）。
func TestRenderIncludeSortedAndStable(t *testing.T) {
	zeta := sshTestMachine("zeta", `{"ssh":{"hostName":"z.internal"}}`)
	alpha := sshTestMachine("alpha", `{"ssh":{"hostName":"a.internal"}}`)
	mid := sshTestMachine("mid", `{"ssh":{"hostName":"m.internal"}}`)

	r1, err := RenderInclude([]*domain.Machine{zeta, alpha, mid}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	r2, err := RenderInclude([]*domain.Machine{zeta, alpha, mid}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	if r1.Content != r2.Content {
		t.Fatalf("render must be deterministic:\n--- first ---\n%s\n--- second ---\n%s", r1.Content, r2.Content)
	}
	wantOrder := []string{"alpha", "mid", "zeta"}
	if len(r1.Included) != 3 {
		t.Fatalf("Included = %v, want %v", r1.Included, wantOrder)
	}
	for i, name := range wantOrder {
		if r1.Included[i] != name {
			t.Fatalf("Included = %v, want %v", r1.Included, wantOrder)
		}
	}
	// 产物中 Host 段的出现顺序与 Included 一致。
	prev := -1
	for _, name := range wantOrder {
		idx := strings.Index(r1.Content, "Host "+name+"\n")
		if idx < 0 {
			t.Fatalf("Host %s missing from content:\n%s", name, r1.Content)
		}
		if idx < prev {
			t.Fatalf("Host blocks out of order in content:\n%s", r1.Content)
		}
		prev = idx
	}
	// 输入顺序变化不改变产物。
	r3, err := RenderInclude([]*domain.Machine{mid, zeta, alpha}, sshTestTime)
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	if r3.Content != r1.Content {
		t.Fatalf("content depends on input order:\n--- a ---\n%s\n--- b ---\n%s", r1.Content, r3.Content)
	}
}

// 注入与非法名：返回包 domain.ErrInvalid 的错误，且不产出任何 Content（FR-12.6）。
func TestRenderIncludeRejectsInjection(t *testing.T) {
	cases := []struct {
		name    string
		machine string
		spec    string
	}{
		{"newline in hostName", "devbox-01", `{"ssh":{"hostName":"devbox.internal\nUser evil"}}`},
		{"carriage return in hostName", "devbox-01", `{"ssh":{"hostName":"a\rb"}}`},
		{"newline in proxyJump", "devbox-01", `{"ssh":{"hostName":"a","proxyJump":"bastion\nHost evil"}}`},
		{"newline in identityFile", "devbox-01", `{"ssh":{"hostName":"a","identityFile":"/k\nHost evil"}}`},
		{"NUL in user", "devbox-01", `{"ssh":{"hostName":"a","user":"dev\u0000x"}}`},
		{"tab in user", "devbox-01", `{"ssh":{"hostName":"a","user":"dev\tx"}}`},
		{"space in machine name", "dev box", `{"ssh":{"hostName":"a"}}`},
		{"wildcard in machine name", "dev*", `{"ssh":{"hostName":"a"}}`},
		{"question mark in machine name", "dev?", `{"ssh":{"hostName":"a"}}`},
		{"negation in machine name", "!dev", `{"ssh":{"hostName":"a"}}`},
		{"comma in machine name", "a,b", `{"ssh":{"hostName":"a"}}`},
		{"newline in machine name", "dev\nHost evil", `{"ssh":{"hostName":"a"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RenderInclude([]*domain.Machine{sshTestMachine(tc.machine, tc.spec)}, sshTestTime)
			if err == nil {
				t.Fatalf("expected error, got content:\n%s", r.Content)
			}
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error must wrap domain.ErrInvalid, got %v", err)
			}
			if r != nil {
				t.Fatalf("no render may be returned on error, got content %q", r.Content)
			}
		})
	}
	// 拒绝发生在组装之前：同批次的合法机器也不会产出半截产物。
	good := sshTestMachine("good", `{"ssh":{"hostName":"good.internal"}}`)
	bad := sshTestMachine("bad", `{"ssh":{"hostName":"x\nUser evil"}}`)
	r, err := RenderInclude([]*domain.Machine{good, bad}, sshTestTime)
	if err == nil || r != nil {
		t.Fatalf("want error and nil render, got err=%v render=%+v", err, r)
	}
}

// spec 解析失败要返回明确错误，不静默跳过整台机器（FR-12.6）。
func TestRenderIncludeRejectsMalformedSpec(t *testing.T) {
	m := sshTestMachine("broken", `{"ssh":{"hostName":`)
	r, err := RenderInclude([]*domain.Machine{m}, sshTestTime)
	if err == nil {
		t.Fatalf("expected error, got content:\n%s", r.Content)
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error must wrap domain.ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error must name the machine, got %v", err)
	}
}

// 安装目标路径只计算、不写文件（FR-12.6、§13.5）。
func TestInstallTarget(t *testing.T) {
	if got, want := InstallTarget("/home/dev"), "/home/dev/.ssh/agent-fleet.conf"; got != want {
		t.Fatalf("InstallTarget = %q, want %q", got, want)
	}
	if got, want := InstallTarget("/root"), "/root/.ssh/agent-fleet.conf"; got != want {
		t.Fatalf("InstallTarget = %q, want %q", got, want)
	}
}
