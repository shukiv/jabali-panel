package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ApplicationHandlerConfig bundles the repositories and services every
// per-app HTTP handler needs. M19 generalised this from the old
// WordPressHandlerConfig; M19.1 dropped the alias once every caller had
// switched.
type ApplicationHandlerConfig struct {
	ApplicationInstalls repository.ApplicationInstallRepository
	Databases           repository.DatabaseRepository
	DatabaseUsers       repository.DatabaseUserRepository
	DatabaseGrants      repository.DatabaseUserGrantRepository
	Domains             repository.DomainRepository
	Users               repository.UserRepository
	Packages            repository.PackageRepository
	Agent               agent.AgentInterface
	// Redis is the panel's jabali_panel-authenticated client (ADR-0148, #406).
	// Provisions the per-tenant wp_<osuser> ACL user that gates the WP object
	// cache. Nil when Redis isn't configured -> the cache switch returns 503.
	Redis *redis.Client
	// CacheTokenSecret derives the stable per-tenant Redis ACL token
	// (HMAC(secret, osuser)); read from JABALI_REDIS_PANEL_TOKEN.
	CacheTokenSecret string
	// CacheTokenSalts persists the per-tenant token salt (Gitea #415).
	CacheTokenSalts repository.CacheTokenSaltRepository
	// CacheWarmupRuns persists warmup run records (JAB-95 Phase 3). Optional;
	// nil disables run recording (warmup still runs).
	CacheWarmupRuns repository.CacheWarmupRunRepository
	// CacheDoctorRuns persists fleet cache-doctor/repair/refresh run records
	// (JAB-11). Optional; nil → the admin fleet endpoints return 503.
	CacheDoctorRuns repository.CacheDoctorRunRepository
	// Reconciler re-renders the domain vhost after the nginx page-cache flag
	// flips (mirrors DomainCacheHandlerConfig.Reconciler). Optional.
	Reconciler DNSScheduler
	// CronJobs lets app installers (ITFlow #206) create + tear down the
	// app-managed cron jobs an app needs. Optional; nil disables auto-cron.
	CronJobs repository.CronJobRepository
	// Apps is the M19 application registry. Nil-safe: the legacy
	// /wordpress-installs handlers in this file don't read it (they
	// hard-code the WordPress shape); only the new /applications
	// handlers in applications.go require it. app.NewWithDeps always
	// populates it for production wiring.
	Apps *apps.Registry
	// (PanelHost field removed in M22 rework — see ADR-0040. The new
	// sso-file design has no panel-side WordPress code calling back to
	// the panel, so the panel's public hostname doesn't need to be
	// threaded through to the agent at install time.)
}

// wordPressHandler keeps the original create/list/get/delete/clone/health
// methods that the M19 /applications surface delegates to (see
// RegisterApplicationRoutes in applications.go). The standalone
// /wordpress-installs route group was removed in M19.1; the only
// remaining surface is /applications.
type wordPressHandler struct{ cfg ApplicationHandlerConfig }

// ---- Request/Response types ----

type createWordPressRequest struct {
	DomainID      string `json:"domain_id" binding:"required"`
	SiteTitle     string `json:"site_title" binding:"required"`
	AdminUsername string `json:"admin_username" binding:"required"`
	AdminEmail    string `json:"admin_email" binding:"required"`
	AdminPassword string `json:"admin_password"`
	Locale        string `json:"locale"`
	UseWWW        bool   `json:"use_www"`
	Subdirectory  string `json:"subdirectory"`
}

type cloneWordPressRequest struct {
	DestDomainID string `json:"dest_domain_id" binding:"required"`
}

type createWordPressResponse struct {
	ID            string    `json:"id"`
	DomainID      string    `json:"domain_id"`
	DBID          string    `json:"db_id"`
	AdminUsername string    `json:"admin_username"`
	AdminPassword string    `json:"admin_password"`
	AdminEmail    string    `json:"admin_email"`
	UseWWW        bool      `json:"use_www"`
	Subdirectory  string    `json:"subdirectory"`
	Status        string    `json:"status"`
	Version       *string   `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type wordPressListResponse struct {
	ID string `json:"id"`
	// AppType is the M19 discriminator used by the UI's App column to
	// render the right icon + label. Pre-M19 rows in
	// application_installs default to "wordpress" via the column
	// default; the UI also falls back to "wordpress" when the field is
	// missing, so an older API build serving this struct without
	// AppType still renders sanely (just always as WordPress).
	AppType       string `json:"app_type"`
	DomainID      string `json:"domain_id"`
	DomainName    string `json:"domain_name"`
	DBID          string `json:"db_id"`
	AdminUsername string `json:"admin_username"`
	AdminEmail    string `json:"admin_email"`
	// Panel owner — the jabali user that installed this app. Distinct
	// from AdminEmail/AdminUsername (those identify the WordPress/Joomla
	// site admin inside the CMS). Admin list view surfaces this so an
	// operator can attribute installs to the hosting account, not the
	// CMS user.
	OwnerEmail    string    `json:"owner_email"`
	OwnerUsername string    `json:"owner_username,omitempty"`
	Locale        string    `json:"locale"`
	UseWWW        bool      `json:"use_www"`
	Subdirectory  string    `json:"subdirectory"`
	Status        string    `json:"status"`
	Version       *string   `json:"version"`
	LastError     string    `json:"last_error"`
	CacheEnabled  bool      `json:"cache_enabled"` // GH #406
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type healthResponse struct {
	WPInstalled bool   `json:"wp_installed"`
	WPVersion   string `json:"wp_version"`
	HTTPStatus  int    `json:"http_status"`
}

// subdirectoryRegex accepts a single path segment: starts with
// lowercase alnum, may contain lowercase alnum plus _ or -, max 64
// chars. No slashes, no dots, no uppercase — prevents traversal and
// keeps the docroot layout sane.
var subdirectoryRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Reserved subdirectory names would shadow WordPress core directories
// and make the install immediately broken.
var reservedSubdirectories = map[string]struct{}{
	"wp-admin":    {},
	"wp-includes": {},
	"wp-content":  {},
}

// validateSubdirectory returns nil for empty (subdirectory is optional).
// Non-empty input must match subdirectoryRegex and not be reserved.
func validateSubdirectory(s string) error {
	if s == "" {
		return nil
	}
	if !subdirectoryRegex.MatchString(s) {
		return fmt.Errorf("subdirectory must match ^[a-z0-9][a-z0-9_-]{0,63}$")
	}
	if _, reserved := reservedSubdirectories[strings.ToLower(s)]; reserved {
		return fmt.Errorf("subdirectory is reserved by WordPress core")
	}
	return nil
}

// ---- Handlers ----

func (h *wordPressHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createWordPressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Validate email
	if !isValidEmail(req.AdminEmail) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_email"})
		return
	}

	// Validate optional subdirectory
	if err := validateSubdirectory(req.Subdirectory); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_subdirectory", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()
	targetUserID := claims.UserID

	// Verify domain ownership (404 if not owner, 403 if cross-user)
	domain, err := h.cfg.Domains.FindByID(ctx, req.DomainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(ctx, "wordpress create: domain lookup failed", "err", err, "domain_id", req.DomainID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if domain.UserID != targetUserID {
		if claims.IsAdmin {
			// Admin can operate on any domain, but reject if different user owns it
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return
	}

	// Check for duplicate install at the same (domain, subdirectory). The
	// pair is unique on disk — same domain can host many installs but each
	// must live at a distinct subdirectory ("" = docroot). Was a per-domain
	// check; that blocked the obvious "main site at root + /blog" pattern.
	existing, err := h.cfg.ApplicationInstalls.FindByDomainAndSubdirectory(ctx, req.DomainID, req.Subdirectory)
	if err == nil && existing != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "install_exists"})
		return
	}
	if err != nil && !isNotFound(err) {
		slog.ErrorContext(ctx, "wordpress create: existing install lookup failed", "err", err, "domain_id", req.DomainID, "subdirectory", req.Subdirectory)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Resolve the domain owner's linux username. Required because the DB
	// name and DB user name are prefixed with it (panel-wide convention,
	// see databases.go), and the agent needs it for systemd-run targeting.
	var osUser string
	if u, uErr := h.cfg.Users.FindByID(ctx, targetUserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" {
		slog.ErrorContext(ctx, "wordpress create: user has no linux username", "user_id", targetUserID)
		c.JSON(http.StatusConflict, gin.H{"error": "user_not_provisioned"})
		return
	}

	// Generate admin password if not provided
	adminPassword := req.AdminPassword
	if adminPassword == "" {
		adminPassword = ids.NewSecret()
	}

	now := time.Now().UTC()

	// FQDN-based DB + user names (GH #196): <osuser>_wp_<fqdn>[_<subdir>]
	// {_db,_user}, so operators can tell at a glance which install owns a
	// database. Unique by construction ((domain, subdirectory) is unique);
	// a short suffix is appended only if truncation/sanitisation collides.
	dbID := ids.NewULID()
	dbName, dbUsername, nameErr := allocateAppDBNames(ctx, h.cfg.Databases, h.cfg.DatabaseUsers, targetUserID, osUser, "wordpress", domain.Name, req.Subdirectory)
	if nameErr != nil {
		slog.ErrorContext(ctx, "wordpress create: db name allocation failed", "err", nameErr)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	database := &models.Database{
		ID:        dbID,
		UserID:    targetUserID,
		Name:      dbName,
		Engine:    "mariadb",
		Charset:   "utf8mb4",
		Collation: "utf8mb4_unicode_ci",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.cfg.Databases.Create(ctx, database); err != nil {
		slog.ErrorContext(ctx, "wordpress create: database row create failed", "err", err, "db_id", dbID, "db_name", dbName)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Provision database user (username computed alongside dbName above).
	dbUserID := ids.NewULID()
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
	if err != nil {
		slog.ErrorContext(ctx, "wordpress create: bcrypt failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	databaseUser := &models.DatabaseUser{
		ID:           dbUserID,
		UserID:       targetUserID,
		Username:     dbUsername,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.cfg.DatabaseUsers.Create(ctx, databaseUser); err != nil {
		// Rollback database
		h.cfg.Databases.Delete(ctx, dbID)
		slog.ErrorContext(ctx, "wordpress create: database user create failed", "err", err, "db_user_id", dbUserID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Provision grant
	grantID := ids.NewULID()
	grant := &models.DatabaseUserGrant{
		ID:             grantID,
		DatabaseUserID: dbUserID,
		DatabaseID:     dbID,
		GrantLevel:     "rw",
		Privileges:     "ALL",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.cfg.DatabaseGrants.Create(ctx, grant); err != nil {
		// Rollback database and user
		h.cfg.DatabaseUsers.Delete(ctx, dbUserID)
		h.cfg.Databases.Delete(ctx, dbID)
		slog.ErrorContext(ctx, "wordpress create: grant create failed", "err", err, "grant_id", grantID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Provision the MariaDB side via the agent: CREATE DATABASE, CREATE
	// USER, GRANT. Mirrors what the /databases and /database-users API
	// paths do — previously the WP install kicked wordpress.install
	// without ever creating the real DB/user, so wp core install hit
	// "Error establishing a database connection".
	if h.cfg.Agent != nil {
		agentCtx, agentCancel := context.WithTimeout(ctx, 30*time.Second)
		defer agentCancel()

		rollbackPanelRows := func() {
			h.cfg.DatabaseGrants.Delete(ctx, grantID)
			h.cfg.DatabaseUsers.Delete(ctx, dbUserID)
			h.cfg.Databases.Delete(ctx, dbID)
		}

		if _, acErr := h.cfg.Agent.Call(agentCtx, "db.create", map[string]any{
			"db_name":   dbName,
			"charset":   "utf8mb4",
			"collation": "utf8mb4_unicode_ci",
		}); acErr != nil {
			rollbackPanelRows()
			slog.ErrorContext(ctx, "wordpress create: agent db.create", "err", acErr, "db_name", dbName)
			respondAgentErr(c, "agent_failed", acErr)
			return
		}

		if _, acErr := h.cfg.Agent.Call(agentCtx, "db_user.create", map[string]any{
			"db_user_name": dbUsername,
			"password":     adminPassword,
		}); acErr != nil {
			// Roll back the MariaDB db we just created.
			h.cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": dbName})
			rollbackPanelRows()
			slog.ErrorContext(ctx, "wordpress create: agent db_user.create", "err", acErr, "db_user", dbUsername)
			respondAgentErr(c, "agent_failed", acErr)
			return
		}

		if _, acErr := h.cfg.Agent.Call(agentCtx, "db_user.grant", map[string]any{
			"db_name":      dbName,
			"db_user_name": dbUsername,
			"grant_level":  "rw",
			"privileges":   []string{"ALL"},
		}); acErr != nil {
			h.cfg.Agent.Call(ctx, "db_user.drop", map[string]any{"db_user_name": dbUsername})
			h.cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": dbName})
			rollbackPanelRows()
			slog.ErrorContext(ctx, "wordpress create: agent db_user.grant", "err", acErr)
			respondAgentErr(c, "agent_failed", acErr)
			return
		}
	}

	// Create WordPress install record with status='pending'
	installID := ids.NewULID()
	install := &models.WordPressInstall{
		ID:            installID,
		UserID:        targetUserID,
		DomainID:      req.DomainID,
		DBID:          models.DBIDPtr(dbID),
		AdminUsername: req.AdminUsername,
		AdminEmail:    req.AdminEmail,
		Locale:        req.Locale,
		UseWWW:        req.UseWWW,
		Subdirectory:  req.Subdirectory,
		Status:        "pending",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := h.cfg.ApplicationInstalls.Create(ctx, install); err != nil {
		// Rollback all
		h.cfg.DatabaseGrants.Delete(ctx, grantID)
		h.cfg.DatabaseUsers.Delete(ctx, dbUserID)
		h.cfg.Databases.Delete(ctx, dbID)
		slog.ErrorContext(ctx, "wordpress create: install row create failed", "err", err, "install_id", installID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Compute site URL from (domain, use_www, subdirectory).
	siteURL := buildSiteURL(domain.Name, req.UseWWW, req.Subdirectory)

	// Spawn async goroutine to install WordPress.
	kickArgs := installKickArgs{
		InstallID:     installID,
		OSUser:        osUser,
		DocRoot:       domain.DocRoot,
		DBName:        dbName,
		DBUser:        dbUsername,
		DBPassword:    adminPassword,
		SiteURL:       siteURL,
		SiteTitle:     req.SiteTitle,
		AdminUsername: req.AdminUsername,
		AdminPassword: adminPassword,
		AdminEmail:    req.AdminEmail,
		Locale:        req.Locale,
		Subdirectory:  req.Subdirectory,
		UseWWW:        req.UseWWW,
	}
	go createInstallAndKickAgent(ctx, kickArgs, h.cfg)

	// Return 202 Accepted with plaintext password
	resp := createWordPressResponse{
		ID:            installID,
		DomainID:      req.DomainID,
		DBID:          dbID,
		AdminUsername: req.AdminUsername,
		AdminEmail:    req.AdminEmail,
		AdminPassword: adminPassword,
		UseWWW:        req.UseWWW,
		Subdirectory:  req.Subdirectory,
		Status:        "pending",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	c.JSON(http.StatusAccepted, resp)
}

func (h *wordPressHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	page, pageSize, opts := parseListOptions(c, 10, 100)

	ctx := c.Request.Context()
	var installs []models.WordPressInstall
	var total int64
	var err error

	// Admins see all; users see only their own
	if claims.IsAdmin {
		installs, total, err = h.cfg.ApplicationInstalls.List(ctx, opts)
	} else {
		installs, total, err = h.cfg.ApplicationInstalls.ListByUserID(ctx, claims.UserID, opts)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if installs == nil {
		installs = []models.WordPressInstall{}
	}

	// Batch-lookup domain names so the UI can render them without an
	// N+1 follow-up per row. A user rarely has many installs, and
	// admin "list all" is bounded by pageSize.
	domainNames := make(map[string]string, len(installs))
	for _, inst := range installs {
		if _, ok := domainNames[inst.DomainID]; ok {
			continue
		}
		if d, err := h.cfg.Domains.FindByID(ctx, inst.DomainID); err == nil && d != nil {
			domainNames[inst.DomainID] = d.Name
		}
	}

	// Batch-lookup the panel owner for each install so the admin list
	// can attribute rows to the jabali user, not the CMS admin. Single
	// FindByIDs avoids N+1 fetches; missing users (rare — referential
	// integrity should prevent it) just leave the owner fields blank.
	ownerEmails := make(map[string]string, len(installs))
	ownerUsernames := make(map[string]string, len(installs))
	if h.cfg.Users != nil {
		ownerIDSet := make(map[string]struct{}, len(installs))
		for _, inst := range installs {
			if inst.UserID != "" {
				ownerIDSet[inst.UserID] = struct{}{}
			}
		}
		ownerIDs := make([]string, 0, len(ownerIDSet))
		for id := range ownerIDSet {
			ownerIDs = append(ownerIDs, id)
		}
		if len(ownerIDs) > 0 {
			owners, oErr := h.cfg.Users.FindByIDs(ctx, ownerIDs)
			if oErr == nil {
				for _, u := range owners {
					ownerEmails[u.ID] = u.Email
					if u.Username != nil {
						ownerUsernames[u.ID] = *u.Username
					}
				}
			}
		}
	}

	out := make([]wordPressListResponse, len(installs))
	for i, inst := range installs {
		appType := inst.AppType
		if appType == "" {
			appType = "wordpress" // pre-M19 row safety net
		}
		out[i] = wordPressListResponse{
			ID:            inst.ID,
			AppType:       appType,
			DomainID:      inst.DomainID,
			DomainName:    domainNames[inst.DomainID],
			DBID:          inst.DBIDOr(),
			AdminUsername: inst.AdminUsername,
			AdminEmail:    inst.AdminEmail,
			OwnerEmail:    ownerEmails[inst.UserID],
			OwnerUsername: ownerUsernames[inst.UserID],
			Locale:        inst.Locale,
			UseWWW:        inst.UseWWW,
			Subdirectory:  inst.Subdirectory,
			Status:        inst.Status,
			Version:       inst.Version,
			LastError:     inst.LastError,
			CacheEnabled:  inst.CacheEnabled,
			CreatedAt:     inst.CreatedAt,
			UpdatedAt:     inst.UpdatedAt,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *wordPressHandler) get(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	installID := c.Param("id")
	ctx := c.Request.Context()

	// Non-admins: check ownership (returns 404 if not owner, not 403)
	var install *models.WordPressInstall
	var err error

	if claims.IsAdmin {
		install, err = h.cfg.ApplicationInstalls.FindByID(ctx, installID)
	} else {
		install, err = h.cfg.ApplicationInstalls.FindByIDAndUserID(ctx, installID, claims.UserID)
	}

	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "install_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Look up the domain name for consistency with the list response;
	// the UI uses the same row shape for both list and detail.
	var domainName string
	if d, err := h.cfg.Domains.FindByID(ctx, install.DomainID); err == nil && d != nil {
		domainName = d.Name
	}

	getAppType := install.AppType
	if getAppType == "" {
		getAppType = "wordpress"
	}
	resp := wordPressListResponse{
		ID:            install.ID,
		AppType:       getAppType,
		DomainID:      install.DomainID,
		DomainName:    domainName,
		DBID:          install.DBIDOr(),
		AdminUsername: install.AdminUsername,
		AdminEmail:    install.AdminEmail,
		Locale:        install.Locale,
		UseWWW:        install.UseWWW,
		Subdirectory:  install.Subdirectory,
		Status:        install.Status,
		Version:       install.Version,
		LastError:     install.LastError,
		CreatedAt:     install.CreatedAt,
		UpdatedAt:     install.UpdatedAt,
	}
	c.JSON(http.StatusOK, resp)
}

func (h *wordPressHandler) delete(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	installID := c.Param("id")
	ctx := c.Request.Context()

	// Ownership check
	var install *models.WordPressInstall
	var err error

	if claims.IsAdmin {
		install, err = h.cfg.ApplicationInstalls.FindByID(ctx, installID)
	} else {
		install, err = h.cfg.ApplicationInstalls.FindByIDAndUserID(ctx, installID, claims.UserID)
	}

	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "install_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Mark as deleting
	if err := h.cfg.ApplicationInstalls.UpdateStatus(ctx, installID, "deleting", nil, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Resolve os_user + docroot so the agent knows what to clean up.
	domain, err := h.cfg.Domains.FindByID(ctx, install.DomainID)
	if err != nil {
		slog.ErrorContext(ctx, "wordpress delete: domain lookup", "err", err, "domain_id", install.DomainID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	var osUser string
	if u, uErr := h.cfg.Users.FindByID(ctx, install.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" {
		slog.ErrorContext(ctx, "wordpress delete: user has no linux username", "user_id", install.UserID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	dbUserID := ""
	var dbUserUsername string
	if grants, gerr := h.cfg.DatabaseGrants.ListByDatabaseID(ctx, install.DBIDOr()); gerr == nil && len(grants) > 0 {
		dbUserID = grants[0].DatabaseUserID
		if dbu, duErr := h.cfg.DatabaseUsers.FindByID(ctx, dbUserID); duErr == nil && dbu != nil {
			dbUserUsername = dbu.Username
		}
	}

	// Spawn async goroutine to delete. Pass the AppType + Subdirectory
	// from the install row so the kicker dispatches to the right per-
	// app deleter (was hardcoded to "wordpress" pre-M19, which silently
	// routed Drupal/Joomla/etc deletes to the WP file-list cleaner).
	// install_id is plumbed through so deleters that opt into the
	// managed-data-dir contract (Moodle/Chamilo) can recompute the
	// /home/<user>/<install_id>-data path and rm it.
	go createDeleteAndKickAgent(ctx, installID, install.UserID, install.AppType, install.Subdirectory, install.DBIDOr(), dbUserID, osUser, domain.DocRoot, domain.Name, dbUserUsername, h.cfg)

	c.JSON(http.StatusAccepted, gin.H{"status": "deleting"})
}

// clone sentinel errors map cloneCore's failure modes to the exact HTTP
// statuses/codes the UI depends on (GH #556 extraction — keep in sync with the
// switch in clone() and TestApplicationsClone_Characterization).
var (
	errCloneSourceNotFound     = errors.New("source_install_not_found")
	errCloneDomainNotFound     = errors.New("domain_not_found")
	errCloneForbidden          = errors.New("forbidden")
	errCloneInstallExists      = errors.New("install_exists")
	errCloneUserNotProvisioned = errors.New("user_not_provisioned")
)

// cloneAgentError wraps an agent-side failure detail so the HTTP wrapper can
// render 502 agent_failed + the underlying detail (the web contract).
type cloneAgentError struct{ detail string }

func (e *cloneAgentError) Error() string { return "agent_failed: " + e.detail }

// clone provisions a copy of a WordPress install onto another domain the caller
// owns. HTTP wrapper: parse request, delegate to cloneCore, map errors → status.
func (h *wordPressHandler) clone(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req cloneWordPressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	resp, err := h.cloneCore(c.Request.Context(), c.Param("id"), req.DestDomainID, claims.IsAdmin, claims.UserID, true)
	var agentErr *cloneAgentError
	switch {
	case err == nil:
		c.JSON(http.StatusAccepted, resp)
	case errors.Is(err, errCloneSourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "source_install_not_found"})
	case errors.Is(err, errCloneDomainNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
	case errors.Is(err, errCloneForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	case errors.Is(err, errCloneInstallExists):
		c.JSON(http.StatusConflict, gin.H{"error": "install_exists"})
	case errors.Is(err, errCloneUserNotProvisioned):
		c.JSON(http.StatusConflict, gin.H{"error": "user_not_provisioned"})
	case errors.As(err, &agentErr):
		respondAgentErr(c, "agent_failed", agentErr)
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}

// CloneApplication clones an install from a non-HTTP caller (CLI, GH #556). It
// runs the SAME core as the web POST /applications/:id/clone path. actorUserID
// scopes ownership exactly as the HTTP handler; async=false blocks until the
// agent clone finishes (the CLI process must stay alive), async=true detaches
// the kick like the web path. Returns the errClone* sentinels / *cloneAgentError
// so callers can message precisely.
func CloneApplication(ctx context.Context, cfg ApplicationHandlerConfig, sourceInstallID, destDomainID string, isAdmin bool, actorUserID string, async bool) (*createWordPressResponse, error) {
	return (&wordPressHandler{cfg: cfg}).cloneCore(ctx, sourceInstallID, destDomainID, isAdmin, actorUserID, async)
}

// cloneCore is the transport-agnostic core of the clone flow. It returns a
// sentinel error (errClone*) or *cloneAgentError for the caller to map to a
// status/message; any other (wrapped) error is an internal failure (→ 500).
// Behavior is locked by TestApplicationsClone_Characterization.
func (h *wordPressHandler) cloneCore(ctx context.Context, sourceInstallID, destDomainID string, isAdmin bool, actorUserID string, async bool) (*createWordPressResponse, error) {
	targetUserID := actorUserID

	// Get source install
	var sourceInstall *models.WordPressInstall
	var err error
	if isAdmin {
		sourceInstall, err = h.cfg.ApplicationInstalls.FindByID(ctx, sourceInstallID)
	} else {
		sourceInstall, err = h.cfg.ApplicationInstalls.FindByIDAndUserID(ctx, sourceInstallID, targetUserID)
	}
	if err != nil {
		if isNotFound(err) {
			return nil, errCloneSourceNotFound
		}
		return nil, fmt.Errorf("source install lookup: %w", err)
	}

	// Verify destination domain ownership (403 if cross-user)
	destDomain, err := h.cfg.Domains.FindByID(ctx, destDomainID)
	if err != nil {
		if isNotFound(err) {
			return nil, errCloneDomainNotFound
		}
		return nil, fmt.Errorf("dest domain lookup: %w", err)
	}
	if destDomain.UserID != targetUserID {
		return nil, errCloneForbidden
	}

	// Check for existing install at the same (dest_domain, source_subdir).
	// Clone preserves the source install's subdirectory, so collision only
	// happens if the destination already hosts an install at that same
	// subdir — sibling installs at other subdirs are fine.
	existing, err := h.cfg.ApplicationInstalls.FindByDomainAndSubdirectory(ctx, destDomainID, sourceInstall.Subdirectory)
	if err == nil && existing != nil {
		return nil, errCloneInstallExists
	}
	if err != nil && !isNotFound(err) {
		return nil, fmt.Errorf("dest collision check: %w", err)
	}

	now := time.Now().UTC()

	// Resolve the domain owner's linux username — same prefix convention
	// as the install path uses. Required for DB naming and future systemd-run
	// targeting.
	var osUser string
	if u, uErr := h.cfg.Users.FindByID(ctx, targetUserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" {
		slog.ErrorContext(ctx, "wordpress clone: user has no linux username", "user_id", targetUserID)
		return nil, errCloneUserNotProvisioned
	}

	// Provision destination database
	destDBID := ids.NewULID()
	destDBSuffix := strings.ToLower(destDBID[len(destDBID)-6:])
	destDBName := osUser + "_wp_" + destDBSuffix
	destDatabase := &models.Database{
		ID:        destDBID,
		UserID:    targetUserID,
		Name:      destDBName,
		Engine:    "mariadb",
		Charset:   "utf8mb4",
		Collation: "utf8mb4_unicode_ci",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.cfg.Databases.Create(ctx, destDatabase); err != nil {
		return nil, fmt.Errorf("create dest database: %w", err)
	}

	// Provision destination database user
	destDBUserID := ids.NewULID()
	destDBUserSuffix := strings.ToLower(destDBUserID[len(destDBUserID)-6:])
	destDBUsername := osUser + "_wp_" + destDBUserSuffix
	plainPassword := ids.NewSecret()
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
	if err != nil {
		h.cfg.Databases.Delete(ctx, destDBID)
		return nil, fmt.Errorf("hash dest db password: %w", err)
	}
	destDatabaseUser := &models.DatabaseUser{
		ID:           destDBUserID,
		UserID:       targetUserID,
		Username:     destDBUsername,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.cfg.DatabaseUsers.Create(ctx, destDatabaseUser); err != nil {
		h.cfg.Databases.Delete(ctx, destDBID)
		return nil, fmt.Errorf("create dest db user: %w", err)
	}

	// Provision grant
	destGrantID := ids.NewULID()
	destGrant := &models.DatabaseUserGrant{
		ID:             destGrantID,
		DatabaseUserID: destDBUserID,
		DatabaseID:     destDBID,
		GrantLevel:     "rw",
		Privileges:     "ALL",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.cfg.DatabaseGrants.Create(ctx, destGrant); err != nil {
		h.cfg.DatabaseUsers.Delete(ctx, destDBUserID)
		h.cfg.Databases.Delete(ctx, destDBID)
		return nil, fmt.Errorf("create dest grant: %w", err)
	}

	// Provision the MariaDB side via the agent: CREATE DATABASE, CREATE
	// USER, GRANT — same pattern as the install handler (ba17cd7). Without
	// this, the clone lands a panel row that points at a non-existent
	// MariaDB database and wp core install/clone bombs with
	// "Error establishing a database connection".
	if h.cfg.Agent != nil {
		agentCtx, agentCancel := context.WithTimeout(ctx, 30*time.Second)
		defer agentCancel()

		rollbackPanelRows := func() {
			h.cfg.DatabaseGrants.Delete(ctx, destGrantID)
			h.cfg.DatabaseUsers.Delete(ctx, destDBUserID)
			h.cfg.Databases.Delete(ctx, destDBID)
		}

		if _, acErr := h.cfg.Agent.Call(agentCtx, "db.create", map[string]any{
			"db_name":   destDBName,
			"charset":   "utf8mb4",
			"collation": "utf8mb4_unicode_ci",
		}); acErr != nil {
			rollbackPanelRows()
			slog.ErrorContext(ctx, "wordpress clone: agent db.create", "err", acErr, "db_name", destDBName)
			return nil, &cloneAgentError{detail: acErr.Error()}
		}

		if _, acErr := h.cfg.Agent.Call(agentCtx, "db_user.create", map[string]any{
			"db_user_name": destDBUsername,
			"password":     plainPassword,
		}); acErr != nil {
			h.cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": destDBName})
			rollbackPanelRows()
			slog.ErrorContext(ctx, "wordpress clone: agent db_user.create", "err", acErr, "db_user", destDBUsername)
			return nil, &cloneAgentError{detail: acErr.Error()}
		}

		if _, acErr := h.cfg.Agent.Call(agentCtx, "db_user.grant", map[string]any{
			"db_name":      destDBName,
			"db_user_name": destDBUsername,
			"grant_level":  "rw",
			"privileges":   []string{"ALL"},
		}); acErr != nil {
			h.cfg.Agent.Call(ctx, "db_user.drop", map[string]any{"db_user_name": destDBUsername})
			h.cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": destDBName})
			rollbackPanelRows()
			slog.ErrorContext(ctx, "wordpress clone: agent db_user.grant", "err", acErr)
			return nil, &cloneAgentError{detail: acErr.Error()}
		}
	}

	// Create clone install record
	cloneInstallID := ids.NewULID()
	cloneInstall := &models.WordPressInstall{
		ID:            cloneInstallID,
		UserID:        targetUserID,
		DomainID:      destDomainID,
		DBID:          models.DBIDPtr(destDBID),
		AdminUsername: sourceInstall.AdminUsername,
		AdminEmail:    sourceInstall.AdminEmail,
		Locale:        sourceInstall.Locale,
		UseWWW:        sourceInstall.UseWWW,
		Subdirectory:  sourceInstall.Subdirectory,
		Status:        "cloning",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := h.cfg.ApplicationInstalls.Create(ctx, cloneInstall); err != nil {
		h.cfg.DatabaseGrants.Delete(ctx, destGrantID)
		h.cfg.DatabaseUsers.Delete(ctx, destDBUserID)
		h.cfg.Databases.Delete(ctx, destDBID)
		return nil, fmt.Errorf("create clone install: %w", err)
	}

	// Kick the file/DB copy. The web path detaches (async); the CLI blocks
	// (async=false) so the process stays alive until the agent finishes.
	// createCloneAndKickAgent already reparents to context.Background()+5min,
	// so blocking here is safe and the web goroutine still survives.
	if async {
		go createCloneAndKickAgent(ctx, cloneInstallID, sourceInstall.DomainID, destDomainID, destDBID, sourceInstall.Subdirectory, sourceInstall.UseWWW, h.cfg)
	} else {
		createCloneAndKickAgent(ctx, cloneInstallID, sourceInstall.DomainID, destDomainID, destDBID, sourceInstall.Subdirectory, sourceInstall.UseWWW, h.cfg)
	}

	return &createWordPressResponse{
		ID:            cloneInstallID,
		DomainID:      destDomainID,
		DBID:          destDBID,
		AdminUsername: sourceInstall.AdminUsername,
		AdminEmail:    sourceInstall.AdminEmail,
		UseWWW:        sourceInstall.UseWWW,
		Subdirectory:  sourceInstall.Subdirectory,
		Status:        "cloning",
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (h *wordPressHandler) health(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	installID := c.Param("id")
	ctx := c.Request.Context()

	// Ownership check
	var err error

	if claims.IsAdmin {
		_, err = h.cfg.ApplicationInstalls.FindByID(ctx, installID)
	} else {
		_, err = h.cfg.ApplicationInstalls.FindByIDAndUserID(ctx, installID, claims.UserID)
	}

	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "install_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Probe wp-includes/version.php existence via the agent's
	// privileged fs.stat. Adds a quick sanity hit to the handler;
	// downstream callers (UI status pill, monitoring scripts) get a
	// real installed/missing read instead of the previous always-
	// false stub.
	resp := healthResponse{}
	install, ferr := h.cfg.ApplicationInstalls.FindByID(ctx, installID)
	if ferr == nil && install != nil && h.cfg.Agent != nil {
		if dom, derr := h.cfg.Domains.FindByID(ctx, install.DomainID); derr == nil && dom != nil && dom.DocRoot != "" {
			subdir := install.Subdirectory
			if subdir != "" && subdir[0] != '/' {
				subdir = "/" + subdir
			}
			probePath := dom.DocRoot + subdir + "/wp-includes/version.php"
			statCtx, statCancel := context.WithTimeout(ctx, 5*time.Second)
			defer statCancel()
			raw, sErr := h.cfg.Agent.Call(statCtx, "fs.stat", map[string]any{"path": probePath})
			if sErr == nil {
				var stat struct {
					Exists bool `json:"exists"`
				}
				if json.Unmarshal(raw, &stat) == nil {
					resp.WPInstalled = stat.Exists
				}
			}
		}
	}
	c.JSON(http.StatusOK, resp)
}

// ---- Async goroutines ----

// createInstallAndKickAgent installs WordPress asynchronously.
// Uses independent context with 5-minute timeout to ensure agent call completes
// even if the original request context is cancelled.
// If panel crashes while installing, the row stays in 'installing' state
// until the reconciler timeout (typically 1 hour) sweeps it as failed.
// installKickArgs bundles everything createInstallAndKickAgent needs.
// It exists because the agent contract has many required fields and we
// do not want a 14-arg function signature.
type installKickArgs struct {
	UserID        string
	InstallID     string
	OSUser        string
	DocRoot       string
	DBName        string
	DBUser        string
	DBPassword    string
	SiteURL       string
	SiteTitle     string
	AdminUsername string
	AdminPassword string
	AdminEmail    string
	Locale        string
	Subdirectory  string
	UseWWW        bool
}

// buildSiteURL composes the canonical WordPress siteurl/home value.
// Matches the rule used by the agent when use_www is true / subdirectory
// is set.
func buildSiteURL(domain string, useWWW bool, subdirectory string) string {
	host := domain
	if useWWW {
		host = "www." + domain
	}
	u := "https://" + host
	if subdirectory != "" {
		u += "/" + subdirectory
	}
	return u
}

func createInstallAndKickAgent(parentCtx context.Context, args installKickArgs, cfg ApplicationHandlerConfig) {
	// Use independent context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Update status to 'installing'
	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		// Log but don't fail — status was already 'pending'
		return
	}

	// Call agent to install WordPress
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	// M19: dispatched through app.install with app_type discriminator.
	// The agent's app dispatcher forwards the body unchanged to the
	// registered "wordpress" installer (see panel-agent/internal/commands/
	// app_dispatch.go). Legacy "wordpress.install" still works on the
	// agent for any straggler caller through M19.1.
	payload := map[string]any{
		"app_type":     "wordpress",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"site_url":     args.SiteURL,
		"site_title":   args.SiteTitle,
		"admin_user":   args.AdminUsername,
		"admin_pass":   args.AdminPassword,
		"admin_email":  args.AdminEmail,
		"locale":       args.Locale,
		"subdirectory": args.Subdirectory,
		"use_www":      args.UseWWW,
	}
	// (M22 magic-link mu-plugin payload plumbing removed in the M22
	// rework — see ADR-0040. SSO files are minted on demand via
	// wordpress.create_sso_file, not threaded through wordpress.install.)

	agentResp, err := cfg.Agent.Call(ctx, "app.install", payload)
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	// Parse version from response
	var respMap map[string]any
	if err := json.Unmarshal(agentResp, &respMap); err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to parse agent response: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	version := ""
	if v, ok := respMap["version"].(string); ok {
		version = v
	}

	// Update status to 'ready' with version
	createAppCrons(ctx, cfg, args.UserID, "wordpress", appInstallPath(args.DocRoot, args.Subdirectory))

	// #597: default the Redis object cache ON for new WordPress installs — the
	// direct cut to the DB-bound render (heavy plugins hit the DB on every
	// uncached request). Done BEFORE the "ready" flip so a --wait CLI caller
	// (short-lived process) doesn't exit and kill this goroutine mid-provision.
	// Best-effort: no Redis/secret (errObjectCacheUnavailable) or any error must
	// NOT fail the install; the per-app Caching toggle stays the override.
	// Reuses the same ACL/token/cache_set path as the toggle.
	if inst, ferr := cfg.ApplicationInstalls.FindByID(ctx, args.InstallID); ferr == nil && inst != nil {
		if cErr := (&wordPressHandler{cfg: cfg}).enableObjectCache(ctx, inst); cErr != nil && !errors.Is(cErr, errObjectCacheUnavailable) {
			slog.WarnContext(ctx, "wordpress: default object-cache enable failed (install still ready)", "err", cErr, "install_id", args.InstallID)
		}
	}

	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// createDeleteAndKickAgent removes the on-disk WordPress files via the
// agent and cleans up every DB row tied to the install (database,
// database user, grants, install record). If the agent file-removal
// fails we flip status to failed but still allow a future retry.
// Non-empty osUser+docroot are required; the handler pre-fills them.
func createDeleteAndKickAgent(parentCtx context.Context, installID, userID, appType, subdirectory, databaseID, dbUserID, osUser, docroot, domainName, dbUserUsername string, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, installID, "failed", &errMsg, nil)
		return
	}

	// Default appType to "wordpress" for any install row that pre-dates
	// the M19 migration (the column NOT NULL DEFAULT 'wordpress' should
	// have backfilled, but treat empty defensively).
	if appType == "" {
		appType = "wordpress"
	}

	// Agent removes the app's files from the docroot AND restores
	// the domain.create placeholder index.html (when domain is non-empty)
	// so the docroot doesn't 403 after delete. Does NOT touch the MySQL
	// side — panel handles that below.
	//
	// M19: dispatched through app.delete with app_type discriminator
	// (was hardcoded "wordpress" before the rename).
	// install_id + subdirectory let deleters that opted into the
	// managed-data-dir contract recompute /home/<user>/<install_id>-data
	// for cleanup, and let subdir installs target the right path.
	_, err := cfg.Agent.Call(ctx, "app.delete", map[string]any{
		"app_type":     appType,
		"install_id":   installID,
		"os_user":      osUser,
		"docroot":      docroot,
		"subdirectory": subdirectory,
		"domain":       domainName,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent delete failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, installID, "failed", &errMsg, nil)
		return
	}

	// Apps that auto-create cron jobs at install — tear them down now
	// (crons aren't FK-linked to the install).
	switch appType {
	case "itflow":
		removeITFlowCrons(ctx, cfg, userID, osUser, appInstallPath(docroot, subdirectory))
	case "wordpress", "joomla", "mediawiki":
		removeAppCrons(ctx, cfg, userID, osUser, appType, appInstallPath(docroot, subdirectory))
	}

	// Best-effort DB-side cleanup. Drop the mariadb database + user on the
	// host via the agent and remove the panel-side rows so the slot is
	// freed up. Order matters:
	//   grants → users → wp install row → database
	// because fk_wpinstalls_db is RESTRICT — deleting the databases row
	// before the wordpress_installs row that references it fails (silently,
	// since the error is swallowed) and leaves an orphan databases row.
	if dbUserID != "" {
		if grants, gErr := cfg.DatabaseGrants.ListByDatabaseUserID(ctx, dbUserID); gErr == nil {
			for _, g := range grants {
				cfg.DatabaseGrants.Delete(ctx, g.ID)
			}
		}
		// Drop the mariadb user on the host. The agent command is
		// `db_user.drop` with `db_user_name` — NOT `mysql.user.delete`
		// (which doesn't exist; the prior call silently no-op'd and the
		// account survived every WP delete).
		if dbUserUsername != "" {
			if _, agentErr := cfg.Agent.Call(ctx, "db_user.drop", map[string]any{"db_user_name": dbUserUsername}); agentErr != nil {
				slog.WarnContext(ctx, "wordpress delete: db_user.drop failed", "err", agentErr, "db_user", dbUserUsername)
			}
		}
		cfg.DatabaseUsers.Delete(ctx, dbUserID)
	}
	// Delete the WP install row BEFORE the database panel row so the
	// fk_wpinstalls_db RESTRICT constraint releases.
	cfg.ApplicationInstalls.Delete(ctx, installID)
	if databaseID != "" {
		if db, dbErr := cfg.Databases.FindByID(ctx, databaseID); dbErr == nil && db != nil {
			// Agent command is `db.drop` with `db_name` — NOT
			// `mysql.database.delete` (same silent-no-op bug as above,
			// the schema survived every delete).
			if _, agentErr := cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": db.Name}); agentErr != nil {
				slog.WarnContext(ctx, "wordpress delete: db.drop failed", "err", agentErr, "db_name", db.Name)
			}
		}
		cfg.Databases.Delete(ctx, databaseID)
	}
}

// createCloneAndKickAgent clones WordPress asynchronously.
func createCloneAndKickAgent(parentCtx context.Context, cloneInstallID, sourceDomainID, destDomainID, destDatabaseID, dstSubdirectory string, useWWW bool, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Status writes must survive the operation deadline. If the agent clone
	// hangs to ctx's timeout (GH #599), a terminal UpdateStatus on that same
	// expired ctx is a silent no-op and the row stays "cloning" forever. Give
	// every status write its own fresh, short-lived context.
	setStatus := func(status string, errMsg, version *string) {
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scancel()
		cfg.ApplicationInstalls.UpdateStatus(sctx, cloneInstallID, status, errMsg, version)
	}

	if cfg.Agent == nil {
		errMsg := "agent not configured"
		setStatus("failed", &errMsg, nil)
		return
	}

	// Look up source domain to get docroot and site URL
	sourceDomain, err := cfg.Domains.FindByID(ctx, sourceDomainID)
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to find source domain: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Look up destination domain to get docroot and site URL
	destDomain, err := cfg.Domains.FindByID(ctx, destDomainID)
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to find destination domain: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Look up destination database details
	destDatabase, err := cfg.Databases.FindByID(ctx, destDatabaseID)
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to find destination database: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Look up database grants for the destination database to find the associated user
	destGrants, err := cfg.DatabaseGrants.ListByDatabaseID(ctx, destDatabaseID)
	if err != nil || len(destGrants) == 0 {
		errMsg := truncateError(fmt.Sprintf("failed to find database grants for dest database: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Get the database user from the first grant
	destDatabaseUser, err := cfg.DatabaseUsers.FindByID(ctx, destGrants[0].DatabaseUserID)
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to find database user: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Look up the user to get OS username
	user, err := cfg.Users.FindByID(ctx, destDomain.UserID)
	if err != nil || user.Username == nil {
		errMsg := truncateError(fmt.Sprintf("failed to find user or missing username: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}
	osUser := *user.Username

	// Look up source install to get database name
	sourceInstall, err := cfg.ApplicationInstalls.FindByDomainAndSubdirectory(ctx, sourceDomainID, dstSubdirectory)
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to find source install: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Look up source database
	var srcDBName string
	if sourceInstall.DBID != nil {
		sourceDatabase, err := cfg.Databases.FindByID(ctx, *sourceInstall.DBID)
		if err != nil {
			errMsg := truncateError(fmt.Sprintf("failed to find source database: %v", err), 1024)
			setStatus("failed", &errMsg, nil)
			return
		}
		srcDBName = sourceDatabase.Name
	} else {
		errMsg := "source install has no database"
		setStatus("failed", &errMsg, nil)
		return
	}

	// Construct site URLs
	srcSiteURL := fmt.Sprintf("https://%s", sourceDomain.Name)
	if useWWW {
		srcSiteURL = fmt.Sprintf("https://www.%s", sourceDomain.Name)
	}

	dstSiteURL := fmt.Sprintf("https://%s", destDomain.Name)
	if useWWW {
		dstSiteURL = fmt.Sprintf("https://www.%s", destDomain.Name)
	}

	// Generate new password for destination database user (can't decrypt stored hash)
	plainPassword := ids.NewSecret()
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), bcrypt.DefaultCost)
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to hash new password: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Update the database user with the new password
	if err := cfg.DatabaseUsers.UpdatePasswordHash(ctx, destDatabaseUser.ID, string(hash)); err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to update database user password: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// Update the MariaDB password via agent. The agent exposes this as
	// db_user.rotate_password (ALTER USER … IDENTIFIED BY …); there is no
	// db_user.set_password handler — the old name silently failed every clone
	// (web path only flips the row to "failed"; the CLI surfaces it).
	if _, err := cfg.Agent.Call(ctx, "db_user.rotate_password", map[string]any{
		"db_user_name": destDatabaseUser.Username,
		"new_password": plainPassword,
	}); err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to update MariaDB password: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	// M19: dispatched through app.clone with all required low-level parameters
	agentResp, err := cfg.Agent.Call(ctx, "app.clone", map[string]any{
		"app_type":         "wordpress",
		"os_user":          osUser,
		"src_docroot":      sourceDomain.DocRoot,
		"dst_docroot":      destDomain.DocRoot,
		"src_db_name":      srcDBName,
		"dst_db_name":      destDatabase.Name,
		"dst_db_user":      destDatabaseUser.Username,
		"dst_db_password":  plainPassword,
		"dst_db_host":      "localhost", // socket via localhost, mirrors install (GH #599)
		"src_site_url":     srcSiteURL,
		"dst_site_url":     dstSiteURL,
		"use_www":          useWWW,
		"dst_subdirectory": dstSubdirectory,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent clone failed: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		// Best-effort cleanup; same dispatcher path.
		cfg.Agent.Call(ctx, "app.delete", map[string]any{
			"app_type":    "wordpress",
			"database_id": destDatabaseID,
		})
		return
	}

	// Parse version from response
	var respMap map[string]any
	if err := json.Unmarshal(agentResp, &respMap); err != nil {
		errMsg := truncateError(fmt.Sprintf("failed to parse agent response: %v", err), 1024)
		setStatus("failed", &errMsg, nil)
		return
	}

	version := ""
	if v, ok := respMap["version"].(string); ok {
		version = v
	}

	// Update status to 'ready' with version
	setStatus("ready", nil, &version)
}

// ---- Helpers ----

func truncateError(msg string, maxLen int) string {
	if len(msg) > maxLen {
		return msg[:maxLen]
	}
	return msg
}

func isValidEmail(email string) bool {
	const emailRegex = `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
	re := regexp.MustCompile(emailRegex)
	return re.MatchString(email)
}

// appDBToken returns the short identifier embedded in an app's database
// and user names (e.g. <osUser>_<token>_<fqdn>_db). WordPress keeps "wp"
// for continuity; every other app uses its own type so a Flarum/Drupal/
// etc. install no longer gets a misleading "_wp_" name (GH #215).
func appDBToken(appType string) string {
	if appType == "" || appType == "wordpress" {
		return "wp"
	}
	if tok := sanitizeDBLabel(appType); tok != "" {
		return tok
	}
	return "app"
}

// allocateAppDBNames builds the FQDN-based database + user names for a WordPress
// install (GH #196): <osuser>_wp_<fqdn>[_<subdir>] with `_db` / `_user`
// suffixes. Dots and any non-[a-z0-9] characters become underscores (the
// DB-user validator forbids dashes), names are lowercased and truncated to
// the 64-char MariaDB identifier limit. (domain, subdirectory) is unique so
// the readable name is normally free; if a collision survives sanitisation
// or truncation, a short random suffix is appended.
func allocateAppDBNames(ctx context.Context, dbs repository.DatabaseRepository, users repository.DatabaseUserRepository, userID, osUser, appType, fqdn, subdir string) (dbName, dbUser string, err error) {
	label := sanitizeDBLabel(fqdn)
	if subdir != "" {
		if sub := sanitizeDBLabel(subdir); sub != "" {
			label += "_" + sub
		}
	}
	prefix := sanitizeDBLabel(osUser) + "_" + appDBToken(appType) + "_"
	for attempt := 0; attempt < 16; attempt++ {
		l := label
		if attempt > 0 {
			u := ids.NewULID()
			l = label + "_" + strings.ToLower(u[len(u)-4:])
		}
		db := fitDBName(prefix, l, "_db")
		usr := fitDBName(prefix, l, "_user")
		dbTaken, e := dbs.ExistsByUserAndName(ctx, userID, db)
		if e != nil {
			return "", "", e
		}
		usrTaken, e := users.ExistsByUserAndUsername(ctx, userID, usr)
		if e != nil {
			return "", "", e
		}
		if !dbTaken && !usrTaken {
			return db, usr, nil
		}
	}
	return "", "", fmt.Errorf("allocateAppDBNames: could not allocate a free name for %q", fqdn)
}

// sanitizeDBLabel lowercases s and maps every non-[a-z0-9] rune to '_',
// collapsing runs and trimming leading/trailing underscores.
func sanitizeDBLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	return strings.Trim(out, "_")
}

// fitDBName assembles prefix+label+suffix, truncating label so the whole
// name fits the 64-char MariaDB identifier limit.
func fitDBName(prefix, label, suffix string) string {
	const max = 64
	budget := max - len(prefix) - len(suffix)
	if budget < 1 {
		budget = 1
	}
	if len(label) > budget {
		label = strings.Trim(label[:budget], "_")
	}
	return prefix + label + suffix
}
