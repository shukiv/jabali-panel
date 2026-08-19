package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/logaccess"
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
	logs.GET("/tail", h.tail)
}

// tail returns the last N lines of a domain's nginx access or error log as a
// JSON snapshot — the request/response counterpart to the /logs/stream WebSocket
// (which is a live follow, unusable from a stateless API client).
//
// It reuses the same vetted path resolver (logFilePathForDomain +
// isSafeDomainSegment) and reads via `tail -n`, exactly like the streamer, so it
// inherits the streamer's path-safety and file-access model. Ownership is
// enforced: a non-admin can only read logs for a domain they own (404 on a
// domain they don't, to avoid leaking existence).
func (h *logHandler) tail(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	logType := c.Query("log_type")
	if logType != "access" && logType != "error" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "log_type must be 'access' or 'error'"})
		return
	}
	domainID := c.Query("domain_id")
	if domainID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "domain_id required"})
		return
	}
	// Clamp lines to [1, 2000], default 200 — bounded so a client can never ask
	// the server to buffer an unbounded log.
	lines := 200
	if v := c.Query("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			lines = n
		}
	}
	if lines < 1 {
		lines = 1
	}
	if lines > 2000 {
		lines = 2000
	}

	if h.cfg.Domains == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "domain service not available"})
		return
	}
	domain, err := h.cfg.Domains.FindByID(c.Request.Context(), domainID)
	if err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain not found"})
		return
	}

	logPath, err := logFilePathForDomain(domain.Name, logType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log request"})
		return
	}

	cctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "tail", "-n", strconv.Itoa(lines), logPath).Output()
	if err != nil {
		// Most commonly the file doesn't exist yet (no traffic). Treat as empty
		// rather than a 500 — an empty log is a valid answer.
		c.JSON(http.StatusOK, gin.H{
			"domain_id": domainID, "log_type": logType, "lines": []string{},
			"note": "log is empty or not yet created",
		})
		return
	}
	trimmed := strings.TrimRight(string(out), "\n")
	var ls []string
	if trimmed != "" {
		ls = strings.Split(trimmed, "\n")
	}
	c.JSON(http.StatusOK, gin.H{"domain_id": domainID, "log_type": logType, "lines": ls})
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

	// Validate grant scope through the shared policy (JAB-303). The caller is
	// the beneficiary here, so claims.IsAdmin is the beneficiary's admin status.
	domainProvided := req.DomainID != ""
	domainOwned := false
	if domainProvided {
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
		domainOwned = domain.UserID == claims.UserID
	}

	if err := logaccess.ValidateGrantScope(claims.IsAdmin, domainProvided, domainOwned); err != nil {
		if errors.Is(err, logaccess.ErrDomainNotOwned) {
			c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "domain_id required for non-admin users"})
		}
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
