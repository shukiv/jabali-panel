package userops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AppInstallLister lists the application installs on a domain so a rename can
// rewrite a WordPress install's stored site URL to the new name (GH #1579).
// Satisfied by repository.ApplicationInstallRepository. Optional on Deps: nil
// skips the app-URL rewrite entirely (the rename still succeeds).
type AppInstallLister interface {
	ListByDomainIDs(ctx context.Context, domainIDs []string) ([]models.ApplicationInstall, error)
}

// MailCertReissuer re-queues a domain's per-domain mail TLS certificate for
// reissuance after a rename (GH #1579), so the reconciler reissues it for
// mail.<new> instead of leaving the mail.<old> cert until it nears expiry.
// Satisfied by repository.MailCertificateRepository. Optional on Deps: nil skips
// the reset (the reconciler's renewal window eventually reissues regardless).
type MailCertReissuer interface {
	ResetForReissue(ctx context.Context, domainID string) (int64, error)
}

// appTypeWordPress is the ApplicationInstall.AppType whose stored site URL a
// rename can rewrite (models.ApplicationInstall defaults app_type to this). Only
// WordPress keeps a rewritable absolute site URL in its OWN database; other app
// types are left untouched.
const appTypeWordPress = "wordpress"

// refreshableOSUserRE mirrors the migration.refresh_reconcile agent verb's
// os_user constraint (panel-agent internal/commands/migration_refresh.go). It is
// stricter than provisioning's usernameRe (it rejects '_'), so a rename whose
// owner name falls outside it skips the WordPress URL rewrite with a warning
// rather than sending a call the agent would reject.
var refreshableOSUserRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

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
// Mail is CARRIED, not refused. Stalwart keys every account on
// (localpart, domainId) and stores its messages under that account's stable
// internal id, so renaming the registry Domain entity in place — via the
// mail.domain.rename agent verb, which keeps the id — carries every account,
// its stored messages, and its DKIM signature to the new address; the DB
// trigger resyncs mailboxes.email_cached so the SQL directory authenticates and
// delivers at the new address. The verb runs BEFORE the DB rename so the entity
// is named `new` before the trigger flips email_cached. It still refuses when:
//   - the new name already carries mail in Stalwart (verb status "conflict") —
//     carrying mail into an occupied name would collide; caught by a dry-run
//     BEFORE any files move, and re-checked on the real run;
//   - the domain is the panel's own primary domain (self-lockout);
//   - the domain has no website to move (web disabled / no docroot);
//   - the domain uses a custom or shared TLS certificate (it covers the old
//     name and cannot be re-issued automatically for the new one);
//   - the owner's Linux account is not provisioned yet;
//   - the docroot path does not contain the old name exactly once (a custom
//     docroot needs a manual move).
//
// The per-domain mail TLS cert is re-queued for reissuance: its row is
// domain_id-keyed (so it survives the rename), and RenameDomain flips it back to
// pending so the reconciler reissues it for mail.<new> on its next tick instead
// of serving mail.<old> until the 30-day renewal window (best-effort; a domain
// with no mail cert simply skips). Not carried (documented limitation, non-fatal):
// a webmail send-as identity created before the rename keeps the old address
// until re-added. The catch-all address (a literal string on the Domain entity)
// IS rewritten by the verb, best-effort.
//
// Installed apps are ALLOWED. A WordPress install's stored site URL (kept in
// the app's OWN database, which the docroot move + DNS/SSL re-key do not touch)
// IS rewritten to the new name, best-effort, via migration.refresh_reconcile;
// any install that could not be rewritten is reported in the returned warnings.
// Other app types keep their own internal configuration unchanged. The
// WordPress reconciler derives its probe path from the domain's current
// docroot, so the install's on-disk path heals regardless.
//
// It returns the best-effort app-URL-rewrite warnings alongside the error: an
// empty slice on a clean rename, one entry per install that could not be
// rewritten (the rename itself still succeeded).
//
// The name is expected to be pre-normalized + format-validated by the caller
// (the HTTP handler runs normalizeDomainName + validateDomainName, the same
// path domain create uses). This function owns the domain-logic gates.
//
// Ordering is chosen so the two AUTHORITATIVE mutations (move the files, then
// rename the row) happen first and every step is idempotent, so a mid-run
// failure is recoverable by re-running (same shape as ChangeDomainOwner):
//  0. dry-run the mail-domain rename BEFORE anything moves, so a conflict (the
//     new name already carries mail) fails while nothing is committed;
//  1. move the docroot on the box (domain.reown, same owner uid — idempotent);
//  2. carry mail: rename the Stalwart registry Domain in place (mail.domain.rename)
//     BEFORE the DB rename, so the entity is named `new` before the trigger flips
//     email_cached — fail-closed (nothing persisted yet; reown is re-runnable);
//  3. rename the DB row (name + doc_root together) — the point of no return;
//  4. tombstone the OLD name (safe only now that no live row carries it) so the
//     reconciler durably tears down the old vhost + old PowerDNS zone; its
//     purge_accounts step is a no-op — the Stalwart entity is already `new`;
//  5. re-key the DNS zone row (name) + reset the web SSL cert AND the per-domain
//     mail cert to pending, so the reconciler re-pushes the zone and reissues
//     both certs under the NEW name;
//  6. schedule a prompt re-render of the new name.
//
// Steps 3–5 are best-effort heals AFTER the rename is committed: a rare failure
// there is logged, not surfaced, because the row+files (the source of truth)
// are already renamed and the periodic reconcile converges the rest.
func RenameDomain(ctx context.Context, d Deps, rec RenameReconciler, domain *models.Domain, newName string) ([]string, error) {
	if d.Domains == nil || d.Agent == nil || d.Users == nil {
		return nil, renameErr("unavailable", "rename is not available (domains/users/agent not wired)")
	}
	if domain == nil {
		return nil, renameErr("not_found", "domain not found")
	}

	oldName := domain.Name
	oldDocRoot := domain.DocRoot
	newName = strings.ToLower(strings.TrimSpace(newName))

	// ---- gate ----
	if newName == "" {
		return nil, renameErr("invalid_name", "a new domain name is required")
	}
	if newName == strings.ToLower(oldName) {
		return nil, renameErr("noop", "the new name is the same as the current name")
	}
	if domain.IsPanelPrimary {
		return nil, renameErr("panel_primary", "%q is the panel's own primary domain and cannot be renamed", oldName)
	}
	if domain.WebDisabled || oldDocRoot == "" {
		return nil, renameErr("web_disabled", "%q has no website to rename", oldName)
	}
	// A custom (operator-uploaded) or shared certificate cannot be re-issued for
	// the new name automatically — its files/lineage cover the OLD name, and the
	// rename's cert reset would only trigger an ACME/self-signed attempt that
	// replaces it. Refuse (fail-closed) so a rename never silently drops a
	// domain's real certificate; the owner switches to Let's Encrypt or
	// self-signed first, or re-uploads for the new name after.
	if domain.SSLMode == models.SSLModeCustom || domain.SSLMode == models.SSLModeShared {
		return nil, renameErr("ssl_custom_cert",
			"%q uses a %s TLS certificate that cannot be re-issued automatically for a new name — switch it to Let's Encrypt or self-signed before renaming (you can re-apply the certificate for the new name after)",
			oldName, domain.SSLMode)
	}

	owner, err := d.Users.FindByID(ctx, domain.UserID)
	if err != nil || owner == nil {
		return nil, renameErr("owner_unresolved", "could not resolve the owner of %q", oldName)
	}
	if owner.LinuxUID == nil || *owner.LinuxUID == 0 {
		return nil, renameErr("owner_unprovisioned", "the owner's Linux account is not fully provisioned yet")
	}

	newDocRoot, pruneOldDir, derr := renameDocRootSegment(oldDocRoot, oldName, newName)
	if derr != nil {
		return nil, derr
	}

	// The new name must be free. A concurrent claim between here and the write
	// is still caught by the unique index (Rename returns a conflict).
	existing, ferr := d.Domains.FindByName(ctx, newName)
	if ferr != nil && !errors.Is(ferr, repository.ErrNotFound) {
		return nil, renameErr("lookup_failed", "could not check name availability: %v", ferr)
	}
	if existing != nil {
		return nil, renameErr("name_taken", "%q already exists", newName)
	}

	// Dry-run the mail-domain rename BEFORE moving any files, so a genuine
	// conflict (the new name already carries mail in Stalwart) fails the rename
	// while nothing is committed. Fail-closed: an agent error here refuses.
	if dryRaw, aerr := d.Agent.Call(ctx, "mail.domain.rename", map[string]any{
		"old": oldName, "new": newName, "dry_run": true,
	}); aerr != nil {
		return nil, renameErr("unavailable", "could not check mail before renaming %q: %v", oldName, aerr)
	} else if st, _ := mailRenameStatus(dryRaw); st == mailRenameConflict {
		return nil, renameErr("mail_domain_conflict",
			"%q already has mail configured in Stalwart — delete or migrate it before renaming %q into it", newName, oldName)
	}

	// ---- orchestrate ----
	// 1. Move the docroot tree old -> new under the SAME owner uid, BEFORE the
	//    DB row is renamed. Idempotent agent-side (reown returns AlreadyDone
	//    when the source is gone and the target exists), so a re-run after a
	//    later mid-failure finishes cleanly. Nothing is persisted yet: a failure
	//    here leaves the domain fully intact.
	reownParams := map[string]any{
		"old_doc_root": oldDocRoot,
		"new_doc_root": newDocRoot,
		"new_uid":      int(*owner.LinuxUID),
	}
	// Nested docroot layout (/home/<u>/domains/<name>/public_html): the leaf
	// moves out of the old-name wrapper dir, leaving it empty — ask the agent to
	// prune it if empty. Empty for the default layout (leaf == name), where the
	// move renames the leaf itself and nothing is left behind.
	if pruneOldDir != "" {
		reownParams["prune_empty_dir"] = pruneOldDir
	}
	if _, aerr := d.Agent.Call(ctx, "domain.reown", reownParams); aerr != nil {
		return nil, renameErr("move_failed", "could not move the site files for %q: %v", oldName, aerr)
	}

	// 1b. Carry mail: rename the Stalwart registry Domain in place BEFORE the DB
	//     rename, so the entity is named `new` before the trigger flips
	//     email_cached to `@new`. Fail-closed: nothing is persisted yet (the DB
	//     row still holds the old name) and reown is idempotent, so a failure here
	//     is fully re-runnable. Statuses renamed | already | not_in_registry all
	//     proceed; a conflict (racing another rename since the dry-run) refuses.
	var mailWarnings []string
	if mailRaw, aerr := d.Agent.Call(ctx, "mail.domain.rename", map[string]any{
		"old": oldName, "new": newName,
	}); aerr != nil {
		return nil, renameErr("mail_rename_failed", "could not carry mail to the new name for %q: %v", oldName, aerr)
	} else if st, w := mailRenameStatus(mailRaw); st == mailRenameConflict {
		return nil, renameErr("mail_domain_conflict",
			"%q already has mail configured in Stalwart — delete or migrate it before renaming %q into it", newName, oldName)
	} else {
		mailWarnings = w
	}

	// 2. Rename the row (name + doc_root as a unit) — the authoritative flip.
	//    Files already moved; a failure here is re-runnable (reown -> AlreadyDone
	//    on the retry). No tombstone exists yet, so a failure can never leave the
	//    sweep tearing down a live domain.
	if rerr := d.Domains.Rename(ctx, domain.ID, newName, newDocRoot); rerr != nil {
		if errors.Is(rerr, repository.ErrConflict) {
			return nil, renameErr("name_taken", "%q already exists", newName)
		}
		return nil, renameErr("persist_failed", "could not rename %q: %v", oldName, rerr)
	}
	domain.Name = newName
	domain.DocRoot = newDocRoot

	// 3. Tombstone the OLD name so the reconciler durably tears down the old
	//    nginx vhost + old PowerDNS zone. Safe now: no live row carries oldName,
	//    so the sweep cannot tear down a live domain. Its purge_accounts step is
	//    a no-op here — the mail carry (step 1b) already renamed the Stalwart
	//    entity to `new`, so the old name is gone from the registry and there are
	//    no accounts under it to purge.
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

	// 4d. Re-queue the per-domain mail TLS cert for reissuance under the NEW name.
	//     The row is keyed by domain_id (survives the rename) but its lineage
	//     still covers mail.<old>; flipping a settled row back to pending makes
	//     the reconciler reissue promptly for mail.<new> — it reads the current
	//     domain name at dispatch — instead of serving the old cert until the
	//     30-day renewal window. Best-effort: a domain with no mail cert (mail
	//     off, or TLS never provisioned) resets nothing; a disabled/in-flight row
	//     is left untouched by ResetForReissue. The old mail.<old> lineage on disk
	//     is reaped by the old-name teardown (domain.delete).
	if d.MailCerts != nil {
		if _, mrerr := d.MailCerts.ResetForReissue(ctx, domain.ID); mrerr != nil {
			logRenameHeal(d.Log, "reset mail cert", oldName, newName, mrerr)
		}
	}

	// 4c. Rewrite a WordPress install's stored site URL from the old name to the
	//     new one. The docroot move + DNS/SSL re-key do NOT touch a WordPress
	//     install's own database, where an absolute site URL is stored; left
	//     stale it redirects visitors back to the old name. Best-effort per
	//     install, surfaced as warnings (never fails the committed rename).
	warnings := mailWarnings
	if d.AppInstalls != nil {
		warnings = append(warnings, rewriteAppSiteURLs(ctx, d, domain, owner, oldName, newName)...)
	}

	// 5. Force a prompt re-render + zone push + cert reissue for the new name.
	if rec != nil {
		rec.Schedule(domain.ID)
	}
	return warnings, nil
}

// mail.domain.rename status values the panel branches on. Only "conflict" is a
// refusal; renamed | already | not_in_registry all proceed.
const mailRenameConflict = "conflict"

// mailRenameStatus decodes a mail.domain.rename agent response into its status
// and any best-effort warnings (e.g. a catch-all that could not be rewritten).
// An undecodable body yields an empty status (treated as "proceed"): the verb is
// a heal around an already-idempotent rename, so a malformed ack must not both
// fail to signal conflict AND block the rename.
func mailRenameStatus(raw json.RawMessage) (status string, warnings []string) {
	var resp struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", nil
	}
	return resp.Status, resp.Warnings
}

// rewriteAppSiteURLs best-effort rewrites the stored site URL of every
// WordPress install on the (already-renamed) domain from oldName to newName,
// via the migration.refresh_reconcile agent verb (wp search-replace across all
// tables + cache flush + FPM reload). It returns a human-readable warning for
// each install it could not rewrite; the rename itself already succeeded, so a
// failure here is a heal to retry, not a reason to roll back.
//
// Only WordPress installs are handled — other app types keep their own
// configuration and the panel does not know how to rewrite it. A domain with no
// WordPress install produces no warning.
func rewriteAppSiteURLs(ctx context.Context, d Deps, domain *models.Domain, owner *models.User, oldName, newName string) []string {
	installs, lerr := d.AppInstalls.ListByDomainIDs(ctx, []string{domain.ID})
	if lerr != nil {
		logRenameHeal(d.Log, "list app installs", oldName, newName, lerr)
		return []string{fmt.Sprintf(
			"could not check for WordPress installs to update after the rename — update any WordPress site URL manually: %v", lerr)}
	}
	// Only WordPress keeps a rewritable absolute site URL in its own database.
	var wp []models.ApplicationInstall
	for _, inst := range installs {
		if inst.AppType == appTypeWordPress {
			wp = append(wp, inst)
		}
	}
	if len(wp) == 0 {
		return nil
	}

	osUser := ""
	if owner.Username != nil {
		osUser = *owner.Username
	}
	// The refresh verb runs wp-cli as this OS user and validates the name against
	// a stricter shape than provisioning (no '_'). If the owner's name is outside
	// it, skip the rewrite with a clear warning rather than send a rejected call.
	if !refreshableOSUserRE.MatchString(osUser) {
		return []string{fmt.Sprintf(
			"the WordPress site URL was not updated automatically — update it manually (change %q to %q in the site's settings)",
			oldName, newName)}
	}

	var warnings []string
	for _, inst := range wp {
		installPath := filepath.Join(domain.DocRoot, inst.Subdirectory)
		// A www-canonical install stores its site URL with the "www." host, so
		// rewrite that host; otherwise the bare name. (The scheme-anchored
		// old_url means neither pass matches an unrelated host.)
		oldHost, newHost := oldName, newName
		if inst.UseWWW {
			oldHost, newHost = "www."+oldName, "www."+newName
		}
		// The stored site URL may use either scheme, and the served scheme may
		// have changed; rewrite both. Each pass is a no-op when scheme://old is
		// absent (wp search-replace simply matches nothing). Dedupe reasons: a
		// broken install fails identically on both passes.
		seen := map[string]bool{}
		var reasons []string
		addReason := func(r string) {
			if r != "" && !seen[r] {
				seen[r] = true
				reasons = append(reasons, r)
			}
		}
		for _, scheme := range []string{"https://", "http://"} {
			raw, aerr := d.Agent.Call(ctx, "migration.refresh_reconcile", map[string]any{
				"os_user":      osUser,
				"install_path": installPath,
				"domain":       newName,
				"old_url":      scheme + oldHost,
				"new_url":      scheme + newHost,
			})
			if aerr != nil {
				addReason(aerr.Error())
				break // a transport/validation error will recur on the next pass
			}
			// The verb reports a wp search-replace FAILURE inside its warnings
			// (nil error), not as a call error — and always adds a benign
			// page-cache note — so inspect the response and surface only genuine
			// search-replace failures. Ignoring the response would let a failed
			// rewrite masquerade as a clean rename.
			for _, f := range refreshReconcileFailures(raw) {
				addReason(f)
			}
		}
		if len(reasons) > 0 {
			logRenameHeal(d.Log, "rewrite WordPress site URL", oldName, newName, errors.New(strings.Join(reasons, "; ")))
			warnings = append(warnings, fmt.Sprintf(
				"could not update the WordPress site URL for the install at %q — update it manually: %s",
				installPath, strings.Join(reasons, "; ")))
		}
	}
	return warnings
}

// refreshReconcileFailures inspects a migration.refresh_reconcile response. The
// verb reports a wp search-replace failure in its `warnings` (prefixed
// "search-replace:") with a nil call error, and always adds a benign page-cache
// note — so a non-nil call error is not the only failure signal, and not every
// warning is a failure. It returns only the genuine search-replace failure
// messages (empty when the rewrite succeeded).
func refreshReconcileFailures(raw json.RawMessage) []string {
	var resp struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return []string{"unexpected response from the update tool"}
	}
	var fails []string
	for _, w := range resp.Warnings {
		if strings.HasPrefix(w, "search-replace:") {
			fails = append(fails, w)
		}
	}
	if !resp.OK && len(fails) == 0 {
		fails = append(fails, "the update tool reported failure")
	}
	return fails
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
//
// It also returns pruneOldDir: the old-name wrapper directory that the docroot
// move empties and the agent should prune. It is the path up to and including
// the old-name segment ONLY when that segment is not the docroot leaf (the
// nested layout, /home/<u>/domains/<oldname>). For the default layout the
// old-name segment IS the leaf — the move renames it in place, leaving nothing
// behind — so pruneOldDir is "".
func renameDocRootSegment(docRoot, oldName, newName string) (newDocRoot, pruneOldDir string, err *RenameError) {
	segs := strings.Split(docRoot, "/")
	idx := -1
	for i, s := range segs {
		if s == oldName {
			if idx != -1 {
				return "", "", renameErr("ambiguous_docroot",
					"docroot %q contains the domain name more than once — rename it manually", docRoot)
			}
			idx = i
		}
	}
	if idx == -1 {
		return "", "", renameErr("custom_docroot",
			"docroot %q does not contain the domain name — rename it manually", docRoot)
	}
	if idx < len(segs)-1 {
		// Old name is a wrapper dir, not the docroot leaf — it is emptied by the
		// move and can be pruned.
		pruneOldDir = strings.Join(segs[:idx+1], "/")
	}
	segs[idx] = newName
	return strings.Join(segs, "/"), pruneOldDir, nil
}
