package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// systemCallTimeout bounds agent calls for system endpoints. Generous
// because service.list probes multiple units sequentially.
const phpVersionCallTimeout = 10 * time.Second

// adminActionTimeout bounds admin-only install/reload operations.
const adminActionTimeout = 5 * time.Minute

// ioncubeFetchTimeout bounds the panel-api download of the ionCube loaders
// tarball (~29 MB) before the .so is extracted, verified, and handed to the
// agent. Generous for a slow link; the fetch runs inside the admin action.
const ioncubeFetchTimeout = 2 * time.Minute

// reloadTimeout bounds the reload operation specifically.
const reloadTimeout = 30 * time.Second

// RegisterPHPVersionRoutes mounts GET /php/versions under the given router group.
// The group is expected to already carry RequireAuth middleware. This endpoint
// is available to all authenticated users (admin and non-admin) since they both
// need to select PHP versions for their domains.
func RegisterPHPVersionRoutes(rg *gin.RouterGroup, cli agent.AgentInterface) {
	php := rg.Group("/php")

	php.GET("/versions", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), phpVersionCallTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "php.version.list", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
}

// RegisterPHPVersionAdminRoutes mounts admin-only PHP version management routes.
// The group is expected to carry RequireAuth and RequireAdmin middleware. The
// settings repo is used for the DB-backed default PHP version — the agent
// reports one it computes locally, but the authoritative default lives in
// server_settings.default_php_version and is overridden into the response.
// settingsRepo may be nil during tests that only mock the agent; in that
// case the agent's value is surfaced unmodified.
func RegisterPHPVersionAdminRoutes(rg *gin.RouterGroup, cli agent.AgentInterface, settingsRepo repository.ServerSettingsRepository) {
	php := rg.Group("/php")

	// GET /php/versions/status — return full version matrix. Server-owned
	// default_php_version (from server_settings) wins over whatever the
	// agent reports so admins see the same default the reconciler uses.
	php.GET("/versions/status", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), phpVersionCallTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "php.version.status", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}

		if settingsRepo == nil {
			c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
			return
		}
		settings, sErr := settingsRepo.Get(ctx)
		if sErr != nil || settings == nil || settings.DefaultPHPVersion == "" {
			// Soft-fail: fall back to the agent's report. Surfacing a
			// 500 here would break the PHP admin page over a non-critical
			// discrepancy.
			c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
			return
		}
		var parsed map[string]any
		if jErr := json.Unmarshal(raw, &parsed); jErr != nil {
			c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
			return
		}
		parsed["default_version"] = settings.DefaultPHPVersion
		c.JSON(http.StatusOK, parsed)
	})

	// POST /php/versions/:version/default — set the server-wide default.
	// Only accepts versions that are currently installed AND FPM-running
	// so we don't leave new users pointing at a broken version.
	php.POST("/versions/:version/default", func(c *gin.Context) {
		version := c.Param("version")
		if !isVersionSupported(version) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported version: " + version})
			return
		}
		if settingsRepo == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "settings repository not configured"})
			return
		}

		statusCtx, statusCancel := context.WithTimeout(c.Request.Context(), phpVersionCallTimeout)
		defer statusCancel()
		statusRaw, sErr := cli.Call(statusCtx, "php.version.status", nil)
		if sErr != nil {
			status, body := translateAgentError(sErr)
			c.JSON(status, body)
			return
		}
		var statusResp struct {
			Versions []struct {
				Version    string `json:"version"`
				Installed  bool   `json:"installed"`
				FPMRunning bool   `json:"fpm_running"`
			} `json:"versions"`
		}
		if err := json.Unmarshal(statusRaw, &statusResp); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "could not parse agent response"})
			return
		}
		var ok bool
		for _, v := range statusResp.Versions {
			if v.Version == version && v.Installed && v.FPMRunning {
				ok = true
				break
			}
		}
		if !ok {
			c.JSON(http.StatusConflict, gin.H{"error": "version " + version + " is not installed and running"})
			return
		}

		settings, gErr := settingsRepo.Get(c.Request.Context())
		if gErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "load settings: " + gErr.Error()})
			return
		}
		if settings == nil {
			settings = &models.ServerSettings{ID: 1}
		}
		settings.DefaultPHPVersion = version
		if uErr := settingsRepo.Upsert(c.Request.Context(), settings); uErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "save settings: " + uErr.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"default_version": version})
	})

	// POST /php/versions/:version/install — install a PHP version
	php.POST("/versions/:version/install", func(c *gin.Context) {
		version := c.Param("version")

		// Validate version is supported
		if !isVersionSupported(version) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "unsupported version: " + version,
			})
			return
		}

		// Installing a PHP version (apt: phpX.Y-fpm + ~20 extensions + pool
		// render) can run for several minutes on a slow/contended host and
		// exceed nginx proxy_read_timeout (300s) → the browser sees a 502 even
		// though the install completes (GH #293). Detach it: dispatch the agent
		// install in the background and return 202 immediately; the SPA polls
		// /admin/php/versions/status until the version shows installed.
		go func() {
			bgctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 30*time.Minute)
			defer cancel()
			if _, err := cli.Call(bgctx, "php.version.install", map[string]string{"version": version}); err != nil {
				slog.Warn("php.version.install failed", "version", version, "err", err)
			}
		}()
		c.JSON(http.StatusAccepted, gin.H{"status": "installing", "version": version})
	})

	// DELETE /php/versions/:version — uninstall a PHP version
	php.DELETE("/versions/:version", func(c *gin.Context) {
		version := c.Param("version")

		if !isVersionSupported(version) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "unsupported version: " + version,
			})
			return
		}

		// Refuse to uninstall the current default PHP version. The
		// reconciler wires every existing FPM pool through this version
		// at start; removing it strands every pool. Operator must flip
		// the default first.
		if settingsRepo != nil {
			settings, sErr := settingsRepo.Get(c.Request.Context())
			if sErr == nil && settings != nil && settings.DefaultPHPVersion == version {
				c.JSON(http.StatusConflict, gin.H{
					"error":  "default_version_protected",
					"detail": "cannot uninstall the current default PHP version; change the default first",
				})
				return
			}
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), adminActionTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "php.version.uninstall", map[string]string{"version": version})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		// Defense-in-depth (GH #187): the agent already errors when the
		// version survives the purge, but never report success if the
		// response still flags it installed — that was the silent-failure.
		var resp struct {
			Installed bool `json:"installed"`
		}
		if uerr := json.Unmarshal(raw, &resp); uerr == nil && resp.Installed {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":  "uninstall_incomplete",
				"detail": "PHP version is still installed after the uninstall — check for held packages",
			})
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// POST /php/versions/:version/reload — reload a PHP-FPM service
	php.POST("/versions/:version/reload", func(c *gin.Context) {
		version := c.Param("version")

		// Validate version is supported
		if !isVersionSupported(version) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "unsupported version: " + version,
			})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), reloadTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "php.version.reload", map[string]string{"version": version})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
}

// isVersionSupported checks if a version is in the supported list.
// Must match the list in panel-agent/internal/commands/php_version_status.go
var supportedPHPVersions = []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"}

func isVersionSupported(version string) bool {
	for _, v := range supportedPHPVersions {
		if v == version {
			return true
		}
	}
	return false
}
