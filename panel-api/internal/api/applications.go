package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/cronops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// adminUsernameAlphabet is the lowercase Latin alphabet — same set
// the operator's directive specified ("6 letters auto generated").
// Avoiding digits and uppercase keeps the generated username trivial
// to communicate verbally and matches MediaWiki/WordPress's broad
// username-rules without needing per-app sanitisation.
const adminUsernameAlphabet = "abcdefghijklmnopqrstuvwxyz"

// generateAdminUsername returns n random lowercase letters from
// crypto/rand. Used for every app's admin account so the UI never has
// to ask for a username — see the descriptor schemas which deliberately
// omit admin_username. Falls back to "admin" + ULID-suffix if rand
// fails so the install doesn't 500 on a transient entropy hiccup.
func generateAdminUsername(n int) string {
	if n <= 0 {
		n = 6
	}
	out := make([]byte, n)
	max := big.NewInt(int64(len(adminUsernameAlphabet)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			fallback := "admin" + strings.ToLower(ids.NewULID()[:6])
			if len(fallback) > n+5 {
				return fallback[:n+5]
			}
			return fallback
		}
		out[i] = adminUsernameAlphabet[idx.Int64()]
	}
	return string(out)
}

// RegisterApplicationRoutes mounts the M19 generic /applications
// surface. The legacy /wordpress-installs routes registered by
// RegisterWordPressRoutes stay live in parallel through M19; the UI in
// step 5 cuts over to /applications.
//
// list / get / delete / clone delegate to the existing wordPressHandler
// methods because the row shape, ownership checks and rollback chain
// are identical — only create needs descriptor-driven dispatch.
func RegisterApplicationRoutes(g *gin.RouterGroup, cfg ApplicationHandlerConfig) {
	if cfg.Apps == nil {
		// Hard fail at registration time, not first request — a missing
		// registry is a wiring bug, not a runtime condition.
		panic("api.RegisterApplicationRoutes: cfg.Apps is nil")
	}
	h := &applicationsHandler{cfg: cfg}
	wp := &wordPressHandler{cfg: cfg}

	g.GET("/applications/registry", h.registry)

	apps := g.Group("/applications")
	apps.POST("", h.create)
	apps.POST("/scan", h.scan)
	apps.GET("", wp.list)
	apps.GET("/:id", wp.get)
	apps.DELETE("/:id", wp.delete)
	apps.POST("/:id/clone", wp.clone)
	apps.PUT("/:id/cache", wp.cache)                     // GH #406: WP object cache + nginx page cache
	apps.GET("/:id/cache-settings", wp.getCacheSettings) // GH #612/#616/#618
	apps.PUT("/:id/cache-settings", wp.setCacheSettings)
	apps.POST("/:id/cache-warmup", wp.cacheWarmup)   // GH #615
	apps.GET("/:id/cache-warmup", wp.getCacheWarmup) // JAB-95 Phase 3: last warmup run
	apps.GET("/cache-profiles", wp.cacheProfiles)    // GH #618
	apps.GET("/:id/cache-stats", wp.cacheStats)      // GH #617
	apps.GET("/:id/cache-health", wp.cacheHealth)    // JAB-11: per-install drift check
	// GH #617: admin cache overview — cache-enabled domains ranked by page-cache
	// hit ratio so low-hit/noisy sites are visible.
	adminCache := g.Group("/admin/cache")
	adminCache.Use(middleware.RequireAdmin())
	adminCache.GET("/overview", wp.cacheOverview)
	// JAB-11: fleet cache-doctor/repair/refresh runs (async — poll by id).
	adminCache.POST("/doctor-runs", wp.startCacheDoctorRun)
	adminCache.GET("/doctor-runs", wp.listCacheDoctorRuns)
	adminCache.GET("/doctor-runs/:id", wp.getCacheDoctorRun)
	apps.POST("/:id/cache-advise", wp.cacheAdvise) // GH #620
}

type applicationsHandler struct{ cfg ApplicationHandlerConfig }

// registryEntry is the JSON body returned by GET /applications/registry.
// We project the App descriptor onto a dedicated struct so internal
// fields (AgentInstallCmd, etc.) never leak to the UI.
type registryEntry struct {
	Name                 string                    `json:"name"`
	DisplayName          string                    `json:"display_name"`
	Icon                 string                    `json:"icon,omitempty"`
	Description          string                    `json:"description,omitempty"`
	Tags                 []string                  `json:"tags,omitempty"`
	DefaultSubdirectory  string                    `json:"default_subdirectory"`
	RequiresDB           bool                      `json:"requires_db"`
	EmailLogin           bool                      `json:"email_login,omitempty"`
	RootOnly             bool                      `json:"root_only,omitempty"`
	SupportedPHPVersions []string                  `json:"supported_php_versions,omitempty"`
	InstallParamSchema   map[string]apps.ParamSpec `json:"install_param_schema,omitempty"`
	// InstallNotice (GH #341) — pre-install warning the UI shows in the
	// install modal. Was defined on the registry + rendered by the SPA, but
	// this DTO dropped it, so the notice never reached the client.
	InstallNotice string `json:"install_notice,omitempty"`
}

func (h *applicationsHandler) registry(c *gin.Context) {
	descriptors := h.cfg.Apps.List()
	out := make([]registryEntry, 0, len(descriptors))
	for _, d := range descriptors {
		out = append(out, registryEntry{
			Name:                 d.Name,
			DisplayName:          d.DisplayName,
			Icon:                 d.Icon,
			Description:          d.Description,
			Tags:                 d.Tags,
			DefaultSubdirectory:  d.DefaultSubdirectory,
			RequiresDB:           d.RequiresDB,
			EmailLogin:           d.EmailLogin,
			RootOnly:             d.RootOnly,
			SupportedPHPVersions: d.SupportedPHPVersions,
			InstallParamSchema:   d.InstallParamSchema,
			InstallNotice:        d.InstallNotice,
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// createApplicationRequest is the M19 generic install request. The
// UI sends app_type to pick a descriptor; params is the per-app
// extension validated against descriptor.InstallParamSchema.
type createApplicationRequest struct {
	AppType      string         `json:"app_type" binding:"required"`
	DomainID     string         `json:"domain_id" binding:"required"`
	Subdirectory string         `json:"subdirectory"`
	UseWWW       bool           `json:"use_www"`
	Params       map[string]any `json:"params"`
}

// createApplicationResponse mirrors createWordPressResponse so the UI
// can render either surface with the same row shape. AppType is added
// so the UI can route the row to the right detail page.
type createApplicationResponse struct {
	ID            string    `json:"id"`
	AppType       string    `json:"app_type"`
	DomainID      string    `json:"domain_id"`
	DBID          string    `json:"db_id"`
	AdminUsername string    `json:"admin_username,omitempty"`
	AdminPassword string    `json:"admin_password,omitempty"`
	AdminEmail    string    `json:"admin_email,omitempty"`
	UseWWW        bool      `json:"use_www"`
	Subdirectory  string    `json:"subdirectory"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (h *applicationsHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	res, cerr := InstallApplication(c.Request.Context(), h.cfg, InstallParams{
		AppType:      req.AppType,
		UserID:       claims.UserID,
		IsAdminCall:  claims.IsAdmin,
		DomainID:     req.DomainID,
		Subdirectory: req.Subdirectory,
		UseWWW:       req.UseWWW,
		Params:       req.Params,
	})
	if cerr != nil {
		body := gin.H{"error": cerr.Code}
		if cerr.Detail != "" {
			body["detail"] = cerr.Detail
		}
		c.JSON(cerr.HTTPStatus, body)
		return
	}

	install := res.Install
	c.JSON(http.StatusAccepted, createApplicationResponse{
		ID:            install.ID,
		AppType:       install.AppType,
		DomainID:      install.DomainID,
		DBID:          install.DBIDOr(),
		AdminUsername: install.AdminUsername,
		AdminPassword: res.AdminPassword,
		AdminEmail:    install.AdminEmail,
		UseWWW:        install.UseWWW,
		Subdirectory:  install.Subdirectory,
		Status:        install.Status,
		CreatedAt:     install.CreatedAt,
		UpdatedAt:     install.UpdatedAt,
	})
}

// validateInstallParams enforces a descriptor's InstallParamSchema
// against the JSON params blob the UI sent. Rejects missing required
// fields, unknown fields, type/regex/enum mismatches, and empty
// strings for required fields. Errors are user-facing — keep them
// concise and field-named.
func validateInstallParams(params map[string]any, schema map[string]apps.ParamSpec) error {
	if len(schema) == 0 {
		// Nothing to validate; allow whatever the caller sent.
		return nil
	}
	for field, spec := range schema {
		raw, present := params[field]
		if !present || raw == nil {
			if spec.Required {
				return fmt.Errorf("missing required param %q", field)
			}
			continue
		}
		if err := validateOne(field, spec, raw); err != nil {
			return err
		}
	}
	for field := range params {
		if _, ok := schema[field]; !ok {
			return fmt.Errorf("unknown param %q", field)
		}
	}
	return nil
}

func validateOne(field string, spec apps.ParamSpec, raw any) error {
	switch spec.Type {
	case "string", "password":
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("param %q: expected string", field)
		}
		if spec.Required && s == "" {
			return fmt.Errorf("param %q: required", field)
		}
		if spec.Pattern != nil {
			re, err := regexp.Compile(*spec.Pattern)
			if err != nil {
				return fmt.Errorf("param %q: invalid pattern", field)
			}
			if !re.MatchString(s) {
				return fmt.Errorf("param %q: does not match pattern", field)
			}
		}
	case "email":
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("param %q: expected string", field)
		}
		if spec.Required && s == "" {
			return fmt.Errorf("param %q: required", field)
		}
		if !isValidEmail(s) {
			return fmt.Errorf("param %q: invalid email", field)
		}
	case "bool":
		if _, ok := raw.(bool); !ok {
			return fmt.Errorf("param %q: expected bool", field)
		}
	case "enum":
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("param %q: expected string", field)
		}
		matched := false
		for _, v := range spec.Values {
			if s == v {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("param %q: must be one of %v", field, spec.Values)
		}
	default:
		return fmt.Errorf("param %q: unsupported type %q", field, spec.Type)
	}
	return nil
}

// paramOr returns the string value at key or the default when missing
// or wrong type. Chosen over Sprint(raw) because the UI may send a
// JSON null which `fmt` would render as "<nil>" — clearly worse than
// a sane default.
func paramOr(params map[string]any, key, def string) string {
	if v, ok := paramString(params, key); ok && v != "" {
		return v
	}
	return def
}

func paramString(params map[string]any, key string) (string, bool) {
	if params == nil {
		return "", false
	}
	v, ok := params[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// provisionedDB carries the IDs and MariaDB-side names a successful
// provisionDBChain produced. The install handler holds onto this so a
// later failure (install row insert) can be unwound via rollbackDBChain
// without re-deriving names from suffixes.
type provisionedDB struct {
	DBID       string
	DBUserID   string
	GrantID    string
	DBName     string
	DBUsername string
	// DBPassword is the generated MariaDB password for DBUsername. It is
	// ALWAYS a fresh secret, distinct from any app admin password (GH #226)
	// — the app installer writes it into the app's DB config.
	DBPassword string
}

// provisionDBChain mirrors the inline DB-create chain in
// wordPressHandler.create: panel rows + MariaDB CREATE DATABASE/USER/
// GRANT via the agent. Each step rolls back the prior ones on failure
// before returning the error.
func provisionDBChain(ctx context.Context, cfg ApplicationHandlerConfig, userID, osUser, appType, dbPassword string) (provisionedDB, error) {
	now := time.Now().UTC()
	dbName, dbUsername, nameErr := allocateAppDBNames(ctx, cfg.Databases, cfg.DatabaseUsers, userID, osUser, appType)
	if nameErr != nil {
		return provisionedDB{}, fmt.Errorf("allocate db names: %w", nameErr)
	}
	dbID := ids.NewULID()
	database := &models.Database{
		ID:        dbID,
		UserID:    userID,
		Name:      dbName,
		Engine:    "mariadb",
		Charset:   "utf8mb4",
		Collation: "utf8mb4_unicode_ci",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := cfg.Databases.Create(ctx, database); err != nil {
		return provisionedDB{}, fmt.Errorf("database row: %w", err)
	}

	dbUserID := ids.NewULID()
	hash, err := bcrypt.GenerateFromPassword([]byte(dbPassword), bcrypt.DefaultCost)
	if err != nil {
		cfg.Databases.Delete(ctx, dbID)
		return provisionedDB{}, fmt.Errorf("bcrypt: %w", err)
	}
	databaseUser := &models.DatabaseUser{
		ID:           dbUserID,
		UserID:       userID,
		Username:     dbUsername,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := cfg.DatabaseUsers.Create(ctx, databaseUser); err != nil {
		cfg.Databases.Delete(ctx, dbID)
		return provisionedDB{}, fmt.Errorf("database user row: %w", err)
	}

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
	if err := cfg.DatabaseGrants.Create(ctx, grant); err != nil {
		cfg.DatabaseUsers.Delete(ctx, dbUserID)
		cfg.Databases.Delete(ctx, dbID)
		return provisionedDB{}, fmt.Errorf("grant row: %w", err)
	}

	chain := provisionedDB{
		DBID:       dbID,
		DBUserID:   dbUserID,
		GrantID:    grantID,
		DBName:     dbName,
		DBUsername: dbUsername,
		DBPassword: dbPassword,
	}

	if cfg.Agent != nil {
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		if _, acErr := cfg.Agent.Call(agentCtx, "db.create", map[string]any{
			"db_name":   dbName,
			"charset":   "utf8mb4",
			"collation": "utf8mb4_unicode_ci",
		}); acErr != nil {
			rollbackDBChain(ctx, cfg, chain)
			return provisionedDB{}, fmt.Errorf("agent db.create: %w", acErr)
		}

		if _, acErr := cfg.Agent.Call(agentCtx, "db_user.create", map[string]any{
			"db_user_name": dbUsername,
			"password":     dbPassword,
		}); acErr != nil {
			cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": dbName})
			rollbackDBChain(ctx, cfg, chain)
			return provisionedDB{}, fmt.Errorf("agent db_user.create: %w", acErr)
		}

		if _, acErr := cfg.Agent.Call(agentCtx, "db_user.grant", map[string]any{
			"db_name":      dbName,
			"db_user_name": dbUsername,
			"grant_level":  "rw",
			"privileges":   []string{"ALL"},
		}); acErr != nil {
			cfg.Agent.Call(ctx, "db_user.drop", map[string]any{"db_user_name": dbUsername})
			cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": dbName})
			rollbackDBChain(ctx, cfg, chain)
			return provisionedDB{}, fmt.Errorf("agent db_user.grant: %w", acErr)
		}
	}

	return chain, nil
}

// rollbackDBChain unwinds a successful provisionDBChain when a later
// step (install row create) fails. Best-effort: agent calls are
// fire-and-forget, panel rows are deleted in child→parent order so
// FK on grants doesn't block.
func rollbackDBChain(ctx context.Context, cfg ApplicationHandlerConfig, chain provisionedDB) {
	if chain.DBID == "" {
		return
	}
	// The agent calls were fire-and-forget: the rows went whether or not the
	// host-side objects did, so a failed drop left an unreferenced MariaDB
	// database and login behind. Keep the rows when a drop fails — an
	// unattached database the owner can see and retry beats one nothing
	// names.
	if cfg.Agent != nil {
		dropFailed := false
		if _, err := cfg.Agent.Call(ctx, "db_user.drop", map[string]any{"db_user_name": chain.DBUsername}); err != nil {
			dropFailed = true
			slog.ErrorContext(ctx, "app install rollback: db_user.drop failed — keeping the panel rows so the account stays visible instead of becoming an orphan",
				"err", err, "db_user", chain.DBUsername)
		}
		if _, err := cfg.Agent.Call(ctx, "db.drop", map[string]any{"db_name": chain.DBName}); err != nil {
			dropFailed = true
			slog.ErrorContext(ctx, "app install rollback: db.drop failed — keeping the panel rows so the database stays visible instead of becoming an orphan",
				"err", err, "db_name", chain.DBName)
		}
		if dropFailed {
			return
		}
	}
	cfg.DatabaseGrants.Delete(ctx, chain.GrantID)
	cfg.DatabaseUsers.Delete(ctx, chain.DBUserID)
	cfg.Databases.Delete(ctx, chain.DBID)
}

// mediaWikiKickArgs is what the install-row kicker passes through to
// the agent's mediawiki installer (via the app.install dispatcher).
// Mirrors installKickArgs (DB fields included, since MediaWiki is
// RequiresDB=true) plus a Language param.
type mediaWikiKickArgs struct {
	UserID       string
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	SiteTitle    string
	AdminUser    string
	AdminPass    string
	AdminEmail   string
	Language     string
	UseWWW       bool
}

// createMediaWikiInstallAndKickAgent flips the install row to
// "installing", dispatches app.install with app_type="mediawiki" via
// the agent dispatcher (step 4), and updates the row to "ready" or
// "failed" based on the result. Mirrors createInstallAndKickAgent +
// createDokuWikiInstallAndKickAgent for the MediaWiki-specific param
// shape.
func createMediaWikiInstallAndKickAgent(parentCtx context.Context, args mediaWikiKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "mediawiki",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"site_title":   args.SiteTitle,
		"admin_user":   args.AdminUser,
		"admin_pass":   args.AdminPass,
		"admin_email":  args.AdminEmail,
		"language":     args.Language,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	createAppCrons(ctx, cfg, args.UserID, "mediawiki", appInstallPath(args.DocRoot, args.Subdirectory))
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

type moodleKickArgs struct {
	InstallID    string
	UserID       string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	SiteTitle    string
	AdminUser    string
	AdminPass    string
	AdminEmail   string
	Language     string
}

// createMoodleInstallAndKickAgent flips the install row to "installing",
// dispatches app.install with app_type="moodle", and updates the row to
// ready/failed. Mirrors createMediaWikiInstallAndKickAgent with Moodle's
// param shape (GH #310).
func createMoodleInstallAndKickAgent(parentCtx context.Context, args moodleKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "moodle",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"site_title":   args.SiteTitle,
		"admin_user":   args.AdminUser,
		"admin_pass":   args.AdminPass,
		"admin_email":  args.AdminEmail,
		"language":     args.Language,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}
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
	createAppCrons(ctx, cfg, args.UserID, "moodle", appInstallPath(args.DocRoot, args.Subdirectory))
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// dokuWikiKickArgs is what the install-row kicker passes to the agent's
// DokuWiki installer. No DB fields — DokuWiki is flat-file (RequiresDB
// false), so the framework never provisions a schema.
type dokuWikiKickArgs struct {
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	SiteTitle    string
	AdminUser    string
	AdminPass    string
	AdminEmail   string
	UseWWW       bool
}

// createDokuWikiInstallAndKickAgent flips the install row to "installing",
// dispatches app.install with app_type="dokuwiki" (no DB params), and
// updates the row to "ready"/"failed". Mirrors
// createMediaWikiInstallAndKickAgent minus the DB fields.
func createDokuWikiInstallAndKickAgent(parentCtx context.Context, args dokuWikiKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "dokuwiki",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"site_title":   args.SiteTitle,
		"admin_user":   args.AdminUser,
		"admin_pass":   args.AdminPass,
		"admin_email":  args.AdminEmail,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// drupalKickArgs is what the install-row kicker passes through to the
// agent's drupal installer (via the app.install dispatcher). Mirrors
// mediaWikiKickArgs (RequiresDB=true, same DB fields) plus a Profile
// param for drush's install-profile selection and SiteMail for the
// outbound From-address.
type drupalKickArgs struct {
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	SiteTitle    string
	AdminUser    string
	AdminPass    string
	AdminEmail   string
	SiteMail     string
	Profile      string
	UseWWW       bool
}

// createDrupalInstallAndKickAgent flips the install row to "installing",
// dispatches app.install with app_type="drupal" via the agent
// dispatcher, and updates the row to "ready" or "failed" based on the
// result. Mirrors createMediaWikiInstallAndKickAgent for the Drupal-
// specific param shape.
//
// Timeout is 20 minutes — `composer require drush/drush` can take 5-10
// min on a cold cache, then drush site:install another 1-2 min.
func createDrupalInstallAndKickAgent(parentCtx context.Context, args drupalKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "drupal",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"site_title":   args.SiteTitle,
		"admin_user":   args.AdminUser,
		"admin_pass":   args.AdminPass,
		"admin_email":  args.AdminEmail,
		"site_mail":    args.SiteMail,
		"profile":      args.Profile,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// joomlaKickArgs is what the install-row kicker passes through to the
// agent's joomla installer (via the app.install dispatcher). Mirrors
// drupalKickArgs minus profile/site_mail; Joomla wants a display name
// (admin_full_name) distinct from the login username.
type joomlaKickArgs struct {
	UserID        string
	InstallID     string
	OSUser        string
	DocRoot       string
	Subdirectory  string
	SiteURL       string
	DBName        string
	DBUser        string
	DBPassword    string
	SiteTitle     string
	AdminUser     string
	AdminPass     string
	AdminEmail    string
	AdminFullName string
	UseWWW        bool
}

// createJoomlaInstallAndKickAgent flips the install row to "installing",
// dispatches app.install with app_type="joomla" via the agent
// dispatcher, and updates the row to "ready" or "failed" based on the
// result.
//
// 10-minute timeout — Joomla's tarball is ~30MB and the CLI installer
// runs ~30s on a typical host; no composer chain so much shorter than
// Drupal's 20-min budget.
func createJoomlaInstallAndKickAgent(parentCtx context.Context, args joomlaKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":        "joomla",
		"os_user":         args.OSUser,
		"docroot":         args.DocRoot,
		"subdirectory":    args.Subdirectory,
		"site_url":        args.SiteURL,
		"db_name":         args.DBName,
		"db_user":         args.DBUser,
		"db_password":     args.DBPassword,
		"db_host":         "localhost",
		"site_title":      args.SiteTitle,
		"admin_user":      args.AdminUser,
		"admin_pass":      args.AdminPass,
		"admin_email":     args.AdminEmail,
		"admin_full_name": args.AdminFullName,
		"use_www":         args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	createAppCrons(ctx, cfg, args.UserID, "joomla", appInstallPath(args.DocRoot, args.Subdirectory))
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// phpbbKickArgs is what the install-row kicker passes through to the
// agent's phpbb installer (via the app.install dispatcher).
type phpbbKickArgs struct {
	InstallID        string
	OSUser           string
	DocRoot          string
	Subdirectory     string
	SiteURL          string
	DBName           string
	DBUser           string
	DBPassword       string
	SiteTitle        string
	BoardDescription string
	AdminUser        string
	AdminPass        string
	AdminEmail       string
	Language         string
	UseWWW           bool
}

// createPhpBBInstallAndKickAgent flips the install row to "installing",
// dispatches app.install with app_type="phpbb" and updates the row
// to "ready" or "failed" based on the result.
func createPhpBBInstallAndKickAgent(parentCtx context.Context, args phpbbKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":          "phpbb",
		"os_user":           args.OSUser,
		"docroot":           args.DocRoot,
		"subdirectory":      args.Subdirectory,
		"site_url":          args.SiteURL,
		"db_name":           args.DBName,
		"db_user":           args.DBUser,
		"db_password":       args.DBPassword,
		"db_host":           "localhost",
		"site_title":        args.SiteTitle,
		"board_description": args.BoardDescription,
		"admin_user":        args.AdminUser,
		"admin_pass":        args.AdminPass,
		"admin_email":       args.AdminEmail,
		"language":          args.Language,
		"use_www":           args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// flarumKickArgs is what the install-row kicker passes through to the
// agent's flarum installer (via the app.install dispatcher).
type flarumKickArgs struct {
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	SiteTitle    string
	AdminUser    string
	AdminPass    string
	AdminEmail   string
	UseWWW       bool
}

// createFlarumInstallAndKickAgent flips the install row to "installing",
// dispatches app.install with app_type="flarum" and updates the row to
// "ready" or "failed" based on the result.
func createFlarumInstallAndKickAgent(parentCtx context.Context, args flarumKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "flarum",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"site_title":   args.SiteTitle,
		"admin_user":   args.AdminUser,
		"admin_pass":   args.AdminPass,
		"admin_email":  args.AdminEmail,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// itflowKickArgs is what the install-row kicker passes through to the
// agent's itflow installer (via the app.install dispatcher).
type itflowKickArgs struct {
	InstallID    string
	UserID       string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	CompanyName  string
	AdminName    string
	AdminEmail   string
	AdminPass    string
	RepoBranch   string
	UseWWW       bool
}

// createITFlowInstallAndKickAgent flips the row to "installing", clones +
// installs ITFlow via the agent, auto-creates the three ITFlow cron jobs,
// and flips the row to "ready"/"failed". Clone + setup_cli can be slow, so
// the timeout is generous.
func createITFlowInstallAndKickAgent(parentCtx context.Context, args itflowKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "itflow",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"company_name": args.CompanyName,
		"admin_name":   args.AdminName,
		"admin_email":  args.AdminEmail,
		"admin_pass":   args.AdminPass,
		"repo_branch":  args.RepoBranch,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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

	// Auto-create the three ITFlow cron jobs. Best-effort: the app is
	// already installed + usable; a cron failure is logged, not fatal.
	createITFlowCrons(ctx, args, cfg)

	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// appInstallPath returns the on-disk install root for an app install
// (docroot, or docroot/<subdir> for a subdirectory install).
func appInstallPath(docRoot, subdirectory string) string {
	if sub := strings.Trim(subdirectory, "/"); sub != "" {
		return docRoot + "/" + sub
	}
	return docRoot
}

// itflowCronSpec is the cron ITFlow needs. As of the 2026-08 stable
// (GH #928) cron.php is a DISPATCHER: it is the only crontab entry — run
// every minute, it wakes, works out which jobs are due from the cron_jobs
// table (edited under Maintenance > Cron), and runs them in-process. The
// old per-job crons (mail_queue.php, ticket_email_parser.php) are no longer
// scheduled directly; the dispatcher invokes them.
var itflowCronSpec = []struct {
	name, schedule, file string
}{
	{"ITFlow Cron", "* * * * *", "cron.php"},
}

func createITFlowCrons(ctx context.Context, args itflowKickArgs, cfg ApplicationHandlerConfig) {
	if cfg.CronJobs == nil || cfg.Users == nil || cfg.Domains == nil {
		return
	}
	installPath := appInstallPath(args.DocRoot, args.Subdirectory)
	deps := cronops.Deps{Users: cfg.Users, Domains: cfg.Domains, CronJobs: cfg.CronJobs, Agent: cfg.Agent}
	for _, c := range itflowCronSpec {
		cmd := fmt.Sprintf("php %s/cron/%s", installPath, c.file)
		if _, err := cronops.Create(ctx, deps, cronops.CreateInput{
			UserID:   args.UserID,
			Name:     c.name,
			Schedule: c.schedule,
			Command:  cmd,
			Enabled:  true,
		}); err != nil {
			slog.WarnContext(ctx, "itflow: create cron failed", "name", c.name, "command", cmd, "err", err)
		}
	}
}

// osticketKickArgs bundles the osTicket install inputs (GH #962, JAB-231).
type osticketKickArgs struct {
	InstallID     string
	UserID        string
	OSUser        string
	DocRoot       string
	SiteURL       string
	DBName        string
	DBUser        string
	DBPassword    string
	HelpdeskName  string
	HelpdeskEmail string
	AdminFirst    string
	AdminLast     string
	AdminEmail    string
	AdminUsername string
	AdminPass     string
}

// createOsTicketInstallAndKickAgent flips the row to "installing", installs
// osTicket via the agent (download + headless Installer->install() + nginx
// PATH_INFO snippet), auto-creates the osTicket cron, and flips the row to
// "ready"/"failed".
func createOsTicketInstallAndKickAgent(parentCtx context.Context, args osticketKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":       "osticket",
		"os_user":        args.OSUser,
		"docroot":        args.DocRoot,
		"site_url":       args.SiteURL,
		"db_name":        args.DBName,
		"db_user":        args.DBUser,
		"db_password":    args.DBPassword,
		"db_host":        "localhost",
		"helpdesk_name":  args.HelpdeskName,
		"helpdesk_email": args.HelpdeskEmail,
		"admin_first":    args.AdminFirst,
		"admin_last":     args.AdminLast,
		"admin_email":    args.AdminEmail,
		"admin_username": args.AdminUsername,
		"admin_pass":     args.AdminPass,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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

	// osTicket cron (email fetch + scheduled tasks). Best-effort: the app is
	// already installed + usable; a cron failure is logged, not fatal.
	createOsTicketCron(ctx, args, cfg)

	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// createOsTicketCron registers osTicket's api/cron.php (every 5 minutes).
func createOsTicketCron(ctx context.Context, args osticketKickArgs, cfg ApplicationHandlerConfig) {
	if cfg.CronJobs == nil || cfg.Users == nil || cfg.Domains == nil {
		return
	}
	installPath := appInstallPath(args.DocRoot, "")
	deps := cronops.Deps{Users: cfg.Users, Domains: cfg.Domains, CronJobs: cfg.CronJobs, Agent: cfg.Agent}
	cmd := fmt.Sprintf("php %s/api/cron.php", installPath)
	if _, err := cronops.Create(ctx, deps, cronops.CreateInput{
		UserID:   args.UserID,
		Name:     "osTicket Cron",
		Schedule: "*/5 * * * *",
		Command:  cmd,
		Enabled:  true,
	}); err != nil {
		slog.WarnContext(ctx, "osticket: create cron failed", "command", cmd, "err", err)
	}
}

// removeITFlowCrons tears down the ITFlow cron jobs for an install on
// delete. Matches by command path (`php <installPath>/cron/`) since crons
// aren't FK-linked to the install. Best-effort.
func removeITFlowCrons(ctx context.Context, cfg ApplicationHandlerConfig, userID, osUser, installPath string) {
	if cfg.CronJobs == nil || userID == "" {
		return
	}
	jobs, err := cfg.CronJobs.ListByUserID(ctx, userID)
	if err != nil {
		return
	}
	marker := "php " + installPath + "/cron/"
	for _, j := range jobs {
		if !strings.HasPrefix(j.Command, marker) {
			continue
		}
		if cfg.Agent != nil && osUser != "" {
			_, _ = cfg.Agent.Call(ctx, "cron.remove", cronRemoveAgentParams{
				UserID:    userID,
				Username:  osUser,
				JobID:     j.ID,
				RunAsRoot: j.RunAsRoot,
			})
		}
		_ = cfg.CronJobs.Delete(ctx, j.ID)
	}
}

// appCron is one auto-created system cron job for an app.
type appCron struct{ name, schedule, command string }

// appCronSpecs returns the system cron jobs an app needs so its scheduled work
// runs reliably without depending on web traffic (WP-Cron, Joomla scheduler,
// MediaWiki job queue). installPath is the on-disk app root. Every command
// stays inside the wp/php cron allowlist (internal/cronvalidate).
func appCronSpecs(appType, installPath string) []appCron {
	switch appType {
	case "wordpress":
		return []appCron{{"WordPress Cron", "*/15 * * * *",
			fmt.Sprintf("wp cron event run --due-now --path=%s", installPath)}}
	case "joomla":
		return []appCron{{"Joomla Scheduler", "*/15 * * * *",
			fmt.Sprintf("php %s/cli/joomla.php scheduler:run --all", installPath)}}
	case "mediawiki":
		return []appCron{{"MediaWiki Job Queue", "*/15 * * * *",
			fmt.Sprintf("php %s/maintenance/run.php runJobs", installPath)}}
	case "moodle":
		return []appCron{{"Moodle Cron", "*/5 * * * *",
			fmt.Sprintf("php %s/admin/cli/cron.php", installPath)}}
	}
	return nil
}

// createAppCrons auto-creates the app's system cron jobs at install time.
// Best-effort: the app is already usable, so a cron failure is logged, not fatal.
func createAppCrons(ctx context.Context, cfg ApplicationHandlerConfig, userID, appType, installPath string) {
	specs := appCronSpecs(appType, installPath)
	if len(specs) == 0 || userID == "" || cfg.CronJobs == nil || cfg.Users == nil || cfg.Domains == nil {
		return
	}
	deps := cronops.Deps{Users: cfg.Users, Domains: cfg.Domains, CronJobs: cfg.CronJobs, Agent: cfg.Agent}
	for _, c := range specs {
		if _, err := cronops.Create(ctx, deps, cronops.CreateInput{
			UserID:   userID,
			Name:     c.name,
			Schedule: c.schedule,
			Command:  c.command,
			Enabled:  true,
		}); err != nil {
			slog.WarnContext(ctx, "app: create cron failed", "app", appType, "name", c.name, "command", c.command, "err", err)
		}
	}
}

// removeAppCrons tears down an app's auto-created cron jobs on delete. Matches
// by EXACT command (rebuilt from installPath) so a docroot install can't sweep
// a subdir install's jobs. Best-effort.
func removeAppCrons(ctx context.Context, cfg ApplicationHandlerConfig, userID, osUser, appType, installPath string) {
	if userID == "" || cfg.CronJobs == nil {
		return
	}
	// The auto-created app crons (exact-command match)...
	want := make(map[string]bool)
	for _, c := range appCronSpecs(appType, installPath) {
		want[c.command] = true
	}
	jobs, err := cfg.CronJobs.ListByUserID(ctx, userID)
	if err != nil {
		return
	}
	// GH #343: also remove any OTHER cron whose command references a path INSIDE
	// the install directory (installPath + "/"). Once the app is deleted its
	// directory is gone, so such a cron would only fail on every run — cleaning
	// it up is cleanup, not data loss.
	pathRef := installPath + "/"
	for _, j := range jobs {
		if !want[j.Command] && (installPath == "" || !strings.Contains(j.Command, pathRef)) {
			continue
		}
		if cfg.Agent != nil && osUser != "" {
			_, _ = cfg.Agent.Call(ctx, "cron.remove", cronRemoveAgentParams{
				UserID:    userID,
				Username:  osUser,
				JobID:     j.ID,
				RunAsRoot: j.RunAsRoot,
			})
		}
		_ = cfg.CronJobs.Delete(ctx, j.ID)
	}
}

// opencartKickArgs and createOpenCartInstallAndKickAgent — e-commerce.
type opencartKickArgs struct {
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	AdminUser    string
	AdminPass    string
	AdminEmail   string
	UseWWW       bool
}

func createOpenCartInstallAndKickAgent(parentCtx context.Context, args opencartKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "opencart",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"admin_user":   args.AdminUser,
		"admin_pass":   args.AdminPass,
		"admin_email":  args.AdminEmail,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}
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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// prestashopKickArgs and createPrestaShopInstallAndKickAgent — e-commerce.
type prestashopKickArgs struct {
	InstallID      string
	OSUser         string
	DocRoot        string
	Subdirectory   string
	SiteURL        string
	DBName         string
	DBUser         string
	DBPassword     string
	SiteTitle      string
	AdminEmail     string
	AdminPass      string
	AdminFirstName string
	AdminLastName  string
	Country        string
	Language       string
	UseWWW         bool
}

func createPrestaShopInstallAndKickAgent(parentCtx context.Context, args prestashopKickArgs, cfg ApplicationHandlerConfig) {
	// 20 min — PrestaShop's schema + sample-catalog import is the long
	// pole; on slower hosts the install can run 8-12 minutes.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":         "prestashop",
		"os_user":          args.OSUser,
		"docroot":          args.DocRoot,
		"subdirectory":     args.Subdirectory,
		"site_url":         args.SiteURL,
		"db_name":          args.DBName,
		"db_user":          args.DBUser,
		"db_password":      args.DBPassword,
		"db_host":          "localhost",
		"site_title":       args.SiteTitle,
		"admin_email":      args.AdminEmail,
		"admin_pass":       args.AdminPass,
		"admin_first_name": args.AdminFirstName,
		"admin_last_name":  args.AdminLastName,
		"country":          args.Country,
		"language":         args.Language,
		"use_www":          args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}
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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// pickAdminUsername prefers the agent-detected WP admin login (from
// wp_users) over the operator's email. Empty / missing detection
// falls back to the operator email so the row at least has SOMETHING
// non-empty (magic_link.go refuses empty admin_username).
func pickAdminUsername(detected, fallback string) string {
	if detected != "" {
		return detected
	}
	return fallback
}

// scan walks the user's homedir for unregistered WordPress / Joomla /
// Drupal / Magento installs + INSERTs an application_installs row for
// each match not already tracked. Triggered from the operator UI's
// "Scan for Applications" button on /jabali-panel/applications.
//
// Idempotent: re-scans skip every (domain_id, subdirectory, app_type)
// triple that already exists. Returns the count of NEW rows + the
// list of detected (added + skipped) for the UI to display.
func (h *applicationsHandler) scan(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unwired"})
		return
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil || user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "user_lookup", "detail": "panel user has no Linux username"})
		return
	}

	raw, err := h.cfg.Agent.Call(c.Request.Context(), "app.scan", map[string]any{
		"username": *user.Username,
	})
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var resp struct {
		Hits []struct {
			Domain        string `json:"domain"`
			Subdirectory  string `json:"subdirectory"`
			AppType       string `json:"app_type"`
			Version       string `json:"version"`
			AdminUsername string `json:"admin_username"`
		} `json:"hits"`
	}
	if jErr := json.Unmarshal(raw, &resp); jErr != nil {
		respondAgentErr(c, "agent_decode", jErr)
		return
	}

	type scanReport struct {
		Domain       string `json:"domain"`
		Subdirectory string `json:"subdirectory"`
		AppType      string `json:"app_type"`
		Version      string `json:"version,omitempty"`
		Action       string `json:"action"` // "added" | "skipped_already_present" | "skipped_no_domain"
		InstallID    string `json:"install_id,omitempty"`
	}
	var report []scanReport
	added := 0
	for _, hit := range resp.Hits {
		// Resolve domain → panel domain id; skip when domain not in panel DB
		// (operator may have removed it, or the docroot dir survived a
		// domain.delete).
		dom, dErr := h.cfg.Domains.FindByName(c.Request.Context(), hit.Domain)
		if dErr != nil || dom == nil {
			report = append(report, scanReport{
				Domain: hit.Domain, Subdirectory: hit.Subdirectory,
				AppType: hit.AppType, Action: "skipped_no_domain",
			})
			continue
		}
		// Owner check — multi-tenant panel mustn't claim an app under
		// someone else's domain.
		if dom.UserID != user.ID {
			report = append(report, scanReport{
				Domain: hit.Domain, Subdirectory: hit.Subdirectory,
				AppType: hit.AppType, Action: "skipped_domain_owned_by_other_user",
			})
			continue
		}
		// Existing-install check.
		existing, _ := h.cfg.ApplicationInstalls.FindByDomainAndSubdirectoryAndAppType(
			c.Request.Context(), dom.ID, hit.Subdirectory, hit.AppType)
		if existing != nil {
			report = append(report, scanReport{
				Domain: hit.Domain, Subdirectory: hit.Subdirectory,
				AppType: hit.AppType, Version: hit.Version,
				Action: "skipped_already_present", InstallID: existing.ID,
			})
			continue
		}
		// Insert a `discovered` row. status=ready so the row is
		// usable immediately; admin_email + admin_username default
		// to the panel user (operator can edit via the existing
		// /:id update path). DB FK is nil because the discovered
		// install already wired its own DB outside the panel.
		install := &models.ApplicationInstall{
			ID:            genULID(),
			UserID:        user.ID,
			DomainID:      dom.ID,
			DBID:          nil,
			AdminUsername: pickAdminUsername(hit.AdminUsername, claims.Email),
			AdminEmail:    claims.Email,
			Locale:        "en_US",
			Subdirectory:  hit.Subdirectory,
			Status:        "ready",
			AppType:       hit.AppType,
		}
		if hit.Version != "" {
			install.Version = &hit.Version
		}
		if cErr := h.cfg.ApplicationInstalls.Create(c.Request.Context(), install); cErr != nil {
			report = append(report, scanReport{
				Domain: hit.Domain, Subdirectory: hit.Subdirectory,
				AppType: hit.AppType, Version: hit.Version,
				Action: "skipped_db_error",
			})
			continue
		}
		added++
		report = append(report, scanReport{
			Domain: hit.Domain, Subdirectory: hit.Subdirectory,
			AppType: hit.AppType, Version: hit.Version,
			Action: "added", InstallID: install.ID,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"scanned": len(resp.Hits),
		"added":   added,
		"report":  report,
	})
}

// privateBinKickArgs — PrivateBin has no accounts and no DB (GH #631), so
// the kick carries only the install-site coordinates + title.
type privateBinKickArgs struct {
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	SiteTitle    string
	UseWWW       bool
}

// createPrivateBinInstallAndKickAgent mirrors the DokuWiki kicker minus
// every credential field.
func createPrivateBinInstallAndKickAgent(parentCtx context.Context, args privateBinKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "privatebin",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"site_title":   args.SiteTitle,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// invoiceShelfKickArgs — InvoiceShelf is RequiresDB=true (Laravel + MariaDB)
// and logs in by email, so no admin_username. GH #631.
type invoiceShelfKickArgs struct {
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	SiteTitle    string
	// Currency is the ISO 4217 code for the company currency (GH #1042);
	// enum-gated by the descriptor, defaulted to USD by the service.
	Currency string
	// Country is the ISO 3166-1 alpha-2 company country (GH #1042
	// follow-up); enum-gated by the descriptor, defaulted to US.
	Country    string
	AdminEmail string
	AdminPass  string
	UseWWW     bool
}

// createInvoiceShelfInstallAndKickAgent flips the install row to
// "installing", dispatches app.install with app_type="invoiceshelf" (the
// agent derives the domain from site_url, like Flarum), and updates the row.
func createInvoiceShelfInstallAndKickAgent(parentCtx context.Context, args invoiceShelfKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "invoiceshelf",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"site_title":   args.SiteTitle,
		"currency":     args.Currency,
		"country":      args.Country,
		"admin_email":  args.AdminEmail,
		"admin_pass":   args.AdminPass,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}

// openEMRKickArgs — OpenEMR is RequiresDB=true and logs in by username.
// GH #631.
type openEMRKickArgs struct {
	InstallID    string
	OSUser       string
	DocRoot      string
	Subdirectory string
	SiteURL      string
	DBName       string
	DBUser       string
	DBPassword   string
	SiteTitle    string
	AdminUser    string
	AdminEmail   string
	AdminPass    string
	UseWWW       bool
}

// createOpenEMRInstallAndKickAgent flips the install row to "installing",
// dispatches app.install with app_type="openemr" (the agent derives the
// domain from site_url), and updates the row.
func createOpenEMRInstallAndKickAgent(parentCtx context.Context, args openEMRKickArgs, cfg ApplicationHandlerConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	if err := cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "installing", nil, nil); err != nil {
		return
	}
	if cfg.Agent == nil {
		errMsg := "agent not configured"
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

	agentResp, err := cfg.Agent.Call(ctx, "app.install", map[string]any{
		"app_type":     "openemr",
		"os_user":      args.OSUser,
		"docroot":      args.DocRoot,
		"subdirectory": args.Subdirectory,
		"site_url":     args.SiteURL,
		"db_name":      args.DBName,
		"db_user":      args.DBUser,
		"db_password":  args.DBPassword,
		"db_host":      "localhost",
		"site_title":   args.SiteTitle,
		"admin_user":   args.AdminUser,
		"admin_email":  args.AdminEmail,
		"admin_pass":   args.AdminPass,
		"use_www":      args.UseWWW,
	})
	if err != nil {
		errMsg := truncateError(fmt.Sprintf("agent install failed: %v", err), 1024)
		cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "failed", &errMsg, nil)
		return
	}

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
	cfg.ApplicationInstalls.UpdateStatus(ctx, args.InstallID, "ready", nil, &version)
}
