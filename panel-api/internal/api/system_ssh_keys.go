// M30.1 follow-up — admin ssh-key listing endpoint (ADR-0078).
//
// /root/.ssh and /etc/jabali-panel/restic-remotes are root-owned and
// blocked from panel-api by ProtectHome=true / ProtectSystem=strict.
// Both ops are dispatched to the agent (root) via NDJSON.
package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
)

type SystemSSHKeysConfig struct {
	Agent agent.AgentInterface
}

func RegisterSystemSSHKeysRoutes(rg *gin.RouterGroup, cfg SystemSSHKeysConfig) {
	if cfg.Agent == nil {
		return
	}
	h := &systemSSHKeysHandler{cfg: cfg}
	admin := rg.Group("/admin", middleware.RequireAdmin())
	admin.GET("/system/ssh-keys", h.list)
	admin.POST("/system/ssh-keys", h.generate)
}

type systemSSHKeysHandler struct {
	cfg SystemSSHKeysConfig
}

type sshKeyEntry struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	PubkeyPath    string `json:"pubkey_path"`
	Pubkey        string `json:"pubkey"`
	HasPassphrase bool   `json:"has_passphrase"`
}

func (h *systemSSHKeysHandler) list(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5_000_000_000)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "system.sshkeys.list", map[string]any{})
	if err != nil {
		respondAgentErrStatus(c, "agent_call", err)
		return
	}
	var resp struct {
		Keys []sshKeyEntry `json:"keys"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "agent_reply_parse", "detail": err.Error()})
		return
	}
	if resp.Keys == nil {
		resp.Keys = []sshKeyEntry{}
	}
	c.JSON(http.StatusOK, gin.H{"data": resp.Keys, "total": len(resp.Keys)})
}

type generateKeyRequest struct {
	Name string `json:"name" binding:"required"`
	Type string `json:"type"`
}

func (h *systemSSHKeysHandler) generate(c *gin.Context) {
	var req generateKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_body", "detail": err.Error()})
		return
	}
	keyType := req.Type
	if keyType == "" {
		keyType = "ed25519"
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30_000_000_000)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "system.sshkeys.generate", map[string]any{
		"name": req.Name, "type": keyType,
	})
	if err != nil {
		respondAgentErrStatus(c, "agent_call", err)
		return
	}
	var resp struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		PubkeyPath string `json:"pubkey_path"`
		Pubkey     string `json:"pubkey"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "error", "error": "agent_reply_parse", "detail": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"status":      "ok",
		"name":        resp.Name,
		"path":        resp.Path,
		"pubkey_path": resp.PubkeyPath,
		"pubkey":      resp.Pubkey,
	})
}
