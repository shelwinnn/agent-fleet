package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrEnrollmentRejected 是 enroll 被拒的统一哨兵错误（§30 Agent 错误组：
// EnrollmentRejected）。对客户端不区分"过期/已用/绑定不符"等细节，避免给
// 枚举攻击提供反馈；细节只进服务端审计日志。
var ErrEnrollmentRejected = errors.New("enrollment rejected")

// ReasonEnrollmentRejected 是 §30 的 Agent 组错误码。
const ReasonEnrollmentRejected = "EnrollmentRejected"

// EnrollmentToken 是一次性 enrollment token 的持久化形态（§4.6）。
// 明文 token 只在签发响应中出现一次，库中仅存 SHA-256 哈希。
type EnrollmentToken struct {
	TokenHash      string // SHA-256(token) hex
	Machine        string // machines.name（沿用库中机器键约定）
	CSRFingerprint string // 绑定的 CSR 公钥指纹 "sha256:<hex>"（§4.6）
	CreatedAt      time.Time
	ExpiresAt      time.Time
	UsedAt         *time.Time // 非空即已消费
}

// EnrollmentTokenRepository 定义 token 的签发与原子消费（FR-11.5）。
type EnrollmentTokenRepository interface {
	// Create 登记 token 哈希及绑定。
	Create(ctx context.Context, tok *EnrollmentToken) error
	// Get 返回 token 登记记录，供拒绝后做服务端审计分类（不改变一次性语义）。
	Get(ctx context.Context, tokenHash string) (*EnrollmentToken, error)
	// Consume 在单事务内完成"校验 token（存在/未过期/未用/machineId 匹配/
	// CSR 指纹匹配）→ 作废"（§4.6 契约）。条件更新影响行数为 0 即拒绝，
	// 并发抢注在数据库层被序列化，至多一个请求成功。
	Consume(ctx context.Context, tokenHash, machine, csrfingerprint string, now time.Time) error
}

// AgentCertificate 是已签发客户端证书的审计记录（§10.1：不含私钥）。
type AgentCertificate struct {
	Serial    string // 证书序列号 hex
	Machine   string
	NotBefore time.Time
	NotAfter  time.Time
	IssuedAt  time.Time
	RetiredAt *time.Time // 机器删除等"到期前仍有效却被拒"的审计事实
}

// AgentCertificateRepository 记录已签发证书。
type AgentCertificateRepository interface {
	Record(ctx context.Context, c *AgentCertificate) error
	// RetireByMachine 把某机全部未退役证书打上退役时刻（§4.6：删除即拒绝，
	// 保留审计事实；机器删除时由控制器调用）。
	RetireByMachine(ctx context.Context, machine string, now time.Time) error
}

// MachineStatusRepository 是 Machine 控制器的 status 写入通道（§4.2：
// Machine 控制器独占写 conditions 与观测字段；REST 更新不改写 status）。
type MachineStatusRepository interface {
	// UpdateStatus 在单事务内读改写 machines.status 并递增 resource_version。
	// mutate 内发生的错误会中止写入。
	UpdateStatus(ctx context.Context, machine string, mutate func(*MachineStatus) error) error
}

// ObservedStateRecord 是一份已入库的节点观测（§10.1 observed_states）。
type ObservedStateRecord struct {
	Machine      string
	InventorySeq int64
	OperationID  string          // 空表示周期上报
	Payload      json.RawMessage // ObservedState JSON
	RecordedAt   time.Time
}

// ObservedStateRepository 维护每机最新观测。
type ObservedStateRepository interface {
	// Store 写入观测。若 inventory_seq 落后于已存高水位，则不得改写
	// （FR-8.7 观测有序性），此时返回 accepted=false 且不视为错误。
	Store(ctx context.Context, rec *ObservedStateRecord) (accepted bool, err error)
	// Latest 取某机当前最新观测（无则 ErrNotFound）。
	Latest(ctx context.Context, machine string) (*ObservedStateRecord, error)
}
