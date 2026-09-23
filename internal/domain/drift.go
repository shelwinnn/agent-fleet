package domain

// Drift 三态的 Unknown reason（FR-1.9/§6.2：从未采集 / 采集已过期 / 期望代已变
// 且尚未按新代采集——三种情形必须 Unknown，不得以 False 冒充"已确认一致"）。
const (
	DriftReasonNeverInventoried         = "NeverInventoried"
	DriftReasonStaleObservation         = "StaleObservation"
	DriftReasonObservationPredatesDesir = "ObservationPredatesDesired"
)

// ProjectionVersionMismatch 是 Agent 错误组码（§7.1 v1.1 增补）：比较双方
// canonicalizationVersion 不一致时不产生 drift 判定，报此码并把 Drifted 置 Unknown。
const ProjectionVersionMismatch = "ProjectionVersionMismatch"

// Reconcile 错误组码（本切片使用到的 §30.3 子集；v1.1 增补与 v1.1.2 补入项）。
const (
	ReasonDesiredStateInvalid       = "DesiredStateInvalid"
	ReasonReplanRequired            = "ReplanRequired"
	ReasonVerifyFailed              = "VerifyFailed"
	ReasonRestoreConflict           = "RestoreConflict"
	ReasonRollbackUnsupported       = "RollbackUnsupported"
	ReasonRollbackFailed            = "RollbackFailed"
	ReasonHealthCheckFailed         = "HealthCheckFailed"
	ReasonConfigParseFailed         = "ConfigParseFailed"
	ReasonConfigWriteFailed         = "ConfigWriteFailed"
	ReasonVersionVerificationFailed = "VersionVerificationFailed"
	ReasonAgentDisconnected         = "AgentDisconnected"
	ReasonStaleExecutionLock        = "StaleExecutionLock"
	ReasonNodeBusy                  = "NodeBusy"
)

// DriftEvaluation 是一次 drift 三态求值的输出（§4.2 条件表/§7.1）。
// 权威判据唯一：desiredProjectionDigest == observedProjectionDigest 且二者
// canonicalizationVersion 一致（FR-8.6）；逐字段 diff 只用于展示。
type DriftEvaluation struct {
	// DriftStatus / ReconciledStatus ∈ ConditionTrue/False/Unknown。
	DriftStatus      string
	DriftReason      string // Unknown 时的 reason；True/False 时为空
	DriftMessage     string
	ReconciledStatus string
	ReconciledReason string
	// DesiredProjectionDigest / ObservedProjectionDigest 是本次求值使用的摘要对。
	DesiredProjectionDigest  string
	ObservedProjectionDigest string
	CanonicalizationVersion  string
}
