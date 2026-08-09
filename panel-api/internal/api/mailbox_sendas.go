// mailbox_sendas.go — GH #347 send-as delegation (Step 4).
//
// Wire contract (owner-or-admin, same auth as forwarders):
//   GET    /mailboxes/:mbid/send-as                list grantors this mailbox may send as
//   POST   /mailboxes/:mbid/send-as                add a grantor {grantor_mailbox_id | grantor_email}
//   DELETE /mailboxes/:mbid/send-as/:grantorId     remove a grantor (by grantor mailbox id)
//
// The delegate (:mbid) may send FROM each grantor's address without receiving the
// grantor's mail. After every change we recompute the FULL pair set and push it to
// the agent (mail.sendas.reconcile), which materialises it into Stalwart's
// mustMatchSender expression (Mechanism C, project_jab347_spike_verdict).

package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type MailboxSendAsHandlerConfig struct {
	Mailboxes       repository.MailboxRepository
	Domains         repository.DomainRepository
	SendDelegations repository.MailboxSendDelegationRepository
	Agent           agent.AgentInterface
}

type sendAsHandler struct {
	cfg MailboxSendAsHandlerConfig
}

type sendAsGrantorResponse struct {
	ID                string `json:"id"` // delegation row id
	GrantorMailboxID  string `json:"grantor_mailbox_id"`
	GrantorEmail      string `json:"grantor_email"`
	DelegateMailboxID string `json:"delegate_mailbox_id"`
	DelegateEmail     string `json:"delegate_email"`
	CreatedAt         string `json:"created_at"`
}

type sendAsCreateRequest struct {
	GrantorMailboxID string `json:"grantor_mailbox_id"`
	GrantorEmail     string `json:"grantor_email"`
}

func RegisterMailboxSendAsRoutes(g *gin.RouterGroup, cfg MailboxSendAsHandlerConfig) {
	if cfg.SendDelegations == nil {
		return
	}
	h := &sendAsHandler{cfg: cfg}
	g.GET("/mailboxes/:mbid/send-as", h.list)
	g.POST("/mailboxes/:mbid/send-as", h.add)
	g.DELETE("/mailboxes/:mbid/send-as/:grantorId", h.del)
}

func (h *sendAsHandler) loadMailbox(ctx context.Context, id string, claims *auth.AccessClaims) (*models.Mailbox, *models.Domain, error) {
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

func (h *sendAsHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	delegate, _, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	rows, err := h.cfg.SendDelegations.ListByDelegate(ctx, delegate.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Batch-load the grantor mailboxes in one query instead of a FindByID per
	// row (JAB-147 pattern; mailboxMapByID already exists for exactly this).
	grantorIDs := make([]string, 0, len(rows))
	for _, d := range rows {
		grantorIDs = append(grantorIDs, d.GrantorMailboxID)
	}
	grantorByID := mailboxMapByID(ctx, h.cfg.Mailboxes, grantorIDs)

	out := make([]sendAsGrantorResponse, 0, len(rows))
	for _, d := range rows {
		grantorEmail := ""
		if gr := grantorByID[d.GrantorMailboxID]; gr != nil {
			grantorEmail = gr.EmailCached
		}
		out = append(out, sendAsGrantorResponse{
			ID:                d.ID,
			GrantorMailboxID:  d.GrantorMailboxID,
			GrantorEmail:      grantorEmail,
			DelegateMailboxID: delegate.ID,
			DelegateEmail:     delegate.EmailCached,
			CreatedAt:         d.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *sendAsHandler) add(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	delegate, delDom, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	var req sendAsCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	// Resolve the grantor mailbox by id or email.
	var grantor *models.Mailbox
	switch {
	case req.GrantorMailboxID != "":
		grantor, err = h.cfg.Mailboxes.FindByID(ctx, req.GrantorMailboxID)
	case req.GrantorEmail != "":
		grantor, err = h.cfg.Mailboxes.FindByEmail(ctx, req.GrantorEmail)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "grantor_required"})
		return
	}
	if err != nil || grantor == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "grantor_not_found"})
		return
	}

	// Policy: real, enabled mailboxes; grantor in the SAME domain as the delegate
	// (surfaced in the ADR); no self-delegation; no duplicates.
	if delegate.IsDisabled || grantor.IsDisabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mailbox_disabled"})
		return
	}
	if grantor.ID == delegate.ID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "self_delegation"})
		return
	}
	if grantor.DomainID != delDom.ID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "grantor_different_domain"})
		return
	}
	exists, exErr := h.cfg.SendDelegations.ExistsPair(ctx, delegate.ID, grantor.ID)
	if exErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "already_delegated"})
		return
	}

	row := &models.MailboxSendDelegation{
		ID:                ids.NewULID(),
		DelegateMailboxID: delegate.ID,
		GrantorMailboxID:  grantor.ID,
	}
	if cErr := h.cfg.SendDelegations.Create(ctx, row); cErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	slog.Info("sendas.add", "delegate", delegate.EmailCached, "grantor", grantor.EmailCached, "actor", claims.UserID)
	h.reconcile(ctx)
	c.JSON(http.StatusCreated, sendAsGrantorResponse{
		ID:                row.ID,
		GrantorMailboxID:  grantor.ID,
		GrantorEmail:      grantor.EmailCached,
		DelegateMailboxID: delegate.ID,
		DelegateEmail:     delegate.EmailCached,
		CreatedAt:         row.CreatedAt.UTC().Format(time.RFC3339),
	})
}

func (h *sendAsHandler) del(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	delegate, _, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	grantorID := c.Param("grantorId")
	if dErr := h.cfg.SendDelegations.DeleteByPair(ctx, delegate.ID, grantorID); dErr != nil {
		if errors.Is(dErr, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	slog.Info("sendas.remove", "delegate", delegate.EmailCached, "grantor_mailbox_id", grantorID, "actor", claims.UserID)
	h.reconcile(ctx)
	c.Status(http.StatusNoContent)
}

// reconcile recomputes the full delegation set and pushes it to the agent, which
// rebuilds Stalwart's mustMatchSender expression. Best-effort + full-replace: a
// failure is logged, and the next change (or an install/update run) self-heals.
func (h *sendAsHandler) reconcile(ctx context.Context) {
	if h.cfg.Agent == nil {
		return
	}
	pairs, err := h.cfg.SendDelegations.ListAllPairs(ctx)
	if err != nil {
		slog.Warn("sendas.reconcile: list pairs failed", "err", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := h.cfg.Agent.Call(cctx, "mail.sendas.reconcile", map[string]any{"pairs": pairs}); err != nil {
		slog.Warn("sendas.reconcile: agent push failed", "err", err)
	}
}

func (h *sendAsHandler) writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errMailboxForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}
