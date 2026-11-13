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
// webmailBrandingMinInterval, so a save shows within about five minutes.
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
	r.webmailBrandingMu.Lock()
	defer r.webmailBrandingMu.Unlock()
	if time.Since(r.webmailBrandingSent) < webmailBrandingMinInterval {
		return // a later pass sends what changed meanwhile
	}
	params := map[string]any{
		"brand_text": brandText,
		"domains":    webmailbranding.AgentDomains(rows),
	}
	key := map[string]any{"params": params, "logos": logos}
	_, _ = r.project(ctx, PhaseWebmailBranding, "jabali-webmail", fingerprint(key), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, webmailAgentTimeout)
		defer cancel()
		if _, err := r.agent.Call(callCtx, "webmail.branding.apply", params); err != nil {
			r.log.Warn("reconcile webmail branding: apply failed", "err", err)
			return err
		}
		r.webmailBrandingSent = time.Now()
		return nil
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
