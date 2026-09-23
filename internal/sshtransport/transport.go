// Package sshtransport 实现 SSH 受限执行器（架构 v1.1.2 §4.5、FR-12.1–FR-12.5）：
//
//   - 只接受**结构化参数**（alias、argv 数组、文件对），不接受拼接命令字符串；
//   - 本地进程直接 exec OpenSSH 二进制（不经 shell），远程命令由本包按 POSIX
//     引用规则构造单个字符串后交 `ssh <alias> -- <cmd>`；
//   - 探测：`ssh -G <alias>` 解析生效配置（HostName/User/Port/ProxyJump/IdentityFile）；
//   - 执行：`ssh -o BatchMode=yes -o ConnectTimeout=<cfg> <alias> -- <cmd>`；文件传输用 `scp`；
//   - 错误按 §30.1 六类分类，不坍缩为 unreachable；
//   - **绝不禁用 host-key 校验、绝不注入 `StrictHostKeyChecking=no`**（§13.3，
//     硬边界：本包不提供任何放宽校验的开关），也不摄取私钥内容（§13.2）。
package sshtransport

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 默认超时（§4.5/§28：connectTimeout=10s、commandTimeout=60s，可配置）。
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultCommandTimeout = 60 * time.Second
)

// Target 是 `ssh -G` 解析出的生效配置（§4.5 探测；也是 FR-12.6 include 导出的
// "Fleet 是否持有显式连接字段"的事实来源之一）。
type Target struct {
	Alias        string
	HostName     string
	User         string
	Port         int
	ProxyJump    string
	IdentityFile []string
	// Raw 是 `ssh -G` 的原始输出（诊断用；不含私钥内容，仅路径）。
	Raw string
}

// Config 是受限执行器参数。
type Config struct {
	// SSHBinary / SCPBinary 默认 "ssh" / "scp"（系统 OpenSSH，FR-12.1）。
	SSHBinary string
	SCPBinary string
	// ClientConfig 非空时作为 `-F <path>` 传给 ssh/scp：控制面专用客户端配置
	// （可 `Include ~/.ssh/agent-fleet.conf`）。为空 = 完全沿用系统默认行为
	// （保留操作者既有 Host/Include/ProxyJump/known_hosts/ssh-agent 选择）。
	ClientConfig string
	// ConnectTimeout / CommandTimeout 默认 10s / 60s。
	ConnectTimeout time.Duration
	CommandTimeout time.Duration
	Log            *slog.Logger
}

// Transport 是受限执行器。零值不可用，请用 New 构造。
type Transport struct {
	cfg Config
}

func New(cfg Config) *Transport {
	if cfg.SSHBinary == "" {
		cfg.SSHBinary = "ssh"
	}
	if cfg.SCPBinary == "" {
		cfg.SCPBinary = "scp"
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = DefaultCommandTimeout
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Transport{cfg: cfg}
}

// Result 是一次远端命令的结果。
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
	// Command 是实际下发的远端命令字符串（已按 POSIX 规则引用；仅含结构化参数，
	// 不含秘密值）。
	Command string
}

// QuoteArgs 把结构化 argv 按 POSIX shell 引用规则拼成**单个**命令字符串。
// 每个元素用单引号包裹、内部单引号转义为 '\”；空串也显式引用。这样远端
// shell 只会看到字面量参数，不可能因参数内容产生额外的解析（§4.5：远程参数
// 经专用工具安全引用）。
func QuoteArgs(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, quoteOne(a))
	}
	return strings.Join(parts, " ")
}

func quoteOne(s string) string {
	if s == "" {
		return "''"
	}
	// 安全字符集之外一律引用（保守：宁可多引号，不可漏引）。
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '/' || r == '.' || r == '_' || r == '-' || r == ':' || r == '@' || r == '+' || r == '=') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Resolve 执行 `ssh -G <alias>` 解析生效配置（§4.5 探测：HostName/User/Port/
// ProxyJump/IdentityFile）。失败按 §30.1 分类返回。
func (t *Transport) Resolve(ctx context.Context, alias string) (*Target, error) {
	if err := validateAlias(alias); err != nil {
		return nil, err
	}
	args := t.baseArgs()
	args = append(args, "-G", alias)
	out, err := t.exec(ctx, t.cfg.SSHBinary, args, "", t.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	if out.ExitCode != 0 {
		return nil, classify(out, false)
	}
	return parseSSHG(alias, string(out.Stdout))
}

// Probe 在远端执行一条只读探测命令并返回结果（§4.2：SSH 探测编排）。
func (t *Transport) Probe(ctx context.Context, alias string, argv []string) (Result, error) {
	return t.Run(ctx, alias, argv)
}

// Run 在远端执行 argv（结构化参数；远端命令字符串由 QuoteArgs 构造）。
// 本地进程直接 exec ssh，不经 shell（§4.5）。
func (t *Transport) Run(ctx context.Context, alias string, argv []string) (Result, error) {
	if err := validateAlias(alias); err != nil {
		return Result{}, err
	}
	if len(argv) == 0 {
		return Result{}, domain.Coded(domain.ReasonRemoteCommandFailed, "empty remote argv")
	}
	remote := QuoteArgs(argv)
	args := t.baseArgs()
	args = append(args, "-o", "BatchMode=yes", "-o",
		"ConnectTimeout="+strconv.Itoa(int(t.cfg.ConnectTimeout.Seconds())))
	args = append(args, alias, "--", remote)
	out, err := t.exec(ctx, t.cfg.SSHBinary, args, remote, t.cfg.CommandTimeout)
	if err != nil {
		return out, err
	}
	if out.ExitCode != 0 {
		return out, classify(out, false)
	}
	return out, nil
}

// Upload 用 scp 上传文件或目录（§4.5：文件传输用 scp）。
func (t *Transport) Upload(ctx context.Context, alias, local, remote string) error {
	return t.scp(ctx, alias, local, alias+":"+remote)
}

// Download 用 scp 下载文件或目录（方向与 Upload 相反：远端 → 本地）。
func (t *Transport) Download(ctx context.Context, alias, remote, local string) error {
	return t.scp(ctx, alias, alias+":"+remote, local)
}

// scp 执行一次传输。src/dst 是**完整操作数**：远端一侧已带 `<alias>:` 前缀。
func (t *Transport) scp(ctx context.Context, alias, src, dst string) error {
	if err := validateAlias(alias); err != nil {
		return err
	}
	if err := validateScpEndpoint(alias, src); err != nil {
		return err
	}
	if err := validateScpEndpoint(alias, dst); err != nil {
		return err
	}
	args := t.baseArgs()
	args = append(args, "-q", "-o", "BatchMode=yes", "-o",
		"ConnectTimeout="+strconv.Itoa(int(t.cfg.ConnectTimeout.Seconds())))
	args = append(args, "-r")
	// 远端操作数一律写成 <alias>:<path> 且 path 必须是绝对路径（本包只使用
	// staging 固定路径）。不含引号：现代 scp 走 SFTP 子系统，远端路径按字面量
	// 处理，不经远端 shell 解析（因此也不需要再引用）。
	args = append(args, src, dst)
	out, err := t.exec(ctx, t.cfg.SCPBinary, args, "", t.cfg.CommandTimeout)
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return classify(out, false)
	}
	return nil
}

// baseArgs 组装 ssh/scp 的公共参数。**不包含**任何放宽 host-key 校验的选项。
func (t *Transport) baseArgs() []string {
	var args []string
	if t.cfg.ClientConfig != "" {
		args = append(args, "-F", t.cfg.ClientConfig)
	}
	return args
}

// exec 直接 exec 二进制（不经 shell），带超时；返回结果与**分类后**的错误。
func (t *Transport) exec(ctx context.Context, bin string, args []string, remote string, timeout time.Duration) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...) // 结构化 argv：无本地 shell
	// 超时/取消时杀掉**整个进程组**：OpenSSH 可能带子进程（ProxyJump 的二级
	// ssh、ControlMaster），只杀父进程会留下孤儿继续持有管道，让等待无法返回。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	res := Result{
		Stdout: stdout.Bytes(), Stderr: stderr.Bytes(),
		Duration: time.Since(start), Command: remote,
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// 非零退出：交给 classify（可能来自连接阶段或远端命令本身）。
			res.ExitCode = ee.ExitCode()
			return res, classify(res, runCtx.Err() != nil)
		}
		// 起不来（二进制缺失）或超时被杀。
		if runCtx.Err() != nil {
			return res, classify(res, true)
		}
		return res, domain.Coded(domain.ReasonRemoteCommandFailed, "exec %s: %v", bin, err)
	}
	return res, nil
}

// classify 把 OpenSSH 的 stderr/退出码映射为 §30.1 六类之一（FR-12.5：不得坍缩
// 为 unreachable）。未识别的 255 归 RemoteCommandFailed（远端命令未成功完成），
// 非零但非 255 的退出码是远端命令自己的失败，同样归 RemoteCommandFailed。
func classify(res Result, timedOut bool) error {
	stderr := string(res.Stderr)
	lower := strings.ToLower(stderr)
	// 连接阶段类别只在"本地 ssh 自己失败、远端尚未产生任何输出"时采用：远端进程
	// 也可能把 "Host key verification failed." 之类的文本写进自己的 stderr 并退 255，
	// 那种情况远端命令**已经启动**，若按连接前失败归类就会把 Unknown 误判成 Failed
	// 并提前释放机器级互斥（§6.4/FR-15.4）。
	noRemoteOutput := len(res.Stdout) == 0
	switch {
	case noRemoteOutput && (strings.Contains(lower, "could not resolve hostname") ||
		strings.Contains(lower, "name or service not known") ||
		strings.Contains(lower, "nodename nor servname provided")):
		return domain.Coded(domain.ReasonDNSResolveFailed, "dns resolve failed: %s", snippet(stderr))
	case noRemoteOutput && (strings.Contains(lower, "host key verification failed") ||
		strings.Contains(lower, "remote host identification has changed") ||
		strings.Contains(lower, "no matching host key type found")):
		// host-key 失败必须显式呈现，绝不静默降级（§13.3/FR-12.3）。
		return domain.Coded(domain.ReasonHostKeyVerificationFailed, "host key verification failed: %s", snippet(stderr))
	case noRemoteOutput && (strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "authentication failed") ||
		strings.Contains(lower, "too many authentication failures") ||
		strings.Contains(lower, "no supported authentication methods")):
		return domain.Coded(domain.ReasonAuthenticationFailed, "authentication failed: %s", snippet(stderr))
	case noRemoteOutput && (strings.Contains(lower, "connection timed out") ||
		strings.Contains(lower, "operation timed out") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "connection closed by remote host")):
		return domain.Coded(domain.ReasonConnectionTimeout, "connection failed: %s", snippet(stderr))
	case timedOut:
		// 我们自己的超时：无输出时按连接阶段超时归类，否则按远端命令失败归类。
		if len(res.Stdout) == 0 {
			return domain.Coded(domain.ReasonConnectionTimeout, "ssh timed out after %s", res.Duration)
		}
		return domain.Coded(domain.ReasonRemoteCommandFailed, "remote command timed out after %s", res.Duration)
	default:
		return domain.Coded(domain.ReasonRemoteCommandFailed,
			"remote command failed (exit=%d): %s", res.ExitCode, snippet(stderr))
	}
}

// parseSSHG 解析 `ssh -G` 输出（小写键 + 值，逐行）。
func parseSSHG(alias, out string) (*Target, error) {
	t := &Target{Alias: alias, Raw: out}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(key) {
		case "hostname":
			t.HostName = val
		case "user":
			t.User = val
		case "port":
			if p, err := strconv.Atoi(val); err == nil {
				t.Port = p
			}
		case "proxyjump":
			if val != "none" {
				t.ProxyJump = val
			}
		case "identityfile":
			if val != "none" {
				t.IdentityFile = append(t.IdentityFile, val)
			}
		}
	}
	if t.HostName == "" {
		return nil, domain.Coded(domain.ReasonRemoteCommandFailed,
			"ssh -G %s produced no hostname", alias)
	}
	return t, nil
}

// validateAlias 拒绝形态非法的 alias（防止把选项注入 ssh 命令行：以 '-' 开头）。
func validateAlias(alias string) error {
	if alias == "" {
		return domain.Coded(domain.ReasonInvalid, "empty ssh alias")
	}
	if strings.HasPrefix(alias, "-") {
		return domain.Coded(domain.ReasonInvalid, "ssh alias must not start with '-': %q", alias)
	}
	if strings.ContainsAny(alias, " \t\n\r\x00") {
		return domain.Coded(domain.ReasonInvalid, "ssh alias contains whitespace: %q", alias)
	}
	return nil
}

// validateScpEndpoint 校验一个完整 scp 操作数：远端一侧（`<alias>:<path>`）的路径
// 必须是绝对路径（本包只使用 staging 固定路径），两侧都拒绝 '-' 前缀与控制字符。
func validateScpEndpoint(alias, operand string) error {
	path := operand
	if rest, ok := strings.CutPrefix(operand, alias+":"); ok {
		path = rest
		if !strings.HasPrefix(path, "/") {
			return domain.Coded(domain.ReasonInvalid, "remote scp path must be absolute: %q", path)
		}
	}
	return validateScpOperand(path)
}

// validateScpOperand 拒绝以 '-' 开头的操作数（scp 选项注入）与控制字符。
func validateScpOperand(p string) error {
	if p == "" {
		return domain.Coded(domain.ReasonInvalid, "empty scp operand")
	}
	if strings.HasPrefix(p, "-") {
		return domain.Coded(domain.ReasonInvalid, "scp operand must not start with '-': %q", p)
	}
	if strings.ContainsAny(p, "\n\r\x00") {
		return domain.Coded(domain.ReasonInvalid, "scp operand contains control characters")
	}
	return nil
}

// UnsupportedPlatformError 是 §30.1 的第六类：远端平台无法确定或不受支持
// （例如 agentd 二进制没有该 GOOS/GOARCH 的构建）。
func UnsupportedPlatformError(goos, goarch, detail string) error {
	return domain.Coded(domain.ReasonUnsupportedPlatform,
		"unsupported remote platform %s/%s: %s", goos, goarch, detail)
}

func snippet(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", "; ")
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
