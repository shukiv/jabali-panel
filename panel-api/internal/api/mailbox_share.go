// mailbox_share.go — M6.5 Step 4 shared folders HTTP handlers.
//
// Wire contract:
//   GET    /mailboxes/:mbid/shares              list shares owned by this mailbox
//   POST   /mailboxes/:mbid/shares              share the mailbox with another mailbox of the same account (409 if already shared)
//   DELETE /mailboxes/:mbid/shares/:shareId     remove a share
//   GET    /mail/shares                         all shares for the caller's mailboxes
//
// Backed by JMAP Mailbox.shareWith. Create and delete apply the owner's share
// list to Stalwart through mailshareops; the reconciler's mailbox-share sweep
// retries a create whose apply failed.

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailshareops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type MailboxShareHandlerConfig struct {
	Mailboxes     repository.MailboxRepository
	Domains       repository.DomainRepository
	MailboxShares repository.MailboxShareRepository
	Agent         agent.AgentInterface
}

type shareResponse struct {
	ID                     string        `json:"id"`
	OwnerMailboxID         string        `json:"owner_mailbox_id"`
	OwnerMailboxEmail      string        `json:"owner_mailbox_email,omitempty"`
	SharedWithMailboxID    string        `json:"shared_with_mailbox_id"`
	SharedWithMailboxEmail string        `json:"shared_with_mailbox_email,omitempty"`
	Rights                 models.Rights `json:"rights"`
	CreatedAt              string        `json:"created_at"`
	// Warning is set when the share was saved but Stalwart did not accept it
	// yet (code convergence_failed); the reconciler retries it.
	Warning *forwarderWarning `json:"warning,omitempty"`
}

type shareCreateRequest struct {
	SharedWithMailboxID string        `json:"shared_with_mailbox_id"`
	Rights              models.Rights `json:"rights"`
}

type shareHandler struct {
	cfg MailboxShareHandlerConfig
}

func RegisterMailboxShareRoutes(g *gin.RouterGroup, cfg MailboxShareHandlerConfig) {
	if cfg.MailboxShares == nil {
		return
	}
	h := &shareHandler{cfg: cfg}
	g.GET("/mailboxes/:mbid/shares", h.list)
	g.POST("/mailboxes/:mbid/shares", h.create)
	g.DELETE("/mailboxes/:mbid/shares/:shareId", h.del)
	g.GET("/mail/shares", h.listAllForUser)
}

func (h *shareHandler) loadMailbox(ctx context.Context, id string, claims *auth.AccessClaims) (*models.Mailbox, *models.Domain, error) {
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

func (h *shareHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, _, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	shares, total, err := h.cfg.MailboxShares.FindByOwnerID(ctx, mb.ID, repository.ListOptions{Limit: 200})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	mbByID, domByID := h.shareRowMaps(ctx, shares)
	items := make([]shareResponse, 0, len(shares))
	for _, s := range shares {
		items = append(items, resolveShareFromMaps(s, mbByID, domByID))
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": 1, "page_size": 200})
}

func (h *shareHandler) listAllForUser(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	page, pageSize := paginationParams(c)
	opts := repository.ListOptions{Offset: (page - 1) * pageSize, Limit: pageSize}

	// JAB-107: scope to the tenant in SQL (owner mailbox -> domain -> user) so
	// every one of their shares is returned regardless of the global row count.
	// The old global newest-500 window filtered in memory hid a tenant's shares
	// past 500 total. Admins keep the cross-tenant view via ListAll.
	var (
		shares []models.MailboxShare
		total  int64
		err    error
	)
	if claims.IsAdmin {
		shares, total, err = h.cfg.MailboxShares.ListAll(ctx, opts)
	} else {
		shares, total, err = h.cfg.MailboxShares.ListByUserID(ctx, claims.UserID, opts)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// JAB-147: batch-load owner/target mailboxes + their domains once.
	mbByID, domByID := h.shareRowMaps(ctx, shares)
	items := make([]shareResponse, 0, len(shares))
	for _, s := range shares {
		owner := mbByID[s.OwnerMailboxID]
		if owner == nil {
			continue
		}
		dom := domByID[owner.DomainID]
		if dom == nil {
			continue
		}
		if !claims.IsAdmin && dom.UserID != claims.UserID {
			continue // defense-in-depth; ListByUserID already enforces this
		}
		items = append(items, resolveShareFromMaps(s, mbByID, domByID))
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": page, "page_size": pageSize})
}

func (h *shareHandler) create(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	owner, _, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	var req shareCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.SharedWithMailboxID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	res, err := mailshareops.Create(ctx, h.deps(), owner, req.SharedWithMailboxID, req.Rights, "m6.5")
	if err != nil {
		switch {
		case errors.Is(err, mailshareops.ErrTargetNotFound):
			// Also a target in another account: the same answer as a
			// missing one, so other tenants' mailboxes cannot be probed.
			c.JSON(http.StatusBadRequest, gin.H{"error": "target_not_found"})
		case errors.Is(err, mailshareops.ErrSelfShare):
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot_share_with_self"})
		case errors.Is(err, mailshareops.ErrNoRights):
			c.JSON(http.StatusBadRequest, gin.H{"error": "rights_required"})
		case errors.Is(err, mailshareops.ErrAlreadyShared):
			c.JSON(http.StatusConflict, gin.H{"error": "already_shared"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}
	resp := h.resolve(ctx, *res.Share)
	if res.ApplyErr != nil {
		// Saved, but not live on Stalwart yet; the reconciler retries it. The
		// detail is this tenant's own mailbox convergence error on an
		// owner-scoped endpoint (the forwarder create does the same).
		resp.Warning = &forwarderWarning{Code: "convergence_failed", Detail: res.ApplyErr.Error()}
	}
	c.JSON(http.StatusCreated, resp)
}

func (h *shareHandler) del(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, _, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	// Scope the delete to the authenticated mailbox. Authenticating :mbid and
	// then deleting by bare :shareId let any authenticated tenant delete
	// another tenant's share — passing their OWN mailbox as :mbid — and so
	// strip that share's JMAP shareWith on Stalwart. mailshareops.Delete only
	// acts on a share owned by mb.
	//
	// The revoke reaches Stalwart before the row goes: when Stalwart does not
	// accept it, the answer is 502 and the row (and the live share) stay.
	if err := mailshareops.Delete(ctx, h.deps(), mb.ID, c.Param("shareId")); err != nil {
		switch {
		case errors.Is(err, mailshareops.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		case errors.Is(err, mailshareops.ErrApply):
			c.JSON(http.StatusBadGateway, gin.H{"error": "share_apply_failed", "detail": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}
	c.JSON(http.StatusNoContent, nil)
}

func (h *shareHandler) deps() mailshareops.Deps {
	return mailshareops.Deps{
		Agent:     h.cfg.Agent,
		Mailboxes: h.cfg.Mailboxes,
		Domains:   h.cfg.Domains,
		Shares:    h.cfg.MailboxShares,
	}
}

// shareRowMaps batch-loads every owner + shared-with mailbox for the given
// shares and their domains into id→row maps (JAB-147), replacing the per-row
// FindByID pairs the list handlers used to run.
func (h *shareHandler) shareRowMaps(ctx context.Context, shares []models.MailboxShare) (map[string]*models.Mailbox, map[string]*models.Domain) {
	mbIDs := make([]string, 0, len(shares)*2)
	for _, s := range shares {
		mbIDs = append(mbIDs, s.OwnerMailboxID, s.SharedWithMailboxID)
	}
	mbByID := mailboxMapByID(ctx, h.cfg.Mailboxes, mbIDs)
	domIDs := make([]string, 0, len(mbByID))
	for _, mb := range mbByID {
		domIDs = append(domIDs, mb.DomainID)
	}
	return mbByID, domainMapByID(ctx, h.cfg.Domains, domIDs)
}

// resolveShareFromMaps builds a shareResponse from pre-batched maps (no query).
func resolveShareFromMaps(s models.MailboxShare, mbByID map[string]*models.Mailbox, domByID map[string]*models.Domain) shareResponse {
	resp := shareResponse{
		ID:                  s.ID,
		OwnerMailboxID:      s.OwnerMailboxID,
		SharedWithMailboxID: s.SharedWithMailboxID,
		Rights:              s.Rights,
		CreatedAt:           s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if owner := mbByID[s.OwnerMailboxID]; owner != nil {
		if dom := domByID[owner.DomainID]; dom != nil {
			resp.OwnerMailboxEmail = owner.LocalPart + "@" + dom.Name
		}
	}
	if target := mbByID[s.SharedWithMailboxID]; target != nil {
		if dom := domByID[target.DomainID]; dom != nil {
			resp.SharedWithMailboxEmail = target.LocalPart + "@" + dom.Name
		}
	}
	return resp
}

func (h *shareHandler) resolve(ctx context.Context, s models.MailboxShare) shareResponse {
	resp := shareResponse{
		ID:                  s.ID,
		OwnerMailboxID:      s.OwnerMailboxID,
		SharedWithMailboxID: s.SharedWithMailboxID,
		Rights:              s.Rights,
		CreatedAt:           s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if owner, err := h.cfg.Mailboxes.FindByID(ctx, s.OwnerMailboxID); err == nil {
		resp.OwnerMailboxEmail = owner.LocalPart + "@" + mustDomainName(ctx, h.cfg.Domains, owner.DomainID)
	}
	if target, err := h.cfg.Mailboxes.FindByID(ctx, s.SharedWithMailboxID); err == nil {
		resp.SharedWithMailboxEmail = target.LocalPart + "@" + mustDomainName(ctx, h.cfg.Domains, target.DomainID)
	}
	return resp
}

func (h *shareHandler) writeErr(c *gin.Context, err error) {
	if isNotFound(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	if errors.Is(err, errMailboxForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
}
