package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainCacheHandlerConfig holds deps for the per-domain nginx
// FastCGI micro-cache toggle (ADR-0108). Reconciler converges the
// vhost (the agent renders the cache directives from domain.create);
// the toggle itself is just DB-as-truth + a reconcile schedule, same
// shape as the SSL/Email per-domain switches.
type DomainCacheHandlerConfig struct {
	Agent      agent.AgentInterface
	Domains    repository.DomainRepository
	Reconciler DNSScheduler
}

// RegisterDomainCacheRoutes mounts the cache endpoints (ADR-0108).
func RegisterDomainCacheRoutes(g *gin.RouterGroup, cfg DomainCacheHandlerConfig) {
	if cfg.Domains == nil {
		return
	}
	h := &domainCacheHandler{cfg: cfg}
	g.GET("/domains/:id/cache", h.get)
	g.PUT("/domains/:id/cache", h.update)
	g.POST("/domains/:id/cache/purge", h.purge)
}

type domainCacheHandler struct{ cfg DomainCacheHandlerConfig }

type cacheResponse struct {
	DomainID   string `json:"domain_id"`
	DomainName string `json:"domain_name"`
	Enabled    bool   `json:"enabled"`
	TTLSeconds int    `json:"ttl_seconds"`
	// QueryAllowlist (migration 000230): param names that get their own cache
	// entry. Empty/omitted = off. Always a JSON array (never null) for the SPA.
	QueryAllowlist []string `json:"cache_query_allowlist"`
}

type cacheUpdateRequest struct {
	Enabled bool `json:"enabled"`
	// TTLSeconds (Gitea #596) optional; nil = leave the per-domain TTL unchanged.
	TTLSeconds *int `json:"ttl_seconds,omitempty"`
	// QueryAllowlist (migration 000230) optional; nil = leave unchanged, empty
	// slice = clear (feature off). Names normalized + validated server-side.
	QueryAllowlist *[]string `json:"cache_query_allowlist,omitempty"`
}

// loadAndAuth returns the domain after owner-or-admin authz; writes the
// HTTP response on failure. Mirrors domain_dnssec.go's loadAndAuth.
func (h *domainCacheHandler) loadAndAuth(c *gin.Context) *models.Domain {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil
	}
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil
	}
	return dom
}

func (h *domainCacheHandler) get(c *gin.Context) {
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	c.JSON(http.StatusOK, cacheResponse{
		DomainID: dom.ID, DomainName: dom.Name, Enabled: dom.CacheEnabled, TTLSeconds: dom.CacheTTLSeconds,
		QueryAllowlist: dom.CacheQueryAllowlistNames(),
	})
}

// update flips the cache switch. DB-as-truth: persist, then schedule a
// reconcile — the reconciler re-renders the vhost via domain.create
// with the new CacheEnabled and the agent reloads nginx. No synchronous
// agent call (unlike DNSSEC): the cache directives are vhost-rendered,
// not a separate agent op.
func (h *domainCacheHandler) update(c *gin.Context) {
	ctx := c.Request.Context()
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	var req cacheUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "details": err.Error()})
		return
	}
	if err := h.cfg.Domains.UpdateCacheEnabled(ctx, dom.ID, req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed", "details": err.Error()})
		return
	}
	ttl := dom.CacheTTLSeconds
	if req.TTLSeconds != nil {
		// Mirror the agent clamp ([10, 86400]) so the API rejects nothing but
		// stores a sane value; the agent re-clamps defensively at render time.
		ttl = *req.TTLSeconds
		if ttl <= 0 {
			ttl = 600
		} else if ttl < 10 {
			ttl = 10
		} else if ttl > 86400 {
			ttl = 86400
		}
		if err := h.cfg.Domains.UpdateCacheTTL(ctx, dom.ID, ttl); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed", "details": err.Error()})
			return
		}
	}
	// GH #643: nginx pins each entry's validity at store time, so a TTL
	// REDUCTION doesn't shorten already-cached pages — purge on reduction so
	// stale content can't linger for up to the old (longer) TTL.
	if req.TTLSeconds != nil && ttl < dom.CacheTTLSeconds && h.cfg.Agent != nil {
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _ = h.cfg.Agent.Call(pctx, "nginx.cache.purge", map[string]any{"domain": dom.Name})
		cancel()
	}
	// Query-param allowlist (migration 000230). nil = unchanged; a supplied
	// list (incl. empty = clear) is normalized + validated, then persisted.
	allowNames := dom.CacheQueryAllowlistNames()
	if req.QueryAllowlist != nil {
		csv, err := normalizeCacheQueryAllowlist(*req.QueryAllowlist)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query_allowlist", "details": err.Error()})
			return
		}
		if err := h.cfg.Domains.UpdateCacheQueryAllowlist(ctx, dom.ID, csv); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed", "details": err.Error()})
			return
		}
		allowNames = splitAllowlistCSV(csv)
	}
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(dom.ID)
	}
	c.JSON(http.StatusOK, cacheResponse{
		DomainID: dom.ID, DomainName: dom.Name, Enabled: req.Enabled, TTLSeconds: ttl,
		QueryAllowlist: allowNames,
	})
}

// cacheQueryParamRe bounds an allowlist entry to a safe query-param name:
// lower-case alnum + underscore, 1-32 chars. This is ALSO the guard that keeps
// the name safe to interpolate into the nginx regex + $arg_<name> in Step 2
// (no regex metacharacters can appear), so do not loosen it without revisiting
// the render.
var cacheQueryParamRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

const cacheQueryAllowlistMax = 8

// normalizeCacheQueryAllowlist lower-cases, validates, de-dups, sorts, and caps
// the supplied param names, returning the comma-joined storage form ("" = off).
// Faceted/combinatorial params multiply cache entries, so the set is
// deliberately bounded.
func normalizeCacheQueryAllowlist(in []string) (string, error) {
	seen := make(map[string]struct{}, len(in))
	var names []string
	for _, raw := range in {
		n := strings.ToLower(strings.TrimSpace(raw))
		if n == "" {
			continue
		}
		if !cacheQueryParamRe.MatchString(n) {
			return "", fmt.Errorf("invalid param name %q (allowed: lower-case letters, digits, underscore, 1-32 chars)", raw)
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	if len(names) > cacheQueryAllowlistMax {
		return "", fmt.Errorf("too many query params: %d (max %d)", len(names), cacheQueryAllowlistMax)
	}
	sort.Strings(names)
	return strings.Join(names, ","), nil
}

// splitAllowlistCSV mirrors Domain.CacheQueryAllowlistNames for a raw csv.
func splitAllowlistCSV(csv string) []string {
	if csv = strings.TrimSpace(csv); csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
}

// purge clears this domain's cached pages. v1: host-key grep-unlink in
// the shared keyzone (ADR-0108); no nginx reload needed.
func (h *domainCacheHandler) purge(c *gin.Context) {
	ctx := c.Request.Context()
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	// GH #619: optional targeted purge. Body {"paths":["/blog","/x"]} purges
	// only those URLs/prefixes; absent/empty body = whole-domain purge (v1).
	var req struct {
		Paths []string `json:"paths"`
	}
	_ = c.ShouldBindJSON(&req)
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := h.cfg.Agent.Call(agentCtx, "nginx.cache.purge", map[string]any{"domain": dom.Name, "paths": req.Paths}); err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	// GH #632: re-warm the cache after a manual purge so the next visitor
	// doesn't eat a cold MISS. Fire-and-forget with a detached context (the
	// request ctx cancels once we respond).
	if dom.CacheEnabled {
		agent, host := h.cfg.Agent, dom.Name
		go func() {
			wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer wcancel()
			_, _ = agent.Call(wctx, "nginx.cache_warmup", map[string]any{"host": host, "max_urls": 50})
		}()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "domain": dom.Name, "paths": req.Paths})
}
