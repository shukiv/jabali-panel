package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type DomainHandlerConfig struct {
	Domains    repository.DomainRepository
	Users      repository.UserRepository
	SSLCerts   repository.SSLCertificateRepository
	Packages   repository.PackageRepository
	Agent      agent.AgentInterface
	Reconciler *reconciler.Reconciler
	// DNSZones + DNSRecords feed the auto-enable-email path on create.
	// Both optional — when unset, create proceeds without flipping email
	// on (matches the pre-auto-enable behaviour). The explicit
	// /domains/:id/email endpoint is wired via DomainEmailHandlerConfig
	// separately and is the retry path for domains that skip auto-enable
	// or hit an error during create.
	DNSZones   repository.DNSZoneRepository
	DNSRecords repository.DNSRecordRepository
	// BWDaily backs the GET /domains/:id/bandwidth endpoint (M13.1).
	// Optional — nil makes the endpoint return 503 instead of 404 so
	// the panel UI knows the feature isn't wired vs. the domain
	// genuinely having no traffic.
	BWDaily repository.BWDailyRepository
	// ManagedIPs is the M24 IP-pool repo. Optional: when nil, the
	// listen_ipv*_id PATCH path is rejected with 503 (the pool is the
	// source of truth) and GET responses skip the denormalized
	// listen_ipv4 / listen_ipv6 nested objects.
	ManagedIPs repository.ManagedIPRepository
	// ServerSettings gates the tenant-safe nginx options opt-in (GH #307).
	ServerSettings repository.ServerSettingsRepository
}

const (
	defaultDomainsPageSize = 20
	maxDomainsPageSize     = 200
)

// Security validation patterns
var (
	// Domain name validation regex - RFC 1035 compliant
	domainNameRe = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)
	// HTML tag detection for XSS prevention
	htmlTagRe = regexp.MustCompile(`<[^>]*>`)
)

func RegisterDomainRoutes(g *gin.RouterGroup, cfg DomainHandlerConfig) {
	h := &domainHandler{cfg: cfg}
	domains := g.Group("/domains")
	domains.GET("", h.list)
	domains.POST("", h.create)
	domains.GET("/:id", h.get)
	domains.PATCH("/:id", h.update)
	domains.DELETE("/:id", h.delete)
	domains.GET("/:id/bandwidth", h.bandwidth)
}

type domainHandler struct{ cfg DomainHandlerConfig }

type createDomainRequest struct {
	Name    string `json:"name" binding:"required"`
	UserID  string `json:"user_id"`
	DocRoot string `json:"doc_root"`
	// GH#181: mail provider chosen at domain-add. Empty -> "jabali".
	// Drives EmailEnabled + SkipAutoSAN via models.DeriveMailFlags.
	MailProvider    string `json:"mail_provider"`
	M365Onmicrosoft string `json:"m365_onmicrosoft"`
	GoogleDKIM      string `json:"google_dkim"`
	// CreateWWW (GH #225) opts the domain into the bootstrap www CNAME.
	// Omitted/false => no www record (the new default).
	CreateWWW bool `json:"create_www"`
	// SSLMode (GH #246) — le|self|none at create. 'custom' is rejected here:
	// it needs a cert upload, set via PUT /domains/:id/ssl/custom after create.
	// Empty defaults to 'le'.
	SSLMode string `json:"ssl_mode"`
}

// validateDomainName validates domain name for security and RFC compliance
func validateDomainName(s string) error {
	// Check for empty or whitespace
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("domain name cannot be empty")
	}

	// Check for whitespace (potential injection)
	if strings.ContainsAny(s, " \t\n\r") {
		return fmt.Errorf("domain name contains invalid whitespace characters")
	}

	// Check length limits per RFC 1035
	if len(s) > 253 {
		return fmt.Errorf("domain name exceeds 253 character limit")
	}

	// Check for HTML tags (XSS prevention)
	if htmlTagRe.MatchString(s) {
		return fmt.Errorf("domain name contains invalid HTML characters")
	}

	// Check for path traversal attempts
	if strings.Contains(s, "..") || strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return fmt.Errorf("domain name contains invalid path characters")
	}

	// RFC 1035 compliance check
	if !domainNameRe.MatchString(s) {
		return fmt.Errorf("domain name is not a valid FQDN (requires at least two labels and 2+ letter TLD)")
	}

	return nil
}

// validateDocumentRoot validates document root path to prevent path traversal
func validateDocumentRoot(docRoot, username, domainName string) error {
	if docRoot == "" {
		return nil // Will use default
	}

	// Must be under user's home directory
	expectedPrefix := "/home/" + username + "/"
	if !strings.HasPrefix(docRoot, expectedPrefix) {
		return fmt.Errorf("document root must be under user's home directory")
	}

	// Check for path traversal attempts
	if strings.Contains(docRoot, "..") {
		return fmt.Errorf("document root contains invalid path traversal sequences")
	}

	return nil
}

// validateTenantDocumentRoot is the stricter confinement for a NON-admin owner
// changing their own domain's docroot (GH #526): the path must stay inside the
// domain's OWN tree /home/<user>/domains/<domain>/ (so an app can point at a sub-
// or parent dir like .../public), never another domain or elsewhere in the home.
// Admins keep the looser validateDocumentRoot (anywhere under the owner's home).
func validateTenantDocumentRoot(docRoot, username, domainName string) error {
	if docRoot == "" {
		return nil // resets to the canonical default
	}
	if strings.Contains(docRoot, "..") {
		return fmt.Errorf("document root contains invalid path traversal sequences")
	}
	clean := filepath.Clean(docRoot)
	base := "/home/" + username + "/domains/" + domainName
	if clean != base && !strings.HasPrefix(clean, base+"/") {
		return fmt.Errorf("document root must be inside /home/%s/domains/%s/", username, domainName)
	}
	return nil
}

type updateDomainRequest struct {
	IsEnabled *bool `json:"is_enabled,omitempty"`
	// DocRoot (GH #265) — admin-only change of the document root. Empty
	// resets to the default /home/<user>/domains/<name>/public_html. The
	// reconciler's per-tick domain.create mkdir -p's the new path + re-renders
	// the vhost, so changing it is safe (old files are not moved).
	DocRoot               *string                  `json:"doc_root,omitempty"`
	NginxCustomDirectives *string                  `json:"nginx_custom_directives,omitempty"`
	RedirectAllTo         *string                  `json:"redirect_all_to,omitempty"`
	RedirectAllType       *string                  `json:"redirect_all_type,omitempty"`
	PageRedirects         *models.PageRedirects    `json:"page_redirects,omitempty"`
	NginxRules            *models.NginxRules       `json:"nginx_rules,omitempty"`
	NginxSafeOptions      *models.NginxSafeOptions `json:"nginx_safe_options,omitempty"`
	IndexPriority         *string                  `json:"index_priority,omitempty"`
	WebmailEnabled        *bool                    `json:"webmail_enabled,omitempty"`
	// GH#181: mail provider + optional DKIM tokens. Pointers so an absent
	// field in the PATCH leaves the columns untouched. When MailProvider is
	// present, EmailEnabled + SkipAutoSAN are re-derived from it.
	MailProvider    *string `json:"mail_provider,omitempty"`
	M365Onmicrosoft *string `json:"m365_onmicrosoft,omitempty"`
	GoogleDKIM      *string `json:"google_dkim,omitempty"`
	// M24: per-domain IP binding. nullableUint64 distinguishes
	// "absent in PATCH" (don't touch the column) from "explicitly null"
	// (clear binding → fall back to server default for the family) from
	// "set to ID" (rebind). PATCH `{}`-only callers retain prior
	// behaviour exactly.
	ListenIPv4ID nullableUint64 `json:"listen_ipv4_id,omitempty"`
	ListenIPv6ID nullableUint64 `json:"listen_ipv6_id,omitempty"`
	// SSLMode (GH #246) — switch TLS mode. 'custom' is set only via
	// PUT /domains/:id/ssl/custom (needs the cert), not here.
	SSLMode *string `json:"ssl_mode,omitempty"`
}

// nullableUint64 is the M24 wrapper that lets a PATCH body distinguish
// "field absent" from "field explicitly null". Gin's binding only invokes
// UnmarshalJSON when the key is present in the body, so Set stays false
// for absent fields. JSON null unmarshals to Set=true, Value=nil. JSON
// number unmarshals to Set=true, Value=&n. Any other JSON shape returns
// an error so the handler can 422.
type nullableUint64 struct {
	Set   bool
	Value *uint64
}

func (n *nullableUint64) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var v uint64
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("listen_ipv*_id must be a positive integer or null: %w", err)
	}
	n.Value = &v
	return nil
}

// sslBadge is the nested SSL-cert summary embedded in domain list rows so
// the admin UI can differentiate self-signed from Let's Encrypt at a glance.
type sslBadge struct {
	Status    string     `json:"status"`
	Issuer    *string    `json:"issuer,omitempty"`
	IssuedAt  *time.Time `json:"issued_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// domainListRow wraps a Domain with the optional SSL badge. Embedded so
// existing consumers of the flat shape keep working.
type domainListRow struct {
	models.Domain
	SSL *sslBadge `json:"ssl,omitempty"`
	// Username is the owning hosting user's Linux account name, batched
	// onto each row so the admin Domains table can show a meaningful
	// owner column without a per-row lookup. nil when the owner can't be
	// resolved (deleted user, admin-only row).
	Username *string `json:"username,omitempty"`
	// Denormalized listen-IP summaries — UI shows the address string
	// without a second roundtrip per row. Always populated when
	// ManagedIPs is wired: explicit binding ⇒ that row's address; null
	// binding ⇒ family default address. nil only when the family default
	// itself is missing (fresh install before a v6 was added etc.).
	ListenIPv4 *ipSummary `json:"listen_ipv4,omitempty"`
	ListenIPv6 *ipSummary `json:"listen_ipv6,omitempty"`
	// Bytes30d is the prior-30-day bandwidth total in bytes harvested
	// by the M13.1 goaccess pipeline. Populated on every list response
	// when BWDaily is wired; omitted otherwise so older clients stay
	// happy and a fresh install (no data yet) renders 0 not "missing".
	Bytes30d *uint64 `json:"bytes_30d,omitempty"`
}

// ipSummary is the denormalized {id, address} blob the UI consumes for
// the per-domain listen IP. id may be 0 when this is a fall-through
// "use server default" case where no managed_ip row exists for the family.
type ipSummary struct {
	ID      uint64 `json:"id"`
	Address string `json:"address"`
}

func (h *domainHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	page, pageSize, opts := parseListOptions(c, defaultDomainsPageSize, maxDomainsPageSize)

	var domains []models.Domain
	var total int64
	var err error

	if claims.IsAdmin {
		// Admins can scope to a single owner via ?user_id (admin breadcrumbs /
		// cross-entity links, #483). Validated as a ULID; empty = all domains.
		if uid := c.Query("user_id"); uid != "" {
			if !ids.IsValidULID(uid) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_id"})
				return
			}
			domains, total, err = h.cfg.Domains.ListByUserID(c.Request.Context(), uid, opts)
		} else {
			domains, total, err = h.cfg.Domains.List(c.Request.Context(), opts)
		}
	} else {
		domains, total, err = h.cfg.Domains.ListByUserID(c.Request.Context(), claims.UserID, opts)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if domains == nil {
		domains = []models.Domain{}
	}

	// Enrich with SSL badge via a single batch lookup. Skipped cleanly if
	// SSLCerts isn't wired (e.g. early-boot tests) so the list still works.
	rows := make([]domainListRow, len(domains))
	for i := range domains {
		rows[i] = domainListRow{Domain: domains[i]}
	}
	if h.cfg.SSLCerts != nil && len(domains) > 0 {
		domainIDs := make([]string, len(domains))
		for i := range domains {
			domainIDs[i] = domains[i].ID
		}
		certs, certErr := h.cfg.SSLCerts.FindByDomainIDs(c.Request.Context(), domainIDs)
		if certErr == nil {
			certMap := make(map[string]*models.SSLCertificate, len(certs))
			for i := range certs {
				certMap[certs[i].DomainID] = &certs[i]
			}
			for i := range rows {
				rows[i].SSL = sslBadgeForDomain(&rows[i].Domain, certMap[rows[i].ID])
			}
		}
		// On SSL lookup error we drop the badge silently — list response
		// still ships flat domain data rather than 500ing.
	}

	// M13.1: denormalize prior-30-day bandwidth bytes onto each row so
	// the admin/user Domains table can render the column without an
	// N+1 fetch. Single batch lookup against bw_daily; on error we
	// drop the field rather than 500ing — older clients ignore it.
	if h.cfg.BWDaily != nil && len(domains) > 0 {
		now := time.Now().UTC()
		from := now.AddDate(0, 0, -29)
		domainIDs := make([]string, len(domains))
		for i := range domains {
			domainIDs[i] = domains[i].ID
		}
		if bw, bwErr := h.cfg.BWDaily.SumByDomainIDs(c.Request.Context(), domainIDs, from, now); bwErr == nil {
			for i := range rows {
				v := bw[rows[i].ID]
				rows[i].Bytes30d = &v
			}
		}
	}

	// Denormalize the owning user's Linux username onto each row so the
	// admin table can show a meaningful owner. Single batch lookup; on
	// error we drop the field rather than 500ing — the row's user_id is
	// still on the wire as a fallback.
	if h.cfg.Users != nil && len(domains) > 0 {
		userIDs := make([]string, 0, len(domains))
		seen := make(map[string]struct{}, len(domains))
		for i := range domains {
			if _, ok := seen[domains[i].UserID]; ok {
				continue
			}
			seen[domains[i].UserID] = struct{}{}
			userIDs = append(userIDs, domains[i].UserID)
		}
		users, userErr := h.cfg.Users.FindByIDs(c.Request.Context(), userIDs)
		if userErr == nil {
			usernameByID := make(map[string]*string, len(users))
			for i := range users {
				usernameByID[users[i].ID] = users[i].Username
			}
			for i := range rows {
				rows[i].Username = usernameByID[rows[i].UserID]
			}
		}
	}

	// M24: denormalize listen_ipv4 / listen_ipv6 onto each row. Pool is
	// capped at 100 (Step 2), so a single ListAll is cheaper than N
	// FindByID calls. Errors silently drop the field rather than 500;
	// the UI's per-IP page is the recovery path.
	if h.cfg.ManagedIPs != nil && len(domains) > 0 {
		ips, ipErr := h.cfg.ManagedIPs.ListAll(c.Request.Context())
		if ipErr == nil {
			ipByID := make(map[uint64]*models.ManagedIP, len(ips))
			var defaultV4, defaultV6 *models.ManagedIP
			for i := range ips {
				ipByID[ips[i].ID] = &ips[i]
				if ips[i].IsDefault {
					switch ips[i].Family {
					case "ipv4":
						defaultV4 = &ips[i]
					case "ipv6":
						defaultV6 = &ips[i]
					}
				}
			}
			for i := range rows {
				rows[i].ListenIPv4 = pickListenSummary(rows[i].ListenIPv4ID, ipByID, defaultV4)
				rows[i].ListenIPv6 = pickListenSummary(rows[i].ListenIPv6ID, ipByID, defaultV6)
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      rows,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// pickListenSummary resolves a domain row's listen_ipv*_id to the
// denormalized {id,address} blob using the prefetched batch maps.
// Falls back to the family default when the binding is null; returns
// nil only when no default is seeded for that family.
func pickListenSummary(id *uint64, byID map[uint64]*models.ManagedIP, def *models.ManagedIP) *ipSummary {
	if id != nil {
		if row, ok := byID[*id]; ok {
			return &ipSummary{ID: row.ID, Address: row.Address}
		}
	}
	if def != nil {
		return &ipSummary{ID: def.ID, Address: def.Address}
	}
	return nil
}

// enrichDomainResponse returns a domainListRow with the SSL badge and
// listen-IP denormalization filled in for a single Domain — used by GET
// /domains/:id and the PATCH response. The list path inlines its own
// loop that batches IP lookups.
func (h *domainHandler) enrichDomainResponse(ctx context.Context, d models.Domain) domainListRow {
	row := domainListRow{Domain: d}
	if h.cfg.SSLCerts != nil {
		certs, err := h.cfg.SSLCerts.FindByDomainIDs(ctx, []string{d.ID})
		if err == nil {
			var cert *models.SSLCertificate
			for i := range certs {
				if certs[i].DomainID == d.ID {
					cert = &certs[i]
					break
				}
			}
			row.SSL = sslBadgeForDomain(&d, cert)
		}
	}
	if h.cfg.ManagedIPs != nil {
		row.ListenIPv4 = h.resolveListenSummary(ctx, d.ListenIPv4ID, "ipv4")
		row.ListenIPv6 = h.resolveListenSummary(ctx, d.ListenIPv6ID, "ipv6")
	}
	return row
}

// resolveListenSummary fetches the {id,address} blob for a domain's
// listen_ipv*_id binding. Explicit binding ⇒ the bound row; nil binding
// ⇒ the family default. Returns nil only when neither resolves (e.g.
// the operator never seeded a v6 default).
func (h *domainHandler) resolveListenSummary(ctx context.Context, id *uint64, family string) *ipSummary {
	if id != nil {
		row, err := h.cfg.ManagedIPs.FindByID(ctx, *id)
		if err == nil {
			return &ipSummary{ID: row.ID, Address: row.Address}
		}
		// Per F-H-2: FK RESTRICT means this should be unreachable, but
		// if it does happen we fall through to default rather than emit
		// a null. The operator UI surfaces a separate "missing IP"
		// warning via the IP manager pages, not the per-domain GET.
	}
	row, err := h.cfg.ManagedIPs.FindDefaultByFamily(ctx, family)
	if err != nil {
		return nil
	}
	return &ipSummary{ID: row.ID, Address: row.Address}
}

// sslBadgeFromCert maps a cert row to the nested badge, filling Issuer
// based on status so the UI doesn't have to encode the label logic.
// sslBadgeForDomain derives the SSL badge from the domain's TLS mode AND its
// cert row (GH #246). The cert status alone is misleading: a None-mode domain
// keeps a revoked cert row, and a Self/None domain with no usable cert should
// not read as "pending". Mode wins.
func sslBadgeForDomain(d *models.Domain, cert *models.SSLCertificate) *sslBadge {
	if d.SSLMode == models.SSLModeNone {
		none := "None"
		return &sslBadge{Status: "none", Issuer: &none}
	}
	// No usable cert yet (no row, or revoked after a mode switch).
	if cert == nil || cert.Status == models.SSLStatusRevoked {
		if d.SSLMode == models.SSLModeSelf {
			iss := "Self-signed"
			return &sslBadge{Status: "provisioning", Issuer: &iss}
		}
		// LE / custom with no cert yet: leave the badge off (UI shows its
		// own issuing state) — unchanged behaviour.
		return nil
	}
	return sslBadgeFromCert(cert)
}

func sslBadgeFromCert(cert *models.SSLCertificate) *sslBadge {
	b := &sslBadge{
		Status:    cert.Status,
		IssuedAt:  cert.IssuedAt,
		ExpiresAt: cert.ExpiresAt,
	}
	switch cert.Status {
	case models.SSLStatusSelfSigned:
		s := "Self-signed"
		b.Issuer = &s
	case models.SSLStatusIssued, models.SSLStatusRenewing:
		s := "Let's Encrypt"
		b.Issuer = &s
	}
	return b
}

func (h *domainHandler) get(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	domain, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.JSON(http.StatusOK, h.enrichDomainResponse(c.Request.Context(), *domain))
}

func (h *domainHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var req createDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	// SECURITY: Validate domain name to prevent XSS and path traversal
	if err := validateDomainName(req.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_domain_name",
			"detail": err.Error(),
		})
		return
	}

	// Sanitize domain name by removing any potential HTML tags
	req.Name = htmlTagRe.ReplaceAllString(req.Name, "")

	targetUserID := req.UserID
	if !claims.IsAdmin {
		targetUserID = claims.UserID
	}
	if targetUserID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_id is required"})
		return
	}

	ctx := c.Request.Context()

	user, err := h.cfg.Users.FindByID(ctx, targetUserID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Admins are panel-only — they have no /home/<name>, so domains
	// can't be hosted under them. Bad request, not authz failure.
	if user.IsAdmin {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "admin_cannot_host",
			"detail": "admin users are panel-only — create a regular user to host domains",
		})
		return
	}

	// Suspended users can't acquire new domains — a fresh vhost would
	// be live + reachable while the panel/SFTP/login stays locked,
	// defeating the suspend cascade. Operator must unsuspend first.
	if user.Suspended {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "user_suspended",
			"detail": "user is suspended — unsuspend before adding domains",
		})
		return
	}

	// Username should always be set for non-admin users.
	if user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Quota check.
	if user.PackageID != nil && *user.PackageID != "" {
		count, err := h.cfg.Domains.CountByUserID(ctx, targetUserID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		pkg, err := h.cfg.Packages.FindByID(ctx, *user.PackageID)
		if err == nil && pkg.MaxDomains > 0 && count >= int64(pkg.MaxDomains) {
			c.JSON(http.StatusConflict, gin.H{"error": "domain_quota_exceeded"})
			return
		}
	}

	// SECURITY: Validate custom document root path
	if err := validateDocumentRoot(req.DocRoot, *user.Username, req.Name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_document_root",
			"detail": err.Error(),
		})
		return
	}

	docRoot := req.DocRoot
	if docRoot == "" {
		// Per-domain subtree under /home/<user>/domains/<name>/ so sibling
		// paths like logs/, ssl/, backups/ can live alongside public_html
		// without polluting the user's home.
		// SECURITY: Domain name is now validated, safe to use in path construction
		docRoot = "/home/" + *user.Username + "/domains/" + req.Name + "/public_html"
	}

	// GH#181 mail provider: default jabali; validate + normalise tokens;
	// EmailEnabled/SkipAutoSAN are DERIVED (never client-set directly).
	mailProvider := req.MailProvider
	if mailProvider == "" {
		mailProvider = models.MailProviderJabali
	}
	if !models.ValidMailProvider(mailProvider) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_mail_provider"})
		return
	}
	m365Tenant, err := dnscompile.NormaliseM365Onmicrosoft(req.M365Onmicrosoft)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_m365_onmicrosoft", "detail": err.Error()})
		return
	}
	googleDKIM, err := dnscompile.ValidateGoogleDKIM(req.GoogleDKIM)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_google_dkim", "detail": err.Error()})
		return
	}
	sslMode := req.SSLMode
	if sslMode == "" {
		sslMode = models.SSLModeLE
	}
	if !models.ValidSSLMode(sslMode) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_ssl_mode"})
		return
	}
	if sslMode == models.SSLModeCustom {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ssl_mode_custom_requires_upload", "detail": "create the domain with le/self/none, then upload a custom cert via the SSL settings"})
		return
	}

	mailEnabled, mailSkipSAN := models.DeriveMailFlags(mailProvider)
	// Email-enabled + no TLS is contradictory (MTA-STS / autoconfig need HTTPS).
	if sslMode == models.SSLModeNone && mailEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ssl_none_with_email", "detail": "a mail-enabled domain needs TLS; choose le/self or set mail provider to none"})
		return
	}

	now := time.Now().UTC()
	domain := &models.Domain{
		ID:         ids.NewULID(),
		UserID:     targetUserID,
		Name:       req.Name,
		DocRoot:    docRoot,
		IsEnabled:  true,
		SSLMode:    sslMode,
		SSLEnabled: models.SSLEnabledForMode(sslMode),
		// ADR-0080: email on by default for new domains. Set explicitly
		// rather than relying on the DB default so GORM emits
		// email_enabled=1 in the INSERT (a Go zero-value bool would be
		// elided, the DB default would still kick in, but explicit is
		// clearer and unit-test fixtures that bypass DB defaults stay
		// correct). Admin can opt-out per-domain via the existing
		// disable endpoint.
		MailProvider:    mailProvider,
		M365Onmicrosoft: strPtrOrNil(m365Tenant),
		GoogleDKIM:      strPtrOrNil(googleDKIM),
		EmailEnabled:    mailEnabled,
		SkipAutoSAN:     mailSkipSAN,
		CreateWWW:       req.CreateWWW,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := h.cfg.Domains.Create(ctx, domain); err != nil {
		if isConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "domain_already_exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Attempt SSL inline (30s timeout): try ACME first with fallback to self-signed.
	// Never errors out — just logs; cert state is already in DB.
	if h.cfg.Reconciler != nil {
		inlineCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		h.cfg.Reconciler.ReconcileSSLInline(inlineCtx, domain)
		cancel()
	}

	// Auto-enable email. Best-effort per ADR-0013: a failure here (agent
	// down, Stalwart refuses the name, DNS sync hiccup) degrades back to
	// the pre-auto-enable model — email_enabled stays 0 and the operator
	// sees the UI's "Enable email" retry switch on the domain's Email
	// tab. DNS-autoconfig warnings aren't returned in the create
	// response (that would change the wire shape); they're surfaced to
	// the operator on the next GET /domains/:id/email poll, which
	// computes live DNS status anyway. Hard errors go to slog.
	if mailProvider == models.MailProviderJabali && h.cfg.Agent != nil && h.cfg.DNSZones != nil && h.cfg.DNSRecords != nil {
		if _, _, warnings, err := EnableDomainEmailInline(ctx, enableDomainEmailDeps{
			Agent:          h.cfg.Agent,
			Domains:        h.cfg.Domains,
			DNSZones:       h.cfg.DNSZones,
			DNSRecords:     h.cfg.DNSRecords,
			ServerSettings: h.cfg.ServerSettings,
			SSLCerts:       h.cfg.SSLCerts,
			SSLReconciler:  h.cfg.Reconciler,
		}, domain); err != nil {
			slog.Warn("auto-enable email failed during domain.create (operator can retry from UI)",
				"domain_id", domain.ID, "domain", domain.Name, "err", err)
		} else if len(warnings) > 0 {
			slog.Info("auto-enable email DNS autoconfig warnings",
				"domain_id", domain.ID, "domain", domain.Name, "warnings", warnings)
		}
	}

	// Schedule reconciliation. The reconciler will converge the domain's
	// OS-level state (nginx vhost, PHP pool, etc.) with the DB state.
	// This is non-blocking and out-of-band.
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}

	// EnableDomainEmailInline mutated domain in place on success so the
	// response already carries email_enabled=true, dkim_selector, etc.
	c.JSON(http.StatusCreated, domain)
}

func (h *domainHandler) update(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	domain, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var req updateDomainRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()

	if req.IsEnabled != nil && *req.IsEnabled != domain.IsEnabled {
		domain.IsEnabled = *req.IsEnabled
	}

	// Nginx Custom Directives (raw textarea) AND Rule Builder
	// (typed JSON rules) are admin-only. The threat model: a tenant
	// with nginx_custom_directives can SSRF localhost services
	// (panel-api, Bulwark, Stalwart admin, PHP-FPM sockets of other
	// tenants), disclose files via `root /etc/jabali-panel/`,
	// suppress CrowdSec via `access_log off;`, or redefine an
	// auth_basic-protected location to drop the auth. nginx -t
	// only catches syntax — none of those is a syntax error.
	// proxy_pass targets in the Rule Builder are likewise unsafe
	// without a target allowlist. Gate the entire surface here so
	// the UI's tab-hiding can't be the only line of defense.
	//
	// Tenants posting these fields get them silently dropped (rather
	// than 403'd) so they can still PATCH unrelated fields like
	// is_enabled. Admin role bypasses the gate.
	if claims.IsAdmin {
		if req.NginxCustomDirectives != nil {
			if msg := validateNginxDirectives(*req.NginxCustomDirectives); msg != "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": msg})
				return
			}
			domain.NginxCustomDirectives = req.NginxCustomDirectives
		}

		if req.NginxRules != nil {
			if err := validateNginxRules(*req.NginxRules); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			domain.NginxRules = *req.NginxRules
		}

		// DocRoot (GH #265): admin-only. Confine to the owner's home via
		// validateDocumentRoot; empty resets to the canonical default.
		if req.DocRoot != nil {
			owner, oerr := h.cfg.Users.FindByID(ctx, domain.UserID)
			if oerr != nil || owner == nil || owner.Username == nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "owner_lookup_failed"})
				return
			}
			newRoot := strings.TrimSpace(*req.DocRoot)
			if newRoot == "" {
				newRoot = "/home/" + *owner.Username + "/domains/" + domain.Name + "/public_html"
			}
			if err := validateDocumentRoot(newRoot, *owner.Username, domain.Name); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_doc_root", "detail": err.Error()})
				return
			}
			domain.DocRoot = newRoot
		}
	}

	// GH #307: a non-admin owner may set a SAFE SUBSET of the Rule Builder
	// (rewrite + custom_header) on their own domain when the admin has opted in
	// (server_settings.tenant_domain_options_enabled). proxy_pass/ip_access stay
	// admin-only (see the admin block above). The field is silently dropped when
	// the opt-in is off, mirroring nginx_safe_options, so other fields still PATCH.
	if !claims.IsAdmin && req.NginxRules != nil {
		allowed := false
		if h.cfg.ServerSettings != nil {
			if st, sErr := h.cfg.ServerSettings.Get(ctx); sErr == nil && st != nil && st.TenantDomainOptionsEnabled {
				allowed = true
			}
		}
		if allowed {
			if err := validateTenantNginxRules(*req.NginxRules); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_nginx_rules", "detail": err.Error()})
				return
			}
			domain.NginxRules = *req.NginxRules
		}
	}

	// GH #526: a non-admin owner may repoint their own domain's docroot within
	// the domain's own tree (e.g. a framework's public/ subdir) when the admin
	// has opted in (tenant_domain_options_enabled). Confined by
	// validateTenantDocumentRoot; admins use the looser whole-home path above.
	// Silently dropped when the opt-in is off so other fields still PATCH.
	if !claims.IsAdmin && req.DocRoot != nil {
		allowed := false
		if h.cfg.ServerSettings != nil {
			if st, sErr := h.cfg.ServerSettings.Get(ctx); sErr == nil && st != nil && st.TenantDomainOptionsEnabled {
				allowed = true
			}
		}
		if allowed {
			owner, oerr := h.cfg.Users.FindByID(ctx, domain.UserID)
			if oerr != nil || owner == nil || owner.Username == nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "owner_lookup_failed"})
				return
			}
			newRoot := strings.TrimSpace(*req.DocRoot)
			if newRoot == "" {
				newRoot = "/home/" + *owner.Username + "/domains/" + domain.Name + "/public_html"
			}
			if err := validateTenantDocumentRoot(newRoot, *owner.Username, domain.Name); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_doc_root", "detail": err.Error()})
				return
			}
			domain.DocRoot = newRoot
		}
	}

	if req.RedirectAllTo != nil {
		trimmed := strings.TrimSpace(*req.RedirectAllTo)
		if trimmed == "" {
			domain.RedirectAllTo = nil
		} else {
			if err := validateRedirectURL(trimmed); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			domain.RedirectAllTo = &trimmed
		}
	}

	if req.RedirectAllType != nil {
		trimmed := strings.TrimSpace(*req.RedirectAllType)
		if trimmed == "" {
			domain.RedirectAllType = nil
		} else if !isValidRedirectType(trimmed) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid redirect type"})
			return
		} else {
			domain.RedirectAllType = &trimmed
		}
	}

	if req.PageRedirects != nil {
		if err := validatePageRedirects(*req.PageRedirects); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		domain.PageRedirects = *req.PageRedirects
	}

	if req.WebmailEnabled != nil {
		domain.WebmailEnabled = *req.WebmailEnabled
	}
	if req.IndexPriority != nil {
		p := strings.TrimSpace(*req.IndexPriority)
		if !isValidIndexPriority(p) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_index_priority"})
			return
		}
		domain.IndexPriority = p
	}

	// Tenant-safe nginx options (GH #307). Owner-settable ONLY when the admin
	// has opted in (server_settings.tenant_domain_options_enabled); admins may
	// always set them. These render to fixed vetted directives (max body, HSTS,
	// security headers, gzip) — never raw config — so they don't carry the
	// admin-only raw-directive surface. A non-admin owner with the opt-in off
	// has the field silently dropped (so other fields still PATCH).
	if req.NginxSafeOptions != nil {
		allowed := claims.IsAdmin
		if !allowed && h.cfg.ServerSettings != nil {
			if st, sErr := h.cfg.ServerSettings.Get(ctx); sErr == nil && st != nil && st.TenantDomainOptionsEnabled {
				allowed = true
			}
		}
		if allowed {
			if err := req.NginxSafeOptions.Validate(); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_nginx_options", "detail": err.Error()})
				return
			}
			domain.NginxSafeOptions = *req.NginxSafeOptions
		}
	}

	// M24: per-domain IP binding — validate FK + family + (for non-admin)
	// is_user_selectable. We resolve and validate before issuing any DB
	// write so a bad ipv4 doesn't half-succeed against ipv6.
	listenUpd, ipErr := h.resolveListenIPUpdate(ctx, claims.IsAdmin, req.ListenIPv4ID, req.ListenIPv6ID)
	if ipErr != nil {
		c.JSON(ipErr.Status, gin.H{"error": ipErr.Code, "detail": ipErr.Detail})
		return
	}

	domain.UpdatedAt = time.Now().UTC()
	if err := h.cfg.Domains.Update(ctx, domain); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Listen IPs are written via dedicated repo method — Domain.Update's
	// allowlist intentionally excludes listen_ipv*_id. Mirror the in-memory
	// struct so the response reflects the new binding.
	if listenUpd.ChangeIPv4 || listenUpd.ChangeIPv6 {
		if err := h.cfg.Domains.SetListenIPs(ctx, domain.ID, listenUpd); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if listenUpd.ChangeIPv4 {
			domain.ListenIPv4ID = listenUpd.IPv4ID
		}
		if listenUpd.ChangeIPv6 {
			domain.ListenIPv6ID = listenUpd.IPv6ID
		}
	}

	// GH#181 mail provider: dedicated repo method (Domain.Update's
	// allowlist excludes these columns). Validate, derive the two mail
	// flags, write. A switch re-publishes DNS + reissues the cert on the
	// next reconcile (Schedule below).
	if req.MailProvider != nil {
		mp := *req.MailProvider
		if !models.ValidMailProvider(mp) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_mail_provider"})
			return
		}
		var m365In, gdkimIn string
		if req.M365Onmicrosoft != nil {
			m365In = *req.M365Onmicrosoft
		}
		if req.GoogleDKIM != nil {
			gdkimIn = *req.GoogleDKIM
		}
		m365Tenant, err := dnscompile.NormaliseM365Onmicrosoft(m365In)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_m365_onmicrosoft", "detail": err.Error()})
			return
		}
		gdkim, err := dnscompile.ValidateGoogleDKIM(gdkimIn)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_google_dkim", "detail": err.Error()})
			return
		}
		emailEnabled, skipSAN := models.DeriveMailFlags(mp)
		if err := h.cfg.Domains.UpdateMailProvider(ctx, domain.ID, repository.DomainMailProvider{
			Provider:        mp,
			EmailEnabled:    emailEnabled,
			SkipAutoSAN:     skipSAN,
			M365Onmicrosoft: strPtrOrNil(m365Tenant),
			GoogleDKIM:      strPtrOrNil(gdkim),
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		domain.MailProvider = mp
		domain.EmailEnabled = emailEnabled
		domain.SkipAutoSAN = skipSAN
		domain.M365Onmicrosoft = strPtrOrNil(m365Tenant)
		domain.GoogleDKIM = strPtrOrNil(gdkim)
	}

	// GH #246: TLS cert mode switch. Dedicated repo method (Domain.Update's
	// allowlist excludes ssl_mode). Invariants guarded: the panel-primary
	// domain must keep TLS, and a mail-enabled domain can't go to 'none'.
	if req.SSLMode != nil {
		mode := *req.SSLMode
		if !models.ValidSSLMode(mode) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_ssl_mode"})
			return
		}
		if mode == models.SSLModeCustom {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ssl_mode_custom_via_upload", "detail": "upload a custom cert via the SSL settings to switch to custom"})
			return
		}
		if domain.IsPanelPrimary && mode == models.SSLModeNone {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ssl_none_panel_primary", "detail": "the panel hostname must keep TLS"})
			return
		}
		if mode == models.SSLModeNone && domain.EmailEnabled {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "ssl_none_with_email", "detail": "disable mail before removing TLS"})
			return
		}
		if err := h.cfg.Domains.UpdateSSLMode(ctx, domain.ID, mode); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		domain.SSLMode = mode
		domain.SSLEnabled = models.SSLEnabledForMode(mode)
	}

	// Schedule reconciliation to sync the domain state with the agent.
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}

	c.JSON(http.StatusOK, h.enrichDomainResponse(ctx, *domain))
}

// listenIPError carries the (status, code, detail) tuple from
// resolveListenIPUpdate so the caller can emit the right JSON shape
// without resorting to multi-error sentinel checks.
type listenIPError struct {
	Status int
	Code   string
	Detail string
}

// resolveListenIPUpdate validates the listen_ipv*_id PATCH fields and
// builds the repository.DomainListenIPs payload. Returns ChangeIPv4 /
// ChangeIPv6 only for fields that were actually present in the request.
//
// Rules:
//   - ManagedIPs repo not wired → 503 (the FK target table is the
//     authoritative source; we won't accept blind writes).
//   - IPv4 field carries an IPv6 address (or vice-versa) → 400.
//   - Referenced row missing → 404.
//   - Non-admin caller picking a row with is_user_selectable=false → 403.
//   - Explicit null → unbind (fall back to server default at render time).
func (h *domainHandler) resolveListenIPUpdate(ctx context.Context, isAdmin bool, v4, v6 nullableUint64) (repository.DomainListenIPs, *listenIPError) {
	upd := repository.DomainListenIPs{}
	if !v4.Set && !v6.Set {
		return upd, nil
	}
	if h.cfg.ManagedIPs == nil {
		return upd, &listenIPError{
			Status: http.StatusServiceUnavailable,
			Code:   "ip_pool_unavailable",
			Detail: "managed IP pool is not configured on this server",
		}
	}
	if v4.Set {
		if v4.Value == nil {
			upd.ChangeIPv4 = true
			upd.IPv4ID = nil
		} else {
			row, err := h.cfg.ManagedIPs.FindByID(ctx, *v4.Value)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return upd, &listenIPError{Status: http.StatusNotFound, Code: "listen_ipv4_not_found"}
				}
				return upd, &listenIPError{Status: http.StatusInternalServerError, Code: "internal"}
			}
			if row.Family != "ipv4" {
				return upd, &listenIPError{Status: http.StatusBadRequest, Code: "listen_ipv4_family_mismatch", Detail: "managed_ip " + strconv.FormatUint(row.ID, 10) + " is not an IPv4 address"}
			}
			if !isAdmin && !row.IsUserSelectable {
				return upd, &listenIPError{Status: http.StatusForbidden, Code: "listen_ipv4_not_user_selectable", Detail: "this IPv4 is not enabled for user selection — ask an administrator"}
			}
			upd.ChangeIPv4 = true
			upd.IPv4ID = &row.ID
		}
	}
	if v6.Set {
		if v6.Value == nil {
			upd.ChangeIPv6 = true
			upd.IPv6ID = nil
		} else {
			row, err := h.cfg.ManagedIPs.FindByID(ctx, *v6.Value)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return upd, &listenIPError{Status: http.StatusNotFound, Code: "listen_ipv6_not_found"}
				}
				return upd, &listenIPError{Status: http.StatusInternalServerError, Code: "internal"}
			}
			if row.Family != "ipv6" {
				return upd, &listenIPError{Status: http.StatusBadRequest, Code: "listen_ipv6_family_mismatch", Detail: "managed_ip " + strconv.FormatUint(row.ID, 10) + " is not an IPv6 address"}
			}
			if !isAdmin && !row.IsUserSelectable {
				return upd, &listenIPError{Status: http.StatusForbidden, Code: "listen_ipv6_not_user_selectable", Detail: "this IPv6 is not enabled for user selection — ask an administrator"}
			}
			upd.ChangeIPv6 = true
			upd.IPv6ID = &row.ID
		}
	}
	return upd, nil
}

func (h *domainHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()
	domain, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Capture name BEFORE deleting — once the DB row is gone, the
	// reconciler can't look it up by ID. We pass the name to
	// ReconcileDeleted which targets the agent-side teardown directly.
	name := domain.Name
	if err := h.cfg.Domains.Delete(ctx, domain.ID); err != nil {
		// M6.4 (ADR-0048): the panel-primary row is delete-protected at
		// the repo layer. Translate to 403 with a specific error code
		// so the panel UI can render a tooltip instead of a generic 500.
		if errors.Is(err, repository.ErrCannotDeletePanelPrimary) {
			c.JSON(http.StatusForbidden, gin.H{"error": "panel_primary_protected"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Tear down OS-level resources out-of-band. Best-effort: the user
	// sees the row gone immediately; if agent teardown fails, the next
	// ReconcileAll tick logs the orphan for ops to investigate.
	if h.cfg.Reconciler != nil {
		go h.cfg.Reconciler.ReconcileDeleted(context.Background(), name)
	}

	c.Status(http.StatusNoContent)
}

// allowedNginxDirectives is a per-line allowlist of nginx directives that users
// can safely include in the server {} block. This is a FIRST line of defense;
// the agent still runs nginx -t before applying, so malformed input is caught there.
var allowedNginxDirectives = map[string]struct{}{
	// Headers/response
	"add_header":        {},
	"add_trailer":       {},
	"expires":           {},
	"etag":              {},
	"if_modified_since": {},
	"return":            {},
	// Rewrites
	"rewrite":    {},
	"set":        {},
	"if":         {},
	"break":      {},
	"error_page": {},
	// Proxy — proxy_pass removed (JAB-66): the structured Rule Builder is the
	// only path for proxy_pass, and it SSRF-validates the target (JAB-65). A raw
	// proxy_pass directive had no host guard at all.
	"proxy_set_header":        {},
	"proxy_hide_header":       {},
	"proxy_pass_header":       {},
	"proxy_buffering":         {},
	"proxy_buffer_size":       {},
	"proxy_buffers":           {},
	"proxy_http_version":      {},
	"proxy_read_timeout":      {},
	"proxy_connect_timeout":   {},
	"proxy_send_timeout":      {},
	"proxy_redirect":          {},
	"proxy_ssl_verify":        {},
	"proxy_ssl_server_name":   {},
	"proxy_request_buffering": {},
	"proxy_cache_bypass":      {},
	"proxy_no_cache":          {},
	// Body/upload
	"client_max_body_size":    {},
	"client_body_buffer_size": {},
	"client_body_timeout":     {},
	"client_header_timeout":   {},
	// FastCGI
	"fastcgi_read_timeout": {},
	"fastcgi_send_timeout": {},
	"fastcgi_buffer_size":  {},
	"fastcgi_buffers":      {},
	"fastcgi_param":        {},
	// Static/locations
	"location":             {},
	"try_files":            {},
	"index":                {},
	"autoindex":            {},
	"autoindex_exact_size": {},
	"autoindex_localtime":  {},
	// sub_filter{,_once,_types} removed (JAB-66): arbitrary content injection /
	// response splitting into proxied responses.
	"charset":       {},
	"default_type":  {},
	"types":         {},
	"log_not_found": {},
	// Access
	"allow":   {},
	"deny":    {},
	"satisfy": {},
	// auth_basic{,_user_file} removed (JAB-66): auth_basic_user_file is an
	// arbitrary-file read / existence oracle (point it at /etc/shadow). Use the
	// structured Directory Privacy feature, which manages the htpasswd file.
	"limit_except":   {},
	"limit_req":      {},
	"limit_req_zone": {},
	"limit_conn":     {},
	// Gzip
	"gzip":            {},
	"gzip_types":      {},
	"gzip_min_length": {},
	"gzip_comp_level": {},
	"gzip_vary":       {},
	"gzip_disable":    {},
	"gzip_proxied":    {},
	// Caching
	"open_file_cache":          {},
	"open_file_cache_valid":    {},
	"open_file_cache_min_uses": {},
	"open_file_cache_errors":   {},
}

func validateNginxDirectives(directives string) string {
	// Reject if input contains null bytes (binary/injection attempt).
	if strings.ContainsRune(directives, '\x00') {
		return "forbidden directive: null byte detected"
	}

	lines := strings.Split(directives, "\n")
	blockDepth := 0
	maxNestingDepth := 3

	for _, line := range lines {
		// Strip comments while respecting strings.
		cleaned := stripComments(line)
		cleaned = strings.TrimSpace(cleaned)

		// Empty lines are allowed.
		if cleaned == "" {
			continue
		}

		// Count opening and closing braces in this line (respecting strings).
		opens, closes := countBraces(cleaned)

		for i := 0; i < opens; i++ {
			blockDepth++
			if blockDepth > maxNestingDepth {
				return "forbidden directive: nesting depth exceeded (max " + strconv.Itoa(maxNestingDepth) + ")"
			}
		}
		for i := 0; i < closes; i++ {
			blockDepth--
			if blockDepth < 0 {
				return "forbidden directive: unbalanced braces (extra closing })"
			}
		}

		// Handle lines that are purely {, }, or {}.
		if cleaned == "{" || cleaned == "}" || cleaned == "{}" {
			continue
		}

		// Extract the directive name (first token).
		directive := extractDirective(cleaned)
		if directive == "" {
			continue
		}

		// Skip if the first token is a brace.
		if directive == "{" || directive == "}" {
			continue
		}

		// Normalize to lowercase and check against allowlist.
		directive = strings.ToLower(directive)
		if _, allowed := allowedNginxDirectives[directive]; !allowed {
			return "forbidden directive: " + directive
		}
	}

	// Ensure braces are balanced.
	if blockDepth != 0 {
		return "forbidden directive: unbalanced braces (unclosed {)"
	}

	return ""
}

// countBraces counts opening and closing braces in a line, respecting quoted strings.
func countBraces(line string) (opens, closes int) {
	inSingleQuote := false
	inDoubleQuote := false

	for _, ch := range line {
		switch ch {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '{':
			if !inSingleQuote && !inDoubleQuote {
				opens++
			}
		case '}':
			if !inSingleQuote && !inDoubleQuote {
				closes++
			}
		}
	}
	return
}

// stripComments removes everything from # onwards, but respects # inside
// single or double-quoted strings.
func stripComments(line string) string {
	inSingleQuote := false
	inDoubleQuote := false

	for i, ch := range line {
		switch ch {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '#':
			if !inSingleQuote && !inDoubleQuote {
				return line[:i]
			}
		}
	}
	return line
}

// extractDirective returns the first whitespace-delimited token from a line.
func extractDirective(line string) string {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func validateRedirectURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid destination URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("destination URL must use http or https scheme")
	}
	if u.Host == "" {
		return fmt.Errorf("destination URL must have a host")
	}
	return nil
}

func isValidRedirectType(s string) bool {
	switch s {
	case "301", "302", "307", "308":
		return true
	default:
		return false
	}
}

func validatePageRedirects(prs models.PageRedirects) error {
	if len(prs) > 100 {
		return fmt.Errorf("too many redirects (max 100)")
	}
	for i, pr := range prs {
		if !strings.HasPrefix(pr.Source, "/") {
			return fmt.Errorf("entry %d: source must start with /", i)
		}
		if strings.ContainsAny(pr.Source, "\n\x00") {
			return fmt.Errorf("entry %d: source contains invalid chars", i)
		}
		if err := validateRedirectURL(pr.Destination); err != nil {
			return fmt.Errorf("entry %d: invalid page redirect destination: %w", i, err)
		}
		if !isValidRedirectType(pr.Type) {
			return fmt.Errorf("entry %d: invalid type for page redirect: %s", i, pr.Type)
		}
		// Wildcard only supports 301 and 302
		if pr.Wildcard && pr.Type != "301" && pr.Type != "302" {
			return fmt.Errorf("entry %d: wildcard redirects only support 301 or 302", i)
		}
	}
	return nil
}

func isValidNginxRuleType(s string) bool {
	switch s {
	case "custom_header", "rewrite", "proxy_pass", "ip_access", "php_setting", "max_upload_size", "static_alias":
		return true
	}
	return false
}

// validateNginxRules checks each rule has the fields required by its
// Type. Field-level constraints (e.g. header name format, valid CIDR)
// are intentionally lenient — nginx -t on the agent is the final check.

// validateNginxRules checks each rule has the fields required by its
// Type. Field-level constraints (e.g. header name format, valid CIDR)
// are intentionally lenient — nginx -t on the agent is the final check.
// maxNginxRules caps the number of typed nginx rules per domain. It is a
// soft DoS/abuse bound, not a correctness limit -- `nginx -t` on the agent is
// the real validator. Raised from 50 (GH #301): real .htaccess conversions and
// framework routing can need hundreds of rewrites.
const maxNginxRules = 500

func validateNginxRules(rules models.NginxRules) error {
	if len(rules) > maxNginxRules {
		return fmt.Errorf("too many rules (max %d)", maxNginxRules)
	}
	for i, r := range rules {
		if !isValidNginxRuleType(r.Type) {
			return fmt.Errorf("rule %d: unknown type %q", i, r.Type)
		}
		switch r.Type {
		case "custom_header":
			if r.Name == "" {
				return fmt.Errorf("rule %d: header name required", i)
			}
			if r.Value == "" {
				return fmt.Errorf("rule %d: header value required", i)
			}
			if strings.ContainsAny(r.Name, " \t\n\r:;") {
				return fmt.Errorf("rule %d: invalid chars in header name", i)
			}
		case "rewrite":
			if r.Pattern == "" || r.Replacement == "" {
				return fmt.Errorf("rule %d: pattern and replacement required", i)
			}
			if err := validateRewritePattern(r.Pattern, 256); err != nil {
				return fmt.Errorf("rule %d: %v", i, err)
			}
			switch r.Flag {
			case "", "last", "break", "redirect", "permanent":
			default:
				return fmt.Errorf("rule %d: invalid flag %q", i, r.Flag)
			}
		case "proxy_pass":
			if r.Path == "" || r.Target == "" {
				return fmt.Errorf("rule %d: path and target required", i)
			}
			if !strings.HasPrefix(r.Target, "http://") && !strings.HasPrefix(r.Target, "https://") {
				return fmt.Errorf("rule %d: target must be an http(s) URL", i)
			}
			if err := validateProxyPassTarget(r.Target); err != nil {
				return fmt.Errorf("rule %d: %v", i, err)
			}
			if r.ReadTimeout != "" && !isNginxDuration(r.ReadTimeout) {
				return fmt.Errorf("rule %d: read_timeout must be an nginx duration (e.g. \"60s\", \"24h\")", i)
			}
		case "ip_access":
			if r.Path == "" {
				return fmt.Errorf("rule %d: path required", i)
			}
			if r.Mode != "allow_list" && r.Mode != "deny_list" {
				return fmt.Errorf("rule %d: mode must be allow_list or deny_list", i)
			}
			// allow_list with zero IPs compiles to a bare `deny all;` — a
			// valid, most-restrictive config (an .htaccess `Deny from all`
			// converts to exactly this). A deny_list with zero IPs would deny
			// nobody (a no-op), so still require at least one there.
			if r.Mode == "deny_list" && len(r.IPs) == 0 {
				return fmt.Errorf("rule %d: deny_list needs at least one IP", i)
			}
		case "php_setting":
			if r.Name == "" || r.Value == "" {
				return fmt.Errorf("rule %d: name and value required", i)
			}
		case "max_upload_size":
			if r.Size == "" {
				return fmt.Errorf("rule %d: size required", i)
			}
		case "static_alias":
			// Serves a filesystem dir (Path=location, Target=alias dir). Used by
			// framework apps (Django STATIC_ROOT). Admin-only (not in the tenant
			// safe set). Constrain the alias to under /home/ so it can never be
			// pointed at /etc or another system path (file disclosure), even by
			// an admin PATCH; static aliases are always a tenant app dir.
			if r.Path == "" || r.Target == "" {
				return fmt.Errorf("rule %d: path and target required", i)
			}
			if !strings.HasPrefix(r.Target, "/home/") || strings.Contains(r.Target, "..") {
				return fmt.Errorf("rule %d: static_alias target must be an absolute path under /home/ with no ..", i)
			}
			if strings.ContainsAny(r.Target, " \t\n\r;{}") {
				return fmt.Errorf("rule %d: invalid chars in static_alias target", i)
			}
		}
		// Forbid control characters everywhere to prevent newline injection into vhost
		allText := r.Name + r.Value + r.Pattern + r.Replacement + r.Target + r.Path + r.Size + r.ReadTimeout
		for _, c := range allText {
			if c < 32 && c != '\t' {
				return fmt.Errorf("rule %d: contains invalid control chars", i)
			}
		}
		for _, ip := range r.IPs {
			if strings.ContainsAny(ip, " \t\n\r") {
				return fmt.Errorf("rule %d: invalid chars in IP %q", i, ip)
			}
		}
	}
	return nil
}

// redosNestedQuantRE flags the classic catastrophic-backtracking shape: a group
// that itself contains a + or * quantifier and is ITSELF quantified with + or *
// — e.g. (a+)+, (.*)*, (\\d+)*. nginx uses PCRE (backtracking), so these can
// pin a worker on every request to the vhost. Go's RE2 is linear and won't
// "detect" ReDoS by running, so we match the shape textually.
var redosNestedQuantRE = regexp.MustCompile(`\([^)]*[+*][^)]*\)\s*[+*]`)

// validateRewritePattern guards the nginx `rewrite` pattern (JAB-72), which is
// rendered verbatim (unquoted) into the vhost. Cap the length, reject nested
// quantifiers (ReDoS), and reject grossly invalid regex syntax via a RE2
// compile pre-check. RE2 is stricter than PCRE on a few constructs (named
// groups, backreferences) which nginx rewrites almost never use in the MATCH;
// the trade favors blocking a worker-pinning pattern over allowing exotic ones.
func validateRewritePattern(pattern string, maxLen int) error {
	if len(pattern) > maxLen {
		return fmt.Errorf("rewrite pattern too long (%d > %d chars)", len(pattern), maxLen)
	}
	if redosNestedQuantRE.MatchString(pattern) {
		return fmt.Errorf("rewrite pattern has nested quantifiers (ReDoS risk)")
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("rewrite pattern is not valid regex: %v", err)
	}
	return nil
}

// validateProxyPassTarget blocks SSRF via the proxy_pass rule type (JAB-65).
// proxy_pass is admin-only, but a compromised admin session could otherwise
// point a public domain at an internal service (MariaDB/Redis/panel-api socket)
// and read its responses through the vhost. Reject unix-socket upstreams and
// literal loopback / private / link-local / unspecified targets; external
// hostnames and public IPs still work. Hostnames are NOT DNS-resolved here
// (resolution is TOCTOU vs DNS-rebind) — the literal-address classes are the
// ones the acceptance criteria enumerate.
func validateProxyPassTarget(target string) error {
	// nginx's unix upstream form is `http://unix:/path:/`; url.Parse mangles it,
	// so match the scheme token directly before parsing.
	if strings.Contains(strings.ToLower(target), "unix:") {
		return fmt.Errorf("target may not be a unix socket")
	}
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("target is not a valid URL: %v", err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("target has no host")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("target may not be localhost")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("target may not point at an internal address (%s)", host)
		}
	}
	return nil
}

// tenantSafeNginxRuleTypes are the Rule Builder types a non-admin owner may set
// on their own domain (GH #307). proxy_pass is excluded (SSRF to localhost
// services — panel-api/Bulwark/Stalwart/other tenants' FPM sockets), as are
// ip_access/php_setting/max_upload_size (admin surface / covered by
// nginx_safe_options).
var tenantSafeNginxRuleTypes = map[string]struct{}{
	"rewrite":       {},
	"custom_header": {},
}

// validateTenantNginxRules enforces the tenant-safe subset on top of the full
// structural validateNginxRules: only rewrite + custom_header, and a rewrite
// replacement must be a LOCAL path (no scheme or host) so it can never become
// an open redirect or a proxy to an internal service.
func validateTenantNginxRules(rules models.NginxRules) error {
	for i, r := range rules {
		if _, ok := tenantSafeNginxRuleTypes[r.Type]; !ok {
			return fmt.Errorf("rule %d: type %q is not available to tenants", i, r.Type)
		}
		if r.Type == "rewrite" {
			rep := strings.TrimSpace(r.Replacement)
			if strings.Contains(rep, "://") || strings.HasPrefix(rep, "//") {
				return fmt.Errorf("rule %d: rewrite target must be a local path (no scheme or host)", i)
			}
			// JAB-72: tenants get a stricter pattern length cap than admins.
			if err := validateRewritePattern(r.Pattern, 128); err != nil {
				return fmt.Errorf("rule %d: %v", i, err)
			}
		}
	}
	return validateNginxRules(rules)
}

func isValidIndexPriority(s string) bool {
	switch s {
	case "html_first", "php_first", "html_only", "php_only", "full":
		return true
	}
	return false
}

func domainLinuxUser(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return "user"
}

// bandwidth returns per-day bytes + monthly totals for a single domain.
// Default window: prior 30 days inclusive of today. Query parameter
// `from` accepts YYYY-MM-DD to override; `to` defaults to today UTC.
//
// Response shape:
//
//	{
//	  "domain_id": "01...",
//	  "from": "2026-04-09", "to": "2026-05-09",
//	  "bytes_total": 12345678, "requests_total": 9876,
//	  "daily": [
//	    {"day": "2026-05-08", "bytes_total": 1234, "requests_total": 56},
//	    ...
//	  ]
//	}
//
// Authorization mirrors GET /domains/:id: admins read any, users only
// their own. Domain ownership is verified via the existing FindByID
// path, which already 404s on cross-tenant lookup.
func (h *domainHandler) bandwidth(c *gin.Context) {
	if h.cfg.BWDaily == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "bandwidth feature not wired"})
		return
	}
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	domainID := c.Param("id")
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}

	now := time.Now().UTC()
	to := now
	from := now.AddDate(0, 0, -29) // 30 days inclusive
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t
		}
	}

	bytesTotal, reqsTotal, err := h.cfg.BWDaily.SumForDomain(c.Request.Context(), dom.ID, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	daily, err := h.cfg.BWDaily.SumPerDayForDomain(c.Request.Context(), dom.ID, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Go marshals a nil slice as JSON `null`, not `[]`. The SPA's
	// DomainBandwidthCard does `data.daily.map(...)` (data is truthy,
	// daily is null) → TypeError → the whole DomainEdit page renders
	// blank (no error boundary). A domain with no bw_daily rows yet
	// (fresh / pre-goaccess-scan) must return an empty array.
	if daily == nil {
		daily = []repository.DailyPoint{}
	}

	c.JSON(http.StatusOK, gin.H{
		"domain_id":      dom.ID,
		"from":           from.Format("2006-01-02"),
		"to":             to.Format("2006-01-02"),
		"bytes_total":    bytesTotal,
		"requests_total": reqsTotal,
		"daily":          daily,
	})
}

// isNginxDuration accepts nginx's standard duration shape: a positive
// integer followed by one of `ms s m h d` (or none = seconds). Used to
// validate proxy_read_timeout on the rule-builder proxy_pass entry
// without pulling in nginx's full grammar.
func isNginxDuration(v string) bool {
	if v == "" {
		return false
	}
	i := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	switch v[i:] {
	case "", "ms", "s", "m", "h", "d":
		return true
	}
	return false
}

// strPtrOrNil returns nil for an empty string, else a pointer to it — so
// the optional mail-provider DKIM tokens write SQL NULL when absent.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
