// docker_apps_user.go — TENANT (user-shell) docker-app surface (M49, GH #170).
//
// Mirrors the admin handler but constrained: only tenant_installable catalog
// entries, loopback-only ports (no public bind, no host-port pinning), owner-
// scoped reads, package-quota + domain-ownership gated, and every install gets
// the hardening profile (cap_drop ALL + tenant_caps + no-new-privileges +
// cgroup_parent under the caller's M18 slice) plus the agent compose-config
// safety gate. No exec, no edit-compose (ADR-0117 Decision 8).
//
// Routes (auth-only base group, subject-scoped):
//
//	GET    /docker-apps/catalog              tenant_installable subset
//	GET    /docker-apps/catalog/:slug/icon
//	GET    /docker-apps                       caller's installs
//	GET    /docker-apps/:id                   own only (404 otherwise)
//	POST   /docker-apps                       install (gated)
//	DELETE /docker-apps/:id                   own only
//	POST   /docker-apps/:id/(start|stop|restart)
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// defaultTenantDockerFlag is the host flag written by `jabali docker
// enable-tenant`; its presence means userns-remap is live and tenant docker is
// permitted on this host. Overridable in tests.
const defaultTenantDockerFlag = "/etc/jabali/docker-tenant-enabled"

// UserDockerAppHandlerConfig bundles tenant-handler dependencies.
type UserDockerAppHandlerConfig struct {
	Repo           repository.DockerAppRepository
	Catalog        *dockerapp.Catalog
	Domains        repository.DomainRepository
	Agent          agent.AgentInterface
	Users          repository.UserRepository
	Packages       repository.PackageRepository
	ServerSettings repository.ServerSettingsRepository
	Log            *slog.Logger
	// TenantFlagPath gates the whole surface. Empty = the production default.
	TenantFlagPath string
}

type userDockerAppHandler struct {
	cfg   UserDockerAppHandlerConfig
	admin *dockerAppHandler // for resolvePorts reuse only (loopback allocation)
}

// RegisterUserDockerAppRoutes mounts the tenant docker-app surface.
func RegisterUserDockerAppRoutes(g *gin.RouterGroup, cfg UserDockerAppHandlerConfig) {
	if cfg.Repo == nil || cfg.Catalog == nil {
		return
	}
	if cfg.TenantFlagPath == "" {
		cfg.TenantFlagPath = defaultTenantDockerFlag
	}
	h := &userDockerAppHandler{
		cfg:   cfg,
		admin: &dockerAppHandler{cfg: DockerAppHandlerConfig{Repo: cfg.Repo, Agent: cfg.Agent, Catalog: cfg.Catalog, Users: cfg.Users, Domains: cfg.Domains}},
	}
	grp := g.Group("/docker-apps")
	grp.Use(h.requireTenantDockerEnabled)
	grp.GET("/catalog", h.listCatalog)
	grp.GET("/catalog/:slug/icon", h.catalogIcon)
	grp.GET("", h.list)
	grp.GET("/:id", h.get)
	grp.POST("", h.install)
	grp.DELETE("/:id", h.delete)
	grp.POST("/:id/start", h.lifecycle(models.DockerAppStatusRunning, "start"))
	grp.POST("/:id/stop", h.lifecycle(models.DockerAppStatusStopped, "stop"))
	grp.POST("/:id/restart", h.lifecycle(models.DockerAppStatusRunning, "restart"))
	grp.GET("/:id/logs", h.logs)
	grp.GET("/:id/env", h.getEnv)
	grp.GET("/usage", h.usage)
}

// requireTenantDockerEnabled gates every verb on the host flag. No userns-remap
// (no flag) → tenant docker is off, full stop.
func (h *userDockerAppHandler) requireTenantDockerEnabled(c *gin.Context) {
	if _, err := os.Stat(h.cfg.TenantFlagPath); err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "docker_tenant_not_enabled"})
		return
	}
	if ginctx.Claims(c) == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.Next()
}

// tenantInstallable reports whether a catalog entry may be exposed to tenants:
// the flag is set AND it declares no default-public port (loopback-only rule).
func tenantInstallable(e dockerapp.Entry) bool {
	if !e.TenantInstallable {
		return false
	}
	for _, p := range e.Ports {
		if p.DefaultBind == "public" {
			return false
		}
	}
	return true
}

// dockerTenantSelection returns the admin-exposed slug set and whether the
// selection is "all eligible" (empty CSV = the curated default, expose all).
func (h *userDockerAppHandler) dockerTenantSelection(ctx context.Context) (map[string]bool, bool) {
	if h.cfg.ServerSettings == nil {
		return nil, true
	}
	cfg, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.DockerTenantApps) == "" {
		return nil, true
	}
	set := map[string]bool{}
	for _, sl := range strings.Split(cfg.DockerTenantApps, ",") {
		if v := strings.TrimSpace(sl); v != "" {
			set[v] = true
		}
	}
	return set, false
}

// tenantExposed reports whether a catalog slug is exposed to tenants given the
// admin selection (empty selection = all eligible).
func (h *userDockerAppHandler) tenantExposed(ctx context.Context, slug string) bool {
	set, all := h.dockerTenantSelection(ctx)
	return all || set[slug]
}

// tenantAllowSet returns the effective tenant-installable slug set for a user
// (GH #170 #3): their hosting package's docker_app_slugs allowlist when set,
// else the server-wide docker_tenant_apps curation. all=true = every eligible
// app. Always still AND-ed with tenant_installable + MaxDockerApps by callers.
func (h *userDockerAppHandler) tenantAllowSet(ctx context.Context, userID string) (map[string]bool, bool) {
	if h.cfg.Users != nil && h.cfg.Packages != nil {
		if u, err := h.cfg.Users.FindByID(ctx, userID); err == nil && u != nil && u.PackageID != nil {
			if pkg, perr := h.cfg.Packages.FindByID(ctx, *u.PackageID); perr == nil && pkg != nil {
				if csv := strings.TrimSpace(pkg.DockerAppSlugs); csv != "" {
					set := map[string]bool{}
					for _, sl := range strings.Split(csv, ",") {
						if v := strings.TrimSpace(sl); v != "" {
							set[v] = true
						}
					}
					return set, false
				}
			}
		}
	}
	return h.dockerTenantSelection(ctx)
}

func (h *userDockerAppHandler) listCatalog(c *gin.Context) {
	claims := ginctx.Claims(c)
	out := make([]catalogEntryResponse, 0)
	set, all := h.tenantAllowSet(c.Request.Context(), claims.UserID)
	for _, e := range h.cfg.Catalog.All() {
		if tenantInstallable(e) && (all || set[e.Slug]) {
			out = append(out, catalogEntryToResponse(e))
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (h *userDockerAppHandler) catalogIcon(c *gin.Context) {
	e, ok := h.cfg.Catalog.Get(c.Param("slug"))
	if !ok || !tenantInstallable(e) || !h.tenantExposed(c.Request.Context(), e.Slug) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	name := e.Icon
	if name == "" {
		name = "icon.svg"
	}
	if strings.ContainsAny(name, "/\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_icon"})
		return
	}
	// Theme-aware variant selection (see resolveThemedIcon in docker_apps.go).
	name = resolveThemedIcon(e.Dir(), name, c.Query("theme"))
	body, err := os.ReadFile(filepath.Join(e.Dir(), name))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	ctype := "image/svg+xml"
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		ctype = "image/png"
	case ".jpg", ".jpeg":
		ctype = "image/jpeg"
	case ".webp":
		ctype = "image/webp"
	}
	c.Data(http.StatusOK, ctype, body)
}

func (h *userDockerAppHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	apps, err := h.cfg.Repo.ListByUserID(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	// Resolve the attached domain per app (the domain back-references the app
	// via docker_app_id) so the "Your apps" table can show it — the DockerApp
	// row itself carries no domain (GH #284: showed "—" though the bind exists).
	var domList []models.Domain
	if h.cfg.Domains != nil {
		domList, _, _ = h.cfg.Domains.ListByUserID(c.Request.Context(), claims.UserID, repository.ListOptions{Limit: diskUsageListLimit})
	}
	out := make([]installedResponse, 0, len(apps))
	for _, a := range apps {
		ports, _ := h.cfg.Repo.ListPortsForApp(c.Request.Context(), a.ID)
		resp := installedResponse{DockerApp: *a, Ports: ports}
		for i := range domList {
			if domList[i].DockerAppID != nil && *domList[i].DockerAppID == a.ID {
				resp.Domain = domList[i].Name
				break
			}
		}
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// loadOwned fetches an app scoped to the caller; writes 404 on a miss (no
// cross-tenant existence leak) and returns nil.
func (h *userDockerAppHandler) loadOwned(c *gin.Context) *models.DockerApp {
	claims := ginctx.Claims(c)
	app, err := h.cfg.Repo.FindByIDForUser(c.Request.Context(), c.Param("id"), claims.UserID)
	if err != nil || app == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return nil
	}
	return app
}

func (h *userDockerAppHandler) get(c *gin.Context) {
	app := h.loadOwned(c)
	if app == nil {
		return
	}
	ports, _ := h.cfg.Repo.ListPortsForApp(c.Request.Context(), app.ID)
	c.JSON(http.StatusOK, installedResponse{DockerApp: *app, Ports: ports})
}

func (h *userDockerAppHandler) lifecycle(statusOnSuccess string, composeArgs ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		app := h.loadOwned(c)
		if app == nil {
			return
		}
		if h.cfg.Agent != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
			defer cancel()
			params := map[string]any{"slug": app.EffectiveSlug()}
			// Bring-up lifecycle (start/restart) re-runs `compose up` from the
			// on-disk compose — make the agent re-validate the tenant gate first
			// (Gitea #513), the same as install/update/restore.
			if statusOnSuccess == models.DockerAppStatusRunning {
				if entry, ok := h.cfg.Catalog.Get(app.Slug); ok {
					params["tenant_validate"] = true
					params["tenant_caps"] = dockerapp.TenantCapAllowlist(entry.TenantCaps)
				}
			}
			if _, err := h.cfg.Agent.Call(ctx, "docker_app."+composeArgs[0], params); err != nil {
				respondAgentErr(c, "agent_error", err)
				return
			}
		}
		_ = h.cfg.Repo.UpdateStatus(c.Request.Context(), app.ID, statusOnSuccess, nil)
		c.JSON(http.StatusOK, gin.H{"id": app.ID, "status": statusOnSuccess})
	}
}

func (h *userDockerAppHandler) delete(c *gin.Context) {
	app := h.loadOwned(c)
	if app == nil {
		return
	}
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if h.cfg.Agent != nil {
		actx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		// Best-effort teardown, purging the on-disk install dir too (tenant
		// installs are per-instance, never reused). A genuine teardown
		// failure is logged but must not strand the row — a tenant has no
		// force/admin escape hatch, so the soft-delete proceeds regardless.
		if _, err := h.cfg.Agent.Call(actx, "docker_app.delete", map[string]any{
			"slug":          app.EffectiveSlug(),
			"purge_volumes": true,
		}); err != nil {
			// Gitea #528: a soft-delete here would hide a workload that may still
			// be running + consuming resources/network. Keep the row VISIBLE in a
			// failed state so it stays in quota/disk/entitlement accounting and
			// the tenant can retry — don't mark it deleted on teardown failure.
			msg := "teardown failed: " + firstLineString(err.Error())
			_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg)
			slog.Warn("tenant docker delete: agent teardown failed, keeping row visible",
				"app_id", app.ID, "slug", app.EffectiveSlug(), "err", err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "teardown_failed", "detail": msg})
			return
		}
	}

	// Clean up the attached domain exactly as the admin delete does, but
	// scoped to the caller's OWN domains (no cross-tenant reach). Without
	// this the soft-deleted app leaves a dangling docker_app_id and a
	// proxy_pass rule still pointing at the now-dead container's port, so
	// the hostname keeps 502-ing and the link can never be reused (GH #284).
	if h.cfg.Domains != nil {
		domList, _, _ := h.cfg.Domains.ListByUserID(ctx, claims.UserID, repository.ListOptions{Limit: diskUsageListLimit})
		for i := range domList {
			dom := domList[i]
			if dom.DockerAppID == nil || *dom.DockerAppID != app.ID {
				continue
			}
			if dom.ManagedBy == models.DomainManagedByDockerApp {
				// Hostname we auto-created for this app at install time —
				// remove the row and tear down its proxy vhost.
				domName := dom.Name
				_ = h.cfg.Domains.Delete(ctx, dom.ID)
				if h.cfg.Agent != nil && domName != "" {
					rmCtx, rmCancel := context.WithTimeout(ctx, 30*time.Second)
					_, _ = h.cfg.Agent.Call(rmCtx, "docker_app.vhost_remove",
						map[string]string{"domain_name": domName})
					rmCancel()
				}
			} else {
				// The tenant's own pre-existing domain — keep it, just
				// detach the app link and clear the injected proxy_pass rule.
				_ = h.cfg.Domains.DetachDockerApp(ctx, dom.ID, true)
			}
		}
	}

	_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusDeleted, nil)
	c.JSON(http.StatusOK, gin.H{"id": app.ID, "status": "deleted"})
}

// ---- install ----------------------------------------------------------------

func (h *userDockerAppHandler) install(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)

	var req installRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !nameRE.MatchString(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name", "detail": "must match ^[a-z0-9-]{1,32}$"})
		return
	}
	if strings.TrimSpace(req.Domain) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain_required", "detail": "a tenant docker app must be attached to a domain you own"})
		return
	}

	entry, ok := h.cfg.Catalog.Get(req.Slug)
	allowSet, allowAll := h.tenantAllowSet(c.Request.Context(), claims.UserID)
	if !ok || !tenantInstallable(entry) || !(allowAll || allowSet[req.Slug]) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_slug", "detail": "not installable on your hosting package"})
		return
	}

	// Resolve the caller. A user MUST have a Linux account AND a hosting
	// package that includes Docker apps. Tenant Docker is a privileged feature,
	// so — unlike the GH #282 "no package = unrestricted" default for ordinary
	// resources — a package-less tenant is DENIED here (Gitea #511); without it
	// a no-package account would skip the MaxDockerApps count, the docker-data
	// disk gate, and the CPU/mem/PID clamps. (Admins are panel-only with no
	// Linux account, so they never reach this tenant route.)
	user, err := h.cfg.Users.FindByID(ctx, claims.UserID)
	if err != nil || user == nil || user.Username == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "no_account", "detail": "account has no Linux user"})
		return
	}
	if user.PackageID == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "docker_apps_require_package", "detail": "Docker apps require a hosting package that includes them"})
		return
	}
	var pkg *models.HostingPackage
	if user.PackageID != nil {
		p, perr := h.cfg.Packages.FindByID(ctx, *user.PackageID)
		if perr != nil || p == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "no_package"})
			return
		}
		pkg = p
		if pkg.MaxDockerApps == 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "docker_apps_not_in_package", "detail": "your hosting package does not include Docker apps"})
			return
		}
		count, cerr := h.cfg.Repo.CountByUserID(ctx, claims.UserID)
		if cerr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "quota_check_failed"})
			return
		}
		if count >= int64(pkg.MaxDockerApps) {
			c.JSON(http.StatusConflict, gin.H{"error": "docker_app_quota_exceeded", "detail": "you have reached your Docker app limit"})
			return
		}
		// Disk gate (Gitea #489): tenant docker-app data lives under
		// /var/lib/jabali/docker-apps, outside the home POSIX quota, so block a
		// NEW install once the tenant's docker footprint already meets/exceeds
		// the package disk allowance. (A hard runtime cap needs fs project
		// quotas — tracked separately; this makes the gate enforcing, not
		// advisory.) DiskQuotaMB==0 = unlimited.
		if pkg.DiskQuotaMB > 0 {
			used, derr := h.cfg.Repo.SumDataBytesByUserID(ctx, claims.UserID)
			if derr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "disk_check_failed"})
				return
			}
			if used >= int64(pkg.DiskQuotaMB)*1024*1024 {
				c.JSON(http.StatusConflict, gin.H{"error": "disk_quota_exceeded", "detail": "your Docker app disk usage has reached your hosting package quota"})
				return
			}
		}
	}

	// Domain ownership: must be a domain the caller owns, or a free hostname
	// we create under their ownership. Never another user's domain.
	if dom, _ := h.cfg.Domains.FindByName(ctx, req.Domain); dom != nil && dom.UserID != claims.UserID {
		c.JSON(http.StatusConflict, gin.H{"error": "domain_in_use", "detail": "domain belongs to another user"})
		return
	}

	username := *user.Username
	instanceSlug := tenantInstanceSlug(req.Slug, claims.UserID, req.Name)

	// Per-app limits, clamped to the package memory ceiling.
	mem := strings.TrimSpace(req.Memory)
	if mem == "" {
		mem = entry.Resources.Memory
	}
	cpu := strings.TrimSpace(req.CPULimit)
	if cpu == "" {
		cpu = entry.Resources.CPU
	}
	pids := entry.Resources.PIDs
	if req.PIDsLimit != nil {
		pids = *req.PIDsLimit
	}

	// Clamp per-app limits to the hosting package ceilings (#488): a tenant
	// must not be able to request more CPU / memory / PIDs than their plan's
	// per-user budget. A package ceiling of 0 means "unlimited" -> no clamp.
	if pkg != nil {
		mem = clampDockerMemory(mem, pkg.MemoryLimitMB)
		cpu = clampDockerCPU(cpu, pkg.CPUQuotaPercent)
		pids = clampPIDs(pids, pkg.MaxTasks)
	}

	app := &models.DockerApp{
		ID:             ulid.Make().String(),
		UserID:         &claims.UserID,
		Slug:           req.Slug,
		InstanceSlug:   instanceSlug,
		Name:           req.Name,
		CatalogVersion: entry.Version,
		Status:         models.DockerAppStatusPending,
		UpdateMode:     models.DockerAppUpdateModeManual,
	}
	if cpu != "" {
		app.CPULimit = &cpu
	}
	if mem != "" {
		app.MemoryLimit = &mem
	}
	if pids > 0 {
		app.PIDsLimit = &pids
	}
	if err := h.cfg.Repo.Create(ctx, app); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed", "detail": err.Error()})
		return
	}

	// Loopback-only ports: empty override list -> catalog defaults, which for
	// a tenant_installable app are all loopback+reverse_proxy (enforced by the
	// tenantInstallable filter rejecting any public-default port).
	resolvedPorts, runtimePorts, err := h.admin.resolvePorts(ctx, entry, nil, app.ID)
	if err != nil {
		_ = h.cfg.Repo.Delete(ctx, app.ID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "port_resolution_failed", "detail": err.Error()})
		return
	}
	for _, p := range resolvedPorts {
		if err := h.cfg.Repo.CreatePort(ctx, p); err != nil {
			_ = h.cfg.Repo.Delete(ctx, app.ID)
			c.JSON(http.StatusConflict, gin.H{"error": "port_persist_failed", "detail": err.Error()})
			return
		}
	}

	// Attach / create the domain under the caller's ownership.
	var loopbackPort *models.DockerAppPublishedPort
	for _, p := range resolvedPorts {
		if p.ReverseProxy && p.Protocol == "tcp" {
			loopbackPort = p
			break
		}
	}
	if h.cfg.Domains != nil && loopbackPort != nil {
		truePtr := true
		rules := models.NginxRules{{Type: "proxy_pass", Path: "/", Target: "http://127.0.0.1:" + intToStr(loopbackPort.HostPort), Websocket: &truePtr}}
		if existing, _ := h.cfg.Domains.FindByName(ctx, req.Domain); existing != nil {
			if aerr := h.cfg.Domains.AttachDockerApp(ctx, existing.ID, app.ID, rules); aerr != nil {
				h.failInstall(c, app.ID, "domain_attach_failed", aerr)
				return
			}
		} else {
			dom := &models.Domain{
				ID: ulid.Make().String(), UserID: claims.UserID, Name: req.Domain,
				IsEnabled: true, SSLEnabled: true, NginxRules: rules,
				ManagedBy: models.DomainManagedByDockerApp, DockerAppID: &app.ID,
			}
			if derr := h.cfg.Domains.Create(ctx, dom); derr != nil {
				h.failInstall(c, app.ID, "domain_create_failed", derr)
				return
			}
		}
	}

	// Validate tenant-supplied env overrides the same way the env-edit path
	// does (Gitea #517) — the install path accepted them unchecked.
	for k, v := range req.EnvOverride {
		if msg := validateEnvKV(k, v); msg != "" {
			_ = h.cfg.Repo.Delete(ctx, app.ID)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_env", "detail": msg})
			return
		}
	}
	envMap, err := dockerapp.MaterialiseEnv(entry, req.EnvOverride)
	if err != nil {
		_ = h.cfg.Repo.Delete(ctx, app.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "env_materialise_failed", "detail": err.Error()})
		return
	}

	composeYML, err := dockerapp.Render(entry, dockerapp.RenderParams{
		Slug:         instanceSlug,
		Name:         req.Name,
		Domain:       req.Domain,
		ImageChannel: entry.ImageChannel,
		DataRoot:     "/var/lib/jabali/docker-apps/" + instanceSlug,
		CPULimit:     cpu,
		MemoryLimit:  mem,
		Ports:        runtimePorts,
		Env:          envMap,
		TenantHardening: &dockerapp.TenantHardening{
			CgroupParent: "jabali-user-" + username + ".slice",
			Caps:         dockerapp.TenantCapAllowlist(entry.TenantCaps),
			PIDsLimit:    pids,
		},
	})
	if err != nil {
		_ = h.cfg.Repo.Delete(ctx, app.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render_failed", "detail": err.Error()})
		return
	}

	if h.cfg.Agent != nil {
		_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusInstalling, nil)
		volumeNames := make([]string, 0, len(entry.Volumes))
		for _, v := range entry.Volumes {
			volumeNames = append(volumeNames, v.Name)
		}
		installParams := map[string]any{
			"slug":                        instanceSlug,
			"compose_yml":                 composeYML,
			"env_file":                    buildEnvFile(envMap),
			"volumes":                     volumeNames,
			"volume_owner":                entry.VolumeOwner,
			"wait_healthy":                true,
			"healthcheck_timeout_seconds": 300,
			// M49: agent safety gate before `up`.
			"tenant_validate": true,
			"tenant_caps":     dockerapp.TenantCapAllowlist(entry.TenantCaps),
		}
		appID := app.ID
		go func() {
			bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
			defer cancel()
			_, agentErr := h.cfg.Agent.Call(bgCtx, "docker_app.install", installParams)
			persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer persistCancel()
			if agentErr != nil {
				msg := firstLineString(agentErr.Error())
				_ = h.cfg.Repo.UpdateStatus(persistCtx, appID, models.DockerAppStatusFailed, &msg)
				return
			}
			_ = h.cfg.Repo.UpdateStatus(persistCtx, appID, models.DockerAppStatusRunning, nil)
		}()
	}

	fresh, _ := h.cfg.Repo.FindByID(ctx, app.ID)
	ports, _ := h.cfg.Repo.ListPortsForApp(ctx, app.ID)
	if fresh != nil {
		c.JSON(http.StatusCreated, installedResponse{DockerApp: *fresh, Ports: ports, Domain: req.Domain})
		return
	}
	c.JSON(http.StatusCreated, installedResponse{DockerApp: *app, Ports: ports, Domain: req.Domain})
}

func (h *userDockerAppHandler) failInstall(c *gin.Context, appID, code string, err error) {
	msg := firstLineString(err.Error())
	_ = h.cfg.Repo.UpdateStatus(c.Request.Context(), appID, models.DockerAppStatusFailed, &msg)
	c.JSON(http.StatusConflict, gin.H{"error": code, "detail": err.Error(), "id": appID})
}

// usage reports the tenant's docker-app disk footprint vs their package disk
// quota (M49 soft meter). over_quota is advisory for a RUNNING app (the UI
// warns; a true runtime cap needs fs project quotas) — but NEW installs are
// blocked at create once usage meets the package quota (Gitea #489).
func (h *userDockerAppHandler) usage(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	used, err := h.cfg.Repo.SumDataBytesByUserID(ctx, claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage_failed"})
		return
	}
	var quotaBytes int64
	if h.cfg.Users != nil && h.cfg.Packages != nil {
		if user, _ := h.cfg.Users.FindByID(ctx, claims.UserID); user != nil && user.PackageID != nil {
			if pkg, _ := h.cfg.Packages.FindByID(ctx, *user.PackageID); pkg != nil {
				quotaBytes = int64(pkg.DiskQuotaMB) * 1024 * 1024
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"used_bytes":  used,
		"quota_bytes": quotaBytes,
		"over_quota":  quotaBytes > 0 && used > quotaBytes,
	})
}

// logs streams the owner's app container logs (read-only). Owner-scoped.
func (h *userDockerAppHandler) logs(c *gin.Context) {
	app := h.loadOwned(c)
	if app == nil {
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	params := map[string]any{"slug": app.EffectiveSlug()}
	if l := c.Query("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			params["lines"] = n
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "docker_app.logs", params)
	if err != nil {
		respondAgentErr(c, "agent_call_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// getEnv reveals the owner's app .env (secrets included — it's their own app,
// they need the generated admin/DB credentials). Owner-scoped, read-only;
// editing env is admin-only in v1 (a tenant recreate must re-inject the
// hardening overlay first — deferred).
func (h *userDockerAppHandler) getEnv(c *gin.Context) {
	app := h.loadOwned(c)
	if app == nil {
		return
	}
	entry, ok := h.cfg.Catalog.Get(app.Slug)
	if !ok {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "catalog_entry_missing"})
		return
	}
	env, err := h.admin.readInstallEnv(c.Request.Context(), app.EffectiveSlug())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "read_env_failed", "detail": firstLineString(err.Error())})
		return
	}
	out := make([]envVarView, 0, len(env))
	seen := make(map[string]bool, len(entry.Env))
	for _, ev := range entry.Env {
		seen[ev.Name] = true
		out = append(out, envVarView{Name: ev.Name, Value: env[ev.Name], Secret: ev.Secret, Generated: ev.Generate != ""})
	}
	for k, v := range env {
		if !seen[k] {
			out = append(out, envVarView{Name: k, Value: v})
		}
	}
	c.JSON(http.StatusOK, gin.H{"env": out})
}

// tenantInstanceSlug namespaces the on-disk / container identity by owner so
// two tenants can install the same catalog app: <slug>-<owner_hash>-<name>,
// lower-cased to satisfy the agent's slug regex.
//
// owner_hash is a hex SHA-256 prefix of the FULL user ID, NOT a raw slice of
// it: jabali user IDs are ULIDs whose first 10 chars are a millisecond
// timestamp (near-zero entropy), so a `userID[:8]` prefix collides for any two
// users created in the same ~minute — same instance_slug -> same data dir +
// container name (jabali-app-<x>), a tenant cross-contamination bug. Hashing
// the whole ID spreads the entropy so the namespace is actually unique.
func tenantInstanceSlug(slug, userID, name string) string {
	sum := sha256.Sum256([]byte(userID))
	short := hex.EncodeToString(sum[:])[:10]
	return strings.ToLower(slug + "-" + short + "-" + name)
}

// clampDockerMemory caps a docker memory string (e.g. "512m", "1g") to capMB
// megabytes. capMB==0 = unlimited. Unparseable input is returned unchanged (the
// catalog default is trusted); values over the cap become "<capMB>m" (#488).
func clampDockerMemory(s string, capMB uint32) string {
	if capMB == 0 {
		return s
	}
	b, ok := parseDockerSize(s)
	if !ok {
		return s
	}
	capBytes := int64(capMB) * 1024 * 1024
	if b > capBytes {
		return strconv.Itoa(int(capMB)) + "m"
	}
	return s
}

// parseDockerSize parses a docker-style size ("512m","1g","1024k","536870912")
// into bytes. Returns ok=false for empty/unparseable input.
func parseDockerSize(s string) (int64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "gb"), strings.HasSuffix(s, "g"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimRight(s, "gb")
	case strings.HasSuffix(s, "mb"), strings.HasSuffix(s, "m"):
		mult = 1024 * 1024
		s = strings.TrimRight(s, "mb")
	case strings.HasSuffix(s, "kb"), strings.HasSuffix(s, "k"):
		mult = 1024
		s = strings.TrimRight(s, "kb")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimRight(s, "b")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return int64(n * float64(mult)), true
}

// clampDockerCPU caps a docker cpu string (cores, e.g. "0.5","2") to the
// package CPU budget (capPct percent = capPct/100 cores). capPct==0 = unlimited.
func clampDockerCPU(s string, capPct uint32) string {
	if capPct == 0 {
		return s
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return s
	}
	capCores := float64(capPct) / 100.0
	if f > capCores {
		return strconv.FormatFloat(capCores, 'f', -1, 64)
	}
	return s
}

// clampPIDs caps a PID limit to the package MaxTasks. capTasks==0 = unlimited.
// A non-positive request (unlimited) is forced down to the cap.
func clampPIDs(p int, capTasks uint32) int {
	if capTasks == 0 {
		return p
	}
	if p <= 0 || p > int(capTasks) {
		return int(capTasks)
	}
	return p
}
