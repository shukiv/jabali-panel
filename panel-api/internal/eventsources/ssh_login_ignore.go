package eventsources

import (
	"context"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// sshIgnoreCache resolves the per-account ssh.login ignore list (GH #1310-
// adjacent, "drfeed spam") with a short TTL so the hot journal-tail path does
// not hit the DB on every login line. A noisy service account (a DR feed's SSH
// pull loop) listed here has its logins dropped before they reach the grouper —
// no immediate notification and no rolling digest.
//
// Fail-OPEN: if the settings read fails and no prior set is cached, nothing is
// ignored (we would rather over-notify than silently swallow a security signal).
type sshIgnoreCache struct {
	ss  repository.ServerSettingsRepository
	now Clock

	mu     sync.Mutex
	set    map[string]struct{}
	loaded time.Time
}

const sshIgnoreTTL = 30 * time.Second

func newSSHIgnoreCache(ss repository.ServerSettingsRepository, now Clock) *sshIgnoreCache {
	if now == nil {
		now = time.Now
	}
	return &sshIgnoreCache{ss: ss, now: now}
}

// ignored reports whether logins by user should be suppressed. A nil cache or
// nil repo (settings unwired) never ignores anyone.
func (c *sshIgnoreCache) ignored(ctx context.Context, user string) bool {
	if c == nil || c.ss == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.set == nil || c.now().Sub(c.loaded) > sshIgnoreTTL {
		s, err := c.ss.Get(ctx)
		if err == nil && s != nil {
			c.set = models.SSHIgnoreSet(s.SSHLoginIgnoreAccounts)
		} else if c.set == nil {
			c.set = map[string]struct{}{} // fail-open until a good read lands
		}
		c.loaded = c.now()
	}
	_, ok := c.set[user]
	return ok
}
