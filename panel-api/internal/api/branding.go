package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// BrandingDir is the on-disk root for operator-uploaded panel logos.
// install.sh creates /var/lib/jabali-panel with owner panel:panel,
// mode 0755; this subdirectory is created lazily on first upload.
const BrandingDir = "/var/lib/jabali-panel/branding"

// MaxLogoBytes caps the accepted logo size. 512KB is more than enough
// for a PNG/SVG brand mark; blocks accidental 10MB hero image uploads.
const MaxLogoBytes = 512 * 1024

// allowedLogoExts is the extension allowlist. Driven by MIME sniff
// result too — filename extension alone isn't trusted.
var allowedLogoExts = map[string]string{
	"image/png":     ".png",
	"image/svg+xml": ".svg",
	"image/webp":    ".webp",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
}

type BrandingHandlerConfig struct {
	Repo  repository.ServerSettingsRepository
	Log   *slog.Logger
	Agent agent.AgentInterface
}

// RegisterBrandingRoutes mounts admin-only logo upload/delete under
// /api/v1/admin/settings/branding/logo.
func RegisterBrandingRoutes(g *gin.RouterGroup, cfg BrandingHandlerConfig) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &brandingHandler{cfg: cfg}
	admin := g.Group("/admin/settings/branding")
	admin.Use(middleware.RequireAdmin())
	admin.POST("/logo/:variant", h.upload)
	admin.DELETE("/logo/:variant", h.clear)
}

// RegisterPublicBrandingRoutes mounts the unauthenticated
// /branding/logo/:variant + /branding endpoints used by the login
// page and topbar pre-auth. Returns 404 when the logo hasn't been
// uploaded so the SPA falls back to the built-in default.
func RegisterPublicBrandingRoutes(g *gin.RouterGroup, cfg BrandingHandlerConfig) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &brandingHandler{cfg: cfg}
	g.GET("/branding", h.publicInfo)
	g.GET("/branding/logo/:variant", h.publicLogo)
}

type brandingHandler struct{ cfg BrandingHandlerConfig }

type brandingInfoResponse struct {
	PanelBrandText           string `json:"panel_brand_text"`
	PanelFontSize            string `json:"panel_font_size"`
	PanelPrimaryColor        string `json:"panel_primary_color"`
	PanelAccentColor         string `json:"panel_accent_color"`
	PanelSuccessColor        string `json:"panel_success_color"`
	PanelWarningColor        string `json:"panel_warning_color"`
	PanelErrorColor          string `json:"panel_error_color"`
	PanelInfoColor           string `json:"panel_info_color"`
	PanelLinkColor           string `json:"panel_link_color"`
	PanelLightTopbarColor    string `json:"panel_light_topbar_color"`
	PanelDarkTopbarColor     string `json:"panel_dark_topbar_color"`
	PanelLightBgColor        string `json:"panel_light_bg_color"`
	PanelDarkBgColor         string `json:"panel_dark_bg_color"`
	PanelLightContainerColor string `json:"panel_light_container_color"`
	PanelDarkContainerColor  string `json:"panel_dark_container_color"`
	PanelLightTextColor      string `json:"panel_light_text_color"`
	PanelDarkTextColor       string `json:"panel_dark_text_color"`
	HasLogoLight             bool   `json:"has_logo_light"`
	HasLogoDark              bool   `json:"has_logo_dark"`
	// PanelHostname is the operator-configured panel hostname (from
	// server_settings). The login page compares it to the browser's
	// current host so it can tell when the panel is being reached over
	// the raw server IP (or a stale FQDN) — where Kratos's same-origin
	// login cookies can't complete — and offer a one-click link to the
	// correct host (GH #1411). Public on purpose: the hostname is already
	// disclosed in the TLS cert SANs the server presents and in DNS.
	// Empty when the operator hasn't set one (IP-only install).
	PanelHostname string `json:"panel_hostname"`
}

func (h *brandingHandler) publicInfo(c *gin.Context) {
	s, err := h.cfg.Repo.Get(c.Request.Context())
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusOK, brandingInfoResponse{})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, brandingInfoResponse{
		PanelBrandText:           s.PanelBrandText,
		PanelFontSize:            fontSizeOrDefault(s.PanelFontSize),
		PanelPrimaryColor:        s.PanelPrimaryColor,
		PanelAccentColor:         s.PanelAccentColor,
		PanelSuccessColor:        s.PanelSuccessColor,
		PanelWarningColor:        s.PanelWarningColor,
		PanelErrorColor:          s.PanelErrorColor,
		PanelInfoColor:           s.PanelInfoColor,
		PanelLinkColor:           s.PanelLinkColor,
		PanelLightTopbarColor:    s.PanelLightTopbarColor,
		PanelDarkTopbarColor:     s.PanelDarkTopbarColor,
		PanelLightBgColor:        s.PanelLightBgColor,
		PanelDarkBgColor:         s.PanelDarkBgColor,
		PanelLightContainerColor: s.PanelLightContainerColor,
		PanelDarkContainerColor:  s.PanelDarkContainerColor,
		PanelLightTextColor:      s.PanelLightTextColor,
		PanelDarkTextColor:       s.PanelDarkTextColor,
		HasLogoLight:             s.LogoLightPath != "" && fileExists(s.LogoLightPath),
		HasLogoDark:              s.LogoDarkPath != "" && fileExists(s.LogoDarkPath),
		// Normalise the same way models.EffectivePreviewBase does
		// (lowercase, drop a trailing FQDN dot) so the SPA's
		// case-insensitive host compare is exact.
		PanelHostname: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Hostname)), "."),
	})
}

func (h *brandingHandler) publicLogo(c *gin.Context) {
	variant := c.Param("variant")
	if variant != "light" && variant != "dark" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "variant must be light or dark"})
		return
	}
	s, err := h.cfg.Repo.Get(c.Request.Context())
	if errors.Is(err, repository.ErrNotFound) || err != nil {
		if err != nil && !errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		c.Status(http.StatusNotFound)
		return
	}
	path := s.LogoLightPath
	if variant == "dark" {
		path = s.LogoDarkPath
	}
	if path == "" || !fileExists(path) {
		c.Status(http.StatusNotFound)
		return
	}
	// Static cache hint — browsers can keep it for 5 min so the topbar
	// doesn't re-fetch on every page nav. Re-uploading generates a new
	// filename (extension may change) so stale cache is harmless.
	c.Header("Cache-Control", "public, max-age=300")
	c.File(path)
}

func (h *brandingHandler) upload(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	variant := c.Param("variant")
	if variant != "light" && variant != "dark" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "variant must be light or dark"})
		return
	}
	// MaxLogoBytes+1 so we can detect oversize; the +1 sniff byte gives
	// a clean 413 rather than a silent truncation.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxLogoBytes+1024)
	file, hdr, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file form field", "detail": err.Error()})
		return
	}
	defer file.Close()

	if hdr.Size > MaxLogoBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("logo must be <= %d bytes", MaxLogoBytes)})
		return
	}

	ext, err := sniffLogoExt(file, hdr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := os.MkdirAll(BrandingDir, 0o755); err != nil {
		h.cfg.Log.Error("mkdir branding dir", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage setup failed"})
		return
	}

	dest := filepath.Join(BrandingDir, variant+ext)

	// Write to temp first, rename in place — never leave a half-written
	// file on disk.
	tmp, err := os.CreateTemp(BrandingDir, "."+variant+".*.tmp")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "temp create failed"})
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
		return
	}
	if err := tmp.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "close failed"})
		return
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "chmod failed"})
		return
	}

	// Remove any stale logo at a different extension before renaming so
	// we don't leave orphan files if the operator re-uploaded png→svg.
	for _, e := range []string{".png", ".svg", ".webp", ".jpg", ".gif"} {
		prior := filepath.Join(BrandingDir, variant+e)
		if prior != dest {
			_ = os.Remove(prior)
		}
	}

	if err := os.Rename(tmpName, dest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "rename failed"})
		return
	}

	ctx := c.Request.Context()
	current, err := h.cfg.Repo.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		current = &models.ServerSettings{ID: 1}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load settings failed"})
		return
	}
	if variant == "light" {
		current.LogoLightPath = dest
	} else {
		current.LogoDarkPath = dest
	}
	if err := h.cfg.Repo.Upsert(ctx, current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist settings failed"})
		return
	}

	h.cfg.Log.Info("event=audit kind=branding_logo_uploaded actor_id=" + claims.UserID + " variant=" + variant + " ext=" + ext)
	h.syncWebmailBranding(current.PanelBrandText)
	c.JSON(http.StatusOK, gin.H{"variant": variant, "ext": ext})
}

// syncWebmailBranding pushes the operator branding (logo + brand text) to
// Bulwark webmail server-wide (GH #200). Best-effort, in the background so
// the admin's upload/clear response isn't blocked on a webmail restart.
func (h *brandingHandler) syncWebmailBranding(brandText string) {
	if h.cfg.Agent == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := h.cfg.Agent.Call(ctx, "webmail.branding.apply", map[string]any{
			"brand_text": brandText,
		}); err != nil && h.cfg.Log != nil {
			h.cfg.Log.Warn("webmail branding sync failed", "err", err)
		}
	}()
}

func (h *brandingHandler) clear(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	variant := c.Param("variant")
	if variant != "light" && variant != "dark" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "variant must be light or dark"})
		return
	}
	ctx := c.Request.Context()
	current, err := h.cfg.Repo.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	prior := current.LogoLightPath
	if variant == "light" {
		current.LogoLightPath = ""
	} else {
		prior = current.LogoDarkPath
		current.LogoDarkPath = ""
	}
	if prior != "" {
		_ = os.Remove(prior)
	}
	if err := h.cfg.Repo.Upsert(ctx, current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist settings failed"})
		return
	}
	h.cfg.Log.Info("event=audit kind=branding_logo_cleared actor_id=" + claims.UserID + " variant=" + variant)
	h.syncWebmailBranding(current.PanelBrandText)
	c.Status(http.StatusNoContent)
}

// sniffLogoExt reads the first 512 bytes, calls http.DetectContentType,
// cross-checks the allowlist, rewinds the file pointer so the caller
// can copy the full body. For SVG — which often sniffs as "text/xml"
// — we additionally require a .svg extension on the upload.
func sniffLogoExt(file multipart.File, hdr *multipart.FileHeader) (string, error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", fmt.Errorf("read file: %w", err)
	}
	head = head[:n]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek: %w", err)
	}
	ct := http.DetectContentType(head)
	// Some sniffers call SVG "text/xml; charset=utf-8" or
	// "text/plain; charset=utf-8". Accept when the filename extension
	// is .svg AND the body begins with "<svg" or "<?xml".
	lc := strings.ToLower(strings.TrimSpace(ct))
	if idx := strings.Index(lc, ";"); idx >= 0 {
		lc = strings.TrimSpace(lc[:idx])
	}
	if ext, ok := allowedLogoExts[lc]; ok {
		return ext, nil
	}
	if strings.EqualFold(filepath.Ext(hdr.Filename), ".svg") {
		peek := strings.TrimSpace(string(head))
		if strings.HasPrefix(peek, "<?xml") || strings.HasPrefix(strings.ToLower(peek), "<svg") {
			return ".svg", nil
		}
	}
	return "", fmt.Errorf("unsupported image type: %s", ct)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// fontSizeOrDefault normalizes a possibly-empty/legacy panel_font_size to one of
// the three allowed values (#433), defaulting to "medium".
func fontSizeOrDefault(v string) string {
	switch v {
	case "small", "large":
		return v
	default:
		return "medium"
	}
}
