package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpext"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// php_pool_extensions.go — GH #1332 item 16. Per-(user, PHP version) opt-in
// extra PHP extensions. Extensions load once per FPM master (per pool), so this
// is pool-scoped, not per-domain; a tenant can turn ON installed extras but not
// turn OFF a base extension enabled server-wide.

const maxExtraExtensions = 40

// PHPPoolExtensionsHandlerConfig wires the per-pool extension routes.
type PHPPoolExtensionsHandlerConfig struct {
	Agent               agent.AgentInterface
	Users               repository.UserRepository
	Packages            repository.PackageRepository
	PHPPools            repository.PHPPoolRepository
	PHPPoolIniOverrides repository.PHPPoolIniOverrideRepository
}

func RegisterPHPPoolExtensionsRoutes(g *gin.RouterGroup, cfg PHPPoolExtensionsHandlerConfig) {
	h := &phpPoolExtensionsHandler{cfg: cfg}
	g.GET("/me/php-extensions", h.list)
	g.PUT("/me/php-extensions", h.set)
}

type phpPoolExtensionsHandler struct {
	cfg PHPPoolExtensionsHandlerConfig
}

// owner resolves the caller + their package; canEdit gates the whole feature on
// the same FPM-editing package flag the tuning card uses.
func (h *phpPoolExtensionsHandler) owner(c *gin.Context) (*models.User, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		return nil, false
	}
	user, err := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID)
	if err != nil || user == nil || user.PackageID == nil {
		return user, false
	}
	pkg, err := h.cfg.Packages.FindByID(c.Request.Context(), *user.PackageID)
	if err != nil || pkg == nil {
		return user, false
	}
	return user, pkg.FpmUserCanEdit
}

func (h *phpPoolExtensionsHandler) pool(c *gin.Context, userID, version string) (*models.PHPPool, bool) {
	ctx := c.Request.Context()
	if version != "" {
		if p, err := h.cfg.PHPPools.FindByUserAndVersion(ctx, userID, version); err == nil && p != nil {
			return p, true
		}
		return nil, false
	}
	if p, err := h.cfg.PHPPools.FindByUserID(ctx, userID); err == nil && p != nil {
		return p, true
	}
	return nil, false
}

// list returns the installed, non-built-in extensions the caller may toggle for
// a version, plus the set currently enabled on that pool.
func (h *phpPoolExtensionsHandler) list(c *gin.Context) {
	user, canEdit := h.owner(c)
	if user == nil || !canEdit {
		c.JSON(http.StatusForbidden, gin.H{"error": "fpm_editing_not_allowed"})
		return
	}
	version := c.Query("php_version")
	pool, ok := h.pool(c, user.ID, version)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
		return
	}
	available := h.installedExtensions(c, pool.PHPVersion)
	c.JSON(http.StatusOK, gin.H{
		"php_version": pool.PHPVersion,
		"available":   available,
		"enabled":     []string(pool.ExtraExtensions),
	})
}

type setExtensionsRequest struct {
	PHPVersion string   `json:"php_version"`
	Extensions []string `json:"extensions"`
}

func (h *phpPoolExtensionsHandler) set(c *gin.Context) {
	user, canEdit := h.owner(c)
	if user == nil || !canEdit {
		c.JSON(http.StatusForbidden, gin.H{"error": "fpm_editing_not_allowed"})
		return
	}
	var req setExtensionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if len(req.Extensions) > maxExtraExtensions {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too_many_extensions"})
		return
	}
	pool, ok := h.pool(c, user.ID, req.PHPVersion)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
		return
	}
	// Validate each against the known-extension catalog and drop built-ins
	// (always present) + duplicates. The agent re-checks the .so is installed.
	seen := map[string]bool{}
	clean := make(models.StringList, 0, len(req.Extensions))
	for _, name := range req.Extensions {
		spec, known := phpext.Lookup(name)
		if !known || spec.BuiltIn || seen[name] {
			continue
		}
		seen[name] = true
		clean = append(clean, name)
	}
	pool.ExtraExtensions = clean
	pool.Status = "pending"
	if err := h.cfg.PHPPools.Update(c.Request.Context(), pool); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Set("audit_target", "php_extensions:"+pool.PHPVersion)
	go reconcilePHPPoolViaAgent(h.cfg.Agent, h.cfg.Users, h.cfg.PHPPoolIniOverrides, h.cfg.PHPPools, pool)
	c.JSON(http.StatusOK, gin.H{"enabled": []string(clean)})
}

// installedExtensions asks the agent which non-built-in extensions are installed
// for the version. Best-effort: on any agent hiccup it falls back to the static
// catalog so the UI still renders something to toggle.
func (h *phpPoolExtensionsHandler) installedExtensions(c *gin.Context, version string) []string {
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "php.ext.list", map[string]string{"version": version})
	if err == nil {
		var env struct {
			Extensions []struct {
				Name      string `json:"name"`
				Installed bool   `json:"installed"`
				BuiltIn   bool   `json:"built_in"`
			} `json:"extensions"`
		}
		if json.Unmarshal(raw, &env) == nil && len(env.Extensions) > 0 {
			out := []string{}
			for _, e := range env.Extensions {
				if e.Installed && !e.BuiltIn {
					out = append(out, e.Name)
				}
			}
			return out
		}
	}
	out := []string{}
	for _, s := range phpext.All() {
		if !s.BuiltIn {
			out = append(out, s.Name)
		}
	}
	return out
}
