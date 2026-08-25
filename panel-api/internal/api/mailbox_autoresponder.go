// mailbox_autoresponder.go — M6.5 Step 3 autoresponder HTTP handlers.
//
// Wire contract: GET/PUT/DELETE /mailboxes/:mbid/autoresponder
// Backed by JMAP VacationResponse (RFC 8621 §8) via the reconciler
// phase + agent autoresponder.set command.

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/autoresponderops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// MailboxAutoresponderHandlerConfig wires the repos + agent this handler needs.
type MailboxAutoresponderHandlerConfig struct {
	Mailboxes      repository.MailboxRepository
	Domains        repository.DomainRepository
	Autoresponders repository.EmailAutoresponderRepository
	Agent          agent.AgentInterface
}

// autoresponderResponse is the JSON envelope the panel UI consumes.
type autoresponderResponse struct {
	MailboxID string     `json:"mailbox_id"`
	Enabled   bool       `json:"enabled"`
	FromDate  *time.Time `json:"from_date"`
	ToDate    *time.Time `json:"to_date"`
	Subject   *string    `json:"subject"`
	TextBody  *string    `json:"text_body"`
	HTMLBody  *string    `json:"html_body"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Warning is set (JAB-346 criterion 5) when the desired state was saved but
	// the inline agent push failed; the reconciler re-asserts shortly. Omitted
	// on a clean push.
	Warning string `json:"warning,omitempty"`
}

type autoresponderUpdateRequest struct {
	Enabled  bool       `json:"enabled"`
	FromDate *time.Time `json:"from_date"`
	ToDate   *time.Time `json:"to_date"`
	Subject  *string    `json:"subject"`
	TextBody *string    `json:"text_body"`
	HTMLBody *string    `json:"html_body"`
}

type mailboxAutoresponderHandler struct {
	cfg MailboxAutoresponderHandlerConfig
}

// RegisterMailboxAutoresponderRoutes mounts the endpoints under g.
// Called from routes_m65.go's registerAutoresponderRoutes.
func RegisterMailboxAutoresponderRoutes(g *gin.RouterGroup, cfg MailboxAutoresponderHandlerConfig) {
	if cfg.Autoresponders == nil {
		return
	}
	h := &mailboxAutoresponderHandler{cfg: cfg}
	g.GET("/mailboxes/:mbid/autoresponder", h.get)
	g.PUT("/mailboxes/:mbid/autoresponder", h.put)
	g.DELETE("/mailboxes/:mbid/autoresponder", h.del)
	g.GET("/domains/:id/autoresponders", h.listByDomain)
}

func (h *mailboxAutoresponderHandler) loadMailboxWithAuth(ctx context.Context, id string, claims *auth.AccessClaims) (*models.Mailbox, *models.Domain, error) {
	mb, err := h.cfg.Mailboxes.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	dom, err := h.cfg.Domains.FindByID(ctx, mb.DomainID)
	if err != nil {
		return nil, nil, err
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		return nil, nil, errMailboxForbidden
	}
	return mb, dom, nil
}

// listByDomain returns every autoresponder for the domain's mailboxes,
// keyed by mailbox id (GH #240). The Mailboxes tab fans this out once per
// domain rather than one GET per mailbox.
func (h *mailboxAutoresponderHandler) listByDomain(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	rows, err := h.cfg.Autoresponders.ListByDomain(ctx, dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make(map[string]autoresponderResponse, len(rows))
	for i := range rows {
		ar := rows[i]
		out[ar.MailboxID] = autoresponderResponse{
			MailboxID: ar.MailboxID,
			Enabled:   ar.Enabled,
			FromDate:  ar.FromDate,
			ToDate:    ar.ToDate,
			Subject:   ar.Subject,
			TextBody:  ar.TextBody,
			HTMLBody:  ar.HTMLBody,
			UpdatedAt: ar.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *mailboxAutoresponderHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, _, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		// Reuse mailbox handler's writeLoadErr via generic mapping.
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		if errors.Is(err, errMailboxForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	ar, err := h.cfg.Autoresponders.FindByMailboxID(ctx, mb.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Not set — return empty shape with defaults.
			c.JSON(http.StatusOK, autoresponderResponse{
				MailboxID: mb.ID,
				Enabled:   false,
				UpdatedAt: time.Time{},
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, autoresponderResponse{
		MailboxID: ar.MailboxID,
		Enabled:   ar.Enabled,
		FromDate:  ar.FromDate,
		ToDate:    ar.ToDate,
		Subject:   ar.Subject,
		TextBody:  ar.TextBody,
		HTMLBody:  ar.HTMLBody,
		UpdatedAt: ar.UpdatedAt,
	})
}

func (h *mailboxAutoresponderHandler) put(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, _, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		if errors.Is(err, errMailboxForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	var req autoresponderUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	email := mb.LocalPart + "@" + mustDomainName(ctx, h.cfg.Domains, mb.DomainID)
	ar, warning, err := autoresponderops.Set(ctx,
		autoresponderops.Deps{Autoresponders: h.cfg.Autoresponders},
		autoresponderops.SetInput{
			MailboxID:    mb.ID,
			MailboxEmail: email,
			Enabled:      req.Enabled,
			Subject:      req.Subject,
			TextBody:     req.TextBody,
			HTMLBody:     req.HTMLBody,
			FromDate:     req.FromDate,
			ToDate:       req.ToDate,
		}, h.pushAutoresponder)
	if err != nil {
		if errors.Is(err, autoresponderops.ErrContentRequired) || errors.Is(err, autoresponderops.ErrInvalidDateRange) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_autoresponder", "detail": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusOK, autoresponderResponse{
		MailboxID: ar.MailboxID,
		Enabled:   ar.Enabled,
		FromDate:  ar.FromDate,
		ToDate:    ar.ToDate,
		Subject:   ar.Subject,
		TextBody:  ar.TextBody,
		HTMLBody:  ar.HTMLBody,
		UpdatedAt: ar.UpdatedAt,
		Warning:   warning,
	})
}

// pushAutoresponder is the autoresponderops.PushFunc backed by the handler's
// agent client — a bounded best-effort call whose error becomes a Set warning.
func (h *mailboxAutoresponderHandler) pushAutoresponder(ctx context.Context, cmd string, params map[string]any) error {
	if h.cfg.Agent == nil {
		return nil
	}
	agentCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := h.cfg.Agent.Call(agentCtx, cmd, params)
	return err
}

func (h *mailboxAutoresponderHandler) del(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, _, err := h.loadMailboxWithAuth(ctx, c.Param("mbid"), claims)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		if errors.Is(err, errMailboxForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	email := mb.LocalPart + "@" + mustDomainName(ctx, h.cfg.Domains, mb.DomainID)
	if err := autoresponderops.Clear(ctx,
		autoresponderops.Deps{Autoresponders: h.cfg.Autoresponders},
		mb.ID, email, h.pushAutoresponder); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

// mustDomainName resolves a domain name by id; returns "" on error.
// Callers are best-effort pushes — Stalwart rejects empty domain names.
func mustDomainName(ctx context.Context, domains repository.DomainRepository, domainID string) string {
	if domains == nil {
		return ""
	}
	d, err := domains.FindByID(ctx, domainID)
	if err != nil || d == nil {
		return ""
	}
	return d.Name
}
