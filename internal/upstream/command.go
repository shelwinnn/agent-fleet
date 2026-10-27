package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CommandQuerier 是 Querier 的生产实现：直接执行与上游 provider v1.5.0
// 相同的只读查询命令（npm → `npm view <pkg> version`，brew →
// `brew info --json=v2 [--cask] <pkg>`），命令只读上游注册表，
// 不触碰目标环境。
//
// 为什么不经由 Manager.GetLatestVersion（KM-48 实现裁决，有测试证据）：
// 上游对查询结果（**含错误**）有进程全局 5 分钟缓存（providers/
// latest_cache.go 的 npmLatestVersionCache / pip / brew 同构，包名做键，
// 无失效入口）。操作者点「检查更新」期望一次真实查询；经上游缓存时，
// 一次网络故障后的重试在 5 分钟内会把陈旧错误当新结果返回——「查不到」
// 与「没查」被混为一谈，违反 spec 的硬要求（永远分得清、记录查询时间）。
// 因此本层按 T1 窄适配纪律收窄：能力声明仍以上游 GetLatestVersion 的
// 覆盖面为准（见 capability.go），运行时查询由本层以相同命令、自带
// 超时/取消/错误分类地执行，结果永远新鲜。
type CommandQuerier struct {
	// lookPath 供测试注入；nil 用 exec.LookPath。
	lookPath func(string) (string, error)
	// brewOutput 供测试注入受控 brew 输出；nil 真实执行。签名参数为
	// (ctx, args)，返回 stdout。
	brewOutput func(ctx context.Context, args []string) ([]byte, error)
}

// NewCommandQuerier 创建查询器。
func NewCommandQuerier() *CommandQuerier { return &CommandQuerier{} }

// LatestVersion 实现 Querier。raw 是来源返回的原样版本串（去首尾空白）。
func (q *CommandQuerier) LatestVersion(ctx context.Context, def SourceDef) (string, error) {
	switch def.Method {
	case "npm":
		return q.npmLatest(ctx, def)
	case "brew":
		return q.brewLatest(ctx, def)
	default:
		// 能力门（Service.checkSource）应已拦截；这里兜底并给出明确原因。
		return "", fmt.Errorf("upstream: method %q has no registry query (declared %s)", def.Method, def.Query)
	}
}

// errNotInPath 标记「查询所需的包管理器不在本机 PATH」。分类走 errors.Is，
// 不做文案匹配（文案是给人看的，不是给分类器看的契约）。
var errNotInPath = errors.New("is not available in PATH")

func (q *CommandQuerier) lookup(name string) error {
	look := q.lookPath
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look(name); err != nil {
		return fmt.Errorf("%s %w: %w", name, errNotInPath, err)
	}
	return nil
}

func (q *CommandQuerier) npmLatest(ctx context.Context, def SourceDef) (string, error) {
	if err := q.lookup("npm"); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "npm", "view", def.Package, "version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("npm view %s: %w", def.Package, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// brewLatest 只走 cask 路径（codex 在上游 catalog 中标记 cask=true，
// 命令固定传 --cask，brew 只会回 casks 数组）。formula 语义（formulae[]
// .versions.stable）在出现 formula 来源之前不实现——留着不可达分支只会
// 制造「看起来支持」的假象。
func (q *CommandQuerier) brewLatest(ctx context.Context, def SourceDef) (string, error) {
	args := []string{"info", "--json=v2", "--cask", def.Package}
	var (
		out []byte
		err error
	)
	if q.brewOutput != nil {
		// 测试注入受控应答：整个执行环境被替换，PATH 门随之跳过。
		out, err = q.brewOutput(ctx, args)
	} else {
		if err := q.lookup("brew"); err != nil {
			return "", err
		}
		out, err = exec.CommandContext(ctx, "brew", args...).Output()
	}
	if err != nil {
		return "", fmt.Errorf("brew info %s: %w", def.Package, err)
	}
	var parsed struct {
		Casks []struct {
			Version string `json:"version"`
		} `json:"casks"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", fmt.Errorf("brew info %s: malformed json: %w", def.Package, err)
	}
	if len(parsed.Casks) > 0 && parsed.Casks[0].Version != "" {
		return strings.TrimSpace(parsed.Casks[0].Version), nil
	}
	return "", fmt.Errorf("brew info %s: no cask version in response", def.Package)
}

// classifyError 把查询错误分类并标注是否值得重试。分类依据全部是本层自己
// 的哨兵与 exec 的类型化错误——不做错误文案匹配（文案不是契约）。
func classifyError(err error) (FailureReason, bool) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonTimeout, true
	case errors.Is(err, context.Canceled):
		// 调用方放弃（操作者关页/请求中止）：不是「查得太慢」，单独成类。
		return ReasonCanceled, false
	case errors.Is(err, errNotInPath):
		// npm/brew 不在本机 PATH：换环境才能解决，重试无意义——这本身就是
		// 要展示给操作者的「适用性」事实。
		return ReasonSourceUnavailable, false
	}
	var lookErr *exec.Error
	if errors.As(err, &lookErr) {
		return ReasonSourceUnavailable, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// 查询命令非零退出：注册表不可达、包不存在等瞬时故障，值得一次
		// 有限重试。
		return ReasonQueryFailed, true
	}
	if strings.Contains(err.Error(), "no registry query") {
		return ReasonMethodUnsupported, false
	}
	return ReasonQueryFailed, true
}

func classifyReason(err error) FailureReason {
	r, _ := classifyError(err)
	return r
}
