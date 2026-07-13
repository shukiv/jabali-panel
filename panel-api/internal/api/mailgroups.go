package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mailgroups.go — M51 mail groups (issue #201, ADR-0132).
//
// A mail group is a domain-scoped principal owning shared resources
// (mailbox / calendar / address book / files). Members are mailboxes;
// membership grants every member access in their own webmail session.
//
// DB-as-truth (ADR-0045): panel-API writes the rows + computes membership
// diffs; the agent projects them into the Stalwart registry. The agent
// call carries emails (the agent resolves registry ids), so the panel is
// the single owner of which mailboxes belong to a group.

const mailGroupAgentTimeout = 30 * time.Second

// MailGroupHandlerConfig plugs the mail-group HTTP handlers into the router.
type MailGroupHandlerConfig struct {
	Groups    repository.MailGroupRepository
	Mailboxes repository.MailboxRepository
	Domains   repository.DomainRepository
	Agent     agent.AgentInterface
}

// RegisterMailGroupRoutes mounts the mail-group endpoints under g:
//
//   - GET    /domains/:id/mailgroups        list groups in a domain
//   - POST   /domains/:id/mailgroups        create a group
//   - GET    /admin/mailgroups              server-wide list (admin)
//   - GET    /mailgroups/:gid               group detail + members
//   - PATCH  /mailgroups/:gid               update meta + resource toggles
//   - PUT    /mailgroups/:gid/members       replace the member set
//   - DELETE /mailgroups/:gid               destroy the group
func RegisterMailGroupRoutes(g *gin.RouterGroup, cfg MailGroupHandlerConfig) {
	h := &mailGroupHandler{cfg: cfg}

	g.GET("/domains/:id/mailgroups", h.list)
	g.POST("/domains/:id/mailgroups", h.create)
	g.GET("/domains/:id/mailbox-group-memberships", h.listMemberships)

	g.GET("/admin/mailgroups", middleware.RequireAdmin(), h.listAllAdmin)

	grp := g.Group("/mailgroups")
	grp.GET("/:gid", h.get)
	grp.PATCH("/:gid", h.update)
	grp.PUT("/:gid/members", h.setMembers)
	grp.POST("/:gid/members/:mbid", h.addMember)
	grp.DELETE("/:gid/members/:mbid", h.removeMember)
	grp.DELETE("/:gid", h.delete)
}

type mailGroupHandler struct{ cfg MailGroupHandlerConfig }

// ---- Request / response types ----

type createMailGroupRequest struct {
	Name           string `json:"name" binding:"required"` // local part; email = name@domain
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	GroupKind      string `json:"group_kind"`
	InternalOnly   bool   `json:"internal_only"`
	HasMailbox     *bool  `json:"has_mailbox"`
	HasCalendar    *bool  `json:"has_calendar"`
	HasAddressbook *bool  `json:"has_addressbook"`
	HasFiles       *bool  `json:"has_files"`
}

type updateMailGroupRequest struct {
	DisplayName    *string `json:"display_name"`
	Description    *string `json:"description"`
	HasMailbox     *bool   `json:"has_mailbox"`
	HasCalendar    *bool   `json:"has_calendar"`
	HasAddressbook *bool   `json:"has_addressbook"`
	HasFiles       *bool   `json:"has_files"`
	InternalOnly   *bool   `json:"internal_only"` // GH #348
}

type setMailGroupMembersRequest struct {
	MailboxIDs []string `json:"mailbox_ids"`
}

type mailGroupResponse struct {
	ID             string    `json:"id"`
	DomainID       string    `json:"domain_id"`
	LocalPart      string    `json:"local_part"`
	Email          string    `json:"email"`
	DisplayName    string    `json:"display_name"`
	Description    string    `json:"description"`
	GroupKind      string    `json:"group_kind"`
	HasMailbox     bool      `json:"has_mailbox"`
	HasCalendar    bool      `json:"has_calendar"`
	HasAddressbook bool      `json:"has_addressbook"`
	HasFiles       bool      `json:"has_files"`
	InternalOnly   bool      `json:"internal_only"` // GH #348
	MemberCount    int64     `json:"member_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type mailGroupMemberDTO struct {
	MailboxID string `json:"mailbox_id"`
	Email     string `json:"email"`
}

type mailGroupDetailResponse struct {
	mailGroupResponse
	Members []mailGroupMemberDTO `json:"members"`
}

func toMailGroupResponse(g models.MailGroup, memberCount int64) mailGroupResponse {
	return mailGroupResponse{
		ID:             g.ID,
		DomainID:       g.DomainID,
		LocalPart:      g.LocalPart,
		Email:          g.EmailCached,
		DisplayName:    g.DisplayName,
		Description:    g.Description,
		GroupKind:      g.GroupKind,
		HasMailbox:     g.HasMailbox,
		HasCalendar:    g.HasCalendar,
		HasAddressbook: g.HasAddressbook,
		HasFiles:       g.HasFiles,
		InternalOnly:   g.InternalOnly,
		MemberCount:    memberCount,
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
	}
}

// normalizeGroupKind defaults blank/unknown kinds to "resource" (the M51
// behaviour) and only accepts the two valid values.
func normalizeGroupKind(k string) string {
	if k == "distribution" {
		return "distribution"
	}
	return "resource"
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// ---- Handlers ----

func (h *mailGroupHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dom, ok := h.loadDomainWithAuth(c, c.Param("id"), claims)
	if !ok {
		return
	}
	rows, err := h.cfg.Groups.ListByDomainID(ctx, dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := make([]mailGroupResponse, len(rows))
	for i, r := range rows {
		out[i] = toMailGroupResponse(r.MailGroup, r.MemberCount)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
}

func (h *mailGroupHandler) listAllAdmin(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.cfg.Groups.ListAllWithDomain(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	type adminRow struct {
		mailGroupResponse
		DomainName   string `json:"domain_name"`
		UserUsername string `json:"user_username"`
	}
	out := make([]adminRow, len(rows))
	for i, r := range rows {
		out[i] = adminRow{
			mailGroupResponse: toMailGroupResponse(r.MailGroup, r.MemberCount),
			DomainName:        r.DomainName,
			UserUsername:      r.UserUsername,
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
}

// listMemberships returns, per mailbox in the domain, the groups it belongs to
// (#238 — the mail users "Groups" column). Shape: { data: { <mailbox_id>:
// [{group_id, group_name, group_email}] } }.
func (h *mailGroupHandler) listMemberships(c *gin.Context) {
	claims := ginctx.Claims(c)
	dom, ok := h.loadDomainWithAuth(c, c.Param("id"), claims)
	if !ok {
		return
	}
	rows, err := h.cfg.Groups.ListMembershipsByDomain(c.Request.Context(), dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	out := map[string][]repository.MailboxGroupMembership{}
	for _, r := range rows {
		out[r.MailboxID] = append(out[r.MailboxID], r)
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

func (h *mailGroupHandler) create(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	dom, ok := h.loadDomainWithAuth(c, c.Param("id"), claims)
	if !ok {
		return
	}
	if !dom.EmailEnabled {
		c.JSON(http.StatusConflict, gin.H{"error": "email_not_enabled",
			"detail": "enable email on the domain before creating groups"})
		return
	}

	var req createMailGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	canonLocal, _, err := mailaddr.Canonicalise(req.Name + "@" + dom.Name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name", "detail": err.Error()})
		return
	}
	// A group must not collide with a mailbox or another group on the same
	// address (both are recipients in Stalwart's directory).
	if exists, err := h.cfg.Groups.ExistsByDomainAndLocalPart(ctx, dom.ID, canonLocal); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	} else if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "group_exists"})
		return
	}
	if exists, err := h.cfg.Mailboxes.ExistsByDomainAndLocalPart(ctx, dom.ID, canonLocal); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	} else if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "address_taken",
			"detail": "a mailbox already uses this address"})
		return
	}

	now := time.Now().UTC()
	g := &models.MailGroup{
		ID:             ids.NewULID(),
		DomainID:       dom.ID,
		LocalPart:      canonLocal,
		DisplayName:    strings.TrimSpace(req.DisplayName),
		Description:    strings.TrimSpace(req.Description),
		GroupKind:      normalizeGroupKind(req.GroupKind),
		HasMailbox:     boolOr(req.HasMailbox, true),
		HasCalendar:    boolOr(req.HasCalendar, true),
		HasAddressbook: boolOr(req.HasAddressbook, true),
		HasFiles:       boolOr(req.HasFiles, true),
		InternalOnly:   req.InternalOnly,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := h.cfg.Groups.Create(ctx, g); err != nil {
		if isConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "group_exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	email := canonLocal + "@" + dom.Name
	h.notifyAgent(ctx, "mailgroup.apply", map[string]any{
		"email":         email,
		"display_name":  g.DisplayName,
		"description":   g.Description,
		"internal_only": g.InternalOnly,
		"has_files":     g.HasFiles,
	})

	g.EmailCached = email
	c.JSON(http.StatusCreated, toMailGroupResponse(*g, 0))
}

func (h *mailGroupHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	g, _, ok := h.loadGroupWithAuth(c, c.Param("gid"), claims)
	if !ok {
		return
	}
	memberIDs, err := h.cfg.Groups.ListMemberMailboxIDs(ctx, g.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// JAB-147: batch-load the member mailboxes once (was per-member FindByID).
	// Iterate memberIDs to preserve order; the map gives O(1) lookup.
	mbByID := mailboxMapByID(ctx, h.cfg.Mailboxes, memberIDs)
	members := make([]mailGroupMemberDTO, 0, len(memberIDs))
	for _, mid := range memberIDs {
		mb := mbByID[mid]
		if mb == nil {
			continue // mailbox vanished mid-read; cascade will clean the edge
		}
		members = append(members, mailGroupMemberDTO{MailboxID: mb.ID, Email: mb.EmailCached})
	}
	c.JSON(http.StatusOK, mailGroupDetailResponse{
		mailGroupResponse: toMailGroupResponse(*g, int64(len(members))),
		Members:           members,
	})
}

func (h *mailGroupHandler) update(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req updateMailGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	g, dom, ok := h.loadGroupWithAuth(c, c.Param("gid"), claims)
	if !ok {
		return
	}

	metaChanged := false
	if req.DisplayName != nil || req.Description != nil {
		dn := g.DisplayName
		if req.DisplayName != nil {
			dn = strings.TrimSpace(*req.DisplayName)
		}
		desc := g.Description
		if req.Description != nil {
			desc = strings.TrimSpace(*req.Description)
		}
		if dn != g.DisplayName || desc != g.Description {
			if err := h.cfg.Groups.UpdateMeta(ctx, g.ID, dn, desc); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			g.DisplayName, g.Description = dn, desc
			metaChanged = true
		}
	}
	if req.HasMailbox != nil || req.HasCalendar != nil || req.HasAddressbook != nil || req.HasFiles != nil {
		mbx := boolOr(req.HasMailbox, g.HasMailbox)
		cal := boolOr(req.HasCalendar, g.HasCalendar)
		ab := boolOr(req.HasAddressbook, g.HasAddressbook)
		fl := boolOr(req.HasFiles, g.HasFiles)
		if err := h.cfg.Groups.UpdateResources(ctx, g.ID, mbx, cal, ab, fl); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		g.HasMailbox, g.HasCalendar, g.HasAddressbook, g.HasFiles = mbx, cal, ab, fl
		// GH #350: re-run apply so a newly-enabled resource (esp. files, whose
		// node is created on demand) is named after the group, not a default.
		metaChanged = true
	}
	// GH #348: toggling internal-only re-projects via mailgroup.apply, which
	// installs/removes the sender-domain sieve on the group account.
	if req.InternalOnly != nil && *req.InternalOnly != g.InternalOnly {
		if err := h.cfg.Groups.UpdateInternalOnly(ctx, g.ID, *req.InternalOnly); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		g.InternalOnly = *req.InternalOnly
		metaChanged = true
	}

	// Re-project the principal description (drives the From name) when the
	// display name / description changed. Idempotent on the agent side.
	if metaChanged {
		h.notifyAgent(ctx, "mailgroup.apply", map[string]any{
			"email":         g.EmailCached,
			"display_name":  g.DisplayName,
			"description":   g.Description,
			"internal_only": g.InternalOnly,
			"has_files":     g.HasFiles,
		})
	}
	_ = dom
	count, _ := h.cfg.Groups.ListMemberMailboxIDs(ctx, g.ID)
	c.JSON(http.StatusOK, toMailGroupResponse(*g, int64(len(count))))
}

func (h *mailGroupHandler) setMembers(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req setMailGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	g, dom, ok := h.loadGroupWithAuth(c, c.Param("gid"), claims)
	if !ok {
		return
	}

	// Resolve + validate the desired mailbox set: every member must be a
	// mailbox in the SAME domain as the group (no cross-tenant adds).
	desiredEmails := make([]string, 0, len(req.MailboxIDs))
	desiredIDs := make([]string, 0, len(req.MailboxIDs))
	seen := map[string]bool{}
	for _, mid := range req.MailboxIDs {
		if seen[mid] {
			continue
		}
		seen[mid] = true
		mb, err := h.cfg.Mailboxes.FindByID(ctx, mid)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_member",
				"detail": "mailbox " + mid + " not found"})
			return
		}
		if mb.DomainID != g.DomainID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "member_wrong_domain",
				"detail": "mailbox " + mb.EmailCached + " is not in this group's domain"})
			return
		}
		desiredIDs = append(desiredIDs, mb.ID)
		desiredEmails = append(desiredEmails, mb.EmailCached)
	}

	// Compute the removed set (current − desired) so the agent can unset
	// memberGroupIds for departing members.
	prevEmails, err := h.cfg.Groups.ListMemberEmails(ctx, g.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	desiredSet := map[string]bool{}
	for _, e := range desiredEmails {
		desiredSet[e] = true
	}
	removed := make([]string, 0)
	for _, e := range prevEmails {
		if !desiredSet[e] {
			removed = append(removed, e)
		}
	}

	if err := h.cfg.Groups.SetMembers(ctx, g.ID, desiredIDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Distribution groups are mailing lists — members receive mail via the
	// SQL directory's queryRecipient fan-out (driven by the DB rows). They do
	// NOT join memberGroupIds, so they don't get the group's shared
	// collections. Only resource (collaboration) groups project membership.
	if g.GroupKind != "distribution" {
		h.notifyAgent(ctx, "mailgroup.members_set", map[string]any{
			"group_email":   g.EmailCached,
			"member_emails": desiredEmails,
			"remove_emails": removed,
		})
	}
	_ = dom
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"member_count": len(desiredIDs)}})
}

// addMember adds a single mailbox to a group without disturbing existing
// members (used by the create-mailbox wizard's "add to groups" step). The
// member-set agent call carries only the added email.
func (h *mailGroupHandler) addMember(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	g, _, ok := h.loadGroupWithAuth(c, c.Param("gid"), claims)
	if !ok {
		return
	}
	mb, err := h.cfg.Mailboxes.FindByID(ctx, c.Param("mbid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_member", "detail": "mailbox not found"})
		return
	}
	if mb.DomainID != g.DomainID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "member_wrong_domain"})
		return
	}
	if err := h.cfg.Groups.AddMember(ctx, g.ID, mb.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if g.GroupKind != "distribution" {
		h.notifyAgent(ctx, "mailgroup.members_set", map[string]any{
			"group_email":   g.EmailCached,
			"member_emails": []string{mb.EmailCached},
			"remove_emails": []string{},
		})
	}
	c.Status(http.StatusNoContent)
}

// removeMember drops a single mailbox from a group (GH #238 — managed from the
// edit-mailbox modal). Idempotent; mirrors addMember.
func (h *mailGroupHandler) removeMember(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	g, _, ok := h.loadGroupWithAuth(c, c.Param("gid"), claims)
	if !ok {
		return
	}
	mb, err := h.cfg.Mailboxes.FindByID(ctx, c.Param("mbid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_member", "detail": "mailbox not found"})
		return
	}
	if mb.DomainID != g.DomainID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "member_wrong_domain"})
		return
	}
	if err := h.cfg.Groups.RemoveMember(ctx, g.ID, mb.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if g.GroupKind != "distribution" {
		h.notifyAgent(ctx, "mailgroup.members_set", map[string]any{
			"group_email":   g.EmailCached,
			"member_emails": []string{},
			"remove_emails": []string{mb.EmailCached},
		})
	}
	c.Status(http.StatusNoContent)
}

func (h *mailGroupHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	g, _, ok := h.loadGroupWithAuth(c, c.Param("gid"), claims)
	if !ok {
		return
	}

	// Agent first: strip memberships + destroy the principal. If that
	// fails, abort before the DB delete so the rows stay consistent with
	// Stalwart (mirrors mailbox.delete ordering).
	memberEmails, err := h.cfg.Groups.ListMemberEmails(ctx, g.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if h.cfg.Agent != nil {
		agentCtx, cancel := context.WithTimeout(ctx, mailGroupAgentTimeout)
		defer cancel()
		if _, err := h.cfg.Agent.Call(agentCtx, "mailgroup.delete", map[string]any{
			"group_email":   g.EmailCached,
			"member_emails": memberEmails,
		}); err != nil {
			respondAgentErr(c, "agent_failed", err)
			return
		}
	}
	if err := h.cfg.Groups.Delete(ctx, g.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- helpers ----

func (h *mailGroupHandler) notifyAgent(ctx context.Context, command string, params any) {
	if h.cfg.Agent == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, mailGroupAgentTimeout)
	defer cancel()
	_, _ = h.cfg.Agent.Call(agentCtx, command, params)
}

func (h *mailGroupHandler) loadDomainWithAuth(c *gin.Context, domainID string, claims *auth.AccessClaims) (*models.Domain, bool) {
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), domainID)
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
	return dom, true
}

func (h *mailGroupHandler) loadGroupWithAuth(c *gin.Context, groupID string, claims *auth.AccessClaims) (*models.MailGroup, *models.Domain, bool) {
	ctx := c.Request.Context()
	g, err := h.cfg.Groups.FindByID(ctx, groupID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "group_not_found"})
			return nil, nil, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, false
	}
	dom, err := h.cfg.Domains.FindByID(ctx, g.DomainID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, nil, false
	}
	return g, dom, true
}
