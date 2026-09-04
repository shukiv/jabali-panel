package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeyops"
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

// sshKeyScheduler adapts the handler's in-process reconciler to
// sshkeyops.Scheduler. It preserves the pre-extraction convergence contract:
// an async, detached 30s reconcile (a client disconnect must not cancel the
// authorized_keys write), warn-logged on error. Nil reconciler → no-op.
type sshKeyScheduler struct {
	reconciler interface {
		ReconcileSSHKeysForUser(ctx context.Context, userID string) error
	}
	logger *slog.Logger
}

func (s sshKeyScheduler) ScheduleUser(userID string) {
	if s.reconciler == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.reconciler.ReconcileSSHKeysForUser(ctx, userID); err != nil && s.logger != nil {
			s.logger.WarnContext(ctx, "ssh key: reconcile user", "user_id", userID, "error", err)
		}
	}()
}

// deps builds the shared-lifecycle dependencies for this request.
func (h *sshKeysHandler) deps() sshkeyops.Deps {
	return sshkeyops.Deps{
		Keys:      h.cfg.SSHKeys,
		Scheduler: sshKeyScheduler{reconciler: h.cfg.Reconciler, logger: h.cfg.Logger},
	}
}

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

	// Validation, fingerprinting, duplicate detection, persistence, and the
	// per-user reconcile schedule all live in internal/sshkeyops (ADR-0083);
	// this handler maps the transport in and the typed result out. The
	// scheduler adapter preserves the async, detached convergence behavior.
	key, err := sshkeyops.Add(ctx, h.deps(), sshkeyops.AddRequest{
		UserID:    claims.UserID,
		Name:      req.Name,
		PublicKey: req.PublicKey,
	})
	if err != nil {
		switch {
		case errors.Is(err, sshkeys.ErrRSATooWeak):
			c.JSON(http.StatusBadRequest, gin.H{"error": "rsa_too_weak"})
		case errors.Is(err, sshkeys.ErrUnsupportedType):
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported_key_type"})
		case errors.Is(err, sshkeys.ErrInvalidKeyFormat):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_key"})
		case errors.Is(err, sshkeyops.ErrDuplicate):
			c.JSON(http.StatusConflict, gin.H{"error": "duplicate_key"})
		default:
			h.cfg.Logger.ErrorContext(ctx, "create ssh key: store key", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_store_key"})
		}
		return
	}

	c.JSON(http.StatusCreated, sshKeyResponse{
		ID:          key.ID,
		Name:        key.Name,
		Fingerprint: key.Fingerprint,
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

	// Owner-scoped lookup (ErrNotFound collapses missing + not-owned so a
	// caller can't probe another user's key IDs), then delete + schedule the
	// per-user reconcile — both in internal/sshkeyops (ADR-0083). The two-step
	// keeps the handler's distinct 404 / verify-500 / delete-500 codes.
	key, err := sshkeyops.Find(ctx, h.deps(), sshkeyops.FindRequest{KeyID: keyID, OwnerID: userID})
	if err != nil {
		if errors.Is(err, sshkeyops.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "key_not_found"})
			return
		}
		h.cfg.Logger.ErrorContext(ctx, "delete ssh key: verify ownership",
			"key_id", keyID, "user_id", userID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_verify_ownership"})
		return
	}

	if err := sshkeyops.RemoveKey(ctx, h.deps(), key); err != nil {
		h.cfg.Logger.ErrorContext(ctx, "delete ssh key: delete key",
			"key_id", keyID, "user_id", userID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed_to_delete_key"})
		return
	}

	c.Status(http.StatusNoContent)
}
