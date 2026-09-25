package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpsync"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// FTP/SFTP subaccounts API (GH #1053 step 5, plans/gh1053-ftp-accounts.md).
//
// Tenant self-service under /me/ftp-accounts, capped by the hosting
// package's max_ftp_accounts (0 = surface hidden, requests 403). Host
// mutations run SYNCHRONOUSLY against the agent and the sshd drop-in re-syncs
// before the response returns, so a fresh account can log in immediately
// instead of waiting a reconcile tick (step-3 review note). The reconciler
// remains the drift healer behind this path. The mutation ordering, agent
// calls, and compensation live in the FTP Account Lifecycle Module
// (internal/ftpops, JAB-276); these handlers resolve authorization and map
// its typed results to the wire.

// FtpAccountsHandlerConfig wires the tenant + admin FTP account routes.
type FtpAccountsHandlerConfig struct {
	Repo            repository.FtpAccountRepository
	Users           repository.UserRepository
	Packages        repository.PackageRepository
	Agent           agent.AgentInterface
	Log             *slog.Logger
	StrictRateLimit gin.HandlerFunc // optional — wire from rl.Strict()
	// PatchRateLimit is an optional per-actor (user-keyed) strict limiter for
	// the sshd-reload-triggering PATCH route (JAB-266). Wire from
	// rl.StrictPerActor(); nil disables it.
	PatchRateLimit gin.HandlerFunc
	// QuotaMount is the filesystem mount path /home lives on (the same value
	// the M18 user-limits reconciler uses, internal/limits.QuotaMountFor).
	// Required for GH #1145 isolated accounts (per-uid setquota); empty
	// disables isolation (the create refuses an isolated request).
	QuotaMount string
}

// ftpSubaccountUIDMin is the floor of the GH #1145 isolated-subaccount uid
// range: above the rootless-container subuid ceiling — see migration 000267 +
// the agent's ftpSubaccountUIDMin. Must match both. (The jail root and the
// naming/credential policy live in internal/ftpops.)
const ftpSubaccountUIDMin = 1000000000

// RegisterFtpAccountRoutes mounts:
//   - GET    /me/ftp-accounts               list caller's accounts
//   - POST   /me/ftp-accounts               create (cap-checked)
//   - PATCH  /me/ftp-accounts/:id           toggle ftp/sftp/enabled
//   - POST   /me/ftp-accounts/:id/password  reset password
//   - DELETE /me/ftp-accounts/:id           delete account
//   - GET    /admin/ftp-accounts            admin: list all
func RegisterFtpAccountRoutes(g *gin.RouterGroup, cfg FtpAccountsHandlerConfig) {
	if cfg.Repo == nil || cfg.Users == nil || cfg.Packages == nil {
		panic("api.RegisterFtpAccountRoutes: Repo, Users, and Packages are required")
	}
	h := &ftpAccountsHandler{cfg: cfg}
	strict := cfg.StrictRateLimit
	if strict == nil {
		strict = func(c *gin.Context) { c.Next() }
	}
	// JAB-266: PATCH toggling sftp_access rewrites and reloads global sshd on
	// every request. The strict tier alone is per-IP, which an actor multiplies
	// across addresses, so PATCH additionally takes a per-user strict budget.
	patchLimit := cfg.PatchRateLimit
	if patchLimit == nil {
		patchLimit = func(c *gin.Context) { c.Next() }
	}
	me := g.Group("/me/ftp-accounts")
	me.GET("", h.list)
	me.POST("", strict, h.create)
	me.PATCH("/:id", patchLimit, h.update)
	me.POST("/:id/password", strict, h.setPassword)
	me.DELETE("/:id", h.delete)

	admin := g.Group("/admin/ftp-accounts", middleware.RequireAdmin())
	admin.GET("", h.adminList)
	// JAB-258: ownership-safe override so an operator can revoke stranded
	// aliases (owner suspended, or package downgraded to zero FTP) that
	// the tenant's own routes may no longer create but must still be able
	// to remove.
	admin.PATCH("/:id", h.adminUpdate)
	admin.DELETE("/:id", h.adminDelete)
}

// adminResolveAccount loads an account by id (any owner) plus the owner's
// Linux username, for the admin override routes.
func (h *ftpAccountsHandler) adminResolveAccount(c *gin.Context) (*models.FtpAccount, string, bool) {
	ctx := c.Request.Context()
	acct, err := h.cfg.Repo.FindByID(ctx, c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return nil, "", false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, "", false
	}
	owner, uerr := h.cfg.Users.FindByID(ctx, acct.UserID)
	if uerr != nil || owner == nil || owner.Username == nil || *owner.Username == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "owner_unresolved", "detail": "account owner has no Linux username"})
		return nil, "", false
	}
	return acct, *owner.Username, true
}

// adminUpdate lets an operator flip is_enabled / ftp_access / sftp_access
// on any account regardless of the owner's package cap (JAB-258).
func (h *ftpAccountsHandler) adminUpdate(c *gin.Context) {
	var req ftpAccountUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	acct, ownerName, ok := h.adminResolveAccount(c)
	if !ok {
		return
	}
	if req.FTPAccess != nil {
		acct.FTPAccess = *req.FTPAccess
	}
	if req.SFTPAccess != nil {
		acct.SFTPAccess = *req.SFTPAccess
	}
	if req.WebDAVAccess != nil {
		acct.WebDAVAccess = *req.WebDAVAccess
	}
	if req.IsEnabled != nil {
		acct.IsEnabled = *req.IsEnabled
	}
	// JAB-276: the persist → detached host apply → sshd re-render ordering lives
	// in the shared lifecycle module, so this door and the tenant door run one
	// implementation (identical transcripts by construction).
	if err := ftpops.UpdateAccess(c.Request.Context(), h.ops(), acct, ownerName); err != nil {
		h.writeOpsErr(c, err, "update_failed")
		return
	}
	c.JSON(http.StatusOK, acct)
}

// adminDelete removes any account (host alias + row), owner-safe.
func (h *ftpAccountsHandler) adminDelete(c *gin.Context) {
	acct, ownerName, ok := h.adminResolveAccount(c)
	if !ok {
		return
	}
	if err := ftpops.Delete(c.Request.Context(), h.ops(), acct, ownerName); err != nil {
		h.writeOpsErr(c, err, "delete_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

type ftpAccountsHandler struct{ cfg FtpAccountsHandlerConfig }

type ftpAccountCreateRequest struct {
	Label      string `json:"label" binding:"required"`
	HomePath   string `json:"home_path" binding:"required"`
	Password   string `json:"password" binding:"required"`
	FTPAccess  bool   `json:"ftp_access"`
	SFTPAccess *bool  `json:"sftp_access"` // default true
	// WebDAVAccess (GH #1146) — the 3rd access protocol. Default false (opt-in).
	WebDAVAccess bool `json:"webdav_access"`
	// Isolated selects the GH #1145 separate-uid jailed model. Nil/false =
	// legacy same-uid alias (back-compat). The UI defaults this on; a direct
	// API caller that omits it stays legacy.
	Isolated *bool `json:"isolated"`
	// QuotaMB is the per-account disk cap (MB), REQUIRED when Isolated — an
	// isolated separate uid escapes the tenant's package quota without it.
	QuotaMB uint32 `json:"quota_mb"`
}

type ftpAccountUpdateRequest struct {
	FTPAccess    *bool `json:"ftp_access"`
	SFTPAccess   *bool `json:"sftp_access"`
	WebDAVAccess *bool `json:"webdav_access"` // GH #1146; nil = leave unchanged
	IsEnabled    *bool `json:"is_enabled"`
}

type ftpAccountPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// resolveTenant loads the calling user and enforces the package gate.
// Returns (user, package, ok). Writes the error response when !ok.
func (h *ftpAccountsHandler) resolveTenant(c *gin.Context) (*models.User, *models.HostingPackage, bool) {
	u, pkg, ok := h.resolveTenantForManagement(c)
	if !ok {
		return nil, nil, false
	}
	// JAB-258: the zero-cap gate applies to CREATION ONLY. Revocation
	// (list/disable/delete/password) must remain reachable after a
	// downgrade so a tenant can remove credentials the package no longer
	// entitles — resolveTenantForManagement omits this check.
	if pkg.MaxFTPAccounts == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "ftp_accounts_not_in_package", "detail": "your hosting package does not include FTP/SFTP accounts"})
		return nil, nil, false
	}
	return u, pkg, true
}

// resolveTenantForManagement loads the caller + package WITHOUT the
// zero-cap creation gate, so list/disable/delete/password stay available
// to revoke credentials after a package downgrade (JAB-258). It still
// requires a real Linux user and a resolvable package (a package-less
// tenant has no FTP surface at all).
func (h *ftpAccountsHandler) resolveTenantForManagement(c *gin.Context) (*models.User, *models.HostingPackage, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil, nil, false
	}
	ctx := c.Request.Context()
	u, err := h.cfg.Users.FindByID(ctx, claims.UserID)
	if err != nil || u == nil || u.Username == nil || *u.Username == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "no_account", "detail": "account has no Linux user"})
		return nil, nil, false
	}
	if u.PackageID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "ftp_accounts_require_package", "detail": "FTP/SFTP accounts require a hosting package that includes them"})
		return nil, nil, false
	}
	pkg, perr := h.cfg.Packages.FindByID(ctx, *u.PackageID)
	if perr != nil || pkg == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "no_package"})
		return nil, nil, false
	}
	return u, pkg, true
}

// ops builds the FTP Account Lifecycle Module dependencies from the handler config.
func (h *ftpAccountsHandler) ops() ftpops.Deps {
	return ftpops.Deps{
		Agent:      h.cfg.Agent,
		Accounts:   h.cfg.Repo,
		Users:      h.cfg.Users,
		Packages:   h.cfg.Packages,
		Log:        h.cfg.Log,
		QuotaMount: h.cfg.QuotaMount,
	}
}

// ftpValidationResponses maps each lifecycle-module validation reason to this
// door's status and error code; the detail comes from the module verbatim.
var ftpValidationResponses = map[error]struct {
	status int
	code   string
}{
	ftpops.ErrInvalidLabel:         {http.StatusUnprocessableEntity, "invalid_label"},
	ftpops.ErrLabelTooLong:         {http.StatusUnprocessableEntity, "label_too_long"},
	ftpops.ErrWeakPassword:         {http.StatusUnprocessableEntity, "weak_password"},
	ftpops.ErrInvalidHomePath:      {http.StatusUnprocessableEntity, "invalid_home_path"},
	ftpops.ErrQuotaRequired:        {http.StatusUnprocessableEntity, "quota_required"},
	ftpops.ErrIsolationUnavailable: {http.StatusServiceUnavailable, "isolation_unavailable"},
}

// writeOpsErr maps a lifecycle-module error to the response: a rejected input
// is its mapped validation code; a desired-state write failure is internal;
// anything else is a host (agent) failure.
func (h *ftpAccountsHandler) writeOpsErr(c *gin.Context, err error, fallback string) {
	var ve *ftpops.ValidationError
	if errors.As(err, &ve) {
		if r, ok := ftpValidationResponses[ve.Reason]; ok {
			c.JSON(r.status, gin.H{"error": r.code, "detail": ve.Detail})
			return
		}
	}
	if errors.Is(err, ftpops.ErrPersist) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	status, payload := h.mapAgentErr(err, fallback)
	c.JSON(status, payload)
}

// syncHostAccess re-renders the sshd drop-in from the full desired set by
// calling the shared ftpsync.SyncFtpHostAccess function. Called synchronously
// after every mutation so login state matches the API response.
func (h *ftpAccountsHandler) syncHostAccess(ctx context.Context, tenantUsername string) {
	ftpsync.SyncFtpHostAccess(ctx, h.cfg.Agent, h.cfg.Repo, h.cfg.Users, h.cfg.Packages, h.cfg.Log, tenantUsername)
}

func (h *ftpAccountsHandler) list(c *gin.Context) {
	u, _, ok := h.resolveTenantForManagement(c)
	if !ok {
		return
	}
	rows, err := h.cfg.Repo.ListByUserID(c.Request.Context(), u.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows), "page": 1, "page_size": len(rows)})
}

func (h *ftpAccountsHandler) create(c *gin.Context) {
	u, pkg, ok := h.resolveTenant(c)
	if !ok {
		return
	}
	var req ftpAccountCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	// JAB-276: validation, cap/quota reservation (JAB-262), isolated uid/jail
	// allocation, the host create, and its compensation live in the shared
	// lifecycle module; this door maps the typed result to its wire shape.
	acct, err := ftpops.Create(c.Request.Context(), h.ops(), ftpops.Owner{UserID: u.ID, Username: *u.Username}, pkg, ftpops.CreateRequest{
		Label:        req.Label,
		HomePath:     req.HomePath,
		Password:     req.Password,
		FTPAccess:    req.FTPAccess,
		SFTPAccess:   req.SFTPAccess,
		WebDAVAccess: req.WebDAVAccess,
		Isolated:     req.Isolated != nil && *req.Isolated,
		QuotaMB:      req.QuotaMB,
	})
	if err != nil {
		switch {
		case errors.Is(err, ftpops.ErrUIDAllocation):
			c.JSON(http.StatusInternalServerError, gin.H{"error": "uid_alloc_failed"})
		case errors.Is(err, repository.ErrFtpCapExceeded):
			c.JSON(http.StatusConflict, gin.H{"error": "ftp_account_quota_exceeded", "detail": "you have reached your FTP/SFTP account limit"})
		case errors.Is(err, repository.ErrFtpQuotaSplitExceeded):
			c.JSON(http.StatusConflict, gin.H{"error": "quota_split_exceeded", "detail": "isolated accounts would exceed the package disk quota"})
		case errors.Is(err, repository.ErrConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "account_exists"})
		default:
			h.writeOpsErr(c, err, "create_failed")
		}
		return
	}
	c.JSON(http.StatusCreated, acct)
}

func (h *ftpAccountsHandler) update(c *gin.Context) {
	u, _, ok := h.resolveTenantForManagement(c)
	if !ok {
		return
	}
	var req ftpAccountUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	ctx := c.Request.Context()
	acct, err := h.cfg.Repo.FindByIDAndUserID(ctx, c.Param("id"), u.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if req.FTPAccess != nil {
		acct.FTPAccess = *req.FTPAccess
	}
	if req.SFTPAccess != nil {
		acct.SFTPAccess = *req.SFTPAccess
	}
	if req.WebDAVAccess != nil {
		acct.WebDAVAccess = *req.WebDAVAccess
	}
	if req.IsEnabled != nil {
		acct.IsEnabled = *req.IsEnabled
	}
	// JAB-269 / JAB-276: persist-first, detached host apply, and the sshd
	// re-render run in the shared lifecycle module (same implementation as the
	// admin door). A host failure leaves the committed row in place for the
	// reconciler to converge to; only the error is reported.
	if err := ftpops.UpdateAccess(ctx, h.ops(), acct, *u.Username); err != nil {
		h.writeOpsErr(c, err, "update_failed")
		return
	}
	c.JSON(http.StatusOK, acct)
}

func (h *ftpAccountsHandler) setPassword(c *gin.Context) {
	u, _, ok := h.resolveTenantForManagement(c)
	if !ok {
		return
	}
	var req ftpAccountPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	// Validate before the lookup so a weak password is rejected (422) even for
	// an id the caller doesn't own — the order the door has always had.
	if err := ftpops.ValidatePassword(req.Password); err != nil {
		h.writeOpsErr(c, err, "password_reset_failed")
		return
	}
	ctx := c.Request.Context()
	acct, err := h.cfg.Repo.FindByIDAndUserID(ctx, c.Param("id"), u.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if err := ftpops.SetPassword(ctx, h.ops(), acct, *u.Username, req.Password); err != nil {
		h.writeOpsErr(c, err, "password_reset_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": true})
}

func (h *ftpAccountsHandler) delete(c *gin.Context) {
	u, _, ok := h.resolveTenantForManagement(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	acct, err := h.cfg.Repo.FindByIDAndUserID(ctx, c.Param("id"), u.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Host first (in the shared module): the row is the only handle, so it must
	// outlive the host alias — a failed host delete keeps the row for the retry.
	if err := ftpops.Delete(ctx, h.ops(), acct, *u.Username); err != nil {
		h.writeOpsErr(c, err, "delete_failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func (h *ftpAccountsHandler) adminList(c *gin.Context) {
	rows, err := h.cfg.Repo.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows), "page": 1, "page_size": len(rows)})
}

// mapFtpAgentError translates agent failures into API responses without
// leaking raw agent internals for the common typed cases.
func mapFtpAgentError(err error, fallback string) (int, gin.H) {
	var ae *agent.AgentError
	if errors.As(err, &ae) {
		switch ae.Code {
		case "invalid_argument":
			return http.StatusUnprocessableEntity, gin.H{"error": fallback, "detail": ae.Message}
		case "already_exists":
			return http.StatusConflict, gin.H{"error": "account_exists", "detail": ae.Message}
		case "not_found":
			return http.StatusNotFound, gin.H{"error": "not_found", "detail": ae.Message}
		case "permission_denied":
			return http.StatusForbidden, gin.H{"error": "forbidden", "detail": ae.Message}
		case "failed_precondition":
			// A host-state precondition isn't met — e.g. an isolated account
			// needs filesystem disk quota that isn't enabled on this host
			// (GH #1053). The agent's message is operator-actionable and safe
			// to surface; mirror the QuotaMount=="" guard's 503
			// isolation_unavailable so the UI treats both the same.
			return http.StatusServiceUnavailable, gin.H{"error": "isolation_unavailable", "detail": ae.Message}
		}
	}
	return http.StatusBadGateway, gin.H{"error": fallback, "detail": "host operation failed — try again or contact the administrator"}
}

// mapAgentErr logs the raw agent failure (op + code + detail) server-side so a
// 648-class error is diagnosable from the panel log — mapFtpAgentError
// deliberately hides host internals from the API response, which previously
// dropped the cause entirely (GH #1053). It then maps to the client response.
func (h *ftpAccountsHandler) mapAgentErr(err error, fallback string) (int, gin.H) {
	if h.cfg.Log != nil {
		var ae *agent.AgentError
		if errors.As(err, &ae) {
			h.cfg.Log.Warn("ftp: agent op failed", "op", fallback, "code", ae.Code, "detail", ae.Message)
		} else {
			h.cfg.Log.Warn("ftp: agent op failed", "op", fallback, "err", err)
		}
	}
	return mapFtpAgentError(err, fallback)
}
