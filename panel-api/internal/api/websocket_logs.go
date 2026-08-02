package api

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

var upgrader = websocket.Upgrader{
	CheckOrigin:     checkLogStreamOrigin,
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// checkLogStreamOrigin enforces same-origin on the log-stream WS so a
// CSRF page can't surreptitiously open a stream against the panel.
//
// Behaviour:
//   - No Origin header (curl, native WS clients, panel-api → panel-api):
//     allow. They can't be CSRF'd because there's no browser ambient
//     credential to leak.
//   - Origin header present: must be a same-host URL (host + port match
//     r.Host verbatim). Cross-origin attempts are rejected even if
//     they happen to know the stream_key — defense in depth.
//
// Schemes are NOT compared because the panel is reachable via both
// http (rare, dev) and https (production); ports ARE compared via
// r.Host so a panel on :8443 doesn't accept origin :443.
func checkLogStreamOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	const sep = "://"
	if idx := stringsIndexOf(origin, sep); idx >= 0 {
		origin = origin[idx+len(sep):]
	}
	return origin == r.Host
}

// stringsIndexOf — local, allocation-free strings.Index. Inline to
// keep the security check obvious at a glance.
func stringsIndexOf(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

type LogStreamHandlerConfig struct {
	LogAccessStreams repository.LogAccessStreamRepository
	Domains          repository.DomainRepository
	Log              *slog.Logger
}

// RegisterLogStreamRoutes sets up WebSocket log streaming endpoints
func RegisterLogStreamRoutes(r gin.IRouter, cfg LogStreamHandlerConfig) {
	handler := &logStreamHandler{cfg: cfg}
	r.GET("/logs/stream/:stream_key", handler.streamLogs)
	// HTTP-render goaccess endpoint — bypasses WS so the iframe loads
	// over a fresh HTTP response that carries its own relaxed CSP
	// (script-src includes 'unsafe-eval' for GoAccess's Function()
	// templating). srcdoc-via-WS inherits parent CSP which forbids
	// unsafe-eval and meta CSP can only tighten — see fix commit.
	r.GET("/logs/stream/:stream_key/goaccess.html", handler.renderGoAccessHTTP)
}

type logStreamHandler struct{ cfg LogStreamHandlerConfig }

// streamLogs handles WebSocket connections for real-time log streaming
func (h *logStreamHandler) streamLogs(c *gin.Context) {
	streamKey := c.Param("stream_key")
	if streamKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stream_key required"})
		return
	}

	// Validate stream key format
	if err := validateStreamKey(streamKey); err != nil {
		h.cfg.Log.Warn("invalid stream key format", "key", streamKey, "err", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid stream key"})
		return
	}

	// Look up and validate the stream
	stream, err := h.cfg.LogAccessStreams.FindByStreamKey(c.Request.Context(), streamKey)
	if err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "stream not found or expired"})
		} else {
			h.cfg.Log.Error("failed to find stream", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.cfg.Log.Error("websocket upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	// No deadlines set here. Initial deadlines (especially the 10s
	// write deadline) would expire while scanner.Scan() blocks waiting
	// for the next log line on an idle site, causing the very first
	// WriteMessage to fail with i/o timeout. Per-write deadlines are
	// set immediately before each WriteMessage in the streaming loops.
	// Pump reader frames in the background so gorilla can auto-reply
	// to control frames (ping/pong, close) — without this the browser
	// can hang on close handshake and the server doesn't notice peer
	// disconnection.
	go func() {
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	// Handle different log types
	switch stream.LogType {
	case "access", "error":
		h.streamLogFile(conn, stream)
	case "goaccess":
		h.streamGoAccess(conn, stream)
	default:
		h.cfg.Log.Error("unsupported log type", "type", stream.LogType)
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "unsupported log type"))
		return
	}
}

// streamLogFile streams nginx access or error logs
func (h *logStreamHandler) streamLogFile(conn *websocket.Conn, stream *models.LogAccessStream) {
	// Determine log file path
	var logPath string
	var err error

	if stream.DomainID != nil {
		// Get domain for per-domain logs
		domain, err := h.cfg.Domains.FindByID(context.Background(), *stream.DomainID)
		if err != nil {
			h.cfg.Log.Error("failed to find domain", "domain_id", *stream.DomainID, "err", err)
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "domain not found"))
			return
		}
		logPath, err = logFilePathForDomain(domain.Name, stream.LogType)
	} else {
		// System-wide logs (admin only)
		logPath, err = systemLogPath(stream.LogType)
	}

	if err != nil {
		h.cfg.Log.Error("failed to determine log path", "err", err)
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "invalid log path"))
		return
	}

	// Verify log file exists and is readable
	if _, err := os.Stat(logPath); err != nil {
		h.cfg.Log.Warn("log file not accessible", "path", logPath, "err", err)
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Log file not found: %s\n", filepath.Base(logPath))))
		return
	}

	// Start tailing the log file
	cmd := exec.Command("tail", "-f", "-n", "50", logPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		h.cfg.Log.Error("failed to create stdout pipe", "err", err)
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "failed to start log stream"))
		return
	}

	if err := cmd.Start(); err != nil {
		h.cfg.Log.Error("failed to start tail command", "err", err)
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "failed to start log stream"))
		return
	}

	// Ensure we clean up the process
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()

	// Stream log lines to WebSocket
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		// Check if stream has expired
		if time.Now().After(stream.ExpiresAt) {
			h.cfg.Log.Info("stream expired", "stream_key", stream.StreamKey)
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stream expired"))
			return
		}

		// Set deadline BEFORE the write — a deadline set after the
		// previous write may have expired during the long scanner
		// block while waiting for the next log line on an idle log.
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

		// Send log line to client
		line := scanner.Text()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(line+"\n")); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				h.cfg.Log.Error("websocket write failed", "err", err)
			}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		h.cfg.Log.Error("log scanner error", "err", err)
	}
}

// streamGoAccess closes the WS with a redirect message — frontend should
// use the HTTP /goaccess.html endpoint instead. Kept for back-compat in
// case a stale frontend caches the old WS path; new frontend never opens
// goaccess WS. The HTTP endpoint carries its own relaxed CSP allowing
// 'unsafe-eval', which the WS-srcdoc path could not (parent CSP inherited).
func (h *logStreamHandler) streamGoAccess(conn *websocket.Conn, stream *models.LogAccessStream) {
	_ = stream
	conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseUnsupportedData,
			"goaccess moved to HTTP /goaccess.html — refresh the panel"))
}

// goaccessAccessLogPath resolves the nginx access-log path for a stream:
// per-domain when stream.DomainID is set, server-wide otherwise. Errors
// when neither path can be determined.
func (h *logStreamHandler) goaccessAccessLogPath(ctx context.Context, stream *models.LogAccessStream) (string, error) {
	if stream.DomainID != nil {
		domain, err := h.cfg.Domains.FindByID(ctx, *stream.DomainID)
		if err != nil {
			return "", fmt.Errorf("domain lookup: %w", err)
		}
		return logFilePathForDomain(domain.Name, "access")
	}
	return systemLogPath("access")
}

// runGoAccess executes the goaccess binary against accessLogPath and
// returns its HTML output. Caller is responsible for cancellation +
// timeouts via ctx. `--no-progress` prevents progress-bar bytes from
// corrupting the HTML stream; `--no-html-last-updated` keeps output
// deterministic across renders (avoids spurious iframe churn).
func runGoAccess(ctx context.Context, accessLogPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "goaccess",
		accessLogPath,
		"--log-format=COMBINED",
		"-o", "html",
		"--no-progress",
		"--no-html-last-updated")
	return cmd.Output()
}

// goaccessCSP is the per-response Content-Security-Policy that the
// rendered GoAccess HTML carries. GoAccess's bundled JS uses
// `new Function(...)` for HTML-template compilation, which requires
// 'unsafe-eval'. The panel's server-scope CSP forbids unsafe-eval — by
// returning a fresh URL-loaded response (not srcdoc), the iframe gets
// THIS CSP instead of the parent's. nginx must NOT add its own CSP
// add_header on this route (otherwise nginx's wins per nginx semantics).
const goaccessCSP = "default-src 'self' 'unsafe-inline' 'unsafe-eval' data: blob:; " +
	"script-src 'self' 'unsafe-inline' 'unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'self'; " +
	"form-action 'self';"

// renderGoAccessHTTP serves the current GoAccess HTML snapshot over HTTP
// (not WS). The response carries the relaxed `goaccessCSP` header so the
// iframe's GoAccess JS can execute its `new Function()` template
// compilation. Authentication reuses the stream-key model: the URL
// embeds a short-lived key looked up via LogAccessStreamRepository.
func (h *logStreamHandler) renderGoAccessHTTP(c *gin.Context) {
	streamKey := c.Param("stream_key")
	if streamKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stream_key required"})
		return
	}
	if err := validateStreamKey(streamKey); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid stream key"})
		return
	}
	stream, err := h.cfg.LogAccessStreams.FindByStreamKey(c.Request.Context(), streamKey)
	if err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "stream not found or expired"})
		} else {
			h.cfg.Log.Error("failed to find stream", "err", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}
	if stream.LogType != "goaccess" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "stream is not goaccess type"})
		return
	}
	if time.Now().After(stream.ExpiresAt) {
		c.JSON(http.StatusGone, gin.H{"error": "stream expired"})
		return
	}

	accessLogPath, err := h.goaccessAccessLogPath(c.Request.Context(), stream)
	if err != nil {
		h.cfg.Log.Error("goaccess: log path resolution failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid log path"})
		return
	}

	// Per-response CSP MUST be set BEFORE any write. nginx add_header
	// at the goaccess location is OFF (see vhost template) so this
	// header is what the browser sees.
	c.Header("Content-Security-Policy", goaccessCSP)
	// frame-ancestors is set in the CSP; the legacy X-Frame-Options from
	// server scope is overridden to SAMEORIGIN by nginx for this location.
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("Content-Type", "text/html; charset=utf-8")
	// goaccess output changes every render — short cache prevents the
	// browser from holding a stale dashboard between iframe refreshes.
	c.Header("Cache-Control", "no-store, max-age=0")

	if _, statErr := os.Stat(accessLogPath); statErr != nil {
		c.String(http.StatusOK,
			"<!doctype html><html><body style='font-family:sans-serif;padding:2em;background:#1f1f1f;color:#fff'>"+
				"<h2>No access log yet</h2>"+
				"<p>File <code>%s</code> doesn't exist. "+
				"GoAccess will start once nginx writes traffic to this domain.</p>"+
				"</body></html>", filepath.Base(accessLogPath))
		return
	}

	cmdCtx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	out, err := runGoAccess(cmdCtx, accessLogPath)
	if err != nil {
		h.cfg.Log.Warn("goaccess render failed", "err", err, "path", accessLogPath)
		c.String(http.StatusOK,
			"<!doctype html><html><body style='font-family:sans-serif;padding:2em;background:#1f1f1f;color:#fff'>"+
				"<h2>GoAccess error</h2><pre>%s</pre></body></html>", err.Error())
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", out)
}

// systemLogPath returns the path to system-wide nginx logs
func systemLogPath(logType string) (string, error) {
	switch logType {
	case "access":
		return "/var/log/nginx/access.log", nil
	case "error":
		return "/var/log/nginx/error.log", nil
	default:
		return "", fmt.Errorf("unsupported system log type: %s", logType)
	}
}
