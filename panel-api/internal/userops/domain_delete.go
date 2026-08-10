package userops

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// JAB-236 — durable domain deletion.
//
// The panel row is the only handle, and every pre-existing delete path gave
// it up before the host-side teardown was safe: REST and the user cascade
// fired a goroutine that a panel restart silently lost, and the CLI
// dispatched nothing at all — deleted domains kept SERVING (live vhost,
// answering pdns zone, Stalwart accounts) with no retry handle left.
//
// The fix inverts #1010 for this shape: a delete-protected row must refuse
// (databases keep the row), but a deleted domain's teardown must be
// DURABLE — so the tombstone (domain_teardowns) is written BEFORE the row
// delete and cleared only after the teardown verifiably succeeds. The
// reconciler retries pending tombstones every sweep.
//
// Ordering matters twice over:
//   - Tombstone before row delete: a crash in between leaves a tombstone
//     whose live-row guard (the sweep checks FindByName) makes it a no-op.
//   - Stalwart purge INSIDE the executor, after the row is gone: the purge
//     destroys live mailboxes, so it must never run for a delete the repo
//     layer then refuses (panel-primary protection).

// teardownStep names one executor step for error reporting.
type teardownStep struct {
	name string
	call func(ctx context.Context) error
}

// ExecuteDomainTeardown runs the full host-side teardown for a domain whose
// panel row is already gone: Stalwart account purge, nginx vhost removal
// (domain.delete also reaps the mail vhost, relay cred, and logs), and the
// pdns zone. Every step is idempotent agent-side, so retries are safe. An
// error means the tombstone must stay for the reconciler to retry.
func ExecuteDomainTeardown(ctx context.Context, agent AgentCaller, name string) error {
	if agent == nil {
		return fmt.Errorf("domain teardown %s: no agent wired", name)
	}
	steps := []teardownStep{
		{"mail.domain.purge_accounts", func(ctx context.Context) error {
			return PurgeDomainMail(ctx, agent, name)
		}},
		{"domain.delete", func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := agent.Call(cctx, "domain.delete", map[string]string{"domain": name})
			return err
		}},
		{"dns.zone.delete", func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := agent.Call(cctx, "dns.zone.delete", map[string]string{"zone": name})
			// A box without the DNS module has no PowerDNS backend at all.
			// That is a permanent condition, not a transient failure — a
			// tombstone must not retry it forever.
			if err != nil && strings.Contains(err.Error(), "powerdns backend not available") {
				return nil
			}
			return err
		}},
	}
	for _, s := range steps {
		if err := s.call(ctx); err != nil {
			return fmt.Errorf("domain teardown %s: %s: %w", name, s.name, err)
		}
	}
	return nil
}

// DeleteDomain removes a domain durably. Row-delete errors (including the
// panel-primary protection) are returned verbatim with NOTHING host-side
// executed and no tombstone left behind. On success the row is gone;
// teardownPending reports whether the host-side teardown is still owed (the
// tombstone stays and the reconciler retries until it succeeds).
//
// async=true (REST, cascade) attempts the teardown in a detached goroutine
// so the caller returns immediately — durability no longer depends on that
// goroutine surviving, only on the tombstone. async=false (CLI) waits and
// reports the true outcome.
func DeleteDomain(ctx context.Context, d Deps, domainID, domainName string, async bool) (teardownPending bool, err error) {
	if d.Domains == nil {
		return false, ErrDeps
	}
	if d.DomainTeardowns != nil {
		if err := d.DomainTeardowns.Ensure(ctx, domainName); err != nil {
			return false, fmt.Errorf("create teardown tombstone: %w", err)
		}
	}
	if err := d.Domains.Delete(ctx, domainID); err != nil {
		// The delete was refused (panel-primary, FK, transient DB error):
		// the row remains the handle, so the tombstone must go — a stale
		// tombstone against a live row is a loaded gun for the sweep's
		// live-row guard to have to catch.
		if d.DomainTeardowns != nil {
			_ = d.DomainTeardowns.Delete(ctx, domainName)
		}
		return false, err
	}

	finish := func(ctx context.Context) bool {
		if terr := ExecuteDomainTeardown(ctx, d.Agent, domainName); terr != nil {
			logWarn(d, "domain delete: host teardown failed — tombstone kept for reconciler retry",
				"domain", domainName, "err", terr)
			if d.DomainTeardowns != nil {
				_ = d.DomainTeardowns.MarkAttempt(ctx, domainName, terr.Error())
			}
			return false
		}
		if d.DomainTeardowns != nil {
			_ = d.DomainTeardowns.Delete(ctx, domainName)
		}
		return true
	}

	if async {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			finish(bgCtx)
		}()
		return true, nil // pending until the goroutine (or the sweep) clears it
	}
	return !finish(ctx), nil
}
