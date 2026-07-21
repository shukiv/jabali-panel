// docker_apps.go — REST surface for the M48 Docker App Marketplace
// (admin-only per ADR-0116 Decision 1).
//
// Routes:
//
//	GET    /admin/docker-apps/catalog              list catalog entries
//	GET    /admin/docker-apps                      list installed apps
//	POST   /admin/docker-apps                      install a catalog app
//	GET    /admin/docker-apps/:id                  get one (with ports)
//	PATCH  /admin/docker-apps/:id                  update install-time settings
//	DELETE /admin/docker-apps/:id                  uninstall (?keep_volumes=1)
//	POST   /admin/docker-apps/:id/start            start
//	POST   /admin/docker-apps/:id/stop             stop
//	POST   /admin/docker-apps/:id/restart          restart
//	POST   /admin/docker-apps/:id/rebuild          force-recreate
//
// Phase 4 ships READ + CRUD + lifecycle. Logs, exec, backup,
// update/rollback, compose-download all queue for later phases.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DockerAppHandlerConfig bundles dependencies.
type DockerAppHandlerConfig struct {
	Repo    repository.DockerAppRepository
	Catalog *dockerapp.Catalog
	// ServerSettings is the gate for the M48 marketplace opt-in. When
	// settings.docker_marketplace_enabled is false, every route in
	// this group returns 503 docker_marketplace_disabled. The flag is
	// flipped on the Server Settings page; the panel-api server_settings
	// PATCH handler dispatches docker.install / docker.disable to the
	// agent on flip.
	ServerSettings repository.ServerSettingsRepository
	// Domains is optional. When set, the install handler creates a
	// `domains` row with managed_by='docker_app' for each install
	// that has a loopback+reverse_proxy=true port, so the reconciler
	// renders an nginx vhost that proxy_passes to the upstream port
	// AND issues LE through the same path as tenant domains.
	// ADR-0116 Decision 4.
	Domains repository.DomainRepository
	Agent   agent.AgentInterface
	// Users resolves a tenant app's OS username for re-render hardening
	// (Gitea #480). Optional; required only for tenant-owned app re-renders.
	Users repository.UserRepository
	Log   *slog.Logger
}

type dockerAppHandler struct{ cfg DockerAppHandlerConfig }

// RegisterDockerAppRoutes mounts the admin docker-app surface.
func RegisterDockerAppRoutes(g *gin.RouterGroup, cfg DockerAppHandlerConfig) {
	h := &dockerAppHandler{cfg: cfg}
	grp := g.Group("/admin/docker-apps")
	grp.Use(requireAdminForDockerApps)
	grp.Use(h.requireDockerMarketplaceEnabled)
	grp.GET("/catalog", h.listCatalog)
	grp.GET("/maintenance/disk-usage", h.maintenanceDiskUsage)
	grp.POST("/maintenance/prune", h.maintenancePrune)
	grp.GET("/engine/status", h.engineStatus)
	grp.GET("/catalog/:slug/icon", h.catalogIcon)
	grp.GET("", h.list)
	grp.POST("", h.install)
	grp.GET("/:id", h.get)
	grp.PATCH("/:id", h.update)
	grp.DELETE("/:id", h.delete)
	grp.POST("/:id/start", h.lifecycle("start"))
	grp.POST("/:id/stop", h.lifecycle("stop"))
	grp.POST("/:id/restart", h.lifecycle("restart"))
	grp.POST("/:id/rebuild", h.lifecycle("rebuild"))
	grp.POST("/:id/update", h.updateImage)
	grp.GET("/:id/env", h.getEnv)
	grp.PUT("/:id/env", h.putEnv)
	grp.POST("/:id/env/regenerate", h.regenerateEnv)
	grp.GET("/:id/logs", h.logs)
	grp.POST("/:id/exec", h.execCmd)
	grp.POST("/:id/backup", h.backup)
	grp.GET("/:id/backups", h.listBackups)
	grp.POST("/:id/backups/:bid/restore", h.restoreBackup)
}

// requireAdminForDockerApps gates the whole route group on
// claims.IsAdmin. Tenants can never reach any verb on this surface
// (ADR-0116 Decision 1).
func requireAdminForDockerApps(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || !claims.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.Next()
}

// ---- catalog -----------------------------------------------------------------

type catalogEntryResponse struct {
	Slug              string                  `json:"slug"`
	Name              string                  `json:"name"`
	Version           string                  `json:"version"`
	Description       string                  `json:"description"`
	Tags              []string                `json:"tags,omitempty"`
	Icon              string                  `json:"icon"`
	Upstream          string                  `json:"upstream,omitempty"`
	Documentation     string                  `json:"documentation,omitempty"`
	PostInstallNote   string                  `json:"post_install_note,omitempty"`
	UpdateMode        string                  `json:"update_mode"`
	Resources         dockerapp.Resources     `json:"resources"`
	Volumes           []dockerapp.Volume      `json:"volumes"`
	Ports             []dockerapp.PortSpec    `json:"ports"`
	Env               []catalogEnvVarResponse `json:"env,omitempty"`
	TenantInstallable bool                    `json:"tenant_installable"`
}

type catalogEnvVarResponse struct {
	Name     string `json:"name"`
	Value    string `json:"value,omitempty"` // omitted when Secret=true
	Secret   bool   `json:"secret,omitempty"`
	Generate string `json:"generate,omitempty"`
}

func (h *dockerAppHandler) listCatalog(c *gin.Context) {
	if h.cfg.Catalog == nil {
		c.JSON(http.StatusOK, gin.H{"items": []catalogEntryResponse{}})
		return
	}
	entries := h.cfg.Catalog.All()
	out := make([]catalogEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, catalogEntryToResponse(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// catalogIcon serves the raw <slug>/icon.svg (or icon.png) bytes from
// the loaded catalog. Sandboxed to slugs known to the catalog so a
// crafted ?slug=../something can't escape /usr/local/share/jabali/
// docker-apps. Content-type is sniffed from the file extension only;
// the catalog loader already validated the file exists at parse time.
func (h *dockerAppHandler) catalogIcon(c *gin.Context) {
	if h.cfg.Catalog == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	slug := c.Param("slug")
	entry, ok := h.cfg.Catalog.Get(slug)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	name := entry.Icon
	if name == "" {
		name = "icon.svg"
	}
	// Reject any path separators - icon must live directly under the
	// entry dir, not in a subfolder.
	if strings.ContainsAny(name, "/\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_icon"})
		return
	}
	// Theme-aware: a "<base>-dark.<ext>" / "-light.<ext>" variant next to the
	// base icon wins when the caller passes ?theme=dark|light. Falls back to
	// the base icon when no variant exists.
	name = resolveThemedIcon(entry.Dir(), name, c.Query("theme"))
	full := filepath.Join(entry.Dir(), name)
	bytes, err := os.ReadFile(full)
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
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(http.StatusOK, ctype, bytes)
}

// resolveThemedIcon returns the icon file to serve for the requested theme.
// If theme is "dark" or "light" and a sibling "<base>-<theme>.<ext>" file
// exists in dir, that variant is returned; otherwise the base name is used.
// theme is trusted only for the two literals, so no path escaping is possible.
func resolveThemedIcon(dir, name, theme string) string {
	if theme != "dark" && theme != "light" {
		return name
	}
	ext := filepath.Ext(name)
	variant := strings.TrimSuffix(name, ext) + "-" + theme + ext
	if _, err := os.Stat(filepath.Join(dir, variant)); err == nil {
		return variant
	}
	return name
}

func catalogEntryToResponse(e dockerapp.Entry) catalogEntryResponse {
	env := make([]catalogEnvVarResponse, 0, len(e.Env))
	for _, v := range e.Env {
		row := catalogEnvVarResponse{Name: v.Name, Secret: v.Secret, Generate: v.Generate}
		if !v.Secret {
			row.Value = v.Value
		}
		env = append(env, row)
	}
	return catalogEntryResponse{
		Slug:              e.Slug,
		Tags:              e.Tags,
		Name:              e.Name,
		Version:           e.Version,
		Description:       e.Description,
		Icon:              e.Icon,
		Upstream:          e.Upstream,
		Documentation:     e.Documentation,
		PostInstallNote:   e.PostInstallNote,
		UpdateMode:        e.UpdateMode,
		Resources:         e.Resources,
		Volumes:           e.Volumes,
		Ports:             e.Ports,
		TenantInstallable: e.TenantInstallable,
		Env:               env,
	}
}

// ---- list installed ---------------------------------------------------------

type installedResponse struct {
	models.DockerApp
	Ports  []*models.DockerAppPublishedPort `json:"ports"`
	Domain string                           `json:"domain,omitempty"`
	// PostInstallNote is the catalog entry's first-run guidance (GH #521) with
	// its "{{domain}}" token substituted for the app's assigned domain. Empty
	// when the catalog entry has no note. The UI shows it as a dismissible
	// hint on the installed-app card so operators know the first-run step
	// (e.g. Pocket ID: create the first admin at https://<domain>/setup).
	PostInstallNote string `json:"post_install_note,omitempty"`
}

// postInstallNoteFor returns the catalog entry's post-install note with the
// "{{domain}}" token replaced by the app's assigned domain. Returns "" when the
// catalog is unavailable, the entry is unknown, or it declares no note.
func (h *dockerAppHandler) postInstallNoteFor(slug, domain string) string {
	if h.cfg.Catalog == nil {
		return ""
	}
	entry, ok := h.cfg.Catalog.Get(slug)
	if !ok || entry.PostInstallNote == "" {
		return ""
	}
	d := domain
	if d == "" {
		d = "<your app's domain>"
	}
	return strings.ReplaceAll(entry.PostInstallNote, "{{domain}}", d)
}

func (h *dockerAppHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	var apps []*models.DockerApp
	var err error
	// Admin owner-scope via ?user_id (#483). Server-level installs (NULL
	// user_id) are excluded from an owner view by ListByUserID.
	if uid := c.Query("user_id"); uid != "" {
		if !ids.IsValidULID(uid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_id"})
			return
		}
		apps, err = h.cfg.Repo.ListByUserID(ctx, uid)
	} else {
		apps, err = h.cfg.Repo.ListAll(ctx)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	var domList []models.Domain
	if h.cfg.Domains != nil {
		domList, _, _ = h.cfg.Domains.List(ctx, repository.ListOptions{})
	}
	out := make([]installedResponse, 0, len(apps))
	for _, a := range apps {
		ports, _ := h.cfg.Repo.ListPortsForApp(ctx, a.ID)
		resp := installedResponse{DockerApp: *a, Ports: ports}
		for _, d := range domList {
			if d.DockerAppID != nil && *d.DockerAppID == a.ID {
				resp.Domain = d.Name
				break
			}
		}
		resp.PostInstallNote = h.postInstallNoteFor(a.Slug, resp.Domain)
		out = append(out, resp)
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// ---- install ----------------------------------------------------------------

type installRequest struct {
	Slug                string               `json:"slug" binding:"required"`
	Name                string               `json:"name" binding:"required"`
	Domain              string               `json:"domain,omitempty"`
	UpdateMode          string               `json:"update_mode,omitempty"`
	CPULimit            string               `json:"cpu_limit,omitempty"`
	Memory              string               `json:"memory_limit,omitempty"`
	PIDsLimit           *int                 `json:"pids_limit,omitempty"`
	EnvOverride         map[string]string    `json:"env,omitempty"`
	Ports               []installPortRequest `json:"ports,omitempty"`
	BackupDestinationID string               `json:"backup_destination_id,omitempty"`
}

type installPortRequest struct {
	Name          string `json:"name"`           // catalog port name
	Enabled       *bool  `json:"enabled"`        // nil = use catalog default
	BindInterface string `json:"bind_interface"` // "loopback" or "public:<managed_ip_id>"
	HostPort      int    `json:"host_port"`      // 0 = auto-allocate
	ReverseProxy  *bool  `json:"reverse_proxy"`  // nil = use catalog default
}

var nameRE = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func (h *dockerAppHandler) install(c *gin.Context) {
	ctx := c.Request.Context()
	var req installRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Validate catalog slug.
	if h.cfg.Catalog == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "catalog_unavailable"})
		return
	}
	entry, ok := h.cfg.Catalog.Get(req.Slug)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_slug", "slug": req.Slug})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !nameRE.MatchString(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name", "detail": "must match ^[a-z0-9-]{1,32}$"})
		return
	}
	// Derived instance slug must fit the agent's slug regex (RFC 1123
	// DNS label budget after the jabali-app- prefix). 52 chars total.
	instanceSlug := req.Slug + "-" + req.Name
	if len(instanceSlug) > 52 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name_too_long", "detail": fmt.Sprintf("catalog slug + name exceeds 52 chars (got %d)", len(instanceSlug))})
		return
	}

	// Reject duplicate slug+name. Exception: a prior install that
	// ended in `failed` is treated as a no-op corpse — delete it and
	// let the caller retry transparently. Without this, the only way
	// to recover from a failed install was via DB surgery, since the
	// install button just kept returning 409.
	if existing, _ := h.cfg.Repo.FindBySlugName(ctx, req.Slug, req.Name); existing != nil {
		if existing.Status == models.DockerAppStatusFailed {
			// Best-effort: drop the orphaned ports + row + any
			// docker_app-managed domain it left behind. The agent's
			// docker compose project may or may not exist on disk; the
			// install verb below will re-create it idempotently.
			ports, _ := h.cfg.Repo.ListPortsForApp(ctx, existing.ID)
			for _, p := range ports {
				_ = h.cfg.Repo.DeletePort(ctx, p.ID)
			}
			if h.cfg.Domains != nil {
				domList, _, _ := h.cfg.Domains.List(ctx, repository.ListOptions{})
				for _, dom := range domList {
					if dom.ManagedBy == models.DomainManagedByDockerApp && dom.DockerAppID != nil && *dom.DockerAppID == existing.ID {
						_ = h.cfg.Domains.Delete(ctx, dom.ID)
					}
				}
			}
			if derr := h.cfg.Repo.Delete(ctx, existing.ID); derr != nil {
				c.JSON(http.StatusConflict, gin.H{"error": "already_installed", "id": existing.ID})
				return
			}
		} else {
			c.JSON(http.StatusConflict, gin.H{"error": "already_installed", "id": existing.ID})
			return
		}
	}

	// Defense in depth: also sweep ANY orphaned docker_app-managed
	// domain pointing at a docker_app_id we can no longer find. This
	// catches the scar where an earlier install died mid-flight before
	// the failed-corpse path above could mark it; the row was deleted
	// manually but the domain row was left behind. Without this sweep
	// the next install retry with the same hostname 409s on the unique
	// constraint at h.cfg.Domains.Create below.
	if h.cfg.Domains != nil && req.Domain != "" {
		if existingDom, _ := h.cfg.Domains.FindByName(ctx, req.Domain); existingDom != nil && existingDom.ManagedBy == models.DomainManagedByDockerApp {
			orphan := existingDom.DockerAppID == nil
			if !orphan {
				if owner, _ := h.cfg.Repo.FindByID(ctx, *existingDom.DockerAppID); owner == nil {
					orphan = true
				}
			}
			if orphan {
				_ = h.cfg.Domains.Delete(ctx, existingDom.ID)
			}
		}
	}

	// Apply defaults from the catalog when caller omits.
	updateMode := req.UpdateMode
	if updateMode == "" {
		updateMode = entry.UpdateMode
	}
	if updateMode != models.DockerAppUpdateModeManual && updateMode != models.DockerAppUpdateModeAuto {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_update_mode"})
		return
	}
	cpu := req.CPULimit
	if cpu == "" {
		cpu = entry.Resources.CPU
	}
	mem := req.Memory
	if mem == "" {
		mem = entry.Resources.Memory
	}
	pids := req.PIDsLimit
	if pids == nil && entry.Resources.PIDs > 0 {
		p := entry.Resources.PIDs
		pids = &p
	}

	// Persist the docker_apps row first; downstream port allocation
	// references its ID. Status starts at `pending` so the reconciler
	// will dispatch the install if our synchronous dispatch below
	// fails for any reason.
	app := &models.DockerApp{
		ID:             ulid.Make().String(),
		Slug:           req.Slug,
		InstanceSlug:   instanceSlug,
		Name:           req.Name,
		CatalogVersion: entry.Version,
		Status:         models.DockerAppStatusPending,
		UpdateMode:     updateMode,
	}
	if cpu != "" {
		app.CPULimit = &cpu
	}
	if mem != "" {
		app.MemoryLimit = &mem
	}
	if pids != nil {
		app.PIDsLimit = pids
	}
	if req.BackupDestinationID != "" {
		v := req.BackupDestinationID
		app.BackupDestinationID = &v
	}
	if err := h.cfg.Repo.Create(ctx, app); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed", "detail": err.Error()})
		return
	}

	// Resolve the ports the caller asked for, defaulting to the
	// catalog's `default_enabled` set.
	resolvedPorts, runtimePorts, err := h.resolvePorts(ctx, entry, req.Ports, app.ID)
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

	// M48 Phase 6: auto-create a domains row when the install supplies
	// a hostname AND there's at least one loopback+reverse_proxy=true
	// port. The reconciler picks the row up on its next tick and
	// renders an nginx vhost via the existing proxy_pass nginx_rules
	// path -- same flow as tenant Rule Builder proxy_pass entries.
	// We tag the row with managed_by='docker_app' so the tenant /domains
	// CRUD handler refuses to delete it (delete is owned by the
	// docker-app handler below).
	if h.cfg.Domains != nil && req.Domain != "" {
		claims := ginctx.Claims(c)
		var loopbackPort *models.DockerAppPublishedPort
		for _, p := range resolvedPorts {
			if p.ReverseProxy && p.Protocol == "tcp" {
				loopbackPort = p
				break
			}
		}
		if loopbackPort != nil && claims != nil {
			target := "http://127.0.0.1:" + intToStr(loopbackPort.HostPort)
			truePtr := true
			rules := models.NginxRules{{
				Type:      "proxy_pass",
				Path:      "/",
				Target:    target,
				Websocket: &truePtr,
			}}
			existingDom, _ := h.cfg.Domains.FindByName(ctx, req.Domain)
			if existingDom != nil && existingDom.ManagedBy != models.DomainManagedByDockerApp {
				// Refuse to silently overwrite a domain row owned by
				// another user (cross-tenant IDOR). Admin must attach
				// only to a domain they own; for tenant-owned domains
				// the tenant has to do the attach themselves.
				if existingDom.UserID != claims.UserID && !claims.IsAdmin {
					msg := "domain already owned by another user"
					_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg)
					c.JSON(http.StatusConflict, gin.H{"error": "domain_in_use", "detail": "domain " + req.Domain + " belongs to another user", "id": app.ID})
					return
				}
				if aerr := h.cfg.Domains.AttachDockerApp(ctx, existingDom.ID, app.ID, rules); aerr != nil {
					msg := "domain attach failed: " + firstLineString(aerr.Error())
					_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg)
					c.JSON(http.StatusConflict, gin.H{"error": "domain_attach_failed", "detail": aerr.Error(), "id": app.ID})
					return
				}
			} else {
				dom := &models.Domain{
					ID:          ulid.Make().String(),
					UserID:      claims.UserID,
					Name:        req.Domain,
					DocRoot:     "",
					IsEnabled:   true,
					SSLEnabled:  true,
					NginxRules:  rules,
					ManagedBy:   models.DomainManagedByDockerApp,
					DockerAppID: &app.ID,
				}
				if derr := h.cfg.Domains.Create(ctx, dom); derr != nil {
					msg := "domain auto-create failed: " + firstLineString(derr.Error())
					_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg)
					c.JSON(http.StatusConflict, gin.H{"error": "domain_create_failed", "detail": derr.Error(), "id": app.ID})
					return
				}
			}
		}
	}

	// Materialise env (catalog + secrets + operator overrides).
	envMap, err := dockerapp.MaterialiseEnv(entry, req.EnvOverride)
	if err != nil {
		_ = h.cfg.Repo.Delete(ctx, app.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "env_materialise_failed", "detail": err.Error()})
		return
	}

	// Render the compose template. Slug = instanceSlug so the
	// rendered compose.yml has per-install paths + container names;
	// without this two installs of the same catalog entry collide
	// on dir + container name.
	dataRoot := "/var/lib/jabali/docker-apps/" + instanceSlug
	composeYML, err := dockerapp.Render(entry, dockerapp.RenderParams{
		Slug:         instanceSlug,
		Name:         req.Name,
		Domain:       req.Domain,
		ImageChannel: entry.ImageChannel,
		DataRoot:     dataRoot,
		CPULimit:     cpu,
		MemoryLimit:  mem,
		Ports:        runtimePorts,
		Env:          envMap,
	})
	if err != nil {
		_ = h.cfg.Repo.Delete(ctx, app.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "render_failed", "detail": err.Error()})
		return
	}

	// Dispatch the install through the agent in the BACKGROUND. The image
	// pull + compose-up + health wait can take minutes; running it on the
	// request goroutine held the response past nginx's proxy_read_timeout
	// -> 502 in the browser (same shape as the update path). The row is
	// created with status=installing and the UI polls every 8s until it
	// flips to running / failed.
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
		}
		appID := app.ID
		go func() {
			bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
			defer cancel()
			_, agentErr := h.cfg.Agent.Call(bgCtx, "docker_app.install", installParams)
			// WithoutCancel/Background so the terminal status lands even
			// though the request already returned.
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

	// Return the row immediately (status=installing). The UI's 8s poll
	// flips it to running / failed when the background install finishes.
	fresh, _ := h.cfg.Repo.FindByID(ctx, app.ID)
	ports, _ := h.cfg.Repo.ListPortsForApp(ctx, app.ID)
	if fresh != nil {
		c.JSON(http.StatusCreated, installedResponse{DockerApp: *fresh, Ports: ports})
		return
	}
	c.JSON(http.StatusCreated, installedResponse{DockerApp: *app, Ports: ports})
}

// resolvePorts walks the catalog's declared ports and produces
// (DB rows, runtime map) for the install request. Catalog defaults
// apply when the request omits a port row.
func (h *dockerAppHandler) resolvePorts(
	ctx context.Context,
	entry dockerapp.Entry,
	req []installPortRequest,
	appID string,
) ([]*models.DockerAppPublishedPort, map[string]dockerapp.RuntimePort, error) {
	overrideByName := make(map[string]installPortRequest, len(req))
	for _, r := range req {
		overrideByName[r.Name] = r
	}

	rows := make([]*models.DockerAppPublishedPort, 0)
	runtime := make(map[string]dockerapp.RuntimePort, len(entry.Ports))

	for _, p := range entry.Ports {
		o, hasOverride := overrideByName[p.Name]
		enabled := p.DefaultEnabled
		if hasOverride && o.Enabled != nil {
			enabled = *o.Enabled
		}
		if !enabled {
			continue
		}
		bind := p.DefaultBind
		if hasOverride && o.BindInterface != "" {
			bind = o.BindInterface
		}
		rp := p.DefaultReverseProxy
		if hasOverride && o.ReverseProxy != nil {
			rp = *o.ReverseProxy
		}
		// Allocate or pin host port.
		hostPort := 0
		if hasOverride && o.HostPort > 0 {
			hostPort = o.HostPort
			// Reject the well-known reserved ports a tenant install
			// must never bind: the panel + its dependencies own these
			// across every fresh deploy. The list lives next to the
			// resolver so a future tweak doesn't require chasing a
			// separate consts file.
			if isReservedHostPort(hostPort) {
				return nil, nil, fmt.Errorf("host port %d is reserved (port %s)", hostPort, p.Name)
			}
			// DB uniqueness on (bind_interface, host_port, protocol) is
			// enforced at insert. Surface the conflict as a clear error
			// instead of a generic 'port_persist_failed' the user can't
			// diagnose.
			if taken, terr := h.cfg.Repo.HostPortInUse(ctx, bindInterfaceForDB(bind), p.Protocol, hostPort, appID); terr != nil {
				return nil, nil, terr
			} else if taken {
				return nil, nil, fmt.Errorf("host port %d/%s on %s is already bound by another install (port %s)", hostPort, p.Protocol, bindInterfaceForDB(bind), p.Name)
			}
		} else {
			free, err := h.cfg.Repo.FindFreeHostPort(ctx, bindInterfaceForDB(bind), p.Protocol)
			if err != nil {
				return nil, nil, err
			}
			hostPort = free
		}
		row := &models.DockerAppPublishedPort{
			ID:            ulid.Make().String(),
			AppID:         appID,
			PortName:      p.Name,
			ContainerPort: p.ContainerPort,
			BindInterface: bindInterfaceForDB(bind),
			HostPort:      hostPort,
			Protocol:      p.Protocol,
			ReverseProxy:  rp,
			Enabled:       true,
		}
		rows = append(rows, row)
		runtime[p.Name] = dockerapp.RuntimePort{
			HostPort:      hostPort,
			ContainerPort: p.ContainerPort,
			BindInterface: bindInterfaceForRuntime(bind),
			Protocol:      p.Protocol,
		}
	}
	return rows, runtime, nil
}

// bindInterfaceForDB normalises the input shape ("loopback" or
// "public" or "public:<ip_id>") for the DB column. We keep the form
// the operator chose so a future migration can resolve "public" to
// the specific managed-IP at agent-call time.
func bindInterfaceForDB(s string) string {
	if s == "" {
		return "loopback"
	}
	return s
}

// bindInterfaceForRuntime turns the DB-form into the actual IP
// literal that goes into the compose `ports:` mapping. Phase 4
// supports "loopback" -> 127.0.0.1. "public" without a specific
// managed-IP picked yet binds to 0.0.0.0; managed-IP resolution
// lands in Phase 6 when domains.docker_app_id is wired.
func bindInterfaceForRuntime(s string) string {
	switch {
	case s == "loopback" || s == "":
		return "127.0.0.1"
	case s == "public":
		return "0.0.0.0"
	case strings.HasPrefix(s, "public:"):
		// TODO(phase6): resolve <managed_ip_id> against managed_ips
		// repo and return the address.
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// buildEnvFile renders an env map into a docker-compose .env file
// shape. Secret values get NO additional escaping; values must be
// alphanumeric/safe — catalog-declared secrets use base64url and
// operator overrides are validated by the schema.
// readInstallEnv fetches an install's on-disk .env (KEY=VALUE) from the agent
// so a re-render preserves the install's generated secrets (DB passwords,
// MASTERKEY, JWT secrets) instead of minting new ones. Empty map when the
// install has no .env. The response carries secrets — never log it.
func (h *dockerAppHandler) readInstallEnv(ctx context.Context, instanceSlug string) (map[string]string, error) {
	if h.cfg.Agent == nil {
		return nil, fmt.Errorf("agent unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "docker_app.read_env", map[string]any{"slug": instanceSlug})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Env == nil {
		resp.Env = map[string]string{}
	}
	return resp.Env, nil
}

// installDomain returns the docker_app-managed hostname bound to this install,
// or "" if none.
func (h *dockerAppHandler) installDomain(ctx context.Context, appID string) string {
	if h.cfg.Domains == nil {
		return ""
	}
	domList, _, _ := h.cfg.Domains.List(ctx, repository.ListOptions{})
	for _, d := range domList {
		if d.DockerAppID != nil && *d.DockerAppID == appID {
			return d.Name
		}
	}
	return ""
}

// tenantValidateParams returns whether a re-render dispatch should carry the
// agent-side tenant compose validation, plus the catalog tenant-cap allowlist,
// for a tenant-owned app (Gitea #480). Returns (false, nil) for admin apps.
func (h *dockerAppHandler) tenantValidateParams(ctx context.Context, app *models.DockerApp) (validate bool, caps []string, cgroup string, err error) {
	// Admin/server-owned app: legitimately no tenant validation.
	if app == nil || app.UserID == nil {
		return false, nil, "", nil
	}
	// Gitea #529: for a TENANT app, missing metadata is NOT "no validation
	// needed" — it means we can't resolve the safety profile and must fail
	// closed so callers don't bring the app up/update/restore unvalidated.
	if h.cfg.Catalog == nil {
		return false, nil, "", fmt.Errorf("catalog unavailable: cannot resolve tenant validation metadata")
	}
	entry, ok := h.cfg.Catalog.Get(app.Slug)
	if !ok {
		return false, nil, "", fmt.Errorf("catalog entry %q not found: cannot resolve tenant validation metadata", app.Slug)
	}
	if h.cfg.Users == nil {
		return false, nil, "", fmt.Errorf("users repo unavailable: cannot resolve owner slice")
	}
	u, uerr := h.cfg.Users.FindByID(ctx, *app.UserID)
	if uerr != nil || u == nil || u.Username == nil || *u.Username == "" {
		return false, nil, "", fmt.Errorf("cannot resolve tenant owner slice for app %s: %v", app.ID, uerr)
	}
	// Gitea #525: the exact owner slice the compose must declare.
	return true, dockerapp.TenantCapAllowlist(entry.TenantCaps), "jabali-user-" + *u.Username + ".slice", nil
}

// applyTenantValidateParams sets tenant_validate/tenant_caps/tenant_cgroup on an
// agent param map for a tenant-owned app, or returns an error when the metadata
// can't be resolved (fail closed, Gitea #529). No-op for admin apps.
func (h *dockerAppHandler) applyTenantValidateParams(ctx context.Context, app *models.DockerApp, params map[string]any) error {
	v, caps, cgroup, err := h.tenantValidateParams(ctx, app)
	if err != nil {
		return err
	}
	if v {
		params["tenant_validate"] = true
		params["tenant_caps"] = caps
		params["tenant_cgroup"] = cgroup
	}
	return nil
}

// renderInstallCompose re-renders an install's compose.yml from the CURRENT
// catalog template, preserving its domain, ports, limits and existing secrets
// (via the on-disk .env). Used by update so catalog fixes + version bumps
// reach existing installs. Returns the rendered compose + merged env file —
// neither must be logged.
func (h *dockerAppHandler) renderInstallCompose(ctx context.Context, app *models.DockerApp, domain string, overrideEnv map[string]string) (string, string, error) {
	if h.cfg.Catalog == nil {
		return "", "", fmt.Errorf("catalog unavailable")
	}
	entry, ok := h.cfg.Catalog.Get(app.Slug)
	if !ok {
		return "", "", fmt.Errorf("catalog entry %q not found", app.Slug)
	}
	existingEnv := overrideEnv
	if existingEnv == nil {
		var err error
		existingEnv, err = h.readInstallEnv(ctx, app.EffectiveSlug())
		if err != nil {
			return "", "", fmt.Errorf("read env: %w", err)
		}
	}
	// existingEnv as overrides → secrets preserved; only genuinely-new
	// catalog keys get freshly generated.
	envMap, err := dockerapp.MaterialiseEnv(entry, existingEnv)
	if err != nil {
		return "", "", fmt.Errorf("materialise env: %w", err)
	}
	current, _ := h.cfg.Repo.ListPortsForApp(ctx, app.ID)
	runtimePorts := make(map[string]dockerapp.RuntimePort, len(current))
	for _, p := range current {
		runtimePorts[p.PortName] = dockerapp.RuntimePort{
			HostPort:      p.HostPort,
			ContainerPort: p.ContainerPort,
			BindInterface: bindInterfaceForRuntime(p.BindInterface),
			Protocol:      p.Protocol,
		}
	}
	cpu, mem, pids := "", "", 0
	if app.CPULimit != nil {
		cpu = *app.CPULimit
	}
	if app.MemoryLimit != nil {
		mem = *app.MemoryLimit
	}
	if app.PIDsLimit != nil {
		pids = *app.PIDsLimit
	}
	// Preserve tenant sandbox hardening on EVERY re-render (Gitea #480). The
	// initial tenant install injects TenantHardening (cap_drop ALL, no-new-
	// privileges, tenant cgroup parent, PIDs limit); this shared re-render path
	// is reused by admin update + env edit, so without re-injecting it a later
	// re-render would rewrite a tenant compose as an unhardened admin compose.
	var tenantHardening *dockerapp.TenantHardening
	if app.UserID != nil {
		if h.cfg.Users == nil {
			return "", "", fmt.Errorf("users repo unavailable for tenant app re-render")
		}
		u, uerr := h.cfg.Users.FindByID(ctx, *app.UserID)
		if uerr != nil || u == nil || u.Username == nil || *u.Username == "" {
			return "", "", fmt.Errorf("resolve tenant username for hardening: %v", uerr)
		}
		tenantHardening = &dockerapp.TenantHardening{
			CgroupParent: "jabali-user-" + *u.Username + ".slice",
			Caps:         dockerapp.TenantCapAllowlist(entry.TenantCaps),
			PIDsLimit:    pids,
		}
	}
	composeYML, err := dockerapp.Render(entry, dockerapp.RenderParams{
		Slug:            app.EffectiveSlug(),
		Name:            app.Name,
		Domain:          domain,
		ImageChannel:    entry.ImageChannel,
		DataRoot:        "/var/lib/jabali/docker-apps/" + app.EffectiveSlug(),
		CPULimit:        cpu,
		MemoryLimit:     mem,
		PIDsLimit:       pids,
		Ports:           runtimePorts,
		Env:             envMap,
		TenantHardening: tenantHardening,
	})
	if err != nil {
		return "", "", fmt.Errorf("render: %w", err)
	}
	return composeYML, buildEnvFile(envMap), nil
}

func buildEnvFile(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range env {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return b.String()
}

// ---- get / update / delete ---------------------------------------------------

func (h *dockerAppHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	ports, _ := h.cfg.Repo.ListPortsForApp(ctx, id)
	resp := installedResponse{DockerApp: *app, Ports: ports}
	if h.cfg.Domains != nil {
		if domList, _, derr := h.cfg.Domains.List(ctx, repository.ListOptions{}); derr == nil {
			for _, d := range domList {
				if d.DockerAppID != nil && *d.DockerAppID == app.ID {
					resp.Domain = d.Name
					break
				}
			}
		}
	}
	resp.PostInstallNote = h.postInstallNoteFor(app.Slug, resp.Domain)
	c.JSON(http.StatusOK, resp)
}

type updateRequest struct {
	UpdateMode  *string `json:"update_mode,omitempty"`
	CPULimit    *string `json:"cpu_limit,omitempty"`
	MemoryLimit *string `json:"memory_limit,omitempty"`
	PIDsLimit   *int    `json:"pids_limit,omitempty"`
	// Domain + Ports trigger a full re-render: the agent gets a new
	// compose.yml and re-runs docker_app.install (idempotent). An
	// explicit empty string for Domain means "drop the managed
	// domain row"; nil means "leave it". Ports nil means "leave port
	// wiring untouched".
	Domain *string              `json:"domain,omitempty"`
	Ports  []installPortRequest `json:"ports,omitempty"`
}

func (h *dockerAppHandler) update(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	var req updateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if req.UpdateMode != nil {
		v := *req.UpdateMode
		if v != models.DockerAppUpdateModeManual && v != models.DockerAppUpdateModeAuto {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_update_mode"})
			return
		}
		app.UpdateMode = v
	}
	if req.CPULimit != nil {
		app.CPULimit = req.CPULimit
	}
	if req.MemoryLimit != nil {
		app.MemoryLimit = req.MemoryLimit
	}
	if req.PIDsLimit != nil {
		app.PIDsLimit = req.PIDsLimit
	}
	if err := h.cfg.Repo.Update(ctx, app); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}

	// Domain + ports changes require a compose re-render — the same tenant-aware
	// redispatch the CLI uses (#546). editDomainPorts holds the SINGLE
	// implementation so the security-critical sandbox re-render is never
	// duplicated.
	if req.Domain != nil || len(req.Ports) > 0 {
		uid, isAdmin := "", false
		if claims := ginctx.Claims(c); claims != nil {
			uid, isAdmin = claims.UserID, claims.IsAdmin
		}
		if e := h.editDomainPorts(ctx, app, req.Domain, req.Ports, uid, isAdmin); e != nil {
			if de, ok := e.(*dockerEditError); ok {
				resp := gin.H{"error": de.Code}
				if de.Detail != "" {
					resp["detail"] = de.Detail
				}
				c.JSON(de.Status, resp)
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": e.Error()})
			}
			return
		}
	}

	c.JSON(http.StatusOK, app)
}

// dockerEditError carries the HTTP status + code/detail the docker-app
// domain/port edit path returns, so both the gin handler and the CLI map to the
// exact same response. (#546)
type dockerEditError struct {
	Status int
	Code   string
	Detail string
}

func (e *dockerEditError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// editDomainPorts applies a docker-app domain/port edit and the tenant-aware
// compose re-render. Gin-free so update() (GUI) and DockerAppEditDomainPorts
// (CLI, #546) share ONE implementation — the security-critical sandbox
// re-render (renderInstallCompose + applyTenantValidateParams / tenant_validate)
// is never duplicated. reqDomain==nil → no domain change; empty reqPorts → keep
// existing ports. Returns *dockerEditError on a mapped failure.
func (h *dockerAppHandler) editDomainPorts(ctx context.Context, app *models.DockerApp, reqDomain *string, reqPorts []installPortRequest, userID string, isAdmin bool) error {
	if app.Status == models.DockerAppStatusRunning {
		return &dockerEditError{http.StatusConflict, "container_running", "stop the container before editing ports or domain"}
	}
	if h.cfg.Catalog == nil {
		return &dockerEditError{http.StatusServiceUnavailable, "catalog_unavailable", ""}
	}
	entry, ok := h.cfg.Catalog.Get(app.Slug)
	if !ok {
		return &dockerEditError{http.StatusInternalServerError, "catalog_entry_missing", "slug " + app.Slug + " no longer in /usr/local/share/jabali/docker-apps/"}
	}

	// --- ports ---
	var runtimePorts map[string]dockerapp.RuntimePort
	if len(reqPorts) > 0 {
		prior, _ := h.cfg.Repo.ListPortsForApp(ctx, app.ID)
		for _, p := range prior {
			_ = h.cfg.Repo.DeletePort(ctx, p.ID)
		}
		resolved, runtime, perr := h.resolvePorts(ctx, entry, reqPorts, app.ID)
		if perr != nil {
			return &dockerEditError{http.StatusBadRequest, "port_resolution_failed", perr.Error()}
		}
		for _, row := range resolved {
			if err := h.cfg.Repo.CreatePort(ctx, row); err != nil {
				return &dockerEditError{http.StatusConflict, "port_persist_failed", err.Error()}
			}
		}
		runtimePorts = runtime
	} else {
		current, _ := h.cfg.Repo.ListPortsForApp(ctx, app.ID)
		runtimePorts = make(map[string]dockerapp.RuntimePort, len(current))
		for _, p := range current {
			runtimePorts[p.PortName] = dockerapp.RuntimePort{
				HostPort:      p.HostPort,
				ContainerPort: p.ContainerPort,
				BindInterface: bindInterfaceForRuntime(p.BindInterface),
				Protocol:      p.Protocol,
			}
		}
	}

	// --- domain ---
	newDomain := ""
	if reqDomain != nil {
		newDomain = strings.TrimSpace(*reqDomain)
		if h.cfg.Domains != nil {
			domList, _, _ := h.cfg.Domains.List(ctx, repository.ListOptions{})
			for _, d := range domList {
				if d.ManagedBy == models.DomainManagedByDockerApp && d.DockerAppID != nil && *d.DockerAppID == app.ID {
					_ = h.cfg.Domains.Delete(ctx, d.ID)
				}
			}
			if newDomain != "" {
				if existingDom, _ := h.cfg.Domains.FindByName(ctx, newDomain); existingDom != nil && existingDom.ManagedBy == models.DomainManagedByDockerApp {
					orphan := existingDom.DockerAppID == nil
					if !orphan {
						if owner, _ := h.cfg.Repo.FindByID(ctx, *existingDom.DockerAppID); owner == nil {
							orphan = true
						}
					}
					if orphan {
						_ = h.cfg.Domains.Delete(ctx, existingDom.ID)
					}
				}
				var loopbackPort *models.DockerAppPublishedPort
				latest, _ := h.cfg.Repo.ListPortsForApp(ctx, app.ID)
				for _, p := range latest {
					if p.ReverseProxy && p.Protocol == "tcp" {
						loopbackPort = p
						break
					}
				}
				if loopbackPort != nil && userID != "" {
					target := "http://127.0.0.1:" + intToStr(loopbackPort.HostPort)
					truePtr := true
					rules := models.NginxRules{{
						Type:      "proxy_pass",
						Path:      "/",
						Target:    target,
						Websocket: &truePtr,
					}}
					existingDom, _ := h.cfg.Domains.FindByName(ctx, newDomain)
					if existingDom != nil && existingDom.ManagedBy != models.DomainManagedByDockerApp {
						if existingDom.UserID != userID && !isAdmin {
							return &dockerEditError{http.StatusConflict, "domain_in_use", "domain " + newDomain + " belongs to another user"}
						}
						if aerr := h.cfg.Domains.AttachDockerApp(ctx, existingDom.ID, app.ID, rules); aerr != nil {
							return &dockerEditError{http.StatusConflict, "domain_attach_failed", aerr.Error()}
						}
					} else {
						dom := &models.Domain{
							ID:          ulid.Make().String(),
							UserID:      userID,
							Name:        newDomain,
							DocRoot:     "",
							IsEnabled:   true,
							SSLEnabled:  true,
							NginxRules:  rules,
							ManagedBy:   models.DomainManagedByDockerApp,
							DockerAppID: &app.ID,
						}
						if derr := h.cfg.Domains.Create(ctx, dom); derr != nil {
							return &dockerEditError{http.StatusConflict, "domain_create_failed", derr.Error()}
						}
					}
				}
			}
		}
	}

	// --- re-render compose + re-dispatch (tenant sandbox preserved) ---
	composeYML, envFile, rerr := h.renderInstallCompose(ctx, app, newDomain, nil)
	_ = runtimePorts // ports already persisted above; helper reads them from the repo
	if rerr != nil {
		return &dockerEditError{http.StatusInternalServerError, "render_failed", rerr.Error()}
	}
	if h.cfg.Agent != nil {
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		volumeNames := make([]string, 0, len(entry.Volumes))
		for _, v := range entry.Volumes {
			volumeNames = append(volumeNames, v.Name)
		}
		installParams := map[string]any{
			"slug":                        app.EffectiveSlug(),
			"compose_yml":                 composeYML,
			"env_file":                    envFile,
			"volumes":                     volumeNames,
			"volume_owner":                entry.VolumeOwner,
			"wait_healthy":                false,
			"healthcheck_timeout_seconds": 60,
		}
		if verr := h.applyTenantValidateParams(callCtx, app, installParams); verr != nil {
			return &dockerEditError{http.StatusInternalServerError, "tenant_validation_unavailable", firstLineString(verr.Error())}
		}
		if _, agentErr := h.cfg.Agent.Call(callCtx, "docker_app.install", installParams); agentErr != nil {
			return &dockerEditError{http.StatusBadGateway, "agent_redispatch_failed", firstLineString(agentErr.Error())}
		}
	}
	return nil
}

// DockerAppEditDomainPorts is the exported entry point for the `jabali
// docker-app edit` CLI (#546). It runs the SAME editDomainPorts the GUI PATCH
// uses, so the stopped-container requirement, port resolution, domain row
// management, and tenant-hardening compose re-render are identical.
// DockerAppPortEdit is the exported port-override the CLI passes to
// DockerAppEditDomainPorts; it maps 1:1 to the GUI's port edit fields.
type DockerAppPortEdit struct {
	Name          string
	Enabled       *bool
	BindInterface string
	HostPort      int
	ReverseProxy  *bool
}

func DockerAppEditDomainPorts(ctx context.Context, cfg DockerAppHandlerConfig, appID string, domain *string, ports []DockerAppPortEdit, userID string, isAdmin bool) (*models.DockerApp, error) {
	h := &dockerAppHandler{cfg: cfg}
	app, err := cfg.Repo.FindByID(ctx, appID)
	if err != nil {
		return nil, err
	}
	internal := make([]installPortRequest, 0, len(ports))
	for _, p := range ports {
		internal = append(internal, installPortRequest{
			Name:          p.Name,
			Enabled:       p.Enabled,
			BindInterface: p.BindInterface,
			HostPort:      p.HostPort,
			ReverseProxy:  p.ReverseProxy,
		})
	}
	if err := h.editDomainPorts(ctx, app, domain, internal, userID, isAdmin); err != nil {
		return nil, err
	}
	return app, nil
}

func (h *dockerAppHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	purge := c.Query("keep_volumes") == ""
	if h.cfg.Agent != nil {
		callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if _, err := h.cfg.Agent.Call(callCtx, "docker_app.delete", map[string]any{
			"slug":          app.EffectiveSlug(),
			"purge_volumes": purge,
		}); err != nil {
			// For rows already in a broken state (failed install / stuck
			// installing), the agent may legitimately have nothing to
			// delete on disk -- the compose dir may have never been
			// written. Refusing the DB delete locks the operator out of
			// the only way to remove the zombie row. Allow force=true
			// OR a broken status to soft-delete the row anyway.
			force := c.Query("force") == "true"
			broken := app.Status == models.DockerAppStatusFailed ||
				app.Status == models.DockerAppStatusInstalling ||
				app.Status == models.DockerAppStatusPending
			if !force && !broken {
				respondAgentErr(c, "agent_delete_failed", err)
				return
			}
			// Fall through to DB cleanup.
		}
	}

	// M48 Phase 6: drop the auto-managed domain row (if any). The FK
	// on docker_app_id is SET NULL, not CASCADE -- we ON_DELETE_SET_NULL
	// at the SQL layer so an accidental docker_apps.Delete doesn't tear
	// down domain rows blindly; the explicit cleanup belongs here.
	if h.cfg.Domains != nil {
		domList, _, _ := h.cfg.Domains.List(ctx, repository.ListOptions{})
		for _, dom := range domList {
			if dom.DockerAppID == nil || *dom.DockerAppID != id {
				continue
			}
			if dom.ManagedBy == models.DomainManagedByDockerApp {
				// We created this row at install time -- nuke it, and
				// tear down its proxy vhost (the reconciler can't, the
				// row is gone after this).
				domName := dom.Name
				_ = h.cfg.Domains.Delete(ctx, dom.ID)
				if h.cfg.Agent != nil && domName != "" {
					rmCtx, rmCancel := context.WithTimeout(ctx, 30*time.Second)
					_, _ = h.cfg.Agent.Call(rmCtx, "docker_app.vhost_remove",
						map[string]string{"domain_name": domName})
					rmCancel()
				}
			} else {
				// Tenant-owned row that the install handler attached
				// to. Don't delete it -- the tenant still owns the
				// hostname. Just detach the docker_app link + clear
				// the proxy_pass rule we injected so nginx stops
				// pointing at the dead container's port.
				_ = h.cfg.Domains.DetachDockerApp(ctx, dom.ID, true)
			}
		}
	}

	if err := h.cfg.Repo.Delete(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- lifecycle ---------------------------------------------------------------

func (h *dockerAppHandler) lifecycle(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		id := c.Param("id")
		app, err := h.cfg.Repo.FindByID(ctx, id)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
			return
		}
		verb := "docker_app." + action
		if h.cfg.Agent == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		lifeParams := map[string]any{"slug": app.EffectiveSlug()}
		// Gitea #526: start/restart/rebuild bring the stack up from the on-disk
		// compose; for a tenant app that compose is the last-mile trust boundary
		// and may have drifted (restore/manual edit). Carry the tenant gate so
		// the agent re-validates before `up`. (stop tears down — no gate needed.)
		if action == "start" || action == "restart" || action == "rebuild" {
			if verr := h.applyTenantValidateParams(callCtx, app, lifeParams); verr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant_validation_unavailable", "detail": firstLineString(verr.Error())})
				return
			}
		}
		raw, err := h.cfg.Agent.Call(callCtx, verb, lifeParams)
		if err != nil {
			respondAgentErr(c, "agent_call_failed", err)
			return
		}
		// Reflect the post-lifecycle state in the docker_apps row so
		// the marketplace UI sees the new status on its next poll.
		// Without this the row stays at whatever value `install`
		// last wrote and the operator clicks Start / Stop / Restart
		// with no visible effect (the container DID flip — the DB
		// row just didn't follow). Rebuild rewrites the stack via
		// docker compose up --force-recreate so the container ends
		// up running too.
		var newStatus string
		switch action {
		case "start", "restart", "rebuild":
			newStatus = models.DockerAppStatusRunning
		case "stop":
			newStatus = models.DockerAppStatusStopped
		}
		if newStatus != "" {
			_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, newStatus, nil)
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	}
}

// updateImage handles POST /:id/update. Dispatches docker_app.update
// and applies the new status to the row based on the agent's outcome.
func (h *dockerAppHandler) updateImage(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}

	// Mark `updating` so the UI shows a spinner; restore on outcome.
	_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusUpdating, nil)

	// Re-render the compose from the CURRENT catalog template so catalog
	// fixes (e.g. a corrected healthcheck) and version bumps reach this
	// install — update otherwise reused the stale on-disk compose.yml.
	// Secrets are preserved via the install's existing .env. (Fast — kept
	// on the request path so render errors are visible; the slow pull runs
	// in the background below.)
	updateParams := map[string]any{
		"slug":                        app.EffectiveSlug(),
		"healthcheck_timeout_seconds": 300,
	}
	domain := h.installDomain(ctx, app.ID)
	if composeYML, envFile, rerr := h.renderInstallCompose(ctx, app, domain, nil); rerr != nil {
		// Gitea #527: for a tenant-owned app, falling back to the on-disk compose
		// would recreate the container WITHOUT the tenant validation gate (the
		// on-disk compose may be stale/restored/unsafe). Fail closed.
		if app.UserID != nil {
			msg := "re-render failed; refusing to update tenant app from unvalidated on-disk compose: " + firstLineString(rerr.Error())
			_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg)
			c.JSON(http.StatusBadGateway, gin.H{"error": "rerender_failed", "detail": msg})
			return
		}
		// Admin/server app: fall back to the on-disk compose so a transient
		// read-env failure or missing catalog entry never blocks the update.
		slog.Warn("docker-app update: re-render skipped, using on-disk compose",
			"id", app.ID, "slug", app.Slug, "err", rerr)
	} else {
		updateParams["compose_yml"] = composeYML
		updateParams["env_file"] = envFile
		if verr := h.applyTenantValidateParams(ctx, app, updateParams); verr != nil {
			msg := firstLineString(verr.Error())
			_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusFailed, &msg)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant_validation_unavailable", "detail": msg})
			return
		}
	}

	// docker_app.update pulls a new image + recreates containers, which can
	// take minutes. Running it on the request goroutine held the HTTP
	// response open past nginx's proxy_read_timeout -> 502 in the browser,
	// even though the update finished in the background and the app ended up
	// updated. Detach it: return 202 now and run the agent call + terminal
	// status in a background goroutine. The status was already set to
	// `updating` above; the UI polls docker-app status every 8s and shows
	// the spinner until the row flips to running / failed.
	appID := app.ID
	appSlug := app.Slug
	go func() {
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		raw, err := h.cfg.Agent.Call(bgCtx, "docker_app.update", updateParams)
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer persistCancel()
		if err != nil {
			msg := firstLineString(err.Error())
			_ = h.cfg.Repo.UpdateStatus(persistCtx, appID, models.DockerAppStatusFailed, &msg)
			return
		}
		var resp struct {
			Outcome  string `json:"outcome"`
			Detail   string `json:"detail"`
			NewImage string `json:"new_image"`
		}
		_ = json.Unmarshal(raw, &resp)
		switch resp.Outcome {
		case "updated":
			_ = h.cfg.Repo.UpdateStatus(persistCtx, appID, models.DockerAppStatusRunning, nil)
			if resp.NewImage != "" {
				_ = h.cfg.Repo.UpdateImageSHA(persistCtx, appID, resp.NewImage)
				_ = h.cfg.Repo.UpdateAvailableDigest(persistCtx, appID, resp.NewImage)
			}
			// The install was just re-rendered from the current catalog, so
			// the version label tracks the catalog instead of freezing at
			// the install-time value.
			if h.cfg.Catalog != nil {
				if entry, ok := h.cfg.Catalog.Get(appSlug); ok && entry.Version != "" {
					_ = h.cfg.Repo.UpdateCatalogVersion(persistCtx, appID, entry.Version)
				}
			}
		case "rolled_back":
			detail := resp.Detail
			_ = h.cfg.Repo.UpdateStatus(persistCtx, appID, models.DockerAppStatusRunning, &detail)
		default:
			_ = h.cfg.Repo.UpdateStatus(persistCtx, appID, models.DockerAppStatusRunning, nil)
		}
	}()

	c.JSON(http.StatusAccepted, gin.H{"status": "updating", "id": appID})
}

// logs proxies docker_app.logs through. Query params: lines (int,
// default 200), service (string, optional).
func (h *dockerAppHandler) logs(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
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
	if svc := c.Query("service"); svc != "" {
		params["service"] = svc
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "docker_app.logs", params)
	if err != nil {
		respondAgentErr(c, "agent_call_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

type execRequest struct {
	Command string `json:"command" binding:"required"`
	Service string `json:"service,omitempty"`
}

// execCmd is admin-only one-shot exec in the app's container. Already
// gated by requireAdminForDockerApps; no additional auth check needed.
func (h *dockerAppHandler) execCmd(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	var req execRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "docker_app.exec", map[string]any{
		"slug":    app.EffectiveSlug(),
		"service": req.Service,
		"command": req.Command,
	})
	if err != nil {
		respondAgentErr(c, "agent_call_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// backup -> docker_app.backup
func (h *dockerAppHandler) backup(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	bkParams := map[string]any{"slug": app.EffectiveSlug(), "reason": "manual"}
	if app.BackupDestinationID != nil {
		bkParams["destination_id"] = *app.BackupDestinationID
	}
	raw, err := h.cfg.Agent.Call(callCtx, "docker_app.backup", bkParams)
	if err != nil {
		respondAgentErr(c, "agent_backup_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// listBackups -> docker_app.list_backups
func (h *dockerAppHandler) listBackups(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	lbParams := map[string]any{"slug": app.EffectiveSlug()}
	if app.BackupDestinationID != nil {
		lbParams["destination_id"] = *app.BackupDestinationID
	}
	raw, err := h.cfg.Agent.Call(callCtx, "docker_app.list_backups", lbParams)
	if err != nil {
		respondAgentErr(c, "agent_call_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// restoreBackup -> docker_app.restore
func (h *dockerAppHandler) restoreBackup(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	bid := c.Param("bid")
	app, err := h.cfg.Repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	rsParams := map[string]any{"slug": app.EffectiveSlug(), "snapshot_id": bid}
	if app.BackupDestinationID != nil {
		rsParams["destination_id"] = *app.BackupDestinationID
	}
	// Tenant-owned restore (Gitea #509): a snapshot can replace compose.yml /
	// .env with an old or unsafe version. Carry the tenant gate so the agent
	// re-validates the restored compose before `up` and fails closed otherwise.
	if verr := h.applyTenantValidateParams(callCtx, app, rsParams); verr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant_validation_unavailable", "detail": firstLineString(verr.Error())})
		return
	}
	raw, err := h.cfg.Agent.Call(callCtx, "docker_app.restore", rsParams)
	if err != nil {
		respondAgentErr(c, "agent_restore_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// requireDockerMarketplaceEnabled rejects every call with 503 when the
// operator hasn't flipped server_settings.docker_marketplace_enabled
// in Server Settings. The /admin/docker-apps/* routes stay MOUNTED
// even with marketplace disabled so the UI can render a clear 503 +
// "enable in Server Settings" hint instead of a generic 404.
func (h *dockerAppHandler) requireDockerMarketplaceEnabled(c *gin.Context) {
	if h.cfg.ServerSettings == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error":  "docker_marketplace_disabled",
			"detail": "server_settings unavailable",
		})
		return
	}
	ss, err := h.cfg.ServerSettings.Get(c.Request.Context())
	if err != nil || ss == nil || !ss.DockerMarketplaceEnabled {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error":  "docker_marketplace_disabled",
			"detail": "enable the Docker App Marketplace in Server Settings",
		})
		return
	}
	c.Next()
}

// firstLineString returns the leading line of a multi-line string.
// Repeated here because cron.go's local helper is unexported and
// adding a cross-file dependency is more cost than the four lines
// of duplication.
func firstLineString(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// intToStr is local to avoid pulling strconv into a hot path that
// only uses it for one integer. Phase 6.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 5)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// reservedHostPorts is the static block list of host ports the
// docker_app install pipeline must never bind. The set covers the
// panel itself (8080/8443/80/443), Kratos (4433/4434), Bulwark
// (4455), PowerDNS (53/8081), mail (25/465/587/993/995), MariaDB
// (3306), Postgres (5432), Redis (6379), the SSH host (22), and the
// common monitoring + telemetry ports. The catalog is allowed to
// declare any container port -- this only checks the host_port the
// operator picks.
var reservedHostPorts = map[int]bool{
	22:    true, // host SSH
	25:    true, // smtp
	53:    true, // dns
	80:    true, // nginx
	110:   true, // pop3
	143:   true, // imap
	443:   true, // nginx tls
	465:   true, // smtps
	587:   true, // submission
	993:   true, // imaps
	995:   true, // pop3s
	3306:  true, // mariadb
	4190:  true, // managesieve
	4433:  true, // kratos public
	4434:  true, // kratos admin
	4455:  true, // bulwark
	5432:  true, // postgres
	6379:  true, // redis
	8080:  true, // panel http
	8081:  true, // pdns webserver
	8443:  true, // panel tls
	9100:  true, // node_exporter
	11211: true, // memcached
}

func isReservedHostPort(p int) bool {
	if p <= 0 || p > 65535 {
		return true
	}
	return reservedHostPorts[p]
}

func (h *dockerAppHandler) maintenanceDiskUsage(c *gin.Context) {
	ctx := c.Request.Context()
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "docker.disk_usage", map[string]any{})
	if err != nil {
		respondAgentErr(c, "agent_call_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

type maintenancePruneRequest struct {
	Volumes bool `json:"volumes"`
	All     bool `json:"all"`
}

func (h *dockerAppHandler) maintenancePrune(c *gin.Context) {
	ctx := c.Request.Context()
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	var req maintenancePruneRequest
	if err := c.ShouldBindJSON(&req); err != nil && c.Request.ContentLength > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "docker.prune", map[string]any{
		"volumes": req.Volumes,
		"all":     req.All,
	})
	if err != nil {
		respondAgentErr(c, "agent_call_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}

// engineStatus proxies docker.status from the agent so the UI can
// poll whether Docker CE finished installing after a marketplace-
// enable toggle. The PATCH /admin/settings dispatch is background-
// async (so the operator's PATCH doesn't block on apt-get); the UI
// needs an out-of-band way to confirm the engine is up before it
// drops its spinner.
func (h *dockerAppHandler) engineStatus(c *gin.Context) {
	ctx := c.Request.Context()
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "docker.status", map[string]any{})
	if err != nil {
		respondAgentErr(c, "agent_call_failed", err)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
