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
	st := h.agentExtState(c, pool.PHPVersion)
	// Additive response (a tenant Automation API script may already read
	// available/enabled): keep those exact keys; add always_on = the version's
	// real server-default-enabled modules (conf.d, built-ins included) and
	// xdebug_on = this pool's Xdebug flag, so the UI can mirror the ACTUAL
	// per-version state instead of showing every extra as off.
	resp := gin.H{
		"php_version": pool.PHPVersion,
		"available":   st.available,
		"enabled":     []string(pool.ExtraExtensions),
		"xdebug_on":   pool.XdebugEnabled,
	}
	// always_on comes only from a live agent read; on any agent hiccup we OMIT it
	// so the UI shows "unknown" (hides the always-on group) rather than falsely
	// rendering server defaults as off — which would re-create the reported bug.
	if st.ok {
		resp["always_on"] = st.alwaysOn
	}
	c.JSON(http.StatusOK, resp)
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
	// Names the tenant must NOT store as an opt-in extra:
	//  - "xdebug": a Zend extension managed by its own control (PHP_INI_SCAN_DIR).
	//    Stored here it would render php_admin_value[extension]=xdebug.so — the
	//    wrong directive (needs zend_extension) — and fight XdebugEnabled. Stripped
	//    STATICALLY so the guard holds even when the agent is unreachable.
	//  - server-default (conf.d) modules: already enabled server-wide, so a pool
	//    php_admin_value[extension]= line is redundant. Best-effort via the agent;
	//    fail-open (a redundant line is only a PHP startup warning).
	locked := map[string]bool{"xdebug": true}
	for _, n := range h.agentExtState(c, req.PHPVersion).alwaysOn {
		locked[n] = true
	}
	// Validate each against the known-extension catalog and drop built-ins
	// (always present), locked names, and duplicates. The agent re-checks the
	// .so is installed.
	seen := map[string]bool{}
	clean := make(models.StringList, 0, len(req.Extensions))
	for _, name := range req.Extensions {
		spec, known := phpext.Lookup(name)
		if !known || spec.BuiltIn || locked[name] || seen[name] {
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

// extState is the agent's view of a version's extensions:
//   - available: installed, non-built-in names the tenant may toggle on
//   - alwaysOn:  names enabled server-wide via conf.d in BOTH SAPIs — the
//     version's real default-enabled set (built-ins included)
//   - ok:        false when the agent didn't answer, in which case available
//     falls back to the static catalog and alwaysOn is unknown (nil), so callers
//     degrade (hide the always-on group) instead of claiming "off".
type extState struct {
	available []string
	alwaysOn  []string
	ok        bool
}

// agentExtState asks the agent (php.ext.list) for the version's extension state.
// Best-effort: on any agent hiccup it falls back to the static catalog for the
// togglable set and reports ok=false.
func (h *phpPoolExtensionsHandler) agentExtState(c *gin.Context, version string) extState {
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "php.ext.list", map[string]string{"version": version})
	if err == nil {
		var env struct {
			Extensions []struct {
				Name      string `json:"name"`
				Installed bool   `json:"installed"`
				Enabled   bool   `json:"enabled"`
				BuiltIn   bool   `json:"built_in"`
			} `json:"extensions"`
		}
		if json.Unmarshal(raw, &env) == nil && len(env.Extensions) > 0 {
			avail := []string{}
			always := []string{}
			for _, e := range env.Extensions {
				if e.Installed && !e.BuiltIn {
					avail = append(avail, e.Name)
				}
				if e.Enabled {
					always = append(always, e.Name)
				}
			}
			return extState{available: avail, alwaysOn: always, ok: true}
		}
	}
	avail := []string{}
	for _, s := range phpext.All() {
		if !s.BuiltIn {
			avail = append(avail, s.Name)
		}
	}
	return extState{available: avail, ok: false}
}
