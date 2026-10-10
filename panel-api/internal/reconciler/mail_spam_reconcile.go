// Package reconciler — the mail server's spam score thresholds (GH #2017).
//
// The admin sets them in Server Settings → Email (server_settings); this
// pass writes them into Stalwart's SpamSettings and reloads Stalwart's
// settings so they take effect. install.sh no longer sets them, so an update
// keeps the admin's values.
//
// PhaseMailSpamScores runs it on the first tick, when the stored thresholds
// change, and every audit interval (which puts back a threshold changed on
// the mail server by hand). A failed run is retried on the next tick; a run
// whose write landed but whose reload failed retries the reload.
package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailspam"
)

const mailSpamScoresTimeout = 30 * time.Second

// SpamScoresApplier writes the thresholds into the mail server. The panel's
// implementation is stalwartadmin.SpamScores; tests inject a fake.
type SpamScoresApplier interface {
	Apply(ctx context.Context, want mailspam.Scores, reload bool) (changed bool, err error)
}

// WithMailSpamScores wires the spam thresholds pass. nil disables it.
func (r *Reconciler) WithMailSpamScores(applier SpamScoresApplier) *Reconciler {
	r.mailSpamScores = applier
	return r
}

// reconcileMailSpamScores converges the mail server's spam thresholds to
// server_settings.
func (r *Reconciler) reconcileMailSpamScores(ctx context.Context) {
	if r.mailSpamScores == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.settingsGet(sctx)
	scancel()
	if err != nil || srv == nil {
		return
	}
	// A server without the mail module has no mail server to configure.
	if !srv.MailEnabled {
		return
	}
	want := mailspam.Scores{Junk: srv.SpamJunkScore, Reject: srv.SpamRejectScore, Discard: srv.SpamDiscardScore}
	if err := want.Validate(); err != nil {
		r.log.Warn("mail-spam-scores: stored thresholds are not valid; mail server left as it is", "error", err)
		return
	}
	_, _ = r.project(ctx, PhaseMailSpamScores, "all", fingerprint(want), false, func() error {
		cctx, cancel := context.WithTimeout(ctx, mailSpamScoresTimeout)
		defer cancel()
		changed, err := r.mailSpamScores.Apply(cctx, want, r.mailSpamReloadPending.Load())
		if changed {
			r.log.Info("mail-spam-scores: set the mail server's spam thresholds",
				"junk", want.Junk, "reject", want.Reject, "discard", want.Discard)
		}
		if err != nil {
			if changed {
				// Stored on the mail server but not live: reload next time
				// even though nothing will differ.
				r.mailSpamReloadPending.Store(true)
			}
			r.log.Warn("mail-spam-scores: apply failed; will retry next tick", "error", err)
			return err
		}
		r.mailSpamReloadPending.Store(false)
		return nil
	})
}
