// Package api — security_snuffleupagus.go (M41, ADR-0088)
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const snuffleupagusCallTimeout = 10 * time.Second

// SecuritySnuffleupagusConfig wires the routes to their dependencies.
type SecuritySnuffleupagusConfig struct {
	Agent      agent.AgentInterface
	Repo       repository.SnuffleupagusRepository
	Reconciler *reconciler.SnuffleupagusReconciler
	BundleDir  string // /opt/jabali-panel/install/snuffleupagus/rules or /usr/share/...
}

func RegisterSecuritySnuffleupagusRoutes(rg *gin.RouterGroup, cfg SecuritySnuffleupagusConfig) {
	g := rg.Group("/admin/security/snuffleupagus", middleware.RequireAdmin())

	g.GET("/status", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), snuffleupagusCallTimeout)
		defer cancel()

		state, err := cfg.Repo.GetState(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
			return
		}

		// Ask the agent for which PHP minors loaded the .so. If the agent
		// is unreachable we still return the DB state so the UI doesn't
		// dead-end on agent flakes.
		agentResp := map[string]any{
			"php_versions_loaded": []any{},
		}
		if cfg.Agent != nil {
			raw, err := cfg.Agent.Call(ctx, "snuffleupagus.status", map[string]any{})
			if err == nil && len(raw) > 0 {
				var m map[string]any
				if jerr := json.Unmarshal(raw, &m); jerr == nil {
					agentResp = m
				}
			}
		}

		resp := gin.H{
			"enabled":             state.Mode != models.SnuffleupagusModeOff,
			"mode":                state.Mode,
			"last_applied_at":     state.LastAppliedAt,
			"active_rules_sha256": state.LastAppliedSha256,
			"php_versions_loaded": agentResp["php_versions_loaded"],
		}
		c.JSON(http.StatusOK, resp)
	})

	g.POST("/mode", func(c *gin.Context) {
		var body struct {
			Mode string `json:"mode"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
		mode := models.SnuffleupagusMode(body.Mode)
		switch mode {
		case models.SnuffleupagusModeOff, models.SnuffleupagusModeSimulation, models.SnuffleupagusModeEnforce:
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_mode", "detail": "must be off|simulation|enforce"})
			return
		}
		ctx := c.Request.Context()
		if err := cfg.Repo.UpdateState(ctx, mode, nil, nil); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
			return
		}
		if cfg.Reconciler != nil {
			if err := cfg.Reconciler.Reconcile(ctx); err != nil {
				c.JSON(http.StatusBadGateway, gin.H{
					"error":  "reconcile_failed",
					"detail": err.Error(),
				})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"mode": mode})
	})

	g.GET("/rules", func(c *gin.Context) {
		// Compose the rules list from on-disk bundle files + override state.
		rules, err := listBundleRules(cfg.BundleDir)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
			return
		}
		ovs, err := cfg.Repo.ListOverrides(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
			return
		}
		ovMap := make(map[string]models.SnuffleupagusRuleOverride, len(ovs))
		for _, o := range ovs {
			ovMap[o.RuleName] = o
		}
		out := make([]gin.H, 0, len(rules))
		for _, r := range rules {
			enabled := true
			var reason *string
			if o, ok := ovMap[r.Name]; ok {
				enabled = o.Enabled
				reason = o.Reason
			}
			out = append(out, gin.H{
				"name":        r.Name,
				"source_file": r.SourceFile,
				"enabled":     enabled,
				"reason":      reason,
			})
		}
		c.JSON(http.StatusOK, gin.H{"rules": out})
	})

	g.POST("/rules/:name/toggle", func(c *gin.Context) {
		name := c.Param("name")
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "rule name required"})
			return
		}
		var body struct {
			Enabled bool   `json:"enabled"`
			Reason  string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
			return
		}
		claims := ginctx.Claims(c)
		var setBy *string
		if claims != nil {
			id := claims.UserID
			setBy = &id
		}
		var reason *string
		if body.Reason != "" {
			reason = &body.Reason
		}
		ov := &models.SnuffleupagusRuleOverride{
			RuleName:    name,
			Enabled:     body.Enabled,
			Reason:      reason,
			SetByUserID: setBy,
			SetAt:       time.Now().UTC(),
		}
		ctx := c.Request.Context()
		if err := cfg.Repo.UpsertOverride(ctx, ov); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
			return
		}
		if cfg.Reconciler != nil {
			if err := cfg.Reconciler.Reconcile(ctx); err != nil {
				c.JSON(http.StatusBadGateway, gin.H{
					"error":  "reconcile_failed",
					"detail": err.Error(),
				})
				return
			}
		}
		c.JSON(http.StatusOK, ov)
	})

	g.GET("/incidents", func(c *gin.Context) {
		opts := repository.IncidentListOptions{
			Rule:     c.Query("rule"),
			DomainID: c.Query("domain"),
		}
		if v := c.Query("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				opts.Since = &t
			}
		}
		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				opts.Limit = n
			}
		}
		if v := c.Query("page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				if opts.Limit == 0 {
					opts.Limit = 50
				}
				opts.Offset = (n - 1) * opts.Limit
			}
		}
		ctx := c.Request.Context()
		rows, total, err := cfg.Repo.ListIncidents(ctx, opts)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusOK, gin.H{"data": []any{}, "total": 0, "page": 1, "page_size": 50})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal", "detail": err.Error()})
			return
		}
		page := 1
		if opts.Limit > 0 && opts.Offset > 0 {
			page = (opts.Offset / opts.Limit) + 1
		}
		size := opts.Limit
		if size == 0 {
			size = 50
		}
		c.JSON(http.StatusOK, gin.H{
			"data":      rows,
			"total":     total,
			"page":      page,
			"page_size": size,
		})
	})
}

type bundleRule struct {
	Name       string
	SourceFile string
}

// listBundleRules synthesizes a stable name per rule line. The upstream
// Snuffleupagus DSL has no `name=` field, so we build one from:
//   sp.<feature>.<verb>(<arg>)            -> sp.<feature>:<arg>
//   sp.<feature>.enable() / .list(...)    -> sp.<feature>
// This means every "sp.disable_function.function('system').drop()"
// gets a distinct, meaningful row instead of collapsing under a single
// "sp.disable_function.function" entry.

var (
	ruleArgRe    = regexp.MustCompile(`^\s*sp\.([a-z_]+)\.([a-z_]+)\("([^"]+)"\)`)
	ruleEnableRe = regexp.MustCompile(`^\s*sp\.([a-z_]+)\.([a-z_]+)\(`)
)

func listBundleRules(dir string) ([]bundleRule, error) {
	if dir == "" {
		dir = "/opt/jabali-panel/install/snuffleupagus/rules"
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.rules"))
	if err != nil {
		return nil, err
	}
	var out []bundleRule
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		base := filepath.Base(f)
		for i, line := range strings.Split(string(data), "\n") {
			var name string
			if m := ruleArgRe.FindStringSubmatch(line); m != nil {
				// sp.disable_function.function("system") -> sp.disable_function:system
				name = "sp." + m[1] + ":" + m[3]
			} else if m := ruleEnableRe.FindStringSubmatch(line); m != nil {
				// sp.harden_random.enable()  -> sp.harden_random
				name = "sp." + m[1]
			} else {
				continue
			}
			// Disambiguate duplicate names within the same file (multiple
			// disable_function rows for different params) by appending the
			// 1-based line number when needed.
			full := base + "#" + strconv.Itoa(i+1) + " " + name
			out = append(out, bundleRule{Name: full, SourceFile: base})
		}
	}
	return out, nil
}
