package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// users_ssh_forwarding.go — admin opt-in for a hosting user's SSH TCP
// forwarding, GH #1229. VS Code Remote-SSH needs to forward to its own VS Code
// Server on 127.0.0.1, which the JAB-352 sandbox lockdown blocks. The opt-in is
// a durable per-user flag (users.ssh_forwarding_enabled); the SSH reconciler
// converges jabali-ssh-forward group membership from it (join → excluded from
// the lockdown Match block, into the loopback-only one). Default OFF keeps
// JAB-352 unchanged for everyone.
//
// Admin-only to flip: relaxing a hardening control is an operator decision. A
// tenant can only READ their own status (GET /me/ssh-forwarding) so the SSH
// page shows it and points them at their admin. Either way the sensitive
// loopback services stay firewall-blocked per-uid, so an opted-in user can
// still only forward to their own apps + the VS Code Server.

type sshForwardingRequest struct {
	Enabled bool `json:"enabled"`
}

// sshForwardingStatus is returned by both the admin setter and the tenant
// read. ssh_enabled lets the UI show the relevant note (the toggle is moot for
// a user whose package has no SSH shell).
type sshForwardingStatus struct {
	SSHForwardingEnabled bool `json:"ssh_forwarding_enabled"`
	SSHEnabled           bool `json:"ssh_enabled"`
}

// setSSHForwarding handles POST /admin/users/:id/ssh-forwarding {enabled}.
func (h *userHandler) setSSHForwarding(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}
	var req sshForwardingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	ctx := c.Request.Context()
	user, err := h.cfg.Repo.FindByID(ctx, userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	// Forwarding applies only to a hosting user with a Linux account.
	if user.Username == nil || *user.Username == "" || user.IsAdmin {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "not_a_hosting_user",
			"detail": "SSH forwarding applies only to hosting users with a Linux account.",
		})
		return
	}

	if err := h.cfg.Repo.SetSSHForwardingEnabled(ctx, userID, req.Enabled); err != nil {
		h.auditSSHForwarding(c, userID, req.Enabled, models.AuditResultError)
		h.log().ErrorContext(ctx, "ssh-forwarding: set flag failed",
			"user_id", userID, "enabled", req.Enabled, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db_write_failed"})
		return
	}
	h.auditSSHForwarding(c, userID, req.Enabled, models.AuditResultOK)

	// Converge the OS group now so the change takes effect on the user's next
	// SSH connection without waiting for the ~60s reconcile tick. Best-effort:
	// the reconciler self-heals from the flag anyway.
	if h.cfg.Reconciler != nil {
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.cfg.Reconciler.ReconcileSSHKeysForUser(rctx, userID); err != nil {
				h.log().WarnContext(rctx, "ssh-forwarding: reconcile after toggle failed",
					"user_id", userID, "error", err)
			}
		}()
	}

	c.JSON(http.StatusOK, sshForwardingStatus{
		SSHForwardingEnabled: req.Enabled,
		SSHEnabled:           h.userSSHEnabled(ctx, user),
	})
}

// getSSHForwarding handles GET /admin/users/:id/ssh-forwarding — admin read
// of a user's opt-in state + whether their package grants SSH at all.
func (h *userHandler) getSSHForwarding(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}
	ctx := c.Request.Context()
	user, err := h.cfg.Repo.FindByID(ctx, userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, sshForwardingStatus{
		SSHForwardingEnabled: user.SSHForwardingEnabled,
		SSHEnabled:           h.userSSHEnabled(ctx, user),
	})
}

// mySSHForwarding handles GET /me/ssh-forwarding — the caller's own status.
// Read-only: a tenant cannot flip the opt-in.
func (h *userHandler) mySSHForwarding(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	ctx := c.Request.Context()
	user, err := h.cfg.Repo.FindByID(ctx, claims.UserID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, sshForwardingStatus{
		SSHForwardingEnabled: user.SSHForwardingEnabled,
		SSHEnabled:           h.userSSHEnabled(ctx, user),
	})
}

// userSSHEnabled reports whether the user's package grants SSH shell access.
// A missing/unfetchable package is the safe default (no SSH).
func (h *userHandler) userSSHEnabled(ctx context.Context, user *models.User) bool {
	if h.cfg.Packages == nil || user.PackageID == nil || *user.PackageID == "" {
		return false
	}
	pkg, err := h.cfg.Packages.FindByID(ctx, *user.PackageID)
	if err != nil || pkg == nil {
		return false
	}
	return pkg.SSHEnabled
}

// auditSSHForwarding records the admin toggle of this security control. No-op
// when the audit repo isn't wired (dev binaries).
func (h *userHandler) auditSSHForwarding(c *gin.Context, userID string, enabled bool, result string) {
	if h.cfg.AuditEvents == nil {
		return
	}
	meta, _ := json.Marshal(map[string]bool{"enabled": enabled})
	subject := userID
	ev := &models.AuditEvent{
		ID:            ids.NewULID(),
		TS:            time.Now().UTC(),
		ActorKind:     models.AuditActorAdmin,
		SubjectUserID: &subject,
		Action:        "admin.user.ssh_forwarding",
		TargetType:    "user",
		TargetID:      userID,
		Result:        result,
		Meta:          meta,
	}
	if claims := ginctx.Claims(c); claims != nil && claims.UserID != "" {
		actor := claims.UserID
		ev.ActorUserID = &actor
	}
	if ip := c.ClientIP(); ip != "" {
		ev.SourceIP = &ip
	}
	if reqID := ginctx.RequestID(c); reqID != "" {
		ev.RequestID = &reqID
	}
	_ = h.cfg.AuditEvents.Create(c.Request.Context(), ev)
}

// log returns a non-nil logger for this handler.
func (h *userHandler) log() *slog.Logger {
	if h.cfg.Log != nil {
		return h.cfg.Log
	}
	return slog.Default()
}
