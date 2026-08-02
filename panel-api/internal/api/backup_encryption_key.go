// M30.1 follow-up — admin-only reveal of the restic master encryption
// key (the password at /etc/jabali-panel/restic-repo.password). Per
// ADR-0075 the operator MUST back this file up out-of-band; losing it
// = losing every snapshot in every repo. Surface it in the panel so
// operators don't have to ssh as root just to read the file.
package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

type BackupEncryptionKeyConfig struct {
	Agent agent.AgentInterface
}

func RegisterBackupEncryptionKeyRoutes(rg *gin.RouterGroup, cfg BackupEncryptionKeyConfig) {
	if cfg.Agent == nil {
		return
	}
	h := &backupEncryptionKeyHandler{cfg: cfg}
	admin := rg.Group("/admin", middleware.RequireAdmin())
	admin.GET("/backup-encryption-key", h.reveal)
}

type backupEncryptionKeyHandler struct {
	cfg BackupEncryptionKeyConfig
}

func (h *backupEncryptionKeyHandler) reveal(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5_000_000_000)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "backup.repo.password.read", map[string]any{})
	if err != nil {
		respondAgentErrStatus(c, "agent_call", err)
		return
	}
	var resp struct {
		Path     string `json:"path"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "agent_reply_parse"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"path":      resp.Path,
		"password":  resp.Password,
		"algorithm": "AES-256-GCM (restic native)",
		"note":      "back this up out-of-band; losing it loses every snapshot",
	})
}
