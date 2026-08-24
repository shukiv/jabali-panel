// mailbox_forwarder.go — M6.5 Step 5 forwarders (alias + external).
//
// Wire contract:
//   GET    /mail/forwarders                    all forwarders for caller's mailboxes
//   POST   /mailboxes/:mbid/forwarders         create forwarder (alias or external)
//   DELETE /forwarders/:id                     delete
//
// Type=alias → x:EmailAlias on UserAccount.aliases.
// Type=external → entry in jabali-fwds sieve script (concatenated per mailbox).

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

type MailboxForwarderHandlerConfig struct {
	Mailboxes  repository.MailboxRepository
	Domains    repository.DomainRepository
	Forwarders repository.EmailForwarderRepository
	Agent      agent.AgentInterface
}

type forwarderResponse struct {
	ID           string `json:"id"`
	MailboxID    string `json:"mailbox_id"`
	MailboxEmail string `json:"mailbox_email"`
	DomainID     string `json:"domain_id"`
	DomainName   string `json:"domain_name"`
	Type         string `json:"type"`
	LocalPart    string `json:"local_part,omitempty"`
	Target       string `json:"target"`
	KeepCopy     bool   `json:"keep_copy"`
	Enabled      bool   `json:"enabled"`
	CreatedAt    string `json:"created_at"`
}

type forwarderCreateRequest struct {
	Type      string `json:"type"`       // alias | external
	LocalPart string `json:"local_part"` // for alias
	Target    string `json:"target"`     // for external (email); for alias (mailbox@domain)
	KeepCopy  bool   `json:"keep_copy"`  // external only: keep a copy in the mailbox
}

type forwarderHandler struct {
	cfg MailboxForwarderHandlerConfig
}

func RegisterMailboxForwarderRoutes(g *gin.RouterGroup, cfg MailboxForwarderHandlerConfig) {
	if cfg.Forwarders == nil {
		return
	}
	h := &forwarderHandler{cfg: cfg}
	g.GET("/mail/forwarders", h.listAll)
	g.POST("/mailboxes/:mbid/forwarders", h.create)
	g.DELETE("/forwarders/:id", h.del)
	// Domain-scoped (NULL mailbox) forwarders — M35 DA importer leaves
	// /etc/virtual/<dom>/aliases entries here. Read-only for now;
	// Stalwart push deferred to a follow-up reconciler phase.
	g.GET("/mail/forwarders/domain-scoped", h.listDomainScoped)
}

func (h *forwarderHandler) loadMailbox(ctx context.Context, id string, claims *auth.AccessClaims) (*models.Mailbox, *models.Domain, error) {
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

func (h *forwarderHandler) listAll(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	page, pageSize := paginationParams(c)
	opts := repository.ListOptions{Offset: (page - 1) * pageSize, Limit: pageSize}

	// JAB-107: scope to the tenant in SQL so every one of their forwarders is
	// returned regardless of the global row count (the old global newest-500
	// window hid a tenant's rows past 500 total). Admins keep the cross-tenant
	// view via ListAll.
	var (
		fwds  []models.EmailForwarder
		total int64
		err   error
	)
	if claims.IsAdmin {
		fwds, total, err = h.cfg.Forwarders.ListAll(ctx, opts)
	} else {
		fwds, total, err = h.cfg.Forwarders.ListByUserID(ctx, claims.UserID, opts)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// JAB-147: batch-load the mailboxes + domains once instead of a per-row
	// FindByID pair (N+1). Build id→row maps, then resolve from memory.
	mbIDs := make([]string, 0, len(fwds))
	domIDs := make([]string, 0, len(fwds))
	for _, f := range fwds {
		if f.MailboxID != nil {
			mbIDs = append(mbIDs, *f.MailboxID)
		}
		domIDs = append(domIDs, f.DomainID)
	}
	mbByID := mailboxMapByID(ctx, h.cfg.Mailboxes, mbIDs)
	domByID := domainMapByID(ctx, h.cfg.Domains, domIDs)

	items := make([]forwarderResponse, 0, len(fwds))
	for _, f := range fwds {
		if f.MailboxID == nil {
			// Domain-scoped DA-imported forwarder; M65 mailbox-keyed UI
			// can't render it. Skip — future domain-scoped endpoint will
			// surface these. (ListByUserID already excludes these; only the
			// admin ListAll path reaches here.)
			continue
		}
		mb := mbByID[*f.MailboxID]
		dom := domByID[f.DomainID]
		if mb == nil || dom == nil {
			continue
		}
		if !claims.IsAdmin && dom.UserID != claims.UserID {
			continue // defense-in-depth; ListByUserID already enforces this
		}
		items = append(items, h.resolve(ctx, f, mb, dom))
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": page, "page_size": pageSize})
}

// applyForwarders re-lists a mailbox's forwarders and pushes the full
// desired state to Stalwart via forwarder.apply. Best-effort: the DB is
// truth, so a transient agent failure is logged and retried on the next
// mutation. GH #237 — until this was added, forwarders were written to the
// DB but NEVER converged: the only caller of forwarder.apply was the
// dormant phases.forwardersPhase (registered via no init(), reached only by
// the never-called ReconcileMailboxAll). Mirrors the inline autoresponder.set
// dispatch.
func (h *forwarderHandler) applyForwarders(ctx context.Context, mb *models.Mailbox, dom *models.Domain) {
	if h.cfg.Agent == nil {
		return
	}
	rows, _, err := h.cfg.Forwarders.ListByMailboxID(ctx, mb.ID, repository.ListOptions{Limit: 500})
	if err != nil {
		slog.Warn("forwarder.apply: list failed", "mailbox_id", mb.ID, "err", err)
		return
	}
	aliases := []map[string]string{}
	externals := []map[string]any{}
	for _, f := range rows {
		if !f.Enabled {
			continue
		}
		switch f.Type {
		case "alias":
			if f.LocalPart != nil {
				aliases = append(aliases, map[string]string{"local_part": *f.LocalPart})
			}
		case "external":
			externals = append(externals, map[string]any{"target": f.Target, "keep_copy": f.KeepCopy})
		}
	}
	params := map[string]any{
		"mailbox_email": mb.LocalPart + "@" + dom.Name,
		"aliases":       aliases,
		"externals":     externals,
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := h.cfg.Agent.Call(cctx, "forwarder.apply", params); err != nil {
		slog.Warn("forwarder.apply: agent push failed", "mailbox_id", mb.ID, "err", err)
	}
}

func (h *forwarderHandler) create(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	mb, dom, err := h.loadMailbox(ctx, c.Param("mbid"), claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	var req forwarderCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if req.Type != "alias" && req.Type != "external" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_type"})
		return
	}
	if req.Type == "alias" && req.LocalPart == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "alias_requires_local_part"})
		return
	}
	if req.Target == "" {
		// External default target is the mailbox itself. For an ALIAS the target
		// column is unused at apply (delivery is by local_part -> mailbox), but it
		// is part of uq_external_forward(mailbox_id,type,target) — every alias used
		// to default to the SAME mailbox address, so the 2nd alias collided on that
		// key and failed (GH #280). Store the alias's OWN address, which is unique
		// per alias, so a mailbox can hold many aliases.
		if req.Type == "alias" {
			req.Target = models.AliasForwarderTarget(req.LocalPart, dom.Name)
		} else {
			req.Target = mb.LocalPart + "@" + dom.Name
		}
	}
	f := &models.EmailForwarder{
		ID:        ids.NewULID(),
		MailboxID: &mb.ID,
		DomainID:  dom.ID,
		Type:      req.Type,
		Target:    req.Target,
		Enabled:   true,
		ManagedBy: "m6.5",
	}
	if req.Type == "alias" {
		lp := req.LocalPart
		f.LocalPart = &lp
	}
	if req.Type == "external" {
		f.KeepCopy = req.KeepCopy
	}
	if err := h.cfg.Forwarders.Create(ctx, f); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.applyForwarders(ctx, mb, dom)
	c.JSON(http.StatusCreated, h.resolve(ctx, *f, mb, dom))
}

func (h *forwarderHandler) del(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	f, err := h.cfg.Forwarders.FindByID(ctx, c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if f.MailboxID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_scoped_forwarder"})
		return
	}
	mb, dom, err := h.loadMailbox(ctx, *f.MailboxID, claims)
	if err != nil {
		h.writeErr(c, err)
		return
	}
	if err := h.cfg.Forwarders.Delete(ctx, f.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.applyForwarders(ctx, mb, dom)
	c.JSON(http.StatusNoContent, nil)
}

func (h *forwarderHandler) resolve(_ context.Context, f models.EmailForwarder, mb *models.Mailbox, dom *models.Domain) forwarderResponse {
	lp := ""
	if f.LocalPart != nil {
		lp = *f.LocalPart
	}
	return forwarderResponse{
		ID: f.ID,
		MailboxID: func() string {
			if f.MailboxID != nil {
				return *f.MailboxID
			}
			return ""
		}(),
		MailboxEmail: mb.LocalPart + "@" + dom.Name,
		DomainID:     f.DomainID,
		DomainName:   dom.Name,
		Type:         f.Type,
		LocalPart:    lp,
		Target:       f.Target,
		KeepCopy:     f.KeepCopy,
		Enabled:      f.Enabled,
		CreatedAt:    f.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *forwarderHandler) writeErr(c *gin.Context, err error) {
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

// domainScopedForwarderResponse is the row shape for the NULL-
// mailbox list. No mailbox_email — the entry redirects without a
// source account.
type domainScopedForwarderResponse struct {
	ID         string `json:"id"`
	DomainID   string `json:"domain_id"`
	DomainName string `json:"domain_name"`
	Type       string `json:"type"`
	LocalPart  string `json:"local_part"`
	Target     string `json:"target"`
	Enabled    bool   `json:"enabled"`
	ManagedBy  string `json:"managed_by"`
	CreatedAt  string `json:"created_at"`
}

func (h *forwarderHandler) listDomainScoped(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	fwds, _, err := h.cfg.Forwarders.ListAll(ctx, repository.ListOptions{Limit: 1000})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	items := make([]domainScopedForwarderResponse, 0)
	for _, f := range fwds {
		if f.MailboxID != nil {
			continue
		}
		dom, err := h.cfg.Domains.FindByID(ctx, f.DomainID)
		if err != nil {
			continue
		}
		if !claims.IsAdmin && dom.UserID != claims.UserID {
			continue
		}
		lp := ""
		if f.LocalPart != nil {
			lp = *f.LocalPart
		}
		items = append(items, domainScopedForwarderResponse{
			ID: f.ID, DomainID: f.DomainID, DomainName: dom.Name,
			Type: f.Type, LocalPart: lp, Target: f.Target,
			Enabled: f.Enabled, ManagedBy: f.ManagedBy,
			CreatedAt: f.CreatedAt.Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": int64(len(items)), "page": 1, "page_size": len(items)})
}
