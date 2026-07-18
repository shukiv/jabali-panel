// applications_cache_settings.go — GH #612/#616/#618. Read/write per-install
// cache tuning (object/page split, TTL, profile, URL exclusions, cookie
// bypass), stored in application_installs.cache_settings (JSON). The Cache
// settings Drawer in the SPA is the consumer; the values flow into the nginx
// page-cache gate at reconcile time (see the vhost template) and into the
// object/page enable split. Owner-scoped exactly like the cache toggle.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	cacheMaxRules    = 50  // cap URL exclusions / cookie bypass entries
	cacheMaxRuleLen  = 128 // per-entry length cap
	cacheSettingsMax = 8192
)

// getCacheSettings returns the install's parsed settings (defaults when unset).
func (h *wordPressHandler) getCacheSettings(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	inst := h.loadOwnedInstall(c, claims)
	if inst == nil {
		return
	}
	data, configured := inst.ParseCacheSettings()
	c.JSON(http.StatusOK, gin.H{
		"cache_enabled": inst.CacheEnabled,
		"configured":    configured,
		"settings":      data,
	})
}

// setCacheSettings validates + persists the settings, then schedules a reconcile
// so the nginx gate re-renders with the new rules.
func (h *wordPressHandler) setCacheSettings(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	inst := h.loadOwnedInstall(c, claims)
	if inst == nil {
		return
	}

	var body models.CacheSettingsData
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if !validRuleList(body.URLExclusions) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_rules"})
		return
	}

	raw, err := json.Marshal(body)
	if err != nil || len(raw) > cacheSettingsMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "settings_too_large"})
		return
	}
	// GH #618: audit trail for cache-profile / safety changes — records WHO set
	// WHICH profile on which install (the #658 path-level audit mechanism), so a
	// risky override (e.g. a brochure profile on a shop) is traceable.
	prof := body.Profile
	if prof == "" {
		prof = "default"
	}
	c.Set("audit_target", inst.ID+" cache-profile="+prof)
	c.Set("audit_target_type", "cache_settings")
	if err := h.cfg.ApplicationInstalls.UpdateCacheSettings(c.Request.Context(), inst.ID, raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}

	// GH #612: re-apply so the new object max-TTL / page-TTL take effect now, not
	// only on the next cache toggle. Only when the install is cache-enabled;
	// non-fatal (settings are already persisted, and the reconcile below still
	// re-renders the page gate).
	if inst.CacheEnabled {
		if err := h.setCacheCore(c.Request.Context(), inst.ID, true, claims.IsAdmin, claims.UserID); err != nil {
			slog.WarnContext(c.Request.Context(), "cache: re-apply after settings save", "err", err, "install_id", inst.ID)
		}
	}
	// Re-render the vhost so the new gate/TTL take effect (page cache only).
	if h.cfg.Reconciler != nil {
		if dom, dErr := h.cfg.Domains.FindByID(c.Request.Context(), inst.DomainID); dErr == nil && dom != nil {
			h.cfg.Reconciler.Schedule(dom.ID)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "settings": body})
}

// loadOwnedInstall resolves the :id install scoped to the caller (admins see
// any), 404s otherwise. Mirrors setCacheCore's ownership rule.
func (h *wordPressHandler) loadOwnedInstall(c *gin.Context, claims *auth.AccessClaims) *models.ApplicationInstall {
	id := c.Param("id")
	var inst *models.ApplicationInstall
	var err error
	if claims.IsAdmin {
		inst, err = h.cfg.ApplicationInstalls.FindByID(c.Request.Context(), id)
	} else {
		inst, err = h.cfg.ApplicationInstalls.FindByIDAndUserID(c.Request.Context(), id, claims.UserID)
	}
	if err != nil || inst == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "install_not_found"})
		return nil
	}
	if inst.AppType != "wordpress" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cache_only_for_wordpress"})
		return nil
	}
	return inst
}

// cacheRuleRE mirrors the agent's bypassPathRE (domain_create.go) so the API
// REJECTS a URL exclusion the agent would silently drop at render time, instead
// of persisting it and confusing the operator (GH #635). Must be an absolute
// path of safe chars.
var cacheRuleRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,127}$`)

func validRuleList(list []string) bool {
	if len(list) > cacheMaxRules {
		return false
	}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > cacheMaxRuleLen || !cacheRuleRE.MatchString(s) {
			return false
		}
	}
	return true
}

// cacheWarmup (GH #615) crawls the install's site (sitemap + homepage) to
// pre-populate the nginx page cache, so operators don't wait for organic
// traffic. Fire-and-forget with a detached context; returns 202 immediately.
func (h *wordPressHandler) cacheWarmup(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	inst := h.loadOwnedInstall(c, claims)
	if inst == nil {
		return
	}
	if !inst.CacheEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cache_not_enabled"})
		return
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), inst.DomainID)
	if err != nil || dom == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	agent, host, installID := h.cfg.Agent, dom.Name, inst.ID
	osUser := ""
	if u, uErr := h.cfg.Users.FindByID(c.Request.Context(), inst.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	trimPath := dom.DocRoot
	if inst.Subdirectory != "" {
		trimPath = path.Join(dom.DocRoot, inst.Subdirectory)
	}
	repo := h.cfg.ApplicationInstalls
	runsRepo := h.cfg.CacheWarmupRuns
	go func() {
		wctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		// JAB-95 Phase 3: record a durable run (running -> done/failed).
		var run *models.CacheWarmupRun
		if runsRepo != nil {
			started := time.Now().UTC()
			run = &models.CacheWarmupRun{ID: ids.NewULID(), InstallID: installID, Host: host, State: models.CacheWarmupStateRunning, Requested: 100, StartedAt: &started}
			_ = runsRepo.Create(context.Background(), run)
		}
		res, err := agent.Call(wctx, "nginx.cache_warmup", map[string]any{"host": host, "max_urls": 100})
		if run != nil {
			finalizeCacheWarmupRun(run, res, err)
			_ = runsRepo.Update(context.Background(), run)
		}
		// GH #612: enforce the object-cache budget after warmup (which adds keys).
		if osUser != "" {
			// JAB-59: surface trim failures instead of swallowing them — the
			// budget is only actually enforced if this succeeds.
			if _, terr := agent.Call(wctx, "wordpress.cache_trim", map[string]any{"os_user": osUser, "install_path": trimPath}); terr != nil {
				slog.WarnContext(wctx, "cache: post-warmup redis budget trim failed", "err", terr, "os_user", osUser, "install", trimPath)
			}
		}
		// GH #615: record a lightweight last-warmup status on the install.
		rec := models.WarmupRecord{At: time.Now().UTC().Format(time.RFC3339)}
		if err != nil {
			rec.Note = "failed"
		} else {
			var out struct {
				Warmed int `json:"warmed"`
				URLs   int `json:"urls"`
			}
			_ = json.Unmarshal(res, &out)
			rec.URLs = out.Warmed + out.URLs
			rec.Note = "ok"
		}
		if cur, gErr := repo.FindByID(context.Background(), installID); gErr == nil && cur != nil {
			settings, _ := cur.ParseCacheSettings()
			settings.LastWarm = &rec
			if raw, mErr := json.Marshal(settings); mErr == nil {
				_ = repo.UpdateCacheSettings(context.Background(), installID, raw)
			}
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "warming": host})
}

// finalizeCacheWarmupRun stamps a run from the agent's nginx.cache_warmup
// response (JAB-95 Phase 1 stats). A transport error -> failed; otherwise done
// with the warmed/attempted/failed counts. (JAB-95 Phase 3)
func finalizeCacheWarmupRun(run *models.CacheWarmupRun, raw json.RawMessage, callErr error) {
	now := time.Now().UTC()
	run.FinishedAt = &now
	if callErr != nil {
		run.State = models.CacheWarmupStateFailed
		run.FirstError = truncateWarmupErr(callErr.Error())
		return
	}
	var r cacheWarmupResult
	if json.Unmarshal(raw, &r) == nil {
		run.Requested = r.Requested
		run.ClampedTo = r.ClampedTo
		run.Total = r.Attempted
		run.CursorPos = r.Attempted
		run.Attempted = r.Attempted
		run.Warmed = r.Warmed
		run.Skipped = r.Skipped
		run.Failed = r.Failed
		run.FirstError = truncateWarmupErr(r.FirstError)
	}
	run.State = models.CacheWarmupStateDone
}

func truncateWarmupErr(s string) string {
	if len(s) > 1024 {
		return s[:1024]
	}
	return s
}

// getCacheWarmup returns the install's most recent warmup run for the UI
// (JAB-95 Phase 3). Null run when none has ever run.
func (h *wordPressHandler) getCacheWarmup(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	inst := h.loadOwnedInstall(c, claims)
	if inst == nil {
		return
	}
	if h.cfg.CacheWarmupRuns == nil {
		c.JSON(http.StatusOK, gin.H{"run": nil})
		return
	}
	run, err := h.cfg.CacheWarmupRuns.LatestForInstall(c.Request.Context(), inst.ID)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusOK, gin.H{"run": nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "warmup_lookup_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": run})
}

// cacheProfiles returns the static cache-profile registry for the UI (GH #618).
func (h *wordPressHandler) cacheProfiles(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"profiles": models.CacheProfiles})
}

// cacheStats (GH #617) returns the install's Redis cache stats (hit ratio, key
// count, memory) by asking the agent to run `wp jabali-cache stats` as tenant.
func (h *wordPressHandler) cacheStats(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	inst := h.loadOwnedInstall(c, claims)
	if inst == nil {
		return
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), inst.DomainID)
	if err != nil || dom == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return
	}
	osUser := ""
	if u, uErr := h.cfg.Users.FindByID(c.Request.Context(), inst.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" || h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	installPath := dom.DocRoot
	if inst.Subdirectory != "" {
		installPath = path.Join(dom.DocRoot, inst.Subdirectory)
	}
	res, err := h.cfg.Agent.Call(c.Request.Context(), "wordpress.cache_stats", map[string]any{
		"os_user": osUser, "install_path": installPath,
	})
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var stats map[string]any
	if uErr := json.Unmarshal(res, &stats); uErr != nil || stats == nil {
		stats = map[string]any{}
	}
	// GH #617: the tenant ACL can't run INFO, so fill the server-wide hit ratio +
	// memory from the panel's privileged Redis client. ADMIN-ONLY: these numbers
	// are Redis-instance-wide (shared across all tenants), so exposing them to a
	// tenant would leak other tenants' aggregate cache activity (used_memory
	// deltas). Tenants see only their own per-prefix stats (keys/connection).
	if claims.IsAdmin && h.cfg.Redis != nil {
		if info, iErr := h.cfg.Redis.Info(c.Request.Context(), "stats", "memory").Result(); iErr == nil {
			hits := infoInt(info, "keyspace_hits")
			miss := infoInt(info, "keyspace_misses")
			if hits+miss > 0 {
				stats["hit_ratio"] = math.Round(float64(hits)/float64(hits+miss)*1000) / 10
			}
			stats["used_memory"] = infoInt(info, "used_memory")
		}
	}
	// GH #617: nginx page-cache HIT/MISS for THIS domain (the owner's own domain,
	// not cross-tenant), so it's visible to the tenant.
	if h.cfg.Agent != nil {
		if pres, perr := h.cfg.Agent.Call(c.Request.Context(), "nginx.cache.stats", map[string]any{"domain": dom.Name}); perr == nil {
			var pageStats map[string]any
			if json.Unmarshal(pres, &pageStats) == nil && pageStats != nil {
				stats["page_cache"] = pageStats
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

// infoInt extracts an integer field from a Redis INFO text block.
func infoInt(info, key string) int {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:(\d+)`)
	if m := re.FindStringSubmatch(info); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// recommendCacheProfile maps a site's active plugins to a cache profile + a
// safe default enable set (GH #620).
func recommendCacheProfile(plugins []string) (profile string, note string) {
	set := map[string]bool{}
	for _, pl := range plugins {
		set[strings.ToLower(strings.TrimSpace(pl))] = true
	}
	switch {
	case set["woocommerce"]:
		return "woocommerce", "WooCommerce detected — cart, checkout, and account pages will always bypass the cache."
	case set["easy-digital-downloads"]:
		return "edd", "Easy Digital Downloads detected — checkout and purchase pages will bypass."
	case set["memberpress"] || set["learndash"] || set["lifterlms"] || set["paid-memberships-pro"] || set["restrict-content-pro"] || set["buddypress"]:
		return "membership_lms", "Membership/LMS plugin detected — member and course pages will bypass."
	default:
		return "", "Brochure/content site — aggressive page caching is safe; only wp-admin/login bypass."
	}
}

// cacheAdvise (GH #620) probes the install and recommends a profile + settings.
// cacheHealth (JAB-11) runs the agent's per-install drift check
// (`wp jabali-cache verify` as the tenant, which fails if the plugin is
// inactive, the drop-in / JABALI_CACHE_* constants are gone, or the Redis ACL /
// socket is unreachable). Read-only: it detects the drift the DB's
// cache_enabled=true flag silently lies about; repair stays on the operator
// cache-doctor path. Bounded to 45s so a single slow install can't hang the
// request past the proxy timeout (long-agent-call scar).
func (h *wordPressHandler) cacheHealth(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	inst := h.loadOwnedInstall(c, claims)
	if inst == nil {
		return
	}
	if inst.AppType != "wordpress" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "not_wordpress"})
		return
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), inst.DomainID)
	if err != nil || dom == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return
	}
	osUser := ""
	if u, uErr := h.cfg.Users.FindByID(c.Request.Context(), inst.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" || h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	installPath := dom.DocRoot
	if inst.Subdirectory != "" {
		installPath = path.Join(dom.DocRoot, inst.Subdirectory)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	res, err := h.cfg.Agent.Call(ctx, "wordpress.cache_health", map[string]any{
		"os_user": osUser, "install_path": installPath, "host": dom.Name,
	})
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var health struct {
		Healthy bool   `json:"healthy"`
		Detail  string `json:"detail"`
	}
	if uErr := json.Unmarshal(res, &health); uErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "unparseable_health"})
		return
	}
	// cache_enabled is the DB's claim; healthy is the live truth. When they
	// disagree the site is drifted — the field the GUI badges.
	c.JSON(http.StatusOK, gin.H{
		"install_id":    inst.ID,
		"cache_enabled": inst.CacheEnabled,
		"healthy":       health.Healthy,
		"drifted":       inst.CacheEnabled && !health.Healthy,
		"detail":        health.Detail,
	})
}

func (h *wordPressHandler) cacheAdvise(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	inst := h.loadOwnedInstall(c, claims)
	if inst == nil {
		return
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), inst.DomainID)
	if err != nil || dom == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return
	}
	osUser := ""
	if u, uErr := h.cfg.Users.FindByID(c.Request.Context(), inst.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" || h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	installPath := dom.DocRoot
	if inst.Subdirectory != "" {
		installPath = path.Join(dom.DocRoot, inst.Subdirectory)
	}
	res, err := h.cfg.Agent.Call(c.Request.Context(), "wordpress.cache_probe", map[string]any{
		"os_user": osUser, "install_path": installPath, "host": dom.Name,
	})
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var probe struct {
		ActivePlugins   []string `json:"active_plugins"`
		WPVersion       string   `json:"wp_version"`
		TTFBMs          int      `json:"ttfb_ms"`
		PageHitVerified bool     `json:"page_hit_verified"`
		RiskyEndpoints  []string `json:"risky_endpoints"`
	}
	_ = json.Unmarshal(res, &probe)
	profile, note := recommendCacheProfile(probe.ActivePlugins)
	// GH #620: extra findings folded into the note.
	if len(probe.RiskyEndpoints) > 0 {
		note += " Detected dynamic endpoints (" + strings.Join(probe.RiskyEndpoints, ", ") + ") — these must stay bypassed; the chosen profile already excludes them."
	}
	if inst.CacheEnabled && !probe.PageHitVerified {
		note += " Warning: the homepage did not return a page-cache HIT on a repeat request — caching may be bypassed (a cookie/plugin) or not yet warmed."
	}
	// Redis eviction pressure (admin-privileged INFO).
	evicted := 0
	if claims.IsAdmin && h.cfg.Redis != nil {
		if info, iErr := h.cfg.Redis.Info(c.Request.Context(), "stats").Result(); iErr == nil {
			evicted = infoInt(info, "evicted_keys")
			if evicted > 0 {
				note += " Redis is evicting keys under memory pressure (evicted_keys>0) — object-cache entries may be dropped early; consider a smaller max-TTL or more Redis memory."
			}
		}
	}
	// Store the diagnostic on the install so it persists (GH #620).
	settings, _ := inst.ParseCacheSettings()
	settings.Diagnostic = &models.CacheDiagnostic{
		RecommendedProfile: profile, Note: note, TTFBMs: probe.TTFBMs,
		PageHitVerified: probe.PageHitVerified, RiskyEndpoints: probe.RiskyEndpoints,
		RedisEvictedKeys: evicted, WPVersion: probe.WPVersion,
	}
	if raw, mErr := json.Marshal(settings); mErr == nil {
		_ = h.cfg.ApplicationInstalls.UpdateCacheSettings(c.Request.Context(), inst.ID, raw)
	}
	c.JSON(http.StatusOK, gin.H{
		"recommended": gin.H{
			"profile":              profile,
			"object_cache_enabled": true,
			"page_cache_enabled":   true,
			"note":                 note,
		},
		"probe": gin.H{
			"active_plugins": probe.ActivePlugins, "wp_version": probe.WPVersion,
			"ttfb_ms": probe.TTFBMs, "page_hit_verified": probe.PageHitVerified,
			"risky_endpoints": probe.RiskyEndpoints, "redis_evicted_keys": evicted,
		},
	})
}

// cacheOverview (GH #617, admin) lists cache-enabled domains ranked by their
// nginx page-cache hit ratio, so operators can spot low-hit / high-bypass /
// noisy domains. Per-domain page-cache stats are not cross-tenant, and this is
// admin-gated anyway. Concurrent, bounded.
func (h *wordPressHandler) cacheOverview(c *gin.Context) {
	ctx := c.Request.Context()
	insts, err := h.cfg.ApplicationInstalls.ListAllCacheEnabled(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	// Dedup to distinct domains (a domain may host several installs; the page
	// cache log is per-domain).
	seen := map[string]bool{}
	names := []string{}
	for i := range insts {
		if dom, dErr := h.cfg.Domains.FindByID(ctx, insts[i].DomainID); dErr == nil && dom != nil && !seen[dom.Name] {
			seen[dom.Name] = true
			names = append(names, dom.Name)
		}
	}
	type overviewRow struct {
		Domain    string  `json:"domain"`
		HitRatio  float64 `json:"hit_ratio"`
		Total     int     `json:"total"`
		Bypass    int     `json:"bypass"`
		Available bool    `json:"available"`
	}
	rows := make([]overviewRow, len(names))
	if h.cfg.Agent != nil {
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for i, name := range names {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, name string) {
				defer wg.Done()
				defer func() { <-sem }()
				r := overviewRow{Domain: name}
				sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				if res, cErr := h.cfg.Agent.Call(sctx, "nginx.cache.stats", map[string]any{"domain": name}); cErr == nil {
					var st struct {
						Available bool    `json:"available"`
						HitRatio  float64 `json:"hit_ratio"`
						Total     int     `json:"total"`
						Bypass    int     `json:"bypass"`
					}
					if json.Unmarshal(res, &st) == nil {
						r.Available, r.HitRatio, r.Total, r.Bypass = st.Available, st.HitRatio, st.Total, st.Bypass
					}
				}
				rows[i] = r
			}(i, name)
		}
		wg.Wait()
	}
	// Rank: domains WITH traffic + a low hit ratio first (the ones needing
	// attention); no-traffic / unavailable sink to the bottom.
	sort.Slice(rows, func(i, j int) bool {
		ai := rows[i].Available && rows[i].Total > 0
		aj := rows[j].Available && rows[j].Total > 0
		if ai != aj {
			return ai
		}
		if rows[i].HitRatio != rows[j].HitRatio {
			return rows[i].HitRatio < rows[j].HitRatio
		}
		return rows[i].Total > rows[j].Total
	})
	// GH #633: server-wide FastCGI cache zone size.
	var sizeInfo any
	if h.cfg.Agent != nil {
		if res, sErr := h.cfg.Agent.Call(ctx, "nginx.cache.size", nil); sErr == nil {
			var m map[string]any
			if json.Unmarshal(res, &m) == nil {
				sizeInfo = m
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"domains": rows, "cache_size": sizeInfo})
}
