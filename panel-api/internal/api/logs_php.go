package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The "php" log type: a domain's PHP error log, ~/logs/<domain>.php-error.log
// in the owner's home (written by PHP since the vhost pins error_log). The dir
// is tenant-owned 0750, so panel-api cannot read it the way it tails the nginx
// logs; the agent reads it (php.error_log.read) without following anything
// the tenant could plant, and returns the lines plus where the next read
// continues. /logs/tail asks for the last lines; the live stream polls.

// phpErrorLogRead is the agent's php.error_log.read response.
type phpErrorLogRead struct {
	Lines []string `json:"lines"`
	// Offset and Ino are where the next read continues.
	Offset int64  `json:"offset"`
	Ino    uint64 `json:"ino"`
	// Missing: nothing logged yet (or since the last rotation).
	Missing bool `json:"missing,omitempty"`
}

// errNoPHPAccount: the domain's owner has no Linux account (an admin-owned
// domain), so it has no PHP pool and no PHP error log.
var errNoPHPAccount = errors.New("the domain's owner has no hosting account")

// tailPHPErrorLog is /logs/tail for log_type=php. The caller has checked the
// domain belongs to the caller (or the caller is an admin).
func (h *logHandler) tailPHPErrorLog(c *gin.Context, domain *models.Domain, lines int) {
	if h.cfg.Agent == nil || h.cfg.Users == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent not available"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	res, err := readDomainPHPErrorLog(ctx, h.cfg.Agent, h.cfg.Users, domain, -1, 0, lines)
	if errors.Is(err, errNoPHPAccount) {
		c.JSON(http.StatusOK, gin.H{
			"domain_id": domain.ID, "log_type": "php", "lines": []string{},
			"note": "this domain has no PHP account",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "PHP error log unavailable"})
		return
	}
	out := gin.H{"domain_id": domain.ID, "log_type": "php", "lines": res.Lines}
	if res.Missing {
		out["note"] = "log is empty or not yet created"
	}
	c.JSON(http.StatusOK, out)
}

// phpErrorLogPollInterval is how often the live PHP error log view asks the
// agent for new lines, unless LogStreamHandlerConfig sets its own.
const phpErrorLogPollInterval = 2 * time.Second

// streamPHPErrorLog is the live view of a domain's PHP error log: the last
// lines, then whatever is appended, read through the agent every
// phpErrorLogPollInterval until the stream expires or the peer goes away
// (closed). The agent restarts from the tail when the log was rotated.
func (h *logStreamHandler) streamPHPErrorLog(conn *websocket.Conn, stream *models.LogAccessStream, closed <-chan struct{}) {
	if stream.DomainID == nil || h.cfg.Agent == nil || h.cfg.Users == nil {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseUnsupportedData, "the PHP error log is per domain"))
		return
	}
	interval := h.cfg.PHPErrorLogPollInterval
	if interval <= 0 {
		interval = phpErrorLogPollInterval
	}
	domain, err := h.cfg.Domains.FindByID(context.Background(), *stream.DomainID)
	if err != nil {
		h.cfg.Log.Error("failed to find domain", "domain_id", *stream.DomainID, "err", err)
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "domain not found"))
		return
	}

	offset, ino := int64(-1), uint64(0)
	first := true
	for {
		if time.Now().After(stream.ExpiresAt) {
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "stream expired"))
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		res, err := readDomainPHPErrorLog(ctx, h.cfg.Agent, h.cfg.Users, domain, offset, ino, 50)
		cancel()
		if errors.Is(err, errNoPHPAccount) {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			conn.WriteMessage(websocket.TextMessage, []byte("This domain has no PHP account, so it has no PHP error log.\n"))
			return
		}
		if err != nil {
			h.cfg.Log.Warn("php error log read failed", "domain", domain.Name, "err", err)
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "PHP error log unavailable"))
			return
		}
		var msgs []string
		if first && res.Missing {
			msgs = append(msgs, "No PHP errors logged yet. New ones appear here as they are written.")
		}
		first = false
		msgs = append(msgs, res.Lines...)
		for _, m := range msgs {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, []byte(m+"\n")); err != nil {
				return
			}
		}
		if res.Missing {
			// Nothing to follow yet: read the tail once the file appears.
			offset, ino = -1, 0
		} else {
			offset, ino = res.Offset, res.Ino
		}

		select {
		case <-closed:
			return
		case <-time.After(interval):
		}
	}
}

// readDomainPHPErrorLog reads the domain's PHP error log through the agent.
// offset < 0 returns the last lines; otherwise what follows offset in the
// file with inode ino.
func readDomainPHPErrorLog(ctx context.Context, ag agent.AgentInterface, users repository.UserRepository, domain *models.Domain, offset int64, ino uint64, lines int) (phpErrorLogRead, error) {
	out := phpErrorLogRead{Lines: []string{}}
	owner, err := users.FindByID(ctx, domain.UserID)
	if err != nil {
		return out, fmt.Errorf("find domain owner: %w", err)
	}
	if owner.Username == nil || *owner.Username == "" {
		return out, errNoPHPAccount
	}
	raw, err := ag.Call(ctx, "php.error_log.read", map[string]any{
		"username": *owner.Username,
		"domain":   domain.Name,
		"offset":   offset,
		"ino":      ino,
		"lines":    lines,
	})
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode php.error_log.read: %w", err)
	}
	if out.Lines == nil {
		out.Lines = []string{}
	}
	return out, nil
}
