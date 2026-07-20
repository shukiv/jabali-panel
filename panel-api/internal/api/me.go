package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// MeHandlerConfig carries the repos the /me/* sub-routes need. Bare
// /me now also calls Users / Packages when set so the panel's My
// Profile page can render username + package without a second
// request; either repo nil falls back to the claims-only payload.
type MeHandlerConfig struct {
	Users          repository.UserRepository
	Packages       repository.PackageRepository
	ServerSettings repository.ServerSettingsRepository
	UIPrefs        repository.UserUIPrefRepository
}

// RegisterMeRoutes wires GET /api/v1/me and GET /api/v1/me/ssh-connection.
// The group passed here must already have RequireAuth applied.
func RegisterMeRoutes(g *gin.RouterGroup, cfg MeHandlerConfig) {
	g.GET("/me", meHandlerWithConfig(cfg))
	if cfg.Users != nil && cfg.ServerSettings != nil {
		h := &meExtHandler{cfg: cfg}
		g.GET("/me/ssh-connection", h.sshConnection)
		// M37 Phase 4: server capability flags any signed-in user
		// (admin OR tenant) needs to render the right UI. Currently
		// only postgres_enabled — add fields here when more
		// engine-/feature-gated UI lands.
		g.GET("/me/server-capabilities", h.serverCapabilities)
	}
	// Server-side per-user UI prefs (GH #218) — independent of the repos
	// above; only needs the UIPrefs store.
	if cfg.UIPrefs != nil {
		ph := &meExtHandler{cfg: cfg}
		g.GET("/me/ui-prefs", ph.uiPrefsGet)
		g.PUT("/me/ui-prefs/:key", ph.uiPrefsSet)
	}
}

// serverCapabilities returns the operator-controlled flags the SPA
// reads to decide whether to expose engine choices, app types, etc.
// Read-only mirror of the relevant server_settings fields, scoped to
// what's safe to share with non-admin tenants.
func (h *meExtHandler) serverCapabilities(c *gin.Context) {
	ctx := c.Request.Context()
	settings, err := h.cfg.ServerSettings.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		// Pre-seed install — every flag defaults to false.
		c.JSON(http.StatusOK, gin.H{"postgres_enabled": false, "docker_marketplace_enabled": false, "docker_apps_user_enabled": false, "python_apps_enabled": false, "tenant_domain_options_enabled": false, "dns_enabled": true, "mail_enabled": true, "security_enabled": true, "quota_enabled": true, "api_enabled": true, "root_terminal_enabled": false, "public_ipv4": "", "public_ipv6": ""})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// docker_apps_user_enabled is the server flag AND the caller's package
	// allowance: a user WITH a package whose MaxDockerApps is 0 doesn't see
	// Docker Apps (GH #283 — hide if not in package). A user with NO package
	// is unrestricted (GH #282), so the server flag alone decides.
	dockerUser := settings.DockerMarketplaceEnabled && settings.DockerAppsForUsersEnabled && tenantDockerHostReady()
	if dockerUser && h.cfg.Users != nil && h.cfg.Packages != nil {
		if claims := ginctx.Claims(c); claims != nil {
			if u, uerr := h.cfg.Users.FindByID(ctx, claims.UserID); uerr == nil && u != nil {
				// Tenant Docker requires a package that includes it (Gitea #511):
				// a package-less tenant is denied at install, so don't advertise
				// the capability either (keeps the nav honest).
				if u.PackageID == nil {
					dockerUser = false
				} else if pkg, perr := h.cfg.Packages.FindByID(ctx, *u.PackageID); perr != nil || pkg == nil || pkg.MaxDockerApps == 0 {
					dockerUser = false
				}
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"postgres_enabled":              settings.PostgresEnabled,
		"docker_marketplace_enabled":    settings.DockerMarketplaceEnabled,
		"docker_apps_user_enabled":      dockerUser,
		"python_apps_enabled":           settings.PythonAppsEnabled,
		"tenant_domain_options_enabled": settings.TenantDomainOptionsEnabled,
		// M353 Phase 1 (GH #353): per-module flags the SPA gates nav + routes on.
		"dns_enabled":      settings.DNSEnabled,
		"mail_enabled":     settings.MailEnabled,
		"security_enabled": settings.SecurityEnabled,
		"quota_enabled":    settings.QuotaEnabled,
		"api_enabled":      settings.APIEnabled,
		// GH #515 / JAB-169: the admin Terminal nav entry stayed visible with
		// the module off. Surface the flag so the sidebar hides it.
		"root_terminal_enabled": settings.RootTerminalEnabled,
		// GH #361: surface the server's public IPv4/IPv6 so the user + admin
		// dashboards can show them (already tracked for the panel cert + DNS;
		// not per-user sensitive). Empty string when unset.
		"public_ipv4": settings.PublicIPv4,
		"public_ipv6": settings.PublicIPv6,
	})
}

// meHandler is the bare claims-only fallback the test suite uses
// when MeHandlerConfig is empty.
func meHandler(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		// Belt and braces — RequireAuth should have aborted already.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":       claims.UserID,
		"email":    claims.Email,
		"is_admin": claims.IsAdmin,
	})
}

// meHandlerWithConfig returns the claims payload PLUS the user row's
// username, hosting-package id, and hosting-package name when the
// optional Users / Packages repos are wired. Missing repos or
// not-found rows degrade gracefully — the panel UI handles every
// optional field as nullable.
func meHandlerWithConfig(cfg MeHandlerConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := ginctx.Claims(c)
		if claims == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		resp := gin.H{
			"id":       claims.UserID,
			"email":    claims.Email,
			"is_admin": claims.IsAdmin,
		}
		// Surface the file-manager upload ceiling so the uploader can
		// reject oversize files client-side with an accurate message
		// (server_settings.upload_max_size_mb; #211). Read-only — the
		// admin sets it in Server Settings.
		if cfg.ServerSettings != nil {
			uploadMB := uint32(1024)
			if ss, serr := cfg.ServerSettings.Get(c.Request.Context()); serr == nil && ss != nil && ss.UploadMaxSizeMB > 0 {
				uploadMB = ss.UploadMaxSizeMB
			}
			resp["upload_max_size_mb"] = uploadMB
		}
		if cfg.Users == nil {
			c.JSON(http.StatusOK, resp)
			return
		}
		user, err := cfg.Users.FindByID(c.Request.Context(), claims.UserID)
		if err != nil || user == nil {
			c.JSON(http.StatusOK, resp)
			return
		}
		if user.Username != nil && *user.Username != "" {
			resp["username"] = *user.Username
		}
		resp["name_first"] = user.NameFirst
		resp["name_last"] = user.NameLast
		if full := strings.TrimSpace(strings.TrimSpace(user.NameFirst) + " " + strings.TrimSpace(user.NameLast)); full != "" {
			resp["full_name"] = full
		}
		resp["created_at"] = user.CreatedAt
		if user.PackageID != nil && *user.PackageID != "" {
			resp["package_id"] = *user.PackageID
			if cfg.Packages != nil {
				if pkg, perr := cfg.Packages.FindByID(c.Request.Context(), *user.PackageID); perr == nil && pkg != nil {
					resp["package_name"] = pkg.Name
				}
			}
		}
		c.JSON(http.StatusOK, resp)
	}
}

type meExtHandler struct{ cfg MeHandlerConfig }

// sshConnection returns everything the SSH Keys page needs to render the
// Connection Details card: server hostname + SSH port (from admin
// settings) and the caller's Linux username. The `command` field is the
// ready-to-copy ssh one-liner; callers don't have to know whether a
// custom port means `-p` or not.
func (h *meExtHandler) sshConnection(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	ctx := c.Request.Context()

	user, err := h.cfg.Users.FindByID(ctx, claims.UserID)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if user.Username == nil || *user.Username == "" {
		// Admins have no Linux account — they don't SFTP in.
		c.JSON(http.StatusConflict, gin.H{
			"error":  "no_linux_account",
			"detail": "this account has no Linux username, SSH access is not applicable",
		})
		return
	}

	settings, err := h.cfg.ServerSettings.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		settings = nil
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	host := ""
	port := uint16(22)
	if settings != nil {
		host = settings.Hostname
		if settings.SSHPort != 0 {
			port = settings.SSHPort
		}
	}
	if host == "" {
		// Fall back to the request's Host header so the page can still
		// render something useful when the admin hasn't filled identity
		// in yet. Strips any ":port" suffix since the browser attaches
		// the panel's own port, not the ssh one.
		host = c.Request.Host
		for i := range host {
			if host[i] == ':' {
				host = host[:i]
				break
			}
		}
	}

	cmd := fmt.Sprintf("ssh %s@%s", *user.Username, host)
	if port != 22 {
		cmd = fmt.Sprintf("ssh -p %d %s@%s", port, *user.Username, host)
	}

	c.JSON(http.StatusOK, gin.H{
		"host":     host,
		"port":     port,
		"username": *user.Username,
		"command":  cmd,
	})
}

// uiPrefsGet returns all server-side UI preferences for the current
// principal as a {key: value} map (GH #218).
func (h *meExtHandler) uiPrefsGet(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	prefs, err := h.cfg.UIPrefs.GetAll(c.Request.Context(), claims.UserID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"prefs": prefs})
}

// uiPrefsSet upserts one UI preference for the current principal. Body:
// {"value":"..."}. Key + value are length-bounded so this can't be abused
// as arbitrary blob storage.
func (h *meExtHandler) uiPrefsSet(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	key := c.Param("key")
	if key == "" || len(key) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key"})
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if len(body.Value) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "value too long"})
		return
	}
	if err := h.cfg.UIPrefs.Set(c.Request.Context(), claims.UserID, key, body.Value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Status(http.StatusNoContent)
}

// tenantDockerHostReady reports whether the host has been made tenant-ready
// (userns-remap done, flag written by `jabali docker enable-tenant`). The
// capability ANDs this so the user Docker Apps tab only appears once the host
// op has actually completed — a failed/aborted enable leaves the tab hidden
// rather than showing it over a tenant API that still 403s.
func tenantDockerHostReady() bool {
	_, err := os.Stat(defaultTenantDockerFlag)
	return err == nil
}
