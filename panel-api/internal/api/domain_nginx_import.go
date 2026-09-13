package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/nginximport"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainNginxImportHandlerConfig wires the nginx-snippet import preview route.
type DomainNginxImportHandlerConfig struct {
	Domains repository.DomainRepository
}

// RegisterDomainNginxImportRoutes adds:
//   - POST /domains/:id/nginx-import/preview
//
// Sibling of the .htaccess importer (ADR-0130) for people migrating from an
// nginx-based panel. Preview is STATELESS: it shape-matches the supplied nginx
// snippet into typed NginxRule entries (rewrite / custom_header / deny_paths /
// static_cache) and returns them with warnings for everything it would not
// convert. It mutates nothing — the UI then applies the (possibly edited) rules
// via PATCH /domains/:id { nginx_rules }, which runs validateTenantNginxRules +
// the reconciler's nginx -t gate.
func RegisterDomainNginxImportRoutes(g *gin.RouterGroup, cfg DomainNginxImportHandlerConfig) {
	h := &domainNginxImportHandler{cfg: cfg}
	g.POST("/domains/:id/nginx-import/preview", h.preview)
}

type domainNginxImportHandler struct{ cfg DomainNginxImportHandlerConfig }

type nginxImportPreviewRequest struct {
	Content string `json:"content" binding:"required"`
}

type nginxImportPreviewResponse struct {
	Rules    models.NginxRules     `json:"rules"`
	Warnings []nginximport.Warning `json:"warnings"`
	Notes    []string              `json:"notes"`
}

// maxNginxSnippetBytes caps the input. Migration snippets are larger than the
// 8 KiB raw-directive grammar (a whole server block), but still small.
const maxNginxSnippetBytes = 64 * 1024

func (h *domainNginxImportHandler) preview(c *gin.Context) {
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

	var req nginxImportPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if len(req.Content) > maxNginxSnippetBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "snippet_too_large"})
		return
	}

	res := nginximport.Convert(req.Content)

	// Re-validate each candidate against the SAME tenant rule validator the
	// apply path (PATCH nginx_rules) uses, and demote any that would be rejected
	// to a warning. This guarantees the previewed rules are exactly the ones
	// that will save — a converter/validator drift shows up here as a warning
	// rather than a surprise 400 on apply, and it enforces the tenant subset
	// (e.g. a rewrite whose replacement is an absolute URL is not silently kept).
	kept := make([]models.NginxRule, 0, len(res.Rules))
	for _, r := range res.Rules {
		if err := validateTenantNginxRules(models.NginxRules{r}); err != nil {
			res.Warnings = append(res.Warnings, nginximport.Warning{
				Line:     0,
				Source:   ruleSummary(r),
				Reason:   "not a valid tenant rule: " + err.Error(),
				Security: false,
			})
			continue
		}
		kept = append(kept, r)
	}

	c.JSON(http.StatusOK, nginxImportPreviewResponse{
		Rules:    models.NginxRules(kept),
		Warnings: res.Warnings,
		Notes:    res.Notes,
	})
}

// ruleSummary is a short human label for a rule that failed tenant validation,
// used as the Source of the demotion warning.
func ruleSummary(r models.NginxRule) string {
	switch r.Type {
	case "rewrite":
		return "rewrite " + r.Pattern + " " + r.Replacement
	case "custom_header":
		return "add_header " + r.Name
	case "deny_paths":
		return "deny_paths " + joinExt(r.Extensions)
	case "static_cache":
		return "static_cache " + joinExt(r.Extensions) + " " + r.Duration
	}
	return r.Type
}

func joinExt(exts []string) string {
	out := ""
	for i, e := range exts {
		if i > 0 {
			out += "|"
		}
		out += e
	}
	return out
}
