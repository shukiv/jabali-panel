package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainPHPSettingsHandlerConfig wires the domain PHP settings routes.
type DomainPHPSettingsHandlerConfig struct {
	Domains  repository.DomainRepository
	PHPPools repository.PHPPoolRepository
	// Agent + PoolIniOverrides power the pool_defaults hint (GH #1543): the real
	// value a domain inherits per directive, so the UI can label each dropdown's
	// inherit option "<value> (Default)". Both optional — nil degrades the
	// response to omit pool_defaults, and the UI falls back to a generic label.
	Agent            agent.AgentInterface
	PoolIniOverrides repository.PHPPoolIniOverrideRepository
}

// RegisterDomainPHPSettingsRoutes adds the PHP settings endpoints:
//   - GET  /domains/:id/php-settings
//   - PATCH /domains/:id/php-settings
func RegisterDomainPHPSettingsRoutes(g *gin.RouterGroup, cfg DomainPHPSettingsHandlerConfig) {
	h := &domainPHPSettingsHandler{cfg: cfg}
	g.GET("/domains/:id/php-settings", h.get)
	g.PATCH("/domains/:id/php-settings", h.patch)
}

type domainPHPSettingsHandler struct {
	cfg DomainPHPSettingsHandlerConfig
}

// getDomainPHPSettingsRequest is unused for GET; response is below.
type getDomainPHPSettingsResponse struct {
	PHPPoolID            *string `json:"php_pool_id,omitempty"`
	PHPVersion           *string `json:"php_version,omitempty"`
	PHPMemoryLimit       *string `json:"php_memory_limit,omitempty"`
	PHPUploadMaxFilesize *string `json:"php_upload_max_filesize,omitempty"`
	PHPPostMaxSize       *string `json:"php_post_max_size,omitempty"`
	PHPMaxInputVars      *int    `json:"php_max_input_vars,omitempty"`
	PHPMaxExecutionTime  *int    `json:"php_max_execution_time,omitempty"`
	PHPMaxInputTime      *int    `json:"php_max_input_time,omitempty"`
	// GH #1332 per-domain runtime directives.
	PHPDisplayErrors  *bool   `json:"php_display_errors,omitempty"`
	PHPErrorReporting *int    `json:"php_error_reporting,omitempty"`
	PHPTimezone       *string `json:"php_timezone,omitempty"`
	// PoolDefaults (GH #1543) is the effective value this domain INHERITS per
	// directive when it sets no override — the pool's ini override if it has
	// one, else the box's FPM php.ini baseline (read live via the agent). Keys
	// are php.ini directive names (memory_limit, upload_max_filesize, …). Absent
	// when the agent/pool can't be resolved; the UI then shows a generic label.
	PoolDefaults map[string]string `json:"pool_defaults,omitempty"`
}

// updateDomainPHPSettingsRequest mirrors the overridable fields plus an optional
// PHPVersion change. NULL values clear the override. The client is expected to
// send the full set on each PATCH (the UI does) — an omitted field is nil, which
// clears it. PHPVersion, if set, updates the DOMAIN OWNER's pool (one pool per
// user, per ADR-0023) — it applies to every domain that user owns.
type updateDomainPHPSettingsRequest struct {
	PHPVersion           *string `json:"php_version"`
	PHPMemoryLimit       *string `json:"php_memory_limit"`
	PHPUploadMaxFilesize *string `json:"php_upload_max_filesize"`
	PHPPostMaxSize       *string `json:"php_post_max_size"`
	PHPMaxInputVars      *int    `json:"php_max_input_vars"`
	PHPMaxExecutionTime  *int    `json:"php_max_execution_time"`
	PHPMaxInputTime      *int    `json:"php_max_input_time"`
	// GH #1332 per-domain runtime directives.
	PHPDisplayErrors  *bool   `json:"php_display_errors"`
	PHPErrorReporting *int    `json:"php_error_reporting"`
	PHPTimezone       *string `json:"php_timezone"`
}

// regexes for input validation
var (
	regexSizeParam = regexp.MustCompile(`^(\d{1,8})([KMG]?)$`)
	// regexTimezone bounds a date.timezone identifier to plain tz-database
	// characters before it is handed to time.LoadLocation — keeps anything
	// with a newline/quote out of the fastcgi_param PHP_VALUE line the agent
	// renders (GH #1332). Must stay in step with phpTimezoneRE in the agent.
	regexTimezone = regexp.MustCompile(`^[A-Za-z0-9_+/-]{1,64}$`)
)

// phpErrorReportingMax is E_ALL in PHP 8. error_reporting is a bitmask, so any
// value in [0, 32767] is a legal combination (0 = report nothing).
const phpErrorReportingMax = 32767

// validateErrorReporting bounds the error_reporting bitmask to [0, E_ALL].
func validateErrorReporting(v int) error {
	if v < 0 || v > phpErrorReportingMax {
		return errInvalidPHPSetting("error_reporting out of range (0..32767)")
	}
	return nil
}

// validateTimezone accepts only a real tz-database identifier. Character-class
// first (cheap + injection guard), then time.LoadLocation confirms PHP will
// accept it (both read the same IANA tz data).
func validateTimezone(s string) error {
	if !regexTimezone.MatchString(s) {
		return errInvalidPHPSetting("invalid timezone format")
	}
	if _, err := time.LoadLocation(s); err != nil {
		return errInvalidPHPSetting("unknown timezone")
	}
	return nil
}

// validateSizeParam validates memory_limit, upload_max_filesize, post_max_size.
// Regex: ^\d+[KMG]?$ (case-insensitive), max 8 chars total, no special characters.
func validateSizeParam(s string) error {
	if len(s) > 8 {
		return errInvalidPHPSetting("too long (max 8 chars)")
	}
	if !regexSizeParam.MatchString(s) {
		return errInvalidPHPSetting("invalid format (digits + optional K/M/G)")
	}
	return nil
}

// validateIntParam validates max_input_vars, max_execution_time, max_input_time.
// Range: 1..86400 seconds (or vars).
func validateIntParam(i int, fieldName string) error {
	if i < 1 || i > 86400 {
		return errInvalidPHPSetting("out of range (1..86400)")
	}
	return nil
}

type errInvalidPHPSetting string

func (e errInvalidPHPSetting) Error() string {
	return "invalid_php_setting: " + string(e)
}

func (h *domainPHPSettingsHandler) get(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx := c.Request.Context()
	domainID := c.Param("id")

	dom, err := h.cfg.Domains.FindByID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(ctx, "get php-settings: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Owner or admin check
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	resp := getDomainPHPSettingsResponse{
		PHPPoolID:            dom.PHPPoolID,
		PHPMemoryLimit:       dom.PHPMemoryLimit,
		PHPUploadMaxFilesize: dom.PHPUploadMaxFilesize,
		PHPPostMaxSize:       dom.PHPPostMaxSize,
		PHPMaxInputVars:      dom.PHPMaxInputVars,
		PHPMaxExecutionTime:  dom.PHPMaxExecutionTime,
		PHPMaxInputTime:      dom.PHPMaxInputTime,
		PHPDisplayErrors:     dom.PHPDisplayErrors,
		PHPErrorReporting:    dom.PHPErrorReporting,
		PHPTimezone:          dom.PHPTimezone,
	}

	// Resolve the effective PHP version + the pool itself. If the domain is
	// bound to a user pool, use that pool. If unbound, fall back to the user's
	// own pool (ADR-0023: one pool per user). If neither exists, leave nil and
	// the UI renders "Server default".
	var pool *models.PHPPool
	if dom.PHPPoolID != nil && *dom.PHPPoolID != "" {
		pool, _ = h.cfg.PHPPools.FindByID(ctx, *dom.PHPPoolID)
	} else {
		pool, _ = h.cfg.PHPPools.FindByUserID(ctx, dom.UserID)
	}
	if pool != nil {
		v := pool.PHPVersion
		resp.PHPVersion = &v
		// GH #1543: the value this domain inherits per directive — pool ini
		// override if set, else the box php.ini baseline for the version. Best
		// effort: any failure just omits pool_defaults (the UI keeps a generic
		// "pool default" label rather than a wrong number).
		resp.PoolDefaults = h.resolvePoolDefaults(ctx, pool)
	}

	c.JSON(http.StatusOK, resp)
}

// poolIniDefaultCacheTTL keeps the per-version agent read rare — the box
// php.ini baseline changes only when the operator retunes it, so a short cache
// spares an agent round-trip on every settings-page load.
const poolIniDefaultCacheTTL = 5 * time.Minute

type cachedIniDefaults struct {
	at   time.Time
	vals map[string]string
}

var (
	poolIniDefaultMu    sync.Mutex
	poolIniDefaultCache = map[string]cachedIniDefaults{}
)

// resolvePoolDefaults returns the effective inherited value per directive for a
// pool: the box php.ini baseline for the pool's PHP version (read via the agent,
// cached per version) with the pool's own ini overrides layered on top. Returns
// nil on any failure so the caller omits the hint.
func (h *domainPHPSettingsHandler) resolvePoolDefaults(ctx context.Context, pool *models.PHPPool) map[string]string {
	if h.cfg.Agent == nil {
		return nil
	}
	base := h.phpIniDefaults(ctx, pool.PHPVersion)
	if base == nil {
		return nil
	}
	// Copy the cached baseline before mutating — the cache entry is shared.
	out := make(map[string]string, len(base))
	for k, v := range base {
		out[k] = v
	}
	// Overlay the pool's own value-kind ini overrides (a flag-kind override —
	// on/off — is not one of these numeric/size directives).
	if h.cfg.PoolIniOverrides != nil {
		if ovs, err := h.cfg.PoolIniOverrides.ListByPool(ctx, pool.ID); err == nil {
			for i := range ovs {
				if ovs[i].Kind == "value" {
					if _, tracked := out[ovs[i].Directive]; tracked {
						out[ovs[i].Directive] = ovs[i].Value
					}
				}
			}
		}
	}
	return out
}

// phpIniDefaults reads (and caches) the box FPM php.ini baseline for a PHP
// version via the agent's php.ini_defaults command.
func (h *domainPHPSettingsHandler) phpIniDefaults(ctx context.Context, version string) map[string]string {
	if version == "" {
		return nil
	}
	poolIniDefaultMu.Lock()
	if c, ok := poolIniDefaultCache[version]; ok && time.Since(c.at) < poolIniDefaultCacheTTL {
		poolIniDefaultMu.Unlock()
		return c.vals
	}
	poolIniDefaultMu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(cctx, "php.ini_defaults", map[string]any{"php_version": version})
	if err != nil {
		return nil
	}
	var resp struct {
		Defaults map[string]string `json:"defaults"`
	}
	if json.Unmarshal(raw, &resp) != nil || resp.Defaults == nil {
		return nil
	}
	poolIniDefaultMu.Lock()
	poolIniDefaultCache[version] = cachedIniDefaults{at: time.Now(), vals: resp.Defaults}
	poolIniDefaultMu.Unlock()
	return resp.Defaults
}

func (h *domainPHPSettingsHandler) patch(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req updateDomainPHPSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	// Validate each field
	if req.PHPMemoryLimit != nil {
		if err := validateSizeParam(*req.PHPMemoryLimit); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPUploadMaxFilesize != nil {
		if err := validateSizeParam(*req.PHPUploadMaxFilesize); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPPostMaxSize != nil {
		if err := validateSizeParam(*req.PHPPostMaxSize); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPMaxInputVars != nil {
		if err := validateIntParam(*req.PHPMaxInputVars, "max_input_vars"); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPMaxExecutionTime != nil {
		if err := validateIntParam(*req.PHPMaxExecutionTime, "max_execution_time"); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPMaxInputTime != nil {
		if err := validateIntParam(*req.PHPMaxInputTime, "max_input_time"); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPErrorReporting != nil {
		if err := validateErrorReporting(*req.PHPErrorReporting); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPTimezone != nil {
		if err := validateTimezone(*req.PHPTimezone); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PHPVersion != nil {
		if !isVersionSupported(*req.PHPVersion) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_php_version"})
			return
		}
	}

	ctx := c.Request.Context()
	domainID := c.Param("id")

	dom, err := h.cfg.Domains.FindByID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(ctx, "patch php-settings: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Owner or admin check
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Update settings
	settings := repository.DomainPHPSettings{
		MemoryLimit:       req.PHPMemoryLimit,
		UploadMaxFilesize: req.PHPUploadMaxFilesize,
		PostMaxSize:       req.PHPPostMaxSize,
		MaxInputVars:      req.PHPMaxInputVars,
		MaxExecutionTime:  req.PHPMaxExecutionTime,
		MaxInputTime:      req.PHPMaxInputTime,
		DisplayErrors:     req.PHPDisplayErrors,
		ErrorReporting:    req.PHPErrorReporting,
		Timezone:          req.PHPTimezone,
	}

	if err := h.cfg.Domains.UpdatePHPSettings(ctx, domainID, settings); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(ctx, "patch php-settings: update", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// PHPVersion change: update the DOMAIN OWNER's pool (one pool per
	// user) and mark it pending so the reconciler re-applies the FPM
	// config with the new version. This affects every domain the user
	// owns, not just this one — see the type-level comment.
	if req.PHPVersion != nil && h.cfg.PHPPools != nil {
		pool, perr := h.cfg.PHPPools.FindByUserID(ctx, dom.UserID)
		if perr != nil && !errors.Is(perr, repository.ErrNotFound) {
			slog.ErrorContext(ctx, "patch php-settings: load user pool", "error", perr, "user_id", dom.UserID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if pool != nil && pool.PHPVersion != *req.PHPVersion {
			// JAB-174: if the user already has a separate per-version pool for
			// the target version (per-domain binding, GH #329), switching the
			// default row to it collides with uniq_user_phpver → a raw 500.
			// Return a clean, actionable 409 instead.
			if other, ferr := h.cfg.PHPPools.FindByUserAndVersion(ctx, dom.UserID, *req.PHPVersion); ferr == nil && other != nil && other.ID != pool.ID {
				c.JSON(http.StatusConflict, gin.H{
					"error":  "version_pool_exists",
					"detail": fmt.Sprintf("this user already has a separate PHP %s pool (bound to specific domains); delete it or rebind its domains before switching the default to %s", *req.PHPVersion, *req.PHPVersion),
				})
				return
			} else if ferr != nil && !errors.Is(ferr, repository.ErrNotFound) {
				slog.ErrorContext(ctx, "patch php-settings: check version pool", "error", ferr, "user_id", dom.UserID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			pool.PHPVersion = *req.PHPVersion
			pool.Status = "pending"
			if uerr := h.cfg.PHPPools.Update(ctx, pool); uerr != nil {
				slog.ErrorContext(ctx, "patch php-settings: update pool", "error", uerr, "pool_id", pool.ID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
		}
	}

	// Trigger reconciler to re-provision this domain
	// (Placeholder: caller should pass Reconciler and invoke ReconcileOne)
	// For now, return 200 OK.

	c.JSON(http.StatusOK, gin.H{"success": true})
}
