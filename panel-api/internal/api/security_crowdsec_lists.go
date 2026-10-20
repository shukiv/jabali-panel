package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/blocklists"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// crowdsecListView is one catalog list with whether the admin turned it on.
type crowdsecListView struct {
	blocklists.List
	Enabled bool `json:"enabled"`
}

type crowdsecListsResponse struct {
	Lists []crowdsecListView `json:"lists"`
	// Status is the agent's report of its last run (security.crowdsec.lists.status);
	// null when the agent could not be asked.
	Status json.RawMessage `json:"status"`
}

// registerCrowdsecListRoutes mounts the admin's extra IP blocklists:
//
//	GET  /blocklists/feeds          the catalog, which lists are on, the last run
//	PUT  /blocklists/feeds          {enabled: [key…]} saves the set; the
//	                                reconciler sends it to the agent within a minute
//	POST /blocklists/feeds/refresh  downloads the lists that are on again now
//
// The agent imports and removes the lists in the background, so neither
// write waits for a download.
func registerCrowdsecListRoutes(g *gin.RouterGroup, cli agent.AgentInterface, settings repository.ServerSettingsRepository) {
	g.GET("/blocklists/feeds", func(c *gin.Context) {
		s, err := settings.Get(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "settings_read", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, crowdsecListsView(c.Request.Context(), cli, storedCrowdsecLists(s.CrowdsecBlocklists)))
	})

	g.PUT("/blocklists/feeds", func(c *gin.Context) {
		var body struct {
			Enabled []string `json:"enabled"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
			return
		}
		enabled, err := blocklists.Normalize(body.Enabled)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_blocklists", "detail": err.Error()})
			return
		}
		s, err := settings.Get(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "settings_read", "detail": err.Error()})
			return
		}
		merged := *s
		merged.CrowdsecBlocklists = strings.Join(enabled, ",")
		if err := settings.Upsert(c.Request.Context(), &merged); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "settings_save", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, crowdsecListsView(c.Request.Context(), cli, enabled))
	})

	g.POST("/blocklists/feeds/refresh", func(c *gin.Context) {
		s, err := settings.Get(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "settings_read", "detail": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), csCallTimeout)
		defer cancel()
		enabled := storedCrowdsecLists(s.CrowdsecBlocklists)
		raw, err := cli.Call(ctx, "security.crowdsec.lists.apply", map[string]any{
			"enabled": enabled,
			"protect": crowdsecProtectedIPs(s.PublicIPv4, s.PublicIPv6),
		})
		if err != nil {
			status, ebody := translateAgentError(err)
			c.JSON(status, ebody)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
}

// storedCrowdsecLists reads the saved set, leaving out keys no longer in the
// catalog.
func storedCrowdsecLists(stored string) []string {
	var keys []string
	for _, k := range strings.Split(stored, ",") {
		if _, ok := blocklists.Lookup(strings.TrimSpace(k)); ok {
			keys = append(keys, strings.TrimSpace(k))
		}
	}
	out, _ := blocklists.Normalize(keys)
	return out
}

// crowdsecProtectedIPs is the server's public IPs, which no list may ban.
// One that does not parse is left out; the agent would refuse the call.
func crowdsecProtectedIPs(v4, v6 string) []string {
	out := []string{}
	for _, raw := range []string{v4, v6} {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

func crowdsecListsView(ctx context.Context, cli agent.AgentInterface, enabled []string) crowdsecListsResponse {
	on := map[string]bool{}
	for _, k := range enabled {
		on[k] = true
	}
	resp := crowdsecListsResponse{Lists: make([]crowdsecListView, 0, len(blocklists.Catalog)), Status: json.RawMessage("null")}
	for _, l := range blocklists.Catalog {
		resp.Lists = append(resp.Lists, crowdsecListView{List: l, Enabled: on[l.Key]})
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if raw, err := cli.Call(callCtx, "security.crowdsec.lists.status", nil); err == nil && json.Valid(raw) {
		resp.Status = raw
	}
	return resp
}
