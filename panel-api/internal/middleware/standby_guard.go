package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// StandbyReadOnly (GH #331) blocks state-mutating API requests when this box is
// a DR standby. A standby's panel DB is a REPLICA of the primary — any tenant
// provisioning written here would diverge from the primary and be silently
// overwritten by the next sync, so the safe behaviour is to refuse the write
// outright (409) rather than accept-then-lose it. Read requests always pass, so
// the operator can inspect the replica; the DR-control + settings routes are
// allowlisted so they can still promote or re-pair the box.
//
// Fail-safe direction is toward "active primary": an unseeded row or a settings
// read error is treated as primary and lets writes through, because wrongly
// freezing a real live primary during a DB blip is far worse than a standby
// briefly accepting a write that the next sync discards anyway.

// standbyAllowlist is the set of /api/v1 path prefixes whose mutations are
// permitted even on a standby — the operator must be able to promote, unpair,
// and adjust the settings that drive those actions.
var standbyAllowlist = []string{
	"/api/v1/admin/dr",
	"/api/v1/admin/settings",
}

type standbyCache struct {
	mu      sync.RWMutex
	repo    repository.ServerSettingsRepository
	cached  *models.ServerSettings
	fetched time.Time
	ttl     time.Duration
	nowFunc func() time.Time
}

func (c *standbyCache) role(ctx *gin.Context) *models.ServerSettings {
	now := c.nowFunc()
	c.mu.RLock()
	if c.cached != nil && now.Sub(c.fetched) < c.ttl {
		s := c.cached
		c.mu.RUnlock()
		return s
	}
	c.mu.RUnlock()
	if c.repo == nil {
		return nil
	}
	s, err := c.repo.Get(ctx.Request.Context())
	if err != nil || s == nil {
		// Fail safe: reuse the last-known value, else nil → treated as primary.
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.cached
	}
	c.mu.Lock()
	c.cached, c.fetched = s, now
	c.mu.Unlock()
	return s
}

func standbyAllowed(path string) bool {
	for _, p := range standbyAllowlist {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// StandbyReadOnly returns the guard middleware bound to a short-TTL settings
// cache so a hot route doesn't issue a DB read per request.
func StandbyReadOnly(repo repository.ServerSettingsRepository, log *slog.Logger) gin.HandlerFunc {
	cache := &standbyCache{repo: repo, ttl: 5 * time.Second, nowFunc: time.Now}
	return func(c *gin.Context) {
		if !isMutating(c.Request.Method) {
			c.Next()
			return
		}
		s := cache.role(c)
		if s == nil || !s.IsStandby() {
			c.Next()
			return
		}
		if standbyAllowed(c.Request.URL.Path) {
			c.Next()
			return
		}
		if log != nil {
			log.Warn("standby refused a mutating request",
				"method", c.Request.Method, "path", c.Request.URL.Path)
		}
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{
			"error":  "server_is_standby",
			"detail": "This server is a DR standby (read-only replica). Promote it with `jabali dr promote` before making changes.",
		})
	}
}
