// Package reconciler — Stalwart registry alias sweep.
//
// Stalwart copies the aliases the SQL directory returns into its own
// registry when an account signs in or receives mail, and it never takes one
// away. An alias that moved to another mailbox therefore kept delivering to
// the old one, and a mailbox created at a once-aliased address signed in to
// the old owner's account. The mailbox create doors clear the address before
// they write the row, and the alias doors clear it after they write theirs,
// but a door can fail (mail server down), and aliases left from before this
// sweep existed stay until something clears them.
//
// Every mailAddressSweepInterval this pass reads every account's aliases and
// takes an alias off an account when the panel's database gives the address
// to a different principal (mailaddrowner.Sweep). An alias the database
// gives to no one is left alone: the panel cannot tell a deleted alias from
// an address someone else manages.
package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailaddrowner"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailboxops"
)

// mailAddressSweepInterval is how often the sweep runs. It reads every
// account in the registry and one database row per alias.
const mailAddressSweepInterval = 10 * time.Minute

const mailAddressSweepTimeout = 60 * time.Second

// WithMailAddressOwners wires the registry alias sweep and the registry
// release the relay mailbox create needs. registry is Stalwart's management
// API (stalwartadmin.Client); owners is the database's view of who owns an
// address (repository.MailAddressOwnerRepository). A nil registry disables
// the sweep, and the relay mailbox create then refuses (mailboxops fails
// closed).
func (r *Reconciler) WithMailAddressOwners(registry mailaddrowner.Registry, owners mailaddrowner.Owners) *Reconciler {
	r.mailAddrRegistry = registry
	r.mailAddrOwners = owners
	return r
}

// mailAddressReleaser is the releaser the relay mailbox create passes to
// mailboxops: nil when no registry is wired, so the create refuses.
func (r *Reconciler) mailAddressReleaser() mailboxops.AddressReleaser {
	if r.mailAddrRegistry == nil {
		return nil
	}
	return mailaddrowner.Releaser{Registry: r.mailAddrRegistry}
}

// reconcileMailAddressOwners runs the sweep once per interval.
func (r *Reconciler) reconcileMailAddressOwners(ctx context.Context) {
	if r.mailAddrRegistry == nil || r.mailAddrOwners == nil {
		return
	}
	// The admin reconcile endpoint can start a full pass while the ticker's
	// is still running.
	if !r.mailAddrMu.TryLock() {
		return
	}
	defer r.mailAddrMu.Unlock()
	if !r.mailAddrLastRun.IsZero() && time.Since(r.mailAddrLastRun) < mailAddressSweepInterval {
		return
	}
	r.mailAddrLastRun = time.Now()

	cctx, cancel := context.WithTimeout(ctx, mailAddressSweepTimeout)
	defer cancel()
	removed, err := mailaddrowner.Sweep(cctx, r.mailAddrRegistry, r.mailAddrOwners)
	for _, rm := range removed {
		r.log.Warn("mail-address: took a stale alias off a mail account",
			"address", rm.Address, "account", rm.Account)
	}
	if err != nil {
		r.log.Warn("mail-address: sweep failed", "err", err)
	}
}
