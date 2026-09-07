package userops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// MailboxCounter reports how many mailboxes a domain has. Satisfied by
// repository.MailboxRepository. RenameDomain uses it as a fail-closed gate: the
// durable teardown of the OLD name purges every Stalwart account on it, so a
// rename is refused unless the domain has zero mailboxes.
type MailboxCounter interface {
	CountByDomainID(ctx context.Context, domainID string) (int64, error)
}

// RenameError is a typed gate/orchestration failure so the HTTP handler can map
// a stable code to a 4xx and any future CLI to a usage error.
type RenameError struct {
	Code    string
	Message string
}

func (e *RenameError) Error() string { return e.Message }

func renameErr(code, format string, args ...any) *RenameError {
	return &RenameError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// RenameDomain renames an existing domain in place (GH #1579): it moves the
// served docroot, re-keys the domain's DNS zone and forces an SSL reissue for
// the new name, and tears down the OLD name's server artifacts (nginx vhost +
// old PowerDNS zone, via the durable JAB-236 tombstone the reconciler retries).
//
// Phase 1 is deliberately limited to a plain web domain. It refuses when:
//   - the domain still has any mailbox (see below) or mail is flagged active —
//     a rename changes every mailbox address, which is a mailbox migration, not
//     a rename; the durable teardown of the old name would also PURGE those
//     retained Stalwart accounts (mail-disable is soft), so this gate is
//     fail-closed: a nil Mailboxes repo refuses too;
//   - the domain is the panel's own primary domain (self-lockout);
//   - the domain has no website to move (web disabled / no docroot);
//   - the owner's Linux account is not provisioned yet;
//   - the docroot path does not contain the old name exactly once (a custom
//     docroot needs a manual move).
//
// Installed apps are ALLOWED, but their internal configuration (e.g. a
// WordPress siteurl stored in the app's own database) is NOT rewritten — the
// caller warns the user. The WordPress reconciler derives its probe path from
// the domain's current docroot, so the install itself heals to the new path.
//
// The name is expected to be pre-normalized + format-validated by the caller
// (the HTTP handler runs normalizeDomainName + validateDomainName, the same
// path domain create uses). This function owns the domain-logic gates.
//
// Ordering is chosen so the two AUTHORITATIVE mutations (move the files, then
// rename the row) happen first and every step is idempotent, so a mid-run
// failure is recoverable by re-running (same shape as ChangeDomainOwner):
//  1. move the docroot on the box (domain.reown, same owner uid — idempotent);
//  2. rename the DB row (name + doc_root together) — the point of no return;
//  3. tombstone the OLD name (safe only now that no live row carries it) so the
//     reconciler durably tears down the old vhost + old PowerDNS zone;
//  4. re-key the DNS zone row (name) + reset the SSL cert to pending, so the
//     reconciler re-pushes the zone and reissues the cert under the NEW name;
//  5. schedule a prompt re-render of the new name.
//
// Steps 3–5 are best-effort heals AFTER the rename is committed: a rare failure
// there is logged, not surfaced, because the row+files (the source of truth)
// are already renamed and the periodic reconcile converges the rest.
func RenameDomain(ctx context.Context, d Deps, rec RenameReconciler, domain *models.Domain, newName string) error {
	if d.Domains == nil || d.Agent == nil || d.Users == nil {
		return renameErr("unavailable", "rename is not available (domains/users/agent not wired)")
	}
	if domain == nil {
		return renameErr("not_found", "domain not found")
	}

	oldName := domain.Name
	oldDocRoot := domain.DocRoot
	newName = strings.ToLower(strings.TrimSpace(newName))

	// ---- gate ----
	if newName == "" {
		return renameErr("invalid_name", "a new domain name is required")
	}
	if newName == strings.ToLower(oldName) {
		return renameErr("noop", "the new name is the same as the current name")
	}
	if domain.EmailEnabled {
		return renameErr("mail_active",
			"disable mail on %q before renaming — a rename changes every mailbox address and cannot migrate mail", oldName)
	}
	// Fail-closed mailbox gate. The old-name teardown's first step
	// (mail.domain.purge_accounts) destroys every Stalwart account on the
	// domain, and mail-disable is soft (mailboxes are retained), so
	// EmailEnabled==false is NOT proof there is no mail to lose. Refuse when the
	// counter is unwired (can't prove zero) or reports any mailbox.
	if d.Mailboxes == nil {
		return renameErr("unavailable", "rename is not available (mailbox check not wired)")
	}
	if n, cerr := d.Mailboxes.CountByDomainID(ctx, domain.ID); cerr != nil {
		return renameErr("unavailable", "could not verify %q has no mailboxes: %v", oldName, cerr)
	} else if n > 0 {
		return renameErr("mailboxes_present",
			"%q still has %d mailbox(es) — delete or migrate them before renaming (a rename would change every mailbox address)", oldName, n)
	}
	if domain.IsPanelPrimary {
		return renameErr("panel_primary", "%q is the panel's own primary domain and cannot be renamed", oldName)
	}
	if domain.WebDisabled || oldDocRoot == "" {
		return renameErr("web_disabled", "%q has no website to rename", oldName)
	}

	owner, err := d.Users.FindByID(ctx, domain.UserID)
	if err != nil || owner == nil {
		return renameErr("owner_unresolved", "could not resolve the owner of %q", oldName)
	}
	if owner.LinuxUID == nil || *owner.LinuxUID == 0 {
		return renameErr("owner_unprovisioned", "the owner's Linux account is not fully provisioned yet")
	}

	newDocRoot, derr := renameDocRootSegment(oldDocRoot, oldName, newName)
	if derr != nil {
		return derr
	}

	// The new name must be free. A concurrent claim between here and the write
	// is still caught by the unique index (Rename returns a conflict).
	existing, ferr := d.Domains.FindByName(ctx, newName)
	if ferr != nil && !errors.Is(ferr, repository.ErrNotFound) {
		return renameErr("lookup_failed", "could not check name availability: %v", ferr)
	}
	if existing != nil {
		return renameErr("name_taken", "%q already exists", newName)
	}

	// ---- orchestrate ----
	// 1. Move the docroot tree old -> new under the SAME owner uid, BEFORE the
	//    DB row is renamed. Idempotent agent-side (reown returns AlreadyDone
	//    when the source is gone and the target exists), so a re-run after a
	//    later mid-failure finishes cleanly. Nothing is persisted yet: a failure
	//    here leaves the domain fully intact.
	if _, aerr := d.Agent.Call(ctx, "domain.reown", map[string]any{
		"old_doc_root": oldDocRoot,
		"new_doc_root": newDocRoot,
		"new_uid":      int(*owner.LinuxUID),
	}); aerr != nil {
		return renameErr("move_failed", "could not move the site files for %q: %v", oldName, aerr)
	}

	// 2. Rename the row (name + doc_root as a unit) — the authoritative flip.
	//    Files already moved; a failure here is re-runnable (reown -> AlreadyDone
	//    on the retry). No tombstone exists yet, so a failure can never leave the
	//    sweep tearing down a live domain.
	if rerr := d.Domains.Rename(ctx, domain.ID, newName, newDocRoot); rerr != nil {
		if errors.Is(rerr, repository.ErrConflict) {
			return renameErr("name_taken", "%q already exists", newName)
		}
		return renameErr("persist_failed", "could not rename %q: %v", oldName, rerr)
	}
	domain.Name = newName
	domain.DocRoot = newDocRoot

	// 3. Tombstone the OLD name so the reconciler durably tears down the old
	//    nginx vhost + old PowerDNS zone. Safe now: no live row carries oldName,
	//    so the sweep cannot tear down a live domain. Its purge_accounts step is
	//    a no-op here — the mailbox gate above guarantees zero accounts.
	if d.DomainTeardowns != nil {
		if terr := d.DomainTeardowns.Ensure(ctx, oldName); terr != nil {
			logRenameHeal(d.Log, "tombstone old name", oldName, newName, terr)
		}
	}

	// 4a. Re-key the domain's DNS zone row to the new name. DB record names are
	//     stored relative and compiled to FQDN against zone.Name, so this single
	//     Update re-qualifies every record (bootstrap AND custom) to the new
	//     name; the reconciler pushes the new zone on its next pass because the
	//     compiled FQDNs — and thus the dispatch hash — change.
	if d.DNSZones != nil {
		if zone, zerr := d.DNSZones.FindByDomainID(ctx, domain.ID); zerr == nil && zone != nil {
			if zone.Name != newName {
				zone.Name = newName
				if uerr := d.DNSZones.Update(ctx, zone); uerr != nil {
					logRenameHeal(d.Log, "rename DNS zone", oldName, newName, uerr)
				}
			}
		} else if zerr != nil && !errors.Is(zerr, repository.ErrNotFound) {
			logRenameHeal(d.Log, "load DNS zone", oldName, newName, zerr)
		}
	}

	// 4b. Reset the SSL cert row to pending so the ACME loop reissues for the
	//     NEW name. The row is keyed by domain_id and carries no name, so a
	//     status reset is all it takes; the old cert files on disk are orphaned
	//     (cosmetic). No cert row (SSL never provisioned) simply skips.
	if d.SSLCerts != nil {
		if cert, cerr := d.SSLCerts.FindByDomainID(ctx, domain.ID); cerr == nil && cert != nil {
			if rerr := d.SSLCerts.ResetForRetry(ctx, cert.ID, time.Now().UTC()); rerr != nil {
				logRenameHeal(d.Log, "reset SSL cert", oldName, newName, rerr)
			}
		} else if cerr != nil && !errors.Is(cerr, repository.ErrNotFound) {
			logRenameHeal(d.Log, "load SSL cert", oldName, newName, cerr)
		}
	}

	// 5. Force a prompt re-render + zone push + cert reissue for the new name.
	if rec != nil {
		rec.Schedule(domain.ID)
	}
	return nil
}

// logRenameHeal records a best-effort post-commit heal failure without failing
// the rename — the row + files are already renamed, and the periodic reconcile
// converges nginx/DNS/SSL regardless.
func logRenameHeal(log *slog.Logger, step, oldName, newName string, err error) {
	if log == nil {
		return
	}
	log.Warn("domain rename: post-commit heal step failed (reconciler will converge)",
		"step", step, "old", oldName, "new", newName, "err", err)
}

// renameDocRootSegment produces the new docroot by replacing the single path
// segment that equals the old domain name with the new name. It handles both
// the default layout (/home/<user>/public_html/<name>) and the importer layout
// (/home/<user>/domains/<name>/public_html). A docroot that contains the old
// name zero times (fully custom) or more than once (ambiguous) is refused — the
// operator moves it manually rather than the panel guessing.
func renameDocRootSegment(docRoot, oldName, newName string) (string, *RenameError) {
	segs := strings.Split(docRoot, "/")
	idx := -1
	for i, s := range segs {
		if s == oldName {
			if idx != -1 {
				return "", renameErr("ambiguous_docroot",
					"docroot %q contains the domain name more than once — rename it manually", docRoot)
			}
			idx = i
		}
	}
	if idx == -1 {
		return "", renameErr("custom_docroot",
			"docroot %q does not contain the domain name — rename it manually", docRoot)
	}
	segs[idx] = newName
	return strings.Join(segs, "/"), nil
}
