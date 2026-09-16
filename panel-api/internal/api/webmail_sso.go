package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailsso"
)

// WebmailSSOHandlerConfig wires the GET /sso/webmail landing endpoint
// that the panel's "Login to Webmail" button targets via the
// per-tenant `mail.<domain>` vhost. M6.6 rewrite (ADR-0045 amended):
// we no longer POST to Bulwark's /api/auth/session — that path was
// gate-blocked AND cookie/localStorage-broken (Bulwark #296). Instead
// we mint a short-lived HS256 JWT and 303 the user to
// `https://mail.<domain>/api/auth/impersonate?token=<jwt>`, which is
// Bulwark 1.7.1's first-class SSO entry point. Bulwark verifies the
// JWT against the shared BULWARK_JWT_AUTH_SECRET, builds a Stalwart
// master-user Basic header, sets the impersonation-slot cookies that
// the SPA actually trusts, then 303s the user to `/`.
//
// SECURITY: we never see Bulwark's session-cookie crypto material;
// the JWT secret signs panel→Bulwark intent only, the master-user
// Basic header lives entirely in Bulwark + Stalwart. The mailbox
// `password_enc` column is no longer read by this path (kept on the
// model for future IMAP-cred display features).
type WebmailSSOHandlerConfig struct {
	Mailboxes repository.MailboxRepository
	Domains   repository.DomainRepository
	SSOKey    *ssokey.Key
	SSOTokens repository.MailboxSSOTokenRepository
	// Users resolves the mailbox owner so the landing gate can enforce
	// current suspend / webmail-disable policy at redemption time (JAB-9).
	Users repository.UserRepository
	// Packages resolves the owner's hosting package so the landing gate can
	// enforce the package webmail entitlement (GH #1628 slice 3, which replaced
	// the removed per-user webmail toggle). A nil PackageID or a dangling id ⇒
	// allowed (the deliberate #282 exception: webmail is a convenience surface,
	// not a hardening clamp). Required; the handler fails loud (503) when nil.
	Packages repository.PackageRepository
	// Minter signs the impersonation JWT. Required; the handler
	// surfaces 503 when nil so misconfigured panels fail loud instead
	// of redirecting users to a guaranteed-401.
	Minter *webmailsso.Minter
	// TokenLifetime is the JWT exp - iat window. Defaults to 60s when
	// zero (well under Bulwark's MAX_TOKEN_LIFETIME_SEC=300 ceiling).
	TokenLifetime time.Duration
	// Log: stderr-by-default slog logger.
	Log *slog.Logger
}

// webmailExpiredHTML is the friendly landing shown when an SSO token is
// unknown / expired / already-consumed (single-use). Self-contained (no
// external assets) since it renders on the per-tenant mail vhost. Links
// back to the webmail login ("/" on mail.<domain>) so the user can sign in
// manually or return to the panel for a fresh link.
const webmailExpiredHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign-in link expired</title>
<style>body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center;padding:24px}.card{max-width:420px;text-align:center}.card h1{font-size:20px;margin:0 0 12px}.card p{color:#94a3b8;line-height:1.5;margin:0 0 20px}.card a{display:inline-block;background:#2563eb;color:#fff;text-decoration:none;padding:10px 18px;border-radius:8px;font-weight:600}</style>
</head><body><div class="card"><h1>Sign-in link expired</h1><p>This webmail sign-in link was already used or has expired. Open the Jabali panel and click <strong>Webmail</strong> again to get a fresh link.</p><a href="/">Go to webmail login</a></div></body></html>`

// RegisterWebmailSSORoutes mounts GET /sso/webmail at the top-level
// engine root (not under /api/v1) because Bulwark / Stalwart vhosts
// don't share the API prefix and the handler is designed to be
// reached via nginx on mail.<domain>.
func RegisterWebmailSSORoutes(r gin.IRouter, cfg WebmailSSOHandlerConfig) {
	h := &webmailSSOHandler{cfg: cfg}
	r.GET("/sso/webmail", h.land)
}

type webmailSSOHandler struct{ cfg WebmailSSOHandlerConfig }

func (h *webmailSSOHandler) land(c *gin.Context) {
	ctx := c.Request.Context()

	// Refuse to act on speculative / prefetch fetches. The token is
	// single-use (consumed on first read), so a Chrome address-bar
	// prefetch or a link-rel-prefetch hit would burn the token before
	// the user's real click — they then see "token is invalid or
	// expired" even though they just landed on the URL. Chrome ships
	// `Purpose: prefetch` (legacy) and `Sec-Purpose: prefetch`
	// (current spec); we honour both.
	if isPrefetchRequest(c.Request) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Status(http.StatusNoContent)
		return
	}
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Header("Pragma", "no-cache")

	token := c.Query("token")
	if token == "" {
		c.String(http.StatusBadRequest, "missing token")
		return
	}
	if h.cfg.SSOKey == nil || h.cfg.SSOTokens == nil || h.cfg.Minter == nil || h.cfg.Users == nil || h.cfg.Packages == nil {
		c.String(http.StatusServiceUnavailable, "webmail sso is not configured on this panel")
		return
	}

	// Hash the token before any DB work so the raw bytes never appear
	// in logs (even when the query-string is captured by upstream
	// access-log middleware).
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid token")
		return
	}
	hash := sha256.Sum256(raw)
	hashHex := hex.EncodeToString(hash[:])

	// JAB-9: consume the token ATOMICALLY (row-lock SELECT + DELETE) BEFORE
	// minting any JWT. PeekByHash + a later DeleteByHash let two concurrent
	// requests both pass the peek and both mint a valid Bulwark JWT. With
	// ConsumeByHash only one request wins the row; the loser gets ErrNotFound.
	// The token is now burned even if a policy check below refuses redemption —
	// exactly the "stays burned on refusal" oracle/retry protection the issue
	// requires.
	tok, err := h.cfg.SSOTokens.ConsumeByHash(ctx, hashHex)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Unknown / expired / already-consumed / lost the race — collapse to
			// one response so an attacker can't tell them apart.
			c.Data(http.StatusForbidden, "text/html; charset=utf-8", []byte(webmailExpiredHTML))
			return
		}
		h.logErr("webmail sso: consume token", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	mb, err := h.cfg.Mailboxes.FindByID(ctx, tok.MailboxID)
	if err != nil {
		h.logErr("webmail sso: find mailbox", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	dom, err := h.cfg.Domains.FindByID(ctx, mb.DomainID)
	if err != nil {
		h.logErr("webmail sso: find domain", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	// The mailbox owner is the domain owner. Load them so we can enforce
	// current account policy (a token minted seconds before suspension /
	// webmail-disable must NOT become a live session).
	usr, err := h.cfg.Users.FindByID(ctx, dom.UserID)
	if err != nil {
		h.logErr("webmail sso: find owner user", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	// GH #1628 slice 3: the per-user webmail toggle is gone; the webmail
	// entitlement now lives on the owner's hosting package. Resolve it so the
	// freshness gate can refuse a session for a package with webmail off. A nil
	// PackageID or a dangling id ⇒ allowed (the #282 convenience exception,
	// matching the reconciler). A real lookup error fails the redemption CLOSED
	// (500) rather than silently granting a session on this auth path.
	packageWebmailAllowed := true
	if usr.PackageID != nil {
		pkg, pErr := h.cfg.Packages.FindByID(ctx, *usr.PackageID)
		switch {
		case pErr == nil:
			packageWebmailAllowed = pkg.WebmailEnabled
		case errors.Is(pErr, repository.ErrNotFound):
			// Dangling package id — treat as no package (#282 exception).
		default:
			h.logErr("webmail sso: find owner package", pErr)
			c.String(http.StatusInternalServerError, "internal error")
			return
		}
	}

	// JAB-9 policy-freshness gate: re-check the CURRENT state after the token
	// is already burned. Any of these disables webmail access; refuse with the
	// SAME generic expired page (no oracle: the browser can't tell expired /
	// used / policy-blocked apart) while the audit log records the exact cause.
	if reason := webmailBlockReason(mb.IsDisabled, dom.WebmailEnabled, packageWebmailAllowed, usr.Suspended); reason != "" {
		h.cfgLog().Warn("webmail sso: redemption refused by current policy",
			"reason", reason, "mailbox_id", mb.ID, "domain", dom.Name, "user_id", usr.ID)
		c.Data(http.StatusForbidden, "text/html; charset=utf-8", []byte(webmailExpiredHTML))
		return
	}

	lifetime := h.cfg.TokenLifetime
	if lifetime <= 0 {
		lifetime = 60 * time.Second
	}
	jwt, err := h.cfg.Minter.Mint(webmailsso.MintInput{
		Mailbox:  mb.EmailCached,
		JTI:      ids.NewULID(),
		Lifetime: lifetime,
	})
	if err != nil {
		h.logErr("webmail sso: mint JWT", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	// (Token was already consumed atomically above.)
	// 303 (See Other) so the browser swaps GET for GET and drops any
	// Authorization headers from this hop. The impersonate endpoint
	// is on the per-tenant mail vhost (mail.<dom.Name>), which sets
	// session cookies under that origin — those cookies don't travel
	// back to panel-hostname, which is exactly the isolation we want.
	target := "https://mail." + dom.Name + "/api/auth/impersonate?token=" + jwt
	c.Redirect(http.StatusSeeOther, target)
}

// isPrefetchRequest reports whether the inbound request was issued by
// a speculative prefetch (Chrome address-bar prerender, <link
// rel=prefetch>, etc) rather than a real navigation. Chrome stamps
// `Sec-Purpose: prefetch` (current spec) or the legacy `Purpose:
// prefetch`; we match both, case-insensitive, on a substring so
// future values like `prefetch;prerender` still hit.
func isPrefetchRequest(r *http.Request) bool {
	for _, h := range []string{"Sec-Purpose", "Purpose"} {
		v := r.Header.Get(h)
		if v == "" {
			continue
		}
		lower := strings.ToLower(v)
		if strings.Contains(lower, "prefetch") || strings.Contains(lower, "prerender") {
			return true
		}
	}
	return false
}

func (h *webmailSSOHandler) logErr(msg string, err error) {
	log := h.cfg.Log
	if log == nil {
		slog.Error(msg, "err", err)
		return
	}
	log.Error(msg, "err", err)
}

// webmailBlockReason returns a non-empty, log-only reason string when current
// policy forbids the webmail session, or "" when redemption may proceed. The
// reason is for audit logs — it is never shown to the browser.
func webmailBlockReason(mailboxDisabled, domainWebmail, packageWebmail, userSuspended bool) string {
	switch {
	case mailboxDisabled:
		return "mailbox_disabled"
	case !domainWebmail:
		return "domain_webmail_disabled"
	case !packageWebmail:
		return "package_webmail_disabled"
	case userSuspended:
		return "user_suspended"
	default:
		return ""
	}
}

func (h *webmailSSOHandler) cfgLog() *slog.Logger {
	if h.cfg.Log != nil {
		return h.cfg.Log
	}
	return slog.Default()
}
