package sshops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/bundle"
	"github.com/shelwinnn/agent-fleet/internal/controller/machine"
	"github.com/shelwinnn/agent-fleet/internal/controller/reconcile"
	"github.com/shelwinnn/agent-fleet/internal/domain"
	"github.com/shelwinnn/agent-fleet/internal/sshtransport"
	"github.com/shelwinnn/agent-fleet/internal/store/sqlite"
)

// 本文件是 SSH-only 闭环的**真实**集成测试（§13.1 SSH 集成测试夹具）：
// 起一个本地 sshd（真实 OpenSSH 服务端 + 真实 host key 校验），用系统 ssh/scp
// 走完整的 inventory → plan → 确认 → apply 闭环，并覆盖 bundle 校验失败、
// 基线变化拒绝、传输错误分类与 SSH-only 新鲜度三态。
//
// 环境要求：ssh / scp / sshd / ssh-keygen / go 可用。缺任一即 skip 并给出原因
// （不伪造通过）。远端 HOME 经 authorized_keys 的 environment="HOME=..." 指向
// 测试临时目录，因此节点侧状态完全落在临时目录内，不触碰真实 home。

const testAlias = "agent-fleet-ssh-test"

type sshEnv struct {
	t          *testing.T
	dir        string
	clientConf string
	remoteHome string
	agentdDir  string
	port       int
	knownHosts string
}

func requireBinaries(t *testing.T, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range names {
		p, err := exec.LookPath(n)
		if err != nil {
			t.Skipf("required binary %q not available: %v", n, err)
		}
		out[n] = p
	}
	return out
}

// startSSHEnv 起本地 sshd 并返回客户端配置（严格 host-key 校验）。
func startSSHEnv(t *testing.T) *sshEnv {
	t.Helper()
	bins := requireBinaries(t, "ssh", "scp", "ssh-keygen", "ssh-keyscan", "go")
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		if p, lerr := exec.LookPath("sshd"); lerr == nil {
			sshd = p
		} else {
			t.Skipf("sshd not available: %v", err)
		}
	}
	dir := t.TempDir()
	remoteHome := filepath.Join(dir, "remote-home")
	if err := os.MkdirAll(remoteHome, 0o700); err != nil {
		t.Fatal(err)
	}
	hostKey := filepath.Join(dir, "host_ed25519")
	clientKey := filepath.Join(dir, "id_ed25519")
	mustRun(t, bins["ssh-keygen"], "-q", "-t", "ed25519", "-N", "", "-f", hostKey, "-C", "fleet-sshd-host")
	mustRun(t, bins["ssh-keygen"], "-q", "-t", "ed25519", "-N", "", "-f", clientKey, "-C", "fleet-test-client")
	pub, err := os.ReadFile(clientKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	// 假 codex CLI：让适配器的版本探测看到"期望版本已安装"（§20.2 的 command
	// installer 尚未实现，版本变化必然失败，因此夹具把版本对齐为已安装态）。
	fakeBin := filepath.Join(dir, "fakebin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "codex"),
		[]byte("#!/bin/sh\necho \"codex-cli 0.154.0\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	authKeys := filepath.Join(dir, "authorized_keys")
	// environment="HOME=...","PATH=..." 让远端会话的 HOME 指向测试临时 home（节点侧
	// 状态全部落在临时目录），并让 PATH 能找到夹具的假 CLI。
	line := fmt.Sprintf("environment=\"HOME=%s\",environment=\"PATH=%s:/usr/bin:/bin\" %s",
		remoteHome, fakeBin, strings.TrimSpace(string(pub)))
	if err := os.WriteFile(authKeys, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	user := currentUser(t)
	sshdConf := filepath.Join(dir, "sshd_config")
	conf := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
StrictModes no
UsePAM no
PermitUserEnvironment yes
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin no
AllowUsers %s
# 现代 scp 走 SFTP 子系统（远端路径按字面量处理，不经远端 shell）。
Subsystem sftp internal-sftp
LogLevel ERROR
`, port, hostKey, filepath.Join(dir, "sshd.pid"), authKeys, user)
	if err := os.WriteFile(sshdConf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "sshd.log")
	cmd := exec.Command(sshd, "-f", sshdConf, "-E", logPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("sshd could not start in this environment (%v): %s", err, out)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	})
	// known_hosts：只信任这台 sshd 的真实 host key（host-key 校验保持开启）。
	knownHosts := filepath.Join(dir, "known_hosts")
	var scanned []byte
	for i := 0; i < 50; i++ {
		out, err := exec.Command(bins["ssh-keyscan"], "-p", strconv.Itoa(port), "-H", "127.0.0.1").Output()
		if err == nil && len(out) > 0 {
			scanned = out
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(scanned) == 0 {
		t.Fatalf("ssh-keyscan produced no host key; sshd log:\n%s", readFileOrEmpty(logPath))
	}
	if err := os.WriteFile(knownHosts, scanned, 0o600); err != nil {
		t.Fatal(err)
	}
	clientConf := filepath.Join(dir, "ssh_config")
	conf = fmt.Sprintf(`Host %s
  HostName 127.0.0.1
  Port %d
  User %s
  IdentityFile %s
  IdentitiesOnly yes
  UserKnownHostsFile %s
  StrictHostKeyChecking yes
`, testAlias, port, user, clientKey, knownHosts)
	if err := os.WriteFile(clientConf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	agentdDir := filepath.Join(dir, "agentd")
	if err := os.MkdirAll(agentdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	buildAgentd(t, filepath.Join(agentdDir, "agentd-linux-amd64"))
	return &sshEnv{t: t, dir: dir, clientConf: clientConf, remoteHome: remoteHome,
		agentdDir: agentdDir, port: port, knownHosts: knownHosts}
}

func (e *sshEnv) transport() *sshtransport.Transport {
	return sshtransport.New(sshtransport.Config{
		ClientConfig:   e.clientConf,
		ConnectTimeout: 5 * time.Second,
		CommandTimeout: 60 * time.Second,
		Log:            quietLog(),
	})
}

func (e *sshEnv) layout() sshtransport.RemoteLayout {
	l, err := sshtransport.NewRemoteLayout(e.remoteHome)
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

// ---- 控制面夹具 ----

type cpFixture struct {
	t          *testing.T
	env        *sshEnv
	machines   domain.MachineRepository
	status     domain.MachineStatusRepository
	profiles   domain.ProfileRepository
	skills     domain.SkillRepository
	snapshots  domain.SnapshotRepository
	ops        domain.OperationRepository
	observed   domain.ObservedStateRepository
	rec        *reconcile.Controller
	orch       *Orchestrator
	artifactID string
	contentHex string
}

func newCPFixture(t *testing.T, env *sshEnv, freshnessWindow time.Duration) *cpFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), quietLog()); err != nil {
		t.Fatal(err)
	}
	machines := sqlite.NewMachineStore(db)
	status := sqlite.NewMachineStatusStore(db)
	profiles := sqlite.NewProfileStore(db)
	skills := sqlite.NewSkillStore(db)
	providers := sqlite.NewProviderStore(db)
	snapshots := sqlite.NewSnapshotStore(db)
	ops := sqlite.NewOperationStore(db)
	observed := sqlite.NewObservedStateStore(db)
	machineCtl := machine.NewController(machines, status, observed, sqlite.NewAgentCertificateStore(db), quietLog())

	// 工件缓存：<root>/<contentDigest-hex>/SKILL.md（§4.7）
	contentHex := strings.Repeat("a1", 32)
	artifactsRoot := filepath.Join(dir, "artifacts", "skills")
	artDir := filepath.Join(artifactsRoot, contentHex)
	if err := os.MkdirAll(filepath.Join(artDir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "SKILL.md"), []byte("# tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 资源：Skill（已解析）→ Profile（引用它）→ Machine（SSH-only）。
	ctx := context.Background()
	skill := &domain.Skill{Metadata: domain.ObjectMeta{Name: "tool"}}
	skill.SetSpecJSON(json.RawMessage(`{"source":{"type":"local","path":"/srv/tool"}}`))
	skill.SetStatusJSON(json.RawMessage(`{"resolvedRevision":"rev-1","contentDigest":"sha256:` + contentHex + `"}`))
	if err := skills.Create(ctx, skill); err != nil {
		t.Fatal(err)
	}
	profile := &domain.AgentProfile{Metadata: domain.ObjectMeta{Name: "default-dev"}}
	profile.SetSpecJSON(json.RawMessage(`{"agents":{"codex":{"enabled":true,"version":"0.154.0",` +
		`"config":{"model":"gpt-5"}}},"skills":[{"name":"tool","skill":"tool"}]}`))
	if err := profiles.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	m := &domain.Machine{Metadata: domain.ObjectMeta{Name: "node-1"}}
	m.SetSpecJSON(json.RawMessage(`{"managementMode":"ssh","ssh":{"hostAlias":"` + testAlias + `"},"profileRef":"default-dev"}`))
	m.SetStatusJSON(json.RawMessage(`{}`))
	if err := machines.Create(ctx, m); err != nil {
		t.Fatal(err)
	}

	render := reconcile.NewRenderer(machines, profiles, skills, providers, "fixture/v1")
	rec := reconcile.NewController(machines, status, snapshots, ops, observed, render,
		reconcile.Config{PlanTimeout: 10 * time.Minute, FreshnessWindow: freshnessWindow,
			InventoryTimeout: 90 * time.Second}, quietLog())
	orch := New(Config{
		Transport: env.transport(), Machines: machines, Status: status, Ops: ops,
		Inventory: machineCtl, Artifacts: bundle.LocalArtifactSource{Root: artifactsRoot},
		AgentdBinDir: env.agentdDir, OperatorHome: t.TempDir(),
		GeneratedDir: filepath.Join(dir, "generated"), Log: quietLog(),
	})
	rec.SetSSHPlanner(orch)
	return &cpFixture{t: t, env: env, machines: machines, status: status, profiles: profiles,
		skills: skills, snapshots: snapshots, ops: ops, observed: observed, rec: rec, orch: orch,
		artifactID: "tool", contentHex: contentHex}
}

func (f *cpFixture) machineStatus() domain.MachineStatus {
	f.t.Helper()
	m, err := f.machines.Get(context.Background(), "node-1")
	if err != nil {
		f.t.Fatal(err)
	}
	st, err := domain.ParseMachineStatus(m.StatusJSON())
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *cpFixture) condition(name string) domain.Condition {
	f.t.Helper()
	st := f.machineStatus()
	c, ok := st.GetCondition(name)
	if !ok {
		f.t.Fatalf("condition %s not set; status=%+v", name, f.machineStatus())
	}
	return c
}

// ---- 闭环主用例 ----

func TestSSHOnlyClosedLoop(t *testing.T) {
	env := startSSHEnv(t)
	f := newCPFixture(t, env, time.Hour)
	ctx := context.Background()

	// 1) 探测：SSHReachable=True + homeDir 来自远端会话（真实 ssh -G + printenv）。
	if err := f.orch.ProbeAndRecord(ctx, "node-1"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	st := f.machineStatus()
	if c := f.condition(domain.ConditionSSHReachable); c.Status != domain.ConditionTrue {
		t.Fatalf("SSHReachable = %s (%s)", c.Status, c.Message)
	}
	if st.HomeDir != env.remoteHome {
		t.Fatalf("probed homeDir = %q, want %q", st.HomeDir, env.remoteHome)
	}
	if st.OS == "" || st.Arch == "" {
		t.Fatalf("probe did not record platform: %+v", st)
	}

	// 2) 尚无期望代 → drift 未定义（不写条件，而不是写 False 冒充"一致"）。
	if d, err := f.rec.EvaluateDrift(ctx, "node-1"); err != nil || d != nil {
		t.Fatalf("drift before any desired generation must be undefined, got %+v err=%v", d, err)
	}
	// 物化第一代期望后、尚未采集任何观测 → Drifted=Unknown(NeverInventoried)（§6.2 三态之一）。
	if _, _, err := f.rec.EnsureSnapshot(ctx, "node-1"); err != nil {
		t.Fatalf("materialize snapshot: %v", err)
	}
	drift, err := f.rec.EvaluateDrift(ctx, "node-1")
	if err != nil {
		t.Fatalf("evaluate drift: %v", err)
	}
	if drift.DriftStatus != domain.ConditionUnknown || drift.DriftReason != domain.DriftReasonNeverInventoried {
		t.Fatalf("want Unknown(NeverInventoried), got %s(%s)", drift.DriftStatus, drift.DriftReason)
	}

	// 3) 手动 inventory（POST /machines/{id}/inventory 的控制器路径）：readOnly 操作。
	invOp, err := f.rec.RequestInventory(ctx, "node-1")
	if err != nil {
		t.Fatalf("request inventory: %v", err)
	}
	if !invOp.Spec.ReadOnly || invOp.Spec.Type != domain.OperationTypeAutoPlan {
		t.Fatalf("inventory op must be readOnly AutoPlan: %+v", invOp.Spec)
	}
	if invOp.Status.Phase != domain.OperationPhaseSucceeded {
		t.Fatalf("inventory op phase = %s (%s) steps=%s", invOp.Status.Phase, invOp.Status.TerminalModifier,
			describeSteps(t, f, invOp.Metadata.Name))
	}
	if invOp.Spec.PlanDigest == "" {
		t.Fatal("AutoPlan operation must record the plan digest it produced")
	}
	st = f.machineStatus()
	if st.LastInventoryAt == nil || st.InventorySeq == 0 {
		t.Fatalf("inventory not recorded on machine status: %+v", st)
	}
	if c := f.condition(domain.ConditionInventoryReady); c.Status != domain.ConditionTrue {
		t.Fatalf("InventoryReady = %s", c.Status)
	}
	// 采集后 → 有观测，drift 求值不再 Unknown(NeverInventoried)。
	drift, err = f.rec.EvaluateDrift(ctx, "node-1")
	if err != nil {
		t.Fatalf("evaluate drift: %v", err)
	}
	if drift.DriftReason == domain.DriftReasonNeverInventoried {
		t.Fatalf("observation should exist now: %+v", drift)
	}

	// 4) reconcile → 产出计划并等待确认（SSH 路径的 AwaitingConfirmation 生产入口）。
	planOp, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile(plan): %v", err)
	}
	if planOp.Status.Phase != domain.OperationPhaseAwaitingConfirmation {
		t.Fatalf("phase = %s, want AwaitingConfirmation (steps=%s)",
			planOp.Status.Phase, describeSteps(t, f, planOp.Metadata.Name))
	}
	if planOp.Spec.PlanDigest == "" || planOp.Status.PlanExpiresAt == nil {
		t.Fatalf("plan digest/expiry missing: %+v", planOp)
	}
	if planOp.Spec.Transport != domain.TransportSSH {
		t.Fatalf("transport = %s, want ssh", planOp.Spec.Transport)
	}
	// 确认窗口内：新的变更类操作被 409 MachineBusy 拒绝（§9.3 第 1 条 /
	// 第 5 片联调里因"无节点在场"被跳过的分支，这里用真实数据闭合）。
	if _, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{}); err == nil {
		t.Fatal("second reconcile during confirmation window must be rejected with MachineBusy")
	} else if !errors.Is(err, domain.ErrMachineBusy) {
		t.Fatalf("want MachineBusy, got %v", err)
	}
	// 确认对象是**具体计划**：错误的摘要必须被拒（ErrReplanRequired）。
	if _, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{
		ConfirmPlanDigest: "sha256:" + strings.Repeat("0", 64)}); err == nil {
		t.Fatal("confirming a different planDigest must be rejected")
	}

	// 5) 用真实 planDigest 确认 → apply（节点侧重取观测、重算 plan、比对基线）。
	applied, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{
		ConfirmPlanDigest: planOp.Spec.PlanDigest})
	if err != nil {
		t.Fatalf("reconcile(apply): %v", err)
	}
	if applied.Status.Phase != domain.OperationPhaseSucceeded {
		t.Fatalf("apply phase = %s (%s: %s)", applied.Status.Phase, applied.Status.TerminalModifier,
			describeSteps(t, f, applied.Metadata.Name))
	}
	if applied.Status.Verify == nil {
		t.Fatal("succeeded apply must carry verify evidence")
	}
	if applied.Status.Verify.DesiredProjectionDigest != applied.Status.Verify.ObservedProjectionDigest {
		t.Fatalf("verify evidence not converged: %+v", applied.Status.Verify)
	}

	// 6) 节点侧真实产物：受管配置文件与 Skill 软链都落在远端 home 下。
	cfgPath := filepath.Join(env.remoteHome, ".codex", "config.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("managed config not written on node: %v", err)
	}
	if !strings.Contains(string(raw), "gpt-5") {
		t.Fatalf("managed config missing desired model:\n%s", raw)
	}
	link := filepath.Join(env.remoteHome, ".codex", "skills", "tool")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("skill link not created: %v", err)
	}
	wantTarget := filepath.Join(env.remoteHome, ".local", "share", "agent-fleet", "skills", "tool",
		"sha256-"+f.contentHex)
	if target != wantTarget {
		t.Fatalf("skill link target = %s, want %s", target, wantTarget)
	}
	if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
		t.Fatalf("bundle artifact not materialized on node: %v", err)
	}

	// 7) 条件写入：成功后 Drifted=False + Reconciled=True（§6.2 门禁证据齐备）。
	if c := f.condition(domain.ConditionDrifted); c.Status != domain.ConditionFalse {
		t.Fatalf("Drifted = %s (%s)", c.Status, c.Message)
	}
	if c := f.condition(domain.ConditionReconciled); c.Status != domain.ConditionTrue {
		t.Fatalf("Reconciled = %s (%s)", c.Status, c.Message)
	}

	// 8) 清理：成功后节点 staging 中的 bundle 已按 §4.5 规则 3 清掉。
	if _, err := os.Stat(filepath.Join(env.remoteHome, ".local/share/agent-fleet/staging/bundle")); !os.IsNotExist(err) {
		t.Fatalf("bundle staging should be cleaned after a confirmed run (err=%v)", err)
	}

	// 9) 幂等：同一 operationId 的 apply 重放直接回放既有结果，不再跑第二条流水线。
	out, err := env.transport().Run(ctx, testAlias, []string{
		filepath.Join(env.remoteHome, ".local/share/agent-fleet/staging/bin/agentd-"+agentdDigest(t, env)),
		"oneshot", "apply", "--bundle", filepath.Join(env.remoteHome, ".local/share/agent-fleet/staging/bundle"),
		"--operation-id", applied.Metadata.Name, "--staging", env.layout().Root(),
		"--home", env.remoteHome,
	})
	if err == nil {
		var replay struct {
			OperationID string `json:"operationId"`
			Phase       string `json:"phase"`
		}
		if jerr := json.Unmarshal(out.Stdout, &replay); jerr != nil {
			t.Fatalf("replay output unparseable: %v (%s)", jerr, out.Stdout)
		}
		if replay.OperationID != applied.Metadata.Name || replay.Phase != domain.OperationPhaseSucceeded {
			t.Fatalf("replay = %+v, want the recorded terminal result", replay)
		}
	} else {
		t.Fatalf("idempotent replay failed: %v", err)
	}

	// 10) drift 检出（真实数据闭合第 5 片 it.skip 的 drift=drifted 分支）。
	if err := os.WriteFile(cfgPath, []byte("model = \"someone-elses-model\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rec.RequestInventory(ctx, "node-1"); err != nil {
		t.Fatalf("inventory after drift: %v", err)
	}
	drift, err = f.rec.EvaluateDrift(ctx, "node-1")
	if err != nil {
		t.Fatalf("evaluate drift: %v", err)
	}
	if drift.DriftStatus != domain.ConditionTrue {
		t.Fatalf("want Drifted=True after local edit, got %s(%s)", drift.DriftStatus, drift.DriftReason)
	}
}

// TestSSHApplyRejectsChangedBaseline：plan 与 apply 之间本地受管文件被改动 →
// apply 零变更、报 ReplanRequired（FR-12.7 的核心契约）。
func TestSSHApplyRejectsChangedBaseline(t *testing.T) {
	env := startSSHEnv(t)
	f := newCPFixture(t, env, time.Hour)
	ctx := context.Background()
	if err := f.orch.ProbeAndRecord(ctx, "node-1"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	planOp, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile(plan): %v", err)
	}
	// 计划产出后，本地文件被"别人"改动（外部编辑）。
	cfgPath := filepath.Join(env.remoteHome, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("model = \"external-edit\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{
		ConfirmPlanDigest: planOp.Spec.PlanDigest})
	if err != nil {
		t.Fatalf("reconcile(apply): %v", err)
	}
	if applied.Status.Phase != domain.OperationPhaseFailed {
		t.Fatalf("phase = %s, want Failed", applied.Status.Phase)
	}
	got := describeSteps(t, f, applied.Metadata.Name)
	if !strings.Contains(got, domain.ReasonReplanRequired) {
		t.Fatalf("want %s in operation steps, got: %s", domain.ReasonReplanRequired, got)
	}
	// 零变更：外部编辑内容原样保留（没有被 apply 覆盖）。
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "external-edit") {
		t.Fatalf("apply must not have written anything; file is now:\n%s", raw)
	}
}

// TestSSHBundleVerificationRejectsTamperedBundle：节点侧 bundle 校验失败即拒绝
// 执行（§7.4 安全点 2/5、FR-12.8），并给出 §30 的 reason code。
func TestSSHBundleVerificationRejectsTamperedBundle(t *testing.T) {
	env := startSSHEnv(t)
	f := newCPFixture(t, env, time.Hour)
	ctx := context.Background()
	if err := f.orch.ProbeAndRecord(ctx, "node-1"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	planOp, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile(plan): %v", err)
	}
	layout := env.layout()
	// 篡改远端 bundle 内的工件内容（模拟传输层之外的替换）。
	artifactDir := filepath.Join(layout.BundleDir(), "artifacts", "skills")
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		t.Fatalf("remote bundle missing: %v", err)
	}
	target := filepath.Join(artifactDir, entries[0].Name(), "SKILL.md")
	if err := os.WriteFile(target, []byte("# tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentd := filepath.Join(layout.BinDir(), "agentd-"+agentdDigest(t, env))
	out, err := env.transport().Run(ctx, testAlias, []string{agentd, "oneshot", "apply",
		"--bundle", layout.BundleDir(), "--operation-id", planOp.Metadata.Name,
		"--staging", layout.Root(), "--home", env.remoteHome,
		"--plan-digest", planOp.Spec.PlanDigest})
	if err == nil {
		t.Fatalf("tampered bundle must be rejected; stdout=%s", out.Stdout)
	}
	var res struct {
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
	}
	if jerr := json.Unmarshal(out.Stdout, &res); jerr != nil {
		t.Fatalf("result unparseable: %v (%s)", jerr, out.Stdout)
	}
	if res.Reason != domain.ReasonSkillDigestMismatch {
		t.Fatalf("reason = %q, want %s", res.Reason, domain.ReasonSkillDigestMismatch)
	}
	if res.Phase != domain.OperationPhaseFailed {
		t.Fatalf("phase = %q, want Failed", res.Phase)
	}
}

// TestSSHPathEscapeRejected：恶意 bundle（路径越界）被节点拒绝整包（§7.4 安全点 3）。
// 夹具的 manifest 会被**重新封装**（bundle.Seal）成摘要正确的形态，这样唯一可能
// 触发拒绝的就是路径校验本身——而不是先被 bundle 摘要校验拦下（核查意见：上一版
// 接受 SkillDigestMismatch 等于没测路径拒绝）。
func TestSSHPathEscapeRejected(t *testing.T) {
	env := startSSHEnv(t)
	f := newCPFixture(t, env, time.Hour)
	ctx := context.Background()
	if err := f.orch.ProbeAndRecord(ctx, "node-1"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	// 先正常 plan 一次：既上传 bundle 也把临时 agentd 放到 staging。
	planOp, err := f.rec.Reconcile(ctx, "node-1", reconcile.ReconcileRequest{})
	if err != nil {
		t.Fatalf("reconcile(plan): %v", err)
	}
	layout := env.layout()
	bundleDir := layout.BundleDir()

	// 真实 bundle 的 machine/generation/snapshot（让夹具只保留"路径越界"这一处差异）。
	realRaw, err := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var real bundle.Manifest
	if err := json.Unmarshal(realRaw, &real); err != nil {
		t.Fatal(err)
	}
	fixtureRaw, err := os.ReadFile(filepath.Join("..", "..", "bundle", "testdata", "bundles",
		"malicious-path-relative", "manifest.json"))
	if err != nil {
		t.Skipf("malicious fixture unavailable: %v", err)
	}
	var evil bundle.Manifest
	if err := json.Unmarshal(fixtureRaw, &evil); err != nil {
		t.Fatal(err)
	}
	evil.Machine = real.Machine
	evil.Generation = real.Generation
	evil.SnapshotDigest = real.SnapshotDigest
	evil.Snapshot = real.Snapshot
	evil.SchemaVersion = real.SchemaVersion
	// 重封：摘要正确，路径仍然是越界的。
	if err := bundle.Seal(bundleDir, &evil); err != nil {
		t.Fatal(err)
	}

	agentd := filepath.Join(layout.BinDir(), "agentd-"+agentdDigest(t, env))
	out, err := env.transport().Run(ctx, testAlias, []string{agentd, "oneshot", "plan",
		"--bundle", bundleDir, "--operation-id", planOp.Metadata.Name,
		"--staging", layout.Root(), "--home", env.remoteHome})
	if err == nil {
		t.Fatalf("path-escaping bundle must be rejected; stdout=%s", out.Stdout)
	}
	var res struct {
		Phase  string `json:"phase"`
		Reason string `json:"reason"`
	}
	if jerr := json.Unmarshal(out.Stdout, &res); jerr != nil {
		t.Fatalf("result unparseable: %v (%s)", jerr, out.Stdout)
	}
	if res.Reason != domain.ReasonSkillPathRejected {
		t.Fatalf("reason = %q, want %s (bundle digest was re-sealed, so only the path check can fire)",
			res.Reason, domain.ReasonSkillPathRejected)
	}
	if res.Phase != domain.OperationPhaseFailed {
		t.Fatalf("phase = %q, want Failed", res.Phase)
	}
}

// TestSSHTransportErrorClassification：§30.1 六类不得坍缩（FR-12.5）。
func TestSSHTransportErrorClassification(t *testing.T) {
	env := startSSHEnv(t)
	dir := t.TempDir()
	writeConf := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := "  User " + currentUser(t) + "\n  IdentityFile " + filepath.Join(env.dir, "id_ed25519") +
		"\n  IdentitiesOnly yes\n  UserKnownHostsFile " + env.knownHosts + "\n  StrictHostKeyChecking yes\n"

	t.Run("host-key-mismatch", func(t *testing.T) {
		// 用另一个 host key 写 known_hosts → 校验必须失败（绝不放宽校验，§13.3）。
		otherKey := filepath.Join(dir, "other_host")
		mustRun(t, "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", otherKey, "-C", "other")
		pub, err := os.ReadFile(otherKey + ".pub")
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(string(pub))
		bad := filepath.Join(dir, "bad_known_hosts")
		line := fmt.Sprintf("[127.0.0.1]:%d %s %s\n", env.port, fields[0], fields[1])
		if err := os.WriteFile(bad, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		conf := writeConf("bad_hostkey.conf", "Host bad\n  HostName 127.0.0.1\n  Port "+
			strconv.Itoa(env.port)+"\n"+strings.Replace(base, env.knownHosts, bad, 1))
		tr := sshtransport.New(sshtransport.Config{ClientConfig: conf, CommandTimeout: 20 * time.Second})
		_, err = tr.Run(context.Background(), "bad", []string{"true"})
		if got := domain.ReasonOf(err); got != domain.ReasonHostKeyVerificationFailed {
			t.Fatalf("want %s, got %s (%v)", domain.ReasonHostKeyVerificationFailed, got, err)
		}
	})

	t.Run("dns-resolution", func(t *testing.T) {
		conf := writeConf("dns.conf", "Host nodns\n  HostName no-such-host.invalid\n  Port 22\n"+base)
		tr := sshtransport.New(sshtransport.Config{ClientConfig: conf, ConnectTimeout: 5 * time.Second})
		_, err := tr.Run(context.Background(), "nodns", []string{"true"})
		if got := domain.ReasonOf(err); got != domain.ReasonDNSResolveFailed {
			t.Fatalf("want %s, got %s (%v)", domain.ReasonDNSResolveFailed, got, err)
		}
	})

	t.Run("connection-refused", func(t *testing.T) {
		closed := freePort(t)
		conf := writeConf("closed.conf", "Host closed\n  HostName 127.0.0.1\n  Port "+
			strconv.Itoa(closed)+"\n"+base)
		tr := sshtransport.New(sshtransport.Config{ClientConfig: conf, ConnectTimeout: 5 * time.Second})
		_, err := tr.Run(context.Background(), "closed", []string{"true"})
		if got := domain.ReasonOf(err); got != domain.ReasonConnectionTimeout {
			t.Fatalf("want %s, got %s (%v)", domain.ReasonConnectionTimeout, got, err)
		}
	})
}

// TestSSHOnlyFreshnessTriState：SSH-only 新鲜度三态用真实观测闭合（§6.2/FR-1.9）。
func TestSSHOnlyFreshnessTriState(t *testing.T) {
	env := startSSHEnv(t)
	f := newCPFixture(t, env, 50*time.Millisecond) // 极小窗口以便断言 StaleObservation
	ctx := context.Background()
	if err := f.orch.ProbeAndRecord(ctx, "node-1"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	invOp, err := f.rec.RequestInventory(ctx, "node-1")
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if invOp.Status.Phase != domain.OperationPhaseSucceeded {
		t.Fatalf("autoplan op failed: %s", describeSteps(t, f, invOp.Metadata.Name))
	}
	// 只读流水线给出了真实双侧投影摘要：尚未 apply → Drifted=True（真实数据，
	// 不是"从未采集"也不是"不可比"）。
	drift, err := f.rec.EvaluateDrift(ctx, "node-1")
	if err != nil {
		t.Fatalf("evaluate drift: %v", err)
	}
	if drift.DriftStatus != domain.ConditionTrue {
		t.Fatalf("want Drifted=True before apply, got %s(%s)", drift.DriftStatus, drift.DriftReason)
	}
	// 让观测超出新鲜度窗口 → Drifted/Reconciled 置 Unknown(StaleObservation)。
	time.Sleep(120 * time.Millisecond)
	if err := f.rec.ScanFreshness(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("freshness scan: %v", err)
	}
	c := f.condition(domain.ConditionDrifted)
	if c.Status != domain.ConditionUnknown || c.Reason != domain.DriftReasonStaleObservation {
		t.Fatalf("want Unknown(StaleObservation), got %s(%s)", c.Status, c.Reason)
	}
	// 期望代前进（profile 改动）而观测仍是旧代 → Unknown(ObservationPredatesDesired)。
	profile, err := f.profiles.Get(ctx, "default-dev")
	if err != nil {
		t.Fatal(err)
	}
	profile.SetSpecJSON(json.RawMessage(`{"agents":{"codex":{"enabled":true,"version":"0.154.0",` +
		`"config":{"model":"gpt-5.1"}}},"skills":[{"name":"tool","skill":"tool"}]}`))
	if err := f.profiles.Update(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.rec.EnsureSnapshot(ctx, "node-1"); err != nil {
		t.Fatalf("materialize new generation: %v", err)
	}
	if _, err := f.rec.EvaluateDrift(ctx, "node-1"); err != nil {
		t.Fatalf("evaluate drift: %v", err)
	}
	c = f.condition(domain.ConditionDrifted)
	if c.Status != domain.ConditionUnknown || c.Reason != domain.DriftReasonObservationPredatesDesir {
		t.Fatalf("want Unknown(ObservationPredatesDesired), got %s(%s)", c.Status, c.Reason)
	}
}

// TestOpenSSHIncludeRenderAndInstall：FR-12.6 的三种动作（preview / export / 显式确认安装）。
func TestOpenSSHIncludeRenderAndInstall(t *testing.T) {
	env := startSSHEnv(t)
	f := newCPFixture(t, env, time.Hour)
	ctx := context.Background()
	// 一台只有 hostAlias 的机器（本夹具）+ 一台显式连接字段的机器。
	extra := &domain.Machine{Metadata: domain.ObjectMeta{Name: "devbox-01"}}
	extra.SetSpecJSON(json.RawMessage(`{"managementMode":"agentd","profileRef":"default-dev",` +
		`"ssh":{"hostName":"devbox.internal","user":"dev","port":2222,"proxyJump":"bastion"}}`))
	extra.SetStatusJSON(json.RawMessage(`{}`))
	if err := f.machines.Create(ctx, extra); err != nil {
		t.Fatal(err)
	}
	render, err := f.orch.RenderInclude(ctx)
	if err != nil {
		t.Fatalf("render include: %v", err)
	}
	if len(render.Included) != 1 || render.Included[0] != "devbox-01" {
		t.Fatalf("included hosts = %v, want [devbox-01]", render.Included)
	}
	if !strings.Contains(render.Content, "Host devbox-01") ||
		!strings.Contains(render.Content, "ProxyJump bastion") {
		t.Fatalf("unexpected include content:\n%s", render.Content)
	}
	if strings.Contains(render.Content, testAlias) {
		t.Fatalf("hostAlias-only machine must not be exported:\n%s", render.Content)
	}
	privateKeyContent, _ := os.ReadFile(filepath.Join(env.dir, "id_ed25519"))
	if strings.Contains(render.Content, strings.TrimSpace(string(privateKeyContent))) {
		t.Fatal("include file must never contain private key material")
	}
	// 安装必须显式确认。
	if _, err := f.orch.InstallInclude(ctx, false); err == nil {
		t.Fatal("install without confirmation must fail")
	}
	path, err := f.orch.InstallInclude(ctx, true)
	if err != nil {
		t.Fatalf("install include: %v", err)
	}
	if path != filepath.Join(f.orch.cfg.OperatorHome, ".ssh", "agent-fleet.conf") {
		t.Fatalf("install path = %s", path)
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != render.Content {
		t.Fatal("installed content differs from preview")
	}
}

// ---- 工具 ----

func buildAgentd(t *testing.T, out string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/agent-fleet-agentd")
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agentd: %v\n%s", err, b)
	}
}

func agentdDigest(t *testing.T, env *sshEnv) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(env.remoteHome, ".local/share/agent-fleet/staging/bin/agentd-*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("temporary agentd not found in staging: %v", err)
	}
	return strings.TrimPrefix(filepath.Base(matches[0]), "agentd-")
}

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func currentUser(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readFileOrEmpty(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// describeSteps 汇总操作的步骤审计（失败诊断：reason code 记在 steps 里）。
func describeSteps(t *testing.T, f *cpFixture, opID string) string {
	t.Helper()
	steps, err := f.ops.ListSteps(context.Background(), opID)
	if err != nil {
		return "steps unreadable: " + err.Error()
	}
	var sb strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&sb, "[%s %s %s] ", s.Name, s.Phase, s.Output)
	}
	return sb.String()
}
