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
package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/auth"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/ids"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/repository"
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
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
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
	path, err := validatePrivacyPath(req.Path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	realm, err := validatePrivacyRealm(req.Realm)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	row := &models.DomainDirectoryPrivacyRule{
		ID:       ids.NewULID(),
		DomainID: dom.ID,
		Path:     path,
		Realm:    realm,
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.cfg.Privacy.CreateRule(ctx, row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	c.JSON(http.StatusCreated, row)
}

func (h *domainDirectoryPrivacyHandler) updateRule(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rule, ok := h.resolveRule(c, dom.ID)
	if !ok {
		return
	}
	var req updateRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	realm, err := validatePrivacyRealm(req.Realm)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.cfg.Privacy.UpdateRule(c.Request.Context(), rule.ID, realm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	rule.Realm = realm
	c.JSON(http.StatusOK, rule)
}

func (h *domainDirectoryPrivacyHandler) deleteRule(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rule, ok := h.resolveRule(c, dom.ID)
	if !ok {
		return
	}
	if err := h.cfg.Privacy.DeleteRule(c.Request.Context(), rule.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	c.JSON(http.StatusOK, gin.H{"id": rule.ID, "deleted": true})
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
	rule, ok := h.resolveRule(c, dom.ID)
	if !ok {
		return
	}
	var req createCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	if err := validatePrivacyUsername(req.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validatePrivacyPassword(req.Password); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := auth.HashPassword(req.Password, h.cfg.BcryptCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash: " + err.Error()})
		return
	}
	row := &models.DomainDirectoryPrivacyCredential{
		ID:           ids.NewULID(),
		RuleID:       rule.ID,
		Username:     req.Username,
		PasswordHash: hash,
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.cfg.Privacy.CreateCredential(ctx, row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	c.JSON(http.StatusCreated, row)
}

func (h *domainDirectoryPrivacyHandler) deleteCredential(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rule, ok := h.resolveRule(c, dom.ID)
	if !ok {
		return
	}
	credID := c.Param("cred_id")
	cred, err := h.cfg.Privacy.FindCredentialByID(c.Request.Context(), credID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "credential_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if cred.RuleID != rule.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential_not_found"})
		return
	}
	if err := h.cfg.Privacy.DeleteCredential(c.Request.Context(), credID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
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

// --- validators ---

var privacyPathRE = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

func validatePrivacyPath(in string) (string, error) {
	p := strings.TrimSpace(in)
	if p == "" {
		return "", errBadField("path required")
	}
	if len(p) > 255 {
		return "", errBadField("path too long (max 255)")
	}
	if p == "/" {
		return "/", nil
	}
	if !privacyPathRE.MatchString(p) {
		return "", errBadField("path must start with / and contain only [A-Za-z0-9_./-]")
	}
	if strings.Contains(p, "//") {
		return "", errBadField("path must not contain //")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", errBadField("path must not contain ..")
		}
	}
	// Strip trailing slash for consistent storage; agent re-adds when
	// emitting `location ^~ <path>/`.
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
		if p == "" {
			p = "/"
		}
	}
	return p, nil
}

func validatePrivacyRealm(in string) (string, error) {
	r := strings.TrimSpace(in)
	if r == "" {
		r = "Restricted"
	}
	if len(r) > 255 {
		return "", errBadField("realm too long (max 255)")
	}
	for _, c := range r {
		if c < 0x20 || c > 0x7e {
			return "", errBadField("realm must be printable ASCII")
		}
		if c == '"' || c == '\\' {
			return "", errBadField(`realm must not contain " or \`)
		}
	}
	return r, nil
}

var privacyUsernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validatePrivacyUsername(in string) error {
	if !privacyUsernameRE.MatchString(in) {
		return errBadField("username must match [A-Za-z0-9._-] (1-64 chars)")
	}
	return nil
}

func validatePrivacyPassword(in string) error {
	if len(in) < 8 {
		return errBadField("password must be at least 8 characters")
	}
	if len(in) > 128 {
		return errBadField("password too long (max 128)")
	}
	return nil
}
