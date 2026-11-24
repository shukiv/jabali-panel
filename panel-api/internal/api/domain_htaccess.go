package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/htaccess"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainHtaccessHandlerConfig wires the .htaccess import preview route.
type DomainHtaccessHandlerConfig struct {
	Domains repository.DomainRepository
}

// RegisterDomainHtaccessRoutes adds:
//   - POST /domains/:id/htaccess/preview
//
// Preview is STATELESS: it converts the supplied .htaccess text into typed
// NginxRule entries and returns them with warnings/notes for the operator to
// review. It mutates nothing — the UI then applies the (possibly edited)
// rules via the existing PATCH /domains/:id { nginx_rules } path, which runs
// validateNginxRules + the reconciler's nginx -t gate.
func RegisterDomainHtaccessRoutes(g *gin.RouterGroup, cfg DomainHtaccessHandlerConfig) {
	h := &domainHtaccessHandler{cfg: cfg}
	g.POST("/domains/:id/htaccess/preview", h.preview)
}

type domainHtaccessHandler struct{ cfg DomainHtaccessHandlerConfig }

type htaccessPreviewRequest struct {
	Content  string `json:"content" binding:"required"`
	BasePath string `json:"base_path"`
}

type htaccessPreviewResponse struct {
	Rules    models.NginxRules  `json:"rules"`
	Warnings []htaccess.Warning `json:"warnings"`
	Notes    []string           `json:"notes"`
	// Redirects go to the domain's Redirects list (page_redirects), not the
	// Rule Builder.
	Redirects models.PageRedirects `json:"redirects"`
	// Invalid lists any converted rule the API would reject on apply. Should
	// be empty (the converter emits only typed, valid rules); surfaced so a
	// converter/validator drift never silently strips a rule on save.
	Invalid []string `json:"invalid,omitempty"`
}

// maxHtaccessBytes caps the input so a pathological paste can't tie up the
// converter. Real .htaccess files are a few KB; 256 KiB is generous.
const maxHtaccessBytes = 768 * 1024 // a migrated site can carry ~1,000 Redirect lines; the request body cap is 1 MiB

func (h *domainHtaccessHandler) preview(c *gin.Context) {
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

	var req htaccessPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if len(req.Content) > maxHtaccessBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "htaccess_too_large"})
		return
	}

	base := strings.TrimSpace(req.BasePath)
	if base == "" {
		base = "/"
	}

	res := htaccess.Convert(req.Content, base)

	// Defensive: the converter is expected to emit only valid typed rules, but
	// run the same validator the apply path uses so any future drift is
	// visible here rather than silently dropping a rule on PATCH.
	var invalid []string
	if err := validateNginxRules(models.NginxRules(res.Rules)); err != nil {
		invalid = append(invalid, err.Error())
	}
	if err := validatePageRedirects(models.PageRedirects(res.Redirects)); err != nil {
		invalid = append(invalid, err.Error())
	}

	// Always emit arrays (never JSON null) so the SPA can read .length/.filter
	// without a null guard — a no-rule conversion is {rules:[],...}, not null.
	rules := res.Rules
	if rules == nil {
		rules = []models.NginxRule{}
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []htaccess.Warning{}
	}
	notes := res.Notes
	if notes == nil {
		notes = []string{}
	}
	redirs := models.PageRedirects(res.Redirects)
	if redirs == nil {
		redirs = models.PageRedirects{}
	}

	c.JSON(http.StatusOK, htaccessPreviewResponse{
		Rules:     models.NginxRules(rules),
		Warnings:  warnings,
		Notes:     notes,
		Redirects: redirs,
		Invalid:   invalid,
	})
}
