// Per-directory password protection (cPanel "Directory Privacy", M50).
//
// Routes (mounted under /api/v1/domains/:id/directory-privacy):
//
//	GET                         list rules for one domain
//	POST                        create a rule
//	PUT    /:rule_id             update a rule (realm only)
//	DELETE /:rule_id             delete a rule
//	GET    /:rule_id/credentials list credentials under a rule
//	POST   /:rule_id/credentials add a credential (plaintext in, hashed at write)
//	DELETE /:rule_id/credentials/:cred_id  delete a credential
//
// Authorization: admins read+write any; users only their own domains.
// Cross-tenant access returns 404 (not 403) to avoid leaking domain
// existence. Mirrors M36 IP ACL handler shape so the two surfaces stay
// in lockstep.
//
// The rule/credential mutation lifecycle (validation, bcrypt-at-write,
// cross-rule containment, converge-schedule) lives in internal/dirprivops so
// this handler and the CLI (cmd/server/domain_directory_privacy_cmd.go) share
// one implementation. This file owns HTTP concerns only: domain authz, request
// binding, read endpoints, and mapping the ops' typed errors onto the wire.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dirprivops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainDirectoryPrivacyHandlerConfig wires the routes. Domains repo +
// privacy repo are required; nil disables route registration.
type DomainDirectoryPrivacyHandlerConfig struct {
	Domains repository.DomainRepository
	Privacy repository.DomainDirectoryPrivacyRepository
	// Reconciler hook so a CRUD mutation kicks an immediate domain
	// converge instead of waiting for the next 60s tick.
	Reconcile func(domainID string)
	// BcryptCost overrides bcrypt.DefaultCost; tests pass a low cost.
	BcryptCost int
}

func RegisterDomainDirectoryPrivacyRoutes(g *gin.RouterGroup, cfg DomainDirectoryPrivacyHandlerConfig) {
	if cfg.Domains == nil || cfg.Privacy == nil {
		return
	}
	h := &domainDirectoryPrivacyHandler{cfg: cfg}
	rg := g.Group("/domains/:id/directory-privacy")
	rg.GET("", h.listRules)
	rg.POST("", h.createRule)
	rg.PUT("/:rule_id", h.updateRule)
	rg.DELETE("/:rule_id", h.deleteRule)
	rg.GET("/:rule_id/credentials", h.listCredentials)
	rg.POST("/:rule_id/credentials", h.createCredential)
	rg.DELETE("/:rule_id/credentials/:cred_id", h.deleteCredential)
}

type domainDirectoryPrivacyHandler struct {
	cfg DomainDirectoryPrivacyHandlerConfig
}

// deps builds the shared-module dependencies from the handler config. The
// Reconcile hook is the immediate-converge Schedule; BcryptCost 0 → default.
func (h *domainDirectoryPrivacyHandler) deps() dirprivops.Deps {
	return dirprivops.Deps{
		Privacy:    h.cfg.Privacy,
		Schedule:   h.cfg.Reconcile,
		BcryptCost: h.cfg.BcryptCost,
	}
}

// abortPrivacyErr maps an ops error onto the wire codes this endpoint has
// always returned: a validation failure → 400 with the bare message, the
// containment/not-found sentinels → 404, anything else (a repository failure)
// → 500 with the handler's action prefix.
func abortPrivacyErr(c *gin.Context, err error, repoPrefix string) {
	var ve *dirprivops.ValidationError
	switch {
	case errors.As(err, &ve):
		c.JSON(http.StatusBadRequest, gin.H{"error": ve.Msg})
	case errors.Is(err, dirprivops.ErrRuleNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "rule_not_found"})
	case errors.Is(err, dirprivops.ErrCredentialNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "credential_not_found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": repoPrefix + err.Error()})
	}
}

type createRuleRequest struct {
	Path  string `json:"path" binding:"required"`
	Realm string `json:"realm"`
}

type updateRuleRequest struct {
	Realm string `json:"realm" binding:"required"`
}

type createCredentialRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// --- rules ---

func (h *domainDirectoryPrivacyHandler) listRules(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rules, err := h.cfg.Privacy.ListRulesByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if rules == nil {
		rules = []models.DomainDirectoryPrivacyRule{}
	}
	c.JSON(http.StatusOK, gin.H{"data": rules, "total": len(rules)})
}

func (h *domainDirectoryPrivacyHandler) createRule(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	var req createRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	row, err := dirprivops.CreateRule(ctx, h.deps(), dom.ID, req.Path, req.Realm)
	if err != nil {
		abortPrivacyErr(c, err, "create: ")
		return
	}
	c.JSON(http.StatusCreated, row)
}

func (h *domainDirectoryPrivacyHandler) updateRule(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	var req updateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	row, err := dirprivops.UpdateRule(c.Request.Context(), h.deps(), dom.ID, c.Param("rule_id"), req.Realm)
	if err != nil {
		abortPrivacyErr(c, err, "update: ")
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *domainDirectoryPrivacyHandler) deleteRule(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	ruleID := c.Param("rule_id")
	if err := dirprivops.DeleteRule(c.Request.Context(), h.deps(), dom.ID, ruleID); err != nil {
		abortPrivacyErr(c, err, "delete: ")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": ruleID, "deleted": true})
}

// --- credentials ---

func (h *domainDirectoryPrivacyHandler) listCredentials(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rule, ok := h.resolveRule(c, dom.ID)
	if !ok {
		return
	}
	rows, err := h.cfg.Privacy.ListCredentialsByRule(c.Request.Context(), rule.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if rows == nil {
		rows = []models.DomainDirectoryPrivacyCredential{}
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}

func (h *domainDirectoryPrivacyHandler) createCredential(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	var req createCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	row, err := dirprivops.CreateCredential(ctx, h.deps(), dom.ID, c.Param("rule_id"), req.Username, req.Password)
	if err != nil {
		abortPrivacyErr(c, err, "create: ")
		return
	}
	c.JSON(http.StatusCreated, row)
}

func (h *domainDirectoryPrivacyHandler) deleteCredential(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	credID := c.Param("cred_id")
	if err := dirprivops.DeleteCredential(c.Request.Context(), h.deps(), dom.ID, c.Param("rule_id"), credID); err != nil {
		abortPrivacyErr(c, err, "delete: ")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": credID, "deleted": true})
}

// --- resolve helpers ---

func (h *domainDirectoryPrivacyHandler) resolveDomain(c *gin.Context) (*models.Domain, bool) {
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

// resolveRule loads a rule for the read endpoints (listCredentials) and
// confirms it belongs to the domain. The mutation paths resolve inside
// internal/dirprivops instead.
func (h *domainDirectoryPrivacyHandler) resolveRule(c *gin.Context, domainID string) (*models.DomainDirectoryPrivacyRule, bool) {
	ruleID := c.Param("rule_id")
	rule, err := h.cfg.Privacy.FindRuleByID(c.Request.Context(), ruleID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "rule_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if rule.DomainID != domainID {
		c.JSON(http.StatusNotFound, gin.H{"error": "rule_not_found"})
		return nil, false
	}
	return rule, true
}
