// Package api — GH #1184 admin File Manager.
//
// A whole-filesystem browser/editor for admins, mounted at /admin/files/*.
// SEPARATE from the tenant /files handler on purpose: the tenant path stays
// exactly as it was, and everything here is gated twice — admin claims AND the
// default-off admin_file_manager_enabled server setting — before any op is
// dispatched to the agent with admin_root=true (root scope minus the hard
// filesafe deny-list, enforced agent-side as the last line of defence).
package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AdminFilesHandlerConfig wires the admin File Manager. Agent + ServerSettings
// are required (nil → routes not mounted). Audits is optional — nil skips the
// audit write (the op still runs), but production always wires it.
type AdminFilesHandlerConfig struct {
	Agent          agent.AgentInterface
	ServerSettings repository.ServerSettingsRepository
	Audits         repository.AuditEventRepository
}

type adminFilesHandler struct{ cfg AdminFilesHandlerConfig }

// RegisterAdminFilesRoutes mounts /admin/files/* behind the two-factor gate
// (admin + default-off setting). Read ops (list/read) and mutations
// (write/delete/mkdir/rename/chmod); every mutation is audited.
func RegisterAdminFilesRoutes(g *gin.RouterGroup, cfg AdminFilesHandlerConfig) {
	if cfg.Agent == nil || cfg.ServerSettings == nil {
		return
	}
	h := &adminFilesHandler{cfg: cfg}
	grp := g.Group("/admin/files", h.gate)
	grp.GET("", h.list)
	grp.GET("/read", h.read)
	grp.POST("/write", h.write)
	grp.DELETE("", h.delete)
	grp.POST("/mkdir", h.mkdir)
	grp.POST("/rename", h.rename)
	grp.POST("/chmod", h.chmod)
}

// gate rejects non-admins (403 forbidden) and, for admins, refuses when the
// admin_file_manager_enabled server setting is off (403 admin_file_manager_disabled)
// so the UI can tell "not allowed" from "feature off".
func (h *adminFilesHandler) gate(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || !claims.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	s, err := h.cfg.ServerSettings.Get(c.Request.Context())
	if err != nil || s == nil || !s.AdminFileManagerEnabled {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin_file_manager_disabled"})
		return
	}
	c.Next()
}

// ident returns (user_id, username) to stamp on agent calls. The admin's email
// is a stable non-empty label; NewAdminScope only requires it non-empty (it is
// NOT used for scoping in admin mode) and it keeps the agent log legible.
func (h *adminFilesHandler) ident(c *gin.Context) (userID, username string) {
	claims := ginctx.Claims(c)
	username = claims.Email
	if username == "" {
		username = "admin"
	}
	return claims.UserID, username
}

// audit records one admin File Manager mutation. Append-only; the path is the
// target (not a secret), never file contents.
func (h *adminFilesHandler) audit(c *gin.Context, action, path, result string) {
	if h.cfg.Audits == nil {
		return
	}
	claims := ginctx.Claims(c)
	var actor *string
	if claims != nil && claims.UserID != "" {
		id := claims.UserID
		actor = &id
	}
	ip := c.ClientIP()
	_ = h.cfg.Audits.Create(c.Request.Context(), &models.AuditEvent{
		ID:          ids.NewULID(),
		TS:          time.Now().UTC(),
		ActorUserID: actor,
		ActorKind:   models.AuditActorAdmin,
		Action:      action,
		TargetType:  "file",
		TargetID:    path,
		Result:      result,
		SourceIP:    &ip,
	})
}

func (h *adminFilesHandler) list(c *gin.Context) {
	userID, username := h.ident(c)
	p, ok := requirePath(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.list", filesListAgentParams{
		UserID: userID, Username: username, AdminRoot: true, Path: p,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var result filesListAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *adminFilesHandler) read(c *gin.Context) {
	userID, username := h.ident(c)
	p, ok := requirePath(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.read", filesReadAgentParams{
		UserID: userID, Username: username, AdminRoot: true, Path: p,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var result filesReadAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *adminFilesHandler) write(c *gin.Context) {
	userID, username := h.ident(c)
	var req writeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.write", filesWriteAgentParams{
		UserID: userID, Username: username, AdminRoot: true, Path: req.Path, Content: req.Content,
	})
	if err != nil {
		h.audit(c, "admin.files.write", req.Path, "error")
		respondAgentError(c, err)
		return
	}
	h.audit(c, "admin.files.write", req.Path, "ok")
	c.JSON(http.StatusOK, gin.H{"path": req.Path})
}

func (h *adminFilesHandler) delete(c *gin.Context) {
	userID, username := h.ident(c)
	p, ok := requirePath(c)
	if !ok {
		return
	}
	recursive := c.Query("recursive") == "true"
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.delete", filesDeleteAgentParams{
		UserID: userID, Username: username, AdminRoot: true, Path: p, Recursive: recursive,
	})
	if err != nil {
		h.audit(c, "admin.files.delete", p, "error")
		respondAgentError(c, err)
		return
	}
	h.audit(c, "admin.files.delete", p, "ok")
	c.JSON(http.StatusOK, gin.H{"deleted": p})
}

func (h *adminFilesHandler) mkdir(c *gin.Context) {
	userID, username := h.ident(c)
	var req mkdirRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.mkdir", filesMkdirAgentParams{
		UserID: userID, Username: username, AdminRoot: true, Path: req.Path,
	})
	if err != nil {
		h.audit(c, "admin.files.mkdir", req.Path, "error")
		respondAgentError(c, err)
		return
	}
	h.audit(c, "admin.files.mkdir", req.Path, "ok")
	c.JSON(http.StatusOK, gin.H{"path": req.Path})
}

func (h *adminFilesHandler) rename(c *gin.Context) {
	userID, username := h.ident(c)
	var req renameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	// new_name must be a single path segment — no separators, no ".." escape,
	// so a rename can't relocate a file across the tree.
	if req.NewName == "" || strings.ContainsRune(req.NewName, '/') || req.NewName == ".." || req.NewName == "." {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_new_name"})
		return
	}
	newPath := filepath.Join(filepath.Dir(req.Path), req.NewName)
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.rename", filesRenameAgentParams{
		UserID: userID, Username: username, AdminRoot: true, OldPath: req.Path, NewPath: newPath,
	})
	if err != nil {
		h.audit(c, "admin.files.rename", req.Path, "error")
		respondAgentError(c, err)
		return
	}
	h.audit(c, "admin.files.rename", req.Path+" -> "+newPath, "ok")
	c.JSON(http.StatusOK, gin.H{"old_path": req.Path, "new_path": newPath})
}

func (h *adminFilesHandler) chmod(c *gin.Context) {
	userID, username := h.ident(c)
	var req chmodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.chmod", filesChmodAgentParams{
		UserID: userID, Username: username, AdminRoot: true, Path: req.Path, Mode: req.Mode,
	})
	if err != nil {
		h.audit(c, "admin.files.chmod", req.Path, "error")
		respondAgentError(c, err)
		return
	}
	h.audit(c, "admin.files.chmod", req.Path+" "+req.Mode, "ok")
	c.JSON(http.StatusOK, gin.H{"path": req.Path, "mode": req.Mode})
}
