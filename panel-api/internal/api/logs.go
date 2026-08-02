package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type LogHandlerConfig struct {
	LogAccessStreams repository.LogAccessStreamRepository
	Domains          repository.DomainRepository
	Users            repository.UserRepository
}

type logHandler struct{ cfg LogHandlerConfig }

type createLogAccessRequest struct {
	DomainID string `json:"domain_id,omitempty"`
	LogType  string `json:"log_type" binding:"required,oneof=access error goaccess"`
}

type logAccessResponse struct {
	StreamKey    string    `json:"stream_key"`
	ExpiresAt    time.Time `json:"expires_at"`
	WebsocketURL string    `json:"websocket_url"`
}

// RegisterLogRoutes sets up log-related API endpoints
func RegisterLogRoutes(g *gin.RouterGroup, cfg LogHandlerConfig) {
	h := &logHandler{cfg: cfg}
	logs := g.Group("/logs")
	logs.POST("/access", h.createAccess)
	logs.DELETE("/access/:stream_key", h.deleteAccess)
	logs.GET("/types", h.listTypes)
}

// listTypes returns available log types and their descriptions
func (h *logHandler) listTypes(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	types := []gin.H{
		{
			"type":        "access",
			"name":        "Access Logs",
			"description": "Nginx access logs showing HTTP requests",
			"realtime":    true,
		},
		{
			"type":        "error",
			"name":        "Error Logs",
			"description": "Nginx error logs showing server errors",
			"realtime":    true,
		},
		{
			"type":        "goaccess",
			"name":        "GoAccess Report",
			"description": "Real-time web log analyzer dashboard",
			"realtime":    true,
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"data": types,
	})
}

// createAccess creates a time-limited access stream for log viewing
func (h *logHandler) createAccess(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var req createLogAccessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Validate domain access if domain_id is provided
	if req.DomainID != "" {
		if h.cfg.Domains == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "domain service not available"})
			return
		}

		domain, err := h.cfg.Domains.FindByID(c.Request.Context(), req.DomainID)
		if err != nil {
			if err == repository.ErrNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			}
			return
		}

		// Non-admin users can only access their own domain logs
		if !claims.IsAdmin && domain.UserID != claims.UserID {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
			return
		}
	} else if !claims.IsAdmin {
		// Non-admin users must specify a domain
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain_id required for non-admin users"})
		return
	}

	// Check rate limit - max 5 concurrent streams per user
	if h.cfg.LogAccessStreams != nil {
		count, err := h.cfg.LogAccessStreams.CountByUserID(c.Request.Context(), claims.UserID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if count >= 5 {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many active log streams"})
			return
		}
	}

	// Generate cryptographically secure stream key
	keyBytes := make([]byte, 16) // 32 hex chars
	if _, err := rand.Read(keyBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	streamKey := hex.EncodeToString(keyBytes)

	// Create stream record with 15-minute expiry
	expiresAt := time.Now().Add(15 * time.Minute)
	var domainID *string
	if req.DomainID != "" {
		domainID = &req.DomainID
	}
	stream := &models.LogAccessStream{
		ID:        ids.NewULID(),
		UserID:    claims.UserID,
		DomainID:  domainID,
		LogType:   req.LogType,
		StreamKey: streamKey,
		ExpiresAt: expiresAt,
	}

	if err := h.cfg.LogAccessStreams.Create(c.Request.Context(), stream); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Build WebSocket URL
	scheme := "ws"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s/api/v1/logs/stream/%s", scheme, c.Request.Host, streamKey)

	c.JSON(http.StatusCreated, logAccessResponse{
		StreamKey:    streamKey,
		ExpiresAt:    expiresAt,
		WebsocketURL: wsURL,
	})
}

// deleteAccess revokes a log access stream
func (h *logHandler) deleteAccess(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	streamKey := c.Param("stream_key")
	if streamKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stream_key required"})
		return
	}

	// Validate stream ownership
	stream, err := h.cfg.LogAccessStreams.FindByStreamKey(c.Request.Context(), streamKey)
	if err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "stream not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}

	// Users can only delete their own streams, admins can delete any
	if !claims.IsAdmin && stream.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}

	if err := h.cfg.LogAccessStreams.DeleteByID(c.Request.Context(), stream.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.Status(http.StatusNoContent)
}

// validateLogType ensures the log type is supported
func validateLogType(logType string) error {
	switch logType {
	case "access", "error", "goaccess":
		return nil
	default:
		return fmt.Errorf("unsupported log type: %s", logType)
	}
}

// validateStreamKey validates stream key format for security
func validateStreamKey(key string) error {
	if len(key) != 32 {
		return fmt.Errorf("invalid stream key length")
	}

	// Must be hex-encoded
	if _, err := hex.DecodeString(key); err != nil {
		return fmt.Errorf("invalid stream key format")
	}

	return nil
}

// logFilePathForDomain returns the log file path for a domain and log type.
// Domain name passes through a strict allowlist (alnum, dot, hyphen) so the
// resulting filepath.Join can never escape /var/log/nginx/. The output is
// re-asserted with filepath.Clean + a HasPrefix check at the call site (see
// resolveLogPath) so the path-validation invariant holds even if a future
// caller forgets it.
func logFilePathForDomain(domainName, logType string) (string, error) {
	if !isSafeDomainSegment(domainName) {
		return "", fmt.Errorf("invalid domain name")
	}
	baseDir := "/var/log/nginx"
	switch logType {
	case "access":
		return filepath.Join(baseDir, fmt.Sprintf("%s-access.log", domainName)), nil
	case "error":
		return filepath.Join(baseDir, fmt.Sprintf("%s-error.log", domainName)), nil
	default:
		return "", fmt.Errorf("unsupported log type for file path: %s", logType)
	}
}

// isSafeDomainSegment is the path-safety predicate the WS streamer uses
// before joining the operator-supplied domain into the log path. Permits
// only RFC-1035-shape labels + dot separators; rejects path metacharacters
// outright (no '/', no '\', no '..', no '%', no spaces).
func isSafeDomainSegment(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-':
		default:
			return false
		}
	}
	return !strings.Contains(s, "..")
}
