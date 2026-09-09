package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// databases_chown.go — admin action to reassign a database (and the DB users
// bound only to it) to a new tenant in place (GH #1609).
//
// The heavy lifting lives in dbops.ReassignDatabaseOwner (shared, so a CLI
// subcommand can reuse it): the agent renames the database + its users onto the
// new owner's prefix (db.rename_db / db.rename_user) and re-points their grants,
// then the panel repoints the rows' owner + name. Like domain chown this is a
// data-moving, cross-tenant operation, so it's admin-only AND behind the
// JAB-380 recent-auth step-up.
//
// v1 refuses a database backing an app install (its config holds the old owner's
// credentials → cross-tenant leak), a shared DB user, a postgres database, and
// off-convention names — see dbops_chown.go. The app-install refusal depends on
// Installs being wired; this handler 503s when it isn't rather than fail open.

type chownDatabaseRequest struct {
	NewOwnerID string `json:"new_owner_id"`
}

// chown handles POST /admin/databases/:id/chown { new_owner_id }.
func (h *databaseHandler) chown(c *gin.Context) {
	// Root-privileged, cross-tenant surface: require a recently-authenticated
	// session (JAB-380). A stale session gets a 403 the SPA turns into re-auth.
	if !requireRecentAuth(c, h.cfg.KratosClient, stepUpWindow) {
		return
	}

	// Fail closed: the cross-tenant-credential (app-install) refusal in dbops is
	// only armed when Installs is wired. Never let a missing dep disable it.
	if h.cfg.Installs == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "chown_unavailable", "detail": "app-install repository not wired"})
		return
	}

	databaseID := c.Param("id")
	if databaseID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "database id required"})
		return
	}
	var req chownDatabaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if req.NewOwnerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "new_owner_id required"})
		return
	}

	// The rename + regrant go through the agent; bound it so a wedged agent can't
	// pin the request forever (same ceiling domain chown uses).
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	// Capture the pre-change owner for the audit before dbops mutates the row.
	var oldOwnerID string
	if db, err := h.cfg.Databases.FindByID(ctx, databaseID); err == nil && db != nil {
		oldOwnerID = db.UserID
	}

	d := dbops.Deps{
		Users:          h.cfg.Users,
		Packages:       h.cfg.Packages,
		Databases:      h.cfg.Databases,
		ServerSettings: h.cfg.ServerSettings,
		DatabaseGrants: h.cfg.DatabaseGrants,
		DatabaseUsers:  h.cfg.DatabaseUsers,
		Installs:       h.cfg.Installs,
		Agent:          h.cfg.Agent,
		Log:            slog.Default(),
	}
	res, err := dbops.ReassignDatabaseOwner(ctx, d, dbops.ReassignInput{
		DatabaseID: databaseID,
		NewOwnerID: req.NewOwnerID,
	})
	if err != nil {
		h.auditDBChown(c, databaseID, oldOwnerID, req.NewOwnerID, "", models.AuditResultError)
		status, body := dbChownErrorResponse(err)
		c.JSON(status, body)
		return
	}

	h.auditDBChown(c, databaseID, oldOwnerID, req.NewOwnerID, res.NewName, models.AuditResultOK)
	c.JSON(http.StatusOK, gin.H{
		"id":            databaseID,
		"user_id":       res.Database.UserID,
		"name":          res.NewName,
		"renamed_users": res.RenamedUsers,
	})
}

// dbChownErrorResponse maps dbops sentinels to an HTTP status + body.
func dbChownErrorResponse(err error) (int, gin.H) {
	switch {
	case errors.Is(err, dbops.ErrDeps):
		return http.StatusServiceUnavailable, gin.H{"error": "chown_unavailable", "detail": err.Error()}
	case errors.Is(err, dbops.ErrNotFound):
		return http.StatusNotFound, gin.H{"error": "not_found", "detail": "database not found"}
	case errors.Is(err, dbops.ErrUserNotFound):
		return http.StatusNotFound, gin.H{"error": "not_found", "detail": "new owner not found"}
	case errors.Is(err, dbops.ErrAttached):
		// Same shape the delete path uses for an app-install-backed DB.
		var ae *dbops.AttachedError
		installID := ""
		if errors.As(err, &ae) {
			installID = ae.InstallID
		}
		return http.StatusConflict, gin.H{"error": "in_use_by_app", "detail": "this database backs an application install — detach or migrate it to the new owner first", "install_id": installID}
	case errors.Is(err, dbops.ErrAgentFailed), errors.Is(err, dbops.ErrInternal):
		return http.StatusInternalServerError, gin.H{"error": "chown_failed", "detail": err.Error()}
	default:
		// User-actionable refusals: same-owner, invalid owner, engine, prefix,
		// shared user, collision, quota, invalid input.
		return http.StatusUnprocessableEntity, gin.H{"error": "chown_failed", "detail": err.Error()}
	}
}

// auditDBChown records the admin change-of-owner. Subject = OLD owner (their
// asset moved away). No-op when the audit repo isn't wired.
func (h *databaseHandler) auditDBChown(c *gin.Context, databaseID, oldOwnerID, newOwnerID, newName, result string) {
	if h.cfg.AuditEvents == nil {
		return
	}
	meta, _ := json.Marshal(map[string]string{
		"new_owner_id": newOwnerID,
		"new_name":     newName,
	})
	ev := &models.AuditEvent{
		ID:         ids.NewULID(),
		TS:         time.Now().UTC(),
		ActorKind:  models.AuditActorAdmin,
		Action:     "admin.database.chown",
		TargetType: "database",
		TargetID:   databaseID,
		Result:     result,
		Meta:       meta,
	}
	if oldOwnerID != "" {
		subject := oldOwnerID
		ev.SubjectUserID = &subject
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
