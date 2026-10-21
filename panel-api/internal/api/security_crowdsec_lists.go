package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
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

// crowdsecCustomListView is one of the admin's own list URLs. Key matches the
// agent's status entry for it.
type crowdsecCustomListView struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

type crowdsecListsResponse struct {
	Lists  []crowdsecListView       `json:"lists"`
	Custom []crowdsecCustomListView `json:"custom"`
	// Status is the agent's report of its last run (security.crowdsec.lists.status);
	// null when the agent could not be asked.
	Status json.RawMessage `json:"status"`
}

// registerCrowdsecListRoutes mounts the admin's extra IP blocklists:
//
//	GET  /blocklists/feeds          the catalog, which lists are on, the custom
//	                                list URLs, the last run
//	PUT  /blocklists/feeds          {enabled: [key…], custom?: [url…]} saves the
//	                                set (custom left out = unchanged); the
//	                                reconciler sends it to the agent within a minute
//	POST /blocklists/feeds/refresh  downloads the lists that are on again now
//
// The agent downloads and applies the lists in the background, so neither
// write waits for a download.
func registerCrowdsecListRoutes(g *gin.RouterGroup, cli agent.AgentInterface, settings repository.ServerSettingsRepository) {
	g.GET("/blocklists/feeds", func(c *gin.Context) {
		s, err := settings.Get(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "settings_read", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, crowdsecListsView(c.Request.Context(), cli, storedCrowdsecLists(s.CrowdsecBlocklists), storedCustomLists(s.CrowdsecCustomBlocklists)))
	})

	g.PUT("/blocklists/feeds", func(c *gin.Context) {
		var body struct {
			Enabled []string  `json:"enabled"`
			Custom  *[]string `json:"custom"`
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
		custom := storedCustomLists(s.CrowdsecCustomBlocklists)
		if body.Custom != nil {
			if custom, err = blocklists.NormalizeCustom(*body.Custom); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_custom_blocklists", "detail": err.Error()})
				return
			}
			// The agent refuses these where it downloads too; say so here.
			if u := customOnOwnAddress(custom, crowdsecProtectedIPs(s.PublicIPv4, s.PublicIPv6)); u != "" {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_custom_blocklists", "detail": u + ": is this server's own address"})
				return
			}
		}
		merged := *s
		merged.CrowdsecBlocklists = strings.Join(enabled, ",")
		merged.CrowdsecCustomBlocklists = strings.Join(custom, "\n")
		if err := settings.Upsert(c.Request.Context(), &merged); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "settings_save", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, crowdsecListsView(c.Request.Context(), cli, enabled, custom))
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
			"custom":  storedCustomLists(s.CrowdsecCustomBlocklists),
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

// customOnOwnAddress returns the first URL whose host is one of own.
func customOnOwnAddress(urls, own []string) string {
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		h := net.ParseIP(u.Hostname())
		for _, o := range own {
			if h != nil && h.Equal(net.ParseIP(o)) {
				return raw
			}
		}
	}
	return ""
}

// storedCustomLists reads the saved custom list URLs, leaving out any that no
// longer pass blocklists.NormalizeCustom (and any past the cap).
func storedCustomLists(stored string) []string {
	return blocklists.ValidCustom(strings.Split(stored, "\n"))
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

func crowdsecListsView(ctx context.Context, cli agent.AgentInterface, enabled, custom []string) crowdsecListsResponse {
	on := map[string]bool{}
	for _, k := range enabled {
		on[k] = true
	}
	resp := crowdsecListsResponse{
		Lists:  make([]crowdsecListView, 0, len(blocklists.Catalog)),
		Custom: make([]crowdsecCustomListView, 0, len(custom)),
		Status: json.RawMessage("null"),
	}
	for _, l := range blocklists.Catalog {
		resp.Lists = append(resp.Lists, crowdsecListView{List: l, Enabled: on[l.Key]})
	}
	for _, u := range custom {
		resp.Custom = append(resp.Custom, crowdsecCustomListView{URL: u, Key: blocklists.CustomKey(u)})
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if raw, err := cli.Call(callCtx, "security.crowdsec.lists.status", nil); err == nil && json.Valid(raw) {
		resp.Status = raw
	}
	return resp
}
