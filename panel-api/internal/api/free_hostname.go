package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Free-hostname activation from Server Settings (JAB-213). Lets an admin turn
// on a jabalihosted.com hostname on an ALREADY-installed box, not just at
// install. panel-api performs the register/claim HTTPS to the service (ADR-0050
// — the agent never talks to third parties); the observed source IP is this
// box's public IP, which is exactly the box that gets the label. On a
// successful claim the agent persists the token file + turns on the lifecycle,
// and the hostname is switched through the existing system.set_hostname path.

// freeHostnameAPI is the service base URL; overridable for tests.
var freeHostnameAPI = "https://api.jabalihosted.com"

// freeHostnameHTTP is the outbound client; overridable for tests. TLS
// verification stays ON (production defaults, per CONVENTIONS security rule).
var freeHostnameHTTP = &http.Client{Timeout: 30 * time.Second}

func (h *serverSettingsHandler) freeHostnamePost(ctx context.Context, path string, in any) (int, []byte, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, freeHostnameAPI+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := freeHostnameHTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, out, nil
}

type freeHostnameRegisterReq struct {
	Email string `json:"email"`
}

// register: POST /admin/settings/free-hostname/register — mail a code.
func (h *serverSettingsHandler) freeHostnameRegister(c *gin.Context) {
	var req freeHostnameRegisterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	if !isValidEmail(strings.TrimSpace(req.Email)) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "bad_email"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	status, body, err := h.freeHostnamePost(ctx, "/v1/register", freeHostnameRegisterReq{Email: strings.TrimSpace(req.Email)})
	if err != nil {
		h.cfg.Log.Error("free-hostname register: service unreachable", "err", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "service_unreachable"})
		return
	}
	switch status {
	case http.StatusOK:
		c.JSON(http.StatusOK, gin.H{"ok": true})
	case http.StatusTooManyRequests:
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited", "message": "a code was just sent — wait a minute"})
	default:
		c.JSON(http.StatusBadGateway, gin.H{"error": "register_failed", "message": jsonField(body, "message")})
	}
}

type freeHostnameClaimReq struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// claim: POST /admin/settings/free-hostname/claim — verify code, persist the
// credential via the agent, and switch the panel hostname to the new label.
func (h *serverSettingsHandler) freeHostnameClaim(c *gin.Context) {
	var req freeHostnameClaimReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_json"})
		return
	}
	email := strings.TrimSpace(req.Email)
	code := strings.TrimSpace(req.Code)
	if !isValidEmail(email) || code == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "bad_input"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	status, body, err := h.freeHostnamePost(ctx, "/v1/claim", freeHostnameClaimReq{Email: email, Code: code})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "service_unreachable"})
		return
	}
	if status != http.StatusOK {
		// Surface the distinct failure so the UI can guide the operator.
		code := "claim_failed"
		switch status {
		case http.StatusForbidden:
			code = "code_invalid"
		case http.StatusTooManyRequests:
			code = "code_attempts"
		case http.StatusUnprocessableEntity:
			code = "bad_source" // CGNAT/bogon public IP — free hostname impossible
		}
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": code, "message": jsonField(body, "message")})
		return
	}

	var claim struct {
		Label string `json:"label"`
		FQDN  string `json:"fqdn"`
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &claim) != nil || claim.FQDN == "" || claim.Token == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "bad_claim_response"})
		return
	}

	// Persist the credential + turn on the lifecycle via the agent (root).
	applyCtx, applyCancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer applyCancel()
	if _, aerr := h.cfg.Agent.Call(applyCtx, "hostname.free.apply", map[string]any{
		"fqdn":  claim.FQDN,
		"label": claim.Label,
		"email": email,
		"token": claim.Token,
		"api":   freeHostnameAPI,
	}); aerr != nil {
		h.cfg.Log.Error("free-hostname apply failed", "err", aerr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "apply_failed"})
		return
	}

	// Switch the panel hostname to the claimed name, reusing the same
	// system.set_hostname cascade the settings PATCH uses.
	if cur, gerr := h.cfg.Repo.Get(applyCtx); gerr == nil && cur != nil {
		cur.Hostname = claim.FQDN
		if uerr := h.cfg.Repo.Upsert(applyCtx, cur); uerr != nil {
			h.cfg.Log.Error("free-hostname: hostname update failed", "err", uerr)
		} else {
			go func(host string) {
				bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := h.cfg.Agent.Call(bg, "system.set_hostname", map[string]any{"hostname": host}); err != nil {
					h.cfg.Log.Error("free-hostname: set_hostname failed", "err", err)
				}
			}(claim.FQDN)
		}
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "fqdn": claim.FQDN})
}

// jsonField pulls a top-level string field from a JSON body, "" on any miss.
func jsonField(body []byte, key string) string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
