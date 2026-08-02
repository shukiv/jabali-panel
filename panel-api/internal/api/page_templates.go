package api

import (
	"errors"
	"log/slog"
	"net/http"
	"text/template"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// validatePageTemplateContent rejects a body that the agent could not use.
// GH #860: domain_default_index is expanded as a Go text/template at
// docroot-write time; if it fails to parse, the agent silently falls back to
// the built-in default, so an operator's edit "did nothing". Validating at
// SAVE time makes that failure visible (the editor shows the parse error)
// instead of a silent no-op on every future domain. Error pages are static
// HTML, so they are not parsed. Returns "" when the content is acceptable.
func validatePageTemplateContent(key, content string) string {
	if key != models.PageTemplateDomainDefaultIndex {
		return ""
	}
	if _, err := template.New("index").Parse(content); err != nil {
		// The Go error carries the position (line/token). Prefix it so the
		// toast reads as guidance, not a bare parser dump — the usual cause is
		// a literal {{ }} that isn't one of the supported placeholders.
		return "Template not saved — it has a syntax error. Only {{.Domain}}, " +
			"{{.Username}} and {{.DocRoot}} are valid; a literal {{ }} from " +
			"another framework will break it. Details: " + err.Error()
	}
	return ""
}

// MaxPageTemplateBytes caps the accepted template body. Large enough
// for complex error pages with inline CSS; small enough that an
// accidental file paste doesn't land a 10MB blob in the DB.
const MaxPageTemplateBytes = 128 * 1024

type PageTemplatesHandlerConfig struct {
	Repo repository.PageTemplateRepository
	Log  *slog.Logger
}

func RegisterPageTemplatesRoutes(g *gin.RouterGroup, cfg PageTemplatesHandlerConfig) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &pageTemplatesHandler{cfg: cfg}
	admin := g.Group("/admin/settings/page-templates")
	admin.Use(middleware.RequireAdmin())
	admin.GET("", h.list)
	admin.GET("/:key", h.get)
	admin.PATCH("/:key", h.update)
	admin.POST("/:key/reset", h.reset)
}

type pageTemplatesHandler struct{ cfg PageTemplatesHandlerConfig }

type pageTemplateDTO struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Content     string `json:"content"`
	IsDefault   bool   `json:"is_default"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func templateMeta(key string) (label, desc string) {
	switch key {
	case models.PageTemplateDomainDefaultIndex:
		return "Domain default index", "Written as index.html on new domain docroots when no real content is present. Supports {{.Domain}}, {{.Username}}, {{.DocRoot}} placeholders."
	case models.PageTemplateDomainDisabled:
		return "Disabled site", "Served for every domain that has been administratively disabled. One shared page — no per-domain placeholders."
	case models.PageTemplateDomainUnconfigured:
		return "Unconfigured domain", "Served when a domain resolves to this server but has not been added to the panel. Only shown when the unconfigured-domain page is enabled in Server Settings; by default unknown hosts are dropped without a response."
	case models.PageTemplateError404:
		return "404 — Not Found", "Served when a requested resource does not exist."
	case models.PageTemplateError403:
		return "403 — Forbidden", "Served when access is denied (directory listing off, blocked by rule, etc.)."
	case models.PageTemplateError500:
		return "500 — Server Error", "Served on internal server errors."
	}
	return key, ""
}

func isKnownTemplateKey(key string) bool {
	for _, k := range models.AllPageTemplateKeys {
		if k == key {
			return true
		}
	}
	return false
}

func (h *pageTemplatesHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.cfg.Repo.List(ctx)
	if err != nil {
		h.cfg.Log.Error("page_templates list failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list failed"})
		return
	}
	byKey := make(map[string]models.PageTemplate, len(rows))
	for _, r := range rows {
		byKey[r.Key] = r
	}
	out := make([]pageTemplateDTO, 0, len(models.AllPageTemplateKeys))
	for _, key := range models.AllPageTemplateKeys {
		label, desc := templateMeta(key)
		defaultBody := repository.DefaultPageTemplateBody(key)
		if row, ok := byKey[key]; ok {
			out = append(out, pageTemplateDTO{
				Key:         key,
				Label:       label,
				Description: desc,
				Content:     row.Content,
				IsDefault:   row.Content == defaultBody,
				UpdatedAt:   row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			})
		} else {
			out = append(out, pageTemplateDTO{
				Key:         key,
				Label:       label,
				Description: desc,
				Content:     defaultBody,
				IsDefault:   true,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *pageTemplatesHandler) get(c *gin.Context) {
	key := c.Param("key")
	if !isKnownTemplateKey(key) {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown template key"})
		return
	}
	label, desc := templateMeta(key)
	defaultBody := repository.DefaultPageTemplateBody(key)
	row, err := h.cfg.Repo.Get(c.Request.Context(), key)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusOK, pageTemplateDTO{
			Key:         key,
			Label:       label,
			Description: desc,
			Content:     defaultBody,
			IsDefault:   true,
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load failed"})
		return
	}
	c.JSON(http.StatusOK, pageTemplateDTO{
		Key:         key,
		Label:       label,
		Description: desc,
		Content:     row.Content,
		IsDefault:   row.Content == defaultBody,
		UpdatedAt:   row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

type updateTemplateRequest struct {
	Content string `json:"content"`
}

func (h *pageTemplatesHandler) update(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	key := c.Param("key")
	if !isKnownTemplateKey(key) {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown template key"})
		return
	}
	var req updateTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "detail": err.Error()})
		return
	}
	if len(req.Content) > MaxPageTemplateBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "content too large"})
		return
	}
	if detail := validatePageTemplateContent(key, req.Content); detail != "" {
		// GH #860: reject a template that won't parse instead of letting the
		// agent silently fall back to the built-in default on every new domain.
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "template_parse_error", "detail": detail})
		return
	}
	if err := h.cfg.Repo.Upsert(c.Request.Context(), key, req.Content); err != nil {
		h.cfg.Log.Error("page_template upsert failed", "key", key, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
		return
	}
	h.cfg.Log.Info("event=audit kind=page_template_updated actor_id=" + claims.UserID + " key=" + key)
	c.Status(http.StatusNoContent)
}

func (h *pageTemplatesHandler) reset(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	key := c.Param("key")
	if !isKnownTemplateKey(key) {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown template key"})
		return
	}
	body := repository.DefaultPageTemplateBody(key)
	if err := h.cfg.Repo.Upsert(c.Request.Context(), key, body); err != nil {
		h.cfg.Log.Error("page_template reset failed", "key", key, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reset failed"})
		return
	}
	h.cfg.Log.Info("event=audit kind=page_template_reset actor_id=" + claims.UserID + " key=" + key)
	c.Status(http.StatusNoContent)
}
