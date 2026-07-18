package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// respondAgentError logs an agent-origin failure server-side (with request-id
// correlation) and returns a generic 502 to the client WITHOUT the raw error
// string (JAB-114, OWASP A05). Agent errors can carry the root daemon's stderr,
// filesystem paths, and driver internals — those belong in the server log, never
// the HTTP body, even for an authenticated caller. `code` is the stable
// machine-readable error code the client already switches on.
func respondAgentErr(c *gin.Context, code string, err error) {
	logAgentError(c, code, err)
	c.JSON(http.StatusBadGateway, gin.H{"error": code})
}

// respondAgentErrorStatus is respondAgentError for the handlers whose error
// envelope also carries a top-level "status":"error" field (e.g. the backups
// endpoints). Same logging + leak-suppression, preserved wire shape.
// respondMigrateConnectErr surfaces a migration SSH-connect failure WITH its
// detail. Unlike respondAgentErr, the error here is the panel's OWN migrate
// connector talking to the SOURCE host over SSH (not the local root agent), so
// the message is an actionable SSH error (auth rejected, host key, dial) the
// admin needs to fix their credentials, not root daemon stderr. Mirrors the
// tenant pull-source path which already returns the detail. (GH #327)
func respondMigrateConnectErr(c *gin.Context, err error) {
	logAgentError(c, "connect_failed", err)
	c.JSON(http.StatusBadGateway, gin.H{"error": "connect_failed", "detail": err.Error()})
}

func respondAgentErrStatus(c *gin.Context, code string, err error) {
	logAgentError(c, code, err)
	c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": code})
}

func logAgentError(c *gin.Context, code string, err error) {
	slog.ErrorContext(c.Request.Context(), "agent call failed",
		"code", code,
		"err", err,
		"method", c.Request.Method,
		"path", c.FullPath(),
		"request_id", ginctx.RequestID(c),
	)
}
