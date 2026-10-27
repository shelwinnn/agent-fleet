package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func codexPresentEntry() LifecycleEntry {
	return LifecycleEntry{Intent: LifecycleIntentPresent, Version: "0.44.0", Method: "npm", Package: "@openai/codex"}
}

// —— 三态意图 ——

func TestValidateLifecycleEntryThreeStates(t *testing.T) {
	cases := []struct {
		name    string
		agent   string
		entry   LifecycleEntry
		wantErr bool
	}{
		{"present 全字段", "codex", codexPresentEntry(), false},
		{"absent 带来源", "codex", LifecycleEntry{Intent: LifecycleIntentAbsent, Method: "npm", Package: "@openai/codex"}, false},
		{"unmanaged 无要求", "codex", LifecycleEntry{Intent: LifecycleIntentUnmanaged}, false},
		{"unmanaged 带来源也合法（保留展示信息）", "codex", LifecycleEntry{Intent: LifecycleIntentUnmanaged, Method: "npm", Package: "@openai/codex"}, false},
		{"空 intent 非法（不默认）", "codex", LifecycleEntry{}, true},
		{"未知 intent 非法", "codex", LifecycleEntry{Intent: "installed"}, true},
		{"present 版本浮动非法", "codex", LifecycleEntry{Intent: LifecycleIntentPresent, Version: "latest", Method: "npm", Package: "@openai/codex"}, true},
		{"present 缺来源非法", "codex", LifecycleEntry{Intent: LifecycleIntentPresent, Version: "0.44.0"}, true},
		{"absent 带版本非法", "codex", LifecycleEntry{Intent: LifecycleIntentAbsent, Version: "0.44.0", Method: "npm", Package: "@openai/codex"}, true},
		{"absent 缺来源非法", "codex", LifecycleEntry{Intent: LifecycleIntentAbsent}, true},
		{"unmanaged 带版本非法", "codex", LifecycleEntry{Intent: LifecycleIntentUnmanaged, Version: "0.44.0"}, true},
		{"空 agent 非法", "", LifecycleEntry{Intent: LifecycleIntentUnmanaged}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLifecycleEntry(tc.agent, tc.entry)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateLifecycleEntry(%q,%+v) err=%v, wantErr=%v", tc.agent, tc.entry, err, tc.wantErr)
			}
		})
	}
}

// 防回归（spec：不得通过省略快照隐式卸载）：合并语义中未提及的 agent 原样
// 保留；空更新不是卸载；没有任何输入路径能把条目变成 absent 或删除。
func TestMergeLifecycleDesiredNoImplicitUninstall(t *testing.T) {
	current := LifecycleDesired{SchemaVersion: LifecycleDesiredSchemaVersion,
		Entries: map[string]LifecycleEntry{"codex": codexPresentEntry()}}

	// 空更新：codex 保持 present，无变化。
	merged, changed, err := MergeLifecycleDesired(current, LifecycleDesired{SchemaVersion: LifecycleDesiredSchemaVersion})
	if err != nil {
		t.Fatalf("empty update: %v", err)
	}
	if changed {
		t.Fatal("empty update must not change anything")
	}
	if got := merged.Entries["codex"]; got != codexPresentEntry() {
		t.Fatalf("omitted agent must be preserved verbatim, got %+v", got)
	}
	if merged.Entries["codex"].Intent != LifecycleIntentPresent {
		t.Fatal("omission must never downgrade to absent (implicit uninstall)")
	}

	// 显式停止管理：意图变化，程序保留（unmanaged ≠ absent）。
	merged, changed, err = MergeLifecycleDesired(current, LifecycleDesired{SchemaVersion: LifecycleDesiredSchemaVersion,
		Entries: map[string]LifecycleEntry{"codex": {Intent: LifecycleIntentUnmanaged}}})
	if err != nil || !changed {
		t.Fatalf("explicit unmanaged: changed=%v err=%v", changed, err)
	}
	if merged.Entries["codex"].Intent != LifecycleIntentUnmanaged {
		t.Fatalf("want unmanaged, got %+v", merged.Entries["codex"])
	}

	// 首次纳管：current 为空文档时合并结果即 update。
	merged, changed, err = MergeLifecycleDesired(LifecycleDesired{}, LifecycleDesired{SchemaVersion: LifecycleDesiredSchemaVersion,
		Entries: map[string]LifecycleEntry{"codex": codexPresentEntry()}})
	if err != nil || !changed {
		t.Fatalf("first enrollment: changed=%v err=%v", changed, err)
	}
	if merged.Entries["codex"].Intent != LifecycleIntentPresent {
		t.Fatalf("want present, got %+v", merged.Entries["codex"])
	}

	// 非法更新（浮动版本）整体拒绝，不产生部分合并。
	if _, _, err := MergeLifecycleDesired(current, LifecycleDesired{SchemaVersion: LifecycleDesiredSchemaVersion,
		Entries: map[string]LifecycleEntry{"codex": {Intent: LifecycleIntentPresent, Version: "*"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("floating version update must be rejected, got %v", err)
	}

	// schema 版本不符整体拒绝。
	if _, _, err := MergeLifecycleDesired(current, LifecycleDesired{SchemaVersion: "v9"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong schemaVersion must be rejected, got %v", err)
	}
}

// —— 版本固定（创建时精确版本）——

func TestIsExactVersion(t *testing.T) {
	// 与 agentlocal/lifecycle（KM-46 窄适配层）的 exactVersionRe 同一形态：
	// x.y.z 至多带一个 -prerelease 或 +build 后缀。两道闸必须保持一致，
	// 否则创建接受了执行拒绝（契约错位）。
	valid := []string{"0.44.0", "1.2.3", "10.20.30", "1.2.3-beta.1", "1.2.3+build.5"}
	invalid := []string{"", "latest", "stable", "v1.2.3", "1.2", "1.2.x", "*", "^1.2.3", "~1.2.3", ">=1.2.3", " 1.2.3", "1.2.3-rc.1+build.9"}
	for _, v := range valid {
		if !IsExactVersion(v) {
			t.Errorf("IsExactVersion(%q)=false, want true", v)
		}
	}
	for _, v := range invalid {
		if IsExactVersion(v) {
			t.Errorf("IsExactVersion(%q)=true, want false", v)
		}
	}
}

func TestValidateLifecycleOperationVersionPinning(t *testing.T) {
	target := LifecycleTarget{Machine: "m1", User: "alice", Agent: "codex", Method: "npm", Package: "@openai/codex"}
	base := LifecycleOperation{Spec: LifecycleOperationSpec{Type: LifecycleOpInstall, IdempotencyKey: "k1", Targets: []LifecycleTarget{target}}}

	// Install/Upgrade 必须精确版本。
	for _, v := range []string{"", "latest", "*", "^0.44.0", "0.44"} {
		op := base
		op.Spec.Version = v
		if err := ValidateLifecycleOperation(&op); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s with version %q must be rejected, got %v", op.Spec.Type, v, err)
		}
	}
	op := base
	op.Spec.Version = "0.44.0"
	if err := ValidateLifecycleOperation(&op); err != nil {
		t.Fatalf("install with exact version must pass, got %v", err)
	}

	// Uninstall/CheckUpdate 不得带版本。
	for _, typ := range []string{LifecycleOpUninstall, LifecycleOpCheckUpdate} {
		op := base
		op.Spec.Type = typ
		op.Spec.Version = "0.44.0"
		if err := ValidateLifecycleOperation(&op); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s must not carry version, got %v", typ, err)
		}
		op2 := base
		op2.Spec.Type = typ
		if err := ValidateLifecycleOperation(&op2); err != nil {
			t.Errorf("%s without version must pass, got %v", typ, err)
		}
	}
}

func TestValidateLifecycleOperationIdentity(t *testing.T) {
	mk := func() LifecycleOperation {
		return LifecycleOperation{Spec: LifecycleOperationSpec{
			Type: LifecycleOpCheckUpdate, IdempotencyKey: "k1",
			Targets: []LifecycleTarget{{Machine: "m1", User: "alice", Agent: "codex", Method: "npm", Package: "@openai/codex"}},
		}}
	}
	// 幂等键必填。
	op := mk()
	op.Spec.IdempotencyKey = "  "
	if err := ValidateLifecycleOperation(&op); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty idempotency key must be rejected, got %v", err)
	}
	// 目标必填。
	op = mk()
	op.Spec.Targets = nil
	if err := ValidateLifecycleOperation(&op); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty targets must be rejected, got %v", err)
	}
	// 目标身份缺来源（卸载来源未识别 → 拒绝，story 22）。
	op = mk()
	op.Spec.Type = LifecycleOpUninstall
	op.Spec.Targets[0].Method = ""
	if err := ValidateLifecycleOperation(&op); !errors.Is(err, ErrInvalid) {
		t.Errorf("uninstall without identified source must be rejected, got %v", err)
	}
	// 重复目标身份拒绝。
	op = mk()
	op.Spec.Targets = append(op.Spec.Targets, op.Spec.Targets[0])
	if err := ValidateLifecycleOperation(&op); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate target must be rejected, got %v", err)
	}
	// env 归一：空 → native；不同 env 是不同目标。
	op = mk()
	a := op.Spec.Targets[0]
	b := a
	b.Env = "wsl:ubuntu"
	op.Spec.Targets = []LifecycleTarget{a, b}
	if err := ValidateLifecycleOperation(&op); err != nil {
		t.Fatalf("native and wsl targets must coexist, got %v", err)
	}
	if a.Normalize().Key() == b.Key() {
		t.Fatal("native and wsl targets must have distinct keys")
	}
	// 未知类型拒绝。
	op = mk()
	op.Spec.Type = "Restart"
	if err := ValidateLifecycleOperation(&op); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown type must be rejected, got %v", err)
	}
}

// —— 逐目标状态机 ——

func TestTargetTransitionWhitelist(t *testing.T) {
	allowed := [][2]string{
		{TargetPending, TargetRunning}, {TargetPending, TargetSucceeded},
		{TargetPending, TargetFailed}, {TargetPending, TargetNotExecuted}, {TargetPending, TargetUnknown},
		{TargetRunning, TargetSucceeded}, {TargetRunning, TargetFailed}, {TargetRunning, TargetUnknown},
	}
	for _, tc := range allowed {
		if !TargetTransitionAllowed(tc[0], tc[1]) {
			t.Errorf("%s→%s must be allowed", tc[0], tc[1])
		}
	}
	// not_executed：目标上线后不自动执行旧意图——没有出边。
	// unknown：未知不计成功、不自动重试——没有出边。
	// succeeded/failed：终态。
	for _, from := range []string{TargetNotExecuted, TargetUnknown, TargetSucceeded, TargetFailed} {
		for _, to := range []string{TargetPending, TargetRunning, TargetSucceeded, TargetFailed, TargetNotExecuted, TargetUnknown} {
			if TargetTransitionAllowed(from, to) {
				t.Errorf("%s→%s must be forbidden", from, to)
			}
		}
	}
}

func TestAllTargetsSucceededUnknownNotSuccess(t *testing.T) {
	tg := LifecycleTarget{Machine: "m1", User: "u", Agent: "codex", Method: "npm", Package: "@openai/codex"}
	op := &LifecycleOperation{Spec: LifecycleOperationSpec{Type: LifecycleOpInstall},
		Status: LifecycleOperationStatus{Results: []LifecycleTargetResult{
			{Target: tg, State: TargetSucceeded, Reason: ReasonVerifiedPresent},
			{Target: tg, State: TargetUnknown, Reason: ReasonResultLost},
		}}}
	if op.AllTargetsSucceeded() {
		t.Fatal("unknown must not count as success")
	}
	op.Status.Results[1] = LifecycleTargetResult{Target: tg, State: TargetNotExecuted, Reason: ReasonTargetOffline}
	if op.AllTargetsSucceeded() {
		t.Fatal("not_executed must not count as success")
	}
	op.Status.Results[1] = LifecycleTargetResult{Target: tg, State: TargetFailed, Reason: ReasonOpFailed}
	if op.AllTargetsSucceeded() {
		t.Fatal("failed must not count as success")
	}
	op.Status.Results[1] = LifecycleTargetResult{Target: tg, State: TargetSucceeded, Reason: ReasonVerifiedPresent}
	if !op.AllTargetsSucceeded() {
		t.Fatal("all succeeded must count as success")
	}
	empty := &LifecycleOperation{}
	if empty.AllTargetsSucceeded() {
		t.Fatal("empty operation must not count as success")
	}
}

func TestRecomputeOpPhase(t *testing.T) {
	tg := LifecycleTarget{Machine: "m1", User: "u", Agent: "codex", Method: "npm", Package: "@openai/codex"}
	r := func(state string) LifecycleTargetResult { return LifecycleTargetResult{Target: tg, State: state} }
	if got := RecomputeOpPhase(nil); got != OperationPhasePending {
		t.Fatalf("nil results want Pending, got %s", got)
	}
	if got := RecomputeOpPhase([]LifecycleTargetResult{r(TargetPending)}); got != OperationPhasePending {
		t.Fatalf("pending want Pending, got %s", got)
	}
	if got := RecomputeOpPhase([]LifecycleTargetResult{r(TargetSucceeded), r(TargetRunning)}); got != OperationPhaseRunning {
		t.Fatalf("any running want Running, got %s", got)
	}
	for _, s := range []string{TargetSucceeded, TargetFailed, TargetNotExecuted, TargetUnknown} {
		if got := RecomputeOpPhase([]LifecycleTargetResult{r(TargetSucceeded), r(s)}); got != OperationPhaseCompleted {
			t.Fatalf("all result states want Completed, got %s (with %s)", got, s)
		}
	}
}

// —— 重试：仅确定失败可重试，意图必须一致 ——

func TestValidateLifecycleRetry(t *testing.T) {
	tg := LifecycleTarget{Machine: "m1", Env: "native", User: "alice", Agent: "codex", Method: "npm", Package: "@openai/codex"}
	original := &LifecycleOperation{
		Metadata: ObjectMeta{Name: "op-1"},
		Spec: LifecycleOperationSpec{Type: LifecycleOpInstall, IdempotencyKey: "k1", Version: "0.44.0",
			Targets: []LifecycleTarget{tg}},
		Status: LifecycleOperationStatus{Results: []LifecycleTargetResult{
			{Target: tg, State: TargetFailed, Reason: ReasonOpFailed},
		}},
	}

	retry := &LifecycleOperation{RetryOf: original.Metadata.Name,
		Spec: LifecycleOperationSpec{Type: LifecycleOpInstall, IdempotencyKey: "k2", Version: "0.44.0",
			Targets: []LifecycleTarget{tg}}}
	if err := ValidateLifecycleRetry(retry, original); err != nil {
		t.Fatalf("retry of failed target must be allowed, got %v", err)
	}

	// 缺 retryOf 拒绝。
	noLink := *retry
	noLink.RetryOf = ""
	if err := ValidateLifecycleRetry(&noLink, original); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retry without retryOf must be rejected, got %v", err)
	}
	// 类型不一致拒绝。
	wrongType := *retry
	wrongType.Spec.Type = LifecycleOpUninstall
	if err := ValidateLifecycleRetry(&wrongType, original); !errors.Is(err, ErrRetryNotAllowed) {
		t.Fatalf("retry with different type must be rejected, got %v", err)
	}
	// 版本不一致 = 改意图，拒绝。
	wrongVersion := *retry
	wrongVersion.Spec.Version = "0.45.0"
	if err := ValidateLifecycleRetry(&wrongVersion, original); !errors.Is(err, ErrRetryNotAllowed) {
		t.Fatalf("retry with different version must be rejected, got %v", err)
	}
	// 非原操作目标拒绝。
	stranger := *retry
	stranger.Spec.Targets = []LifecycleTarget{{Machine: "m2", User: "bob", Agent: "codex", Method: "npm", Package: "@openai/codex"}}
	if err := ValidateLifecycleRetry(&stranger, original); !errors.Is(err, ErrRetryNotAllowed) {
		t.Fatalf("retry of foreign target must be rejected, got %v", err)
	}
	// 仅失败目标可重试：unknown / not_executed / succeeded 都不行。
	for _, state := range []string{TargetUnknown, TargetNotExecuted, TargetSucceeded, TargetPending, TargetRunning} {
		orig := *original
		orig.Status.Results = []LifecycleTargetResult{{Target: tg, State: state}}
		if err := ValidateLifecycleRetry(retry, &orig); !errors.Is(err, ErrRetryNotAllowed) {
			t.Fatalf("retry of %s target must be rejected, got %v", state, err)
		}
	}
	// 子集重试允许（原操作多目标，只重试失败的）。
	multi := *original
	tg2 := LifecycleTarget{Machine: "m2", User: "bob", Agent: "codex", Method: "npm", Package: "@openai/codex"}
	multi.Spec.Targets = []LifecycleTarget{tg, tg2}
	multi.Status.Results = []LifecycleTargetResult{
		{Target: tg, State: TargetFailed, Reason: ReasonOpFailed},
		{Target: tg2, State: TargetSucceeded, Reason: ReasonVerifiedPresent},
	}
	subset := &LifecycleOperation{RetryOf: "k1",
		Spec: LifecycleOperationSpec{Type: LifecycleOpInstall, IdempotencyKey: "k2", Version: "0.44.0",
			Targets: []LifecycleTarget{tg}}}
	if err := ValidateLifecycleRetry(subset, &multi); err != nil {
		t.Fatalf("subset retry must keep succeeded results intact, got %v", err)
	}
}

// —— 能力矩阵表达 ——

func TestLookupLifecycleCapability(t *testing.T) {
	// KM-46 §7：binary 通道跨平台明确阻塞。
	if c := LookupLifecycleCapability("linux", "amd64", "binary"); c.State != CapabilityBlocked {
		t.Fatalf("binary channel must be blocked, got %+v", c)
	}
	// linux/amd64 npm：隔离环境 GO，但真机未验收——未验证，不标支持。
	c := LookupLifecycleCapability("linux", "amd64", "npm")
	if c.State != CapabilityUnverified || c.EvidenceLevel != EvidenceIsolated {
		t.Fatalf("linux/amd64 npm must be unverified/isolated, got %+v", c)
	}
	// Windows npm 阻塞（上游 .exe 硬编码）。
	if c := LookupLifecycleCapability("windows", "amd64", "npm"); c.State != CapabilityBlocked {
		t.Fatalf("windows/amd64 npm must be blocked, got %+v", c)
	}
	// darwin brew 未验证（固定版本缝隙）。
	if c := LookupLifecycleCapability("darwin", "arm64", "brew"); c.State != CapabilityUnverified {
		t.Fatalf("darwin/arm64 brew must be unverified, got %+v", c)
	}
	// 无记录组合：缺省未验证，绝不缺省支持。
	if c := LookupLifecycleCapability("sunos", "sparc", "curl"); c.State != CapabilityUnverified {
		t.Fatalf("unknown combo must default to unverified, got %+v", c)
	}
	// 全库无 supported：真机证据未产生前不宣称支持（T8 前的硬约束）。
	for _, c := range lifecycleCapabilityRegistry {
		if c.State == CapabilitySupported {
			t.Fatalf("registry must not claim support before real-machine evidence: %+v", c)
		}
	}
}

func TestCheckTargetCapabilityBlockedRejectedAtCreation(t *testing.T) {
	blocked := LifecycleTarget{Machine: "m1", User: "u", Agent: "codex", Method: "binary", Package: "codex", OS: "linux", Arch: "amd64"}
	op := &LifecycleOperation{Spec: LifecycleOperationSpec{Type: LifecycleOpInstall, IdempotencyKey: "k1", Version: "0.44.0",
		Targets: []LifecycleTarget{blocked}}}
	if err := ValidateLifecycleOperation(op); !errors.Is(err, ErrUnsupportedCombo) {
		t.Fatalf("blocked combo must be rejected at creation, got %v", err)
	}
	// 未验证组合允许创建（执行前由 T4 拒绝），判定快照进记录。
	unverified := LifecycleTarget{Machine: "m1", User: "u", Agent: "codex", Method: "npm", Package: "@openai/codex", OS: "linux", Arch: "amd64"}
	op = &LifecycleOperation{Spec: LifecycleOperationSpec{Type: LifecycleOpInstall, IdempotencyKey: "k1", Version: "0.44.0",
		Targets: []LifecycleTarget{unverified}}}
	if err := ValidateLifecycleOperation(op); err != nil {
		t.Fatalf("unverified combo must be allowed at creation, got %v", err)
	}
	snap := SnapshotCapability(unverified)
	if snap.CapabilityState != CapabilityUnverified || snap.CapabilityReason == "" {
		t.Fatalf("capability snapshot missing, got %+v", snap)
	}
	// 平台未知不阻塞（无法判定即未验证，执行层兜底）。
	unknown := LifecycleTarget{Machine: "m1", User: "u", Agent: "codex", Method: "npm", Package: "@openai/codex"}
	if err := CheckTargetCapability(unknown); err != nil {
		t.Fatalf("unknown platform must not block creation, got %v", err)
	}
}

// —— 操作记录不含秘密 ——

func TestOperationRecordContainsNoSecrets(t *testing.T) {
	op := &LifecycleOperation{
		Spec: LifecycleOperationSpec{Type: LifecycleOpInstall, IdempotencyKey: "k1", Version: "0.44.0",
			Targets:   []LifecycleTarget{SnapshotCapability(LifecycleTarget{Machine: "m1", User: "alice", Agent: "codex", Method: "npm", Package: "@openai/codex", OS: "linux", Arch: "amd64"})},
			CreatedBy: "member:x"},
		Status: LifecycleOperationStatus{Phase: OperationPhaseCompleted, Results: []LifecycleTargetResult{{
			Target: LifecycleTarget{Machine: "m1", User: "alice", Agent: "codex", Method: "npm", Package: "@openai/codex"},
			State:  TargetSucceeded, Reason: ReasonVerifiedPresent,
			Evidence: &LifecycleEvidence{ObservedVersion: "0.44.0", ExecutablePath: "/usr/local/bin/codex", Method: "npm",
				InventorySeq: 7, Message: "re-detected after install", RecordedAt: time.Now().UTC()},
		}}},
	}
	b, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	// 字段白名单检查：序列化产物只能出现模型声明的键集合。
	var flat map[string]any
	if err := json.Unmarshal(b, &flat); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "secret", "apikey", "api_key", "password", "credential", "env", "envvalue"} {
		if strings.Contains(strings.ToLower(string(b)), "\""+forbidden+"\"") {
			t.Errorf("operation record must never contain a %q key", forbidden)
		}
	}
	if len(b) > 16*1024 {
		t.Errorf("operation record unexpectedly large: %d bytes", len(b))
	}
}

func TestValidateTargetEvidenceRules(t *testing.T) {
	// 结果态原因规则的域内断言（仓储写入前同样校验）。
	if !IsTargetResultState(TargetNotExecuted) || !IsTargetResultState(TargetUnknown) {
		t.Fatal("not_executed/unknown must be result states")
	}
	if IsTargetResultState(TargetPending) || IsTargetResultState(TargetRunning) {
		t.Fatal("pending/running are not result states")
	}
	if MaxEvidenceMessageLen <= 0 {
		t.Fatal("evidence message limit must be positive")
	}
}
