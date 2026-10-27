// Package upstream 实现独立于期望状态偏差（drift）的上游版本发现服务
// （KM-48，依据 docs/superpowers/specs/2026-10-27-codex-lifecycle-spec.md：
// 「上游版本发现独立于 drift，记录来源、渠道、适用平台、查询时间及失败；
// 不假定版本均为 SemVer」）。
//
// 与 lifecycle（KM-46 窄适配层）同样的上游事实约束（v1.5.0，见
// docs/KM-46-agentmanager-接入核查报告.md §4）：
//
//   - Manager.GetLatestVersion 仅覆盖 npm/pip/brew；native/binary 等明确
//     返回 "version checking not supported"——能力声明必须如实反映该覆盖面。
//   - 上游 ParseVersion 对垃圾输入静默返回 {Raw: 输入}, nil（非零、非错误）。
//     本包不做二次信任：严格 SemVer 解析失败的输入按来源规则再解释，仍不可
//     解释则显式上报 version_parse_failed，绝不静默降级为可用版本。
//   - 上游对最近一次查询结果（含错误）有进程内 5 分钟缓存（providers/
//     latest_cache.go registryLatestVersionTTL）。本包缓存只服务失败回退，
//     并如实记录各自的取得时间；上游内部缓存语义在设计说明中披露。
//
// 本包只读上游注册表，不触碰任何目标环境（无安装/卸载/探测宿主包管理器
// 的路径），与 DetectAll 的副作用问题（核查报告 §7）无涉。
package upstream

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// VersionScheme 标记版本串的解释方式。发现结果必须显式携带 scheme：
// 调用方（T5 前端）据此决定如何展示与比较，而不是拿到裸字符串猜。
type VersionScheme string

const (
	// SchemeSemver：严格 SemVer（x.y.z[-pre][+build]，容忍 v 前缀），
	// 按 semver.org 优先级规则比较。
	SchemeSemver VersionScheme = "semver"
	// SchemeSourceRule：非 SemVer，但可按来源规则解释与排序（如 brew 的
	// "1.2.3_1"、日期式 "2024.11"）。比较规则见 compareRuleSegments。
	SchemeSourceRule VersionScheme = "source_rule"
	// SchemeUnparsed：完全不可解释（无任何数字结构）。发现服务把它上报为
	// version_parse_failed，不会作为 ok 结果返回。
	SchemeUnparsed VersionScheme = "unparsed"
)

// ErrIncomparable 标记两个版本在给定规则下无法比较（scheme 混用或存在
// unparsed 方）。比较失败必须显式暴露，由调用方决定展示方式。
var ErrIncomparable = errors.New("upstream: versions are not comparable under their schemes")

// Version 是一个被发现（或被缓存）的上游版本。Raw 永远保存来源返回的
// 原样字符串；结构化字段只是解释结果，不替代 Raw。
type Version struct {
	Raw    string        `json:"raw"`
	Scheme VersionScheme `json:"scheme"`
	sem    semver        // Scheme==semver 时有效
	rule   []ruleSegment
}

// String 实现 fmt.Stringer，返回 Raw。
func (v Version) String() string { return v.Raw }

// IsZero 报告是否为零值（未取得任何版本）。
func (v Version) IsZero() bool { return v.Raw == "" }

// ParseVersion 把来源返回的版本串解释为 Version。解释顺序：
// 严格 SemVer → 来源规则 → unparsed。任何输入都不会 panic；
// unparsed 不是 error——是否算失败由发现服务的状态机判定（见 Check）。
func ParseVersion(raw string) Version {
	raw = strings.TrimSpace(raw)
	if v, ok := parseSemver(raw); ok {
		return v
	}
	if segs, ok := parseRuleSegments(raw); ok {
		return Version{Raw: raw, Scheme: SchemeSourceRule, rule: segs}
	}
	return Version{Raw: raw, Scheme: SchemeUnparsed}
}

// Compare 按 Version 自身的 scheme 比较两版本。规则：
//
//   - Raw 完全相等 → 0（同串必等，先于 scheme 判定）。
//   - 双方均为 semver → semver.org 优先级规则（build 元数据不参与）。
//   - 双方均为 source_rule → 分段规则（数字段按数值，其余按字典序；
//     数字段排在非数字段之前；缺段视为更小）。
//   - scheme 混用或任一为 unparsed → ErrIncomparable。
func Compare(a, b Version) (int, error) {
	if a.Raw == b.Raw {
		return 0, nil
	}
	if a.Scheme != b.Scheme || a.Scheme == SchemeUnparsed {
		return 0, fmt.Errorf("%w: %q (%s) vs %q (%s)", ErrIncomparable,
			a.Raw, a.Scheme, b.Raw, b.Scheme)
	}
	if a.Scheme == SchemeSemver {
		return a.sem.compare(b.sem), nil
	}
	return compareRuleSegments(a.rule, b.rule), nil
}

// semver 是严格 SemVer 的结构化形态。
type semver struct {
	major, minor, patch uint64
	pre                 string // 无 prerelease 时空串；有值时整体小于无值
}

// semverRe 只接受完整三段数字版本；v 前缀容忍（展示场景，非安装 spec——
// 安装侧的精确形态校验仍归 lifecycle.exactVersionRe，两者职责不同）。
var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

func parseSemver(raw string) (Version, bool) {
	m := semverRe.FindStringSubmatch(raw)
	if m == nil {
		return Version{}, false
	}
	var parts [3]uint64
	for i, s := range m[1:4] {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return Version{}, false // 超出 uint64 的畸形数字按不可解释处理
		}
		parts[i] = n
	}
	return Version{
		Raw:    raw,
		Scheme: SchemeSemver,
		sem:    semver{major: parts[0], minor: parts[1], patch: parts[2], pre: m[4]},
	}, true
}

func (s semver) compare(o semver) int {
	if s.major != o.major {
		return cmpUint(s.major, o.major)
	}
	if s.minor != o.minor {
		return cmpUint(s.minor, o.minor)
	}
	if s.patch != o.patch {
		return cmpUint(s.patch, o.patch)
	}
	return comparePrerelease(s.pre, o.pre)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePrerelease 实现 semver.org 第 11 条：无 prerelease 者更大；
// 逐段比较，数字段按数值（数字段小于字母数字段），其余按 ASCII 字典序。
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := comparePreIdent(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(as), len(bs))
}

func comparePreIdent(a, b string) int {
	an, aerr := strconv.ParseUint(a, 10, 64)
	bn, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		return cmpUint(an, bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// ruleSegment 是来源规则比较的最小单元：数字段或非数字段。
type ruleSegment struct {
	num   uint64
	text  string
	isNum bool
}

// ruleSplitRe 把版本串切成段：数字段与字母数字段。
var ruleSplitRe = regexp.MustCompile(`[._-]`)

// parseRuleSegments 接受「至少含一个数字」的非 SemVer 串（如 1.2.3_1、
// 2024.11、1.2a）；完全无数字结构（latest、cheese、空串）返回 false，
// 由调用方按解析失败处理。
func parseRuleSegments(raw string) ([]ruleSegment, bool) {
	if raw == "" {
		return nil, false
	}
	parts := ruleSplitRe.Split(raw, -1)
	segs := make([]ruleSegment, 0, len(parts))
	sawDigitStructure := false
	for _, p := range parts {
		if p == "" {
			continue
		}
		if n, err := strconv.ParseUint(p, 10, 64); err == nil {
			segs = append(segs, ruleSegment{num: n, isNum: true})
			sawDigitStructure = true
			continue
		}
		segs = append(segs, ruleSegment{text: strings.ToLower(p)})
	}
	return segs, sawDigitStructure
}

// compareRuleSegments 逐段比较：数字段按数值且恒小于非数字段；非数字段按
// 字典序；缺段视为更小（1.2 < 1.2.1）。这是展示排序用的确定性全序，
// 不宣称等价于任何来源的官方版本学。
func compareRuleSegments(a, b []ruleSegment) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		switch {
		case x.isNum && y.isNum:
			if c := cmpUint(x.num, y.num); c != 0 {
				return c
			}
		case x.isNum != y.isNum:
			if x.isNum {
				return -1
			}
			return 1
		default:
			if c := strings.Compare(x.text, y.text); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}
