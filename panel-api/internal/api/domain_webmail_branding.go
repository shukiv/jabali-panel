package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailbranding"
)

// DomainWebmailBrandingHandlerConfig wires a domain's webmail branding (GH
// #2502): the name, logos, favicon and links webmail shows on mail.<domain>.
type DomainWebmailBrandingHandlerConfig struct {
	Domains  repository.DomainRepository
	Users    repository.UserRepository
	Packages repository.PackageRepository
	Branding repository.DomainWebmailBrandingRepository
	Log      *slog.Logger
	// StrictRateLimit (optional) limits the changes.
	StrictRateLimit gin.HandlerFunc
	// Dir overrides webmailbranding.Dir (tests).
	Dir string
}

// RegisterDomainWebmailBrandingRoutes mounts, for the domain's owner and
// administrators:
//
//   - GET    /domains/:id/webmail-branding          the branding, and whether the caller may change it
//   - PUT    /domains/:id/webmail-branding          the name and link fields
//   - DELETE /domains/:id/webmail-branding          back to the server-wide branding
//   - GET    /domains/:id/webmail-branding/:asset   a picture, for the panel's preview
//   - POST   /domains/:id/webmail-branding/:asset   upload a picture (multipart "file")
//   - DELETE /domains/:id/webmail-branding/:asset   remove a picture
//
// :asset is logo-light, logo-dark or favicon. The owner may change it while
// their package has webmail branding; an administrator always. The reconciler
// sends it to webmail on its next pass, about a minute.
func RegisterDomainWebmailBrandingRoutes(g *gin.RouterGroup, cfg DomainWebmailBrandingHandlerConfig) {
	if cfg.Domains == nil || cfg.Users == nil || cfg.Branding == nil {
		panic("api.RegisterDomainWebmailBrandingRoutes: Domains, Users and Branding are required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Dir == "" {
		cfg.Dir = webmailbranding.Dir
	}
	h := &domainWebmailBrandingHandler{cfg: cfg}
	limited := func(f gin.HandlerFunc) []gin.HandlerFunc {
		if cfg.StrictRateLimit == nil {
			return []gin.HandlerFunc{f}
		}
		return []gin.HandlerFunc{cfg.StrictRateLimit, f}
	}
	rg := g.Group("/domains/:id/webmail-branding")
	rg.GET("", h.get)
	rg.PUT("", limited(h.put)...)
	rg.DELETE("", limited(h.reset)...)
	rg.GET("/:asset", h.picture)
	rg.POST("/:asset", limited(h.upload)...)
	rg.DELETE("/:asset", limited(h.removePicture)...)
}

type domainWebmailBrandingHandler struct {
	cfg DomainWebmailBrandingHandlerConfig
}

// webmailBrandingResponse is a domain's branding. LogoLight, LogoDark and
// Favicon are "" when the domain has no such picture. Applies: webmail shows
// it, because an administrator set it or the owner's package has webmail
// branding.
type webmailBrandingResponse struct {
	Host        string `json:"host"`
	CanEdit     bool   `json:"can_edit"`
	Applies     bool   `json:"applies"`
	SetByAdmin  bool   `json:"set_by_admin"`
	AppName     string `json:"app_name"`
	CompanyName string `json:"company_name"`
	WebsiteURL  string `json:"website_url"`
	PrivacyURL  string `json:"privacy_url"`
	ImprintURL  string `json:"imprint_url"`
	LogoLight   string `json:"logo_light"`
	LogoDark    string `json:"logo_dark"`
	Favicon     string `json:"favicon"`
}

// brandingAccess is who asks about which domain.
type brandingAccess struct {
	dom           *models.Domain
	actorID       string
	admin         bool
	packageAllows bool
}

func (a *brandingAccess) canEdit() bool { return a.admin || a.packageAllows }

// access loads the domain and checks the caller is its owner or an
// administrator. It answers the request itself when not.
func (h *domainWebmailBrandingHandler) access(c *gin.Context) (*brandingAccess, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	ctx := c.Request.Context()
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	allows, err := h.packageAllows(ctx, dom.UserID)
	if err != nil {
		h.cfg.Log.Error("webmail branding: owner's package", "domain_id", dom.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return &brandingAccess{dom: dom, actorID: claims.UserID, admin: claims.IsAdmin, packageAllows: allows}, true
}

// packageAllows reports whether the user's package has webmail branding. A
// user with no package, or a package that is gone, has not (GH #282).
func (h *domainWebmailBrandingHandler) packageAllows(ctx context.Context, userID string) (bool, error) {
	if h.cfg.Packages == nil {
		return false, nil
	}
	u, err := h.cfg.Users.FindByID(ctx, userID)
	if err != nil {
		return false, err
	}
	if u.PackageID == nil {
		return false, nil
	}
	pkg, err := h.cfg.Packages.FindByID(ctx, *u.PackageID)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return pkg.WebmailBrandingEnabled, nil
}

// mayEdit answers a change the caller may not make.
func (h *domainWebmailBrandingHandler) mayEdit(c *gin.Context, a *brandingAccess) bool {
	if a.canEdit() {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{
		"error":  "webmail_branding_not_in_package",
		"detail": "your hosting package does not include webmail branding",
	})
	return false
}

// load returns the domain's branding, or an empty one.
func (h *domainWebmailBrandingHandler) load(ctx context.Context, domainID string) (*models.DomainWebmailBranding, error) {
	row, err := h.cfg.Branding.Get(ctx, domainID)
	if errors.Is(err, repository.ErrNotFound) {
		return &models.DomainWebmailBranding{DomainID: domainID}, nil
	}
	return row, err
}

func (h *domainWebmailBrandingHandler) respond(c *gin.Context, a *brandingAccess, row *models.DomainWebmailBranding) {
	c.JSON(http.StatusOK, webmailBrandingResponse{
		Host:        "mail." + a.dom.Name,
		CanEdit:     a.canEdit(),
		Applies:     !row.Empty() && (row.SetByAdmin || a.packageAllows),
		SetByAdmin:  row.SetByAdmin,
		AppName:     row.AppName,
		CompanyName: row.CompanyName,
		WebsiteURL:  row.WebsiteURL,
		PrivacyURL:  row.PrivacyURL,
		ImprintURL:  row.ImprintURL,
		LogoLight:   row.LogoLight,
		LogoDark:    row.LogoDark,
		Favicon:     row.Favicon,
	})
}

// save stores the branding as the caller's, or deletes it, pictures
// included, when nothing is left in it.
func (h *domainWebmailBrandingHandler) save(ctx context.Context, a *brandingAccess, row *models.DomainWebmailBranding) error {
	now := time.Now().UTC()
	if row.Empty() {
		if err := h.cfg.Branding.Delete(ctx, row.DomainID); err != nil {
			return err
		}
		return webmailbranding.RemoveAll(h.cfg.Dir, row.DomainID)
	}
	row.SetByAdmin = a.admin
	row.UpdatedAt = now
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	return h.cfg.Branding.Put(ctx, row)
}

func (h *domainWebmailBrandingHandler) audit(kind string, a *brandingAccess, extra ...any) {
	args := append([]any{"event", "audit", "kind", kind, "actor_id", a.actorID, "domain_id", a.dom.ID}, extra...)
	h.cfg.Log.Info("webmail branding changed", args...)
}

func (h *domainWebmailBrandingHandler) get(c *gin.Context) {
	a, ok := h.access(c)
	if !ok {
		return
	}
	row, err := h.load(c.Request.Context(), a.dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.respond(c, a, row)
}

type webmailBrandingRequest struct {
	AppName     string `json:"app_name"`
	CompanyName string `json:"company_name"`
	WebsiteURL  string `json:"website_url"`
	PrivacyURL  string `json:"privacy_url"`
	ImprintURL  string `json:"imprint_url"`
}

func (h *domainWebmailBrandingHandler) put(c *gin.Context) {
	a, ok := h.access(c)
	if !ok || !h.mayEdit(c, a) {
		return
	}
	var req webmailBrandingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	var fields [5]string
	var err error
	checks := []func() (string, error){
		func() (string, error) {
			return webmailbranding.Text("app_name", req.AppName, webmailbranding.MaxAppName)
		},
		func() (string, error) {
			return webmailbranding.Text("company_name", req.CompanyName, webmailbranding.MaxCompanyName)
		},
		func() (string, error) { return webmailbranding.Link("website_url", req.WebsiteURL) },
		func() (string, error) { return webmailbranding.Link("privacy_url", req.PrivacyURL) },
		func() (string, error) { return webmailbranding.Link("imprint_url", req.ImprintURL) },
	}
	for i, check := range checks {
		if fields[i], err = check(); err != nil {
			var fe *webmailbranding.FieldError
			if errors.As(err, &fe) {
				c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_field", "field": fe.Field, "detail": fe.Error()})
				return
			}
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_field", "detail": err.Error()})
			return
		}
	}
	ctx := c.Request.Context()
	row, err := h.load(ctx, a.dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	row.AppName, row.CompanyName, row.WebsiteURL, row.PrivacyURL, row.ImprintURL = fields[0], fields[1], fields[2], fields[3], fields[4]
	if err := h.save(ctx, a, row); err != nil {
		h.cfg.Log.Error("webmail branding: save", "domain_id", a.dom.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.audit("webmail_branding_saved", a)
	h.respond(c, a, row)
}

func (h *domainWebmailBrandingHandler) reset(c *gin.Context) {
	a, ok := h.access(c)
	if !ok || !h.mayEdit(c, a) {
		return
	}
	ctx := c.Request.Context()
	if err := h.cfg.Branding.Delete(ctx, a.dom.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if err := webmailbranding.RemoveAll(h.cfg.Dir, a.dom.ID); err != nil {
		h.cfg.Log.Warn("webmail branding: remove pictures", "domain_id", a.dom.ID, "err", err)
	}
	h.audit("webmail_branding_reset", a)
	h.respond(c, a, &models.DomainWebmailBranding{DomainID: a.dom.ID})
}

// asset parses :asset, answering an unknown one.
func asset(c *gin.Context) (webmailbranding.Asset, bool) {
	as, ok := webmailbranding.ParseAsset(c.Param("asset"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown_picture", "detail": "the picture is logo-light, logo-dark or favicon"})
	}
	return as, ok
}

// pictureHash is the row's field for the picture.
func pictureHash(row *models.DomainWebmailBranding, as webmailbranding.Asset) *string {
	switch as {
	case webmailbranding.LogoLight:
		return &row.LogoLight
	case webmailbranding.LogoDark:
		return &row.LogoDark
	default:
		return &row.Favicon
	}
}

func (h *domainWebmailBrandingHandler) picture(c *gin.Context) {
	a, ok := h.access(c)
	if !ok {
		return
	}
	as, ok := asset(c)
	if !ok {
		return
	}
	row, err := h.load(c.Request.Context(), a.dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	path := webmailbranding.Path(h.cfg.Dir, a.dom.ID, as)
	if *pictureHash(row, as) == "" {
		c.Status(http.StatusNotFound)
		return
	}
	if _, err := os.Stat(path); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", "image/png")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=60")
	c.File(path)
}

func (h *domainWebmailBrandingHandler) upload(c *gin.Context) {
	a, ok := h.access(c)
	if !ok || !h.mayEdit(c, a) {
		return
	}
	as, ok := asset(c)
	if !ok {
		return
	}
	// Room for the multipart framing around a picture of the largest size.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, webmailbranding.MaxUploadBytes+64*1024)
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "picture_too_large", "detail": "the picture must be at most 512 KB"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_file", "detail": "send the picture in the form field \"file\""})
		return
	}
	defer file.Close()
	data, hash, err := webmailbranding.Encode(file)
	switch {
	case errors.Is(err, webmailbranding.ErrTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "picture_too_large", "detail": "the picture must be at most 512 KB"})
		return
	case errors.Is(err, webmailbranding.ErrBadPicture):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_picture", "detail": "upload a PNG or JPEG picture"})
		return
	case errors.Is(err, webmailbranding.ErrBadSize):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "picture_size", "detail": "the picture must be at most 1024 × 1024 pixels"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	ctx := c.Request.Context()
	row, err := h.load(ctx, a.dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if err := webmailbranding.Write(h.cfg.Dir, a.dom.ID, as, data); err != nil {
		h.cfg.Log.Error("webmail branding: store picture", "domain_id", a.dom.ID, "picture", as, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage_failed"})
		return
	}
	*pictureHash(row, as) = hash
	if err := h.save(ctx, a, row); err != nil {
		h.cfg.Log.Error("webmail branding: save", "domain_id", a.dom.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.audit("webmail_branding_picture_uploaded", a, "picture", string(as))
	h.respond(c, a, row)
}

func (h *domainWebmailBrandingHandler) removePicture(c *gin.Context) {
	a, ok := h.access(c)
	if !ok || !h.mayEdit(c, a) {
		return
	}
	as, ok := asset(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	row, err := h.load(ctx, a.dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	*pictureHash(row, as) = ""
	if err := h.save(ctx, a, row); err != nil {
		h.cfg.Log.Error("webmail branding: save", "domain_id", a.dom.ID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if err := webmailbranding.Remove(h.cfg.Dir, a.dom.ID, as); err != nil {
		h.cfg.Log.Warn("webmail branding: remove picture", "domain_id", a.dom.ID, "picture", as, "err", err)
	}
	h.audit("webmail_branding_picture_removed", a, "picture", string(as))
	h.respond(c, a, row)
}
