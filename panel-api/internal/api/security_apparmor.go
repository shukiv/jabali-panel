package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// M40 (ADR-0086) AppArmor admin routes. Read-only status + per-profile
// mode flip (complain/enforce). Filesystem is the truth — DB doesn't
// mirror per-profile state. The one AppArmor table is apparmor_mode_acks
// (GH #2078): profiles an administrator acknowledged in complain mode, so the
// Server Status "not enforcing" alert stops naming them until the mode changes.

const apparmorCallTimeout = 10 * time.Second

// apparmorProfileNameRE is the shape of a profile name in a URL path. The name
// is then checked against the profiles the agent reports.
var apparmorProfileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RegisterSecurityAppArmorRoutes mounts the AppArmor admin routes. acks may be
// nil (no database): the acknowledge routes are then not mounted and the
// status carries no "acknowledged" list.
func RegisterSecurityAppArmorRoutes(rg *gin.RouterGroup, cli agent.AgentInterface, acks repository.AppArmorModeAckRepository) {
	g := rg.Group("/admin/security/apparmor", middleware.RequireAdmin())

	g.GET("/status", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), apparmorCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.apparmor.status", map[string]any{})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		if acks != nil {
			raw = withAppArmorAcks(c.Request.Context(), acks, raw)
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	g.POST("/profiles/:name/mode", func(c *gin.Context) {
		name := c.Param("name")
		var body struct {
			Mode string `json:"mode"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_json"})
			return
		}
		if body.Mode != "complain" && body.Mode != "enforce" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "mode must be complain|enforce"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), apparmorCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.apparmor.set_mode", map[string]any{
			"profile": name,
			"mode":    body.Mode,
		})
		if err != nil {
			status, errBody := translateAgentError(err)
			c.JSON(status, errBody)
			return
		}
		// GH #2078: an acknowledgement ends when the profile's mode changes.
		if acks != nil {
			if err := acks.Delete(c.Request.Context(), name); err != nil {
				slog.WarnContext(c.Request.Context(), "apparmor: acknowledgement not deleted after a mode change", "profile", name, "error", err)
			}
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	if acks == nil {
		return
	}

	// GH #2078: acknowledge a profile kept in complain mode on purpose. Only
	// complain can be acknowledged, and only while the profile is in it.
	g.POST("/profiles/:name/acknowledge", func(c *gin.Context) {
		name := c.Param("name")
		if !apparmorProfileNameRE.MatchString(name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_profile"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), apparmorCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "security.apparmor.summary", map[string]any{})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		modes, ok := appArmorProfileModes(raw)
		if !ok {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "apparmor_inactive", "detail": "AppArmor isn't active on this server, so there's nothing to acknowledge."})
			return
		}
		mode, found := modes[name]
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "unknown_profile"})
			return
		}
		if mode != "complain" {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "not_in_complain_mode",
				"detail": fmt.Sprintf("Only a profile in complain mode can be acknowledged; %s is %s.", name, mode),
			})
			return
		}
		row := &models.AppArmorModeAck{Profile: name, Mode: mode, AcknowledgedAt: time.Now().UTC()}
		if cl := ginctx.Claims(c); cl != nil && cl.UserID != "" {
			by := cl.UserID
			row.AcknowledgedBy = &by
		}
		if err := acks.Put(c.Request.Context(), row); err != nil {
			slog.ErrorContext(c.Request.Context(), "apparmor: acknowledgement not saved", "profile", name, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profile": name, "mode": mode, "acknowledged": true})
	})

	g.DELETE("/profiles/:name/acknowledge", func(c *gin.Context) {
		name := c.Param("name")
		if !apparmorProfileNameRE.MatchString(name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_profile"})
			return
		}
		if err := acks.Delete(c.Request.Context(), name); err != nil {
			slog.ErrorContext(c.Request.Context(), "apparmor: acknowledgement not deleted", "profile", name, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profile": name, "acknowledged": false})
	})
}

// withAppArmorAcks adds "acknowledged" (the profiles whose acknowledgement
// still holds) to a security.apparmor.status body. On any failure it returns
// the body unchanged: no list means no profile shows as acknowledged.
func withAppArmorAcks(ctx context.Context, acks repository.AppArmorModeAckRepository, raw json.RawMessage) json.RawMessage {
	acked := currentAppArmorAcks(ctx, acks, raw)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return raw
	}
	names := make([]string, 0, len(acked))
	for name := range acked {
		names = append(names, name)
	}
	sort.Strings(names)
	list, _ := json.Marshal(names)
	body["acknowledged"] = list
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}

// appArmorAcknowledged reports whether a profile in mode is left out of the
// AppArmor alert because an administrator acknowledged it in that mode
// (GH #2078). Only complain can be acknowledged: a missing or unconfined
// profile is a fault, never a choice, so it is always named.
func appArmorAcknowledged(mode, ackedMode string) bool {
	return mode == "complain" && ackedMode == "complain"
}

// currentAppArmorAcks returns profile → acknowledged mode for the
// acknowledgements that still hold, and deletes those whose profile the
// summary shows in another mode, or no longer lists, so an acknowledgement
// ends when the mode changes (GH #2078). A summary that wasn't read (nil, or
// AppArmor off) deletes nothing. A failed read of the acknowledgements returns
// nil: the alert then names every profile.
func currentAppArmorAcks(ctx context.Context, repo repository.AppArmorModeAckRepository, summary json.RawMessage) map[string]string {
	if repo == nil {
		return nil
	}
	rows, err := repo.List(ctx)
	if err != nil {
		slog.WarnContext(ctx, "apparmor: acknowledgements unreadable", "error", err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	modes, observed := appArmorProfileModes(summary)
	acked := map[string]string{}
	for _, r := range rows {
		if !observed || modes[r.Profile] == r.Mode {
			acked[r.Profile] = r.Mode
			continue
		}
		if err := repo.Delete(ctx, r.Profile); err != nil {
			slog.WarnContext(ctx, "apparmor: stale acknowledgement not deleted", "profile", r.Profile, "error", err)
		}
	}
	return acked
}

// appArmorProfileModes reads profile → mode from a security.apparmor.summary
// or security.apparmor.status body. ok is false when there is no body, it
// doesn't parse, or AppArmor is off: no mode was observed.
func appArmorProfileModes(raw json.RawMessage) (map[string]string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var payload struct {
		Enabled  bool `json:"enabled"`
		Profiles []struct {
			Name string `json:"name"`
			Mode string `json:"mode"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || !payload.Enabled {
		return nil, false
	}
	modes := make(map[string]string, len(payload.Profiles))
	for _, p := range payload.Profiles {
		modes[p.Name] = p.Mode
	}
	return modes, true
}
