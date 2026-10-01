// GH #1816 / ADR-0170 — domain ownership proof: the owner's view and
// Verify now, and the administrator's pending list, approve, revoke and the
// proof-required switch.
//
//	GET  /domains/:id/ownership                               owner or admin
//	POST /domains/:id/ownership/verify                        owner or admin, rate-limited
//	POST /domains/:id/aliases/:alias_id/ownership/verify      owner or admin, rate-limited
//	GET  /admin/domain-ownership/pending                      admin
//	POST /admin/domain-ownership/domains/:id/approve          admin, audited
//	POST /admin/domain-ownership/domains/:id/revoke           admin, audited
//	POST /admin/domain-ownership/aliases/:alias_id/approve    admin, audited
//	POST /admin/domain-ownership/aliases/:alias_id/revoke     admin, audited
//	GET  /admin/domain-ownership/settings                     admin
//	PUT  /admin/domain-ownership/settings                     admin, audited
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ownershipops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// OwnershipActions are the ownership-proof actions (ownershipops.Service).
type OwnershipActions interface {
	CheckDomain(ctx context.Context, d *models.Domain) (string, error)
	CheckAlias(ctx context.Context, a *models.WebDomainAlias) (string, error)
	ApproveDomain(ctx context.Context, id string) (bool, error)
	RevokeDomain(ctx context.Context, id string) (bool, error)
	ApproveAlias(ctx context.Context, id string) (bool, error)
	RevokeAlias(ctx context.Context, id string) (bool, error)
}

// DomainOwnershipHandlerConfig wires the routes. Domains, Store and Actions
// are required; without them no route is registered.
type DomainOwnershipHandlerConfig struct {
	Domains repository.DomainRepository
	Aliases repository.WebDomainAliasRepository
	Store   repository.DomainOwnershipRepository
	Actions OwnershipActions
	// Users names the owner in the admin pending list. Optional.
	Users repository.UserRepository
	// Audit records the admin actions. Optional.
	Audit audit.Recorder
}

// verifyPerMinute caps Verify now per user: each click asks three public
// resolvers.
const verifyPerMinute = 6

// ownershipCheckTimeout bounds one Verify now (three resolvers and an NS read).
const ownershipCheckTimeout = 30 * time.Second

func RegisterDomainOwnershipRoutes(g *gin.RouterGroup, cfg DomainOwnershipHandlerConfig) {
	if cfg.Domains == nil || cfg.Store == nil || cfg.Actions == nil {
		return
	}
	h := &domainOwnershipHandler{cfg: cfg}
	limit := verifyRateLimit(newBroadcastLimit(time.Minute, verifyPerMinute))
	g.GET("/domains/:id/ownership", h.get)
	g.POST("/domains/:id/ownership/verify", limit, h.verify)
	if cfg.Aliases != nil {
		g.POST("/domains/:id/aliases/:alias_id/ownership/verify", limit, h.verifyAlias)
	}

	admin := g.Group("/admin/domain-ownership", middleware.RequireAdmin())
	admin.GET("/pending", h.listPending)
	admin.POST("/domains/:id/approve", h.approveDomain)
	admin.POST("/domains/:id/revoke", h.revokeDomain)
	if cfg.Aliases != nil {
		admin.POST("/aliases/:alias_id/approve", h.approveAlias)
		admin.POST("/aliases/:alias_id/revoke", h.revokeAlias)
	}
	admin.GET("/settings", h.getSettings)
	admin.PUT("/settings", h.putSettings)
}

// verifyRateLimit answers 429 once a user has asked for verifyPerMinute
// checks within a minute (the per-user bucket of the broadcast limiter).
func verifyRateLimit(l *perAdminBroadcastLimit) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := ginctx.Claims(c)
		if claims == nil || claims.UserID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
			return
		}
		if !l.allow(claims.UserID) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":  "rate_limited",
				"detail": "too many checks in a minute; the panel also re-checks on its own",
			})
			return
		}
		c.Next()
	}
}

type domainOwnershipHandler struct{ cfg DomainOwnershipHandlerConfig }

// ownershipView is the wire shape of one name's ownership state, with the
// record its owner must publish.
type ownershipView struct {
	Status         string     `json:"status"`
	Method         string     `json:"method"`
	ChallengeName  string     `json:"challenge_name"`
	ChallengeValue string     `json:"challenge_value"`
	LastResult     string     `json:"last_result"`
	PendingSince   *time.Time `json:"pending_since,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
	NextCheckAt    *time.Time `json:"next_check_at,omitempty"`
	// ExpiresAt is when an unproven name is released; absent when it never is.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func newOwnershipView(name string, st models.OwnershipState, expires *time.Time) ownershipView {
	status := st.OwnershipStatus
	if status != models.OwnershipVerified {
		status = models.OwnershipPending // unknown and empty read as pending
	}
	return ownershipView{
		Status:         status,
		Method:         st.OwnershipMethod,
		ChallengeName:  domainops.OwnershipChallengeName(name),
		ChallengeValue: domainops.OwnershipChallengeValue(st.OwnershipToken),
		LastResult:     st.OwnershipLastResult,
		PendingSince:   st.OwnershipPendingSince,
		VerifiedAt:     st.OwnershipVerifiedAt,
		CheckedAt:      st.OwnershipCheckedAt,
		NextCheckAt:    st.OwnershipNextCheckAt,
		ExpiresAt:      expires,
	}
}

func domainOwnershipView(d *models.Domain) ownershipView {
	return newOwnershipView(d.Name, d.OwnershipState, domainops.DomainOwnershipExpires(d))
}

func aliasOwnershipView(a *models.WebDomainAlias) ownershipView {
	return newOwnershipView(a.Hostname, a.OwnershipState, domainops.OwnershipExpiresAt(a.OwnershipState))
}

// ownedDomain loads :id for its owner or an admin. Another tenant's domain
// is a 404, as everywhere else.
func (h *domainOwnershipHandler) ownedDomain(c *gin.Context) (*models.Domain, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil, false
	}
	d, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && d.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return nil, false
	}
	return d, true
}

func (h *domainOwnershipHandler) get(c *gin.Context) {
	d, ok := h.ownedDomain(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, domainOwnershipView(d))
}

func (h *domainOwnershipHandler) verify(c *gin.Context) {
	d, ok := h.ownedDomain(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), ownershipCheckTimeout)
	defer cancel()
	result, err := h.cfg.Actions.CheckDomain(ctx, d)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	fresh, err := h.cfg.Domains.FindByID(ctx, d.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": result, "ownership": domainOwnershipView(fresh)})
}

func (h *domainOwnershipHandler) verifyAlias(c *gin.Context) {
	d, ok := h.ownedDomain(c)
	if !ok {
		return
	}
	a, err := h.cfg.Aliases.FindByID(c.Request.Context(), c.Param("alias_id"))
	if err != nil || a.DomainID != d.ID {
		if err == nil || isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "alias_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), ownershipCheckTimeout)
	defer cancel()
	result, err := h.cfg.Actions.CheckAlias(ctx, a)
	if err != nil && !errors.Is(err, repository.ErrOwnershipChanged) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	fresh, err := h.cfg.Aliases.FindByID(ctx, a.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": result, "ownership": aliasOwnershipView(fresh)})
}

type pendingDomainRow struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	UserID   string        `json:"user_id"`
	Username string        `json:"username,omitempty"`
	State    ownershipView `json:"ownership"`
}

type pendingAliasRow struct {
	ID         string        `json:"id"`
	Hostname   string        `json:"hostname"`
	DomainID   string        `json:"domain_id"`
	DomainName string        `json:"domain_name"`
	UserID     string        `json:"user_id"`
	Username   string        `json:"username,omitempty"`
	State      ownershipView `json:"ownership"`
}

func (h *domainOwnershipHandler) listPending(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	names := map[string]string{}
	username := func(id string) string {
		if h.cfg.Users == nil || id == "" {
			return ""
		}
		if n, ok := names[id]; ok {
			return n
		}
		n := ""
		if u, err := h.cfg.Users.FindByID(ctx, id); err == nil && u != nil && u.Username != nil {
			n = *u.Username
		}
		names[id] = n
		return n
	}

	domains, err := h.cfg.Store.ListPendingDomains(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]pendingDomainRow, 0, len(domains))
	for i := range domains {
		d := &domains[i]
		out = append(out, pendingDomainRow{ID: d.ID, Name: d.Name, UserID: d.UserID,
			Username: username(d.UserID), State: domainOwnershipView(d)})
	}

	aliasRows := make([]pendingAliasRow, 0)
	if h.cfg.Aliases != nil {
		aliases, err := h.cfg.Store.ListPendingAliases(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		for i := range aliases {
			a := &aliases[i]
			row := pendingAliasRow{ID: a.ID, Hostname: a.Hostname, DomainID: a.DomainID, State: aliasOwnershipView(a)}
			if d, err := h.cfg.Domains.FindByID(ctx, a.DomainID); err == nil {
				row.DomainName, row.UserID, row.Username = d.Name, d.UserID, username(d.UserID)
			}
			aliasRows = append(aliasRows, row)
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out), "aliases": aliasRows})
}

func (h *domainOwnershipHandler) record(c *gin.Context, subjectUserID, action, targetType, targetID string, meta map[string]any) {
	if h.cfg.Audit == nil {
		return
	}
	actor := ""
	if cl := ginctx.Claims(c); cl != nil {
		actor = cl.UserID
	}
	h.cfg.Audit.Record(audit.DomainOwnership(actor, subjectUserID, action, targetType, targetID, c.ClientIP(), ginctx.RequestID(c), meta))
}

// adminDomain loads :id for an admin action.
func (h *domainOwnershipHandler) adminDomain(c *gin.Context) (*models.Domain, bool) {
	d, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return d, true
}

func (h *domainOwnershipHandler) approveDomain(c *gin.Context) {
	d, ok := h.adminDomain(c)
	if !ok {
		return
	}
	changed, err := h.cfg.Actions.ApproveDomain(c.Request.Context(), d.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !changed {
		c.JSON(http.StatusConflict, gin.H{"error": "not_pending", "detail": "the domain is not waiting for proof"})
		return
	}
	h.record(c, d.UserID, "domain.ownership.approve", "domain", d.ID, map[string]any{"domain": d.Name})
	c.JSON(http.StatusOK, gin.H{"id": d.ID, "status": models.OwnershipVerified, "method": models.OwnershipMethodAdmin})
}

func (h *domainOwnershipHandler) revokeDomain(c *gin.Context) {
	d, ok := h.adminDomain(c)
	if !ok {
		return
	}
	changed, err := h.cfg.Actions.RevokeDomain(c.Request.Context(), d.ID)
	switch {
	case errors.Is(err, ownershipops.ErrPanelPrimary):
		c.JSON(http.StatusConflict, gin.H{"error": "panel_primary_protected", "detail": "the panel's own domain cannot be revoked"})
		return
	case errors.Is(err, ownershipops.ErrDockerAppDomain):
		c.JSON(http.StatusConflict, gin.H{"error": "docker_app_domain", "detail": "a docker app's domain cannot be revoked; remove the app instead"})
		return
	}
	if changed {
		h.record(c, d.UserID, "domain.ownership.revoke", "domain", d.ID, map[string]any{"domain": d.Name})
	}
	if err != nil {
		// The cascade to the names under it did not finish. Repeating the
		// revoke finishes it.
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cascade_incomplete",
			"detail": "some names under this domain were not updated; repeat the revoke"})
		return
	}
	if !changed {
		c.JSON(http.StatusConflict, gin.H{"error": "not_verified", "detail": "the domain is already waiting for proof"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": d.ID, "status": models.OwnershipPending})
}

// adminAlias loads :alias_id and its domain for an admin action.
func (h *domainOwnershipHandler) adminAlias(c *gin.Context) (*models.WebDomainAlias, *models.Domain, bool) {
	a, err := h.cfg.Aliases.FindByID(c.Request.Context(), c.Param("alias_id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "alias_not_found"})
			return nil, nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, false
	}
	d, err := h.cfg.Domains.FindByID(c.Request.Context(), a.DomainID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, false
	}
	return a, d, true
}

func (h *domainOwnershipHandler) approveAlias(c *gin.Context) {
	a, d, ok := h.adminAlias(c)
	if !ok {
		return
	}
	changed, err := h.cfg.Actions.ApproveAlias(c.Request.Context(), a.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !changed {
		c.JSON(http.StatusConflict, gin.H{"error": "not_pending", "detail": "the alias is not waiting for proof"})
		return
	}
	h.record(c, d.UserID, "domain.alias.ownership.approve", "domain_alias", a.ID, map[string]any{"alias": a.Hostname, "domain": d.Name})
	c.JSON(http.StatusOK, gin.H{"id": a.ID, "status": models.OwnershipVerified, "method": models.OwnershipMethodAdmin})
}

func (h *domainOwnershipHandler) revokeAlias(c *gin.Context) {
	a, d, ok := h.adminAlias(c)
	if !ok {
		return
	}
	changed, err := h.cfg.Actions.RevokeAlias(c.Request.Context(), a.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !changed {
		c.JSON(http.StatusConflict, gin.H{"error": "not_verified", "detail": "the alias is already waiting for proof"})
		return
	}
	h.record(c, d.UserID, "domain.alias.ownership.revoke", "domain_alias", a.ID, map[string]any{"alias": a.Hostname, "domain": d.Name})
	c.JSON(http.StatusOK, gin.H{"id": a.ID, "status": models.OwnershipPending})
}

func (h *domainOwnershipHandler) getSettings(c *gin.Context) {
	s, err := h.cfg.Store.GetSettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, s)
}

type ownershipSettingsRequest struct {
	RequireProof *bool `json:"require_proof" binding:"required"`
}

// proofOffWarning is returned when an admin switches the requirement off.
const proofOffWarning = "Tenants can now add any domain name without proving they control it, including names that belong to someone else. " +
	"Domains added while proof is off are live at once and stay live when you switch it back on. " +
	"Domains already waiting for proof stay pending; approve them from the pending list."

func (h *domainOwnershipHandler) putSettings(c *gin.Context) {
	var req ownershipSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.RequireProof == nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": "require_proof (boolean) is required"})
		return
	}
	actor := ""
	if cl := ginctx.Claims(c); cl != nil {
		actor = cl.UserID
	}
	if err := h.cfg.Store.SetRequireProof(c.Request.Context(), *req.RequireProof, actor, time.Now().UTC()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.record(c, "", "domain.ownership.policy", "server", "domain_ownership", map[string]any{"require_proof": *req.RequireProof})
	s, err := h.cfg.Store.GetSettings(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	resp := gin.H{"require_proof": s.RequireProof, "updated_by": s.UpdatedBy, "updated_at": s.UpdatedAt}
	if !s.RequireProof {
		resp["warning"] = proofOffWarning
	}
	c.JSON(http.StatusOK, resp)
}
