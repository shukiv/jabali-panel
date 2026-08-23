package api

// M45 root web terminal — token mint + WS↔agent-PTY bridge (ADR-0096).
//
// panel-api never gets root: it validates the off-by-default gate +
// the one-shot IP/admin-bound token, then relays opaque frames between
// the browser WebSocket and the root PTY broker on
// /run/jabali/agent-pty.sock. The agent owns the PTY + the asciinema
// recording.
//
// Browser WS messages are binary, [1B opcode][payload] (WS preserves
// message boundaries — no length prefix needed). The agent UDS uses
// [1B opcode][4B BE len][payload]. This bridge is the 1:1 translator.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	terminalTokenTTL   = 60 * time.Second
	terminalAgentPTYSk = "/run/jabali/agent-pty.sock"

	// Wire opcodes — identical on the browser WS side and the agent UDS
	// side so this bridge is a pure relay.
	tWSStdout byte = 0x00 // agent → browser
	tWSStdin  byte = 0x01 // browser → agent
	tWSResize byte = 0x02 // browser → agent (JSON {cols,rows})
	tWSExit   byte = 0x03 // agent → browser
	tWSInit   byte = 0x10 // panel-api → agent (first UDS frame only)
)

type TerminalHandlerConfig struct {
	Sessions       repository.TerminalSessionRepository
	ServerSettings repository.ServerSettingsRepository
	Notifications  *notifications.Queue
	AgentPTYSocket string // override for tests; default terminalAgentPTYSk
	Log            *slog.Logger
	// KratosClient powers the JAB-380 recent-auth (step-up) check on token
	// mint. Optional: nil disables step-up (tests) — the mint is still admin +
	// root_terminal_enabled gated.
	KratosClient *kratosclient.Client
}

type terminalHandler struct{ cfg TerminalHandlerConfig }

// RegisterTerminalRoutes mounts under the admin RequireAdmin group:
//
//	POST /admin/terminal/session   mint a one-shot token
//	GET  /admin/terminal/ws/:token WS upgrade → root PTY
func RegisterTerminalRoutes(admin gin.IRouter, cfg TerminalHandlerConfig) {
	if cfg.AgentPTYSocket == "" {
		cfg.AgentPTYSocket = terminalAgentPTYSk
	}
	h := &terminalHandler{cfg: cfg}
	admin.POST("/terminal/session", h.mint)
	admin.GET("/terminal/ws/:token", h.ws)
}

func (h *terminalHandler) gateOpen(ctx context.Context) bool {
	if h.cfg.ServerSettings == nil {
		return false
	}
	s, err := h.cfg.ServerSettings.Get(ctx)
	return err == nil && s != nil && s.RootTerminalEnabled
}

type terminalMintResponse struct {
	Token        string    `json:"token"`
	WebsocketURL string    `json:"websocket_url"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (h *terminalHandler) mint(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || !claims.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if !h.gateOpen(c.Request.Context()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "root_terminal_disabled",
			"detail": "Enable it in Server Settings first (off by default)."})
		return
	}
	// JAB-380 recent-auth (step-up): opening a root shell requires a recently
	// authenticated interactive session. Checked at mint; the WS upgrade
	// inherits it via the one-shot 60s IP+uid-bound token, so ws() doesn't
	// re-prompt. nil KratosClient (tests) leaves mint admin+opt-in gated only.
	if h.cfg.KratosClient != nil {
		if !requireRecentAuth(c, h.cfg.KratosClient, stepUpWindow) {
			return
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw) // 43 chars
	now := time.Now()
	sess := &models.TerminalSession{
		ID:        ids.NewULID(),
		UserID:    claims.UserID,
		Token:     token,
		ClientIP:  c.ClientIP(),
		ExpiresAt: now.Add(terminalTokenTTL),
		CreatedAt: now,
	}
	if err := h.cfg.Sessions.Create(c.Request.Context(), sess); err != nil {
		h.cfg.Log.Error("terminal: mint create failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	scheme := "wss"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") != "https" {
		scheme = "ws"
	}
	c.JSON(http.StatusOK, terminalMintResponse{
		Token:        token,
		WebsocketURL: fmt.Sprintf("%s://%s/api/v1/admin/terminal/ws/%s", scheme, c.Request.Host, token),
		ExpiresAt:    sess.ExpiresAt,
	})
}

func (h *terminalHandler) ws(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || !claims.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if !h.gateOpen(c.Request.Context()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "root_terminal_disabled"})
		return
	}
	token := c.Param("token")
	if len(token) != 43 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad_token"})
		return
	}
	// Atomic single-use consume (UPDATE ... WHERE used_at IS NULL).
	sess, err := h.cfg.Sessions.ConsumeValid(c.Request.Context(), token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token_invalid_or_used"})
		return
	}
	// Re-bind: same admin, same client IP as the mint.
	if sess.UserID != claims.UserID || sess.ClientIP != c.ClientIP() {
		h.cfg.Log.Warn("terminal: token rebind mismatch",
			"session", sess.ID, "want_uid", sess.UserID, "got_uid", claims.UserID,
			"want_ip", sess.ClientIP, "got_ip", c.ClientIP())
		c.JSON(http.StatusForbidden, gin.H{"error": "token_rebind_denied"})
		return
	}

	cols := atoiDefault(c.Query("cols"), 80)
	rows := atoiDefault(c.Query("rows"), 24)

	// Dial the root PTY broker BEFORE upgrading, so a broker-down
	// situation surfaces as a clean HTTP 502 not a half-open WS.
	d := net.Dialer{Timeout: 5 * time.Second}
	uds, derr := d.DialContext(c.Request.Context(), "unix", h.cfg.AgentPTYSocket)
	if derr != nil {
		h.cfg.Log.Error("terminal: agent PTY broker unreachable", "err", derr)
		c.JSON(http.StatusBadGateway, gin.H{"error": "pty_broker_unreachable"})
		return
	}
	defer uds.Close()

	conn, uerr := upgrader.Upgrade(c.Writer, c.Request, nil)
	if uerr != nil {
		h.cfg.Log.Error("terminal: ws upgrade failed", "err", uerr)
		return
	}
	defer conn.Close()

	// Init frame → agent (carries session id for the .cast filename).
	initJSON, _ := json.Marshal(map[string]any{
		"session_id": sess.ID, "cols": cols, "rows": rows,
	})
	if werr := udsWriteFrame(uds, tWSInit, initJSON); werr != nil {
		h.cfg.Log.Error("terminal: init frame failed", "err", werr)
		return
	}

	castPath := filepath.Join("/var/log/jabali/terminal", sess.ID+".cast")
	_ = h.cfg.Sessions.MarkStarted(c.Request.Context(), sess.ID, castPath)
	h.notifyOpen(claims, sess, c.ClientIP())
	h.cfg.Log.Warn("ROOT TERMINAL opened",
		"session", sess.ID, "admin", claims.Email, "ip", c.ClientIP(), "cast", castPath)

	defer func() {
		_ = h.cfg.Sessions.MarkEnded(context.Background(), sess.ID)
		h.cfg.Log.Warn("ROOT TERMINAL closed", "session", sess.ID, "admin", claims.Email)
	}()

	var once sync.Once
	stop := make(chan struct{})
	end := func() { once.Do(func() { close(stop) }) }

	// agent UDS → browser WS.
	go func() {
		defer end()
		for {
			op, payload, rerr := udsReadFrame(uds)
			if rerr != nil {
				return
			}
			// Relay as a single binary WS message: [1B op][payload].
			msg := append([]byte{op}, payload...)
			if op == tWSExit {
				_ = conn.WriteMessage(2 /*binary*/, msg)
				return
			}
			if conn.WriteMessage(2, msg) != nil {
				return
			}
		}
	}()

	// browser WS → agent UDS.
	go func() {
		defer end()
		for {
			mt, data, rerr := conn.ReadMessage()
			if rerr != nil {
				return
			}
			if mt != 2 /*binary*/ || len(data) < 1 {
				continue
			}
			op, body := data[0], data[1:]
			switch op {
			case tWSStdin, tWSResize:
				if udsWriteFrame(uds, op, body) != nil {
					return
				}
			case tWSExit:
				_ = udsWriteFrame(uds, tWSExit, nil)
				return
			}
		}
	}()

	<-stop
}

// notifyOpen publishes the M14 critical security event on session open.
// Best-effort: a queue hiccup must not block the operator's shell.
func (h *terminalHandler) notifyOpen(claims *auth.AccessClaims, sess *models.TerminalSession, ip string) {
	if h.cfg.Notifications == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = h.cfg.Notifications.Publish(ctx, notifications.Envelope{
		EventKind: "security.root_terminal.opened",
		Severity:  models.NotificationSeverityCritical,
		Title:     "Root web terminal opened",
		Body: fmt.Sprintf("Admin %s opened a root shell from %s. Session %s — recorded at %s.",
			claims.Email, ip, sess.ID, sess.CastPath),
		UserID: claims.UserID,
	})
}

// --- UDS framing (matches panel-agent terminal_pty.go) -------------------

func udsWriteFrame(c net.Conn, op byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = op
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := c.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := c.Write(payload)
		return err
	}
	return nil
}

func udsReadFrame(c net.Conn) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > (1 << 20) {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	buf := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(c, buf); err != nil {
			return 0, nil, err
		}
	}
	return hdr[0], buf, nil
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil && v > 0 && v < 100000 {
		return v
	}
	return def
}
