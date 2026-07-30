package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// users_sessions.go — GH #338. Admin "Active Sessions" view: list live panel
// (Kratos) sessions with user + source IP + channel, and revoke one. Backed by
// the Kratos admin sessions API (kratosclient.ListActiveSessions / RevokeSession).

// sessionRow is the UI-facing shape (flattened from the Kratos session).
type sessionRow struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	Username        string `json:"username"`
	IsAdmin         bool   `json:"is_admin"`
	IP              string `json:"ip"`
	UserAgent       string `json:"user_agent"`
	Channel         string `json:"channel"`
	AAL             string `json:"aal"`
	AuthenticatedAt string `json:"authenticated_at"`
	ExpiresAt       string `json:"expires_at"`
}

// listSessions handles GET /admin/sessions — all active panel sessions.
func (h *userHandler) listSessions(c *gin.Context) {
	if h.cfg.KratosClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kratos_unavailable"})
		return
	}
	sessions, err := h.cfg.KratosClient.ListActiveSessions(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_sessions_failed", "detail": err.Error()})
		return
	}
	rows := make([]sessionRow, 0, len(sessions))
	for _, s := range sessions {
		r := sessionRow{
			ID: s.ID, AAL: s.AAL, Channel: "panel",
			AuthenticatedAt: s.AuthenticatedAt, ExpiresAt: s.ExpiresAt,
		}
		if s.Identity != nil {
			r.Email = s.Identity.GetTraitEmail()
			r.Username = s.Identity.GetTraitUsername()
			// JAB-5: the panel DB is authoritative for role — never trust
			// the Kratos is_admin trait for display or anything else.
			// Resolve from the DB by the session's email; fall back to the
			// trait only when no panel row matches (deleted user, or a
			// dev environment running without Kratos↔DB in lock-step).
			r.IsAdmin = s.Identity.GetTraitIsAdmin()
			if h.cfg.Repo != nil && r.Email != "" {
				if u, uErr := h.cfg.Repo.FindByEmail(c.Request.Context(), r.Email); uErr == nil && u != nil {
					r.IsAdmin = u.IsAdmin
				}
			}
		}
		if len(s.Devices) > 0 {
			r.IP = s.Devices[0].IPAddress
			r.UserAgent = s.Devices[0].UserAgent
		}
		rows = append(rows, r)
	}
	// GH #338 (SSH channel): merge live SSH sessions from the agent.
	if h.cfg.Agent != nil {
		if raw, aerr := h.cfg.Agent.Call(c.Request.Context(), "sessions.ssh.list", nil); aerr == nil {
			var sshResp struct {
				Sessions []struct {
					ID       string `json:"id"`
					User     string `json:"user"`
					RemoteIP string `json:"remote_ip"`
					Since    string `json:"since"`
					Channel  string `json:"channel"`
				} `json:"sessions"`
			}
			if json.Unmarshal(raw, &sshResp) == nil {
				for _, ss := range sshResp.Sessions {
					ch := ss.Channel // "ssh" | "sftp" (GH #338)
					if ch == "" {
						ch = "ssh"
					}
					rows = append(rows, sessionRow{
						ID: ss.ID, Username: ss.User, IP: ss.RemoteIP,
						Channel: ch, AuthenticatedAt: ss.Since,
					})
				}
			}
		}
	}
	// JAB-179: the public demo must never publish real visitors' source IPs
	// or user-agents (third-party PII). Mask both for every row — Kratos and
	// SSH sourced — at this single choke point. No-op in production.
	if DemoRedactionEnabled() {
		for i := range rows {
			rows[i].IP = demoMaskIP(rows[i].IP)
			rows[i].UserAgent = demoRedact(rows[i].UserAgent)
		}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": rows})
}

// revokeSession handles DELETE /admin/sessions/:id — deactivate one session.
func (h *userHandler) revokeSession(c *gin.Context) {
	id := c.Param("id")
	c.Set("audit_target_type", "session")
	// GH #338: SSH sessions revoke via the agent (terminate the sshd process);
	// panel/Kratos sessions revoke via Kratos.
	if strings.HasPrefix(id, "ssh:") {
		c.Set("audit_target", id+" (ssh session revoke)")
		if h.cfg.Agent == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
			return
		}
		pid, perr := strconv.Atoi(strings.TrimPrefix(id, "ssh:"))
		if perr != nil || pid < 2 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_ssh_session"})
			return
		}
		if _, err := h.cfg.Agent.Call(c.Request.Context(), "sessions.ssh.revoke", map[string]any{"pid": pid}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "revoke_failed", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	c.Set("audit_target", id+" (panel session revoke)")
	if h.cfg.KratosClient == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "kratos_unavailable"})
		return
	}
	if err := h.cfg.KratosClient.RevokeSession(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revoke_failed", "detail": err.Error()})
		return
	}
	// JAB-3: the revoked session is now dead in Kratos, but panel-api caches
	// positive whoami results (TTL ~10s). The DELETE carries only a session ID,
	// which we can't map to a cache entry (keyed by cookie hash), so flush the
	// whole positive cache — a rare, admin-only emergency action — to make the
	// revocation effective immediately instead of lingering for the TTL.
	h.cfg.KratosClient.ClearCache()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
