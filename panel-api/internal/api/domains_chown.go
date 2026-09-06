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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// domains_chown.go — admin action to reassign a domain to a new tenant in place
// (GH #1238, WebUI follow-up to the `jabali domain chown` CLI).
//
// The heavy lifting lives in userops.ChangeDomainOwner (shared with the CLI):
// the agent moves + re-owns the docroot tree (domain.reown), the panel repoints
// the DB row (TransferOwner — domain_id survives, so tombstones/SSL/DNS/mail
// stay), and the reconciler re-binds the PHP pool + re-renders the vhost under
// the new owner. This handler is the thin admin surface: a data-moving,
// cross-tenant operation, so it's admin-only AND behind the JAB-380 recent-auth
// step-up, same as rename.
//
// v1 REFUSES a domain that has an app install (its config holds the current
// owner's DB credentials → cross-tenant leak). That refusal lives in userops
// and depends on AppInstalls being wired; this handler 503s when it isn't,
// rather than let the security check fail open.

type chownDomainRequest struct {
	NewOwnerID string `json:"new_owner_id"`
}

// chown handles POST /admin/domains/:id/chown { new_owner_id }.
func (h *domainHandler) chown(c *gin.Context) {
	// Root-privileged, cross-tenant surface: require a recently-authenticated
	// interactive session (JAB-380). A stale session gets a 403 the SPA turns
	// into a re-auth + retry.
	if !requireRecentAuth(c, h.cfg.KratosClient, stepUpWindow) {
		return
	}

	// Fail closed: the cross-tenant-credential refusal in userops is only armed
	// when AppInstalls is wired. Never let a missing dep silently disable it.
	if h.cfg.AppInstalls == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "chown_unavailable", "detail": "app-install repository not wired"})
		return
	}

	domainID := c.Param("id")
	if domainID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain id required"})
		return
	}
	var req chownDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if req.NewOwnerID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "new_owner_id required"})
		return
	}

	// Moving + re-owning the docroot goes through the agent; bound it so a
	// wedged agent can't pin the request forever (same 5-min ceiling the CLI
	// uses).
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	domain, err := h.cfg.Domains.FindByID(ctx, domainID)
	if err != nil || domain == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}
	newOwner, err := h.cfg.Users.FindByID(ctx, req.NewOwnerID)
	if err != nil || newOwner == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "new owner not found"})
		return
	}

	// Capture the pre-change state for the audit record before userops mutates
	// the in-memory domain.
	oldOwnerID := domain.UserID
	oldDocRoot := domain.DocRoot

	d := userops.Deps{
		Users:   h.cfg.Users,
		Domains: h.cfg.Domains,
		Agent:   h.cfg.Agent,
		Log:     slog.Default(),
	}
	cd := userops.ChownDeps{AppInstalls: h.cfg.AppInstalls}
	// Unlike rename (which leaves the reconciler nil and waits for the periodic
	// pass), chown wires it: Reconciler.Schedule is a non-blocking enqueue, so
	// the docroot/PHP-pool rebind + vhost re-render happen promptly without the
	// request blocking on a synchronous convergence.
	//
	// Typed-nil guard: cfg.Reconciler is a *reconciler.Reconciler; assigning a
	// nil pointer straight to the ChownReconciler interface yields a non-nil
	// interface, so userops' `cd.Reconciler != nil` would call Schedule on a nil
	// receiver and panic. Only wire it when the pointer is actually set.
	if h.cfg.Reconciler != nil {
		cd.Reconciler = h.cfg.Reconciler
	}

	if err := userops.ChangeDomainOwner(ctx, d, cd, domain, newOwner); err != nil {
		h.auditChown(c, domainID, oldOwnerID, req.NewOwnerID, oldDocRoot, "", models.AuditResultError)
		// ChangeDomainOwner's errors are user-actionable (already-owner, not a
		// tenant, has an app install, docroot outside home, agent failure) —
		// pass the message through so the admin sees exactly why.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "chown_failed", "detail": err.Error()})
		return
	}
	h.auditChown(c, domainID, oldOwnerID, req.NewOwnerID, oldDocRoot, domain.DocRoot, models.AuditResultOK)

	c.JSON(http.StatusOK, gin.H{
		"id":       domainID,
		"user_id":  domain.UserID,
		"doc_root": domain.DocRoot,
	})
}

// auditChown records the admin change-of-owner. The subject is the OLD owner
// (their asset moved away); the new owner + docroot move land in meta. No-op
// when the audit repo isn't wired (dev binaries).
func (h *domainHandler) auditChown(c *gin.Context, domainID, oldOwnerID, newOwnerID, oldDocRoot, newDocRoot, result string) {
	if h.cfg.AuditEvents == nil {
		return
	}
	meta, _ := json.Marshal(map[string]string{
		"new_owner_id": newOwnerID,
		"old_doc_root": oldDocRoot,
		"new_doc_root": newDocRoot,
	})
	subject := oldOwnerID
	ev := &models.AuditEvent{
		ID:            ids.NewULID(),
		TS:            time.Now().UTC(),
		ActorKind:     models.AuditActorAdmin,
		SubjectUserID: &subject,
		Action:        "admin.domain.chown",
		TargetType:    "domain",
		TargetID:      domainID,
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
