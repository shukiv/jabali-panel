package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// UserHandlerConfig plugs the users resource handlers into the router. Repo
// is the only required field; BcryptCost defaults to bcrypt.DefaultCost;
// Agent is optional and used for best-effort OS user provisioning.
type UserHandlerConfig struct {
	Repo            repository.UserRepository
	BcryptCost      int
	Agent           agent.AgentInterface
	StrictRateLimit gin.HandlerFunc
	Domains         repository.DomainRepository
	Databases       repository.DatabaseRepository
	DatabaseUsers   repository.DatabaseUserRepository
	DockerApps      repository.DockerAppRepository
	Mailboxes       repository.MailboxRepository
	Packages        repository.PackageRepository
	Reconciler      *reconciler.Reconciler
	Log             *slog.Logger
	// Redis is the shared client used to revoke a tenant's wp_<osuser> cache
	// ACL user on the delete cascade (GH #408). Optional; nil skips revoke.
	Redis *redis.Client

	// KratosClient is the (required in production) identity-provider client.
	// POST /users atomically creates both a panel user row and a Kratos
	// identity; if the client is nil (dev-without-Kratos), the identity
	// write is skipped and the panel row is created in isolation.
	KratosClient *kratosclient.Client
}

// kratosEnabled reports whether the Kratos client is wired up on this handler
// config. Dev/test paths leave it nil; production always sets it.
func (c UserHandlerConfig) kratosEnabled() bool {
	return c.KratosClient != nil
}

// Paging defaults/limits chosen so a misbehaving client can't issue
// million-row sweeps, and the SPA can ask for reasonable page sizes without
// extra config.
const (
	defaultUsersPageSize = 20
	maxUsersPageSize     = 200
)

// RegisterUserRoutes mounts /users* on g. g must already enforce RequireAuth.
//
// Authorisation:
//   - list / create / delete → admin only (RequireAdmin)
//   - get / patch            → admin or owner (RequireOwner)
//
// Fine-grained rules (can't demote the last admin, owner must provide
// current_password to change their own password, etc.) live inside the
// handler functions where they can return informative errors.
func RegisterUserRoutes(g *gin.RouterGroup, cfg UserHandlerConfig) {
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	h := &userHandler{cfg: cfg}

	g.GET("/users", middleware.RequireAdmin(), h.list)
	g.POST("/users", middleware.RequireAdmin(), h.create)
	g.GET("/users/:id", middleware.RequireOwner("id"), h.get)
	g.PATCH("/users/:id", middleware.RequireOwner("id"), h.update)
	g.DELETE("/users/:id", middleware.RequireAdmin(), h.delete)
	reprov := []gin.HandlerFunc{middleware.RequireAdmin()}
	if cfg.StrictRateLimit != nil {
		reprov = append(reprov, cfg.StrictRateLimit)
	}
	reprov = append(reprov, h.reprovision)
	g.POST("/users/:id/reprovision", reprov...)

	// Admin-only per-user systemd slice status (Step 8 of per-user-slices).
	g.GET("/admin/users/:id/slice-status", middleware.RequireAdmin(), h.sliceStatus)
	// GH #338 active sessions.
	g.GET("/admin/sessions", middleware.RequireAdmin(), h.listSessions)
	g.DELETE("/admin/sessions/:id", middleware.RequireAdmin(), h.revokeSession)

	// Admin-only 2FA reset for locked-out users (Kratos JSON-Patch
	// removes totp + lookup_secret credentials).
	g.POST("/admin/users/:id/2fa/reset", middleware.RequireAdmin(), h.reset2FA)
	g.POST("/admin/users/:id/password/reset", middleware.RequireAdmin(), h.resetPassword)

	// Admin-only user suspend / unsuspend. Suspending pushes the
	// user offline in three steps: flag, Kratos state=inactive,
	// bulk-disable owned domains. See users_suspend.go.
	g.POST("/admin/users/:id/suspend", middleware.RequireAdmin(), h.suspend)
	g.POST("/admin/users/:id/unsuspend", middleware.RequireAdmin(), h.unsuspend)
}

type userHandler struct{ cfg UserHandlerConfig }

// ---------- request / response shapes ----------

type createUserRequest struct {
	Email         string  `json:"email"                    binding:"omitempty,email"`
	Password      string  `json:"password"                 binding:"required,min=10"`
	Username      *string `json:"username,omitempty"       binding:"omitempty,min=1,max=32"`
	NameFirst     string  `json:"name_first"`
	NameLast      string  `json:"name_last"`
	IsAdmin       bool    `json:"is_admin"`
	PackageID     *string `json:"package_id,omitempty"`
	SkipProvision bool    `json:"skip_provision,omitempty"`
}

// updateUserRequest uses pointers so the handler can distinguish "omit this
// field" from "set this field to the zero value" (e.g. clearing a name).
// Passwords are intentionally absent: they live in Kratos post-M20, and
// users change them through the Kratos self-service settings flow rather
// than PATCHing a panel row.
type updateUserRequest struct {
	// Email is validated in the handler, not via the binding `email` tag: a
	// *string pointer to "" is non-nil, so validator omitempty does NOT skip
	// it and the empty string fails `email` (GH #258 — email is optional).
	Email     *string `json:"email,omitempty"`
	NameFirst *string `json:"name_first,omitempty"`
	NameLast  *string `json:"name_last,omitempty"`
	IsAdmin   *bool   `json:"is_admin,omitempty"`
	PackageID *string `json:"package_id,omitempty"`
	// WebmailEnabled (GH #316) toggles webmail for all of this user's domains.
	// Admin-only; mirrors PackageID handling.
	WebmailEnabled *bool `json:"webmail_enabled,omitempty"`
	// Password, when set, rotates the user's auth password: bcrypt-hashed
	// into the DB row, pushed to Kratos via Identity API, and (for users
	// with an OS account) synced to the system passwd via the agent's
	// user.password command. Empty string is treated as "no change" so the
	// admin UI can omit the field on submits where the form left it blank.
	Password *string `json:"password,omitempty" binding:"omitempty,min=10"`
	// CurrentPassword is required when a non-admin OWNER rotates their own
	// password (GH #482): re-authentication before a password change. Admins
	// resetting another user's password do not supply it.
	CurrentPassword *string `json:"current_password,omitempty"`
}

type reprovisionRequest struct {
	Password string `json:"password" binding:"required,min=10"`
}

type listUsersResponse struct {
	Data     []models.User `json:"data"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// ---------- handlers ----------

func (h *userHandler) list(c *gin.Context) {
	page, pageSize, opts := parseListOptions(c, defaultUsersPageSize, maxUsersPageSize)
	// Optional ?is_admin=true|false scopes the result. Anything else is
	// silently ignored so the legacy "list all" behaviour stays intact.
	switch c.Query("is_admin") {
	case "true":
		t := true
		opts.IsAdmin = &t
	case "false":
		f := false
		opts.IsAdmin = &f
	}
	// Optional ?suspended=true|false filter. Admin Users page chips
	// All / Active / Suspended map to absent / false / true respectively.
	switch c.Query("suspended") {
	case "true":
		t := true
		opts.Suspended = &t
	case "false":
		f := false
		opts.Suspended = &f
	}
	users, total, err := h.cfg.Repo.List(c.Request.Context(), opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, listUsersResponse{
		Data:     users,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	})
}

func (h *userHandler) get(c *gin.Context) {
	u, err := h.cfg.Repo.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.translateErr(c, err)
		return
	}
	c.JSON(http.StatusOK, u)
}

// create is a thin REST wrapper around panel-api/internal/userops.Create.
// All validation, kratos atomic, agent provisioning, prefix handling
// and the panel-side row insert live in userops; this handler is
// purely HTTP envelope + status mapping (M41 ADR-0083 follow-up).
func (h *userHandler) create(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	res, err := userops.Create(c.Request.Context(), userops.Deps{
		Users:        h.cfg.Repo,
		Packages:     h.cfg.Packages,
		Agent:        h.cfg.Agent,
		KratosClient: h.cfg.KratosClient,
		BcryptCost:   h.cfg.BcryptCost,
		Log:          h.cfg.Log,
	}, userops.CreateInput{
		Email:         req.Email,
		Password:      req.Password,
		Username:      req.Username,
		NameFirst:     req.NameFirst,
		NameLast:      req.NameLast,
		IsAdmin:       req.IsAdmin,
		PackageID:     req.PackageID,
		SkipProvision: req.SkipProvision,
	})
	if err != nil {
		userOpsRESTError(c, err)
		return
	}
	if res.ProvisionWarning != "" {
		c.JSON(http.StatusCreated, struct {
			*models.User
			ProvisionWarning string `json:"provision_warning"`
		}{
			User:             res.User,
			ProvisionWarning: res.ProvisionWarning,
		})
		return
	}
	c.JSON(http.StatusCreated, res.User)
}

// userOpsRESTError translates userops sentinels to HTTP status + JSON.
func userOpsRESTError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, userops.ErrInvalidUsername):
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_username",
			"detail": err.Error(),
		})
	case errors.Is(err, userops.ErrInvalidPackage):
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_package_id",
			"detail": "hosting package not found",
		})
	case errors.Is(err, userops.ErrUsernameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "username_taken"})
	case errors.Is(err, userops.ErrEmailTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "email_taken"})
	case errors.Is(err, userops.ErrKratosFailed):
		c.JSON(http.StatusBadGateway, gin.H{
			"error":  "identity_provider_failed",
			"detail": err.Error(),
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}

func (h *userHandler) update(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		// Defence in depth — RequireAuth + RequireOwner should have stopped this.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// GH #258: email is optional. An empty string clears it; a non-empty value
	// must be a valid address. (createUserRequest's plain-string field already
	// allows empty via omitempty; the update pointer needs this explicit pass.)
	if req.Email != nil {
		if e := strings.TrimSpace(*req.Email); e != "" {
			if _, perr := mail.ParseAddress(e); perr != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_email", "detail": "email must be a valid address or empty"})
				return
			}
			*req.Email = e
		} else {
			*req.Email = ""
		}
	}

	// Only admins may toggle is_admin. A non-admin owner who sends the field
	// (even with their own current value) gets 403 — easier to reason about
	// than silently stripping it.
	if req.IsAdmin != nil && !claims.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	ctx := c.Request.Context()
	id := c.Param("id")

	existing, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		h.translateErr(c, err)
		return
	}

	// Snapshot pre-update package assignment so the trailing
	// reconcile-limits dispatch only fires on actual changes.
	prevPackageID := ""
	if existing.PackageID != nil {
		prevPackageID = *existing.PackageID
	}

	// Refuse demoting the last admin — otherwise a careless PATCH locks
	// everyone out. Check BEFORE mutating anything.
	if req.IsAdmin != nil && existing.IsAdmin && !*req.IsAdmin {
		n, err := h.cfg.Repo.CountAdmins(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if n <= 1 {
			c.JSON(http.StatusConflict, gin.H{"error": "cannot_demote_last_admin"})
			return
		}
	}

	// Apply field-level updates. Repo.Update explicitly excludes is_admin.
	if req.Email != nil {
		existing.Email = *req.Email
	}
	if req.NameFirst != nil {
		existing.NameFirst = *req.NameFirst
	}
	if req.NameLast != nil {
		existing.NameLast = *req.NameLast
	}
	// Validate and apply package_id if provided (including clearing it with empty
	// string). Admin-only (GH #481): package assignment controls quotas/feature
	// limits, so a non-admin owner cannot move themselves between packages — the
	// field is silently ignored for non-admins (the owner UI never sends it).
	if req.PackageID != nil && claims.IsAdmin {
		if *req.PackageID != "" {
			if h.cfg.Packages == nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			_, err := h.cfg.Packages.FindByID(ctx, *req.PackageID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					c.JSON(http.StatusBadRequest, gin.H{
						"error":  "invalid_package_id",
						"detail": "hosting package not found",
					})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			existing.PackageID = req.PackageID
		} else {
			// Empty string means clear the package assignment
			existing.PackageID = nil
		}
	}

	// Per-user webmail toggle (GH #316): admin-only, like package_id. The owner
	// UI never sends it, so silently ignore for non-admins.
	if req.WebmailEnabled != nil && claims.IsAdmin {
		existing.WebmailEnabled = *req.WebmailEnabled
	}

	if err := h.cfg.Repo.Update(ctx, existing); err != nil {
		h.translateErr(c, err)
		return
	}

	// If the package assignment actually changed, kick the limits
	// reconciler immediately so POSIX quota + cgroup drop-ins land
	// without waiting for the next 60s ReconcileAll tick. Background
	// goroutine — PATCH responds as soon as the DB row is durable.
	newPackageID := ""
	if existing.PackageID != nil {
		newPackageID = *existing.PackageID
	}
	if newPackageID != prevPackageID && h.cfg.Reconciler != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			h.cfg.Reconciler.ReconcileUserLimits(bgCtx)
		}()
	}

	// Flip is_admin in its own call so the repo's privilege-safe Update
	// doesn't have to widen. Admin-only guard was checked above.
	if req.IsAdmin != nil {
		if err := h.cfg.Repo.SetAdmin(ctx, id, *req.IsAdmin); err != nil {
			h.translateErr(c, err)
			return
		}
		existing.IsAdmin = *req.IsAdmin
	}

	// Password rotation. Order: hash -> Kratos -> OS (agent) -> DB.
	// Kratos is the authoritative auth backend post-M20; if it fails the
	// DB hash stays old so login keeps working with the previous password.
	// OS sync is best-effort; failure surfaces as 502 with detail but the
	// Kratos+DB sides have already converged so login still works.
	if req.Password != nil && *req.Password != "" {
		// Re-authentication (GH #482): a non-admin owner changing their OWN
		// password must prove knowledge of the current one. Verified against the
		// stored bcrypt hash (kept in sync on every rotation). Admins are exempt
		// — they legitimately reset other accounts' passwords.
		if !claims.IsAdmin {
			if req.CurrentPassword == nil || *req.CurrentPassword == "" {
				c.JSON(http.StatusForbidden, gin.H{
					"error":  "current_password_required",
					"detail": "changing your password requires current_password",
				})
				return
			}
			if existing.PasswordHash == "" || !auth.VerifyPassword(existing.PasswordHash, *req.CurrentPassword) {
				c.JSON(http.StatusForbidden, gin.H{
					"error":  "current_password_invalid",
					"detail": "current_password does not match",
				})
				return
			}
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*req.Password), h.cfg.BcryptCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if existing.KratosIdentityID == nil || *existing.KratosIdentityID == "" {
			c.JSON(http.StatusConflict, gin.H{
				"error":  "no_kratos_identity",
				"detail": "user has no Kratos identity to update",
			})
			return
		}
		if h.cfg.KratosClient == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kratos_unavailable"})
			return
		}
		kctx, kcancel := context.WithTimeout(ctx, 10*time.Second)
		if err := h.cfg.KratosClient.SetPassword(kctx, *existing.KratosIdentityID, string(hash)); err != nil {
			kcancel()
			slog.Warn("kratos SetPassword failed", "user_id", id, "err", err)
			c.JSON(http.StatusBadGateway, gin.H{
				"error":  "kratos_error",
				"detail": err.Error(),
			})
			return
		}
		kcancel()
		if !existing.IsAdmin && existing.Username != nil && *existing.Username != "" && h.cfg.Agent != nil {
			actx, acancel := context.WithTimeout(ctx, 10*time.Second)
			_, agentErr := h.cfg.Agent.Call(actx, "user.password", map[string]any{
				"username": *existing.Username,
				"password": *req.Password,
			})
			acancel()
			if agentErr != nil {
				slog.Warn("agent user.password failed", "user_id", id, "err", agentErr)
				// JAB-114: the agent error is logged above, never echoed. This
				// path is reachable by a NON-ADMIN owner via PATCH /users/:id,
				// so a tenant changing their password while the agent is
				// failing would otherwise receive the root daemon's raw stderr
				// and filesystem paths. The sync flags below are the part the
				// client legitimately needs.
				c.JSON(http.StatusBadGateway, gin.H{
					"error":         "agent_error",
					"kratos_synced": true,
					"db_synced":     false,
					"os_synced":     false,
				})
				return
			}
		}
		existing.PasswordHash = string(hash)
		if err := h.cfg.Repo.Update(ctx, existing); err != nil {
			h.translateErr(c, err)
			return
		}
	}

	c.JSON(http.StatusOK, existing)
}

func (h *userHandler) delete(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	id := c.Param("id")

	// Self-delete lockout protection: the only way out would be through the
	// DB, which is worse than just refusing here.
	if id == claims.UserID {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot_delete_self"})
		return
	}

	// Last-admin lockout protection, same reasoning as demotion.
	target, err := h.cfg.Repo.FindByID(c.Request.Context(), id)
	if err != nil {
		h.translateErr(c, err)
		return
	}
	if target.IsAdmin {
		n, err := h.cfg.Repo.CountAdmins(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if n <= 1 {
			c.JSON(http.StatusConflict, gin.H{"error": "cannot_delete_last_admin"})
			return
		}
	}

	// Cascade-delete all domains owned by this user. DB first, then
	// out-of-band agent teardown via the reconciler. Best-effort: any
	// per-domain failure is logged, never fails the user delete.
	if h.cfg.Domains != nil {
		// Page through to avoid loading millions of rows in one shot.
		// Realistically a user has a handful of domains, but bound the
		// loop anyway.
		const batchSize = 500
		for {
			owned, _, err := h.cfg.Domains.ListByUserID(c.Request.Context(), id, repository.ListOptions{Limit: batchSize})
			if err != nil {
				slog.Warn("cascade delete: list user domains failed",
					"user_id", id, "err", err)
				break
			}
			if len(owned) == 0 {
				break
			}
			for i := range owned {
				d := &owned[i]
				name := d.Name
				// Purge EVERY Stalwart registry account under this domain
				// BEFORE the domain delete FK-cascades the panel mailbox
				// rows away (the domain must still be registered in
				// Stalwart for the purge to resolve it). jabali user
				// delete otherwise left the Stalwart account behind →
				// re-creating/re-migrating the same address collided with
				// {"type":"primaryKeyViolation","properties":["email"]}.
				//
				// Purge BY DOMAIN, not per panel mailbox row: a row-driven
				// loop skips orphans (a migration that pushed mail to
				// Stalwart without a matching row, or a failed prior
				// delete that removed the row but not the account), which
				// is exactly how the orphan kept surviving. A domain
				// belongs to one user, so destroying all its accounts on
				// that user's delete is correct. Best-effort: a failure
				// here logs + continues (orphan is recoverable; a blocked
				// user delete is worse).
				if h.cfg.Agent != nil {
					if delErr := userops.PurgeDomainMail(c.Request.Context(), h.cfg.Agent, name); delErr != nil {
						slog.Warn("cascade delete: stalwart domain purge failed",
							"user_id", id, "domain", name, "err", delErr)
					}
				}
				if err := h.cfg.Domains.Delete(c.Request.Context(), d.ID); err != nil {
					slog.Warn("cascade delete: domain DB delete failed",
						"user_id", id, "domain_id", d.ID, "domain", name, "err", err)
					continue
				}
				if h.cfg.Reconciler != nil {
					// Fire-and-forget — don't block the user delete on nginx
					// teardown. Use a fresh context because c.Request.Context
					// ends when the handler returns.
					name := name // capture
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						h.cfg.Reconciler.ReconcileDeleted(ctx, name)
					}()
				}
			}
			if len(owned) < batchSize {
				break
			}
		}
	}

	// Capture username BEFORE deleting so we can tear down the OS user
	// even after the DB row is gone. For admins, username is NULL.
	var username string
	if target.Username != nil {
		username = *target.Username
	}

	// Cascade-drop MariaDB schemas + grants on the data plane BEFORE the
	// panel row goes (which CASCADEs the metadata rows). Best-effort: any
	// per-DB failure is logged, never blocks the user delete. Operator
	// chose destructive — every panel-managed artefact must follow.
	if h.cfg.Databases != nil && h.cfg.Agent != nil && username != "" {
		const batchSize = 500
		for {
			dbs, _, dbErr := h.cfg.Databases.ListByUserID(c.Request.Context(), id, repository.ListOptions{Limit: batchSize})
			if dbErr != nil {
				slog.Warn("cascade delete: list user databases failed",
					"user_id", id, "err", dbErr)
				break
			}
			if len(dbs) == 0 {
				break
			}
			for i := range dbs {
				dbName := dbs[i].Name
				agentCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
				_, dropErr := h.cfg.Agent.Call(agentCtx, "db.drop", map[string]any{
					"db_name": dbName,
				})
				cancel()
				if dropErr != nil {
					slog.Warn("cascade delete: db.drop failed",
						"user_id", id, "db_name", dbName, "err", dropErr)
				}
			}
			if len(dbs) < batchSize {
				break
			}
		}
	}
	if h.cfg.DatabaseUsers != nil && h.cfg.Agent != nil && username != "" {
		const batchSize = 500
		for {
			dbus, _, duErr := h.cfg.DatabaseUsers.ListByUserID(c.Request.Context(), id, repository.ListOptions{Limit: batchSize})
			if duErr != nil {
				slog.Warn("cascade delete: list user database_users failed",
					"user_id", id, "err", duErr)
				break
			}
			if len(dbus) == 0 {
				break
			}
			for i := range dbus {
				duName := dbus[i].Username
				agentCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
				_, dropErr := h.cfg.Agent.Call(agentCtx, "db_user.drop", map[string]any{
					"db_user_name": duName,
				})
				cancel()
				if dropErr != nil {
					slog.Warn("cascade delete: db_user.drop failed",
						"user_id", id, "db_user_name", duName, "err", dropErr)
				}
			}
			if len(dbus) < batchSize {
				break
			}
		}
	}

	// Drop the per-user MariaDB shadow-admin account (<osuser>_mysqladmin).
	// db.mysqladmin.ensure provisions it for tenant DB management, but it is
	// NOT a database_users row, so the loop above never reaps it — leaving an
	// orphaned MySQL login with a valid password after the account is gone
	// (found live: ~20 <user>_mysqladmin accounts survived their deleted
	// owners). Construct the canonical name from the OS username rather than
	// the stored users.mysqladmin_username, which is null for accounts
	// provisioned outside the API (e.g. migrated users) whose shadow account
	// still exists. Reuse db_user.drop (idempotent DROP USER IF EXISTS), so a
	// user without a shadow account is a harmless no-op.
	if h.cfg.Agent != nil && username != "" {
		shadowUser := username + "_mysqladmin"
		agentCtx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		_, dropErr := h.cfg.Agent.Call(agentCtx, "db_user.drop", map[string]any{
			"db_user_name": shadowUser,
		})
		cancel()
		if dropErr != nil {
			slog.Warn("cascade delete: mysqladmin shadow drop failed",
				"user_id", id, "mysqladmin_user", shadowUser, "err", dropErr)
		}
	}

	// Cascade-tear-down the user's docker apps (M49, GH #170) BEFORE the panel
	// row CASCADEs the docker_apps metadata. The FK drops the rows, but the
	// CONTAINERS + data trees on the host outlive the DB without an explicit
	// agent teardown. Best-effort: a failure is logged, never blocks the delete.
	if h.cfg.DockerApps != nil && h.cfg.Agent != nil {
		apps, derr := h.cfg.DockerApps.ListByUserID(c.Request.Context(), id)
		if derr != nil {
			slog.Warn("cascade delete: list user docker apps failed", "user_id", id, "err", derr)
		}
		var teardownFailed []string
		for _, app := range apps {
			agentCtx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
			// purge_volumes=true so the agent rm -rf's the app data tree, not
			// just `compose down` — otherwise a deleted user's docker data is
			// orphaned under /var/lib/jabali/docker-apps (Gitea #523).
			_, delErr := h.cfg.Agent.Call(agentCtx, "docker_app.delete", map[string]any{"slug": app.EffectiveSlug(), "purge_volumes": true})
			cancel()
			if delErr != nil {
				slog.Warn("cascade delete: docker_app.delete failed",
					"user_id", id, "slug", app.EffectiveSlug(), "err", delErr)
				teardownFailed = append(teardownFailed, app.EffectiveSlug())
			}
		}
		// Gitea #532: if any Docker teardown failed, do NOT remove the owner — the
		// container/data may still exist on the host and would be left with no
		// owner row, quota accounting, suspension, or lifecycle controls. Block
		// the delete so the operator can resolve it (and retry).
		if len(teardownFailed) > 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error":  "docker_teardown_failed",
				"detail": "refusing to delete user: Docker app teardown failed for " + strings.Join(teardownFailed, ", ") + " — resolve on the host and retry",
			})
			return
		}
	}

	// Remove the Kratos identity so the username + email free up immediately.
	// Without this the identity is orphaned and recreating the same user
	// collides on the unique identifier ("username_taken" / kratos conflict) —
	// the user "still exists" even after delete (GH#132 follow-up, johnnyq).
	// Synchronous + before the DB delete so a 200 means the identity is gone
	// and recreate works right away. DeleteIdentity treats 404 as success.
	// Best-effort on a Kratos outage: log loudly rather than strand the DB
	// delete, but the common (Kratos-up) path now fully cleans up.
	if h.cfg.KratosClient != nil && target.KratosIdentityID != nil && *target.KratosIdentityID != "" {
		kctx, kcancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		if kerr := h.cfg.KratosClient.DeleteIdentity(kctx, *target.KratosIdentityID); kerr != nil {
			slog.Warn("user delete: kratos identity delete failed (orphan may block recreate)",
				"user_id", id, "identity_id", *target.KratosIdentityID, "err", kerr)
		}
		kcancel()
	}

	// Revoke the tenant's WordPress-cache Redis ACL user (GH #408 / ADR-0148).
	// The OS account + all its installs are being torn down, so wp_<username>
	// must not linger in the aclfile — a recycled username would otherwise
	// re-derive the same token and inherit the stale principal + keyspace.
	// Best-effort: a revoke failure is logged, never blocks the delete.
	if h.cfg.Redis != nil && username != "" {
		rctx, rcancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		if rErr := revokeAllUserCacheACLs(rctx, h.cfg.Redis, username); rErr != nil {
			slog.Warn("cascade delete: revoke tenant cache ACL failed",
				"user_id", id, "username", username, "err", rErr)
		}
		rcancel()
	}

	if err := h.cfg.Repo.Delete(c.Request.Context(), id); err != nil {
		h.translateErr(c, err)
		return
	}

	// Always-destructive OS teardown. The "delete user" operation in the
	// UI/CLI now removes EVERYTHING the user owns — domains (above),
	// MariaDB schemas + users (above), then the OS account + home dir
	// here. There is no "preserve tenant data" mode anymore; the operator
	// chose to delete and the cascade follows.
	if h.cfg.Agent != nil && username != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := h.cfg.Agent.Call(ctx, "user.delete", map[string]any{
				"username":    username,
				"remove_home": true,
			})
			if err != nil {
				slog.Warn("user agent teardown failed",
					"user_id", id, "username", username, "err", err)
			}
			// M33: re-evaluate maldet inotify watches after teardown.
			reloadMalwareMonitor(h.cfg.Agent)
		}()
	}

	slog.Info("audit",
		"event", "user_deleted",
		"actor_id", claims.UserID,
		"target_id", id,
		"target_email", target.Email)

	c.Status(http.StatusNoContent)
}

func (h *userHandler) reprovision(c *gin.Context) {
	var req reprovisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()
	id := c.Param("id")

	user, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		h.translateErr(c, err)
		return
	}

	// Admins are panel-only; reprovisioning them would create a stray
	// OS account that shouldn't exist.
	if user.IsAdmin {
		c.JSON(http.StatusBadRequest, gin.H{"error": "admin_has_no_os_account"})
		return
	}

	// Username should always be set for non-admin users.
	if user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cannot_derive_username"})
		return
	}
	username := *user.Username

	// This endpoint is deliberately agent-first + synchronous. Manual
	// recovery needs to tell the admin whether the OS side actually
	// converged — firing a goroutine and returning 200 would hide the
	// real failure. If the agent call fails, the DB is untouched.
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, agentErr := h.cfg.Agent.Call(agentCtx, "user.create", map[string]any{
		"username": username,
		"home_dir": "/home/" + username,
		"shell":    "/usr/local/bin/jabali-ssh-shell",
		"password": req.Password,
	})
	if agentErr != nil {
		// If the OS user already exists, steer the admin to the
		// password-sync path instead — useradd would just fail.
		var ae *agent.AgentError
		if errors.As(agentErr, &ae) && ae.Code == agent.CodeAlreadyExists {
			c.JSON(http.StatusConflict, gin.H{
				"error":  "os_user_exists",
				"detail": "OS user already exists — use PATCH /users/:id { password } to sync the password only",
			})
			return
		}
		slog.Warn("reprovision agent call failed",
			"user_id", id, "username", username, "err", agentErr)
		// Logged above; not echoed (JAB-114) — agent errors carry root-daemon
		// stderr and host paths.
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_error"})
		return
	}

	// Agent succeeded — update DB hash so the panel password matches.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), h.cfg.BcryptCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	user.PasswordHash = string(hash)
	if err := h.cfg.Repo.Update(ctx, user); err != nil {
		h.translateErr(c, err)
		return
	}
	// M33: re-evaluate maldet inotify watches (reprovision may have
	// recreated the home dir).
	go reloadMalwareMonitor(h.cfg.Agent)

	claims := ginctx.Claims(c)
	if claims != nil {
		slog.Info("audit",
			"event", "user_reprovisioned",
			"actor_id", claims.UserID,
			"target_id", id,
			"target_email", user.Email)
	}

	c.JSON(http.StatusOK, user)
}

// ---------- helpers ----------

// translateErr maps repository sentinels to HTTP responses. Keep the branches
// narrow — any unknown error is internal.
func (h *userHandler) translateErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	case errors.Is(err, repository.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "conflict"})
	default:
		slog.Warn("user handler internal error",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"err", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}

// parsePagination reads ?page=&page_size= with sane defaults. Negative or
// out-of-range values are clamped rather than rejected — the SPA can send
// whatever and still get data.
func parsePagination(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.Query("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(c.Query("page_size"))
	if pageSize < 1 {
		pageSize = defaultUsersPageSize
	}
	if pageSize > maxUsersPageSize {
		pageSize = maxUsersPageSize
	}
	return page, pageSize
}

var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// validUsername returns true if s matches the POSIX username regex:
// ^[a-z_][a-z0-9_-]{0,31}$
func validUsername(s string) bool {
	return usernameRe.MatchString(s)
}

// linuxUserFromEmail derives a Linux username from an email. Takes the
// part before '@'. Callers are expected to validate downstream (the
// agent's user.create enforces the POSIX regex).
func linuxUserFromEmail(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return ""
}

// reloadMalwareMonitor fires security.malware.monitor.reload after a
// user create/delete so LMD's inotify watches re-evaluate the
// /etc/passwd UID >= inotify_minuid set immediately. Best-effort:
// LMD's inotify_minutes=45 covers the case where this fails.
//
// Caller is expected to wrap in `go` so the user CRUD response does
// not block on systemctl. Cancel-safe via own short timeout.
func reloadMalwareMonitor(a agent.AgentInterface) {
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := a.Call(ctx, "security.malware.monitor.reload", map[string]any{}); err != nil {
		slog.Debug("malware monitor reload skipped", "err", err)
	}
}
