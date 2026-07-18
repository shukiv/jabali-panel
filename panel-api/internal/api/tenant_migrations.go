package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/wordpressplugin"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/wordpressssh"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// TenantMigrationsConfig wires the tenant-facing WordPress migration surface.
// EVERY endpoint is owner-scoped: a tenant may only migrate INTO a domain they
// own, target_user_id is always forced to the authenticated caller (never taken
// from the request), and only the WordPress source kinds are allowed (panel-
// account migrations stay admin-only).
type TenantMigrationsConfig struct {
	Jobs     repository.MigrationJobRepository
	Domains  repository.DomainRepository
	Users    repository.UserRepository
	Agent    agent.AgentInterface
	Settings repository.ServerSettingsRepository
}

type tenantMigrationsHandler struct{ cfg TenantMigrationsConfig }

// RegisterTenantMigrationRoutes mounts /migrations/* for authenticated tenants.
func RegisterTenantMigrationRoutes(rg *gin.RouterGroup, cfg TenantMigrationsConfig) {
	if cfg.Jobs == nil || cfg.Domains == nil || cfg.Users == nil || cfg.Agent == nil {
		return
	}
	h := &tenantMigrationsHandler{cfg: cfg}
	g := rg.Group("/migrations")
	g.POST("/wordpress", h.create)
	g.POST("/:id/secrets", h.uploadSecrets)
	g.POST("/:id/pull-source", h.pull)
	g.POST("/:id/verify", h.verify)
	g.POST("/:id/scan-wp", h.scanWP)
	g.POST("/:id/source-path", h.setSourcePath)
	g.POST("/:id/import-wp", h.importWP)
	g.GET("/:id", h.get)
}

// caller returns the authenticated user's id, or "" + writes 401.
func (h *tenantMigrationsHandler) caller(c *gin.Context) string {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return ""
	}
	return claims.UserID
}

// ownedJob loads the :id job and confirms the caller owns it (target_user_id).
// A job the caller does not own returns 404 (not 403) so existence never leaks.
func (h *tenantMigrationsHandler) ownedJob(c *gin.Context, uid string) *models.MigrationJob {
	job, err := h.cfg.Jobs.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil
	}
	if job.TargetUserID == nil || *job.TargetUserID != uid {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil
	}
	return job
}

// ownedDomain confirms the caller owns dest domain `name`; returns it or writes
// the error response.
func (h *tenantMigrationsHandler) ownedDomain(c *gin.Context, uid, name string) *models.Domain {
	dom, err := h.cfg.Domains.FindByName(c.Request.Context(), name)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil
		}
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil
	}
	if dom.UserID != uid {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return nil
	}
	return dom
}

type tenantCreateWPRequest struct {
	SourceKind string `json:"source_kind"`
	SourceHost string `json:"source_host"`
	SourceUser string `json:"source_user"`
	SourcePath string `json:"source_path"`
	DestDomain string `json:"dest_domain"`
}

// normalizePluginURL canonicalizes a jabali-migrator source URL so trailing
// slashes and an accidentally-pasted REST path don't spawn duplicate jobs
// (the migration natural key is source_host). GH #648.
func normalizePluginURL(u string) string {
	u = strings.TrimSpace(u)
	if i := strings.Index(u, "/wp-json/"); i >= 0 {
		u = u[:i]
	}
	return strings.TrimRight(u, "/")
}

// create — a tenant creates a WordPress migration INTO a domain they own.
func (h *tenantMigrationsHandler) create(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	var req tenantCreateWPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	// Only WordPress source kinds — tenants can't run panel-account migrations.
	if req.SourceKind != models.MigrationSourceWordPressSSH &&
		req.SourceKind != models.MigrationSourceWordPressPlugin {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_kind", "detail": "tenant migrations support wordpress_ssh or wordpress_plugin"})
		return
	}
	if strings.TrimSpace(req.SourceHost) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_fields", "detail": "source_host required"})
		return
	}
	// Dest domain is OPTIONAL: if given, the caller must own it; if blank, the
	// pull auto-detects the source site's domain and creates it (GH #647).
	destDomain := strings.TrimSpace(req.DestDomain)
	if destDomain != "" && h.ownedDomain(c, uid, destDomain) == nil {
		return
	}
	// Caller's OS username = the forced import destination user.
	user, err := h.cfg.Users.FindByID(c.Request.Context(), uid)
	if err != nil || user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "user_lookup"})
		return
	}
	sourceUser := strings.TrimSpace(req.SourceUser)
	if sourceUser == "" {
		sourceUser = "wp" // unused for wordpress_plugin
	}
	forcedUID := uid // target is ALWAYS the caller — never from the request
	destUser := *user.Username
	srcHost := strings.TrimSpace(req.SourceHost)
	if req.SourceKind == models.MigrationSourceWordPressPlugin {
		srcHost = normalizePluginURL(srcHost) // strip trailing slash + REST path (dedup)
	}
	row := &models.MigrationJob{
		ID:           genULID(),
		SourceKind:   req.SourceKind,
		SourceHost:   srcHost,
		SourceUser:   sourceUser,
		TargetUserID: &forcedUID,
		DestUser:     &destUser, // set -> pull auto-imports (background job)
		State:        models.MigrationStatePending,
		StartedAt:    time.Now().UTC(),
	}
	if destDomain != "" {
		row.DestDomain = &destDomain // else the pull auto-derives + creates it
	}
	if err := h.cfg.Jobs.Create(c.Request.Context(), row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
		return
	}
	if sp := strings.TrimSpace(req.SourcePath); sp != "" {
		_ = h.cfg.Jobs.UpdateSourcePath(c.Request.Context(), row.ID, sp)
	}
	c.JSON(http.StatusCreated, gin.H{"id": row.ID, "source_kind": row.SourceKind, "state": row.State})
}

type tenantSecretsRequest struct {
	SSHPassword   string `json:"ssh_password"`
	SSHPrivateKey string `json:"ssh_private_key"`
	PluginToken   string `json:"plugin_token"`
}

func (h *tenantMigrationsHandler) uploadSecrets(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	if h.ownedJob(c, uid) == nil {
		return
	}
	var req tenantSecretsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if req.SSHPassword == "" && req.SSHPrivateKey == "" && req.PluginToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_credential"})
		return
	}
	h.callAgent(c, "migration.secrets_write", map[string]any{
		"job_id":          c.Param("id"),
		"ssh_password":    req.SSHPassword,
		"ssh_private_key": req.SSHPrivateKey,
		"plugin_token":    req.PluginToken,
	})
}

type tenantPullRequest struct {
	SSHUser string `json:"ssh_user"`
}

func (h *tenantMigrationsHandler) pull(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	if h.ownedJob(c, uid) == nil {
		return
	}
	var req tenantPullRequest
	_ = c.ShouldBindJSON(&req)
	h.callAgent(c, "migration.pull_source_run", map[string]any{
		"job_id":   c.Param("id"),
		"ssh_user": req.SSHUser,
	})
}

type tenantImportRequest struct {
	DestDomain string `json:"dest_domain"`
}

// importWP — dest_user is FORCED to the caller's own OS username and the dest
// domain must be one the caller owns. Neither is taken on trust from the body.
func (h *tenantMigrationsHandler) importWP(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	if h.ownedJob(c, uid) == nil {
		return
	}
	var req tenantImportRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.DestDomain) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dest_domain required"})
		return
	}
	if h.ownedDomain(c, uid, req.DestDomain) == nil {
		return
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), uid)
	if err != nil || user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "user_lookup"})
		return
	}
	h.callAgent(c, "migration.import_wp_run", map[string]any{
		"job_id":      c.Param("id"),
		"dest_user":   *user.Username, // FORCED — the caller's own OS user
		"dest_domain": req.DestDomain,
	})
}

func (h *tenantMigrationsHandler) get(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	job := h.ownedJob(c, uid)
	if job == nil {
		return
	}
	c.JSON(http.StatusOK, job)
}

// scanWP SSH-connects to the source and lists WordPress installs (GH #647) so
// the tenant can pick which one to migrate. Requires the job's SSH secret to be
// uploaded first. Owner-scoped; wordpress_ssh only.
func (h *tenantMigrationsHandler) scanWP(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	job := h.ownedJob(c, uid)
	if job == nil {
		return
	}
	if job.SourceKind != models.MigrationSourceWordPressSSH {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scan is SSH-only"})
		return
	}
	allowPrivate := false
	if h.cfg.Settings != nil {
		if st, err := h.cfg.Settings.Get(c.Request.Context()); err == nil && st != nil {
			allowPrivate = st.MigrationAllowPrivateHosts
		}
	}
	sshUser := job.SourceUser
	if sshUser == "" || sshUser == "wp" {
		sshUser = "root"
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	secret := migrate.SecretRef{Path: filepath.Join(migrate.SecretsDir, job.ID+".env")}
	sess, err := wordpressssh.Connect(ctx, job.SourceHost, 0, sshUser, secret, allowPrivate)
	if err != nil {
		respondMigrateConnectErr(c, err)
		return
	}
	defer sess.Close()
	installs, err := wordpressssh.ScanWordPress(ctx, sess)
	if err != nil {
		respondAgentErr(c, "scan_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"installs": installs})
}

// setSourcePath records which scanned WP install the tenant picked.
func (h *tenantMigrationsHandler) setSourcePath(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	if h.ownedJob(c, uid) == nil {
		return
	}
	var req struct {
		SourcePath string `json:"source_path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.SourcePath) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_path required"})
		return
	}
	if err := h.cfg.Jobs.UpdateSourcePath(c.Request.Context(), c.Param("id"), strings.TrimSpace(req.SourcePath)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// verify does a pre-flight HANDSHAKE against the source before the migration
// starts: plugin -> ping (reachable + token + version) + manifest; ssh -> connect
// + discover. Returns the discovered facts, or an error the wizard shows so the
// operator fixes it (wrong token, unreachable, an outdated plugin) before Start.
func (h *tenantMigrationsHandler) verify(c *gin.Context) {
	uid := h.caller(c)
	if uid == "" {
		return
	}
	job := h.ownedJob(c, uid)
	if job == nil {
		return
	}
	allowPrivate := false
	if h.cfg.Settings != nil {
		if st, err := h.cfg.Settings.Get(c.Request.Context()); err == nil && st != nil {
			allowPrivate = st.MigrationAllowPrivateHosts
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	secretPath := filepath.Join(migrate.SecretsDir, job.ID+".env")

	if job.SourceKind == models.MigrationSourceWordPressPlugin {
		token := readSecretValue(secretPath, "PLUGIN_TOKEN")
		if token == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no_token", "detail": "upload the migration token first"})
			return
		}
		siteURL := job.SourceHost
		if !strings.HasPrefix(siteURL, "http://") && !strings.HasPrefix(siteURL, "https://") {
			siteURL = "https://" + siteURL
		}
		cli := wordpressplugin.New(siteURL, token, allowPrivate)
		ping, err := cli.PingInfo(ctx)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "handshake_failed", "detail": "cannot reach the plugin — check the site URL and token: " + err.Error()})
			return
		}
		facts, err := cli.Manifest(ctx)
		if err != nil {
			respondAgentErr(c, "manifest_failed", err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"ok": true, "kind": "plugin",
			"plugin_version": ping.Version,
			"needs_update":   ping.Version < "0.1.2", // db-export pure-PHP fallback landed in 0.1.2
			"siteurl":        facts.SiteURL, "wp_version": facts.WPVersion,
			"file_count": facts.FileCount, "db_bytes": facts.DBBytes,
		})
		return
	}

	// wordpress_ssh
	sshUser := job.SourceUser
	if sshUser == "" || sshUser == "wp" {
		sshUser = "root"
	}
	sess, err := wordpressssh.Connect(ctx, job.SourceHost, 0, sshUser, migrate.SecretRef{Path: secretPath}, allowPrivate)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "connect_failed", "detail": "SSH connect failed — check host + credentials: " + err.Error()})
		return
	}
	defer sess.Close()
	hint := ""
	if job.SourcePath != nil {
		hint = *job.SourcePath
	}
	facts, err := wordpressssh.DiscoverWordPress(ctx, sess, hint)
	if err != nil {
		respondAgentErr(c, "discover_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true, "kind": "ssh",
		"siteurl": facts.SiteURL, "wp_version": facts.WPVersion,
		"table_prefix": facts.TablePrefix, "db_bytes": facts.BytesTotal, "wp_cli": facts.WPCLI,
	})
}

// readSecretValue pulls one KEY=value from a migration secret file.
func readSecretValue(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

// callAgent invokes an agent verb and maps the result to a JSON response.
func (h *tenantMigrationsHandler) callAgent(c *gin.Context, verb string, params map[string]any) {
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unconfigured"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if _, err := h.cfg.Agent.Call(ctx, verb, params); err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
