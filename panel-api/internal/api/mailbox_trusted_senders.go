// mailbox_trusted_senders.go — GH #2017: senders a mailbox trusts.
//
// Wire contract (owner-or-admin, same auth as send-as and forwarders):
//   GET    /mailboxes/:mbid/trusted-senders       the mailbox's list
//   POST   /mailboxes/:mbid/trusted-senders       add {address}
//   DELETE /mailboxes/:mbid/trusted-senders/:id   remove one
//
// Mail from a trusted sender is not treated as spam when it passes SPF or
// DMARC. The rows are the truth; after a change the handler sends the
// mailbox's whole list to the agent (trustedsenders.Push), which writes it
// into Stalwart as contact cards. The reconciler sends it again later when
// that push fails, and every hour.

package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/trustedsenders"
)

// trustedSendersPushTimeout stays under the server's 30 s write timeout.
const trustedSendersPushTimeout = 20 * time.Second

type MailboxTrustedSenderHandlerConfig struct {
	Mailboxes      repository.MailboxRepository
	Domains        repository.DomainRepository
	TrustedSenders repository.MailboxTrustedSenderRepository
	Agent          agent.AgentInterface
}

type trustedSenderHandler struct {
	cfg MailboxTrustedSenderHandlerConfig
}

type trustedSenderResponse struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	CreatedAt string `json:"created_at"`
	// Warning is set when the sender was saved but the mail server has not
	// taken it yet; the panel retries.
	Warning string `json:"warning,omitempty"`
}

type trustedSenderCreateRequest struct {
	Address string `json:"address"`
}

func RegisterMailboxTrustedSenderRoutes(g *gin.RouterGroup, cfg MailboxTrustedSenderHandlerConfig) {
	if cfg.TrustedSenders == nil {
		return
	}
	h := &trustedSenderHandler{cfg: cfg}
	g.GET("/mailboxes/:mbid/trusted-senders", h.list)
	g.POST("/mailboxes/:mbid/trusted-senders", h.add)
	g.DELETE("/mailboxes/:mbid/trusted-senders/:id", h.del)
}

func (h *trustedSenderHandler) loadMailbox(ctx context.Context, id string, claims *auth.AccessClaims) (*models.Mailbox, error) {
	mb, err := h.cfg.Mailboxes.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	dom, err := h.cfg.Domains.FindByID(ctx, mb.DomainID)
	if err != nil {
		return nil, err
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		return nil, errMailboxForbidden
	}
	return mb, nil
}

func trustedSenderJSON(row models.MailboxTrustedSender) trustedSenderResponse {
	return trustedSenderResponse{ID: row.ID, Address: row.Address, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339)}
}

func (h *trustedSenderHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	mb, err := h.loadMailbox(ctx, c.Param("mbid"), ginctx.Claims(c))
	if err != nil {
		h.writeErr(c, err)
		return
	}
	rows, err := h.cfg.TrustedSenders.ListByMailbox(ctx, mb.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]trustedSenderResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, trustedSenderJSON(row))
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out), "max": trustedsenders.MaxPerMailbox})
}

func (h *trustedSenderHandler) add(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	var req trustedSenderCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	address, err := mailaddr.CanonicaliseSender(strings.TrimSpace(req.Address))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "invalid_address",
			"detail": "Enter one full address, like name@example.com. Before the @, use only letters, digits and . _ - + =",
		})
		return
	}
	n, err := h.cfg.TrustedSenders.CountByMailbox(ctx, mb.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if n >= trustedsenders.MaxPerMailbox {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "too_many_trusted_senders",
			"detail": "A mailbox can trust at most 500 senders.",
		})
		return
	}
	row := &models.MailboxTrustedSender{ID: ids.NewULID(), MailboxID: mb.ID, Address: address}
	if err := h.cfg.TrustedSenders.Create(ctx, row); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "already_trusted"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	slog.Info("trusted_senders.add", "mailbox", mb.EmailCached, "sender", address, "actor", claims.UserID)
	resp := trustedSenderJSON(*row)
	if err := h.push(ctx, mb, ""); err != nil {
		slog.Warn("trusted_senders.add: mail server push failed; the reconciler retries", "mailbox", mb.EmailCached, "err", err)
		resp.Warning = "Saved. The mail server has not taken it yet; the panel retries within a few minutes."
	}
	c.JSON(http.StatusCreated, resp)
}

func (h *trustedSenderHandler) del(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	id := c.Param("id")
	rows, err := h.cfg.TrustedSenders.ListByMailbox(ctx, mb.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	found := false
	for _, row := range rows {
		if row.ID == id {
			found = true
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	// The mail server first: once the row is gone, a mailbox with no rows
	// left is not swept, so a card the push missed would stay trusted.
	if err := h.push(ctx, mb, id); err != nil {
		slog.Warn("trusted_senders.remove: mail server push failed", "mailbox", mb.EmailCached, "err", err)
		c.JSON(http.StatusBadGateway, gin.H{
			"error":  "mail_server_unavailable",
			"detail": "The mail server could not remove the sender, so it is still trusted. Try again.",
		})
		return
	}
	if err := h.cfg.TrustedSenders.Delete(ctx, mb.ID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	slog.Info("trusted_senders.remove", "mailbox", mb.EmailCached, "id", id, "actor", claims.UserID)
	c.Status(http.StatusNoContent)
}

// push sends mb's stored list, without the row skipID when it is set, to the
// mail server. Without an agent there is no mail server to update.
func (h *trustedSenderHandler) push(ctx context.Context, mb *models.Mailbox, skipID string) error {
	if h.cfg.Agent == nil {
		return nil
	}
	rows, err := h.cfg.TrustedSenders.ListByMailbox(ctx, mb.ID)
	if err != nil {
		return err
	}
	keep := rows[:0:0]
	for _, row := range rows {
		if row.ID != skipID {
			keep = append(keep, row)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, trustedSendersPushTimeout)
	defer cancel()
	return trustedsenders.Push(cctx, h.cfg.Agent, mb.EmailCached, trustedsenders.Addresses(keep))
}

func (h *trustedSenderHandler) writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errMailboxForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}
