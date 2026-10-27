package upstream

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ResultStatus 是一个来源检查的终态。三态语义（spec User Story 14）：
// 永远分得清「查到了」与「没查到」——只有 StatusOK 携带版本；
// failed/unsupported 一律带 Failure，并可附最近一次成功缓存。
type ResultStatus string

const (
	StatusOK          ResultStatus = "ok"
	StatusFailed      ResultStatus = "failed"
	StatusUnsupported ResultStatus = "unsupported"
)

// FailureReason 是失败/不可用的机器可读分类。前端据此渲染「未知 ≠ 最新」。
type FailureReason string

const (
	// ReasonQueryFailed：外部查询命令失败（注册表不可达、包不存在等）。
	ReasonQueryFailed FailureReason = "query_failed"
	// ReasonTimeout：查询超时（达到单次超时或重试耗尽）。
	ReasonTimeout FailureReason = "timeout"
	// ReasonSourceUnavailable：查询所需的包管理器不在本机 PATH（npm/brew 缺失）。
	ReasonSourceUnavailable FailureReason = "source_unavailable"
	// ReasonParseFailed：来源返回了不可解释的版本串——显式上报，不静默降级。
	ReasonParseFailed FailureReason = "version_parse_failed"
	// ReasonMethodUnsupported：上游不支持该安装方式的版本查询。
	ReasonMethodUnsupported FailureReason = "method_unsupported"
	// ReasonChannelUnsupported：请求了上游查询不到的非默认渠道。
	ReasonChannelUnsupported FailureReason = "channel_unsupported"
)

// Failure 是一次失败/不可用的记录。Detail 保留上游错误原文（不含敏感值——
// npm view / brew info 的报错只含包名与退出码）。
type Failure struct {
	Reason   FailureReason `json:"reason"`
	Detail   string        `json:"detail"`
	Attempts int           `json:"attempts"`
}

// Querier 是上游版本查询的接缝。生产实现是 CommandQuerier（与上游 provider
// 相同的只读命令、自带超时与错误分类）；测试注入受控版本源。
// 实现约定：错误时返回空串；raw 必须是来源返回的原样字符串。
type Querier interface {
	LatestVersion(ctx context.Context, def SourceDef) (string, error)
}

// Config 是发现服务的装配参数；零值字段取对应 Default* 常量。
type Config struct {
	// QueryTimeout 是单次外部查询的超时上限。
	QueryTimeout time.Duration
	// MaxAttempts 是单来源 Check 的总尝试上限（≥1；1 = 不重试）。
	MaxAttempts int
	// RetryBackoff 是可重试失败之间的固定间隔。
	RetryBackoff time.Duration
	// Now 供测试注入时钟；nil 用 time.Now。
	Now func() time.Time
}

// Service 是独立于 drift 的上游版本发现服务。它只读上游注册表，
// 不触碰目标环境（无安装/卸载/宿主包管理器探测路径）。
type Service struct {
	cfg Config
	q   Querier

	mu     sync.Mutex
	lastOK map[cacheKey]cachedSuccess
}

type cacheKey struct {
	agent, method, channel string
}

// cachedSuccess 是最近一次成功查询（含取得时间）。只在失败回退时对调用方
// 可见，并且永远带着 queriedAt——缓存不会伪装成新查询。
type cachedSuccess struct {
	version   Version
	queriedAt time.Time
}

// New 创建发现服务。
func New(cfg Config, q Querier) *Service {
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = DefaultQueryTimeout
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = DefaultRetryBackoff
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: q, lastOK: make(map[cacheKey]cachedSuccess)}
}

// SourceResult 是一个来源的一次检查结果。字段同时覆盖能力声明（来源/渠道/
// 适用平台）与本次查询记录（状态/版本/时间/失败/缓存回退）。
type SourceResult struct {
	AgentID string `json:"agentId"`
	Method  string `json:"method"`
	Package string `json:"package,omitempty"`
	Channel string `json:"channel"`
	// Platforms 是该来源的适用平台与证据（来自 T1 能力矩阵的静态声明）。
	Platforms []PlatformApplicability `json:"platforms"`
	Status    ResultStatus            `json:"status"`
	// Version 仅在 Status==ok 时非零。Raw 是来源原样返回值，Scheme 显式
	// 标注解释方式（semver / source_rule）——调用方不得自行猜测。
	Version *Version `json:"version,omitempty"`
	// QueriedAt 是本次成功查询的完成时间（Status==ok 时必填）。
	QueriedAt *time.Time `json:"queriedAt,omitempty"`
	// Attempts 是实际发起的查询次数（Status==unsupported 时为 0）。
	Attempts int `json:"attempts"`
	// Failure 在 failed/unsupported 时非空。
	Failure *Failure `json:"failure,omitempty"`
	// LastSuccess 仅在 failed 时可能出现：最近一次成功缓存及其取得时间。
	// 为空表示该来源从未成功查询过——前端应显示「未知」，不得显示「最新」。
	LastSuccess *CachedVersion `json:"lastSuccess,omitempty"`
}

// CachedVersion 是缓存回退中的版本与时间（JSON 形态与 Version 字段一致）。
type CachedVersion struct {
	Version   Version   `json:"version"`
	QueriedAt time.Time `json:"queriedAt"`
}

// Report 是一次「检查更新」的完整结果：该 agent 全部已声明来源的逐源记录。
type Report struct {
	AgentID   string         `json:"agent"`
	CheckedAt time.Time      `json:"checkedAt"`
	Sources   []SourceResult `json:"sources"`
}

// Check 对指定 agent 的全部已声明来源各做一次上游查询（操作者点一次
// 「检查更新」）。未知 agent 返回错误（HTTP 404 由调用方映射）。
func (s *Service) Check(ctx context.Context, agentID string) (Report, error) {
	defs := SourcesFor(agentID)
	if len(defs) == 0 {
		return Report{}, fmt.Errorf("upstream: unknown agent %q", agentID)
	}
	report := Report{AgentID: agentID, CheckedAt: s.cfg.Now(), Sources: make([]SourceResult, 0, len(defs))}
	for _, def := range defs {
		report.Sources = append(report.Sources, s.checkSource(ctx, def))
	}
	return report, nil
}

// checkSource 是单来源的检查：能力门（不支持的来源不发起查询）→
// 带超时与有限重试的外部查询 → 版本解释（解析失败显式上报）→ 缓存回退。
func (s *Service) checkSource(ctx context.Context, def SourceDef) SourceResult {
	res := SourceResult{
		AgentID: def.AgentID, Method: def.Method, Package: def.Package,
		Channel: def.Channel, Platforms: def.Platforms,
	}
	// 能力门 1：上游不支持该方式的版本查询（binary/native/...）。
	// 如实报告为 unsupported——不从来源清单上抹掉，也不伪装成查询失败。
	if def.Query != QuerySupported {
		res.Status = StatusUnsupported
		res.Failure = &Failure{
			Reason:   ReasonMethodUnsupported,
			Detail:   def.QueryUnsupportedReason(),
			Attempts: 0,
		}
		return res
	}
	// 能力门 2：非默认渠道。上游 GetLatestVersion 没有渠道参数，查不了
	// dist-tag / homebrew 版本渠道——声明边界而不是假装查过。
	if !def.DefaultChannel {
		res.Status = StatusUnsupported
		res.Failure = &Failure{
			Reason: ReasonChannelUnsupported,
			Detail: fmt.Sprintf("upstream GetLatestVersion queries only the default %q channel", def.Channel),
		}
		return res
	}

	attempts := 0
	var lastErr error
	for attempt := 1; attempt <= s.cfg.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		attempts = attempt
		qctx, cancel := context.WithTimeout(ctx, s.cfg.QueryTimeout)
		raw, err := s.q.LatestVersion(qctx, def)
		cancel()
		if err == nil {
			return s.recordSuccess(def, raw, res, attempts)
		}
		lastErr = err
		reason, retryable := classifyError(err)
		if !retryable {
			return s.recordFailure(def, res, Failure{Reason: reason, Detail: err.Error(), Attempts: attempts})
		}
		// 有限重试：仅在父 context 仍有预算时等待后重试。
		if attempt < s.cfg.MaxAttempts {
			timer := time.NewTimer(s.cfg.RetryBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				lastErr = ctx.Err()
				reason, _ = classifyError(lastErr)
				return s.recordFailure(def, res, Failure{Reason: reason, Detail: lastErr.Error(), Attempts: attempts})
			case <-timer.C:
			}
		}
	}
	return s.recordFailure(def, res, Failure{
		Reason: classifyReason(lastErr), Detail: lastErr.Error(), Attempts: attempts,
	})
}

// recordSuccess 解释并记录一次成功查询。解析失败在这里显式上报：
// 上游 ParseVersion 对垃圾输入返回 {Raw}, nil（非错误），本层不得把
// 不可解释的串当版本收进缓存——那正是「静默降级」。
func (s *Service) recordSuccess(def SourceDef, raw string, res SourceResult, attempts int) SourceResult {
	if strings.TrimSpace(raw) == "" {
		return s.recordFailure(def, res, Failure{
			Reason:   ReasonParseFailed,
			Detail:   fmt.Sprintf("%s %s: upstream returned an empty version string", def.Method, def.Package),
			Attempts: attempts,
		})
	}
	v := ParseVersion(raw)
	if v.Scheme == SchemeUnparsed {
		return s.recordFailure(def, res, Failure{
			Reason: ReasonParseFailed,
			Detail: fmt.Sprintf("%s %s: upstream version %q is not interpretable (no version structure)",
				def.Method, def.Package, raw),
			Attempts: attempts,
		})
	}
	now := s.cfg.Now()
	s.mu.Lock()
	s.lastOK[cacheKey{def.AgentID, def.Method, def.Channel}] = cachedSuccess{version: v, queriedAt: now}
	s.mu.Unlock()

	res.Status = StatusOK
	res.Version = &v
	res.QueriedAt = &now
	res.Attempts = attempts
	return res
}

func (s *Service) recordFailure(def SourceDef, res SourceResult, f Failure) SourceResult {
	res.Status = StatusFailed
	res.Failure = &f
	res.Attempts = f.Attempts
	if c, ok := s.lookup(def); ok {
		res.LastSuccess = &CachedVersion{Version: c.version, QueriedAt: c.queriedAt}
	}
	return res
}

func (s *Service) lookup(def SourceDef) (cachedSuccess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.lastOK[cacheKey{def.AgentID, def.Method, def.Channel}]
	return c, ok
}
