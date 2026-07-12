package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// users_suspend.go — admin suspend/unsuspend. The multi-step cascade (DB flag,
// Kratos identity state + whoami invalidation, domain disable, Docker stop, OS
// user lock) lives in userops.Suspend/Unsuspend so the `jabali user
// suspend`/`unsuspend` CLI shares exactly the same behaviour (JAB-127).

type suspendRequest struct {
	// Reason is operator-facing audit text surfaced in the admin user list.
	Reason string `json:"reason"`
}

// suspendDeps wires userops.Deps from the handler config.
func (h *userHandler) suspendDeps() userops.Deps {
	return userops.Deps{
		Users:        h.cfg.Repo,
		Domains:      h.cfg.Domains,
		DockerApps:   h.cfg.DockerApps,
		Agent:        h.cfg.Agent,
		KratosClient: h.cfg.KratosClient,
		Log:          slog.Default(),
	}
}

// suspend handles POST /admin/users/:id/suspend.
//
// Suspending an admin user is refused — admins are the only path to
// re-enable anyone, so we never lock the whole panel out. Kratos client nil
// (dev binaries) is surfaced as a warning, not a hard failure.
func (h *userHandler) suspend(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	var req suspendRequest
	_ = c.ShouldBindJSON(&req) // body optional

	ctx := c.Request.Context()
	user, err := h.cfg.Repo.FindByID(ctx, userID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if user.IsAdmin {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "cannot_suspend_admin",
			"detail": "Refusing to suspend an admin user. Demote first via /admin/users.",
		})
		return
	}

	res, err := userops.Suspend(ctx, h.suspendDeps(), user, req.Reason)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "suspend_db_write_failed", "detail": err.Error()})
		return
	}
	if res.AlreadySuspended {
		c.JSON(http.StatusOK, gin.H{"ok": true, "already_suspended": true, "domains_disabled": int64(0)})
		return
	}

	resp := gin.H{"ok": true, "domains_disabled": res.DomainsDisabled}
	if res.KratosWarning != "" {
		resp["kratos_warning"] = res.KratosWarning
	}
	if res.DomainWarning != "" {
		resp["domain_warning"] = res.DomainWarning
	}
	if res.OSWarning != "" {
		resp["os_warning"] = res.OSWarning
	}
	c.JSON(http.StatusOK, resp)
}

// unsuspend handles POST /admin/users/:id/unsuspend. Reverses the cascade.
func (h *userHandler) unsuspend(c *gin.Context) {
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

	res, err := userops.Unsuspend(ctx, h.suspendDeps(), user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unsuspend_db_write_failed", "detail": err.Error()})
		return
	}
	if res.AlreadyActive {
		c.JSON(http.StatusOK, gin.H{"ok": true, "already_active": true, "domains_enabled": int64(0)})
		return
	}

	resp := gin.H{"ok": true, "domains_enabled": res.DomainsEnabled}
	if res.KratosWarning != "" {
		resp["kratos_warning"] = res.KratosWarning
	}
	if res.DomainWarning != "" {
		resp["domain_warning"] = res.DomainWarning
	}
	if res.OSWarning != "" {
		resp["os_warning"] = res.OSWarning
	}
	c.JSON(http.StatusOK, resp)
}
