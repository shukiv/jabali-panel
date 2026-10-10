// Package reconciler — the mail server's app passwords and API keys.
//
// Stalwart keeps the app passwords and API keys a mailbox creates (webmail,
// Settings > Security) apart from the mailbox password it reads from the
// panel's database, so they went on working after the panel changed the
// password, disabled the mailbox or suspended its owner. This pass removes
// every API key, every app password of an account that is not a mailbox
// that may sign in, and each app password created at or before the
// mailbox's cutoff, a minute after its password last changed
// (mailcreds.Sweep).
//
// PhaseMailCredentials runs it on the first tick (the first passes after
// the 000318 migration remove the app passwords made before the update), on
// every tick where the list of mailboxes that may sign in or their cutoffs
// changed, once a cutoff still ahead has passed, and every audit interval. A
// failed run is retried on the next tick.
package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailcreds"
)

const mailCredentialsTimeout = 2 * time.Minute

// WithMailCredentials wires the mail credentials pass. registry is
// Stalwart's management API (stalwartadmin.Client); logins lists the
// mailboxes that may sign in (repository.MailLoginRepository). nil on either
// disables the pass.
func (r *Reconciler) WithMailCredentials(registry mailcreds.Registry, logins mailcreds.Logins) *Reconciler {
	r.mailCredRegistry = registry
	r.mailCredLogins = logins
	return r
}

// reconcileMailCredentials sweeps the mail server's app passwords and API
// keys against the mailboxes that may sign in.
func (r *Reconciler) reconcileMailCredentials(ctx context.Context) {
	if r.mailCredRegistry == nil || r.mailCredLogins == nil {
		return
	}
	// A server without the mail module has no mail server to sweep. Skip on
	// a positive "mail off" reading only: settings that can't be read keep
	// the pass.
	if r.serverSettings != nil {
		sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
		srv, err := r.settingsGet(sctx)
		scancel()
		if err == nil && srv != nil && !srv.MailEnabled {
			return
		}
	}
	logins, err := r.mailCredLogins.ListMailLogins(ctx)
	if err != nil {
		r.log.Warn("mail-credentials: list the mailboxes that may sign in failed; nothing removed", "error", err)
		return
	}
	// A cutoff still ahead (a password changed in the last minute) passes
	// later; counting them in the fingerprint runs the pass again then.
	now := time.Now()
	if r.mailCredNow != nil {
		now = r.mailCredNow()
	}
	ahead := 0
	for _, cutoff := range logins {
		if cutoff.After(now) {
			ahead++
		}
	}
	desired := struct {
		Logins map[string]time.Time
		Ahead  int
	}{logins, ahead}
	_, _ = r.project(ctx, PhaseMailCredentials, "all", fingerprint(desired), false, func() error {
		cctx, cancel := context.WithTimeout(ctx, mailCredentialsTimeout)
		defer cancel()
		removed, err := mailcreds.Sweep(cctx, r.mailCredRegistry, logins)
		for _, rm := range removed {
			r.log.Info("mail-credentials: removed a credential from a mail account",
				"account", rm.Account, "type", rm.Type, "reason", rm.Reason)
		}
		if len(removed) > 0 && r.agent != nil {
			// The mail server answers webmail logins from a cache (GH #1925).
			fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
			if _, ferr := r.agent.Call(fctx, "mail.auth_cache.flush", map[string]any{}); ferr != nil {
				r.log.Warn("mail-credentials: login cache flush failed", "error", ferr)
			}
			fcancel()
		}
		if err != nil {
			r.log.Warn("mail-credentials: sweep failed; will retry next tick", "error", err)
		}
		return err
	})
}
