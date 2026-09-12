// Admin web (nginx) templates (GH #1624 / ADR-0169 Phase 3).
//
// Admin routes (mounted under /api/v1/admin, RequireAdmin):
//
//	GET    /web-templates          list templates
//	POST   /web-templates          create a template
//	GET    /web-templates/:id      one template
//	PUT    /web-templates/:id      replace a template's fields
//	DELETE /web-templates/:id      delete a template
//
// A web template is a named preset of raw nginx directives an admin authors
// once; at Web Domain create an ADMIN may select one (web_template_id) and its
// directives are snapshot-copied onto the new domain's nginx_custom_directives.
// The directives are validated with the SAME ValidateNginxDirectivesAdmin the
// admin custom-directives PATCH uses, so a template can never carry a directive
// that path would reject.
//
// There is deliberately NO tenant-facing route: admin-select-only in this phase.
// The admin denylist does not block proxy_pass, so a globally-visible template a
// tenant could pick is an SSRF vector with the admin as unwitting author.
package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// WebTemplateHandlerConfig wires the routes. A nil Templates repo disables
// registration (the feature is unwired).
type WebTemplateHandlerConfig struct {
	Templates repository.WebTemplateRepository
}

type webTemplateHandler struct{ cfg WebTemplateHandlerConfig }

// RegisterAdminWebTemplateRoutes mounts the admin CRUD under an already
// RequireAdmin-gated group.
func RegisterAdminWebTemplateRoutes(admin *gin.RouterGroup, cfg WebTemplateHandlerConfig) {
	if cfg.Templates == nil {
		return
	}
	h := &webTemplateHandler{cfg: cfg}
	g := admin.Group("/web-templates")
	g.GET("", h.list)
	g.POST("", h.create)
	g.GET("/:id", h.get)
	g.PUT("/:id", h.update)
	g.DELETE("/:id", h.delete)
}

type webTemplateInput struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	NginxDirectives string `json:"nginx_directives"`
}

// webTemplateMaxDirectivesBytes caps the raw directive blob. 16 KiB is far more
// than any real per-vhost snippet and keeps a template row bounded.
const webTemplateMaxDirectivesBytes = 16 * 1024

// buildWebTemplate validates the input and returns a populated (unsaved)
// template with a fresh ID, or an (http status, code, detail) rejection. The
// directives are validated with the SAME relaxed denylist the admin
// custom-directives PATCH uses (ValidateNginxDirectivesAdmin).
func buildWebTemplate(in webTemplateInput) (*models.WebTemplate, int, string, string) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, http.StatusBadRequest, "name_required", "a template name is required"
	}
	if len(name) > 120 {
		return nil, http.StatusBadRequest, "name_too_long", "template name exceeds 120 characters"
	}
	if len(in.Description) > 500 {
		return nil, http.StatusBadRequest, "description_too_long", "template description exceeds 500 characters"
	}
	directives := strings.TrimSpace(in.NginxDirectives)
	if directives == "" {
		return nil, http.StatusBadRequest, "directives_required", "a template must carry at least one nginx directive"
	}
	if len(directives) > webTemplateMaxDirectivesBytes {
		return nil, http.StatusBadRequest, "directives_too_long", "nginx directives exceed 16 KiB"
	}
	if msg := ValidateNginxDirectivesAdmin(directives); msg != "" {
		return nil, http.StatusBadRequest, "invalid_directives", msg
	}
	return &models.WebTemplate{
		ID:              ids.NewULID(),
		Name:            name,
		Description:     strings.TrimSpace(in.Description),
		NginxDirectives: directives,
	}, 0, "", ""
}

func (h *webTemplateHandler) create(c *gin.Context) {
	var in webTemplateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	tmpl, status, code, detail := buildWebTemplate(in)
	if status != 0 {
		c.JSON(status, gin.H{"error": code, "detail": detail})
		return
	}
	if err := h.cfg.Templates.Create(c.Request.Context(), tmpl); err != nil {
		if isDuplicateKeyErr(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "name_taken", "detail": "a template with that name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusCreated, tmpl)
}

func (h *webTemplateHandler) update(c *gin.Context) {
	var in webTemplateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	tmpl, status, code, detail := buildWebTemplate(in)
	if status != 0 {
		c.JSON(status, gin.H{"error": code, "detail": detail})
		return
	}
	tmpl.ID = c.Param("id")
	if err := h.cfg.Templates.Update(c.Request.Context(), tmpl); err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		case isDuplicateKeyErr(err):
			c.JSON(http.StatusConflict, gin.H{"error": "name_taken", "detail": "a template with that name already exists"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}
	c.JSON(http.StatusOK, tmpl)
}

func (h *webTemplateHandler) get(c *gin.Context) {
	tmpl, err := h.cfg.Templates.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, tmpl)
}

func (h *webTemplateHandler) list(c *gin.Context) {
	tmpls, err := h.cfg.Templates.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if tmpls == nil {
		tmpls = []models.WebTemplate{}
	}
	c.JSON(http.StatusOK, gin.H{"templates": tmpls})
}

func (h *webTemplateHandler) delete(c *gin.Context) {
	if err := h.cfg.Templates.Delete(c.Request.Context(), c.Param("id")); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
