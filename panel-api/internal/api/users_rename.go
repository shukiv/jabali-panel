package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// users_rename.go — admin action to rename a tenant's Linux/login username in
// place (GH #1238, WebUI follow-up to the `jabali user rename` CLI).
//
// The heavy lifting lives in userops.RenameUser (shared with the CLI): the agent
// does usermod -l / -d -m, the panel repoints the DB + Kratos login, and the
// owned domains re-render. This handler is the thin admin surface: it's a
// data-moving, login-changing operation, so it's admin-only AND gated behind the
// JAB-380 recent-auth step-up, same as the root File Manager. The Reconciler is
// left nil (as the CLI does) so the owned domains re-render on the periodic
// reconcile rather than blocking the HTTP request on a long synchronous pass.

type renameUserRequest struct {
	NewUsername string `json:"new_username"`
}

// rename handles POST /admin/users/:id/rename { new_username }.
func (h *userHandler) rename(c *gin.Context) {
	// Root-privileged surface: require a recently-authenticated interactive
	// session (JAB-380). A stale session gets a 403 the SPA turns into a
	// re-auth + retry.
	if !requireRecentAuth(c, h.cfg.KratosClient, stepUpWindow) {
		return
	}

	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}
	var req renameUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	// Rename touches the OS account + home move; bound it so a wedged agent
	// can't pin the request forever (same 5-min ceiling the CLI uses).
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	target, err := h.cfg.Repo.FindByID(ctx, userID)
	if err != nil || target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	d := userops.Deps{
		Users:        h.cfg.Repo,
		Domains:      h.cfg.Domains,
		KratosClient: h.cfg.KratosClient,
		Agent:        h.cfg.Agent,
		Log:          h.log(),
	}
	rd := userops.RenameDeps{
		FtpAccounts: h.cfg.FtpAccounts,
		PythonApps:  h.cfg.PythonApps,
		// Reconciler nil: the periodic reconcile re-renders the owned domains
		// (mirrors the CLI), keeping the HTTP request off a long re-render.
	}

	if err := userops.RenameUser(ctx, d, rd, target, req.NewUsername); err != nil {
		h.auditRename(c, userID, req.NewUsername, models.AuditResultError)
		// userops.RenameUser's errors are user-actionable (invalid name, v1
		// refusals like "has FTP subaccounts", cross-fs, already-taken) — pass
		// the message through so the admin sees exactly why.
		status := http.StatusUnprocessableEntity
		if errors.Is(err, repository.ErrConflict) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": "rename_failed", "detail": err.Error()})
		return
	}
	h.auditRename(c, userID, req.NewUsername, models.AuditResultOK)

	c.JSON(http.StatusOK, gin.H{
		"id":       userID,
		"username": req.NewUsername,
	})
}

// auditRename records the admin rename of a tenant account. No-op when the audit
// repo isn't wired (dev binaries).
func (h *userHandler) auditRename(c *gin.Context, userID, newName, result string) {
	if h.cfg.AuditEvents == nil {
		return
	}
	meta, _ := json.Marshal(map[string]string{"new_username": newName})
	subject := userID
	ev := &models.AuditEvent{
		ID:            ids.NewULID(),
		TS:            time.Now().UTC(),
		ActorKind:     models.AuditActorAdmin,
		SubjectUserID: &subject,
		Action:        "admin.user.rename",
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
