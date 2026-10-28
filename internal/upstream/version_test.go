package upstream

import (
	"errors"
	"strings"
	"testing"
)

func TestParseVersionSchemes(t *testing.T) {
	tests := []struct {
		raw    string
		scheme VersionScheme
	}{
		{"0.45.0", SchemeSemver},
		{"v1.2.3", SchemeSemver},
		{"1.2.3-beta.2", SchemeSemver},
		{"1.2.3+build.7", SchemeSemver},
		{"2024.11.1", SchemeSemver}, // 三段纯数字即 SemVer 形态（semver 不限位数）
		{"1.2.3_1", SchemeSourceRule},
		{"2024.11", SchemeSourceRule},
		{"1.2a", SchemeSourceRule},
		{"latest", SchemeUnparsed},
		{"cheese", SchemeUnparsed},
		{"", SchemeUnparsed},
		{"   ", SchemeUnparsed},
	}
	for _, tc := range tests {
		v := ParseVersion(tc.raw)
		if v.Scheme != tc.scheme {
			t.Errorf("ParseVersion(%q).Scheme = %q, want %q", tc.raw, v.Scheme, tc.scheme)
		}
		// Raw 保存的是去空白后的原样串（与来源返回一一对应）。
		if want := strings.TrimSpace(tc.raw); v.Raw != want {
			t.Errorf("ParseVersion(%q).Raw = %q, want %q", tc.raw, v.Raw, want)
		}
	}
}

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.44.0", "0.45.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.1.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0-alpha", "1.0.0", -1},      // 无 prerelease 者更大
		{"1.0.0-alpha", "1.0.0-beta", -1}, // 字典序
		{"1.0.0-alpha.1", "1.0.0-alpha.2", -1},
		{"1.0.0-2", "1.0.0-10", -1}, // 数字段按数值
		{"1.0.0-rc.1", "1.0.0", -1},
		{"v1.2.3", "1.2.3", 0},                 // v 前缀归一
		{"1.0.0+build.1", "1.0.0+build.99", 0}, // build 元数据不参与
	}
	for _, tc := range tests {
		got, err := Compare(ParseVersion(tc.a), ParseVersion(tc.b))
		if err != nil {
			t.Fatalf("Compare(%q,%q): %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCompareSourceRule(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.2.3_1", "1.2.2_9", 1},   // brew 修订段
		{"1.2.3_2", "1.2.3_10", -1}, // 数值比较而非字典序
		{"1.2.3_1", "1.2.3_1", 0},
		{"2024.11", "2024.12", -1}, // 同为日期式（两段不成 SemVer）
		{"2024.11a", "2024.11b", -1},
		{"1.2a", "1.2b", -1},
	}
	for _, tc := range tests {
		got, err := Compare(ParseVersion(tc.a), ParseVersion(tc.b))
		if err != nil {
			t.Fatalf("Compare(%q,%q): %v", tc.a, tc.b, err)
		}
		if got != tc.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCompareIncomparableIsExplicit(t *testing.T) {
	// scheme 混用、unparsed 参与、同串相等三类边界都必须可区分。
	if got, err := Compare(ParseVersion("1.2.3"), ParseVersion("1.2.3_1")); !errors.Is(err, ErrIncomparable) {
		t.Errorf("semver vs source_rule: got (%d, %v), want ErrIncomparable", got, err)
	}
	if got, err := Compare(ParseVersion("latest"), ParseVersion("1.2.3")); !errors.Is(err, ErrIncomparable) {
		t.Errorf("unparsed vs semver: got (%d, %v), want ErrIncomparable", got, err)
	}
	if got, err := Compare(ParseVersion("0.45.0"), ParseVersion("0.45.0")); got != 0 || err != nil {
		t.Errorf("identical raw: got (%d, %v), want (0, nil)", got, err)
	}
}
