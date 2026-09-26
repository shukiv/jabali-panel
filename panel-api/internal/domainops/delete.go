package domainops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Delete is the domain lifecycle module's one delete entrypoint (JAB-279).
// The REST delete door, `jabali domain delete`, and the user-delete cascade
// (which the billing cancel also runs) are adapters: each resolves and
// authorizes the domain, then calls Delete and maps the error it returns. The
// reconciler's tombstone sweep retries a failed teardown through
// ExecuteTeardown.
//
// JAB-236 — deletion is durable. The panel row is the only handle, and every
// earlier delete path gave it up before the host-side teardown was safe: REST
// and the user cascade fired a goroutine that a panel restart silently lost,
// and the CLI dispatched nothing at all — deleted domains kept SERVING (live
// vhost, answering pdns zone, Stalwart accounts) with no retry handle left.
//
// So the tombstone (domain_teardowns) is written BEFORE the row delete and
// cleared only after the teardown verifiably succeeds. The reconciler retries
// pending tombstones every sweep. A delete-protected row must still refuse
// (the repository keeps the panel-primary row).
//
// Ordering matters twice over:
//   - Tombstone before row delete: a crash in between leaves a tombstone whose
//     live-row guard (the sweep checks FindByName) makes it a no-op.
//   - Stalwart purge INSIDE the teardown, after the row is gone: the purge
//     destroys live mailboxes, so it must never run for a delete the
//     repository then refuses (panel-primary protection).

// ErrDeleteDeps means Delete was called without a domain store — a wiring
// bug, not a policy result.
var ErrDeleteDeps = errors.New("domainops: delete dependencies are not wired")

// DomainDeleter is the slice of the domain repository Delete needs.
type DomainDeleter interface {
	Delete(ctx context.Context, id string) error
}

// DeleteDeps wires Delete.
type DeleteDeps struct {
	// Domains deletes the row. Required.
	Domains DomainDeleter
	// Teardowns persists the JAB-236 tombstones. Nil keeps the pre-tombstone
	// behavior: the teardown is attempted once and not retried.
	Teardowns repository.DomainTeardownRepository
	// Ports releases the domain's reverse-proxy port. Nil skips the release;
	// a domain with no reservation is a no-op either way.
	Ports repository.PortAllocationRepository
	// Agent runs the host-side teardown. Nil fails the teardown, which leaves
	// the tombstone for the reconciler. Callers holding a concrete client
	// pointer must leave this nil when the pointer is nil.
	Agent agent.AgentInterface
	// Log records the failures a delete survives. Nil uses slog.Default().
	Log *slog.Logger
}

// Delete removes a domain durably. A row-delete error (including the
// panel-primary refusal) is returned unchanged, with NOTHING run host-side and
// no tombstone left behind. On success the row is gone; teardownPending
// reports whether the host-side teardown is still owed (the tombstone stays
// and the reconciler retries until it succeeds).
//
// async=true (REST, cascade) attempts the teardown in a detached goroutine so
// the caller returns at once — durability depends on the tombstone, not on
// that goroutine surviving. async=false (CLI) waits and reports the outcome.
func Delete(ctx context.Context, d DeleteDeps, domainID, domainName string, async bool) (teardownPending bool, err error) {
	if d.Domains == nil {
		return false, ErrDeleteDeps
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	if d.Teardowns != nil {
		if err := d.Teardowns.Ensure(ctx, domainName); err != nil {
			return false, fmt.Errorf("create teardown tombstone: %w", err)
		}
	}
	if err := d.Domains.Delete(ctx, domainID); err != nil {
		// The delete was refused (panel-primary, FK, transient DB error): the
		// row remains the handle, so the tombstone must go — a stale tombstone
		// against a live row is a loaded gun for the sweep's live-row guard.
		if d.Teardowns != nil {
			_ = d.Teardowns.Delete(ctx, domainName)
		}
		return false, err
	}

	// GH #1175 / AC4: the row is gone, so free any reverse-proxy port it
	// reserved. The row was the port's last handle — port_allocations has no
	// foreign key to domains and nothing sweeps orphans — so the release runs
	// detached from the caller's cancellation and a failure is logged.
	if err := ReleaseReverseProxyPort(ctx, d.Ports, domainID); err != nil {
		log.Warn("domain delete: reverse-proxy port release failed — the port stays reserved",
			"domain", domainName, "domain_id", domainID, "err", err)
	}

	finish := func(ctx context.Context) bool {
		if terr := ExecuteTeardown(ctx, d.Agent, domainName); terr != nil {
			log.Warn("domain delete: host teardown failed — tombstone kept for reconciler retry",
				"domain", domainName, "err", terr)
			if d.Teardowns != nil {
				_ = d.Teardowns.MarkAttempt(ctx, domainName, terr.Error())
			}
			return false
		}
		if d.Teardowns != nil {
			_ = d.Teardowns.Delete(ctx, domainName)
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

// teardownStep names one teardown step for error reporting.
type teardownStep struct {
	name string
	call func(ctx context.Context) error
}

// ExecuteTeardown runs the host-side teardown for a domain whose panel row is
// already gone: Stalwart account purge, nginx vhost removal (domain.delete also
// reaps the mail vhost, relay credential, and logs), and the pdns zone. Every
// step is idempotent agent-side, so retries are safe. An error means the
// tombstone must stay for the reconciler to retry.
func ExecuteTeardown(ctx context.Context, ag agent.AgentInterface, name string) error {
	if ag == nil {
		return fmt.Errorf("domain teardown %s: no agent wired", name)
	}
	steps := []teardownStep{
		{"mail.domain.purge_accounts", func(ctx context.Context) error {
			return PurgeDomainMail(ctx, ag, name)
		}},
		{"domain.delete", func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := ag.Call(cctx, "domain.delete", map[string]string{"domain": name})
			return err
		}},
		{"dns.zone.delete", func(ctx context.Context) error {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := ag.Call(cctx, "dns.zone.delete", map[string]string{"zone": name})
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

// PurgeDomainMail destroys EVERY Stalwart registry account under a domain. It
// is the first teardown step, so it runs only once the row delete has
// succeeded.
//
// Purge BY DOMAIN, not per panel mailbox row: a row-driven loop skips orphans
// (a migration that pushed mail to Stalwart without a matching row, or a failed
// prior delete that removed the row but not the account). Before this, a
// re-migrated address collided with the orphan:
// {"type":"primaryKeyViolation","properties":["email"]}.
//
// remove_domain: this runs only when the domain itself is being deleted, so
// the agent also destroys the domain's DKIM signatures and the Stalwart
// domain. Left behind, the domain kept the old owner's catch-all address and
// DKIM key for whoever adds the same name next. (The mail-only purge in
// api/domain_mail_purge.go calls the agent without it: the web domain stays
// and re-enabling mail reuses the Stalwart domain.)
//
// A nil agent or empty domain is a no-op.
func PurgeDomainMail(ctx context.Context, ag agent.AgentInterface, domain string) error {
	if ag == nil || domain == "" {
		return nil
	}
	mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err := ag.Call(mctx, "mail.domain.purge_accounts", map[string]any{
		"domain":        domain,
		"remove_domain": true,
	})
	return err
}
