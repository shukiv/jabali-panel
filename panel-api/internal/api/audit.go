package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// M49 unified audit log — read surface (ADR-0106). One store, two
// server-scoped views:
//
//   - GET /api/v1/admin/audit    (RequireAdmin)        — every row, raw
//   - GET /api/v1/me/activity    (RequireKratosSession) — caller's own
//     subject-scoped rows (their actions + actions taken ON their
//     account)
//
// The /me/activity scope is applied SERVER-SIDE via the repository's
// subject filter (never a client param) — the IDOR discipline that the
// live security testing validated on the domain/RequireOwner routes.
//
// Impersonation-visibility (ADR-0106 `audit_show_impersonation`,
// default-on) is moot in this codebase line: impersonation has no
// implementation (no emitter), so no `impersonation.*` rows exist to
// gate. Wire the server_settings toggle when/if impersonation lands.

const (
	defaultAuditPageSize = 50
	maxAuditPageSize     = 200
)

// AuditHandlerConfig — Repo is required (panic at boot if nil, per the
// route-family convention: programmer error, not a 500). Users is
// OPTIONAL: when set, the read handlers batch-resolve actor/subject
// IDs to a display name; when nil, rows still render with raw IDs.
type AuditHandlerConfig struct {
	Repo  repository.AuditEventRepository
	Users repository.UserRepository
	Log   *slog.Logger
}

type auditHandler struct{ cfg AuditHandlerConfig }

// RegisterAuditRoutes mounts the read surface. Call from app.go off the
// v1 group (which already carries RequireKratosSession); the admin
// view adds RequireAdmin on its own route.
func RegisterAuditRoutes(g *gin.RouterGroup, cfg AuditHandlerConfig) {
	if cfg.Repo == nil {
		panic("api.RegisterAuditRoutes: cfg.Repo is nil")
	}
	h := &auditHandler{cfg: cfg}
	g.GET("/admin/audit", middleware.RequireAdmin(), h.adminList)
	// GH #571 — hash-chain integrity verification + retention pruning,
	// surfaced from the admin Audit Log. Same core as `jabali audit
	// verify` / `jabali audit prune`; runs in-process (panel-api owns the
	// audit DB — no agent hop).
	g.POST("/admin/audit/verify", middleware.RequireAdmin(), h.verify)
	g.POST("/admin/audit/prune", middleware.RequireAdmin(), h.prune)
	g.GET("/me/activity", h.meActivity)
}

// verify recomputes the tamper-evidence hash chain over every audit row
// (read-only) and reports the checked/total counts + the first broken row.
func (h *auditHandler) verify(c *gin.Context) {
	rows, err := h.cfg.Repo.AllForVerify(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	anchor, _ := h.cfg.Repo.ChainAnchor(c.Request.Context())
	brokenID, checked, ok := audit.VerifyChain(rows, anchor)
	c.JSON(http.StatusOK, gin.H{
		"ok":        ok,
		"checked":   checked,
		"total":     len(rows),
		"broken_id": brokenID,
	})
}

// prune deletes audit rows older than the given retention window (days,
// default 365) and — per ADR-0106 — records the prune itself as an audit
// event so retention is never a silent selective delete.
func (h *auditHandler) prune(c *gin.Context) {
	var req struct {
		Days int `json:"days"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Days < 1 {
		req.Days = 365
	}
	ctx := c.Request.Context()
	cutoff := time.Now().UTC().AddDate(0, 0, -req.Days)
	n, err := h.cfg.Repo.PruneOlderThanWithAnchor(ctx, cutoff)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	var actorID *string
	if claims := ginctx.Claims(c); claims != nil && claims.UserID != "" {
		uid := claims.UserID
		actorID = &uid
	}
	meta, _ := json.Marshal(map[string]any{
		"cutoff": cutoff.Format(time.RFC3339), "pruned": n, "days": req.Days,
	})
	_ = h.cfg.Repo.Create(ctx, &models.AuditEvent{
		ID:          ids.NewULID(),
		TS:          time.Now().UTC(),
		ActorKind:   models.AuditActorAdmin,
		ActorUserID: actorID,
		Action:      "audit.retention.prune",
		TargetType:  "audit",
		Result:      models.AuditResultOK,
		Meta:        meta,
	})
	c.JSON(http.StatusOK, gin.H{"pruned": n, "cutoff": cutoff.Format(time.RFC3339), "days": req.Days})
}

// adminList — full forensics view (admin only).
func (h *auditHandler) adminList(c *gin.Context) {
	page, pageSize, opts := parseListOptions(c, defaultAuditPageSize, maxAuditPageSize)
	rows, total, err := h.cfg.Repo.ListAll(c.Request.Context(), opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.enrichNames(c.Request.Context(), rows)
	c.JSON(http.StatusOK, gin.H{
		"data":      rows,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// enrichNames batch-resolves the page's actor/subject user IDs to a
// display name (username, falling back to email) and writes it onto
// ActorName/SubjectName so the UI can show "alice" instead of a raw
// ULID. ONE lookup per page (no N+1) per the M13.1 denormalize
// convention. Display-only — scoping stays server-side in the repo,
// and a lookup failure never fails the audit read (raw IDs still
// render). No-op when Users is unconfigured.
func (h *auditHandler) enrichNames(ctx context.Context, rows []models.AuditEvent) {
	if h.cfg.Users == nil || len(rows) == 0 {
		return
	}
	idset := make(map[string]struct{})
	for i := range rows {
		if rows[i].ActorUserID != nil && *rows[i].ActorUserID != "" {
			idset[*rows[i].ActorUserID] = struct{}{}
		}
		if rows[i].SubjectUserID != nil && *rows[i].SubjectUserID != "" {
			idset[*rows[i].SubjectUserID] = struct{}{}
		}
	}
	if len(idset) == 0 {
		return
	}
	ids := make([]string, 0, len(idset))
	for id := range idset {
		ids = append(ids, id)
	}
	users, err := h.cfg.Users.FindByIDs(ctx, ids)
	if err != nil {
		if h.cfg.Log != nil {
			h.cfg.Log.Warn("audit: actor-name enrichment failed", "err", err)
		}
		return
	}
	name := make(map[string]string, len(users))
	for i := range users {
		u := users[i]
		dn := u.Email
		if u.Username != nil && *u.Username != "" {
			dn = *u.Username
		}
		name[u.ID] = dn
	}
	for i := range rows {
		if rows[i].ActorUserID != nil {
			if dn, ok := name[*rows[i].ActorUserID]; ok {
				v := dn
				rows[i].ActorName = &v
			}
		}
		if rows[i].SubjectUserID != nil {
			if dn, ok := name[*rows[i].SubjectUserID]; ok {
				v := dn
				rows[i].SubjectName = &v
			}
		}
	}
}

// meActivity — the caller's own activity feed: events they ACTED plus
// events about their account, scoped to the session identity in the
// repo (ListByActorOrSubject) — never a client filter. Subject-only
// showed an empty feed because the generic recorder subject-tags only
// /api/v1/me/* routes; actor-or-subject is what "see all his last
// actions" actually means. A blank identity matches nothing.
func (h *auditHandler) meActivity(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	page, pageSize, opts := parseListOptions(c, defaultAuditPageSize, maxAuditPageSize)
	rows, total, err := h.cfg.Repo.ListByActorOrSubject(c.Request.Context(), claims.UserID, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.enrichNames(c.Request.Context(), rows)
	c.JSON(http.StatusOK, gin.H{
		"data":      rows,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
