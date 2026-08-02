package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeys"
)

// SSHKeysHandlerConfig wires SSH keys CRUD routes.
type SSHKeysHandlerConfig struct {
	SSHKeys    repository.SSHKeyRepository
	Reconciler interface {
		ReconcileSSHKeysForUser(ctx context.Context, userID string) error
	}
	Logger *slog.Logger
}

// RegisterSSHKeysRoutes registers SSH keys CRUD routes under /api/v1/ssh-keys.
// Routes:
//   - POST   /api/v1/ssh-keys                    { "name": "...", "public_key": "..." }
//   - GET    /api/v1/ssh-keys                    list caller's keys
//   - DELETE /api/v1/ssh-keys/:id                delete caller's key (enforces ownership)
func RegisterSSHKeysRoutes(g *gin.RouterGroup, cfg SSHKeysHandlerConfig) {
	h := &sshKeysHandler{cfg: cfg}
	g.POST("/ssh-keys", h.create)
	g.GET("/ssh-keys", h.list)
	g.DELETE("/ssh-keys/:id", h.delete)
}

type sshKeysHandler struct{ cfg SSHKeysHandlerConfig }

// createSSHKeyRequest is the body for POST /api/v1/ssh-keys.
type createSSHKeyRequest struct {
	Name      string `json:"name" binding:"required"`
	PublicKey string `json:"public_key" binding:"required"`
}

// sshKeyResponse is the format for a single SSH key (list and create endpoints).
// Note: fingerprint is included but normalized key is never leaked.
type sshKeyResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
}

// sshKeyListResponse is the format for GET /api/v1/ssh-keys.
type sshKeyListResponse struct {
	Items []sshKeyResponse `json:"items"`
}

// create handles POST /api/v1/ssh-keys: create a new SSH key.
// Validates the public key format, checks for duplicates, and triggers
// per-user reconciliation.
func (h *sshKeysHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createSSHKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}

	ctx := c.Request.Context()
	userID := claims.UserID

	// Validate and parse the public key
	normalizedKey, fingerprint, err := sshkeys.ParseAndFingerprint(req.PublicKey)
	if err != nil {
		if errors.Is(err, sshkeys.ErrInvalidKeyFormat) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_key"})
			return
		}
		if errors.Is(err, sshkeys.ErrRSATooWeak) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "rsa_too_weak"})
			return
		}
		if errors.Is(err, sshkeys.ErrUnsupportedType) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_key_type"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_key"})
		return
	}

	// Create the SSH key
	key := &models.SSHKey{
		ID:          ids.NewULID(),
		UserID:      userID,
		Name:        req.Name,
		PublicKey:   normalizedKey,
		Fingerprint: fingerprint,
	}

	if err := h.cfg.SSHKeys.Create(ctx, key); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "duplicate_key"})
			return
		}
		h.cfg.Logger.ErrorContext(ctx, "create ssh key: store key", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_store_key"})
		return
	}

	// Trigger per-user SSH keys reconciliation asynchronously
	if h.cfg.Reconciler != nil {
		go func() {
			reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.cfg.Reconciler.ReconcileSSHKeysForUser(reconcileCtx, userID); err != nil {
				h.cfg.Logger.WarnContext(reconcileCtx, "create ssh key: reconcile user",
					"user_id", userID, "key_id", key.ID, "error", err)
			}
		}()
	}

	c.JSON(http.StatusCreated, sshKeyResponse{
		ID:          key.ID,
		Name:        key.Name,
		Fingerprint: fingerprint,
		CreatedAt:   key.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// list handles GET /api/v1/ssh-keys: list the caller's SSH keys.
// Returns only public_key metadata (id, name, fingerprint, created_at),
// not the normalized public key itself.
func (h *sshKeysHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx := c.Request.Context()
	userID := claims.UserID

	keys, err := h.cfg.SSHKeys.ListByUserID(ctx, userID)
	if err != nil {
		h.cfg.Logger.ErrorContext(ctx, "list ssh keys: fetch keys", "user_id", userID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_fetch_keys"})
		return
	}

	items := make([]sshKeyResponse, len(keys))
	for i, key := range keys {
		items[i] = sshKeyResponse{
			ID:          key.ID,
			Name:        key.Name,
			Fingerprint: key.Fingerprint,
			CreatedAt:   key.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}
	}

	c.JSON(http.StatusOK, sshKeyListResponse{Items: items})
}

// delete handles DELETE /api/v1/ssh-keys/:id: delete the caller's SSH key.
// Enforces ownership via FindByIDAndUserID. Returns 204 on success,
// 404 if not found.
func (h *sshKeysHandler) delete(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx := c.Request.Context()
	userID := claims.UserID
	keyID := c.Param("id")

	// Verify ownership
	_, err := h.cfg.SSHKeys.FindByIDAndUserID(ctx, keyID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "key_not_found"})
			return
		}
		h.cfg.Logger.ErrorContext(ctx, "delete ssh key: verify ownership",
			"key_id", keyID, "user_id", userID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_verify_ownership"})
		return
	}

	// Delete the key
	if err := h.cfg.SSHKeys.Delete(ctx, keyID); err != nil {
		h.cfg.Logger.ErrorContext(ctx, "delete ssh key: delete key",
			"key_id", keyID, "user_id", userID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_delete_key"})
		return
	}

	// Trigger per-user SSH keys reconciliation asynchronously
	if h.cfg.Reconciler != nil {
		go func() {
			reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.cfg.Reconciler.ReconcileSSHKeysForUser(reconcileCtx, userID); err != nil {
				h.cfg.Logger.WarnContext(reconcileCtx, "delete ssh key: reconcile user",
					"user_id", userID, "key_id", keyID, "error", err)
			}
		}()
	}

	c.Status(http.StatusNoContent)
}
