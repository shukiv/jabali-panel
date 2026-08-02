// Per-domain IP allow/deny lists (M36).
//
// Routes (mounted under /api/v1/domains/:id/acls):
//
//	GET            list rules for one domain
//	POST           create a rule
//	DELETE /:acl_id delete one rule
//
// Authorization: admins read+write any; users only their own domains.
// Cross-tenant access returns 404 (not 403) to avoid leaking domain
// existence.
package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainIPACLHandlerConfig wires the routes. Domains repo + ACLs repo
// are required; nil disables route registration.
type DomainIPACLHandlerConfig struct {
	Domains repository.DomainRepository
	ACLs    repository.DomainIPACLRepository
	// Reconciler hook so a CRUD mutation kicks an immediate domain
	// converge instead of waiting for the next 60s tick.
	Reconcile func(domainID string)
}

func RegisterDomainIPACLRoutes(g *gin.RouterGroup, cfg DomainIPACLHandlerConfig) {
	if cfg.Domains == nil || cfg.ACLs == nil {
		return
	}
	h := &domainIPACLHandler{cfg: cfg}
	rg := g.Group("/domains/:id/acls")
	rg.GET("", h.list)
	rg.POST("", h.create)
	rg.DELETE("/:acl_id", h.delete)
}

type domainIPACLHandler struct{ cfg DomainIPACLHandlerConfig }

type createACLRequest struct {
	CIDR     string `json:"cidr" binding:"required"`
	Action   string `json:"action" binding:"required"` // allow|deny
	Priority int    `json:"priority"`
	Comment  string `json:"comment"`
}

func (h *domainIPACLHandler) list(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rows, err := h.cfg.ACLs.ListByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if rows == nil {
		rows = []models.DomainIPACL{}
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}

func (h *domainIPACLHandler) create(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	var req createACLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	if err := validateACLInput(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row := &models.DomainIPACL{
		ID:       ids.NewULID(),
		DomainID: dom.ID,
		CIDR:     req.CIDR,
		Action:   req.Action,
		Priority: req.Priority,
		Comment:  strings.TrimSpace(req.Comment),
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.cfg.ACLs.Create(ctx, row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	c.JSON(http.StatusCreated, row)
}

func (h *domainIPACLHandler) delete(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	aclID := c.Param("acl_id")
	row, err := h.cfg.ACLs.FindByID(c.Request.Context(), aclID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "acl_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if row.DomainID != dom.ID {
		// Cross-domain ACL — same 404 as cross-tenant domain access.
		c.JSON(http.StatusNotFound, gin.H{"error": "acl_not_found"})
		return
	}
	if err := h.cfg.ACLs.Delete(c.Request.Context(), aclID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	c.JSON(http.StatusOK, gin.H{"id": aclID, "deleted": true})
}

// resolveDomain looks up the domain by URL :id, enforces ownership,
// and writes the right HTTP error if it bails. Returns (nil, false)
// when the caller should stop.
func (h *domainIPACLHandler) resolveDomain(c *gin.Context) (*models.Domain, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil, false
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return nil, false
	}
	return dom, true
}

// validateACLInput rejects bad CIDR strings + unknown action values
// at the API boundary so the reconciler never sees garbage input.
func validateACLInput(req *createACLRequest) error {
	switch req.Action {
	case "allow", "deny":
	default:
		return errBadField("action must be 'allow' or 'deny'")
	}
	cidr := strings.TrimSpace(req.CIDR)
	if cidr == "" {
		return errBadField("cidr required")
	}
	// Accept either bare IP or IP/CIDR.
	if !strings.Contains(cidr, "/") {
		// Treat as /32 or /128 depending on family.
		ip := net.ParseIP(cidr)
		if ip == nil {
			return errBadField("invalid IP address")
		}
		if ip.To4() != nil {
			cidr += "/32"
		} else {
			cidr += "/128"
		}
		req.CIDR = cidr
		return nil
	}
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return errBadField("invalid CIDR: " + err.Error())
	}
	req.CIDR = cidr
	return nil
}
