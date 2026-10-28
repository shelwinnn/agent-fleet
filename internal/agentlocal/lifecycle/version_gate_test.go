package lifecycle

import (
	"testing"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// 版本闸双闸对齐（KM-47 评审修复）：KM-46 窄适配层的 exactVersionRe 与 KM-47
// 域模型 domain.IsExactVersion 必须对同一语料给出同一判定——两道闸一旦错位，
// 会出现"创建接受了、执行拒绝"（或反方向）的契约裂缝。本测试把"手工保持
// 一致"的注释承诺变成失败即红的锁定。
func TestVersionGateAlignedWithDomainModel(t *testing.T) {
	corpus := []string{
		"0.44.0", "1.2.3", "10.20.30", "1.2.3-beta.1", "1.2.3+build.5",
		"", "latest", "stable", "latest-1", "v1.2.3", "1.2", "1.2.x", "*",
		"^1.2.3", "~1.2.3", ">=1.2.3", " 1.2.3", "1.2.3-rc.1+build.9", "1.2.3-",
	}
	for _, v := range corpus {
		if got, want := exactVersionRe.MatchString(v), domain.IsExactVersion(v); got != want {
			t.Errorf("version gate mismatch for %q: agentlocal=%v domain=%v", v, got, want)
		}
	}
}
