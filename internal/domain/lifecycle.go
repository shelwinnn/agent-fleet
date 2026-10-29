// 生命周期操作模型（KM-47；依据 docs/superpowers/specs/2026-10-27-codex-lifecycle-spec.md）：
//
//   - 期望状态显式三态：存在（present）、缺席（absent）、停止管理（unmanaged）。
//     省略绝不构成隐式卸载——意图只能由显式条目表达（MergeLifecycleDesired）。
//   - 生命周期操作以幂等键创建为持久操作；重复提交同一请求复用同一操作。
//   - 逐目标持久化状态与证据：成功、失败、未执行、未知各带原因；未知不计成功、
//     不自动重试；提交前离线标记未执行，上线后不自动执行旧意图（状态机白名单）。
//   - 仅确定失败的目标可重试；重试操作经 RetryOf 关联原操作保留追溯链。
//   - 安装/升级在创建操作时固定精确版本；操作记录不含秘密。
//   - 能力矩阵（KM-46 §7）模型化：未验证不标支持；明确阻塞的组合在创建时拒绝。
//
// 本模型是 T4 执行引擎与 T5 前端共同消费的契约来源；执行与编排不在本层。
package domain

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LifecycleDesiredSchemaVersion 是生命周期意图文档的 schema 版本。
const LifecycleDesiredSchemaVersion = "lifecycle-v1"

// LifecycleIntent 是期望状态的三态取值（spec：明确区分存在、缺席和停止管理）。
// 没有"未指定"或"省略"值：未出现在文档中的 agent 不携带任何意图，
// 尤其不得解释为 absent（防隐式卸载，MergeLifecycleDesired 是唯一合并入口）。
const (
	LifecycleIntentPresent   = "present"   // 应存在：Version 必须是精确固定版本
	LifecycleIntentAbsent    = "absent"    // 应消失：按已识别来源卸载
	LifecycleIntentUnmanaged = "unmanaged" // 停止管理：保留程序，仅解除纳管
)

// LifecycleOpType 是生命周期操作的类型（与 agentlocal/lifecycle 的 Op 对齐）。
// CheckUpdate 只读上游，不变更目标环境。
const (
	LifecycleOpInstall     = "Install"
	LifecycleOpUpgrade     = "Upgrade"
	LifecycleOpUninstall   = "Uninstall"
	LifecycleOpCheckUpdate = "CheckUpdate"
)

// LifecycleTargetState 是逐目标状态机的状态。pending/running 是执行前与执行中
// 的瞬态；四种结果态（spec：至少区分成功、失败、未执行、未知）各带原因。
// not_executed 与 unknown 没有出边：目标上线不自动执行旧意图；未知只能通过
// 创建新操作（核对或重试）离开，永不原地改写。
const (
	TargetPending     = "pending"
	TargetRunning     = "running"
	TargetSucceeded   = "succeeded"
	TargetFailed      = "failed"
	TargetNotExecuted = "not_executed"
	TargetUnknown     = "unknown"
)

// 逐目标原因码（四态各带原因；succeeded 的原因由核验方式决定）。
const (
	ReasonTargetOffline    = "target-offline"    // not_executed：提交前离线
	ReasonDispatchFailed   = "dispatch-failed"   // not_executed：派发不可达
	ReasonResultLost       = "result-lost"       // unknown：结果丢失，先核对
	ReasonNodeDisconnected = "node-disconnected" // unknown：执行中节点失联
	ReasonTimeoutNoResult  = "timeout-no-result" // unknown：超时且无结果回报
	ReasonVersionMismatch  = "version-mismatch"  // failed：重发现版本 ≠ 固定版本
	ReasonDiscoveryFailed  = "discovery-failed"  // failed：操作后重发现失败（零值/空 inventory 不是"未安装"）
	ReasonOpFailed         = "op-failed"         // failed：执行命令失败
	ReasonUnsupportedCombo = "unsupported-combo" // failed：OS/架构/来源/方式组合被拒
	ReasonSourceUnknown    = "source-unknown"    // 卸载被拒：来源未识别
	ReasonVerifiedPresent  = "verified-present"  // succeeded：重发现到固定版本
	ReasonVerifiedAbsent   = "verified-absent"   // succeeded：重发现实例确已消失
	ReasonImpactChanged    = "impact-changed"    // 重试需重新确认：实际状态与原意图前提不一致
)

// 生命周期模型新增哨兵错误（HTTP 层映射为 reason code 与状态码）。
var (
	// ErrIdempotencyConflict 表示同一幂等键携带了不同内容的请求。
	ErrIdempotencyConflict = fmt.Errorf("idempotency key reused with different request")
	// ErrRetryNotAllowed 表示重试请求指向了非确定失败的目标或意图不一致。
	ErrRetryNotAllowed = fmt.Errorf("retry not allowed for target")
	// ErrUnsupportedCombo 表示目标的 OS/架构/来源/安装方式组合被能力矩阵明确阻塞。
	ErrUnsupportedCombo = fmt.Errorf("lifecycle capability blocked for combo")
)

// capability 状态取值（spec：未验证不标为支持）。
const (
	CapabilitySupported  = "supported"  // 有真机证据（当前为空集，见 T8）
	CapabilityUnverified = "unverified" // 未验证：如实展示，执行前由引擎拒绝
	CapabilityBlocked    = "blocked"    // 明确阻塞：创建操作即拒绝
)

// CapabilityEvidenceLevel 是证据级别（supported 必须有 real_machine 证据；
// isolated 表示隔离环境已验证、真机待验收——对应 KM-46 的 GO（有条件））。
const (
	EvidenceNone        = "none"
	EvidenceIsolated    = "isolated"
	EvidenceRealMachine = "real_machine"
)

// LifecycleTarget 是生命周期操作的最小目标身份（spec：主机、OS 环境、受管用户、
// 明确安装实例）。同一主机的 Windows 原生与 WSL 以不同 Env 区分。Method+Package
// 是安装来源身份：卸载必须按已识别来源执行，来源未知在创建时即被拒绝。
type LifecycleTarget struct {
	Machine string `json:"machine"`
	// Env 是 OS 执行环境；空值在 Normalize 时归一为 "native"。
	Env     string `json:"env,omitempty"`
	User    string `json:"user"`
	Agent   string `json:"agent"`
	Method  string `json:"method"`
	Package string `json:"package"`
	// OS/Arch 是创建时从机器台账取得的平台事实（不参与身份），用于能力矩阵
	// 判定与审计："当时以为是哪个平台"。
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
	// CapabilityState/Reason 是创建时按能力矩阵快照的判定结果（不参与身份），
	// 让执行层与审计能看到"创建时该组合是未验证/已验证"。
	CapabilityState  string `json:"capabilityState,omitempty"`
	CapabilityReason string `json:"capabilityReason,omitempty"`
}

// Normalize 归一目标字段（Env 空值 → native，两端去空白）。返回副本。
func (t LifecycleTarget) Normalize() LifecycleTarget {
	t.Machine = strings.TrimSpace(t.Machine)
	t.Env = strings.TrimSpace(t.Env)
	if t.Env == "" {
		t.Env = "native"
	}
	t.User = strings.TrimSpace(t.User)
	t.Agent = strings.TrimSpace(t.Agent)
	t.Method = strings.TrimSpace(t.Method)
	t.Package = strings.TrimSpace(t.Package)
	return t
}

// Key 返回目标身份键（单元分隔符拼接，避免字段值碰撞）。
func (t LifecycleTarget) Key() string {
	return strings.Join([]string{t.Machine, t.Env, t.User, t.Agent, t.Method, t.Package}, "\x1f")
}

// Validate 校验目标身份的必填项（spec：最小目标身份 + 卸载按已识别来源）。
func (t LifecycleTarget) Validate() error {
	t = t.Normalize()
	if t.Machine == "" {
		return fmt.Errorf("%w: lifecycle target requires machine", ErrInvalid)
	}
	if t.User == "" {
		return fmt.Errorf("%w: lifecycle target %s requires user", ErrInvalid, t.Key())
	}
	if t.Agent == "" {
		return fmt.Errorf("%w: lifecycle target %s requires agent", ErrInvalid, t.Key())
	}
	if t.Method == "" {
		return fmt.Errorf("%w: lifecycle target %s requires install method (source must be identified)", ErrInvalid, t.Key())
	}
	if t.Package == "" {
		return fmt.Errorf("%w: lifecycle target %s requires package", ErrInvalid, t.Key())
	}
	return nil
}

// LifecycleEvidence 是逐目标证据（不含秘密：上游输出限长脱敏后放 Message，
// 版本是操作后重发现的实测值——上游返回值不采信，KM-46 零值陷阱）。
type LifecycleEvidence struct {
	ObservedVersion string    `json:"observedVersion,omitempty"`
	ExecutablePath  string    `json:"executablePath,omitempty"`
	Method          string    `json:"method,omitempty"`
	InventorySeq    int64     `json:"inventorySeq,omitempty"`
	Message         string    `json:"message,omitempty"` // 限长脱敏
	RecordedAt      time.Time `json:"recordedAt,omitempty"`
}

// MaxEvidenceMessageLen 是证据消息的长度上限（超限拒绝，不静默截断）。
const MaxEvidenceMessageLen = 2000

// sensitiveRe 抹掉证据文本里 token 形态的值（npm 配置/授权头可能回显）。
// 值统一替换为 [redacted]；键名保留供定位。
var sensitiveRe = regexp.MustCompile(`(?i)(token|authorization|password|secret|_auth)(\s*[=:]\s*)("[^"]*"|\S+)`)

// RedactEvidence 是证据文本的统一脱敏（KM-47 契约：message 限长脱敏，写入前
// 由上报方执行）。节点侧（上报前）与控制面（入库前兜底）共用同一实现——
// 脱敏规则只有一份，不会漂移。
func RedactEvidence(s string) string {
	return sensitiveRe.ReplaceAllString(s, "$1$2[redacted]")
}

// LifecycleTargetResult 是一个目标的持久化状态与证据。Reason 规则：失败、
// 未执行、未知必须非空；成功必须为两个核验原因之一；pending 不带原因。
type LifecycleTargetResult struct {
	Target   LifecycleTarget    `json:"target"`
	State    string             `json:"state"`
	Reason   string             `json:"reason,omitempty"`
	Message  string             `json:"message,omitempty"`
	Evidence *LifecycleEvidence `json:"evidence,omitempty"`
}

// CountsAsSuccess 报告该结果是否计为成功。未知与未执行都不是成功
// （spec：未知不计成功；未执行 = 意图未发生）。
func (r LifecycleTargetResult) CountsAsSuccess() bool {
	return r.State == TargetSucceeded
}

// 各结果态的合法原因集合（四态各带原因；域内白名单，仓储写入前校验）。
var lifecycleReasons = map[string]map[string]bool{
	TargetSucceeded:   {ReasonVerifiedPresent: true, ReasonVerifiedAbsent: true},
	TargetFailed:      {ReasonVersionMismatch: true, ReasonDiscoveryFailed: true, ReasonOpFailed: true, ReasonUnsupportedCombo: true},
	TargetNotExecuted: {ReasonTargetOffline: true, ReasonDispatchFailed: true},
	TargetUnknown:     {ReasonResultLost: true, ReasonNodeDisconnected: true, ReasonTimeoutNoResult: true},
	TargetPending:     {"": true},
	TargetRunning:     {"": true},
}

// ValidateTargetResult 校验状态与原因的配对（未知必须写明原因——它不是成功，
// 也不得无声无息）。
func ValidateTargetResult(state, reason string) error {
	allowed, ok := lifecycleReasons[state]
	if !ok {
		return fmt.Errorf("%w: unknown lifecycle target state %q", ErrInvalid, state)
	}
	if !allowed[reason] {
		return fmt.Errorf("%w: state %q does not allow reason %q", ErrInvalid, state, reason)
	}
	return nil
}

// lifecycleTargetTransitions 是逐目标状态机的白名单。不在表中的状态没有出边：
// succeeded/failed 是终态；not_executed 上线不复活；unknown 只能经新操作离开。
var lifecycleTargetTransitions = map[string][]string{
	TargetPending: {TargetRunning, TargetSucceeded, TargetFailed, TargetNotExecuted, TargetUnknown},
	TargetRunning: {TargetSucceeded, TargetFailed, TargetUnknown},
}

// TargetTransitionAllowed 报告 from→to 是否为合法迁移。
func TargetTransitionAllowed(from, to string) bool {
	for _, to2 := range lifecycleTargetTransitions[from] {
		if to2 == to {
			return true
		}
	}
	return false
}

// IsTargetResultState 报告 state 是否为四种结果态之一。
func IsTargetResultState(state string) bool {
	switch state {
	case TargetSucceeded, TargetFailed, TargetNotExecuted, TargetUnknown:
		return true
	}
	return false
}

// LifecycleOperationSpec 是生命周期操作的不可变规格。IdempotencyKey 由调用方
// 提供（建议 UUID），同一键的重复创建复用同一操作；Version 仅 Install/Upgrade
// 允许且必须是创建时固定的精确版本。
type LifecycleOperationSpec struct {
	Type           string            `json:"type"`
	IdempotencyKey string            `json:"idempotencyKey"`
	Version        string            `json:"version,omitempty"`
	Targets        []LifecycleTarget `json:"targets"`
	CreatedBy      string            `json:"createdBy,omitempty"`
}

// LifecycleOperationStatus 是逐目标状态集合与操作级相位。Phase 由仓储按目标
// 状态推导：任一目标 running → Running；全部处于结果态 → Completed。
type LifecycleOperationStatus struct {
	Phase   string                  `json:"phase"`
	Results []LifecycleTargetResult `json:"results"`
	// ConfirmedAt 是操作者预览确认时刻；nil 表示尚未确认（执行层不得派发）。
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
	// NeedsReconfirmation 是重试前重发现发现影响变化时的标记（spec：影响变化
	// 时重新确认）。置位后执行层不得派发，直到操作者重新确认。
	NeedsReconfirmation bool `json:"needsReconfirmation,omitempty"`
	// ReconfirmReason 是置位 NeedsReconfirmation 时的原因（ReasonImpactChanged
	// 语义的人读说明，如"重发现到目标已是 0.45.0，与固定版本 0.44.0 的前提不符"）。
	ReconfirmReason string     `json:"reconfirmReason,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
}

// LifecycleOperation 是一次生命周期意图的持久操作（含重试追溯链）。
type LifecycleOperation struct {
	Metadata ObjectMeta               `json:"metadata"`
	Spec     LifecycleOperationSpec   `json:"spec"`
	Status   LifecycleOperationStatus `json:"status"`
	// RetryOf 是本操作重试的原操作 id；空表示首次创建。
	RetryOf string `json:"retryOf,omitempty"`
}

// TargetByKey 按目标身份键取结果；不存在返回 nil。
func (op *LifecycleOperation) TargetByKey(key string) *LifecycleTargetResult {
	for i := range op.Status.Results {
		if op.Status.Results[i].Target.Key() == key {
			return &op.Status.Results[i]
		}
	}
	return nil
}

// AllTargetsSucceeded 报告是否所有目标都成功（未知、未执行、失败都使整体
// 不成立——未知不计成功）。空目标集返回 false。
func (op *LifecycleOperation) AllTargetsSucceeded() bool {
	if len(op.Status.Results) == 0 {
		return false
	}
	for _, r := range op.Status.Results {
		if !r.CountsAsSuccess() {
			return false
		}
	}
	return true
}

// exactVersionRe 与 agentlocal/lifecycle（KM-46 窄适配层）的精确版本闸同一形态：
// x.y.z 可带 -prerelease / +build；拒绝 v 前缀、range、latest、*、不完整三段式。
var exactVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

// IsExactVersion 报告 v 是否为可固定的精确版本。
func IsExactVersion(v string) bool { return exactVersionRe.MatchString(v) }

// ValidateLifecycleEntry 校验一条显式意图。空 intent 非法（不默认成任何状态）。
func ValidateLifecycleEntry(agent string, e LifecycleEntry) error {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return fmt.Errorf("%w: lifecycle entry requires agent", ErrInvalid)
	}
	switch e.Intent {
	case LifecycleIntentPresent:
		if !IsExactVersion(e.Version) {
			return fmt.Errorf("%w: agent %q present requires exact pinned version, got %q", ErrInvalid, agent, e.Version)
		}
		if e.Method == "" || e.Package == "" {
			return fmt.Errorf("%w: agent %q present requires install source (method+package)", ErrInvalid, agent)
		}
	case LifecycleIntentAbsent:
		// 卸载必须按已识别来源执行（spec：来源未知时拒绝卸载）；缺席意图同样
		// 需要来源身份，版本必须为空（absent 不指向任何版本）。
		if e.Version != "" {
			return fmt.Errorf("%w: agent %q absent must not carry a version", ErrInvalid, agent)
		}
		if e.Method == "" || e.Package == "" {
			return fmt.Errorf("%w: agent %q absent requires identified install source (method+package)", ErrInvalid, agent)
		}
	case LifecycleIntentUnmanaged:
		if e.Version != "" {
			return fmt.Errorf("%w: agent %q unmanaged must not carry a version", ErrInvalid, agent)
		}
	default:
		return fmt.Errorf("%w: agent %q intent %q must be one of present|absent|unmanaged", ErrInvalid, agent, e.Intent)
	}
	return nil
}

// LifecycleEntry 是一个 agent 的显式生命周期意图。省略条目不构成意图：
// 合并时未提及的 agent 原样保留，唯一解除纳管的路径是显式 unmanaged。
type LifecycleEntry struct {
	Intent string `json:"intent"`
	// Version 仅 present 允许，且必须是精确固定版本。
	Version string `json:"version,omitempty"`
	// Method/Package 是安装来源身份；present/absent 必填（卸载按来源执行）。
	Method  string `json:"method,omitempty"`
	Package string `json:"package,omitempty"`
}

// LifecycleDesired 是一台机器的显式生命周期意图文档。Entries 按 agent 家族键；
// 文档只增不改地表达意图变化（变化轨迹由不可变代次承载，见仓储）。
type LifecycleDesired struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Entries       map[string]LifecycleEntry `json:"entries"`
}

// ValidateLifecycleDesired 校验整份文档：schema 版本与逐条目合法性。
func ValidateLifecycleDesired(d LifecycleDesired) error {
	if d.SchemaVersion != LifecycleDesiredSchemaVersion {
		return fmt.Errorf("%w: lifecycle desired schemaVersion %q want %q", ErrInvalid, d.SchemaVersion, LifecycleDesiredSchemaVersion)
	}
	agents := make([]string, 0, len(d.Entries))
	for a := range d.Entries {
		agents = append(agents, a)
	}
	sort.Strings(agents)
	for _, a := range agents {
		if err := ValidateLifecycleEntry(a, d.Entries[a]); err != nil {
			return err
		}
	}
	return nil
}

// MergeLifecycleDesired 把 update 合并进 current（防隐式卸载的唯一合并语义）：
// update 提及的 agent 显式替换；未提及的原样保留；没有任何路径能把条目删除
// 或降级为 absent——解除纳管只能显式声明 unmanaged。返回合并结果与是否有变化。
// current 为空文档表示首次纳管。
func MergeLifecycleDesired(current, update LifecycleDesired) (LifecycleDesired, bool, error) {
	if err := ValidateLifecycleDesired(update); err != nil {
		return LifecycleDesired{}, false, err
	}
	merged := LifecycleDesired{SchemaVersion: LifecycleDesiredSchemaVersion, Entries: map[string]LifecycleEntry{}}
	for a, e := range current.Entries {
		merged.Entries[a] = e
	}
	for a, e := range update.Entries {
		merged.Entries[a] = e
	}
	// 变化判定：逐 agent 逐字段比较（map 无序，先比较键集合再比较值）。
	changed := len(merged.Entries) != len(current.Entries)
	if !changed {
		for a, e := range merged.Entries {
			if cur, ok := current.Entries[a]; !ok || cur != e {
				changed = true
				break
			}
		}
	}
	return merged, changed, nil
}

// ValidateLifecycleOperation 校验操作规格的创建时不变量（版本固定、目标身份、
// 类型约束、来源识别、能力阻塞）。重试专属规则在 ValidateLifecycleRetry。
func ValidateLifecycleOperation(op *LifecycleOperation) error {
	if op == nil {
		return fmt.Errorf("%w: lifecycle operation required", ErrInvalid)
	}
	switch op.Spec.Type {
	case LifecycleOpInstall, LifecycleOpUpgrade, LifecycleOpUninstall, LifecycleOpCheckUpdate:
	default:
		return fmt.Errorf("%w: unknown lifecycle operation type %q", ErrInvalid, op.Spec.Type)
	}
	key := strings.TrimSpace(op.Spec.IdempotencyKey)
	if key == "" || len(key) > 128 {
		return fmt.Errorf("%w: idempotencyKey required (1-128 chars)", ErrInvalid)
	}
	if len(op.Spec.Targets) == 0 {
		return fmt.Errorf("%w: lifecycle operation requires at least one target", ErrInvalid)
	}
	switch op.Spec.Type {
	case LifecycleOpInstall, LifecycleOpUpgrade:
		if !IsExactVersion(op.Spec.Version) {
			return fmt.Errorf("%w: %s requires exact version pinned at creation, got %q", ErrInvalid, op.Spec.Type, op.Spec.Version)
		}
	case LifecycleOpUninstall, LifecycleOpCheckUpdate:
		if op.Spec.Version != "" {
			return fmt.Errorf("%w: %s must not carry a version", ErrInvalid, op.Spec.Type)
		}
	}
	seen := map[string]bool{}
	for _, t := range op.Spec.Targets {
		t = t.Normalize()
		if err := t.Validate(); err != nil {
			return err
		}
		if op.Spec.Type == LifecycleOpUninstall && (t.Method == "" || t.Package == "") {
			return fmt.Errorf("%w: uninstall target %s requires identified source", ErrInvalid, t.Key())
		}
		if seen[t.Key()] {
			return fmt.Errorf("%w: duplicate lifecycle target %s", ErrInvalid, t.Key())
		}
		seen[t.Key()] = true
		if err := CheckTargetCapability(t); err != nil {
			return err
		}
	}
	return nil
}

// ValidateLifecycleRetry 校验重试操作与原操作的追溯一致性（spec：仅重试确定
// 失败的目标；重试是同一意图，改意图必须新建操作）。调用方已保证 original 为
// 已持久化的原操作。
func ValidateLifecycleRetry(retry, original *LifecycleOperation) error {
	if retry.RetryOf == "" {
		return fmt.Errorf("%w: retry operation must reference original via retryOf", ErrInvalid)
	}
	if retry.Spec.Type != original.Spec.Type {
		return fmt.Errorf("%w: retry type %s differs from original %s", ErrRetryNotAllowed, retry.Spec.Type, original.Spec.Type)
	}
	if retry.Spec.Version != original.Spec.Version {
		return fmt.Errorf("%w: retry version %q differs from original %q (changing intent requires a new operation)", ErrRetryNotAllowed, retry.Spec.Version, original.Spec.Version)
	}
	for _, t := range retry.Spec.Targets {
		tr := original.TargetByKey(t.Normalize().Key())
		if tr == nil {
			return fmt.Errorf("%w: target %s was not part of original operation", ErrRetryNotAllowed, t.Key())
		}
		if tr.State != TargetFailed {
			return fmt.Errorf("%w: target %s is %s, only definite failures are retryable", ErrRetryNotAllowed, t.Key(), tr.State)
		}
	}
	return nil
}

// OperationPhaseCompleted 是生命周期操作的全目标落定相位（区别于配置流水线
// 的机器级相位：生命周期操作的目标粒度状态才是权威）。
const OperationPhaseCompleted = "Completed"

// RecomputeOpPhase 按目标结果推导操作相位。
func RecomputeOpPhase(results []LifecycleTargetResult) string {
	if len(results) == 0 {
		return OperationPhasePending
	}
	running, done := false, 0
	for _, r := range results {
		switch r.State {
		case TargetRunning:
			running = true
		case TargetSucceeded, TargetFailed, TargetNotExecuted, TargetUnknown:
			done++
		}
	}
	switch {
	case running:
		return OperationPhaseRunning
	case done == len(results):
		return OperationPhaseCompleted
	default:
		return OperationPhasePending
	}
}

// LifecycleCapability 是一条 OS × 架构 × 安装方式的能力记录（KM-46 §7 能力
// 矩阵的模型化）。State=supported 必须有真机证据；未验证不得标支持。
type LifecycleCapability struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Method        string `json:"method"`
	State         string `json:"state"`
	EvidenceLevel string `json:"evidenceLevel,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// lifecycleCapabilityRegistry 是 KM-46 核查报告 §7 的数据快照（T2 创建时判定
// 与 T5 展示的事实来源）。它是**文档数据**而非支持声明：除 isolated 证据外
// 一律未验证；supported 集合为空，等 T8 真机验收后由后续工单登记。
// OS/Arch 的 "*" 表示跨平台组合（如 binary 通道的上游缺陷）。
var lifecycleCapabilityRegistry = []LifecycleCapability{
	{
		OS: "linux", Arch: "amd64", Method: "npm",
		State: CapabilityUnverified, EvidenceLevel: EvidenceIsolated,
		Reason: "KM-46: 隔离环境验证 GO（有条件：须经窄适配层）；真机全流程待 T4/T8 验收，未标支持",
	},
	{
		OS: "windows", Arch: "amd64", Method: "npm",
		State: CapabilityBlocked, EvidenceLevel: EvidenceNone,
		Reason: "KM-46 §7: 上游 Windows 平台层只认 .exe，npm/codex 的 .cmd shim 不可见；待 Windows 真机确认，属引擎级决策",
	},
	{
		OS: "darwin", Arch: "arm64", Method: "npm",
		State: CapabilityUnverified, EvidenceLevel: EvidenceNone,
		Reason: "KM-46: 交叉编译通过，行为零验证；darwin 真机全流程待验收",
	},
	{
		OS: "darwin", Arch: "arm64", Method: "brew",
		State: CapabilityUnverified, EvidenceLevel: EvidenceNone,
		Reason: "KM-46: brew 通道无固定版本缝隙，需先解决版本固定策略；行为零验证",
	},
	{
		OS: "*", Arch: "*", Method: "binary",
		State: CapabilityBlocked, EvidenceLevel: EvidenceNone,
		Reason: "KM-46 §7: catalog binary 条目缺安装 Command，上游遇空 Command 直接报错；如需该通道须另行授权自建",
	},
}

// LifecycleCapabilities 返回能力矩阵的快照副本（KM-46 §7 文档数据的只读出口，
// 供 GET /api/v1/lifecycle/capabilities 展示）。它是文档数据而非支持声明：
// supported 集合在 T8 真机证据前为空。
func LifecycleCapabilities() []LifecycleCapability {
	out := make([]LifecycleCapability, len(lifecycleCapabilityRegistry))
	copy(out, lifecycleCapabilityRegistry)
	return out
}

// LookupLifecycleCapability 查询组合能力：精确匹配优先，其次通配；无记录
// 一律按未验证处理（未验证不标支持——缺省即未验证）。
func LookupLifecycleCapability(os, arch, method string) LifecycleCapability {
	unknown := LifecycleCapability{OS: os, Arch: arch, Method: method,
		State: CapabilityUnverified, EvidenceLevel: EvidenceNone,
		Reason: "无能力记录：默认未验证，不标支持"}
	var wildcard *LifecycleCapability
	for i := range lifecycleCapabilityRegistry {
		c := &lifecycleCapabilityRegistry[i]
		if c.Method != method {
			continue
		}
		if c.OS == os && c.Arch == arch {
			return *c
		}
		if c.OS == "*" && c.Arch == "*" {
			wildcard = c
		}
	}
	if wildcard != nil {
		return *wildcard
	}
	return unknown
}

// CheckTargetCapability 在创建操作时判定目标组合：明确阻塞的组合拒绝创建
// （合法取值边界）；未验证组合允许创建但把判定快照进目标记录，执行前由
// T4 引擎按其验收标准再次拒绝。平台未知时仍按方法通配判定——跨平台阻塞
// （如 binary 通道的上游缺陷）不能靠省略 os/arch 绕过；仅平台特定的阻塞
// 组合在平台未知时无法判定，留待执行层拒绝。
func CheckTargetCapability(t LifecycleTarget) error {
	if t.OS == "" || t.Arch == "" {
		if wc := LookupLifecycleCapability("*", "*", t.Method); wc.State == CapabilityBlocked {
			return fmt.Errorf("%w: %s: %s", ErrUnsupportedCombo, t.Method, wc.Reason)
		}
		return nil
	}
	lc := LookupLifecycleCapability(t.OS, t.Arch, t.Method)
	if lc.State == CapabilityBlocked {
		return fmt.Errorf("%w: %s/%s+%s: %s", ErrUnsupportedCombo, t.OS, t.Arch, t.Method, lc.Reason)
	}
	return nil
}

// SnapshotCapability 把能力判定快照进目标副本（创建时由服务端调用，
// 让操作记录携带"当时的矩阵判定"；平台未知时快照方法通配判定）。
func SnapshotCapability(t LifecycleTarget) LifecycleTarget {
	if t.OS == "" || t.Arch == "" {
		wc := LookupLifecycleCapability("*", "*", t.Method)
		if wc.State == CapabilityBlocked {
			t.CapabilityState = wc.State
			t.CapabilityReason = wc.Reason
		}
		return t
	}
	lc := LookupLifecycleCapability(t.OS, t.Arch, t.Method)
	t.CapabilityState = lc.State
	t.CapabilityReason = lc.Reason
	return t
}

// LifecycleDesiredRecord 是持久化的一代生命周期意图文档（不可变，revision 为
// 身份、digest 为内容指纹；与配置期望快照同一代次模式，但独立演进互不触碰）。
type LifecycleDesiredRecord struct {
	Machine   string
	Revision  int64
	Digest    string
	Desired   LifecycleDesired
	CreatedAt time.Time
}

// LifecycleRepository 是生命周期意图与操作的仓储（KM-47）。幂等以持久层唯一
// 索引表达；逐目标状态机迁移以白名单 + 单事务 CAS 表达。
type LifecycleRepository interface {
	// CurrentDesired 返回机器当前意图文档（最高 revision）；从未纳管返回
	// ErrNotFound（"未纳管"是显式可查询的状态，不是 absent）。
	CurrentDesired(ctx context.Context, machine string) (*LifecycleDesiredRecord, error)
	// PutDesired 以合并语义落库一代新意图（MergeLifecycleDesired：未提及的
	// agent 原样保留）；内容无变化不新增代，返回当代表与 changed=false。
	PutDesired(ctx context.Context, machine string, update LifecycleDesired) (*LifecycleDesiredRecord, bool, error)
	// CreateOperation 以幂等键 get-or-create 一个持久操作。同一键重复提交
	// 返回既有操作（created=false），不产生重复变更；同一键不同内容返回
	// ErrIdempotencyConflict。RetryOf 非空时在同一事务内校验追溯一致性
	//（ValidateLifecycleRetry：仅确定失败目标、意图必须与原操作一致）。
	CreateOperation(ctx context.Context, op *LifecycleOperation) (stored *LifecycleOperation, created bool, err error)
	// GetOperation 按 id 取操作（含逐目标状态与证据）。
	GetOperation(ctx context.Context, id string) (*LifecycleOperation, error)
	// GetOperationByIdempotencyKey 按幂等键取操作；不存在返回 ErrNotFound。
	GetOperationByIdempotencyKey(ctx context.Context, key string) (*LifecycleOperation, error)
	// ListOperationsByMachine 列出涉及某机器的操作（创建时间序）。
	ListOperationsByMachine(ctx context.Context, machine string) ([]*LifecycleOperation, error)
	// ListUnfinishedOperations 列出相位未到 Completed 的操作（创建时间序）。
	// 执行编排（KM-49）据此扫描可派发的已确认操作与超时未归的目标；
	// not_executed/unknown/succeeded/failed 都算落定，不会无限滞留本列表。
	ListUnfinishedOperations(ctx context.Context) ([]*LifecycleOperation, error)
	// ListRetryChildren 返回以 id 为原操作的重试操作（追溯链的直接子代）。
	ListRetryChildren(ctx context.Context, id string) ([]*LifecycleOperation, error)
	// TransitionTarget 在单事务内做逐目标白名单迁移并重算操作相位。目标不存在
	// 返回 ErrNotFound；白名单外迁移返回 ErrOpState；状态-原因配对非法或证据
	// 超限返回 ErrInvalid。not_executed/unknown/succeeded/failed 无出边。
	TransitionTarget(ctx context.Context, opID, targetKey, to, reason, message string, ev *LifecycleEvidence) error
	// SetNeedsReconfirmation 置位/清除重试需重新确认标记（重发现影响变化时
	// 置位；操作者重新确认时经 MarkConfirmed 清除）。
	SetNeedsReconfirmation(ctx context.Context, opID string, needs bool, reason string) error
	// MarkConfirmed 记录操作者确认时刻并清除需重新确认标记（同一已批准操作的
	// 重试不重复确认；影响变化后必须重新确认才能派发）。
	MarkConfirmed(ctx context.Context, opID string, at time.Time) error
}
