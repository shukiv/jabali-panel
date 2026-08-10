package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/hostedsvc"
)

// JAB-235 — the Cloudflare API token behind the DNS-01 fallback.
//
// The token lets the panel write _acme-challenge TXT records into
// Cloudflare-hosted customer zones, so CDN-fronted domains still get a real
// Let's Encrypt origin cert. It is a high-value secret (DNS write access to
// every zone it covers): stored ONLY sealed with the sso key
// (AES-256-GCM, server_settings.cf_api_token_enc), written ONLY through
// these endpoints, and NEVER returned by any response — status reports
// configured yes/no. Least privilege: advise a dedicated token scoped to
// Zone:DNS:Edit + Zone:Read on the zones it should cover.

// cfTokenMaxLen bounds the accepted token length — real CF tokens are
// ~40 chars; anything much larger is garbage (and must not be sealed into
// the varbinary(512) column, which caps the envelope).
const cfTokenMaxLen = 256

type cfTokenStatusResponse struct {
	Configured bool `json:"configured"`
}

type cfTokenSetRequest struct {
	Token string `json:"token"`
}

type cfTokenSetResponse struct {
	Configured bool `json:"configured"`
	Zones      int  `json:"zones"`
}

func (h *serverSettingsHandler) cfTokenStatus(c *gin.Context) {
	s, err := h.cfg.Repo.Get(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_read_failed", "detail": "could not read server settings"})
		return
	}
	c.JSON(http.StatusOK, cfTokenStatusResponse{Configured: s != nil && len(s.CFAPITokenEnc) > 0})
}

func (h *serverSettingsHandler) cfTokenSet(c *gin.Context) {
	var req cfTokenSetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": "body must be {\"token\": \"...\"}"})
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || len(token) > cfTokenMaxLen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_token", "detail": "token is empty or too long"})
		return
	}
	if h.cfg.SSOKey == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured — cannot store the token securely"})
		return
	}

	// Verify against Cloudflare BEFORE storing — a mistyped token that only
	// fails at issuance time would surface days later as a cert error.
	verify := h.cfg.CFVerify
	if verify == nil {
		verify = func(ctx context.Context, tok string) (int, error) {
			return hostedsvc.NewCloudflareAPI(tok).VerifyToken(ctx)
		}
	}
	verifyCtx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	zones, err := verify(verifyCtx, token)
	cancel()
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "token_verify_failed", "detail": err.Error()})
		return
	}

	sealed, err := h.cfg.SSOKey.Seal([]byte(token))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seal_failed", "detail": "could not encrypt the token"})
		return
	}
	ctx := c.Request.Context()
	s, err := h.cfg.Repo.Get(ctx)
	if err != nil || s == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_read_failed", "detail": "could not read server settings"})
		return
	}
	s.CFAPITokenEnc = sealed
	if err := h.cfg.Repo.Upsert(ctx, s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_write_failed", "detail": "could not store the token"})
		return
	}
	if h.cfg.Log != nil {
		h.cfg.Log.Info("cloudflare API token stored for DNS-01 fallback", "zones_visible", zones)
	}
	c.JSON(http.StatusOK, cfTokenSetResponse{Configured: true, Zones: zones})
}

func (h *serverSettingsHandler) cfTokenClear(c *gin.Context) {
	ctx := c.Request.Context()
	s, err := h.cfg.Repo.Get(ctx)
	if err != nil || s == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_read_failed", "detail": "could not read server settings"})
		return
	}
	s.CFAPITokenEnc = nil
	if err := h.cfg.Repo.Upsert(ctx, s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_write_failed", "detail": "could not clear the token"})
		return
	}
	if h.cfg.Log != nil {
		h.cfg.Log.Info("cloudflare API token cleared")
	}
	c.JSON(http.StatusOK, cfTokenStatusResponse{Configured: false})
}
