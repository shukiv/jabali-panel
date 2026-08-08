// automation_billing.go — ADR-0164 (JAB-190) billing endpoints for the
// Automation API: account lifecycle (create / delete / password / package)
// plus billing reads (usage, packages, user lookup helpers).
//
// Everything writes through the SAME userops functions the GUI handlers
// call (one write path), on the JAB-140 spine: HMAC → requireWriteScope →
// per-token write rate limit → bindSigned → act → auditWrite.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// automationUserRow is the thin, secrets-free user shape for the
// automation read surface. `suspended` was added by ADR-0164 (billing
// panels sync suspension state); older fields keep their names.
func automationUserRow(u *models.User) map[string]any {
	username := ""
	if u.Username != nil {
		username = *u.Username
	}
	pkg := ""
	if u.PackageID != nil {
		pkg = *u.PackageID
	}
	return map[string]any{
		"id":         u.ID,
		"email":      u.Email,
		"username":   username,
		"package_id": pkg,
		"is_admin":   u.IsAdmin,
		"suspended":  u.Suspended,
	}
}

// automationUserLookupResponse renders a single-user filter result in the
// list envelope: hit → one row, miss → empty data, error → 500.
func automationUserLookupResponse(c *gin.Context, u *models.User, err error) {
	if err != nil && !isNotFound(err) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if u == nil || err != nil {
		c.JSON(http.StatusOK, gin.H{"data": []any{}, "total": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": []any{automationUserRow(u)}, "total": 1})
}

// billingUserOpsDeps assembles the shared userops dependency set from the
// automation config (mirror of userHandler.userOpsDeps — one write path).
func billingUserOpsDeps(cfg AutomationConfig) userops.Deps {
	return userops.Deps{
		Users:        cfg.Users,
		Packages:     cfg.Packages,
		Domains:      cfg.Domains,
		DockerApps:   cfg.DockerApps,
		Agent:        cfg.Agent,
		KratosClient: cfg.KratosClient,
		BcryptCost:   cfg.BcryptCost,
		Log:          cfg.Log,
	}
}

func billingDeleteDeps(cfg AutomationConfig) userops.DeleteDeps {
	dd := userops.DeleteDeps{
		Databases:     cfg.Databases,
		DatabaseUsers: cfg.DatabaseUsers,
		Reconciler:    cfg.DomainReconciler,
	}
	if cfg.Redis != nil {
		rdb := cfg.Redis
		dd.RevokeCacheACLs = func(ctx context.Context, osUser string) error {
			return revokeAllUserCacheACLs(ctx, rdb, osUser)
		}
	}
	return dd
}

// registerAutomationBilling mounts the ADR-0164 endpoints and returns
// their capability entries. Wired from registerAutomationWrites so the
// write rate limiter + capabilities list stay unified.
func registerAutomationBilling(g *gin.RouterGroup, cfg AutomationConfig, wl *writeRateLimiter) []capability {
	var caps []capability

	// Lifecycle writes need the users repo at minimum (BcryptCost is
	// defaulted by RegisterAutomation).
	if cfg.Users != nil {
		g.POST("/users", wl.middleware(), requireWriteScope("write:users"), userCreateHandler(cfg))
		g.POST("/users/:id/password", wl.middleware(), requireWriteScope("write:users"), userPasswordHandler(cfg))
		caps = append(caps,
			capability{"users.create", "POST", "/automation/users", "write:users", false},
			capability{"users.password", "POST", "/automation/users/:id/password", "write:users", false})
		// JAB-233: account-create can auto-create a primary domain when the
		// request carries `domain` (needs write:domains too). Advertised only
		// when the domain deps are wired.
		if cfg.DomainCreate.Domains != nil {
			caps = append(caps, capability{"domains.create", "POST", "/automation/users", "write:domains", false})
		}

		if cfg.Packages != nil {
			g.PUT("/users/:id/package", wl.middleware(), requireWriteScope("write:users"), userPackageHandler(cfg))
			caps = append(caps, capability{"users.package", "PUT", "/automation/users/:id/package", "write:users", false})
		}

		g.DELETE("/users/:id", wl.middleware(), requireWriteScope("delete:users"), userDeleteHandler(cfg))
		caps = append(caps, capability{"users.delete", "DELETE", "/automation/users/:id", "delete:users", false})

		// ADR-0165: one-time panel login via Kratos admin recovery links.
		if cfg.KratosClient != nil {
			g.POST("/users/:id/login-token", wl.middleware(), requireWriteScope("write:users"), userLoginTokenHandler(cfg))
			caps = append(caps, capability{"users.login_token", "POST", "/automation/users/:id/login-token", "write:users", false})
		}
	}

	// Billing reads. Not write actions, but advertised so the modules can
	// feature-detect without probing (forward-compatible: entries are
	// {action,...} objects; consumers key on `action`).
	if cfg.Users != nil && cfg.DiskSnapshots != nil {
		g.GET("/users/:id/usage", middleware.RequireScope("read:users"), userUsageHandler(cfg))
		g.GET("/usage", middleware.RequireScope("read:users"), bulkUsageHandler(cfg))
		caps = append(caps, capability{"usage.read", "GET", "/automation/users/:id/usage", "read:users", false})
	}
	if cfg.Packages != nil {
		g.GET("/packages", middleware.RequireScope("read:packages"), packagesListHandler(cfg))
		caps = append(caps, capability{"packages.read", "GET", "/automation/packages", "read:packages", false})
	}

	return caps
}

// --- create ---

type automationCreateUserReq struct {
	Email     string  `json:"email"`
	Password  string  `json:"password"`
	Username  *string `json:"username,omitempty"`
	PackageID *string `json:"package_id,omitempty"`
	// Domain is accepted for forward compatibility with the integration
	// contract but NOT auto-created in v1 (domain creation has its own
	// validation + reconciler flow). Billing modules document this.
	Domain string `json:"domain,omitempty"`
}

func userCreateHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		const action = "POST /automation/users"
		var req automationCreateUserReq
		if !bindSigned(c, &req) {
			return
		}
		// bindSigned is plain json.Unmarshal — no gin binding tags run here.
		// Enforce the REST contract explicitly (ADR-0164).
		if len(req.Password) < 10 {
			autoErr(c, http.StatusUnprocessableEntity, "unsupported", "password must be at least 10 characters")
			return
		}
		req.Email = strings.TrimSpace(req.Email)
		if req.Email != "" {
			if _, err := mail.ParseAddress(req.Email); err != nil {
				autoErr(c, http.StatusUnprocessableEntity, "unsupported", "email must be a valid address")
				return
			}
		}
		if req.Email == "" && (req.Username == nil || *req.Username == "") {
			autoErr(c, http.StatusUnprocessableEntity, "unsupported", "email or username required")
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()

		// JAB-233: optional primary-domain auto-create. Pre-validate the domain
		// (scope + FQDN + global name-conflict) BEFORE the account exists, so a
		// bad/taken domain fails fast and never leaves a half-created user.
		// Absent `domain` → write:users only, unchanged from before.
		var domName string
		if strings.TrimSpace(req.Domain) != "" {
			if !tok.Scopes.Has("write:domains") {
				auditWrite(c, cfg.Audits, tok, action, "user", req.Email, models.AuditResultDenied)
				autoErr(c, http.StatusForbidden, "scope_denied", "creating a domain requires the write:domains scope")
				return
			}
			domName = normalizeDomainName(req.Domain)
			if verr := validateDomainName(domName); verr != nil {
				autoErr(c, http.StatusBadRequest, "validation", "invalid domain: "+verr.Error())
				return
			}
			domName = htmlTagRe.ReplaceAllString(domName, "")
			// Fail-fast conflict: taken by anyone other than the user this
			// (possibly retried) create resolves to. The same-owner retry case
			// falls through and is handled idempotently after the account
			// resolves (createAutomationDomain).
			if existing, ferr := cfg.DomainCreate.Domains.FindByName(ctx, domName); ferr == nil && existing != nil {
				owner := findExactUserMatch(ctx, cfg.Users, req.Email, req.Username)
				if owner == nil || existing.UserID != owner.ID {
					autoErr(c, http.StatusConflict, "conflict", "domain already exists")
					return
				}
			}
		}

		// is_admin is NOT exposed: automation can never mint an admin.
		res, err := userops.Create(ctx, billingUserOpsDeps(cfg), userops.CreateInput{
			Email:     req.Email,
			Password:  req.Password,
			Username:  req.Username,
			PackageID: req.PackageID,
		})
		if err != nil {
			// Idempotent-by-target-state retry (ADR-0164): if BOTH identifiers
			// resolve to the same existing user, hand back its id instead of
			// failing the retry. Partial matches stay conflicts.
			if userIsErrTaken(err) {
				if existing := findExactUserMatch(ctx, cfg.Users, req.Email, req.Username); existing != nil {
					auditWrite(c, cfg.Audits, tok, action, "user", existing.ID, models.AuditResultOK)
					resp := gin.H{"ok": true, "status": "exists", "user_id": existing.ID, "message": "user already exists"}
					// JAB-233: idempotent domain on retry — create if the user
					// doesn't own it yet, no-op (return its id) if they do.
					if domName != "" {
						if did, warn := createAutomationDomain(c, cfg, tok, existing.ID, domName); did != "" {
							resp["domain_id"] = did
						} else if warn != "" {
							resp["domain_warning"] = warn
						}
					}
					c.JSON(http.StatusOK, resp)
					return
				}
				auditWrite(c, cfg.Audits, tok, action, "user", req.Email, models.AuditResultError)
				autoErr(c, http.StatusConflict, "conflict", "email or username already taken")
				return
			}
			auditWrite(c, cfg.Audits, tok, action, "user", req.Email, models.AuditResultError)
			mapUserOpsErr(c, err)
			return
		}

		auditWrite(c, cfg.Audits, tok, action, "user", res.User.ID, models.AuditResultOK)
		notifyWrite(cfg, "automation.user.created", "info", "Automation created a user",
			"User "+res.User.Email+" was created via the automation API.", "/jabali-admin/users")
		msg := "user created"
		if res.ProvisionWarning != "" {
			msg = res.ProvisionWarning
		}
		resp := gin.H{"ok": true, "status": "created", "user_id": res.User.ID, "message": msg}
		// JAB-233: create the primary domain. A failure here does NOT roll back
		// the account (ProvisionWarning precedent) — surface domain_warning on
		// the 201 and let the operator retry the domain.
		if domName != "" {
			if did, warn := createAutomationDomain(c, cfg, tok, res.User.ID, domName); did != "" {
				resp["domain_id"] = did
			} else if warn != "" {
				resp["domain_warning"] = warn
			}
		}
		c.JSON(http.StatusCreated, resp)
	}
}

// createAutomationDomain creates ownerID's primary domain via the shared GUI
// orchestration (createDomainOp), with inline SSL skipped — the reconciler
// bootstraps the cert on its first tick, exactly like `jabali domain create`.
// Idempotent: if the owner already owns a domain of that name, returns its id
// with no warning. A failure AFTER the account exists never rolls back; it
// returns an empty id + a warning the caller surfaces as `domain_warning`.
// Audits the domain-scoped write (success/failure) and bells on success.
func createAutomationDomain(c *gin.Context, cfg AutomationConfig, tok *models.AutomationToken, ownerID, name string) (domainID, warning string) {
	const action = "POST /automation/users (domain)"
	ctx := c.Request.Context()
	if cfg.DomainCreate.Domains == nil {
		return "", "domain not created: domain support not wired on this panel"
	}
	// Idempotent no-op: owner already owns it.
	if existing, err := cfg.DomainCreate.Domains.FindByName(ctx, name); err == nil && existing != nil {
		if existing.UserID == ownerID {
			return existing.ID, ""
		}
		auditWrite(c, cfg.Audits, tok, action, "domain", name, models.AuditResultDenied)
		return "", "domain already exists"
	}
	h := &domainHandler{cfg: cfg.DomainCreate}
	dom, oerr := createDomainOp(ctx, h, createDomainInput{OwnerID: ownerID, Name: name, SkipInlineSSL: true})
	if oerr != nil {
		auditWrite(c, cfg.Audits, tok, action, "domain", name, models.AuditResultError)
		w := oerr.Code
		if oerr.Detail != "" {
			w = oerr.Code + ": " + oerr.Detail
		}
		return "", "domain not created: " + w
	}
	auditWrite(c, cfg.Audits, tok, action, "domain", dom.ID, models.AuditResultOK)
	notifyWrite(cfg, "automation.domain.created", "info", "Automation created a domain",
		"Domain "+dom.Name+" was created via the automation API.", "/jabali-panel/domains")
	return dom.ID, ""
}

func userIsErrTaken(err error) bool {
	return errorsIsAny(err, userops.ErrEmailTaken, userops.ErrUsernameTaken)
}

// findExactUserMatch returns the existing user an identical create retry
// refers to — or nil, which the caller turns into a 409 conflict.
//
// "Identical" must be proven POSITIVELY on BOTH identifiers, so a retry can
// never bind the caller to someone else's account (a create that collides
// on username must not hand back an unrelated user's id — that would let a
// billing panel manage the wrong tenant). Rules:
//   - No email supplied → we cannot confirm identity beyond a unique
//     username, and the original create may have carried an email, so we
//     refuse to call it a match (→ 409). Username is the only unique key
//     (email is NOT unique post-M54), but username-alone is insufficient
//     for an "exists" claim.
//   - Email supplied → resolve by username (given, else derived the same
//     way userops.Create derives it) AND require the stored email to agree.
func findExactUserMatch(ctx context.Context, users repository.UserRepository, email string, username *string) *models.User {
	if email == "" {
		return nil
	}
	effective := ""
	if username != nil && *username != "" {
		effective = *username
	} else {
		effective = userops.UserFromEmail(email)
	}
	if effective == "" {
		return nil
	}
	u, err := users.FindByUsername(ctx, effective)
	if err != nil || u == nil {
		return nil
	}
	// EqualFold is case-insensitive but not accent-folding; the DB collation
	// (utf8mb4_unicode_ci) is stricter, so a mismatch here is fail-closed
	// (spurious 409, never adoption).
	if !strings.EqualFold(u.Email, email) {
		return nil
	}
	return u
}

func mapUserOpsErr(c *gin.Context, err error) {
	switch {
	case errorsIsAny(err, userops.ErrInvalidUsername):
		autoErr(c, http.StatusUnprocessableEntity, "unsupported", "invalid username or password: "+err.Error())
	case errorsIsAny(err, userops.ErrInvalidPackage):
		autoErr(c, http.StatusNotFound, "not_found", "hosting package not found")
	case errorsIsAny(err, userops.ErrKratosFailed, userops.ErrKratosSetPassword, userops.ErrKratosUnavailable):
		autoErr(c, http.StatusBadGateway, "internal", "identity provider error")
	case errorsIsAny(err, userops.ErrNoKratosIdentity):
		autoErr(c, http.StatusConflict, "conflict", "user has no identity-provider record")
	case errorsIsAny(err, userops.ErrAgentPassword):
		autoErr(c, http.StatusBadGateway, "internal", "OS password sync failed (identity already rotated)")
	default:
		autoErr(c, http.StatusBadGateway, "internal", "operation failed")
	}
}

func errorsIsAny(err error, targets ...error) bool {
	for _, t := range targets {
		if t != nil && errors.Is(err, t) {
			return true
		}
	}
	return false
}

// --- password ---

type automationPasswordReq struct {
	Password string `json:"password"`
}

func userPasswordHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		const action = "POST /automation/users/:id/password"
		id := c.Param("id")
		var req automationPasswordReq
		if !bindSigned(c, &req) {
			return
		}
		if len(req.Password) < 10 {
			autoErr(c, http.StatusUnprocessableEntity, "unsupported", "password must be at least 10 characters")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		u, err := cfg.Users.FindByID(ctx, id)
		if err != nil || u == nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		// Headless key never manages admins (ADR-0164 blanket refusal).
		if u.IsAdmin {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultDenied)
			autoErr(c, http.StatusConflict, "conflict", "refusing to change an admin user's password")
			return
		}
		if err := userops.RotatePassword(ctx, billingUserOpsDeps(cfg), u, req.Password); err != nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			mapUserOpsErr(c, err)
			return
		}
		auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultOK)
		autoOK(c, "password updated")
	}
}

// --- package ---

type automationPackageReq struct {
	PackageID string `json:"package_id"`
}

func userPackageHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		const action = "PUT /automation/users/:id/package"
		id := c.Param("id")
		var req automationPackageReq
		if !bindSigned(c, &req) {
			return
		}
		if req.PackageID == "" {
			autoErr(c, http.StatusUnprocessableEntity, "unsupported", "package_id required")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		u, err := cfg.Users.FindByID(ctx, id)
		if err != nil || u == nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		if u.IsAdmin {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultDenied)
			autoErr(c, http.StatusConflict, "conflict", "admin users have no hosting package")
			return
		}
		if err := userops.SetPackage(ctx, billingUserOpsDeps(cfg), u, &req.PackageID, cfg.LimitsReconciler); err != nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			mapUserOpsErr(c, err)
			return
		}
		auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultOK)
		autoOK(c, "package assigned")
	}
}

// --- delete ---

type automationDeleteReq struct {
	Confirm bool `json:"confirm"`
	DryRun  bool `json:"dry_run,omitempty"`
}

func userDeleteHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		const action = "DELETE /automation/users/:id"
		id := c.Param("id")
		var req automationDeleteReq
		if !bindSigned(c, &req) {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
		defer cancel()
		u, err := cfg.Users.FindByID(ctx, id)
		if err != nil || u == nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		if u.IsAdmin {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultDenied)
			autoErr(c, http.StatusConflict, "conflict", "refusing to delete an admin user")
			return
		}
		if req.DryRun {
			preview, _ := userops.PreviewDeleteCascade(ctx, billingUserOpsDeps(cfg), billingDeleteDeps(cfg), id)
			c.JSON(http.StatusOK, gin.H{
				"ok":      true,
				"status":  "dry_run",
				"message": "no changes made",
				"preview": preview,
			})
			return
		}
		// First destructive automation verb: demand the explicit signed
		// confirm bit (ADR-0157 reservation, ADR-0164 implementation).
		if !req.Confirm {
			autoErr(c, http.StatusUnprocessableEntity, "unsupported", "confirm:true required for delete")
			return
		}
		if err := userops.DeleteCascade(ctx, billingUserOpsDeps(cfg), billingDeleteDeps(cfg), u, "automation:"+tok.ID); err != nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			var dte *userops.DockerTeardownError
			if errors.As(err, &dte) {
				autoErr(c, http.StatusConflict, "conflict",
					"refusing to delete user: Docker app teardown failed for "+strings.Join(dte.Slugs, ", ")+" — resolve on the host and retry")
				return
			}
			autoErr(c, http.StatusBadGateway, "internal", "delete failed")
			return
		}
		auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultOK)
		notifyWrite(cfg, "automation.user.deleted", "warning", "Automation deleted a user",
			"User "+u.Email+" and all owned resources were deleted via the automation API.", "/jabali-admin/users")
		autoOK(c, "user "+id+" deleted")
	}
}

// --- login token (ADR-0165) ---

type automationLoginTokenReq struct {
	TTLSeconds int `json:"ttl_s,omitempty"`
	// ClientIP is accepted for wire-compat with the integration contract
	// but not enforced — Kratos recovery codes are not IP-bindable
	// (ADR-0165 compensating controls: single-use, short TTL, HTTPS).
	ClientIP string `json:"client_ip,omitempty"`
}

func userLoginTokenHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		const action = "POST /automation/users/:id/login-token"
		id := c.Param("id")
		var req automationLoginTokenReq
		if !bindSigned(c, &req) {
			return
		}
		ttl := req.TTLSeconds
		if ttl <= 0 {
			ttl = 300
		}
		if ttl < 60 {
			ttl = 60
		}
		if ttl > 3600 {
			ttl = 3600
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		u, err := cfg.Users.FindByID(ctx, id)
		if err != nil || u == nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		// A login link is a full session — never mint one for an admin
		// from a headless key (ADR-0164 blanket refusal).
		if u.IsAdmin {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultDenied)
			autoErr(c, http.StatusConflict, "conflict", "refusing to mint a login link for an admin user")
			return
		}
		if u.KratosIdentityID == nil || *u.KratosIdentityID == "" {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusConflict, "conflict", "user has no identity-provider record")
			return
		}

		rc, err := cfg.KratosClient.CreateRecoveryCode(ctx, *u.KratosIdentityID, strconv.Itoa(ttl)+"s")
		if err != nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusBadGateway, "internal", "identity provider could not issue a login link")
			return
		}

		// JAB-232 (ADR-0165 addendum): Kratos's code-strategy admin recovery
		// API returns a link WITHOUT the code embedded (the code is a separate
		// field designed for out-of-band email delivery), and opening that bare
		// link lands on /login unauthenticated. We keep the code strategy and
		// fold the code into the URL as a query param — the SPA's public
		// /recovery route reads flow+code and submits the redemption, which
		// creates the session. The code-in-URL is the same sensitivity class as
		// the link itself (both are full single-use, short-TTL login
		// credentials over HTTPS); it is never logged (audit records the mint
		// only, below). Parse+re-encode so we never assume `?flow=` vs `&`.
		loginURL := rc.RecoveryLink
		if parsed, perr := neturl.Parse(rc.RecoveryLink); perr == nil {
			q := parsed.Query()
			q.Set("code", rc.RecoveryCode)
			parsed.RawQuery = q.Encode()
			loginURL = parsed.String()
		}

		// Audit the MINT, never the link or code.
		auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultOK)
		c.JSON(http.StatusOK, gin.H{
			"ok":         true,
			"url":        loginURL,
			"expires_in": ttl,
			"message":    "one-time login link minted",
		})
	}
}

// --- usage ---

// snapshotDiskFields is the subset of the disk-usage snapshot payload the
// billing surface needs (payload shape: me_disk_usage.go diskUsageResponse).
type snapshotDiskFields struct {
	TotalBytes uint64 `json:"total_bytes"`
	QuotaBytes uint64 `json:"quota_bytes"`
}

type usageRow struct {
	UserID     string     `json:"user_id"`
	Username   string     `json:"username"`
	Email      string     `json:"email"`
	DiskUsedMB uint64     `json:"disk_used_mb"`
	DiskQuota  uint64     `json:"disk_quota_mb"`
	BWUsedMB   uint64     `json:"bw_used_mb"`
	BWQuota    uint64     `json:"bw_quota_mb"`
	BWWindow   string     `json:"bw_window"`
	ComputedAt *time.Time `json:"disk_computed_at,omitempty"`
}

// buildUsageRow composes one user's usage from pre-fetched batch data —
// callers do the batching (M13.1 no-N+1 rule).
func buildUsageRow(u *models.User, snap *models.DiskUsageSnapshot, bwBytes uint64, pkg *models.HostingPackage) usageRow {
	row := usageRow{
		UserID:   u.ID,
		Email:    u.Email,
		BWUsedMB: bwBytes / (1024 * 1024),
		BWWindow: "30d",
	}
	if u.Username != nil {
		row.Username = *u.Username
	}
	if snap != nil {
		var f snapshotDiskFields
		if err := json.Unmarshal([]byte(snap.Payload), &f); err == nil {
			row.DiskUsedMB = f.TotalBytes / (1024 * 1024)
			if f.QuotaBytes > 0 {
				row.DiskQuota = f.QuotaBytes / (1024 * 1024)
			}
		}
		ca := snap.ComputedAt
		row.ComputedAt = &ca
	}
	if pkg != nil {
		if row.DiskQuota == 0 {
			row.DiskQuota = uint64(pkg.DiskQuotaMB)
		}
		row.BWQuota = uint64(pkg.BandwidthQuotaMB)
	}
	return row
}

// bwWindow returns the shared prior-30-day window (matches the M13.1
// domains-list denormalization convention).
func bwWindow() (time.Time, time.Time) {
	now := time.Now().UTC()
	return now.AddDate(0, 0, -29), now
}

func userUsageHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		u, err := cfg.Users.FindByID(ctx, id)
		if err != nil || u == nil {
			autoErr(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		var snap *models.DiskUsageSnapshot
		if s, err := cfg.DiskSnapshots.Get(ctx, id); err == nil {
			snap = s
		}
		var bwBytes uint64
		if cfg.BWDaily != nil && cfg.Domains != nil {
			if owned, _, derr := cfg.Domains.ListByUserID(ctx, id, repository.ListOptions{Limit: 1000}); derr == nil && len(owned) > 0 {
				ids := make([]string, len(owned))
				for i := range owned {
					ids[i] = owned[i].ID
				}
				from, to := bwWindow()
				if sums, berr := cfg.BWDaily.SumByDomainIDs(ctx, ids, from, to); berr == nil {
					for _, v := range sums {
						bwBytes += v
					}
				}
			}
		}
		var pkg *models.HostingPackage
		if cfg.Packages != nil && u.PackageID != nil && *u.PackageID != "" {
			if p, perr := cfg.Packages.FindByID(ctx, *u.PackageID); perr == nil {
				pkg = p
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": buildUsageRow(u, snap, bwBytes, pkg)})
	}
}

func bulkUsageHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()

		users, _, err := cfg.Users.List(ctx, repository.ListOptions{Limit: 1000})
		if err != nil {
			autoErr(c, http.StatusBadGateway, "internal", "list users failed")
			return
		}

		// One batch per table (M13.1): snapshots, domains→bw, packages.
		snapByUser := map[string]*models.DiskUsageSnapshot{}
		if snaps, serr := cfg.DiskSnapshots.ListAll(ctx); serr == nil {
			for i := range snaps {
				snapByUser[snaps[i].UserID] = &snaps[i]
			}
		}

		bwByUser := map[string]uint64{}
		if cfg.BWDaily != nil && cfg.Domains != nil {
			if domains, _, derr := cfg.Domains.List(ctx, repository.ListOptions{Limit: 5000}); derr == nil && len(domains) > 0 {
				ids := make([]string, len(domains))
				ownerByDomain := make(map[string]string, len(domains))
				for i := range domains {
					ids[i] = domains[i].ID
					ownerByDomain[domains[i].ID] = domains[i].UserID
				}
				from, to := bwWindow()
				if sums, berr := cfg.BWDaily.SumByDomainIDs(ctx, ids, from, to); berr == nil {
					for domainID, bytes := range sums {
						bwByUser[ownerByDomain[domainID]] += bytes
					}
				}
			}
		}

		pkgByID := map[string]*models.HostingPackage{}
		if cfg.Packages != nil {
			if pkgs, _, perr := cfg.Packages.List(ctx, repository.ListOptions{Limit: 500}); perr == nil {
				for i := range pkgs {
					pkgByID[pkgs[i].ID] = &pkgs[i]
				}
			}
		}

		out := make([]usageRow, 0, len(users))
		for i := range users {
			u := &users[i]
			if u.IsAdmin {
				continue // admins are not billable accounts
			}
			var pkg *models.HostingPackage
			if u.PackageID != nil {
				pkg = pkgByID[*u.PackageID]
			}
			out = append(out, buildUsageRow(u, snapByUser[u.ID], bwByUser[u.ID], pkg))
		}
		c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
	}
}

// --- packages ---

func packagesListHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		rows, _, err := cfg.Packages.List(ctx, repository.ListOptions{Limit: 500})
		if err != nil {
			autoErr(c, http.StatusBadGateway, "internal", "list packages failed")
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for i := range rows {
			p := &rows[i]
			out = append(out, map[string]any{
				"id":                 p.ID,
				"name":               p.Name,
				"disk_quota_mb":      p.DiskQuotaMB,
				"bandwidth_quota_mb": p.BandwidthQuotaMB,
				"max_domains":        p.MaxDomains,
				"max_email_accounts": p.MaxEmailAccounts,
				"max_databases":      p.MaxDatabases,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
	}
}
