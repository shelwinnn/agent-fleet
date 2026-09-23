// 健康门禁的因果绑定（架构 v1.1.2 §4.4 契约）：四条件必须与同一 machineId/
// operationId/目标代快照因果绑定。证据以**身份绑定（operationId）+ 序绑定
// （inventorySeq）**承载；本结构体中根本不存在"接收时间"字段——用类型系统
// 结构性地排除"以接收时间代替因果"与"缓存 status 冒充证据"的路径。
package deployment

import (
	"fmt"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// BoundObservation 是与某次操作因果绑定的 apply 后观测（门禁条件 2/3/4 的
// 唯一可接受证据形态）。不携带服务端接收时间：节点时钟不被信任，接收序不能
// 证明采集序（§4.4 明确不采纳时间比较）。
type BoundObservation struct {
	// OperationID 必须等于门禁所针对的操作 id（身份绑定）。周期上报的
	// inventory（无 operationId）不构成门禁证据。
	OperationID string
	// InventorySeq 是节点本地单调采集序（序绑定，仅单机内比较）。
	InventorySeq int64
	// ObservedProjectionDigest 是该份观测的受管投影摘要（判据右侧）。
	ObservedProjectionDigest string
	// DesiredProjectionDigest 是同一观测内、由节点按目标快照计算的期望侧摘要
	//（ADR-1 双侧投影；判据左侧，与操作 verify 证据交叉核对）。
	DesiredProjectionDigest string
	CanonicalizationVersion string
	// AdapterHealth 是同一份观测中的适配器健康结果。
	AdapterHealth string
}

// GateInput 是门禁评估的全部输入。
type GateInput struct {
	MachineID        string
	TargetGeneration int64 // 有效目标代（回滚 = 每机物化后的新代）
	// Op 是该目标本次派发的操作终态记录（nil 即无操作记录 → 不通过）。
	Op *domain.Operation
	// PostApply 是与该操作绑定的 apply 后观测（nil 即无绑定观测 → 不通过）。
	PostApply *BoundObservation
}

// GateResult 是门禁判定。
type GateResult struct {
	Passed          bool
	FailedCondition int    // 1–4（§4.4 表）
	Reason          string // 不通过的原因（人读 + 审计）
}

// EvaluateGate 逐条评估门禁四条件（缺一不可，FR-10.3）：
//
//  1. 操作成功：该 operationId 的 OperationResult{Succeeded} 且
//     desiredGeneration == 有效目标代；Succeeded(Superseded/Skipped) 不满足；
//     机器 status 中的历史 phase 不被接受。
//  2. apply 后 inventory 完成：与该操作同 operationId 绑定的观测（含
//     inventorySeq）；未标注 operationId 的周期 inventory 不被接受。
//  3. 投影摘要一致：以条件 2 那份观测计算的 observed 与目标代 desired 投影
//     摘要相等，且 canonicalizationVersion 一致；status 缓存的摘要不被接受。
//  4. 适配器健康通过：同一份观测（或该操作 verify 证据）的健康结果；
//     上一次操作的健康结果不被接受。
func EvaluateGate(in GateInput) GateResult {
	// —— 条件 1：操作成功（身份 + 代绑定）——
	if in.Op == nil {
		return GateResult{FailedCondition: 1, Reason: "no operation record bound to this target"}
	}
	if in.Op.Spec.Machine != in.MachineID {
		return GateResult{FailedCondition: 1, Reason: fmt.Sprintf(
			"operation %s belongs to %q, not %q", in.Op.Metadata.Name, in.Op.Spec.Machine, in.MachineID)}
	}
	if in.Op.Status.Phase != domain.OperationPhaseSucceeded || in.Op.Status.TerminalModifier != "" {
		return GateResult{FailedCondition: 1, Reason: fmt.Sprintf(
			"operation %s terminal state is %s(%s), not a plain Succeeded",
			in.Op.Metadata.Name, in.Op.Status.Phase, in.Op.Status.TerminalModifier)}
	}
	if in.Op.Spec.DesiredGeneration != in.TargetGeneration {
		return GateResult{FailedCondition: 1, Reason: fmt.Sprintf(
			"operation %s targeted generation %d, gate requires %d",
			in.Op.Metadata.Name, in.Op.Spec.DesiredGeneration, in.TargetGeneration)}
	}

	// —— 条件 2：apply 后 inventory 完成（身份绑定）——
	if in.PostApply == nil {
		return GateResult{FailedCondition: 2, Reason: "no post-apply observation bound to operation " + in.Op.Metadata.Name}
	}
	if in.PostApply.OperationID != in.Op.Metadata.Name {
		return GateResult{FailedCondition: 2, Reason: fmt.Sprintf(
			"observation is bound to operation %q, not %q; periodic inventory is not gate evidence",
			in.PostApply.OperationID, in.Op.Metadata.Name)}
	}

	// —— 条件 3：投影摘要一致 + 版本一致（FR-8.6/§7.1）——
	verify := in.Op.Status.Verify
	if verify == nil || verify.DesiredProjectionDigest == "" {
		return GateResult{FailedCondition: 3, Reason: "operation carries no verify evidence"}
	}
	// §7.1/§4.4 条件 3 的**权威判据**：期望侧受管投影摘要 == 观测侧受管投影摘要。
	// 只交叉核对"操作证据 vs 观测"两侧的一致性还不够——两边都报同一个漂移摘要
	// 时交叉核对会通过，但机器实际处于 drift（这正是 §7.1 要求先判摘要相等的原因）。
	if verify.DesiredProjectionDigest != verify.ObservedProjectionDigest {
		return GateResult{FailedCondition: 3, Reason: fmt.Sprintf(
			"observed managed projection differs from desired (%s vs %s): drift, gate requires equality",
			short(verify.ObservedProjectionDigest), short(verify.DesiredProjectionDigest))}
	}
	if verify.DesiredProjectionDigest != in.PostApply.DesiredProjectionDigest {
		return GateResult{FailedCondition: 3, Reason: fmt.Sprintf(
			"desired projection digest mismatch between operation evidence and observation (%s vs %s)",
			short(verify.DesiredProjectionDigest), short(in.PostApply.DesiredProjectionDigest))}
	}
	if verify.CanonicalizationVersion != in.PostApply.CanonicalizationVersion {
		return GateResult{FailedCondition: 3, Reason: fmt.Sprintf(
			"canonicalizationVersion mismatch (%q vs %q): not comparable, no verdict may be reused",
			verify.CanonicalizationVersion, in.PostApply.CanonicalizationVersion)}
	}
	if verify.ObservedProjectionDigest != in.PostApply.ObservedProjectionDigest {
		return GateResult{FailedCondition: 3, Reason: fmt.Sprintf(
			"observed projection digest mismatch between operation evidence and observation (%s vs %s)",
			short(verify.ObservedProjectionDigest), short(in.PostApply.ObservedProjectionDigest))}
	}

	// —— 条件 4：适配器健康通过（同一份证据；观测与操作步骤 10 任一报告失败
	// 即不通过——证据冲突时取严，绝不放行）——
	health := in.PostApply.AdapterHealth
	if health == "" {
		health = verify.AdapterHealth
	}
	if health != domain.AdapterHealthPassed ||
		verify.AdapterHealth == domain.AdapterHealthFailed {
		return GateResult{FailedCondition: 4, Reason: fmt.Sprintf(
			"adapter health is %q (verify=%q), need %q (evidence must come from the same operation/observation)",
			health, verify.AdapterHealth, domain.AdapterHealthPassed)}
	}

	return GateResult{Passed: true}
}

func short(s string) string {
	if len(s) > 19 {
		return s[:19] + "…"
	}
	return s
}
