package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpsync"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// FTP/SFTP subaccounts API (GH #1053 step 5, plans/gh1053-ftp-accounts.md).
//
// Tenant self-service under /me/ftp-accounts, capped by the hosting
// package's max_ftp_accounts (0 = surface hidden, requests 403). Host
// mutations run SYNCHRONOUSLY against the agent — create/delete talk to the
// host first, the DB row second, and the sshd drop-in re-syncs before the
// response returns, so a fresh account can log in immediately instead of
// waiting a reconcile tick (step-3 review note). The reconciler remains the
// drift healer behind this path.

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

// GH #1145 isolated-subaccount constants. These MUST match the panel-agent
// (ftp_account_jail.go) and migration 000267 — the panel computes the jail
// path + validates the uid range the agent re-checks.
const (
	ftpJailRoot       = "/var/lib/jabali-ftp-jails"
	ftpJailMountpoint = "data"
	// Above the rootless-container subuid ceiling — see migration 000267 + the
	// agent's ftpSubaccountUIDMin. Must match both.
	ftpSubaccountUIDMin = 1000000000
)

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
	if req.IsEnabled != nil {
		acct.IsEnabled = *req.IsEnabled
	}
	ctx := c.Request.Context()
	if err := h.agentCall(ctx, "ftpaccount.set_access", map[string]any{
		"tenant_username": ownerName,
		"username":        acct.Username,
		"ftp_access":      acct.FTPAccess,
		"enabled":         acct.IsEnabled,
	}); err != nil {
		status, payload := mapFtpAgentError(err, "update_failed")
		c.JSON(status, payload)
		return
	}
	acct.UpdatedAt = time.Now().UTC()
	if err := h.cfg.Repo.Update(ctx, acct); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.syncHostAccess(ctx, ownerName)
	c.JSON(http.StatusOK, acct)
}

// adminDelete removes any account (host alias + row), owner-safe.
func (h *ftpAccountsHandler) adminDelete(c *gin.Context) {
	acct, ownerName, ok := h.adminResolveAccount(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := h.agentCall(ctx, "ftpaccount.delete", map[string]any{
		"tenant_username": ownerName,
		"username":        acct.Username,
	}); err != nil {
		status, payload := mapFtpAgentError(err, "delete_failed")
		c.JSON(status, payload)
		return
	}
	if err := h.cfg.Repo.Delete(ctx, acct.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.syncHostAccess(ctx, ownerName)
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

type ftpAccountsHandler struct{ cfg FtpAccountsHandlerConfig }

// ftpLabelRE mirrors the agent's subaccount label rule — validate at the
// panel boundary too (defense in depth; the agent re-checks).
var ftpLabelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,19}$`)

const (
	ftpUsernameMaxLen  = 32
	ftpPasswordMinLen  = 12
	ftpPasswordMaxLen  = 128
	ftpAgentTimeout    = 30 * time.Second
	ftpHomePathBadRune = " \t\r\n\"'\\:"
)

type ftpAccountCreateRequest struct {
	Label      string `json:"label" binding:"required"`
	HomePath   string `json:"home_path" binding:"required"`
	Password   string `json:"password" binding:"required"`
	FTPAccess  bool   `json:"ftp_access"`
	SFTPAccess *bool  `json:"sftp_access"` // default true
	// Isolated selects the GH #1145 separate-uid jailed model. Nil/false =
	// legacy same-uid alias (back-compat). The UI defaults this on; a direct
	// API caller that omits it stays legacy.
	Isolated *bool `json:"isolated"`
	// QuotaMB is the per-account disk cap (MB), REQUIRED when Isolated — an
	// isolated separate uid escapes the tenant's package quota without it.
	QuotaMB uint32 `json:"quota_mb"`
}

type ftpAccountUpdateRequest struct {
	FTPAccess  *bool `json:"ftp_access"`
	SFTPAccess *bool `json:"sftp_access"`
	IsEnabled  *bool `json:"is_enabled"`
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

// validateHomePath enforces the panel-side half of the home_path contract:
// absolute, clean, safe charset, inside the tenant home. The agent
// additionally symlink-resolves it.
func validateHomePath(homePath, tenantHome string) error {
	if !filepath.IsAbs(homePath) || filepath.Clean(homePath) != homePath {
		return errors.New("home_path must be an absolute, clean path")
	}
	if strings.ContainsAny(homePath, ftpHomePathBadRune) {
		return errors.New("home_path must not contain whitespace, quotes, backslashes, or colons")
	}
	rel, err := filepath.Rel(tenantHome, homePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("home_path must be inside your home directory (%s)", tenantHome)
	}
	return nil
}

func (h *ftpAccountsHandler) agentCall(ctx context.Context, method string, params map[string]any) error {
	if h.cfg.Agent == nil {
		return errors.New("agent unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, ftpAgentTimeout)
	defer cancel()
	_, err := h.cfg.Agent.Call(callCtx, method, params)
	return err
}

// syncHostAccess re-renders the sshd drop-in from the full desired set and
// flips the tenant home to the M12 chroot layout. Called synchronously
// after every mutation so login state matches the API response.
func (h *ftpAccountsHandler) syncHostAccess(ctx context.Context, tenantUsername string) {
	if h.cfg.Agent == nil {
		return
	}
	if _, err := h.cfg.Agent.Call(ctx, "ssh.user.home_chown", map[string]any{
		"username": tenantUsername, "mode": "sftp",
	}); err != nil && h.cfg.Log != nil {
		h.cfg.Log.Warn("ftp: home chroot flip failed (reconciler will retry)", "tenant", tenantUsername, "err", err)
	}
	// Stamp the generation BEFORE reading the snapshot. The agent's stale-drop
	// gate is only sound if stamp order precedes read order: a sync that can
	// override a revocation carries a higher generation, so it was stamped after
	// the revocation's stamp, so — stamping before reading — it read after the
	// revocation committed and its own snapshot already reflects the revocation.
	// Stamping at dispatch (after the read) reopens the JAB-267 inversion.
	gen := ftpsync.NextGeneration()
	rows, err := h.cfg.Repo.List(ctx)
	if err != nil {
		return
	}
	type syncAccount struct {
		Username  string `json:"username"`
		ChrootDir string `json:"chroot_dir"`
		StartDir  string `json:"start_dir"`
	}
	desired := []syncAccount{}
	nameCache := map[string]string{}
	for _, a := range rows {
		if !a.IsEnabled || !a.SFTPAccess {
			continue
		}
		uname, ok := nameCache[a.UserID]
		if !ok {
			u, uerr := h.cfg.Users.FindByID(ctx, a.UserID)
			if uerr != nil || u == nil || u.Username == nil {
				nameCache[a.UserID] = ""
				continue
			}
			uname = *u.Username
			nameCache[a.UserID] = uname
		}
		if uname == "" {
			continue
		}
		var chroot, start string
		if a.Isolated && a.JailPath != "" {
			// GH #1145: isolated accounts chroot to their root-owned jail; the
			// selected sub-tree is bind-mounted at /<mountpoint> inside it.
			chroot = a.JailPath
			start = "/" + ftpJailMountpoint
		} else {
			chroot = "/home/" + uname
			start = "/"
			if rel, rerr := filepath.Rel(chroot, a.HomePath); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				start = "/" + rel
			}
		}
		desired = append(desired, syncAccount{Username: a.Username, ChrootDir: chroot, StartDir: start})
	}
	if err := h.agentCall(ctx, "ftpaccount.sshd_sync", map[string]any{
		"accounts":   desired,
		"generation": gen,
	}); err != nil && h.cfg.Log != nil {
		h.cfg.Log.Warn("ftp: sshd_sync failed (reconciler will retry)", "err", err)
	}
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
	ctx := c.Request.Context()
	tenant := *u.Username

	if !ftpLabelRE.MatchString(req.Label) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_label", "detail": "label must be lowercase letters, digits, or underscores (max 20 chars)"})
		return
	}
	username := tenant + "_" + req.Label
	if len(username) > ftpUsernameMaxLen {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "label_too_long", "detail": fmt.Sprintf("full account name %q exceeds %d characters", username, ftpUsernameMaxLen)})
		return
	}
	if len(req.Password) < ftpPasswordMinLen || len(req.Password) > ftpPasswordMaxLen {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "weak_password", "detail": fmt.Sprintf("password must be %d-%d characters", ftpPasswordMinLen, ftpPasswordMaxLen)})
		return
	}
	tenantHome := "/home/" + tenant
	if err := validateHomePath(req.HomePath, tenantHome); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_home_path", "detail": err.Error()})
		return
	}
	count, err := h.cfg.Repo.CountByUserID(ctx, u.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "quota_check_failed"})
		return
	}
	if count >= int64(pkg.MaxFTPAccounts) {
		c.JSON(http.StatusConflict, gin.H{"error": "ftp_account_quota_exceeded", "detail": "you have reached your FTP/SFTP account limit"})
		return
	}

	sftpAccess := true
	if req.SFTPAccess != nil {
		sftpAccess = *req.SFTPAccess
	}

	isolated := req.Isolated != nil && *req.Isolated
	createParams := map[string]any{
		"tenant_username": tenant,
		"username":        username,
		"home_path":       req.HomePath,
		"password":        req.Password,
		"ftp_access":      req.FTPAccess,
	}
	var allocUID uint32
	var jailPath string
	if isolated {
		if h.cfg.QuotaMount == "" {
			// setquota can't run without a mount → an isolated uid would be
			// unquota'd (disk-fill). Refuse rather than ship an unenforced one.
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "isolation_unavailable", "detail": "per-account disk quota is not configured on this host"})
			return
		}
		if req.QuotaMB == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "quota_required", "detail": "an isolated account requires a disk quota (quota_mb, in MB)"})
			return
		}
		// Split allocation: Σ existing isolated sub quotas + this one must fit
		// the package quota (when the package is capped; 0 = unlimited package,
		// but the per-account cap still bounds the sub).
		if pkg.DiskQuotaMB > 0 {
			used, serr := h.cfg.Repo.SumIsolatedQuotaByUserID(ctx, u.ID)
			if serr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "quota_check_failed"})
				return
			}
			if used+uint64(req.QuotaMB) > uint64(pkg.DiskQuotaMB) {
				c.JSON(http.StatusConflict, gin.H{"error": "quota_split_exceeded", "detail": fmt.Sprintf("isolated accounts would total %d MB, over the package quota of %d MB", used+uint64(req.QuotaMB), pkg.DiskQuotaMB)})
				return
			}
		}
		var aerr error
		allocUID, aerr = h.cfg.Repo.AllocateUID(ctx)
		if aerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "uid_alloc_failed"})
			return
		}
		jailPath = ftpJailRoot + "/" + tenant + "/" + username
		createParams["isolated"] = true
		createParams["uid"] = allocUID
		createParams["quota_mb"] = req.QuotaMB
		createParams["quota_mount"] = h.cfg.QuotaMount
		createParams["jail_path"] = jailPath
	}

	// Host first, row second: a row without a host user is a broken
	// credential; a host user without a row is swept by the reconciler.
	if err := h.agentCall(ctx, "ftpaccount.create", createParams); err != nil {
		status, payload := mapFtpAgentError(err, "create_failed")
		c.JSON(status, payload)
		return
	}

	now := time.Now().UTC()
	acct := &models.FtpAccount{
		ID:         ids.NewULID(),
		UserID:     u.ID,
		Username:   username,
		HomePath:   req.HomePath,
		FTPAccess:  req.FTPAccess,
		SFTPAccess: sftpAccess,
		IsEnabled:  true,
		Isolated:   isolated,
		QuotaMB:    req.QuotaMB,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if isolated {
		acct.UID = &allocUID
		acct.JailPath = jailPath
	}
	if err := h.cfg.Repo.Create(ctx, acct); err != nil {
		// Compensate: remove the host alias so no unaccounted access
		// survives a DB failure.
		_ = h.agentCall(ctx, "ftpaccount.delete", map[string]any{
			"tenant_username": tenant, "username": username,
		})
		if errors.Is(err, repository.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "account_exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.syncHostAccess(ctx, tenant)
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
	if req.IsEnabled != nil {
		acct.IsEnabled = *req.IsEnabled
	}
	if err := h.agentCall(ctx, "ftpaccount.set_access", map[string]any{
		"tenant_username": *u.Username,
		"username":        acct.Username,
		"ftp_access":      acct.FTPAccess,
		"enabled":         acct.IsEnabled,
	}); err != nil {
		status, payload := mapFtpAgentError(err, "update_failed")
		c.JSON(status, payload)
		return
	}
	acct.UpdatedAt = time.Now().UTC()
	if err := h.cfg.Repo.Update(ctx, acct); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.syncHostAccess(ctx, *u.Username)
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
	if len(req.Password) < ftpPasswordMinLen || len(req.Password) > ftpPasswordMaxLen {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "weak_password", "detail": fmt.Sprintf("password must be %d-%d characters", ftpPasswordMinLen, ftpPasswordMaxLen)})
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
	if err := h.agentCall(ctx, "ftpaccount.set_password", map[string]any{
		"tenant_username": *u.Username,
		"username":        acct.Username,
		"password":        req.Password,
		// JAB-261: chpasswd drops the shadow lock; send the desired lock
		// state so the agent re-locks a disabled account in the same verb.
		"enabled": acct.IsEnabled,
	}); err != nil {
		status, payload := mapFtpAgentError(err, "password_reset_failed")
		c.JSON(status, payload)
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
	// Host first: the row is the only handle, so it must outlive the host
	// alias (a failed host delete keeps the row for the retry; the reverse
	// order would strand an unaccounted host credential).
	if err := h.agentCall(ctx, "ftpaccount.delete", map[string]any{
		"tenant_username": *u.Username,
		"username":        acct.Username,
	}); err != nil {
		status, payload := mapFtpAgentError(err, "delete_failed")
		c.JSON(status, payload)
		return
	}
	if err := h.cfg.Repo.Delete(ctx, acct.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.syncHostAccess(ctx, *u.Username)
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
		}
	}
	return http.StatusBadGateway, gin.H{"error": fallback, "detail": "host operation failed — try again or contact the administrator"}
}
