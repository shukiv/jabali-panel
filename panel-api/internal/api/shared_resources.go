// shared_resources.go — M52 (ADR-0133) REST surface for standalone shared
// resources (calendar / address book / file folder / mailbox) and their
// grants. DB is truth; the reconciler converges to Stalwart host principals +
// per-collection shareWith. Create/delete also kick the agent directly for
// instant host provisioning (the mailgroup.apply pattern); grant changes
// converge on the next reconcile pass.
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const sharedResourceAgentTimeout = 15 * time.Second

var sharedResourceKinds = map[string]bool{
	"mailbox": true, "calendar": true, "addressbook": true, "files": true,
}
var sharedResourceRights = map[string]bool{
	"read": true, "readwrite": true, "admin": true,
}

type SharedResourceHandlerConfig struct {
	Resources repository.SharedResourceRepository
	Domains   repository.DomainRepository
	Agent     agent.AgentInterface
}

type sharedResourceHandler struct{ cfg SharedResourceHandlerConfig }

// RegisterSharedResourceRoutes mounts the M52 endpoints:
//
//   - GET    /domains/:id/shared-resources       list (optional ?kind=)
//   - POST   /domains/:id/shared-resources       create
//   - GET    /shared-resources/:rid              detail + grants
//   - PATCH  /shared-resources/:rid              update display name
//   - PUT    /shared-resources/:rid/grants       replace the grant set
//   - DELETE /shared-resources/:rid              delete (+ agent destroy)
func RegisterSharedResourceRoutes(g *gin.RouterGroup, cfg SharedResourceHandlerConfig) {
	if cfg.Resources == nil || cfg.Domains == nil {
		return
	}
	h := &sharedResourceHandler{cfg: cfg}
	g.GET("/domains/:id/shared-resources", h.list)
	g.POST("/domains/:id/shared-resources", h.create)
	grp := g.Group("/shared-resources")
	grp.GET("/:rid", h.get)
	grp.PATCH("/:rid", h.update)
	grp.PUT("/:rid/grants", h.setGrants)
	grp.DELETE("/:rid", h.delete)
}

// ---- request/response ----

type createSharedResourceRequest struct {
	Name        string `json:"name"` // local part of the host address
	Kind        string `json:"kind"` // mailbox|calendar|addressbook|files
	DisplayName string `json:"display_name"`
}

type grantInput struct {
	GranteeKind string `json:"grantee_kind"` // mailbox|group
	GranteeID   string `json:"grantee_id"`
	Rights      string `json:"rights"` // read|readwrite|admin
}

type sharedResourceWithGrants struct {
	models.SharedResource
	Grants []models.SharedResourceGrant `json:"grants"`
}

// ---- handlers ----

func (h *sharedResourceHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	dom, ok := h.loadDomainWithAuth(c, c.Param("id"), claims)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	var (
		rows []models.SharedResource
		err  error
	)
	if kind := c.Query("kind"); kind != "" {
		rows, err = h.cfg.Resources.ListByDomainAndKind(ctx, dom.ID, kind)
	} else {
		rows, err = h.cfg.Resources.ListByDomainID(ctx, dom.ID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}

func (h *sharedResourceHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	dom, ok := h.loadDomainWithAuth(c, c.Param("id"), claims)
	if !ok {
		return
	}
	if !dom.EmailEnabled {
		c.JSON(http.StatusConflict, gin.H{"error": "email_not_enabled",
			"detail": "enable email on the domain before creating shared resources"})
		return
	}
	var req createSharedResourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if !sharedResourceKinds[req.Kind] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_kind"})
		return
	}
	canonLocal, _, err := mailaddr.Canonicalise(req.Name + "@" + dom.Name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name", "detail": err.Error()})
		return
	}
	ctx := c.Request.Context()
	email := canonLocal + "@" + dom.Name
	if exists, err := h.cfg.Resources.ExistsByEmail(ctx, email); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	} else if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "address_taken"})
		return
	}

	now := time.Now().UTC()
	local := canonLocal
	sr := &models.SharedResource{
		ID:          ids.NewULID(),
		DomainID:    dom.ID,
		Kind:        req.Kind,
		LocalPart:   &local,
		EmailCached: &email,
		DisplayName: strings.TrimSpace(req.DisplayName),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.cfg.Resources.Create(ctx, sr); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Instant host provisioning (reconciler also converges).
	h.notifyAgent(ctx, "sharedresource.apply", map[string]any{
		"email": email, "display_name": sr.DisplayName, "kind": sr.Kind,
	})
	c.JSON(http.StatusCreated, sr)
}

func (h *sharedResourceHandler) get(c *gin.Context) {
	claims := ginctx.Claims(c)
	sr, _, ok := h.loadResourceWithAuth(c, c.Param("rid"), claims)
	if !ok {
		return
	}
	grants, err := h.cfg.Resources.ListGrants(c.Request.Context(), sr.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if grants == nil {
		grants = []models.SharedResourceGrant{}
	}
	c.JSON(http.StatusOK, sharedResourceWithGrants{SharedResource: *sr, Grants: grants})
}

func (h *sharedResourceHandler) update(c *gin.Context) {
	claims := ginctx.Claims(c)
	sr, _, ok := h.loadResourceWithAuth(c, c.Param("rid"), claims)
	if !ok {
		return
	}
	var req struct {
		DisplayName *string `json:"display_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if req.DisplayName != nil {
		dn := strings.TrimSpace(*req.DisplayName)
		if err := h.cfg.Resources.UpdateMeta(c.Request.Context(), sr.ID, dn); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		// Propagate the rename to Stalwart (collection / From name). The
		// reconciler gates apply on first-projection, so push it here.
		if sr.EmailCached != nil && *sr.EmailCached != "" {
			h.notifyAgent(c.Request.Context(), "sharedresource.apply", map[string]any{
				"email": *sr.EmailCached, "display_name": dn, "kind": sr.Kind,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *sharedResourceHandler) setGrants(c *gin.Context) {
	claims := ginctx.Claims(c)
	sr, _, ok := h.loadResourceWithAuth(c, c.Param("rid"), claims)
	if !ok {
		return
	}
	var req struct {
		Grants []grantInput `json:"grants"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	grants := make([]models.SharedResourceGrant, 0, len(req.Grants))
	for _, g := range req.Grants {
		if g.GranteeKind != "mailbox" && g.GranteeKind != "group" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grantee_kind"})
			return
		}
		if g.GranteeID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing_grantee_id"})
			return
		}
		if !sharedResourceRights[g.Rights] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_rights"})
			return
		}
		grants = append(grants, models.SharedResourceGrant{
			ResourceID:  sr.ID,
			GranteeKind: g.GranteeKind,
			GranteeID:   g.GranteeID,
			Rights:      g.Rights,
		})
	}
	if err := h.cfg.Resources.ReplaceGrants(c.Request.Context(), sr.ID, grants); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Convergence to Stalwart shareWith happens on the next reconcile pass.
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *sharedResourceHandler) delete(c *gin.Context) {
	claims := ginctx.Claims(c)
	sr, _, ok := h.loadResourceWithAuth(c, c.Param("rid"), claims)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if sr.EmailCached != nil && *sr.EmailCached != "" {
		// Tombstone first (durable teardown), then best-effort instant destroy.
		// The reconciler GC retries destroy until it succeeds, then clears it.
		_ = h.cfg.Resources.AddTombstone(ctx, *sr.EmailCached)
		h.notifyAgent(ctx, "sharedresource.destroy", map[string]any{"email": *sr.EmailCached})
	}
	if err := h.cfg.Resources.Delete(ctx, sr.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- helpers ----

func (h *sharedResourceHandler) notifyAgent(ctx context.Context, command string, params any) {
	if h.cfg.Agent == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, sharedResourceAgentTimeout)
	defer cancel()
	_, _ = h.cfg.Agent.Call(agentCtx, command, params)
}

func (h *sharedResourceHandler) loadDomainWithAuth(c *gin.Context, domainID string, claims *auth.AccessClaims) (*models.Domain, bool) {
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	return dom, true
}

func (h *sharedResourceHandler) loadResourceWithAuth(c *gin.Context, id string, claims *auth.AccessClaims) (*models.SharedResource, *models.Domain, bool) {
	ctx := c.Request.Context()
	sr, err := h.cfg.Resources.FindByID(ctx, id)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "resource_not_found"})
			return nil, nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, false
	}
	dom, err := h.cfg.Domains.FindByID(ctx, sr.DomainID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, nil, false
	}
	return sr, dom, true
}
