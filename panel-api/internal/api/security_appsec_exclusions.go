package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// security_appsec_exclusions.go — GH #1649. The admin side of CrowdSec AppSec
// false-positive handling:
//
//   - GET /events: what the WAF blocked recently, grouped by rules + host + URI
//     the way `jabali appsec explain` groups it, with the rules that scored
//     told apart from the CRS rules that ride along on every block.
//   - GET, POST /exclusions and DELETE /exclusions/:id: the operator CRS
//     exclusions (JAB-227), applied to the WAF at once through appsecops.
//
// Both used to need a root shell on the box.

const (
	// appsecEventsDefaultLimit and appsecEventsMaxLimit bound how many alerts
	// one triage request inspects. The agent runs one `cscli alerts inspect`
	// per alert, so the request time grows with the limit. The CLI allows 200,
	// for an operator who can wait.
	appsecEventsDefaultLimit = 25
	appsecEventsMaxLimit     = 50

	// appsecExclusionWriteBudget covers the slowest add or remove: an apply
	// that fails, the undo, and a second apply (appsecops allows 45s for each
	// apply).
	appsecExclusionWriteBudget = 2 * time.Minute

	// appsecExclusionIDMaxLen is a ULID's length. Longer ids are never stored.
	appsecExclusionIDMaxLen = 26
)

// appsecEventsTimeout bounds one triage request. A var so a test can shorten it.
var appsecEventsTimeout = 90 * time.Second

// SecurityAppSecExclusionConfig is what the routes need. Without the two
// repositories only /events is mounted.
type SecurityAppSecExclusionConfig struct {
	Agent      agent.AgentInterface
	Exclusions repository.CRSRuleExclusionRepository
	HostModes  repository.CRSHostModeRepository
}

// RegisterSecurityAppSecExclusionRoutes mounts the admin-only AppSec triage and
// exclusion routes under /admin/security/crowdsec/appsec.
func RegisterSecurityAppSecExclusionRoutes(rg *gin.RouterGroup, cfg SecurityAppSecExclusionConfig) {
	if cfg.Agent == nil {
		return
	}
	g := rg.Group("/admin/security/crowdsec/appsec", middleware.RequireAdmin())
	g.GET("/events", appsecEventsHandler(cfg.Agent))

	if cfg.Exclusions == nil || cfg.HostModes == nil {
		return
	}
	d := appsecops.Deps{Agent: cfg.Agent, Exclusions: cfg.Exclusions, HostModes: cfg.HostModes}

	g.GET("/exclusions", func(c *gin.Context) {
		rows, err := appsecops.ListExclusions(c.Request.Context(), d)
		if err != nil {
			writeAppSecExclusionError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
	})

	g.POST("/exclusions", func(c *gin.Context) {
		var body struct {
			Host      string `json:"host"`
			URIPrefix string `json:"uri_prefix"`
			RuleID    string `json:"rule_id"`
			Note      string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
			return
		}
		extendWriteDeadline(c, appsecExclusionWriteBudget)
		row, res, err := appsecops.AddExclusion(c.Request.Context(), d, appseccfg.Exclusion{
			Host: body.Host, URIPrefix: body.URIPrefix, RuleID: body.RuleID, Note: body.Note,
		})
		if err != nil {
			writeAppSecExclusionError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"exclusion": row, "apply": res})
	})

	g.DELETE("/exclusions/:id", func(c *gin.Context) {
		id := c.Param("id")
		if id == "" || len(id) > appsecExclusionIDMaxLen {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
			return
		}
		extendWriteDeadline(c, appsecExclusionWriteBudget)
		res, err := appsecops.RemoveExclusion(c.Request.Context(), d, id)
		if err != nil {
			writeAppSecExclusionError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "apply": res})
	})
}

func appsecEventsHandler(cli agent.AgentInterface) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := appsecEventsDefaultLimit
		if s := c.Query("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > appsecEventsMaxLimit {
				c.JSON(http.StatusBadRequest, gin.H{
					"status": "error", "error": "invalid_limit",
					"detail": fmt.Sprintf("limit must be between 1 and %d", appsecEventsMaxLimit),
				})
				return
			}
			limit = n
		}

		extendWriteDeadline(c, appsecEventsTimeout+10*time.Second)
		ctx, cancel := context.WithTimeout(c.Request.Context(), appsecEventsTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, appsecops.EventsVerb, map[string]any{"limit": limit})
		if err != nil {
			// When this request's own deadline passes, the agent client
			// reports a socket timeout, not an AgentError, so
			// translateAgentError would say 500. The operator's fix is to
			// inspect fewer alerts, which the UI offers on a 504.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				c.JSON(http.StatusGatewayTimeout, gin.H{
					"status": "error", "error": "agent_timeout",
					"detail": fmt.Sprintf("inspecting %d alerts took too long; inspect fewer", limit),
				})
				return
			}
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		var resp appsecops.EventsResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"status": "error", "error": "agent_response", "detail": "decode appsec events: " + err.Error(),
			})
			return
		}
		// alerts_scanned is how many alerts cscli listed; an alert the agent
		// could not inspect adds no events. events_count is what was read.
		c.JSON(http.StatusOK, gin.H{
			"patterns":       appsecops.GroupEvents(resp.Events),
			"inline_blocks":  appsecops.GroupInlineBlocks(resp.InlineBlocks),
			"alerts_scanned": resp.AlertsScanned,
			"events_count":   len(resp.Events),
			"inline_count":   len(resp.InlineBlocks),
			"limit":          limit,
		})
	}
}

// writeAppSecExclusionError maps an appsecops error onto a status and body.
func writeAppSecExclusionError(c *gin.Context, err error) {
	var invalid *appsecops.InvalidError
	var applyErr *appsecops.ApplyError
	switch {
	case errors.As(err, &invalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "invalid_exclusion", "detail": invalid.Error()})
	case errors.Is(err, appsecops.ErrDuplicate):
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "duplicate_exclusion", "detail": err.Error()})
	case errors.Is(err, appsecops.ErrTooMany):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"status": "error", "error": "too_many_exclusions", "detail": err.Error()})
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found"})
	case errors.Is(err, appsecops.ErrNotConfigured):
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": "not_configured"})
	case errors.As(err, &applyErr):
		status, body := translateAgentError(applyErr.Err)
		body["detail"] = appsecApplyFailureDetail(applyErr, fmt.Sprint(body["detail"]))
		body["rolled_back"] = applyErr.RolledBack
		c.JSON(status, body)
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "internal", "detail": err.Error()})
	}
}

// appsecRenderConfigHint is the root command that rewrites the operator WAF
// file from the stored rows.
const appsecRenderConfigHint = "Run `jabali appsec render-config --reconcile --reload` as root to bring the WAF in line with the list."

func appsecApplyFailureDetail(e *appsecops.ApplyError, cause string) string {
	switch {
	case !e.RolledBack:
		return "The WAF could not be updated, and undoing the change in the panel failed too, so the list shows the change but the WAF may not run it. " +
			appsecRenderConfigHint + " Cause: " + cause
	case e.ReapplyErr != nil:
		return "The WAF could not be updated, so the change was undone. Rewriting the WAF file afterwards also failed, so the file may still hold the change. " +
			appsecRenderConfigHint + " Cause: " + cause
	default:
		return "The WAF could not be updated, so nothing was changed. Cause: " + cause
	}
}

// extendWriteDeadline lets one slow request outlive the server's 30s
// WriteTimeout, up to d from now. Best-effort: a writer without deadline
// support keeps the default.
func extendWriteDeadline(c *gin.Context, d time.Duration) {
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(d))
}
