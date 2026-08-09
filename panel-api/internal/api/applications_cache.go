// applications_cache.go — GH #406. PUT /applications/:id/cache {enabled}.
//
// A single owner-scoped switch that drives BOTH layers of caching for a
// WordPress install:
//   - the Redis OBJECT cache (the bundled jabali-cache plugin), gated by a
//     per-tenant Redis ACL user wp_<osuser> scoped ~jc:<osuser>:* (ADR-0148); and
//   - the nginx FastCGI page cache for the app's domain (ADR-0108), via the same
//     domains.cache_enabled flag the More-menu Caching toggle writes.
//
// The per-tenant ACL token is derived (HMAC(CacheTokenSecret, osuser)) so it is
// STABLE across re-enables and across multiple installs of the same tenant — we
// never have to store or recover it, and re-running ACL SETUSER is idempotent.
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type setCacheRequest struct {
	Enabled bool `json:"enabled"`
}

// cacheTenantToken derives the per-tenant Redis ACL token from the global
// secret, the OS user, AND a persisted per-tenant salt (Gitea #415). The salt
// makes the token non-deterministic from (secret, osuser) alone, so a single
// tenant can be rotated (regenerate its salt) without rotating the global
// secret and breaking every other tenant.
// cacheInstallToken derives the PER-INSTALL Redis ACL token (JAB-62). Including
// installID in the HMAC input means each install gets a distinct credential, so a
// compromised install's stamped token can't authenticate as a sibling.
func cacheInstallToken(secret, osUser, installID, salt string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("wp-cache-install:" + osUser + ":" + installID + ":" + salt))
	return hex.EncodeToString(m.Sum(nil))
}

// installACLUser is the per-install Redis ACL username (JAB-62). MUST match the
// agent's derivation from p.Prefix (wp_<osuser>_<installID>) exactly, or the
// bundled object cache can't authenticate.
func installACLUser(osUser, installID string) string { return "wp_" + osUser + "_" + installID }

// installACLKeyPattern fences an ACL user to ONE install's keyspace. NOT the
// broad ~jc:<osUser>:* — that was the JAB-62 cross-install hole.
func installACLKeyPattern(osUser, installID string) string {
	return "~jc:" + osUser + ":" + installID + ":*"
}

// cachePathFromSubdir maps a WP install subdirectory to the nginx page-cache
// path prefix (Gitea #420). "" (root install) → "/" (whole domain); "blog" or
// "/blog/" → "/blog".
func cachePathFromSubdir(subdir string) string {
	s := strings.Trim(strings.TrimSpace(subdir), "/")
	if s == "" {
		return "/"
	}
	return "/" + s
}

// revokeTenantRedisACL removes the per-tenant wp_<osUser> Redis ACL user and
// persists the aclfile (GH #408 / ADR-0148 Lifecycle). Idempotent — DELUSER on
// an absent user is a no-op. The caller must ensure the tenant has no remaining
// cache-enabled install, because wp_<osUser> is shared across a tenant's
// installs. Used by both the cache-disable path and the user-delete cascade.
func revokeTenantRedisACL(ctx context.Context, rdb *redis.Client, osUser string) error {
	if rdb == nil || osUser == "" {
		return nil
	}
	if err := rdb.Do(ctx, "ACL", "DELUSER", "wp_"+osUser).Err(); err != nil {
		return err
	}
	return rdb.Do(ctx, "ACL", "SAVE").Err()
}

// revokeInstallACL removes ONE install's per-install ACL user (JAB-62). Idempotent
// (DELUSER of an absent user is a no-op). Unlike revokeTenantRedisACL, no
// sibling-coordination is needed — each install owns its own ACL user.
func revokeInstallACL(ctx context.Context, rdb *redis.Client, osUser, installID string) error {
	if rdb == nil || osUser == "" || installID == "" {
		return nil
	}
	if err := rdb.Do(ctx, "ACL", "DELUSER", installACLUser(osUser, installID)).Err(); err != nil {
		return err
	}
	return rdb.Do(ctx, "ACL", "SAVE").Err()
}

// revokeAllUserCacheACLs removes EVERY cache ACL user of an OS user on the
// user-delete cascade (JAB-62): the legacy shared wp_<osUser> plus every
// per-install wp_<osUser>_<installID>. Enumerates via ACL LIST so it needs no
// install repo. The per-install match is anchored to the fixed 26-char ULID so a
// sibling account named "<osUser>_..." (whose own install users carry an extra
// _<ULID> suffix) is never caught. Idempotent + best-effort.
// ReapLegacySharedCacheACL removes the legacy per-OS-user shared cache ACL user
// (wp_<osUser>) during the JAB-62 per-install migration. Exported for the
// app-cache doctor's --migrate-acl reap. The caller MUST have re-provisioned
// EVERY one of the user's cache-enabled installs to a per-install ACL user
// first, or a not-yet-migrated sibling loses its cache (DELUSER pulls the shared
// principal it still authenticates with).
func ReapLegacySharedCacheACL(ctx context.Context, rdb *redis.Client, osUser string) error {
	return revokeTenantRedisACL(ctx, rdb, osUser)
}

func revokeAllUserCacheACLs(ctx context.Context, rdb *redis.Client, osUser string) error {
	if rdb == nil || osUser == "" {
		return nil
	}
	_ = rdb.Do(ctx, "ACL", "DELUSER", "wp_"+osUser).Err() // legacy shared user
	// GH #1016: the general per-tenant Redis credential (t_<osUser>) shares this
	// tenant's OS user, so tear it down on the same delete path — a recycled
	// username must not inherit the old principal + keyspace.
	_ = rdb.Do(ctx, "ACL", "DELUSER", tenantRedisACLUser(osUser)).Err()
	lines, err := rdb.Do(ctx, "ACL", "LIST").StringSlice()
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`^wp_` + regexp.QuoteMeta(osUser) + `_[0-9A-Z]{26}$`)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "user" && re.MatchString(fields[1]) {
			_ = rdb.Do(ctx, "ACL", "DELUSER", fields[1]).Err()
		}
	}
	return rdb.Do(ctx, "ACL", "SAVE").Err()
}

// cache toggles the WP object cache + nginx page cache for a WordPress install.
// setCache sentinel errors map the cache core's failure modes to the exact
// HTTP statuses/codes the UI depends on (GH #556 extraction — keep in sync
// with the switch in cache() and TestApplicationsCache_Characterization).
var (
	errCacheInstallNotFound  = errors.New("install_not_found")
	errCacheNotWordPress     = errors.New("cache_only_for_wordpress")
	errCacheRedisUnavailable = errors.New("redis_unavailable")
	errCacheSaltFailed       = errors.New("salt_failed")
	errCacheACLProvision     = errors.New("acl_provision_failed")
	errCacheAgentFailed      = errors.New("agent_failed")
)

// cache toggles the WP object cache + nginx page cache for a WordPress install.
// HTTP wrapper: parse request, delegate to setCacheCore, map errors → status.
func (h *wordPressHandler) cache(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req setCacheRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	err := h.setCacheCore(c.Request.Context(), c.Param("id"), req.Enabled, claims.IsAdmin, claims.UserID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"cache_enabled": req.Enabled})
	case errors.Is(err, errCacheInstallNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "install_not_found"})
	case errors.Is(err, errCacheNotWordPress):
		c.JSON(http.StatusBadRequest, gin.H{"error": "cache_only_for_wordpress"})
	case errors.Is(err, errCacheRedisUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis_unavailable"})
	case errors.Is(err, errCacheSaltFailed):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "salt_failed"})
	case errors.Is(err, errCacheACLProvision):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "acl_provision_failed"})
	case errors.Is(err, errCacheAgentFailed):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_failed"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}

// SetApplicationCache toggles an install's cache from a non-HTTP caller (CLI,
// GH #556). It runs the SAME core as the web PUT /applications/:id/cache path.
// actorUserID/isAdmin scope ownership exactly as the HTTP handler (an operator
// CLI passes isAdmin=true). Returns the errCache* sentinels so callers can
// message precisely; the periodic reconciler converges the nginx page-cache
// vhost when cfg.Reconciler is nil (the CLI path).
func SetApplicationCache(ctx context.Context, cfg ApplicationHandlerConfig, installID string, enabled, isAdmin bool, actorUserID string) error {
	return (&wordPressHandler{cfg: cfg}).setCacheCore(ctx, installID, enabled, isAdmin, actorUserID)
}

// setCacheCore is the transport-agnostic core of the cache toggle. It returns a
// sentinel error (errCache*) for the caller to map to a status/message; any
// other (wrapped) error is an internal failure (→ 500). Behavior is locked by
// TestApplicationsCache_Characterization.
func (h *wordPressHandler) setCacheCore(ctx context.Context, installID string, enabled, isAdmin bool, actorUserID string) error {
	// Ownership check (admins may toggle any install).
	var install *models.WordPressInstall
	var err error
	if isAdmin {
		install, err = h.cfg.ApplicationInstalls.FindByID(ctx, installID)
	} else {
		install, err = h.cfg.ApplicationInstalls.FindByIDAndUserID(ctx, installID, actorUserID)
	}
	if err != nil {
		if isNotFound(err) {
			return errCacheInstallNotFound
		}
		return fmt.Errorf("install lookup: %w", err)
	}
	if install.AppType != "wordpress" {
		return errCacheNotWordPress
	}

	// GH #612: behavioral cache settings (object max-TTL, page TTL) travel as
	// panel-stamped constants + the nginx page-cache TTL.
	cacheSettings, _ := install.ParseCacheSettings()
	// GH #612: object + page caches are independently controllable. The master
	// toggle (`enabled`, from the app-row Cache switch) gates both; the per-
	// install pointers then refine WHICH layer is active. An unset pointer means
	// "follow the master" (both on), preserving pre-split behavior.
	objectOn := enabled && (cacheSettings.ObjectCacheEnabled == nil || *cacheSettings.ObjectCacheEnabled)
	pageOn := enabled && (cacheSettings.PageCacheEnabled == nil || *cacheSettings.PageCacheEnabled)
	// Resolve the domain (docroot + name for nginx) and the tenant os user.
	domain, err := h.cfg.Domains.FindByID(ctx, install.DomainID)
	if err != nil {
		slog.ErrorContext(ctx, "cache: domain lookup", "err", err, "domain_id", install.DomainID)
		return fmt.Errorf("domain lookup: %w", err)
	}
	var osUser string
	if u, uErr := h.cfg.Users.FindByID(ctx, install.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" {
		slog.ErrorContext(ctx, "cache: user has no linux username", "user_id", install.UserID)
		return fmt.Errorf("user %s has no linux username", install.UserID)
	}

	// The WP install lives at <docroot>/<subdirectory> (subdir empty => docroot).
	installPath := domain.DocRoot
	if install.Subdirectory != "" {
		installPath = path.Join(domain.DocRoot, install.Subdirectory)
	}

	// Per-site prefix UNDER the per-tenant ACL namespace. The plugin wraps the
	// value as jc:<value>: ; "<osuser>:<installID>" -> jc:<osuser>:<installID>:
	// which the ACL key pattern ~jc:<osuser>:* still matches, while staying unique
	// per install so a tenant's two sites never collide.
	prefix := osUser + ":" + installID

	if objectOn {
		// 1. Provision the per-tenant ACL user BEFORE the plugin tries to auth.
		if h.cfg.Redis == nil || h.cfg.CacheTokenSecret == "" {
			return errCacheRedisUnavailable
		}
		salt := ""
		if h.cfg.CacheTokenSalts != nil {
			s2, sErr := h.cfg.CacheTokenSalts.GetOrCreate(ctx, install.UserID)
			if sErr != nil {
				slog.ErrorContext(ctx, "cache: salt get/create", "err", sErr, "user_id", install.UserID)
				return errCacheSaltFailed
			}
			salt = s2
		}
		token := cacheInstallToken(h.cfg.CacheTokenSecret, osUser, installID, salt)
		if err := h.provisionInstallACL(ctx, osUser, installID, token); err != nil {
			slog.ErrorContext(ctx, "cache: ACL provision", "err", err, "os_user", osUser, "install_id", installID)
			return errCacheACLProvision
		}
		// 2. Agent: stage plugin + write config + activate + enable, as the tenant.
		params := map[string]any{
			"install_path":   installPath,
			"os_user":        osUser,
			"enable":         true,
			"redis_db":       1,
			"prefix":         prefix,
			"redis_password": token,
			"max_ttl":        cacheSettings.ObjectMaxTTL, // GH #612 JABALI_CACHE_MAXTTL
			// nginx is the primary page cache; the WP advanced-cache page layer
			// stays off (page_cache=false) to avoid double-caching. page_ttl is
			// stamped for completeness but inert while page_cache is false.
			"page_cache": false,
			"page_ttl":   cacheSettings.PageTTL,
			"max_mem_mb": cacheSettings.RedisMaxMemoryMB,
		}
		if _, err := h.cfg.Agent.Call(ctx, "wordpress.cache_set", params); err != nil {
			slog.ErrorContext(ctx, "cache: agent enable", "err", err, "install_id", installID)
			return errCacheAgentFailed
		}
	} else {
		// Disable the plugin. Leave the ACL user in place — other installs of
		// the same tenant share wp_<osuser>; it's harmless without a config.
		params := map[string]any{
			"install_path": installPath,
			"os_user":      osUser,
			"enable":       false,
		}
		if _, err := h.cfg.Agent.Call(ctx, "wordpress.cache_set", params); err != nil {
			slog.ErrorContext(ctx, "cache: agent disable", "err", err, "install_id", installID)
			return errCacheAgentFailed
		}
	}

	// 3. Couple the nginx page cache (domains.cache_enabled, ADR-0108). Enabling
	// is additive. On DISABLE, a domain can host multiple installs (root +
	// /blog), so only flip the per-domain flag OFF when no sibling install on
	// the same domain still wants it — otherwise we'd kill page cache for a
	// site whose own switch is still ON (GH #409).
	desiredDomainCache := pageOn
	if !pageOn {
		siblings, sErr := h.cfg.ApplicationInstalls.CountCacheEnabledByDomainID(ctx, domain.ID, installID)
		if sErr != nil {
			// Conservative on error: leave the page cache as-is rather than risk
			// clobbering a sibling. Skip the domain flag write this pass.
			slog.WarnContext(ctx, "cache: count siblings on domain", "err", sErr, "domain_id", domain.ID)
			desiredDomainCache = domain.CacheEnabled
		} else if siblings > 0 {
			desiredDomainCache = true // a sibling still wants the page cache
		}
	}
	// Gitea #420: scope the page cache to this install's path, so enabling cache
	// on a /blog WP install doesn't apply WP-tuned caching to unrelated content
	// elsewhere on the domain. "/" = whole domain (root install).
	pathChanged := false
	if pageOn {
		newPath := cachePathFromSubdir(install.Subdirectory)
		pathChanged = newPath != domain.CachePath
		if perr := h.cfg.Domains.UpdateCachePath(ctx, domain.ID, newPath); perr != nil {
			slog.ErrorContext(ctx, "cache: domain cache_path", "err", perr, "domain_id", domain.ID)
		}
	}
	// GH #612: apply the per-install nginx page-cache TTL (clamped [10,86400]).
	if enabled && cacheSettings.PageTTL > 0 {
		ttl := cacheSettings.PageTTL
		if ttl < 10 {
			ttl = 10
		} else if ttl > 86400 {
			ttl = 86400
		}
		if ttl != domain.CacheTTLSeconds {
			if err := h.cfg.Domains.UpdateCacheTTL(ctx, domain.ID, ttl); err != nil {
				slog.WarnContext(ctx, "cache: domain ttl", "err", err, "domain_id", domain.ID)
			}
		}
	}
	flagChanged := desiredDomainCache != domain.CacheEnabled
	if flagChanged {
		if err := h.cfg.Domains.UpdateCacheEnabled(ctx, domain.ID, desiredDomainCache); err != nil {
			slog.ErrorContext(ctx, "cache: domain flag", "err", err, "domain_id", domain.ID)
			return fmt.Errorf("domain flag: %w", err)
		}
	}
	// GH #600: re-render the vhost when the page-cache PATH changed too, not only
	// when the enabled flag flipped. Enabling a second install (e.g. /blog) on a
	// domain whose cache is already on updates domains.cache_path in the DB but
	// left desiredDomainCache == domain.CacheEnabled, so no reconcile fired and
	// nginx kept serving the old path gate until an unrelated reconcile.
	if h.cfg.Reconciler != nil && (flagChanged || pathChanged) {
		h.cfg.Reconciler.Schedule(domain.ID) // re-render vhost + nginx reload
	}

	// GH #615: warm the page cache after enabling, once the reconcile has
	// re-rendered the vhost with the cache directives. Delayed + detached
	// (best-effort) so we don't crawl before the cache is live.
	if pageOn && h.cfg.Agent != nil {
		agent, host := h.cfg.Agent, domain.Name
		go func() {
			time.Sleep(75 * time.Second)
			wctx, wcancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer wcancel()
			raw, werr := agent.Call(wctx, "nginx.cache_warmup", map[string]any{"host": host, "max_urls": 100})
			logCacheWarmupResult(wctx, host, raw, werr)
		}()
	}

	// 4. Persist the install-level switch state.
	if err := h.cfg.ApplicationInstalls.UpdateCacheEnabled(ctx, installID, enabled); err != nil {
		slog.ErrorContext(ctx, "cache: install flag", "err", err, "install_id", installID)
		return fmt.Errorf("install flag: %w", err)
	}

	// 5. On disable, reap the per-tenant Redis ACL user once the tenant has no
	// other cache-enabled install (GH #408). wp_<osuser> is shared across a
	// tenant's installs, so it may only be DELUSER'd when this was the last —
	// otherwise siblings lose their cache. Best-effort: a revoke failure is
	// logged, never fails the disable (the toggle already succeeded).
	if !enabled {
		// JAB-62: each install owns its per-install ACL user, so revoke exactly
		// this one — no sibling coordination, siblings are untouched. Best-effort.
		if rErr := revokeInstallACL(ctx, h.cfg.Redis, osUser, installID); rErr != nil {
			slog.WarnContext(ctx, "cache: revoke install ACL", "err", rErr, "os_user", osUser, "install_id", installID)
		}
	}

	// 6. GH #615: pre-warm the freshly-enabled page cache. Fire-and-forget
	// with a background context (the HTTP request returns immediately) and a
	// short delay so the vhost reconcile applies the FastCGI cache directive
	// before we crawl. Best-effort: a warmup failure never affects the toggle.
	if enabled && desiredDomainCache && h.cfg.Agent != nil {
		host := domain.Name
		go func() {
			time.Sleep(20 * time.Second) // let the vhost reconcile land
			wctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			raw, werr := h.cfg.Agent.Call(wctx, "nginx.cache_warmup", map[string]any{"host": host})
			logCacheWarmupResult(wctx, host, raw, werr)
		}()
	}

	return nil
}

// provisionTenantACL idempotently creates/updates the per-tenant Redis ACL user
// wp_<osuser> scoped to its own jc:<osuser>:* keyspace, read/write only, no
// dangerous/admin commands. Runs as the jabali_panel client (which holds +acl).
// errObjectCacheUnavailable signals that Redis or the cache-token secret isn't
// provisioned (ADR-0148 / #595) — the caller decides whether that's a 503 (the
// toggle) or a silent skip (default-on-install, #597).
var errObjectCacheUnavailable = errors.New("object cache unavailable: redis/secret not provisioned")

// enableObjectCache provisions the per-tenant Redis ACL user, activates the WP
// object-cache plugin for one install (as the tenant, via the agent), and flips
// application_installs.cache_enabled on. Self-contained so both the /cache
// toggle and the auto-enable-on-new-WP-install path (#597) share the SAME
// security-critical steps: the per-tenant ACL fence, the cacheTenantToken
// derivation, and the wordpress.cache_set agent call. It does NOT touch the
// per-domain nginx page cache — that stays an explicit opt-in (ADR-0108).
func (h *wordPressHandler) enableObjectCache(ctx context.Context, install *models.ApplicationInstall) error {
	if h.cfg.Redis == nil || h.cfg.CacheTokenSecret == "" {
		return errObjectCacheUnavailable
	}
	domain, err := h.cfg.Domains.FindByID(ctx, install.DomainID)
	if err != nil {
		return fmt.Errorf("domain lookup: %w", err)
	}
	var osUser string
	if u, uErr := h.cfg.Users.FindByID(ctx, install.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" {
		return fmt.Errorf("user %s has no linux username", install.UserID)
	}
	installPath := domain.DocRoot
	if install.Subdirectory != "" {
		installPath = path.Join(domain.DocRoot, install.Subdirectory)
	}
	prefix := osUser + ":" + install.ID
	salt := ""
	if h.cfg.CacheTokenSalts != nil {
		s2, sErr := h.cfg.CacheTokenSalts.GetOrCreate(ctx, install.UserID)
		if sErr != nil {
			return fmt.Errorf("salt get/create: %w", sErr)
		}
		salt = s2
	}
	token := cacheInstallToken(h.cfg.CacheTokenSecret, osUser, install.ID, salt)
	if err := h.provisionInstallACL(ctx, osUser, install.ID, token); err != nil {
		return fmt.Errorf("acl provision: %w", err)
	}
	if _, err := h.cfg.Agent.Call(ctx, "wordpress.cache_set", map[string]any{
		"install_path":   installPath,
		"os_user":        osUser,
		"enable":         true,
		"redis_db":       1,
		"prefix":         prefix,
		"redis_password": token,
	}); err != nil {
		return fmt.Errorf("agent cache_set: %w", err)
	}
	if err := h.cfg.ApplicationInstalls.UpdateCacheEnabled(ctx, install.ID, true); err != nil {
		return fmt.Errorf("mark cache_enabled: %w", err)
	}
	return nil
}

func (h *wordPressHandler) provisionInstallACL(ctx context.Context, osUser, installID, token string) error {
	user := installACLUser(osUser, installID)
	// resetkeys/resetchannels make the rule absolute (idempotent re-apply); the
	// keyspace is fenced to ~jc:<osuser>:* — no access to jabali:* / automation:*.
	// Explicit command ALLOWLIST (Gitea #413) instead of the broad +@keyspace
	// category — @keyspace included RANDOMKEY/DBSIZE, which Redis ACLs do NOT
	// pattern-scope, so a tenant could enumerate other tenants' key names /
	// total key count past the ~jc:<osuser>:* fence. This is exactly the set the
	// bundled object cache issues (GET/SET/SETEX/DEL/MGET/INCRBY/DECRBY/SCAN/
	// SELECT/PING/AUTH) plus a little headroom — no RANDOMKEY, DBSIZE, KEYS, or
	// FLUSH. `reset` first makes the rule fully authoritative on re-apply, so an
	// already-provisioned user loses any prior @keyspace grant.
	if err := h.cfg.Redis.Do(ctx, "ACL", "SETUSER", user,
		"reset",
		"on", ">"+token,
		installACLKeyPattern(osUser, installID),
		"+GET", "+SET", "+SETEX", "+PSETEX", "+SETNX", "+DEL", "+UNLINK",
		"+MGET", "+MSET", "+INCR", "+INCRBY", "+DECR", "+DECRBY",
		"+EXPIRE", "+PEXPIRE", "+TTL", "+PTTL", "+PERSIST", "+TYPE", "+EXISTS",
		"+SCAN", "+SELECT", "+PING", "+AUTH", "+HELLO",
	).Err(); err != nil {
		return err
	}
	// Persist to the aclfile so it survives a redis restart.
	return h.cfg.Redis.Do(ctx, "ACL", "SAVE").Err()
}

// cacheWarmupResult mirrors the agent's nginx.cache_warmup response (JAB-95
// Phase 1). Logged so an operator can see what warmup actually did and whether
// the requested URL limit was clamped, instead of the old silent fire-and-forget.
type cacheWarmupResult struct {
	Requested  int    `json:"requested"`
	ClampedTo  int    `json:"clamped_to"`
	Attempted  int    `json:"attempted"`
	Warmed     int    `json:"warmed"`
	Skipped    int    `json:"skipped"`
	Failed     int    `json:"failed"`
	FirstError string `json:"first_error"`
	DurationMs int64  `json:"duration_ms"`
}

func logCacheWarmupResult(ctx context.Context, host string, raw json.RawMessage, err error) {
	if err != nil {
		slog.WarnContext(ctx, "cache: warmup", "err", err, "host", host)
		return
	}
	var r cacheWarmupResult
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	attrs := []any{
		"host", host, "warmed", r.Warmed, "attempted", r.Attempted, "failed", r.Failed,
		"requested", r.Requested, "clamped_to", r.ClampedTo, "duration_ms", r.DurationMs,
	}
	if r.Requested > r.ClampedTo && r.ClampedTo > 0 {
		attrs = append(attrs, "clamped", true)
	}
	if r.FirstError != "" {
		attrs = append(attrs, "first_error", r.FirstError)
	}
	slog.InfoContext(ctx, "cache: warmup done", attrs...)
}
