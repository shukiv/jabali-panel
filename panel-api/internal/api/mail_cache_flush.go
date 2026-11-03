package api

import (
	"context"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// mailCacheFlushTimeout bounds the agent call. The agent runs Stalwart's
// InvalidateCaches action, at most once per few seconds (a change inside that
// gap gets one trailing flush).
const mailCacheFlushTimeout = 10 * time.Second

// flushMailCaches asks the agent to drop Stalwart's caches after a change that
// takes an address away from where its mail went: an alias or forwarder
// deleted, a group member removed, a group deleted, a catch-all changed.
//
// Stalwart 1.0 caches recipient lookups. After an alias was deleted from the
// panel's database, 1.0 went on accepting mail for it (250) until its caches
// were dropped; 0.16.27 refused it at once (seen on 1.0.2, 2026-11-03). A new
// address needs no flush: 1.0 accepted an alias added a moment after it had
// refused it. The flush is harmless on 0.16.
//
// Best effort: the change itself is already saved, so a failed flush is
// logged, not returned.
func flushMailCaches(ctx context.Context, ag agent.AgentInterface, after string) {
	if ag == nil {
		return
	}
	actx, cancel := context.WithTimeout(ctx, mailCacheFlushTimeout)
	defer cancel()
	if _, err := ag.Call(actx, "mail.auth_cache.flush", map[string]any{}); err != nil {
		slog.Warn("mail cache flush failed", "after", after, "err", err)
	}
}
