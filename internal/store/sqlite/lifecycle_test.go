package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

func codexTarget(machine, user string) domain.LifecycleTarget {
	// Normalize 使测试内的 Key() 与仓储归一化后的身份一致（Env 空 → native）。
	return domain.LifecycleTarget{Machine: machine, User: user, Agent: "codex",
		Method: "npm", Package: "@openai/codex", OS: "linux", Arch: "amd64"}.Normalize()
}

func installOp(key, version string, targets ...domain.LifecycleTarget) *domain.LifecycleOperation {
	return &domain.LifecycleOperation{
		Spec: domain.LifecycleOperationSpec{
			Type: domain.LifecycleOpInstall, IdempotencyKey: key, Version: version,
			Targets: targets, CreatedBy: "test",
		},
	}
}

// —— 幂等：重复提交同一请求复用同一操作 ——

func TestLifecycleCreateIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewLifecycleStore(db)

	op := installOp("idem-1", "0.44.0", codexTarget("m1", "alice"))
	stored, created, err := store.CreateOperation(ctx, op)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	if stored.Spec.IdempotencyKey != "idem-1" || len(stored.Status.Results) != 1 ||
		stored.Status.Results[0].State != domain.TargetPending {
		t.Fatalf("unexpected stored op: %+v", stored)
	}

	// 同键同内容：复用同一操作，不产生第二行。
	again := installOp("idem-1", "0.44.0", codexTarget("m1", "alice"))
	dup, created, err := store.CreateOperation(ctx, again)
	if err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if created {
		t.Fatal("duplicate submit must reuse the existing operation")
	}
	if dup.Metadata.Name != stored.Metadata.Name {
		t.Fatalf("duplicate must return same operation id: %s vs %s", dup.Metadata.Name, stored.Metadata.Name)
	}
	// 结果集仍只有一个 pending 目标（无重复变更）。
	got, err := store.GetOperation(ctx, stored.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Results) != 1 || got.Status.Results[0].State != domain.TargetPending {
		t.Fatalf("duplicate submit must not duplicate targets: %+v", got.Status.Results)
	}

	// 同键不同内容：客户端缺陷，冲突。
	conflict := installOp("idem-1", "0.45.0", codexTarget("m1", "alice"))
	if _, _, err := store.CreateOperation(ctx, conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same key different payload must conflict, got %v", err)
	}

	// 目标顺序不同但内容相同：同一意图，复用。
	reordered := installOp("idem-2", "0.44.0", codexTarget("m2", "bob"), codexTarget("m1", "alice"))
	s2, created2, err := store.CreateOperation(ctx, reordered)
	if err != nil || !created2 {
		t.Fatalf("multi-target create: created=%v err=%v", created2, err)
	}
	same := installOp("idem-2", "0.44.0", codexTarget("m1", "alice"), codexTarget("m2", "bob"))
	if _, created, err = store.CreateOperation(ctx, same); err != nil || created {
		t.Fatalf("reordered duplicate must reuse: created=%v err=%v", created, err)
	}
	if len(s2.Status.Results) != 2 {
		t.Fatalf("want 2 targets, got %d", len(s2.Status.Results))
	}
}

// —— 三态意图文档：revision 演进与防隐式卸载 ——

func TestLifecycleDesiredStoreRoundtrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewLifecycleStore(db)

	// 从未纳管：显式 ErrNotFound（"未纳管"可查询，不是 absent）。
	if _, err := store.CurrentDesired(ctx, "m1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unmanaged machine must be ErrNotFound, got %v", err)
	}

	rec, changed, err := store.PutDesired(ctx, "m1", domain.LifecycleDesired{
		SchemaVersion: domain.LifecycleDesiredSchemaVersion,
		Entries: map[string]domain.LifecycleEntry{
			"codex": {Intent: domain.LifecycleIntentPresent, Version: "0.44.0", Method: "npm", Package: "@openai/codex"},
		},
	})
	if err != nil || !changed {
		t.Fatalf("first put: changed=%v err=%v", changed, err)
	}
	if rec.Revision != 1 {
		t.Fatalf("first revision = %d", rec.Revision)
	}

	// 相同内容：不新增代。
	rec2, changed, err := store.PutDesired(ctx, "m1", domain.LifecycleDesired{
		SchemaVersion: domain.LifecycleDesiredSchemaVersion,
		Entries: map[string]domain.LifecycleEntry{
			"codex": {Intent: domain.LifecycleIntentPresent, Version: "0.44.0", Method: "npm", Package: "@openai/codex"},
		},
	})
	if err != nil || changed || rec2.Revision != 1 {
		t.Fatalf("identical put must not bump revision: changed=%v rev=%d err=%v", changed, rec2.Revision, err)
	}

	// 未提及 codex 的更新：条目原样保留（防隐式卸载的仓储级防线）。
	rec3, changed, err := store.PutDesired(ctx, "m1", domain.LifecycleDesired{SchemaVersion: domain.LifecycleDesiredSchemaVersion})
	if err != nil || changed {
		t.Fatalf("omission-only put must not change: changed=%v err=%v", changed, err)
	}
	if rec3.Desired.Entries["codex"].Intent != domain.LifecycleIntentPresent {
		t.Fatalf("omitted entry must be preserved, got %+v", rec3.Desired.Entries["codex"])
	}
	if rec3.Revision != 1 {
		t.Fatalf("merge-no-op must not bump revision, got %d", rec3.Revision)
	}

	// 显式停止管理：新代，程序保留。
	rec4, changed, err := store.PutDesired(ctx, "m1", domain.LifecycleDesired{
		SchemaVersion: domain.LifecycleDesiredSchemaVersion,
		Entries:       map[string]domain.LifecycleEntry{"codex": {Intent: domain.LifecycleIntentUnmanaged}},
	})
	if err != nil || !changed || rec4.Revision != 2 {
		t.Fatalf("explicit unmanaged put: changed=%v rev=%d err=%v", changed, rec4.Revision, err)
	}
	cur, err := store.CurrentDesired(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Revision != 2 || cur.Desired.Entries["codex"].Intent != domain.LifecycleIntentUnmanaged {
		t.Fatalf("current must be rev2 unmanaged, got rev=%d entries=%+v", cur.Revision, cur.Desired.Entries)
	}

	// 非法意图（浮动版本）整体拒绝。
	if _, _, err := store.PutDesired(ctx, "m1", domain.LifecycleDesired{
		SchemaVersion: domain.LifecycleDesiredSchemaVersion,
		Entries:       map[string]domain.LifecycleEntry{"codex": {Intent: domain.LifecycleIntentPresent, Version: "latest"}},
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("floating version must be rejected, got %v", err)
	}
}

// —— 逐目标状态机：离线未执行、上线不复活、未知不改写 ——

func TestLifecycleTargetStateMachine(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewLifecycleStore(db)
	tg := codexTarget("m1", "alice")
	key := tg.Key()

	op, _, err := store.CreateOperation(ctx, installOp("sm-1", "0.44.0", tg))
	if err != nil {
		t.Fatal(err)
	}
	opID := op.Metadata.Name

	// 提交时离线：pending → not_executed（带原因）。
	err = store.TransitionTarget(ctx, opID, key, domain.TargetNotExecuted, domain.ReasonTargetOffline, "machine offline at submit", nil)
	if err != nil {
		t.Fatalf("mark not_executed: %v", err)
	}
	got, _ := store.GetOperation(ctx, opID)
	if got.Status.Phase != domain.OperationPhaseCompleted {
		t.Fatalf("all-terminal op must be Completed, got %s", got.Status.Phase)
	}
	if got.Status.CompletedAt == nil {
		t.Fatal("Completed op must record completedAt")
	}
	// 未执行不计成功。
	if got.AllTargetsSucceeded() {
		t.Fatal("not_executed must not count as success")
	}

	// 目标上线：not_executed 无出边——旧意图不自动执行。
	err = store.TransitionTarget(ctx, opID, key, domain.TargetRunning, "", "", nil)
	if !errors.Is(err, domain.ErrOpState) {
		t.Fatalf("not_executed must have no outgoing edge, got %v", err)
	}
	err = store.TransitionTarget(ctx, opID, key, domain.TargetPending, "", "", nil)
	if !errors.Is(err, domain.ErrOpState) {
		t.Fatalf("not_executed → pending must be forbidden, got %v", err)
	}

	// unknown：结果丢失先核对，不允许原地改写为任何状态。
	op2, _, err := store.CreateOperation(ctx, installOp("sm-2", "0.44.0", tg))
	if err != nil {
		t.Fatal(err)
	}
	id2 := op2.Metadata.Name
	if err := store.TransitionTarget(ctx, id2, key, domain.TargetRunning, "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionTarget(ctx, id2, key, domain.TargetUnknown, domain.ReasonResultLost, "result lost", nil); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{domain.TargetSucceeded, domain.TargetFailed, domain.TargetRunning} {
		if err := store.TransitionTarget(ctx, id2, key, to, "", "", nil); !errors.Is(err, domain.ErrOpState) {
			t.Fatalf("unknown → %s must be forbidden, got %v", to, err)
		}
	}
	got2, _ := store.GetOperation(ctx, id2)
	if got2.AllTargetsSucceeded() {
		t.Fatal("unknown must not count as success")
	}

	// 白名单外的原因配对拒绝（unknown 必须带未知类原因）。
	op3, _, err := store.CreateOperation(ctx, installOp("sm-3", "0.44.0", tg))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionTarget(ctx, op3.Metadata.Name, key, domain.TargetUnknown, domain.ReasonTargetOffline, "", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown with offline reason must be rejected, got %v", err)
	}
	// succeeded 只接受核验原因。
	if err := store.TransitionTarget(ctx, op3.Metadata.Name, key, domain.TargetSucceeded, domain.ReasonOpFailed, "", nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("succeeded with op-failed reason must be rejected, got %v", err)
	}

	// 正常路径：pending → running → succeeded（证据持久化，StartedAt 记录）。
	now := time.Now().UTC()
	ev := &domain.LifecycleEvidence{ObservedVersion: "0.44.0", ExecutablePath: "/usr/local/bin/codex",
		Method: "npm", InventorySeq: 9, Message: "re-detected", RecordedAt: now}
	if err := store.TransitionTarget(ctx, op3.Metadata.Name, key, domain.TargetRunning, "", "", nil); err != nil {
		t.Fatal(err)
	}
	got3, _ := store.GetOperation(ctx, op3.Metadata.Name)
	if got3.Status.Phase != domain.OperationPhaseRunning || got3.Status.StartedAt == nil {
		t.Fatalf("running op must have phase=Running and startedAt, got %s %v", got3.Status.Phase, got3.Status.StartedAt)
	}
	if err := store.TransitionTarget(ctx, op3.Metadata.Name, key, domain.TargetSucceeded, domain.ReasonVerifiedPresent, "", ev); err != nil {
		t.Fatal(err)
	}
	got3, _ = store.GetOperation(ctx, op3.Metadata.Name)
	r := got3.TargetByKey(key)
	if r == nil || r.State != domain.TargetSucceeded || r.Evidence == nil ||
		r.Evidence.ObservedVersion != "0.44.0" || r.Evidence.InventorySeq != 9 {
		t.Fatalf("evidence must persist: %+v", r)
	}
	if !got3.AllTargetsSucceeded() {
		t.Fatal("verified success must count")
	}

	// 未知目标 / 未知操作：ErrNotFound。
	if err := store.TransitionTarget(ctx, op3.Metadata.Name, "m1\x1fnative\x1falice\x1fcodex\x1fbrew\x1fother", domain.TargetFailed, domain.ReasonOpFailed, "", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign target must be NotFound, got %v", err)
	}
	if err := store.TransitionTarget(ctx, "no-such-op", key, domain.TargetFailed, domain.ReasonOpFailed, "", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign op must be NotFound, got %v", err)
	}
}

// —— 重试：仅确定失败目标可重试，追溯链持久化 ——

func TestLifecycleRetryChain(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewLifecycleStore(db)
	tg := codexTarget("m1", "alice")

	original, _, err := store.CreateOperation(ctx, installOp("rt-1", "0.44.0", tg))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionTarget(ctx, original.Metadata.Name, tg.Key(),
		domain.TargetFailed, domain.ReasonOpFailed, "npm exited 1", nil); err != nil {
		t.Fatal(err)
	}

	retry := installOp("rt-1-retry", "0.44.0", tg)
	retry.RetryOf = original.Metadata.Name
	stored, created, err := store.CreateOperation(ctx, retry)
	if err != nil || !created {
		t.Fatalf("retry create: created=%v err=%v", created, err)
	}
	if stored.RetryOf != original.Metadata.Name {
		t.Fatalf("retry must link original, got %q", stored.RetryOf)
	}

	// 追溯链可查。
	children, err := store.ListRetryChildren(ctx, original.Metadata.Name)
	if err != nil || len(children) != 1 || children[0].Metadata.Name != stored.Metadata.Name {
		t.Fatalf("retry chain: %v %+v", err, children)
	}

	// 只允许重试确定失败：unknown / not_executed / succeeded 都拒绝。
	for _, tc := range []struct {
		state, reason string
	}{
		{domain.TargetUnknown, domain.ReasonResultLost},
		{domain.TargetNotExecuted, domain.ReasonTargetOffline},
		{domain.TargetSucceeded, domain.ReasonVerifiedPresent},
	} {
		base, _, err := store.CreateOperation(ctx, installOp("rt-base-"+tc.state, "0.44.0", tg))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.TransitionTarget(ctx, base.Metadata.Name, tg.Key(), tc.state, tc.reason, "", nil); err != nil {
			t.Fatal(err)
		}
		r := installOp("rt-retry-"+tc.state, "0.44.0", tg)
		r.RetryOf = base.Metadata.Name
		if _, _, err := store.CreateOperation(ctx, r); !errors.Is(err, domain.ErrRetryNotAllowed) {
			t.Fatalf("retry of %s must be rejected, got %v", tc.state, err)
		}
	}

	// 改意图（换版本）必须新建操作，不得借重试通道。
	wrongVersion := installOp("rt-1-wrong", "0.45.0", tg)
	wrongVersion.RetryOf = original.Metadata.Name
	if _, _, err := store.CreateOperation(ctx, wrongVersion); !errors.Is(err, domain.ErrRetryNotAllowed) {
		t.Fatalf("retry with changed version must be rejected, got %v", err)
	}
	// 原操作不存在。
	ghost := installOp("rt-ghost", "0.44.0", tg)
	ghost.RetryOf = "no-such-op"
	if _, _, err := store.CreateOperation(ctx, ghost); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("retry of missing original must be rejected, got %v", err)
	}
}

// —— 确认与重新确认 ——

func TestLifecycleConfirmAndReconfirm(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewLifecycleStore(db)
	tg := codexTarget("m1", "alice")

	original, _, err := store.CreateOperation(ctx, installOp("cf-1", "0.44.0", tg))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionTarget(ctx, original.Metadata.Name, tg.Key(),
		domain.TargetFailed, domain.ReasonOpFailed, "", nil); err != nil {
		t.Fatal(err)
	}
	retry := installOp("cf-1-retry", "0.44.0", tg)
	retry.RetryOf = original.Metadata.Name
	stored, _, err := store.CreateOperation(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}

	// 重发现影响变化：置位需重新确认（带原因）。
	if err := store.SetNeedsReconfirmation(ctx, stored.Metadata.Name, true, "重发现到目标已是 0.45.0，与固定版本 0.44.0 的前提不符"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetOperation(ctx, stored.Metadata.Name)
	if !got.Status.NeedsReconfirmation || got.Status.ReconfirmReason == "" {
		t.Fatalf("reconfirm flag must persist: %+v", got.Status)
	}

	// 操作者重新确认：标记清除、确认时刻记录。
	if err := store.MarkConfirmed(ctx, stored.Metadata.Name, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetOperation(ctx, stored.Metadata.Name)
	if got.Status.NeedsReconfirmation || got.Status.ConfirmedAt == nil {
		t.Fatalf("confirm must clear reconfirm flag and record time: %+v", got.Status)
	}

	// 未知操作：ErrNotFound。
	if err := store.MarkConfirmed(ctx, "no-such-op", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("confirm foreign op must be NotFound, got %v", err)
	}
	if err := store.SetNeedsReconfirmation(ctx, "no-such-op", true, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reconfirm foreign op must be NotFound, got %v", err)
	}
}

// —— 按机器列出操作 ——

func TestLifecycleListByMachine(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewLifecycleStore(db)

	op1, _, err := store.CreateOperation(ctx, installOp("ls-1", "0.44.0", codexTarget("m1", "alice")))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateOperation(ctx, installOp("ls-2", "0.44.0", codexTarget("m2", "bob"))); err != nil {
		t.Fatal(err)
	}
	multi, _, err := store.CreateOperation(ctx, installOp("ls-3", "0.44.0",
		codexTarget("m1", "alice"), codexTarget("m2", "bob")))
	if err != nil {
		t.Fatal(err)
	}

	ops, err := store.ListOperationsByMachine(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Metadata.Name != op1.Metadata.Name || ops[1].Metadata.Name != multi.Metadata.Name {
		t.Fatalf("m1 must list exactly its two ops in order, got %d", len(ops))
	}
	// 按幂等键取回。
	byKey, err := store.GetOperationByIdempotencyKey(ctx, "ls-2")
	if err != nil || byKey.Metadata.Name == "" || byKey.Spec.IdempotencyKey != "ls-2" {
		t.Fatalf("get by idempotency key: %v %+v", err, byKey)
	}
	if _, err := store.GetOperationByIdempotencyKey(ctx, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing key must be NotFound, got %v", err)
	}
}

// —— 能力判定快照持久化（审计"创建时的矩阵判定"）——

func TestLifecycleCapabilitySnapshotPersisted(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	store := NewLifecycleStore(db)
	// 阻塞组合在创建即拒绝（KM-46 §7：binary 通道）。
	blocked := codexTarget("m1", "alice")
	blocked.Method = "binary"
	op := installOp("cap-1", "0.44.0", blocked)
	if _, _, err := store.CreateOperation(ctx, op); !errors.Is(err, domain.ErrUnsupportedCombo) {
		t.Fatalf("blocked combo must be rejected at creation, got %v", err)
	}
	// 未验证组合：创建放行，判定快照入列。
	stored, _, err := store.CreateOperation(ctx, installOp("cap-2", "0.44.0", codexTarget("m1", "alice")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetOperation(ctx, stored.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	tr := got.TargetByKey(codexTarget("m1", "alice").Key())
	if tr == nil || tr.Target.CapabilityState != domain.CapabilityUnverified || tr.Target.CapabilityReason == "" {
		t.Fatalf("capability snapshot must persist: %+v", tr)
	}
}
