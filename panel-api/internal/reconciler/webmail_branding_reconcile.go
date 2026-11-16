package reconciler

import (
	"context"
	"errors"
	"os"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailbranding"
)

// webmailBrandingMinInterval is the least time between two sends. A send
// that changes the branding restarts webmail for every domain, so saves
// closer together than this wait and go out as one.
const webmailBrandingMinInterval = 5 * time.Minute

// WithWebmailBranding injects the domains' webmail branding (GH #2502).
func (r *Reconciler) WithWebmailBranding(repo repository.DomainWebmailBrandingRepository) *Reconciler {
	r.webmailBranding = repo
	return r
}

// reconcileWebmailBranding sends webmail its branding: the server-wide
// brand text, which the agent pairs with the panel's logos, and the entry of
// every domain whose branding applies (an administrator set it, or the
// owner's package has webmail branding). The agent writes them to webmail's
// branding environment file and restarts webmail only when that changed.
//
// It is the only sender: every call carries the whole branding, so many
// saves cost one webmail restart. It sends at most once in
// webmailBrandingMinInterval. A save runs it at once (WebmailBrandingSoon);
// a change it holds back for the interval goes out when the interval ends.
// A domain's branding can drop out while its row stays
// (its owner's package lost webmail branding); the pictures of a domain with
// no row any more (it was deleted) are removed here.
func (r *Reconciler) reconcileWebmailBranding(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil || r.webmailBranding == nil {
		return
	}
	brandText := ""
	var logos []any
	s, err := r.settingsGet(ctx)
	switch {
	case err == nil && s != nil:
		// Saving it only trims it; the agent refuses a control character.
		brandText = agentwire.CleanWebmailBrandingText(s.PanelBrandText)
		// The agent reads the logo files itself; their size and time make
		// a new upload a change.
		for _, p := range []string{s.LogoLightPath, s.LogoDarkPath} {
			logos = append(logos, fileStamp(p))
		}
	case errors.Is(err, repository.ErrNotFound):
	default:
		r.log.Warn("reconcile webmail branding: server settings", "err", err)
		return
	}
	rows, err := r.webmailBranding.ListAll(ctx)
	if err != nil {
		r.log.Warn("reconcile webmail branding: list", "err", err)
		return
	}
	dir := r.webmailBrandingDir
	if dir == "" {
		dir = webmailbranding.Dir
	}
	keep := make(map[string]bool, len(rows))
	for _, row := range rows {
		keep[row.DomainID] = true
	}
	if err := webmailbranding.Prune(dir, keep); err != nil {
		r.log.Warn("reconcile webmail branding: remove a deleted domain's pictures", "err", err)
	}
	params := map[string]any{
		"brand_text": brandText,
		"domains":    webmailbranding.AgentDomains(rows),
	}
	key := fingerprint(map[string]any{"params": params, "logos": logos})
	r.webmailBrandingMu.Lock()
	defer r.webmailBrandingMu.Unlock()
	if due := r.webmailBrandingDue(time.Now()); due.After(time.Now()) {
		// A change waits for the interval to pass, then goes out at once
		// rather than on the first pass after it.
		if key != r.webmailBrandingSentKey {
			r.retryWebmailBrandingAt(due)
		}
		return
	}
	_, _ = r.project(ctx, PhaseWebmailBranding, "jabali-webmail", key, false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		if _, err := r.agent.Call(callCtx, "webmail.branding.apply", params); err != nil {
			r.log.Warn("reconcile webmail branding: apply failed", "err", err)
			return err
		}
		r.webmailBrandingSent.Store(time.Now().UnixNano())
		r.webmailBrandingSentKey = key
		return nil
	})
}

// WebmailBrandingSoon has the branding sent to webmail now, out of band,
// instead of on the next pass (GH #2502: a save shows at once). It returns
// about when webmail shows it: now, or when webmailBrandingMinInterval has
// passed since the last send, which is when a change held back goes out. Safe
// to call from request handlers: it never waits for a send under way.
func (r *Reconciler) WebmailBrandingSoon() time.Time {
	r.ScheduleResource(ResourceKey{Kind: KindWebmailBranding})
	return r.webmailBrandingDue(time.Now())
}

// webmailBrandingDue is the earliest time the branding may be sent again: now,
// or webmailBrandingMinInterval after the last send.
func (r *Reconciler) webmailBrandingDue(now time.Time) time.Time {
	if sent := r.webmailBrandingSent.Load(); sent != 0 {
		if due := time.Unix(0, sent).Add(webmailBrandingMinInterval); due.After(now) {
			return due
		}
	}
	return now
}

// retryWebmailBrandingAt schedules the phase out of band at due, unless that
// is already scheduled. Called with webmailBrandingMu held.
func (r *Reconciler) retryWebmailBrandingAt(due time.Time) {
	if !r.webmailBrandingRetryAt.Before(due) {
		return
	}
	r.webmailBrandingRetryAt = due
	after := r.webmailBrandingAfterFunc
	if after == nil {
		after = func(d time.Duration, f func()) { time.AfterFunc(d, f) }
	}
	// A moment past due, so the pass is not still inside the interval.
	after(time.Until(due)+time.Second, func() {
		r.ScheduleResource(ResourceKey{Kind: KindWebmailBranding})
	})
}

// fileStamp is a file's path, size and modification time, or only its path
// when it is missing.
func fileStamp(path string) []any {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return []any{path}
	}
	return []any{path, fi.Size(), fi.ModTime().UTC().Format(time.RFC3339Nano)}
}
