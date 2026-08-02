package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// M26 Step 4 (ADR-0053). CrowdSec admin endpoints.

const csCallTimeout = 10 * time.Second

// csMetricsTimeout is the wider ceiling for /metrics specifically.
// cscli metrics fans out three sub-calls (metrics + decisions list +
// alerts list); decisions list scans the LAPI's decision table which
// grows to 100k+ rows after a Firehol import. 10s was too tight —
// observed 30s+ stalls on /jabali-admin/security?sub=overview that
// timed the UI out. 30s gives ample headroom; the agent handler also
// caps its decisions enumeration at --limit 100000.
const csMetricsTimeout = 30 * time.Second

// validCrowdSecScopes mirrors the agent-side allow-list. Reject unknown
// scope values at the API edge so the operator gets a clean 400 instead
// of a generic agent error. See feedback_verify_wire_contract — keep
// the wire-contract values explicit.
var validCrowdSecScopes = map[string]bool{
	"ip": true, "range": true, "country": true, "as": true,
}

// RegisterSecurityCrowdSecRoutes mounts admin-only CrowdSec endpoints.
// `settings` may be nil for tests that stub the agent without the full
// repo graph — captcha + profiles routes are skipped in that case.
func RegisterSecurityCrowdSecRoutes(rg *gin.RouterGroup, cli agent.AgentInterface, settings repository.ServerSettingsRepository) {
	g := rg.Group("/admin/security/crowdsec", middleware.RequireAdmin())

	g.GET("/status", agentPassthrough(cli, "security.crowdsec.status", nil, csCallTimeout))

	g.GET("/decisions", func(c *gin.Context) {
		params := map[string]any{}
		if scope := c.Query("scope"); scope != "" {
			if !validCrowdSecScopes[scope] {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error",
					"error":  "invalid_scope",
					"detail": "scope must be one of ip|range|country|as",
				})
				return
			}
			params["scope"] = scope
		}
		if limit := c.Query("limit"); limit != "" {
			n, err := strconv.Atoi(limit)
			if err != nil || n < 1 || n > 1000 {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error",
					"error":  "invalid_limit",
				})
				return
			}
			params["limit"] = n
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.decisions.list", params)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	g.POST("/decisions", func(c *gin.Context) {
		var body struct {
			Scope    string `json:"scope"`
			Value    string `json:"value"`
			Duration string `json:"duration"`
			Reason   string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.decisions.add", map[string]any{
			"scope":    body.Scope,
			"value":    body.Value,
			"duration": body.Duration,
			"reason":   body.Reason,
		})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusCreated, "application/json; charset=utf-8", raw)
	})

	g.DELETE("/decisions/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_id"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.decisions.delete", map[string]any{"id": id})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// DELETE /decisions?ip=1.2.3.4  -> unban every active decision
	// targeting that IP/CIDR. Useful when the Test IP card shows the
	// operator's own egress as banned (typically the case after they
	// fat-fingered an SSH password and tripped ssh-bf). The agent
	// handler accepts an IP or CIDR; we validate shape here and let
	// the agent do the cscli call.
	g.DELETE("/decisions", func(c *gin.Context) {
		ip := strings.TrimSpace(c.Query("ip"))
		if ip == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "ip required"})
			return
		}
		// Cheap shape check; the agent re-validates with net.ParseIP /
		// ParseCIDR before invoking cscli.
		if len(ip) > 64 || !ipOrCIDRShape(ip) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid ip"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.decisions.delete", map[string]any{"ip": ip})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	g.GET("/bouncers", agentPassthrough(cli, "security.crowdsec.bouncers.list", nil, csCallTimeout))
	g.GET("/blocklists", agentPassthrough(cli, "security.crowdsec.blocklists.list", nil, 30*time.Second))
	g.POST("/blocklists/refresh", agentPassthrough(cli, "security.crowdsec.blocklists.refresh", nil, 60*time.Second))
	g.GET("/metrics", agentPassthrough(cli, "security.crowdsec.metrics", nil, csMetricsTimeout))

	g.GET("/hub", func(c *gin.Context) {
		params := map[string]any{}
		if t := c.Query("type"); t != "" {
			params["type"] = t
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.hub.list", params)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// M27 follow-up — install/remove curated free hub items (collections,
	// scenarios, parsers, appsec-rules). Wire contract:
	//   POST   /admin/security/crowdsec/hub/install  {type, name, force?}
	//   DELETE /admin/security/crowdsec/hub?type=...&name=...
	g.POST("/hub/install", func(c *gin.Context) {
		var body struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Force bool   `json:"force,omitempty"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
			return
		}
		body.Type = strings.TrimSpace(body.Type)
		body.Name = strings.TrimSpace(body.Name)
		if body.Type == "" || body.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "type_and_name_required"})
			return
		}
		// cscli install talks to upstream registry — give it more time
		// than the default cscli call budget. 30s covers slow networks.
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.hub.install", body)
		if err != nil {
			status, ebody := translateAgentError(err)
			c.JSON(status, ebody)
			return
		}
		c.Data(http.StatusCreated, "application/json; charset=utf-8", raw)
	})

	g.DELETE("/hub", func(c *gin.Context) {
		typ := strings.TrimSpace(c.Query("type"))
		name := strings.TrimSpace(c.Query("name"))
		if typ == "" || name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "type_and_name_required"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.hub.remove", map[string]any{
			"type": typ,
			"name": name,
		})
		if err != nil {
			status, ebody := translateAgentError(err)
			c.JSON(status, ebody)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// M27 Step 2 — allowlists (ADR-0061). Wire contract:
	//   GET    /admin/security/crowdsec/allowlists           → {items}
	//   POST   /admin/security/crowdsec/allowlists           {value, reason}
	//   DELETE /admin/security/crowdsec/allowlists?value=... (query param; CIDR has `/` which :path would segment)
	g.GET("/allowlists", agentPassthrough(cli, "security.crowdsec.allowlists.list", nil, csCallTimeout))

	g.POST("/allowlists", func(c *gin.Context) {
		var body struct {
			Value  string `json:"value"`
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
			return
		}
		body.Value = strings.TrimSpace(body.Value)
		if body.Value == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "value_required"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.allowlists.add", body)
		if err != nil {
			status, ebody := translateAgentError(err)
			c.JSON(status, ebody)
			return
		}
		c.Data(http.StatusCreated, "application/json; charset=utf-8", raw)
	})

	g.DELETE("/allowlists", func(c *gin.Context) {
		value := strings.TrimSpace(c.Query("value"))
		if value == "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "value_required"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		_, err := cli.Call(ctx, "security.crowdsec.allowlists.remove", map[string]any{"value": value})
		if err != nil {
			status, ebody := translateAgentError(err)
			c.JSON(status, ebody)
			return
		}
		c.Status(http.StatusNoContent)
	})

	// M27 Step 3 — alerts view (read-only). Alerts = scenario fires,
	// decisions = active enforcement. Both overlap but alerts show the
	// signal path (scenario hit → maybe decision → maybe expired).
	g.GET("/alerts", agentPassthrough(cli, "security.crowdsec.alerts.list", nil, csCallTimeout))

	// Bucketed alert counts for the engine-dashboard "Alerts over time"
	// chart. ?since=24h|7d|30d (default 7d).
	g.GET("/alerts/timeseries", func(c *gin.Context) {
		since := c.DefaultQuery("since", "7d")
		switch since {
		case "24h", "7d", "30d":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "since must be 24h|7d|30d"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.alerts.timeseries", map[string]any{"since": since})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json", raw)
	})

	// Top source IPs by alert count over a rolling window. Engine
	// dashboard "Top sources" card. ?since=24h|7d|30d&limit=1..50.
	g.GET("/alerts/top-sources", func(c *gin.Context) {
		since := c.DefaultQuery("since", "24h")
		switch since {
		case "24h", "7d", "30d":
		default:
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "since must be 24h|7d|30d"})
			return
		}
		limit := 10
		if s := c.Query("limit"); s != "" {
			if v, err := strconv.Atoi(s); err == nil && v > 0 && v <= 50 {
				limit = v
			}
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.alerts.top_sources", map[string]any{
			"since": since,
			"limit": limit,
		})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json", raw)
	})

	g.GET("/alerts/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_id"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.crowdsec.alerts.inspect", map[string]any{"id": id})
		if err != nil {
			status, ebody := translateAgentError(err)
			c.JSON(status, ebody)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	// CrowdSec Console — removed 2026-06-03. Engine no longer integrates
	// with app.crowdsec.net (community-tier 500-alerts/month cap was
	// unusable); per-host /jabali-admin/security tabs replace it.
	// install.sh now disenrolls + disables forwarding on every
	// jabali update. CAPI community blocklist pull is independent and
	// still works.

	// M27 Step 5 — captcha remediation. DB is truth for the toggle +
	// creds (secret NEVER returned). Agent rewrites the four bouncer-
	// conf keys (CAPTCHA_PROVIDER/SITE_KEY/SECRET_KEY/FALLBACK_REMEDIATION)
	// via rewriteBouncerConfKeys so the M26-written AppSec lines survive.
	if settings != nil {
		g.GET("/captcha", func(c *gin.Context) {
			s, err := settings.Get(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"status": "error", "error": "settings_read", "detail": err.Error(),
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"enabled":  s.CrowdSecCaptchaEnabled,
				"provider": s.CrowdSecCaptchaProvider,
				"site_key": s.CrowdSecCaptchaSiteKey,
				// secret_key deliberately omitted (write-only)
			})
		})

		// M27 Step 6 — per-scenario remediation override. GET merges
		// scenario list + current overrides + captcha_enabled so the UI
		// can grey out the captcha option when captcha isn't configured.
		g.GET("/profiles", func(c *gin.Context) {
			s, err := settings.Get(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"status": "error", "error": "settings_read", "detail": err.Error(),
				})
				return
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
			defer cancel()
			scenRaw, err := cli.Call(ctx, "security.crowdsec.scenarios.list", nil)
			if err != nil {
				status, ebody := translateAgentError(err)
				c.JSON(status, ebody)
				return
			}
			profRaw, err := cli.Call(ctx, "security.crowdsec.profiles.get", nil)
			if err != nil {
				status, ebody := translateAgentError(err)
				c.JSON(status, ebody)
				return
			}
			// Merge into one JSON response. Parse + re-emit.
			var scens struct {
				Items []map[string]any `json:"items"`
			}
			var profs struct {
				Overrides []map[string]any `json:"overrides"`
			}
			_ = json.Unmarshal(scenRaw, &scens)
			_ = json.Unmarshal(profRaw, &profs)
			c.JSON(http.StatusOK, gin.H{
				"scenarios":       scens.Items,
				"overrides":       profs.Overrides,
				"captcha_enabled": s.CrowdSecCaptchaEnabled,
			})
		})

		g.PUT("/profiles", func(c *gin.Context) {
			var body struct {
				Overrides []struct {
					Scenario string `json:"scenario"`
					Action   string `json:"action"`
				} `json:"overrides"`
			}
			if err := c.ShouldBindJSON(&body); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
				return
			}
			s, err := settings.Get(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"status": "error", "error": "settings_read", "detail": err.Error(),
				})
				return
			}
			for _, o := range body.Overrides {
				if o.Action != "captcha" && o.Action != "off" {
					c.JSON(http.StatusBadRequest, gin.H{
						"status": "error", "error": "invalid_action",
						"detail": `action must be "captcha" or "off"`,
					})
					return
				}
				if o.Action == "captcha" && !s.CrowdSecCaptchaEnabled {
					c.JSON(http.StatusBadRequest, gin.H{
						"status": "error", "error": "captcha_disabled",
						"detail": "enable captcha remediation before selecting captcha as an override",
					})
					return
				}
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			raw, err := cli.Call(ctx, "security.crowdsec.profiles.set", map[string]any{
				"overrides": body.Overrides,
			})
			if err != nil {
				status, ebody := translateAgentError(err)
				c.JSON(status, ebody)
				return
			}
			c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
		})

		g.PUT("/captcha", func(c *gin.Context) {
			var body struct {
				Enabled   bool   `json:"enabled"`
				Provider  string `json:"provider"`
				SiteKey   string `json:"site_key"`
				SecretKey string `json:"secret_key"`
			}
			if err := c.ShouldBindJSON(&body); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
				return
			}
			// Merge semantics: empty secret = "keep existing"
			current, err := settings.Get(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"status": "error", "error": "settings_read", "detail": err.Error(),
				})
				return
			}
			merged := *current
			merged.CrowdSecCaptchaEnabled = body.Enabled
			merged.CrowdSecCaptchaProvider = strings.TrimSpace(body.Provider)
			merged.CrowdSecCaptchaSiteKey = strings.TrimSpace(body.SiteKey)
			if s := strings.TrimSpace(body.SecretKey); s != "" {
				merged.CrowdSecCaptchaSecretKey = s
			}
			if body.Enabled && merged.CrowdSecCaptchaSecretKey == "" {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error", "error": "secret_required",
					"detail": "secret_key required when enabling",
				})
				return
			}
			// GH #685: apply to the bouncer FIRST, persist to server_settings only
			// on success — otherwise a failed agent apply would leave the DB
			// claiming captcha is enabled while the bouncer is unchanged.
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			_, err = cli.Call(ctx, "security.crowdsec.captcha.apply", map[string]any{
				"enabled":    merged.CrowdSecCaptchaEnabled,
				"provider":   merged.CrowdSecCaptchaProvider,
				"site_key":   merged.CrowdSecCaptchaSiteKey,
				"secret_key": merged.CrowdSecCaptchaSecretKey,
			})
			if err != nil {
				status, ebody := translateAgentError(err)
				c.JSON(status, ebody)
				return
			}
			if err := settings.Upsert(c.Request.Context(), &merged); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"status": "error", "error": "settings_save", "detail": err.Error(),
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"enabled":  merged.CrowdSecCaptchaEnabled,
				"provider": merged.CrowdSecCaptchaProvider,
				"site_key": merged.CrowdSecCaptchaSiteKey,
			})
		})
	}
}

// RegisterSecurityAppSecRoutes mounts the admin AppSec-geoblock surface.
// Split from RegisterSecurityCrowdSecRoutes because it needs the
// ServerSettings repo (DB is the source of truth; agent rewrites the
// YAML rule on every set). When settings is nil the endpoints are
// intentionally skipped so tests that only stub the agent don't need to
// bring up the full repo graph.
func RegisterSecurityAppSecRoutes(rg *gin.RouterGroup, cli agent.AgentInterface, settings repository.ServerSettingsRepository) {
	if settings == nil {
		return
	}
	g := rg.Group("/admin/security/crowdsec/appsec", middleware.RequireAdmin())

	g.GET("/geoblock", func(c *gin.Context) {
		s, err := settings.Get(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "error", "error": "settings_read", "detail": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, appsecGeoblockResponse(s))
	})

	g.PUT("/geoblock", func(c *gin.Context) {
		var body struct {
			Mode      string   `json:"mode"`
			Countries []string `json:"countries"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
			return
		}
		if _, ok := appsecGeoblockModes[body.Mode]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"status": "error", "error": "invalid_mode",
				"detail": "mode must be off|allow|deny",
			})
			return
		}
		cleaned, errResp := cleanCountries(body.Countries)
		if errResp != nil {
			c.JSON(http.StatusBadRequest, errResp)
			return
		}
		if (body.Mode == "allow" || body.Mode == "deny") && len(cleaned) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"status": "error", "error": "empty_countries",
				"detail": "mode " + body.Mode + " requires at least one country",
			})
			return
		}
		// Dispatch agent first so a failure to rewrite the YAML rule
		// doesn't leave the DB drifted from the filesystem.
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		if _, err := cli.Call(ctx, "security.crowdsec.appsec.geoblock.set", map[string]any{
			"mode":      body.Mode,
			"countries": cleaned,
		}); err != nil {
			status, errBody := translateAgentError(err)
			c.JSON(status, errBody)
			return
		}
		s, err := settings.Get(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "error", "error": "settings_read", "detail": err.Error(),
			})
			return
		}
		s.AppSecGeoblockMode = body.Mode
		s.AppSecGeoblockCountries = strings.Join(cleaned, ",")
		if err := settings.Upsert(c.Request.Context(), s); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "error", "error": "settings_write", "detail": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, appsecGeoblockResponse(s))
	})
}

// appsecGeoblockModes mirrors the agent + CONVENTIONS list. Duplicated
// at the API edge so bad input gets a 400 without a round-trip.
var appsecGeoblockModes = map[string]struct{}{
	"off": {}, "allow": {}, "deny": {},
}

var appsecCountryCodeRE = regexp.MustCompile(`^[A-Z]{2}$`)

func appsecGeoblockResponse(s *models.ServerSettings) gin.H {
	countries := []string{}
	if s.AppSecGeoblockCountries != "" {
		countries = strings.Split(s.AppSecGeoblockCountries, ",")
	}
	return gin.H{
		"mode":      s.AppSecGeoblockMode,
		"countries": countries,
	}
}

func cleanCountries(in []string) ([]string, gin.H) {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, c := range in {
		code := strings.ToUpper(strings.TrimSpace(c))
		if code == "" {
			continue
		}
		if !appsecCountryCodeRE.MatchString(code) {
			return nil, gin.H{
				"status": "error", "error": "invalid_country",
				"detail": "country " + c + " must be a 2-letter ISO 3166-1 code",
			}
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out, nil
}

// agentPassthrough is a shared helper for "GET that just forwards to a
// no-arg agent command and returns the raw JSON response."
func agentPassthrough(cli agent.AgentInterface, command string, params any, timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		raw, err := cli.Call(ctx, command, params)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	}
}

// ipOrCIDRShape is a cheap pre-validator for the DELETE /decisions
// IP query param. It allows v4/v6 IPs with an optional /N suffix
// using only the character set IP/CIDR notation can produce. The
// agent handler does the strict net.ParseIP / net.ParseCIDR
// validation before invoking cscli.
var ipOrCIDRRe = regexp.MustCompile(`^[0-9a-fA-F:.]+(/[0-9]{1,3})?$`)

func ipOrCIDRShape(s string) bool {
	return ipOrCIDRRe.MatchString(s)
}
