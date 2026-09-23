package enrollment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// TokenService 实现 enrollment token 的签发与原子消费（§4.6，FR-11.1/11.5）。
type TokenService struct {
	repo       domain.EnrollmentTokenRepository
	defaultTTL time.Duration
}

// DefaultTokenTTL 是 token 的默认短时效（§4.6：默认 10 分钟，可配置）。
const DefaultTokenTTL = 10 * time.Minute

func NewTokenService(repo domain.EnrollmentTokenRepository) *TokenService {
	return &TokenService{repo: repo, defaultTTL: DefaultTokenTTL}
}

// Issue 签发一次性 token：明文只在返回值中出现一次，库中仅存哈希；
// 绑定 machineId 与 CSR 公钥指纹（§4.6）。ttl 非正时取默认值。
func (s *TokenService) Issue(ctx context.Context, machine, csrFingerprint string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	if machine == "" || csrFingerprint == "" {
		return "", time.Time{}, fmt.Errorf("%w: machine and csr fingerprint are required", domain.ErrInvalid)
	}
	if ttl <= 0 {
		ttl = s.defaultTTL
	}
	token, err = randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expiresAt = now.Add(ttl)
	err = s.repo.Create(ctx, &domain.EnrollmentToken{
		TokenHash:      TokenHash(token),
		Machine:        machine,
		CSRFingerprint: csrFingerprint,
		CreatedAt:      now,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// Redeem 消费 token：校验（存在/未过期/未用/machineId 匹配/CSR 指纹匹配）与
// 作废处于同一条条件 UPDATE（§4.6 原子消费契约）。拒绝细节只写入服务端
// 审计日志，对客户端统一返回 ErrEnrollmentRejected。
func (s *TokenService) Redeem(ctx context.Context, machine, token, csrFingerprint string) error {
	now := time.Now().UTC()
	err := s.repo.Consume(ctx, TokenHash(token), machine, csrFingerprint, now)
	if err == nil {
		return nil
	}
	// 审计分类（服务端日志；§11 安全事件审计、S3 相关性检测的原料）。
	var rejected *domain.EnrollmentToken
	var detail string
	if t, getErr := s.repo.Get(ctx, TokenHash(token)); getErr == nil {
		rejected = t
	}
	switch {
	case rejected == nil:
		detail = "unknown_token"
	case !rejected.ExpiresAt.After(now):
		detail = "expired"
	case rejected.UsedAt != nil:
		detail = "already_used"
	case rejected.Machine != machine:
		detail = "machine_mismatch"
	case rejected.CSRFingerprint != csrFingerprint:
		detail = "csr_mismatch"
	default:
		detail = "rejected"
	}
	slog.Warn("enroll rejected", "event", "enroll_rejected", "reason_code", domain.ReasonEnrollmentRejected,
		"machine_id", machine, "detail", detail, "token_known", rejected != nil)
	return fmt.Errorf("%w: %s", domain.ErrEnrollmentRejected, detail)
}

// TokenHash 计算 token 的 SHA-256 哈希（hex）。明文 token 不落库、不进日志。
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
