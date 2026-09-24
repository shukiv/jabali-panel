package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/forwarderops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileMailboxSieve (GH #1795) converges every mailbox that has an external
// forwarder or an autoresponder into its single active standard SieveScript —
// the store Stalwart actually executes at delivery. It is the fleet backfill:
// jabali historically wrote external forwards to the x:SieveUserScript extension
// store (never run at delivery) and the autoresponder to a separate
// VacationResponse object, so on existing boxes those rules silently no-op until
// a tenant re-saves them. This per-tick sweep re-asserts the correct composite
// script for the whole fleet on the first reconcile after `jabali update`, with
// no one-shot migration tool, and self-heals drift thereafter.
//
// Cost profile: mailbox.sieve.apply is ~8 JMAP round-trips with a 15s timeout,
// far heavier than the sendmail cred file write, so this sweep MUST stay cheap
// in steady state. It reads the two rule tables in full ONCE per tick
// (ListAll × 2) and one batch mailbox load, groups by mailbox_id in memory, and
// hashes each mailbox's rule set as a CHANGE DETECTOR against an in-memory
// done-cache. A cache hit is a map lookup — no agent call. The hash is only a
// detector: if it ever drifts from what Converge re-reads, the cost is one extra
// idempotent apply, never a wrong state (Converge itself re-reads the DB, which
// is truth). At most sieveConvergeBudgetPerTick mailboxes converge per tick; the
// rest are picked up on following ticks, so the first tick on a box with many
// forwarders cannot stall the whole ReconcileAll pass with hundreds of
// serialized multi-second agent calls.
//
// A mailbox whose rules were removed drops out of both ListAll results and is
// simply not swept — its live state was already cleared by the API/CLI delete
// path (which calls Converge with the empty composite). The sweep exists to fix
// EXISTING rules written to the wrong store, not to garbage-collect removed ones.
//
// The done-cache is process-local and resets on restart, so a `jabali update`
// (which restarts panel-api) re-evaluates every mailbox exactly once — the
// converge is a content-compare noop when nothing changed.
const sieveConvergeBudgetPerTick = 50

// sieveReDispatchInterval self-heals Stalwart-side drift the DB fingerprint
// cannot see (a script deactivated or deleted out-of-band — an admin in
// Stalwart's UI, webmail writing a native VacationResponse, a Stalwart restore):
// even on a cache hit, a mailbox not re-converged within this window is treated
// as a miss so the composite is re-asserted. Matches the ssh-keys / ftp / domain
// dispatch caches (15m). The per-tick budget bounds the periodic re-sweep.
const sieveReDispatchInterval = 15 * time.Minute

// sieveDoneEntry is the done-cache value: the last rule-set fingerprint applied
// to a mailbox and when. The timestamp drives the periodic self-heal above.
type sieveDoneEntry struct {
	hash string
	at   time.Time
}

func (r *Reconciler) reconcileMailboxSieve(ctx context.Context) {
	if r.agent == nil || r.mailboxes == nil || r.sieveForwarders == nil || r.sieveAutoresponders == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.serverSettings.Get(sctx)
	scancel()
	if err != nil || srv == nil {
		return
	}
	// Server-side mail rules are a mail-module artifact: with the mail module
	// off there is no Stalwart to apply the composite to. Skip entirely — same
	// gate reconcileSendmailCreds uses.
	if !srv.MailEnabled {
		return
	}

	// Group the desired rule state by mailbox_id. Only type=external, enabled
	// forwarders compile to redirect(s); alias forwarders are served by the SQL
	// directory, not Sieve. A forwarder with a NULL mailbox_id cannot be applied
	// by the per-mailbox mailbox.sieve.apply and is skipped (it is not converged
	// by the API path either).
	// Limit 0 on the raw repo means no LIMIT clause — the full table. (This is
	// the repository method, not the HTTP list envelope, so the #1796 over-max
	// page_size clamp does not apply here; a backfill must see every row.)
	fwds, _, err := r.sieveForwarders.ListAll(ctx, repository.ListOptions{})
	if err != nil {
		r.log.Warn("mailbox-sieve: list forwarders failed", "error", err)
		return
	}
	ars, err := r.sieveAutoresponders.ListAll(ctx)
	if err != nil {
		r.log.Warn("mailbox-sieve: list autoresponders failed", "error", err)
		return
	}

	byMailbox := make(map[string]*sieveDesired)
	get := func(id string) *sieveDesired {
		d := byMailbox[id]
		if d == nil {
			d = &sieveDesired{}
			byMailbox[id] = d
		}
		return d
	}
	for i := range fwds {
		f := &fwds[i]
		if f.MailboxID == nil || *f.MailboxID == "" {
			continue
		}
		if !f.Enabled || f.Type != "external" {
			continue
		}
		d := get(*f.MailboxID)
		d.forwards = append(d.forwards, f)
	}
	for i := range ars {
		a := &ars[i]
		if a.MailboxID == "" {
			continue
		}
		get(a.MailboxID).autoresponder = a
	}
	if len(byMailbox) == 0 {
		return
	}

	// One batch load for the emails — mailbox.sieve.apply is keyed by email.
	ids := make([]string, 0, len(byMailbox))
	for id := range byMailbox {
		ids = append(ids, id)
	}
	mbs, err := r.mailboxes.FindByIDs(ctx, ids)
	if err != nil {
		r.log.Warn("mailbox-sieve: batch load mailboxes failed", "error", err)
		return
	}
	emailByID := make(map[string]string, len(mbs))
	for i := range mbs {
		emailByID[mbs[i].ID] = mbs[i].EmailCached
	}

	r.sieveMu.Lock()
	if r.sieveDone == nil {
		r.sieveDone = make(map[string]sieveDoneEntry)
	}
	r.sieveMu.Unlock()

	now := time.Now()
	converged := 0
	for id, desired := range byMailbox {
		if converged >= sieveConvergeBudgetPerTick {
			return // remaining mailboxes converge on following ticks
		}
		email := emailByID[id]
		if email == "" {
			continue // mailbox row gone; its rules are orphaned, skip
		}
		want := sieveFingerprint(desired)
		r.sieveMu.Lock()
		e := r.sieveDone[id]
		// Skip only when the rule set is unchanged AND it was applied recently —
		// a stale entry falls through to a re-converge so out-of-band drift heals.
		done := e.hash == want && now.Sub(e.at) < sieveReDispatchInterval
		r.sieveMu.Unlock()
		if done {
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := forwarderops.Converge(cctx, r.agent, r.sieveForwarders, r.sieveAutoresponders, id, email)
		cancel()
		converged++
		if err != nil {
			// Stays out of the done-cache → retried next tick.
			r.log.Warn("mailbox-sieve: converge failed", "mailbox", email, "error", err)
			continue
		}
		r.sieveMu.Lock()
		r.sieveDone[id] = sieveDoneEntry{hash: want, at: now}
		r.sieveMu.Unlock()
	}
}

// sieveDesired is the in-memory grouping of a mailbox's rule rows for one tick.
// forwards holds only enabled, type=external forwarders (pointers into the
// ListAll slice — read-only, never mutated).
type sieveDesired struct {
	forwards      []*models.EmailForwarder
	autoresponder *models.EmailAutoresponder
}

// sieveFingerprint hashes the fields that change the rendered composite script,
// so a steady-state tick is a map compare. It is a change detector only — the
// authoritative render happens in forwarderops.Converge, which re-reads the DB.
func sieveFingerprint(d *sieveDesired) string {
	// Order-independent: sort forwarders by target so row ordering in the DB
	// list cannot flip the fingerprint.
	lines := make([]string, 0, len(d.forwards))
	for _, f := range d.forwards {
		lines = append(lines, f.Target+"\x1f"+strconv.FormatBool(f.KeepCopy))
	}
	sort.Strings(lines)

	h := sha256.New()
	for _, l := range lines {
		fmt.Fprintf(h, "f:%s\x1e", l)
	}
	if a := d.autoresponder; a != nil {
		fmt.Fprintf(h, "a:%v\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\x1e",
			a.Enabled,
			sieveTime(a.FromDate), sieveTime(a.ToDate),
			sieveStr(a.Subject), sieveStr(a.TextBody), sieveStr(a.HTMLBody))
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

func sieveTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func sieveStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// WithMailboxSieve wires the GH #1795 forward+autoresponder → single active
// standard SieveScript backfill sweep. Both repos required; nil on either
// disables the loop (the API/CLI convergence path still fixes rules as they are
// saved). Shares the mailbox repo already wired via WithSendmailCreds for the
// email lookup.
func (r *Reconciler) WithMailboxSieve(forwarders repository.EmailForwarderRepository, autoresponders repository.EmailAutoresponderRepository) *Reconciler {
	r.sieveForwarders = forwarders
	r.sieveAutoresponders = autoresponders
	return r
}
