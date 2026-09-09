// Custom DNS templates (GH #1627).
//
// Admin routes (mounted under /api/v1/admin, RequireAdmin):
//
//	GET    /dns-templates          list templates (with records)
//	POST   /dns-templates          create a template
//	GET    /dns-templates/:id      one template (with records)
//	PUT    /dns-templates/:id      replace a template's fields + records
//	DELETE /dns-templates/:id      delete a template
//
// Tenant route (mounted on the authed base group):
//
//	GET    /dns-templates          list templates (id/name/description only)
//
// A template is a named preset of DNS records an admin defines once; a tenant
// selects one at Web Domain / DNS Zone create (dns_template_id) and the
// reconciler seeds its records into the fresh zone. Templates are GLOBAL in
// this phase — every tenant sees every template — which is why the tenant list
// carries no ownership scope: a tenant can already type any record into their
// own zone, so a template is convenience, not an entitlement (GH #282 does not
// apply). Each record is validated at admin-create with the SAME
// ValidateDNSRecord the tenant DNS record API uses, so a template can never
// carry a record the record API would reject.
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DNSTemplateHandlerConfig wires the routes. A nil Templates repo disables
// registration (the feature is unwired).
type DNSTemplateHandlerConfig struct {
	Templates repository.DNSTemplateRepository
}

type dnsTemplateHandler struct{ cfg DNSTemplateHandlerConfig }

// RegisterAdminDNSTemplateRoutes mounts the admin CRUD under an already
// RequireAdmin-gated group.
func RegisterAdminDNSTemplateRoutes(admin *gin.RouterGroup, cfg DNSTemplateHandlerConfig) {
	if cfg.Templates == nil {
		return
	}
	h := &dnsTemplateHandler{cfg: cfg}
	g := admin.Group("/dns-templates")
	g.GET("", h.adminList)
	g.POST("", h.create)
	g.GET("/:id", h.get)
	g.PUT("/:id", h.update)
	g.DELETE("/:id", h.delete)
}

// RegisterDNSTemplateRoutes mounts the tenant-visible read-only list on the
// authed base group.
func RegisterDNSTemplateRoutes(v1 *gin.RouterGroup, cfg DNSTemplateHandlerConfig) {
	if cfg.Templates == nil {
		return
	}
	h := &dnsTemplateHandler{cfg: cfg}
	v1.GET("/dns-templates", h.tenantList)
}

type dnsTemplateRecordInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
}

type dnsTemplateInput struct {
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Records     []dnsTemplateRecordInput `json:"records"`
}

// dnsTemplateSummary is the tenant-facing shape: no record bodies.
type dnsTemplateSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

const dnsTemplateMaxRecords = 100

// buildTemplate validates the input and returns a populated (unsaved) template
// with fresh record IDs, or an (http status, code, detail) rejection.
func buildTemplate(in dnsTemplateInput) (*models.DNSTemplate, int, string, string) {
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
	if len(in.Records) > dnsTemplateMaxRecords {
		return nil, http.StatusBadRequest, "too_many_records", "a template may hold at most 100 records"
	}
	tmpl := &models.DNSTemplate{
		ID:          ids.NewULID(),
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		// Non-nil so a zero-record template serialises as "records": [] rather
		// than null (a null slice renders blank in the SPA table).
		Records: make([]models.DNSTemplateRecord, 0, len(in.Records)),
	}
	for i, rin := range in.Records {
		// Validate each record with the SAME validator the tenant record API
		// runs. ValidateDNSRecord normalises Type/Name/Content in place, so the
		// stored blueprint is already canonical. The {domain} token is a literal
		// here (no whitespace), so it passes for MX/CNAME/TXT and is only
		// rejected where it makes no sense (e.g. an A record needs a real IP).
		probe := &models.DNSRecord{
			Name:     rin.Name,
			Type:     rin.Type,
			Content:  rin.Content,
			TTL:      rin.TTL,
			Priority: rin.Priority,
		}
		if err := ValidateDNSRecord(probe); err != nil {
			return nil, http.StatusBadRequest, "invalid_record", "record " + strconv.Itoa(i+1) + ": " + err.Error()
		}
		tmpl.Records = append(tmpl.Records, models.DNSTemplateRecord{
			ID:       ids.NewULID(),
			Name:     probe.Name,
			Type:     probe.Type,
			Content:  probe.Content,
			TTL:      probe.TTL,
			Priority: probe.Priority,
		})
	}
	return tmpl, 0, "", ""
}

func (h *dnsTemplateHandler) create(c *gin.Context) {
	var in dnsTemplateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	tmpl, status, code, detail := buildTemplate(in)
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

func (h *dnsTemplateHandler) update(c *gin.Context) {
	var in dnsTemplateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	tmpl, status, code, detail := buildTemplate(in)
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

func (h *dnsTemplateHandler) get(c *gin.Context) {
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

func (h *dnsTemplateHandler) adminList(c *gin.Context) {
	tmpls, err := h.cfg.Templates.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if tmpls == nil {
		tmpls = []models.DNSTemplate{}
	}
	c.JSON(http.StatusOK, gin.H{"templates": tmpls})
}

func (h *dnsTemplateHandler) delete(c *gin.Context) {
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

func (h *dnsTemplateHandler) tenantList(c *gin.Context) {
	tmpls, err := h.cfg.Templates.ListSummaries(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]dnsTemplateSummary, 0, len(tmpls))
	for _, t := range tmpls {
		out = append(out, dnsTemplateSummary{ID: t.ID, Name: t.Name, Description: t.Description})
	}
	c.JSON(http.StatusOK, gin.H{"templates": out})
}
