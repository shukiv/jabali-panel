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
	"fmt"
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
	if rErr := h.reconcile(ctx); rErr != nil {
		// GH #2267: a grant the mail server did not take can't be used, so
		// undo it. The tab then lists only what the server lets through.
		if dErr := h.cfg.SendDelegations.DeleteByPair(ctx, delegate.ID, grantor.ID); dErr != nil {
			slog.Warn("sendas.add: undo after a failed reconcile", "err", dErr)
		}
		h.writeNotApplied(c, rErr)
		return
	}
	slog.Info("sendas.add", "delegate", delegate.EmailCached, "grantor", grantor.EmailCached, "actor", claims.UserID)
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
	// Kept so the grant can be put back if the mail server refuses the removal.
	var removed *models.MailboxSendDelegation
	if rows, lErr := h.cfg.SendDelegations.ListByDelegate(ctx, delegate.ID); lErr == nil {
		for i := range rows {
			if rows[i].GrantorMailboxID == grantorID {
				removed = &rows[i]
				break
			}
		}
	}
	if dErr := h.cfg.SendDelegations.DeleteByPair(ctx, delegate.ID, grantorID); dErr != nil {
		if errors.Is(dErr, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if rErr := h.reconcile(ctx); rErr != nil {
		// GH #2267: the mailbox can still send as the address, so keep the
		// grant listed rather than show it removed.
		if removed != nil {
			if cErr := h.cfg.SendDelegations.Create(ctx, removed); cErr != nil {
				slog.Warn("sendas.remove: restore after a failed reconcile", "err", cErr)
			}
		}
		h.writeNotApplied(c, rErr)
		return
	}
	slog.Info("sendas.remove", "delegate", delegate.EmailCached, "grantor_mailbox_id", grantorID, "actor", claims.UserID)
	c.Status(http.StatusNoContent)
}

// reconcile recomputes the full delegation set and pushes it to the agent, which
// rebuilds Stalwart's mustMatchSender expression. Full-replace, so the next
// change or panel start also heals a push that was missed.
func (h *sendAsHandler) reconcile(ctx context.Context) error {
	if h.cfg.Agent == nil {
		return nil
	}
	pairs, err := h.cfg.SendDelegations.ListAllPairs(ctx)
	if err != nil {
		return fmt.Errorf("list pairs: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = h.cfg.Agent.Call(cctx, "mail.sendas.reconcile", map[string]any{"pairs": pairs})
	return err
}

// writeNotApplied answers a change the mail server did not take. The reason
// goes to the panel log only: the agent's message can carry stalwart-cli
// output (JAB-114).
func (h *sendAsHandler) writeNotApplied(c *gin.Context, err error) {
	logAgentError(c, "sendas_not_applied", err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"error":  "sendas_not_applied",
		"detail": "The mail server did not take the change, so nothing was changed. Try again in a minute. If it keeps failing, the panel log says why.",
	})
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
