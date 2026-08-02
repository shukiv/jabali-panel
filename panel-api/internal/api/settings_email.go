// Settings → Email endpoint. Read-only view of the panel-primary domain
// for the admin UI's Email card (M6.4 / ADR-0048).
//
// Wire contract (verify against this file, per
// feedback_verify_wire_contract.md):
//
//	GET /api/v1/admin/settings/email
//
//	Two distinct response shapes, discriminated by HTTP status code
//	(NOT by field presence). Clients MUST switch on status.
//
//	200 OK — panel-primary domain row exists:
//	  {
//	    "primary_domain_name": "jabali-panel.local",
//	    "webmail_url":         "https://mail.jabali-panel.local/",
//	    "dkim_published":      true,                        // DkimPublicKey != nil && != ""
//	    "email_enabled_at":    "2026-04-22T18:00:00Z"       // RFC3339, or null
//	  }
//
//	202 Accepted — row absent (install still converging, or pathological
//	operator SQL delete). Minimal shape, no null-filled fields:
//	  {
//	    "primary_domain_name": null,
//	    "status":              "initializing"
//	  }
//
// Absence is the designed behavior during the fresh-install convergence
// window; install.sh creates the row, the reconciler writes DKIM +
// zone records, and the UI progresses from "Initializing" to "Published"
// within ~30 seconds.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// SettingsEmailHandlerConfig wires the handler to its repository + logger.
type SettingsEmailHandlerConfig struct {
	Domains repository.DomainRepository
	Log     *slog.Logger
}

// RegisterSettingsEmailRoutes mounts GET /admin/settings/email under v1.
// Must be called after v1's auth middleware is attached.
func RegisterSettingsEmailRoutes(g *gin.RouterGroup, cfg SettingsEmailHandlerConfig) {
	h := &settingsEmailHandler{cfg: cfg}
	admin := g.Group("/admin/settings/email")
	admin.Use(middleware.RequireAdmin())
	admin.GET("", h.get)
}

type settingsEmailHandler struct {
	cfg SettingsEmailHandlerConfig
}

// settingsEmailOK is the 200 body shape.
type settingsEmailOK struct {
	PrimaryDomainName string     `json:"primary_domain_name"`
	WebmailURL        string     `json:"webmail_url"`
	DKIMPublished     bool       `json:"dkim_published"`
	EmailEnabledAt    *time.Time `json:"email_enabled_at"`
}

// settingsEmailInitializing is the 202 body shape. Deliberately separate
// struct so the client can discriminate on HTTP status without parsing
// against a union with nullable-everywhere fields.
type settingsEmailInitializing struct {
	PrimaryDomainName *string `json:"primary_domain_name"` // always nil; present for schema sanity
	Status            string  `json:"status"`              // always "initializing"
}

func (h *settingsEmailHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	d, err := h.cfg.Domains.FindPanelPrimary(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrPanelPrimaryNotFound) {
			c.JSON(http.StatusAccepted, settingsEmailInitializing{
				PrimaryDomainName: nil,
				Status:            "initializing",
			})
			return
		}
		h.cfg.Log.Error("find panel primary", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	dkimPublished := d.DkimPublicKey != nil && *d.DkimPublicKey != ""
	c.JSON(http.StatusOK, settingsEmailOK{
		PrimaryDomainName: d.Name,
		WebmailURL:        "https://mail." + d.Name + "/",
		DKIMPublished:     dkimPublished,
		EmailEnabledAt:    d.EmailEnabledAt,
	})
}
