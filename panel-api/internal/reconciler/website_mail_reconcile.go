package reconciler

import (
	"context"
	"errors"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/websitemail"
)

// reconcileWebsiteMail (GH #2056, ADR 0174) keeps the server on the saved
// website-mail setting. The settings endpoint applies it on save; this loop
// covers what changes afterwards: in smarthost mode a new site, a suspended
// user or a newly verified domain reaches the relay's sender list within a
// tick, and in local mode the relay stays off. Fingerprint-gated, with a
// re-apply at least every websiteMailReapply so drift on the box heals.
const websiteMailReapply = 10 * time.Minute

type websiteMailState struct {
	mu sync.Mutex
	fp string
	at time.Time
}

func (r *Reconciler) reconcileWebsiteMail(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil || r.users == nil || r.domains == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.settingsGet(sctx)
	cancel()
	if err != nil || srv == nil {
		return
	}
	req, err := websitemail.Request(ctx, websitemail.Deps{Users: r.users, Domains: r.domains}, srv, r.sendmailSSOKey)
	if err != nil {
		r.log.Warn("website-mail: can't build the relay settings", "error", err)
		return
	}
	fp := websitemail.Fingerprint(req)
	r.websiteMail.mu.Lock()
	fresh := r.websiteMail.fp == fp && time.Since(r.websiteMail.at) < websiteMailReapply
	r.websiteMail.mu.Unlock()
	if fresh {
		return
	}

	actx, acancel := context.WithTimeout(ctx, 60*time.Second)
	resp, err := websitemail.Apply(actx, r.agent, req)
	acancel()
	if err != nil {
		var ae *agent.AgentError
		if !errors.As(err, &ae) || ae.Code != agent.CodeUnknownCommand {
			r.log.Warn("website-mail: apply failed", "mode", req.Mode, "error", err)
			return
		}
		// An agent from before the relay (mid-update): nothing to apply yet.
		// Try again after the re-apply window, not every tick.
	}
	r.websiteMail.mu.Lock()
	r.websiteMail.fp, r.websiteMail.at = fp, time.Now()
	r.websiteMail.mu.Unlock()
	if resp != nil && resp.Changed {
		r.log.Info("website-mail: applied", "mode", resp.Mode, "senders", resp.Senders, "skipped", resp.Skipped)
	}
}
