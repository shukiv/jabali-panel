package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// PythonAppHandlerConfig wires the Python Application Manager endpoints
// (ADR-0131 / GH #203).
type PythonAppHandlerConfig struct {
	Apps       repository.PythonAppRepository
	Domains    repository.DomainRepository
	Users      repository.UserRepository
	Packages   repository.PackageRepository
	Settings   repository.ServerSettingsRepository
	Agent      agent.AgentInterface
	Reconciler interface{ Schedule(id string) }
	// Catalog is the JAB-164 framework marketplace (optional). Drives
	// GET /frameworks and the framework-driven create path.
	Catalog *pyframeworks.Catalog
}

type pythonAppHandler struct{ cfg PythonAppHandlerConfig }

// RegisterPythonAppRoutes mounts /python-apps (authenticated; owner-scoped).
// Every route is gated on the python_apps_enabled server setting.
func RegisterPythonAppRoutes(g *gin.RouterGroup, cfg PythonAppHandlerConfig) {
	h := &pythonAppHandler{cfg: cfg}
	grp := g.Group("/python-apps")
	grp.Use(h.requireEnabled)
	grp.GET("", h.list)
	grp.GET("/versions", h.versions)
	grp.GET("/frameworks", h.frameworks)
	grp.POST("", h.create)
	grp.GET("/:id", h.get)
	grp.DELETE("/:id", h.delete)
	grp.POST("/:id/control", h.control)
	grp.GET("/:id/logs", h.logs)
	grp.PUT("/:id/env", h.putEnv)
}

func (h *pythonAppHandler) requireEnabled(c *gin.Context) {
	if h.cfg.Settings != nil {
		if s, err := h.cfg.Settings.Get(c.Request.Context()); err == nil && s != nil && s.PythonAppsEnabled {
			c.Next()
			return
		}
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "python_apps_disabled"})
}

var (
	baseURIRe    = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	pyVersionRe  = regexp.MustCompile(`^3\.(?:[0-9]|1[0-9])$`)
	entrypointRe = regexp.MustCompile(`^[A-Za-z0-9_.]+:[A-Za-z0-9_]+$`)
)

// loadOwned fetches the app and enforces owner/admin scope.
func (h *pythonAppHandler) loadOwned(c *gin.Context) (*models.PythonApp, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil, false
	}
	app, err := h.cfg.Apps.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil, false
	}
	if !claims.IsAdmin && app.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	return app, true
}

func (h *pythonAppHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var apps []*models.PythonApp
	var err error
	if claims.IsAdmin {
		// Admin owner-scope via ?user_id (#483).
		if uid := c.Query("user_id"); uid != "" {
			if !ids.IsValidULID(uid) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_id"})
				return
			}
			apps, err = h.cfg.Apps.ListByUser(c.Request.Context(), uid)
		} else {
			apps, err = h.cfg.Apps.ListAll(c.Request.Context())
		}
	} else {
		apps, err = h.cfg.Apps.ListByUser(c.Request.Context(), claims.UserID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": apps})
}

func (h *pythonAppHandler) get(c *gin.Context) {
	app, ok := h.loadOwned(c)
	if !ok {
		return
	}
	env, _ := h.cfg.Apps.ListEnv(c.Request.Context(), app.ID)
	c.JSON(http.StatusOK, gin.H{"app": app, "env": env})
}

type createPythonAppRequest struct {
	DomainID      string            `json:"domain_id" binding:"required"`
	Name          string            `json:"name" binding:"required"`
	PythonVersion string            `json:"python_version" binding:"required"`
	AppRoot       string            `json:"app_root" binding:"required"`
	AppType       string            `json:"app_type"`
	Entrypoint    string            `json:"entrypoint"` // required unless Framework is set
	BaseURI       string            `json:"base_uri"`
	Env           map[string]string `json:"env,omitempty"`
	// Framework, when set, is a marketplace slug (JAB-164). It derives
	// app_type + entrypoint from the catalog and drives the one-shot scaffold.
	Framework string `json:"framework,omitempty"`
}

func (h *pythonAppHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	var req createPythonAppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	// JAB-164: a framework install derives app_type + entrypoint from the
	// catalog entry and carries the static split for the nginx alias.
	var fwStaticURL, fwStaticRoot string
	if req.Framework != "" {
		if h.cfg.Catalog == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "framework_catalog_unavailable"})
			return
		}
		fe, ok := h.cfg.Catalog.Get(req.Framework)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_framework", "detail": req.Framework})
			return
		}
		spec, serr := fe.ResolveCreate()
		if serr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "resolve_framework", "detail": serr.Error()})
			return
		}
		req.AppType = spec.AppType
		req.Entrypoint = spec.Entrypoint
		fwStaticURL, fwStaticRoot = spec.StaticURL, spec.StaticRoot
	}
	if req.AppType == "" {
		req.AppType = "wsgi"
	}
	if req.BaseURI == "" {
		req.BaseURI = "/"
	}
	if !pyVersionRe.MatchString(req.PythonVersion) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_python_version"})
		return
	}
	// GH #357: reject a version whose interpreter isn't installed on this
	// host, echoing the installed list — instead of accepting it and letting
	// the app die pending→failed at the venv step (which shells out to
	// python<ver>) with a cryptic "Unit not found" on the next Restart.
	if avail, derr := h.installedPythonVersions(c.Request.Context()); derr == nil && len(avail) > 0 && !containsStr(avail, req.PythonVersion) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":     "python_version_not_available",
			"detail":    "Python " + req.PythonVersion + " is not installed on this server",
			"available": avail,
		})
		return
	}
	if req.AppType != "wsgi" && req.AppType != "asgi" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_app_type"})
		return
	}
	if !entrypointRe.MatchString(req.Entrypoint) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_entrypoint", "detail": "expected module:callable"})
		return
	}
	if !baseURIRe.MatchString(req.BaseURI) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_base_uri"})
		return
	}

	ctx := c.Request.Context()
	domain, err := h.cfg.Domains.FindByID(ctx, req.DomainID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return
	}
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Package entitlement + per-user quota (Gitea #491). Mirrors the tenant
	// Docker path: gate only when the user has a package (no package = no
	// quota gate, same as docker). Admins bypass.
	if !claims.IsAdmin && h.cfg.Users != nil && h.cfg.Packages != nil {
		user, uerr := h.cfg.Users.FindByID(ctx, claims.UserID)
		if uerr != nil || user == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "user_lookup_failed"})
			return
		}
		if user.PackageID != nil {
			pkg, perr := h.cfg.Packages.FindByID(ctx, *user.PackageID)
			if perr != nil || pkg == nil {
				c.JSON(http.StatusForbidden, gin.H{"error": "no_package"})
				return
			}
			if pkg.MaxPythonApps == 0 {
				c.JSON(http.StatusForbidden, gin.H{"error": "python_apps_not_in_package", "detail": "your hosting package does not include Python apps"})
				return
			}
			existing, lerr := h.cfg.Apps.ListByUser(ctx, claims.UserID)
			if lerr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "quota_check_failed"})
				return
			}
			if int64(len(existing)) >= int64(pkg.MaxPythonApps) {
				c.JSON(http.StatusForbidden, gin.H{"error": "quota_exceeded", "detail": "Python app limit reached for your hosting package"})
				return
			}
		}
	}

	port, err := h.cfg.Apps.FindFreeLoopbackPort(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no_free_port"})
		return
	}

	app := &models.PythonApp{
		ID:            ulid.Make().String(),
		UserID:        domain.UserID,
		DomainID:      domain.ID,
		Name:          req.Name,
		Runtime:       "python",
		PythonVersion: req.PythonVersion,
		AppRoot:       req.AppRoot,
		AppType:       req.AppType,
		Entrypoint:    req.Entrypoint,
		BaseURI:       req.BaseURI,
		LoopbackPort:  &port,
		Framework:     req.Framework,
		Status:        models.PythonAppStatusPending,
	}
	if err := h.cfg.Apps.Create(ctx, app); err != nil {
		if isConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "mount_in_use", "detail": "another app already owns this domain + path"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if len(req.Env) > 0 {
		_ = h.cfg.Apps.ReplaceEnv(ctx, app.ID, envMapToRows(req.Env))
	}

	// Attach the nginx proxy (+ framework static_alias) to the domain.
	h.attachProxyRule(ctx, domain, app, fwStaticURL, fwStaticRoot)
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}
	c.JSON(http.StatusCreated, gin.H{"app": app})
}

func (h *pythonAppHandler) delete(c *gin.Context) {
	app, ok := h.loadOwned(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if h.cfg.Agent != nil {
		_, _ = h.cfg.Agent.Call(ctx, "app.python.remove", map[string]any{"app_id": app.ID})
	}
	// Detach the proxy rule from the domain.
	if domain, err := h.cfg.Domains.FindByID(ctx, app.DomainID); err == nil {
		h.detachProxyRule(ctx, domain, app)
		if h.cfg.Reconciler != nil {
			h.cfg.Reconciler.Schedule(domain.ID)
		}
	}
	if err := h.cfg.Apps.Delete(ctx, app.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

func (h *pythonAppHandler) control(c *gin.Context) {
	app, ok := h.loadOwned(c)
	if !ok {
		return
	}
	var req struct {
		Action string `json:"action" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "app.python.control", map[string]any{"app_id": app.ID, "action": req.Action})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

func (h *pythonAppHandler) logs(c *gin.Context) {
	app, ok := h.loadOwned(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "app.python.logs", map[string]any{"app_id": app.ID, "lines": 200})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

func (h *pythonAppHandler) putEnv(c *gin.Context) {
	app, ok := h.loadOwned(c)
	if !ok {
		return
	}
	var req struct {
		Env map[string]string `json:"env"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if err := h.cfg.Apps.ReplaceEnv(c.Request.Context(), app.ID, envMapToRows(req.Env)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(app.DomainID)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// attachProxyRule appends a proxy_pass nginx rule (base_uri -> loopback port)
// to the domain so the existing reconciler renders the vhost.
func (h *pythonAppHandler) attachProxyRule(ctx context.Context, domain *models.Domain, app *models.PythonApp, staticURL, staticRoot string) {
	rules := append(models.NginxRules{}, domain.NginxRules...)
	upsert := func(rule models.NginxRule) {
		for i := range rules {
			if rules[i].Type == rule.Type && rules[i].Path == rule.Path {
				rules[i] = rule
				return
			}
		}
		rules = append(rules, rule)
	}
	ws := true
	upsert(models.NginxRule{Type: "proxy_pass", Path: app.BaseURI, Target: proxyTargetFor(app), Websocket: &ws})
	// Framework static split (Django STATIC_ROOT): serve <base><static_url> from
	// <app_root>/<static_root>. Django prefixes STATIC_URL with SCRIPT_NAME
	// (=base_uri), so the location is base_uri + static_url.
	if staticURL != "" && staticRoot != "" {
		loc := strings.TrimSuffix(app.BaseURI, "/") + staticURL
		alias := strings.TrimSuffix(app.AppRoot, "/") + "/" + strings.Trim(staticRoot, "/") + "/"
		upsert(models.NginxRule{Type: "static_alias", Path: loc, Target: alias})
	}
	domain.NginxRules = rules
	_ = h.cfg.Domains.Update(ctx, domain)
}

// frameworkDTO is the catalog projection the UI needs — no template bytes, and
// snake_case keys consistent with the rest of the API.
type frameworkDTO struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Docs        string   `json:"docs,omitempty"`
	AppType     string   `json:"app_type"`
	Server      string   `json:"server"`
	PythonMin   string   `json:"python_min,omitempty"`
	NeedsDB     string   `json:"needs_db,omitempty"`
}

// frameworks lists the JAB-164 marketplace entries for the Catalog tab.
func (h *pythonAppHandler) frameworks(c *gin.Context) {
	out := []frameworkDTO{}
	if h.cfg.Catalog != nil {
		for _, e := range h.cfg.Catalog.All() {
			out = append(out, frameworkDTO{
				Slug: e.Slug, Name: e.Name, Version: e.Version, Description: e.Description,
				Tags: e.Tags, Icon: e.Icon, Docs: e.Docs, AppType: e.AppType,
				Server: e.Server, PythonMin: e.PythonMin, NeedsDB: e.NeedsDB,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"frameworks": out})
}

func (h *pythonAppHandler) detachProxyRule(ctx context.Context, domain *models.Domain, app *models.PythonApp) {
	target := proxyTargetFor(app)
	aliasPrefix := strings.TrimSuffix(app.AppRoot, "/") + "/"
	var kept models.NginxRules
	for _, r := range domain.NginxRules {
		if r.Type == "proxy_pass" && r.Target == target {
			continue
		}
		// Drop the framework static_alias that points into this app's tree.
		if r.Type == "static_alias" && app.AppRoot != "" && strings.HasPrefix(r.Target, aliasPrefix) {
			continue
		}
		kept = append(kept, r)
	}
	domain.NginxRules = kept
	_ = h.cfg.Domains.Update(ctx, domain)
}

func proxyTargetFor(app *models.PythonApp) string {
	port := 0
	if app.LoopbackPort != nil {
		port = *app.LoopbackPort
	}
	return "http://127.0.0.1:" + itoaPort(port)
}

func itoaPort(p int) string {
	return strings.TrimSpace(intToStr(p))
}

func envMapToRows(m map[string]string) []models.PythonAppEnv {
	rows := make([]models.PythonAppEnv, 0, len(m))
	for k, v := range m {
		rows = append(rows, models.PythonAppEnv{ID: ulid.Make().String(), Key: k, Value: v})
	}
	return rows
}

// versions lists the Python interpreters installed on this host so the UI
// only ever offers a version that will actually build (GH #357).
func (h *pythonAppHandler) versions(c *gin.Context) {
	if h.cfg.Agent == nil {
		c.JSON(http.StatusOK, gin.H{"versions": []string{}, "default": ""})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "app.python.versions", nil)
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

// installedPythonVersions asks the agent which python3.X interpreters exist.
// Fail-open: on any agent/probe error the caller skips the create-time gate
// rather than blocking creation when the probe is unavailable.
func (h *pythonAppHandler) installedPythonVersions(ctx context.Context) ([]string, error) {
	if h.cfg.Agent == nil {
		return nil, nil
	}
	raw, err := h.cfg.Agent.Call(ctx, "app.python.versions", nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Versions, nil
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
