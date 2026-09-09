// Web domain aliases (GH #1625).
//
// Routes (mounted under /api/v1/domains/:id/aliases):
//
//	GET               list aliases for one domain
//	POST              add an alias hostname
//	DELETE /:alias_id remove one alias
//
// An alias is served from the SAME vhost + docroot as the owning web
// domain: the reconciler adds every alias to the main server block's
// server_name, and — only when the alias resolves DIRECTLY to this
// server — to the domain's TLS cert as a SAN (a CDN-fronted alias is
// left off the cert on purpose; see reconciler.sanHostnamesForDomain).
//
// Authorization mirrors the ACL routes: admins read+write any; users
// only their own domains. Cross-tenant access returns 404 (not 403) to
// avoid leaking domain existence.
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainAliasHandlerConfig wires the routes. Domains + Aliases repos are
// required; nil disables route registration. Certs + Settings are
// optional enrichment (per-alias cert coverage and the panel-FQDN
// denylist) — nil degrades safely.
type DomainAliasHandlerConfig struct {
	Domains  repository.DomainRepository
	Aliases  repository.WebDomainAliasRepository
	Certs    repository.SSLCertificateRepository
	Settings repository.ServerSettingsRepository
	// Reconciler hook so a CRUD mutation kicks an immediate domain
	// converge (re-render server_name + reissue the cert) instead of
	// waiting for the next tick.
	Reconcile func(domainID string)
}

func RegisterDomainAliasRoutes(g *gin.RouterGroup, cfg DomainAliasHandlerConfig) {
	if cfg.Domains == nil || cfg.Aliases == nil {
		return
	}
	h := &domainAliasHandler{cfg: cfg}
	rg := g.Group("/domains/:id/aliases")
	rg.GET("", h.list)
	rg.POST("", h.create)
	rg.DELETE("/:alias_id", h.delete)
}

type domainAliasHandler struct{ cfg DomainAliasHandlerConfig }

type createAliasRequest struct {
	Hostname string `json:"hostname" binding:"required"`
}

// aliasRow is the wire shape: the stored alias plus its live certificate
// coverage so the UI can show "cert pending — point DNS here" without a
// second call. cert_status is "active" when the issued leaf cert already
// names the alias, else "pending" (with pending_reason).
type aliasRow struct {
	models.WebDomainAlias
	CertStatus    string `json:"cert_status"`
	PendingReason string `json:"pending_reason,omitempty"`
}

const (
	aliasCertActive  = "active"
	aliasCertPending = "pending"
)

func (h *domainAliasHandler) list(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	rows, err := h.cfg.Aliases.ListByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]aliasRow, 0, len(rows))
	covered := h.coveredSANs(c.Request.Context(), dom.ID)
	reason := h.pendingReason(c.Request.Context())
	for _, r := range rows {
		row := aliasRow{WebDomainAlias: r, CertStatus: aliasCertPending, PendingReason: reason}
		if _, ok := covered[strings.ToLower(r.Hostname)]; ok {
			row.CertStatus = aliasCertActive
			row.PendingReason = ""
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
}

func (h *domainAliasHandler) create(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	var req createAliasRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	hostname, status, code, detail := h.validateAliasHostname(ctx, dom, req.Hostname)
	if status != 0 {
		c.JSON(status, gin.H{"error": code, "detail": detail})
		return
	}
	row := &models.WebDomainAlias{
		ID:       ids.NewULID(),
		DomainID: dom.ID,
		Hostname: hostname,
	}
	if err := h.cfg.Aliases.Create(ctx, row); err != nil {
		// The UNIQUE index is the backstop for a race between the
		// pre-check and the insert; surface it as the same 409.
		if isDuplicateKeyErr(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "alias_exists", "detail": "that hostname is already an alias"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	c.JSON(http.StatusCreated, aliasRow{
		WebDomainAlias: *row,
		CertStatus:     aliasCertPending,
		PendingReason:  h.pendingReason(ctx),
	})
}

func (h *domainAliasHandler) delete(c *gin.Context) {
	dom, ok := h.resolveDomain(c)
	if !ok {
		return
	}
	aliasID := c.Param("alias_id")
	row, err := h.cfg.Aliases.FindByID(c.Request.Context(), aliasID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "alias_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if row.DomainID != dom.ID {
		// Cross-domain alias — same 404 as cross-tenant domain access.
		c.JSON(http.StatusNotFound, gin.H{"error": "alias_not_found"})
		return
	}
	if err := h.cfg.Aliases.Delete(c.Request.Context(), aliasID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete: " + err.Error()})
		return
	}
	if h.cfg.Reconcile != nil {
		h.cfg.Reconcile(dom.ID)
	}
	c.JSON(http.StatusOK, gin.H{"id": aliasID, "deleted": true})
}

// resolveDomain looks up the domain by URL :id, enforces ownership, and
// writes the right HTTP error if it bails. Returns (nil, false) when the
// caller should stop.
func (h *domainAliasHandler) resolveDomain(c *gin.Context) (*models.Domain, bool) {
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

// aliasHelperPrefixes are the auto-derived helper subdomains the panel
// serves for EVERY domain (its own vhost + mail/ACME server_name). An
// alias equal to "<prefix><some other domain>" would add a second nginx
// server block with the same server_name as that domain's helper vhost;
// nginx keeps the first and silently drops the rest, so the alias could
// shadow another tenant's mail or web vhost. Rejected at create. Kept in
// lockstep with the agent's webmail_vhost server_name list and
// reconciler.sanHostnamesForDomain (GH #1625).
var aliasHelperPrefixes = []string{"www.", "mail.", "autoconfig.", "autodiscover.", "mta-sts."}

// aliasCollision is the REVERSE of validateAliasHostname's helper check: it
// reports whether creating (or renaming to) a domain named `name` would claim
// an nginx server_name already held by another domain's web-domain alias.
// validateAliasHostname stops an alias from colliding with an existing domain;
// without this, a domain created AFTER an alias with the same name reopens the
// exact cross-tenant vhost-hijack hole (a duplicate server_name across two
// server blocks silently lets the first-loaded win — nginx -t only warns).
// A domain named N claims N, www.N, and its four mail-helper server_names, so
// all six are checked against the alias table regardless of the domain's
// EmailEnabled: a domain can enable mail later and its helper vhost would then
// collide. Returns the colliding hostname and true on a hit. A nil repo means
// the feature is unwired → no collision (fail-open ONLY when unwired, never on
// a live lookup error — FindByHostname's ErrNotFound is the only "no hit").
func aliasCollision(ctx context.Context, aliases repository.WebDomainAliasRepository, name string) (string, bool) {
	if aliases == nil {
		return "", false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", false
	}
	candidates := make([]string, 0, len(aliasHelperPrefixes)+1)
	candidates = append(candidates, name)
	for _, p := range aliasHelperPrefixes {
		candidates = append(candidates, p+name)
	}
	for _, cand := range candidates {
		if existing, err := aliases.FindByHostname(ctx, cand); err == nil && existing != nil {
			return cand, true
		}
	}
	return "", false
}

// validateAliasHostname normalizes + validates an alias hostname and
// enforces the collision rules. Returns (normalized, 0, "", "") on
// success, or (_, status, code, detail) to reject. Cheap syntactic
// checks run before any DB lookup.
func (h *domainAliasHandler) validateAliasHostname(ctx context.Context, dom *models.Domain, raw string) (string, int, string, string) {
	host := normalizeDomainName(raw)
	if err := validateDomainName(host); err != nil {
		return "", http.StatusBadRequest, "invalid_hostname", err.Error()
	}
	if host == dom.Name {
		return "", http.StatusBadRequest, "alias_is_primary", "that is already the domain's primary name"
	}
	if host == "www."+dom.Name {
		return "", http.StatusBadRequest, "alias_is_www", "enable www via the domain's www option, not as an alias"
	}
	// A web-disabled domain (DNS-only zone / mail-only domain) has no vhost, so
	// the reconciler never renders the alias into a server_name — the row would
	// be inert yet still squat a globally-unique hostname (the cheapest domain
	// type to create). Reject so an alias can only attach to a domain that
	// actually serves it (GH #1625).
	if dom.WebDisabled {
		return "", http.StatusBadRequest, "domain_has_no_web", "aliases require a web-hosted domain; this domain has web hosting disabled"
	}
	// Panel FQDN — an alias here would shadow the panel's own vhost.
	if h.cfg.Settings != nil {
		if s, err := h.cfg.Settings.Get(ctx); err == nil && s != nil {
			panelHost := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s.Hostname), "."))
			if panelHost != "" && host == panelHost {
				return "", http.StatusConflict, "alias_reserved_panel", "that hostname is reserved by the panel"
			}
		}
	}
	// Bare apex of any domain on the server (own primary already caught).
	if existing, err := h.cfg.Domains.FindByName(ctx, host); err == nil && existing != nil {
		return "", http.StatusConflict, "alias_taken_by_domain", "that hostname is already a domain on this server"
	}
	// Helper server_name of any domain — the cross-tenant hijack vector.
	for _, p := range aliasHelperPrefixes {
		if !strings.HasPrefix(host, p) {
			continue
		}
		base := strings.TrimPrefix(host, p)
		if base == "" {
			continue
		}
		if existing, err := h.cfg.Domains.FindByName(ctx, base); err == nil && existing != nil {
			return "", http.StatusConflict, "alias_conflicts_helper", "that hostname is a reserved mail/web name for the domain " + base
		}
	}
	// Global alias uniqueness (the DB UNIQUE index is the backstop).
	if existing, err := h.cfg.Aliases.FindByHostname(ctx, host); err == nil && existing != nil {
		return "", http.StatusConflict, "alias_exists", "that hostname is already an alias"
	}
	return host, 0, "", ""
}

// coveredSANs returns the lowercased SAN set the domain's ISSUED leaf
// cert actually names, so the list handler can mark each alias
// active/pending truthfully (no DNS lookup). Empty when there is no cert
// repo, no cert row, or the leaf can't be read — every alias then reads
// as pending, which is the safe under-report.
func (h *domainAliasHandler) coveredSANs(ctx context.Context, domainID string) map[string]struct{} {
	if h.cfg.Certs == nil {
		return nil
	}
	cert, err := h.cfg.Certs.FindByDomainID(ctx, domainID)
	if err != nil || cert == nil {
		return nil
	}
	actual := actualWebCertSANs(cert.Status, cert.CertPath)
	if len(actual) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(actual))
	for _, s := range actual {
		set[strings.ToLower(s)] = struct{}{}
	}
	return set
}

// pendingReason is the tenant-facing explanation of the deliberate
// CDN-fronted-alias gap (GH #1625): only aliases that resolve directly
// to this server are added to the cert.
func (h *domainAliasHandler) pendingReason(ctx context.Context) string {
	ip := ""
	if h.cfg.Settings != nil {
		if s, err := h.cfg.Settings.Get(ctx); err == nil && s != nil {
			ip = strings.TrimSpace(s.PublicIPv4)
		}
	}
	if ip != "" {
		return "point an A/AAAA record for this hostname directly at " + ip +
			" to include it on the certificate; CDN-fronted aliases are not added automatically"
	}
	return "point an A/AAAA record for this hostname directly at this server " +
		"to include it on the certificate; CDN-fronted aliases are not added automatically"
}
