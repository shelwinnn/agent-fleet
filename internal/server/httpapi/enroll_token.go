package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/shelwinnn/agent-fleet/internal/domain"
)

// EnrollTokenIssuer 由 enrollment 服务实现（KM-22：一次性 enrollment token 签发）。
type EnrollTokenIssuer interface {
	// Issue 签发一次性 token；明文只在本响应出现一次（§4.6）。
	Issue(ctx context.Context, machine, csrFingerprint string, ttl time.Duration) (token string, expiresAt time.Time, err error)
}

// csrFingerprintRE 校验 CSR 公钥指纹形态（§4.7 digest 风格：sha256:<64hex>）。
var csrFingerprintRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type enrollTokenRequest struct {
	CSRPubKeySha256 string `json:"csrPubKeySha256"`
	TTLSeconds      int    `json:"ttlSeconds,omitempty"`
}

type enrollTokenResponse struct {
	MachineID string    `json:"machineId"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Note      string    `json:"note"`
}

// handleEnrollToken 处理 POST /api/v1/machines/{name}/enroll-token（§8.1 动作端点）：
// 为既有 Machine 签发一次性 enrollment token，绑定 CSR 公钥指纹。
// token 明文仅在响应中出现一次，服务端只存哈希；默认有效期 10 分钟。
func (s *Server) handleEnrollToken(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EnrollTokens == nil {
		writeError(w, http.StatusNotImplemented, domain.ReasonNotImplemented,
			"enrollment token issuance is not configured", nil)
		return
	}
	name := r.PathValue("name")
	if _, err := s.machines.Get(r.Context(), name); err != nil {
		writeStoreError(w, err)
		return
	}
	var req enrollTokenRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid,
			"request body must be a JSON object: "+err.Error(), nil)
		return
	}
	if !csrFingerprintRE.MatchString(req.CSRPubKeySha256) {
		writeError(w, http.StatusBadRequest, domain.ReasonInvalid,
			"csrPubKeySha256 must match ^sha256:[0-9a-f]{64}$ (agentd enroll prints the fingerprint)", nil)
		return
	}
	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	token, expiresAt, err := s.cfg.EnrollTokens.Issue(r.Context(), name, req.CSRPubKeySha256, ttl)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, enrollTokenResponse{
		MachineID: name,
		Token:     token,
		ExpiresAt: expiresAt,
		Note:      "one-time token; shown only once and stored only as a hash",
	})
}
