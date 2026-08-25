package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// MailboxHandlerConfig plugs the mailbox HTTP handlers into the router.
type MailboxHandlerConfig struct {
	Mailboxes repository.MailboxRepository
	Domains   repository.DomainRepository
	Agent     agent.AgentInterface
	// SSOKey + SSOTokens enable the webmail SSO flow (M6 Step 8 Phase B).
	// When either is nil, create/rotate still succeed but password_enc
	// stays NULL and POST /mailboxes/:id/sso returns 503 — matches the
	// panel-running-without-sso.key pre-M20 topology.
	SSOKey    *ssokey.Key
	SSOTokens repository.MailboxSSOTokenRepository
	// SSOTokenTTL controls how long a minted SSO token is valid before
	// the landing endpoint refuses it. Defaults to 5 minutes (matches
	// PhpMyAdmin SSO) when zero-valued.
	SSOTokenTTL time.Duration
}

const (
	defaultMailboxesPageSize = 20
	maxMailboxesPageSize     = 200

	// Mailbox quota floor/default + bcrypt cost are owned by the shared Mailbox
	// Lifecycle package (JAB-291) and aliased here so these handlers and the CLI
	// reference one source and can't drift.
	defaultMailboxQuotaBytes = mailboxops.DefaultQuotaBytes
	minMailboxQuotaBytes     = mailboxops.MinQuotaBytes
	mailboxBcryptCost        = mailboxops.BcryptCost

	// Agent call budget. Matches other handlers that SSH or shell out.
	mailboxAgentTimeout = 30 * time.Second
)

// RegisterMailboxRoutes mounts the mailbox endpoints under g:
//
//   - GET  /domains/:id/mailboxes               list mailboxes in a domain
//   - POST /domains/:id/mailboxes               create a mailbox
//   - GET  /mailboxes/:mbid                     fetch a single mailbox
//   - PATCH /mailboxes/:mbid                    update quota
//   - POST /mailboxes/:mbid/rotate-password     rotate (or set) password
//   - DELETE /mailboxes/:mbid                   destroy mailbox
//
// The domain-scoped create/list live under /domains/:id/mailboxes so
// ownership is enforced once (via the domain row). The per-mailbox
// endpoints look up the mailbox, resolve its domain, and re-check the
// same ownership — this matches how database_users / database-user-grants
// are split between /database-users and /database-user-grants.
//
// ADR-0042 + ADR-0045: the panel-API is the only writer. We INSERT the
// row first (Stalwart's SqlDirectory reads on every auth, no cache to
// invalidate), then fire the agent cmd as a typed no-op acknowledgement
// so the shape stays consistent with M7's per-resource pattern.
func RegisterMailboxRoutes(g *gin.RouterGroup, cfg MailboxHandlerConfig) {
	h := &mailboxHandler{cfg: cfg}

	g.GET("/domains/:id/mailboxes", h.list)
	g.POST("/domains/:id/mailboxes", h.create)

	g.GET("/admin/mailboxes", middleware.RequireAdmin(), h.listAllAdmin)

	mbox := g.Group("/mailboxes")
	mbox.GET("/:mbid", h.get)
	mbox.PATCH("/:mbid", h.update)
	mbox.POST("/:mbid/rotate-password", h.rotatePassword)
	mbox.POST("/:mbid/sso", h.mintSSO)
	mbox.DELETE("/:mbid", h.delete)
}

type mailboxHandler struct{ cfg MailboxHandlerConfig }

// ---- Request / response types ----

type createMailboxRequest struct {
	// LocalPart — the "alice" in alice@example.com. Canonicalised
	// (lowercased, +tag stripped, ASCII-only) by internal/mailaddr
	// before we INSERT.
	LocalPart string `json:"local_part" binding:"required"`

	// Password — plaintext. We bcrypt it before storing. If empty,
	// we generate one and return it reveal-once in the response.
	Password string `json:"password"`

	// QuotaBytes — optional. Zero means "use default" (1 GiB).
	QuotaBytes uint64 `json:"quota_bytes"`

	// DisplayName — optional human-readable name (GH #197). Wired to the
	// Stalwart principal description → Bulwark webmail identity name.
	DisplayName string `json:"display_name"`

	// SendOnly (GH #371) — when true the mailbox can authenticate for SMTP
	// submission but never receives or stores mail. Handy for per-service
	// notification credentials.
	SendOnly bool `json:"send_only"`
}

type createMailboxResponse struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	QuotaBytes  uint64 `json:"quota_bytes"`
	DisplayName string `json:"display_name"`
	SendOnly    bool   `json:"send_only"`
	// Password is returned exactly once when the caller did NOT send a
	// password — the agent-computed random one. Empty when the caller
	// supplied their own.
	Password string `json:"password,omitempty"`
}

type rotateMailboxPasswordRequest struct {
	// NewPassword — optional. If empty, server generates one and
	// returns it reveal-once.
	NewPassword string `json:"new_password"`
}

type rotateMailboxPasswordResponse struct {
	Password string `json:"password,omitempty"`
}

// updateMailboxRequest is a partial update — any nil field is left
// unchanged. Used by the user edit + the admin Mail tab (GH #197).
type updateMailboxRequest struct {
	QuotaBytes  *uint64 `json:"quota_bytes"`
	DisplayName *string `json:"display_name"`
	IsDisabled  *bool   `json:"is_disabled"`
	SendOnly    *bool   `json:"send_only"`
}

type mailboxResponse struct {
	ID             string     `json:"id"`
	DomainID       string     `json:"domain_id"`
	Email          string     `json:"email"`
	DisplayName    string     `json:"display_name"`
	QuotaBytes     uint64     `json:"quota_bytes"`
	IsDisabled     bool       `json:"is_disabled"`
	SendOnly       bool       `json:"send_only"`
	LastUsageBytes uint64     `json:"last_usage_bytes"`
	LastUsageAt    *time.Time `json:"last_usage_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ---- Handlers ----

func (h *mailboxHandler) list(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	page, pageSize, opts := parseListOptions(c, defaultMailboxesPageSize, maxMailboxesPageSize)
	opts.ExcludeSystem = true // GH #1056: the JAB-230 relay is infra, not a listed mailbox

	rows, total, err := h.cfg.Mailboxes.ListByDomainID(ctx, dom.ID, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if rows == nil {
		rows = []models.Mailbox{}
	}

	out := make([]mailboxResponse, len(rows))
	for i, mb := range rows {
		out[i] = toMailboxResponse(mb)
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *mailboxHandler) create(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var req createMailboxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Shared Mailbox Lifecycle create (JAB-291): the EmailEnabled gate, address
	// canonicalization, quota default/floor, password gen/hash/seal, duplicate
	// rule, and display_name/send_only all live in the op. Authorization stayed
	// here (the ownership check above).
	mb, generatedPassword, err := mailboxops.Create(ctx, mailboxops.Deps{
		Mailboxes: h.cfg.Mailboxes,
		SSOKey:    h.cfg.SSOKey,
	}, mailboxops.CreateInput{
		Domain:      dom,
		LocalPart:   req.LocalPart,
		Password:    req.Password,
		QuotaBytes:  req.QuotaBytes,
		DisplayName: req.DisplayName,
		SendOnly:    req.SendOnly,
	}, h.notifyAgent)
	if err != nil {
		switch {
		case errors.Is(err, mailboxops.ErrEmailNotEnabled):
			c.JSON(http.StatusConflict, gin.H{"error": "email_not_enabled", "detail": "enable email on the domain before creating mailboxes"})
		case errors.Is(err, mailboxops.ErrInvalidLocalPart):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_local_part", "detail": err.Error()})
		case errors.Is(err, mailboxops.ErrMailboxExists):
			c.JSON(http.StatusConflict, gin.H{"error": "mailbox_exists"})
		case errors.Is(err, mailboxops.ErrQuotaTooSmall):
			c.JSON(http.StatusBadRequest, gin.H{"error": "quota_too_small", "detail": "quota_bytes must be at least 16 MiB"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}

	c.JSON(http.StatusCreated, createMailboxResponse{
		ID:          mb.ID,
		Email:       mb.EmailCached,
		QuotaBytes:  mb.QuotaBytes,
		DisplayName: mb.DisplayName,
		SendOnly:    mb.SendOnly,
		Password:    generatedPassword,
	})
}

func (h *mailboxHandler) get(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	mb, dom, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeLoadErr(c, err)
		return
	}
	_ = dom

	c.JSON(http.StatusOK, toMailboxResponse(*mb))
}

func (h *mailboxHandler) update(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req updateMailboxRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	mb, dom, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeLoadErr(c, err)
		return
	}

	// Quota.
	if req.QuotaBytes != nil {
		if *req.QuotaBytes < minMailboxQuotaBytes {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "quota_too_small",
				"detail": "quota_bytes must be at least 16 MiB",
			})
			return
		}
		if err := h.cfg.Mailboxes.UpdateQuota(ctx, mb.ID, *req.QuotaBytes); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		mb.QuotaBytes = *req.QuotaBytes
		h.notifyAgent(ctx, "mailbox.set_quota", map[string]any{
			"id":          mb.ID,
			"email":       mb.LocalPart + "@" + dom.Name,
			"quota_bytes": *req.QuotaBytes,
		})
	}

	// Display name (GH #197). Stalwart's SqlDirectory reads display_name
	// as the principal description on the next auth, so new logins reflect
	// it; an already-created webmail identity may not retro-update until
	// re-login (best-effort, ADR note).
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if len(name) > 255 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "display_name_too_long", "detail": "max 255 chars"})
			return
		}
		if err := h.cfg.Mailboxes.UpdateDisplayName(ctx, mb.ID, name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		mb.DisplayName = name
		// Push the new description to the Stalwart account (GH #197) so
		// Bulwark webmail's From name updates. Best-effort: the DB is
		// authoritative; the agent sets the JMAP Account description.
		h.notifyAgent(ctx, "mailbox.set_display_name", map[string]any{
			"email":        mb.LocalPart + "@" + dom.Name,
			"display_name": name,
		})
	}

	// Enable / disable. queryLogin filters is_disabled = 0, so the change
	// takes effect on the next authentication (live directory).
	if req.IsDisabled != nil {
		if err := h.cfg.Mailboxes.SetDisabled(ctx, mb.ID, *req.IsDisabled); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		mb.IsDisabled = *req.IsDisabled
	}

	if req.SendOnly != nil {
		if err := h.cfg.Mailboxes.SetSendOnly(ctx, mb.ID, *req.SendOnly); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		mb.SendOnly = *req.SendOnly
	}

	mb.UpdatedAt = time.Now().UTC()
	c.JSON(http.StatusOK, toMailboxResponse(*mb))
}

func (h *mailboxHandler) rotatePassword(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req rotateMailboxPasswordRequest
	// Body is optional here — empty body means "generate a new one".
	_ = c.ShouldBindJSON(&req)

	mb, dom, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeLoadErr(c, err)
		return
	}

	// Shared Mailbox Lifecycle rotate (JAB-291): hash + sealed envelope are
	// updated atomically — with a live SSO key the envelope is re-sealed, without
	// one it is cleared (never left stale, which previously made the SSO mint
	// decrypt to the old password Stalwart no longer accepts).
	generated, err := mailboxops.RotatePassword(ctx, mailboxops.Deps{
		Mailboxes: h.cfg.Mailboxes,
		SSOKey:    h.cfg.SSOKey,
	}, mb.LocalPart+"@"+dom.Name, req.NewPassword, h.notifyAgent)
	if err != nil {
		if errors.Is(err, mailboxops.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mailbox_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusOK, rotateMailboxPasswordResponse{Password: generated})
}

func (h *mailboxHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	mb, dom, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeLoadErr(c, err)
		return
	}

	// Shared Mailbox Lifecycle delete (JAB-291): destroy the Stalwart account
	// first (hard dependency), then the row — a failed destroy aborts before the
	// row delete so the DB never tombstones a mailbox whose Stalwart side is live.
	if err := mailboxops.Delete(ctx, h.cfg.Mailboxes, h.cfg.Agent.Call, mb.LocalPart+"@"+dom.Name); err != nil {
		switch {
		case errors.Is(err, mailboxops.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "mailbox_not_found"})
		case errors.Is(err, mailboxops.ErrAgentUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		default:
			respondAgentErr(c, "agent_failed", err)
		}
		return
	}

	c.Status(http.StatusNoContent)
}

// mintSSOResponse carries the short-lived URL the UI redirects the
// user's browser to. `mail_host` is the mail.<domain> origin — exposed
// so the UI can render the full URL itself when preferred (matches
// the phpMyAdmin SSO response shape).
type mintSSOResponse struct {
	URL       string `json:"url"`
	MailHost  string `json:"mail_host"`
	ExpiresIn int    `json:"expires_in"`
}

// mintSSO handles POST /mailboxes/:mbid/sso. Returns a one-shot URL
// the panel UI opens in a new tab; the browser follows it, the
// landing endpoint at /sso/webmail on mail.<domain> consumes the
// token and forwards the user into Bulwark with a session cookie set.
//
// Auth model: admin or the mailbox's owning user (same scope as
// rotatePassword / updateQuota). Tokens are mailbox-scoped, so the
// "user who clicks" must be able to see the mailbox to begin with.
func (h *mailboxHandler) mintSSO(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if h.cfg.SSOKey == nil || h.cfg.SSOTokens == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_not_configured"})
		return
	}

	mb, dom, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeLoadErr(c, err)
		return
	}
	// M6.6: password_enc is no longer required for SSO. The impersonate
	// flow uses Stalwart's master-user Basic header, not the mailbox's
	// plaintext password. The encrypted column stays on the model for
	// future IMAP-cred display features but isn't a SSO precondition.

	// 32 random bytes → base64url token (plaintext) + SHA-256 hash.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	plaintextToken := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256(raw)

	ttl := h.cfg.SSOTokenTTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	now := time.Now().UTC()
	tok := &models.MailboxSSOToken{
		ID:        ids.NewULID(),
		MailboxID: mb.ID,
		UserID:    claims.UserID,
		TokenHash: hex.EncodeToString(hash[:]),
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}
	if err := h.cfg.SSOTokens.Create(ctx, tok); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	mailHost := "mail." + dom.Name
	url := "https://" + mailHost + "/sso/webmail?token=" + plaintextToken
	c.JSON(http.StatusOK, mintSSOResponse{
		URL:       url,
		MailHost:  mailHost,
		ExpiresIn: int(ttl.Seconds()),
	})
}

// ---- helpers ----

// loadMailboxWithAuth fetches a mailbox by ID, loads its owning domain,
// and verifies that `claims` can see it (admin, or the domain's owner).
// Returns one of these error sentinels for the caller to translate:
//   - repository.ErrNotFound → 404
//   - errMailboxForbidden → 403
//   - any other err → 500
func (h *mailboxHandler) loadMailboxWithAuth(ctx context.Context, id string, claims *auth.AccessClaims) (*models.Mailbox, *models.Domain, error) {
	mb, err := h.cfg.Mailboxes.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	dom, err := h.cfg.Domains.FindByID(ctx, mb.DomainID)
	if err != nil {
		return nil, nil, err
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		return nil, nil, errMailboxForbidden
	}
	return mb, dom, nil
}

var errMailboxForbidden = &mailboxErr{kind: "forbidden"}

type mailboxErr struct{ kind string }

func (e *mailboxErr) Error() string { return "mailbox: " + e.kind }

func (h *mailboxHandler) writeLoadErr(c *gin.Context, err error) {
	if isNotFound(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if err == errMailboxForbidden {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
}

// notifyAgent runs an agent call without failing the HTTP response on
// error — per ADR-0013 inline-best-effort. If the agent is nil (tests)
// this is a no-op. Errors are swallowed; the panel's reconciler is
// responsible for re-asserting state agents dropped.
func (h *mailboxHandler) notifyAgent(ctx context.Context, command string, params any) {
	if h.cfg.Agent == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, mailboxAgentTimeout)
	defer cancel()
	_, _ = h.cfg.Agent.Call(agentCtx, command, params)
}

// adminMailboxResponse is a server-wide mailbox row for the admin Mail tab:
// the per-mailbox fields plus its domain + owner.
type adminMailboxResponse struct {
	mailboxResponse
	DomainName   string `json:"domain_name"`
	OwnerUserID  string `json:"owner_user_id"`
	UserUsername string `json:"user_username"`
}

// listAllAdmin returns every mailbox on the server (admin-only) for the
// server-wide Mail tab. GET /api/v1/admin/mailboxes.
func (h *mailboxHandler) listAllAdmin(c *gin.Context) {
	ctx := c.Request.Context()
	var rows []repository.MailboxWithDomain
	var err error
	// Admin owner-scope via ?user_id (#483).
	if uid := c.Query("user_id"); uid != "" {
		if !ids.IsValidULID(uid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_id"})
			return
		}
		rows, err = h.cfg.Mailboxes.ListByOwnerWithDomain(ctx, uid)
	} else {
		rows, err = h.cfg.Mailboxes.ListAllWithDomain(ctx)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]adminMailboxResponse, 0, len(rows))
	for _, r := range rows {
		if r.Mailbox.System {
			continue // GH #1056: the JAB-230 relay is infra, not a listed mailbox
		}
		out = append(out, adminMailboxResponse{
			mailboxResponse: toMailboxResponse(r.Mailbox),
			DomainName:      r.DomainName,
			OwnerUserID:     r.OwnerUserID,
			UserUsername:    r.UserUsername,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
}

func toMailboxResponse(mb models.Mailbox) mailboxResponse {
	return mailboxResponse{
		ID:             mb.ID,
		DomainID:       mb.DomainID,
		Email:          mb.EmailCached,
		DisplayName:    mb.DisplayName,
		QuotaBytes:     mb.QuotaBytes,
		IsDisabled:     mb.IsDisabled,
		SendOnly:       mb.SendOnly,
		LastUsageBytes: mb.LastUsageBytes,
		LastUsageAt:    mb.LastUsageAt,
		CreatedAt:      mb.CreatedAt,
		UpdatedAt:      mb.UpdatedAt,
	}
}
