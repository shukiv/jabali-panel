package api

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

// systemCallTimeout bounds agent calls for system endpoints. Generous
// because service.list probes multiple units sequentially.
const systemCallTimeout = 10 * time.Second

// resolverSetTimeout is longer than systemCallTimeout because the agent has
// to write the drop-in and wait for systemd-resolved to restart (up to ~5s
// on a slow box) before returning. 15s leaves headroom.
const resolverSetTimeout = 15 * time.Second

// maxResolverEntries caps the list to match the agent-side check. Keep the
// two in sync so the API surface the admin sees matches what the agent
// would accept.
const maxResolverEntries = 8

// resolverSetRequest is the PUT body for DNS resolvers. The agent does the
// authoritative validation; this layer just filters out obvious garbage
// (too many entries, non-IPs) so we can return 400 with a nice message
// instead of bouncing off the agent with a generic bad-request.
type resolverSetRequest struct {
	Resolvers    []string `json:"resolvers"`
	SearchDomain string   `json:"search_domain"`
}

// RegisterSystemRoutes mounts admin-only system endpoints under the
// given router group. The group is expected to already carry RequireAuth
// middleware; we add RequireAdmin ourselves.
func RegisterSystemRoutes(rg *gin.RouterGroup, cli agent.AgentInterface) {
	sys := rg.Group("/system", middleware.RequireAdmin())

	sys.GET("/info", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "system.info", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	sys.GET("/services", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "service.list", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// POST /system/services/:name/{restart,stop,start,enable,disable} —
	// the agent enforces the same allow-list as /services. Each verb
	// forwards with no body. Self-destruct guard: stop + disable are
	// rejected for jabali-panel and jabali-agent at this layer — a root
	// operator who genuinely wants to stop them can `systemctl stop`
	// from the shell they're already on. The panel UI must not ship a
	// "click to lock yourself out" button.
	registerServiceAction := func(verb string, selfDestructGuard bool) {
		sys.POST("/services/:name/"+verb, func(c *gin.Context) {
			name := strings.TrimSpace(c.Param("name"))
			if name == "" {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error",
					"error":  "missing_name",
					"detail": "service name required",
				})
				return
			}
			// Same panel-critical set as the /admin/services guard
			// (panelSelfDestructUnits): stopping any of these bricks the
			// management plane (GH #746: nginx -> 502, redis-server -> the
			// panel process dies).
			if selfDestructGuard && panelSelfDestructUnits[name] {
				c.JSON(http.StatusConflict, gin.H{
					"status": "error",
					"error":  "self_destruct_refused",
					"detail": "stop/disable on " + name + " via the panel is refused — use systemctl from the shell",
				})
				return
			}

			ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
			defer cancel()

			raw, err := cli.Call(ctx, "service."+verb, map[string]any{"name": name})
			if err != nil {
				status, body := translateAgentError(err)
				c.JSON(status, body)
				return
			}
			c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
		})
	}
	registerServiceAction("restart", false) // restart is safe — panel + agent come right back up
	registerServiceAction("start", false)
	registerServiceAction("stop", true)
	registerServiceAction("enable", false)
	registerServiceAction("disable", true) // disable = won't auto-start on next boot; same class of foot-gun

	// DNS resolvers — truth lives on disk (systemd-resolved drop-in) so we
	// round-trip to the agent for both read and write; no DB involvement.
	sys.GET("/resolvers", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "system.resolver.get", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	sys.PUT("/resolvers", func(c *gin.Context) {
		var req resolverSetRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"status": "error",
				"error":  "validation_failed",
				"detail": err.Error(),
			})
			return
		}

		// Normalize + validate at the API edge. Mirrors the agent-side rules
		// so the admin sees a 400 with a clear detail instead of a 502 from
		// a generic agent bounce.
		cleaned := make([]string, 0, len(req.Resolvers))
		seen := map[string]bool{}
		for _, r := range req.Resolvers {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			addr, err := netip.ParseAddr(r)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error",
					"error":  "invalid_resolver",
					"detail": "not a valid IP: " + r,
				})
				return
			}
			if addr.IsUnspecified() || addr.IsMulticast() {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error",
					"error":  "invalid_resolver",
					"detail": r + " is not a usable unicast address",
				})
				return
			}
			if seen[r] {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error",
					"error":  "duplicate_resolver",
					"detail": "duplicate resolver: " + r,
				})
				return
			}
			seen[r] = true
			cleaned = append(cleaned, r)
		}
		if len(cleaned) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"status": "error",
				"error":  "no_resolvers",
				"detail": "at least one resolver required",
			})
			return
		}
		if len(cleaned) > maxResolverEntries {
			c.JSON(http.StatusBadRequest, gin.H{
				"status": "error",
				"error":  "too_many_resolvers",
				"detail": "at most 8 resolvers allowed",
			})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), resolverSetTimeout)
		defer cancel()

		raw, err := cli.Call(ctx, "system.resolver.set", map[string]any{
			"resolvers":     cleaned,
			"search_domain": strings.TrimSpace(req.SearchDomain),
		})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// M13 SSH shell sandbox: list available nspawn images. The Server
	// Settings card uses this for the "default image" dropdown; the
	// per-user Edit drawer uses it for the per-user pin select.
	sys.GET("/nspawn-images", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "system.list_nspawn_images", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// GH #574 — nspawn image lifecycle from the GUI. build is a long-running
	// debootstrap run as a transient unit (poll build/status); prune is quick +
	// dry-run-first. All proxy the operator `jabali nspawn …` CLI via the agent.
	sys.POST("/nspawn-images/build", func(c *gin.Context) {
		var req struct {
			Codename string `json:"codename"`
			Version  string `json:"version"`
			Snapshot string `json:"snapshot"`
			Suite    string `json:"suite"`
			Includes string `json:"includes"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "system.nspawn_build", map[string]any{
			"codename": req.Codename, "version": req.Version, "snapshot": req.Snapshot,
			"suite": req.Suite, "includes": req.Includes,
		})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
	sys.GET("/nspawn-images/build/status", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "system.nspawn_build_status", map[string]any{"since": c.Query("since")})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
	sys.DELETE("/nspawn-images/build", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "system.unit_stop", map[string]any{"unit": "jabali-nspawn-build.service"})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
	sys.POST("/nspawn-images/prune", func(c *gin.Context) {
		var req struct {
			Apply bool `json:"apply"`
		}
		_ = c.ShouldBindJSON(&req)
		ctx, cancel := context.WithTimeout(c.Request.Context(), systemCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "system.nspawn_prune", map[string]any{"apply": req.Apply})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
}
