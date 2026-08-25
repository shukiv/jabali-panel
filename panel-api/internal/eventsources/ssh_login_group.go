package eventsources

import (
	"fmt"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// Smart grouping for ssh.login (GH #1238-adjacent, "drfeed spam"): a noisy
// account (a per-minute rsync/backup/feed loop over publickey) otherwise fires
// one notification per login and floods the admin inbox. Instead we group by
// user + source IP + auth method (the server is implicit — one box per panel):
//
//   - first login in a new group      → notify immediately (the security signal)
//   - repeats within the group window → suppressed, only a counter increments
//   - group quiet for the window      → emit a summary "N logins in ~M minutes"
//   - continuously active past a window→ emit a rolling summary + start a new
//     window, so a never-quiet feed still surfaces a bounded, counted digest
//   - a new IP or auth method          → a distinct group → fresh immediate notify
//
// This lives entirely in the eventsource (no schema/dispatcher change). True
// single-notification live-update and the per-event policy modes (every / smart
// / threshold / digest / disabled) are the follow-up.
const (
	// sshGroupWindow is both the quiet gap that closes a group and the rolling
	// digest cadence for a continuously-active one.
	sshGroupWindow = 10 * time.Minute
	// sshFlushInterval paces the ticker that emits quiet-close + rolling digest
	// summaries. Finer than the window so a summary lands within ~a minute of a
	// group going quiet.
	sshFlushInterval = 1 * time.Minute
)

type sshGroup struct {
	user, ip, method string
	firstAt, lastAt  time.Time // firstAt resets each rolling window
	count            int       // logins in the current window
}

// sshLoginGrouper aggregates ssh.login events. Safe for concurrent use: the
// journal-tail goroutine calls observe, the flush ticker calls flush.
type sshLoginGrouper struct {
	now    Clock
	mu     sync.Mutex
	groups map[string]*sshGroup
}

func newSSHLoginGrouper(now Clock) *sshLoginGrouper {
	if now == nil {
		now = time.Now
	}
	return &sshLoginGrouper{now: now, groups: map[string]*sshGroup{}}
}

func sshGroupKey(user, ip, method string) string {
	return user + "\x00" + ip + "\x00" + method
}

// observe records a login and returns the envelopes to publish now: an immediate
// notification when the login opens a new group (optionally preceded by the
// summary of a just-expired group for the same key), or nothing when it is a
// suppressed repeat.
func (g *sshLoginGrouper) observe(user, ip, method string) []notifications.Envelope {
	now := g.now()
	key := sshGroupKey(user, ip, method)

	g.mu.Lock()
	defer g.mu.Unlock()

	cur, ok := g.groups[key]
	if ok && now.Sub(cur.lastAt) <= sshGroupWindow {
		// Open group — suppress, just count.
		cur.count++
		cur.lastAt = now
		return nil
	}

	var out []notifications.Envelope
	// A stale group for this key expired without the ticker flushing it yet;
	// emit its summary before the new group replaces it.
	if ok && cur.count > 1 {
		out = append(out, sshSummaryEnvelope(cur, now))
	}
	out = append(out, sshImmediateEnvelope(user, ip, method))
	g.groups[key] = &sshGroup{user: user, ip: ip, method: method, firstAt: now, lastAt: now, count: 1}
	return out
}

// flush closes quiet groups (emitting a summary for any with repeats) and emits
// a rolling summary for groups that have stayed active across a full window,
// resetting their window so the next digest counts fresh. Call on a ticker.
func (g *sshLoginGrouper) flush() []notifications.Envelope {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()

	var out []notifications.Envelope
	for key, grp := range g.groups {
		if now.Sub(grp.lastAt) > sshGroupWindow {
			// Quiet → close.
			if grp.count > 1 {
				out = append(out, sshSummaryEnvelope(grp, now))
			}
			delete(g.groups, key)
			continue
		}
		if now.Sub(grp.firstAt) >= sshGroupWindow && grp.count > 1 {
			// Continuously active across a full window → rolling digest, then
			// start a new window (keep the group open).
			out = append(out, sshSummaryEnvelope(grp, now))
			grp.firstAt = now
			grp.count = 0
		}
	}
	return out
}

func sshImmediateEnvelope(user, ip, method string) notifications.Envelope {
	body := fmt.Sprintf("User %s logged in over SSH from %s", user, ip)
	if method != "" {
		body += fmt.Sprintf(" via %s", method)
	}
	body += fmt.Sprintf(". (ssh:%s@%s)", user, ip)
	return notifications.Envelope{
		EventKind: "ssh.login",
		Severity:  models.NotificationSeverityInfo,
		Title:     fmt.Sprintf("SSH login: %s from %s", user, ip),
		Body:      body,
		Deeplink:  "/jabali-admin/security",
	}
}

func sshSummaryEnvelope(grp *sshGroup, now time.Time) notifications.Envelope {
	mins := int((now.Sub(grp.firstAt) + 30*time.Second) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	via := ""
	if grp.method != "" {
		via = fmt.Sprintf(" via %s", grp.method)
	}
	body := fmt.Sprintf("%s logged in over SSH %d times from %s%s in the last %d minute(s). (ssh:%s@%s)",
		grp.user, grp.count, grp.ip, via, mins, grp.user, grp.ip)
	return notifications.Envelope{
		EventKind: "ssh.login",
		Severity:  models.NotificationSeverityInfo,
		Title:     fmt.Sprintf("SSH login: %s — %d logins from %s", grp.user, grp.count, grp.ip),
		Body:      body,
		Deeplink:  "/jabali-admin/security",
	}
}
