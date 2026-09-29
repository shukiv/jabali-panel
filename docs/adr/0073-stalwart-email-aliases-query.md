# ADR-0073 — Stalwart alias resolution + apply-plan schema evolution

Status: Accepted
Date: 2026-04-27
Supersedes: nothing
Amends: ADR-0045 (Stalwart bootstrap), ADR-0051 (M6.5 DB-as-truth)

## Context

VM smoke on `mx.jabali-panel.com` ( 2026-04-27) confirmed
the M6.5 forwarder/alias feature ships through to the database
(`email_forwarders` row created on `jabali mailbox forwarder add`) but
is never observed by Stalwart. Sending to an alias returns
`550 5.1.2 Mailbox does not exist`.

Two findings drove this ADR:

1. **Two SqlDirectory queries needed patching, not one.**
   `queryEmailAliases` only populates an Account's `aliases` list
   (visible in `stalwart-cli get Account`); SMTP RCPT acceptance is
   independently gated by `queryRecipient`. Even with aliases on the
   account, `queryRecipient` rejected the alias address because it
   only matched `email_cached`. Both queries need alias-aware SQL.

2. **`@type: create` is not idempotent.** Re-running an apply-plan
   that creates `x:Directory` produces a *duplicate* Directory each
   run because Stalwart auto-generates an id and there's no
   name-based dedup. The previous `skip_apply` guard in install.sh
   silently relied on this — but it also meant any change to the
   directory's query fields could never reach existing hosts.

## Decision

1. **`queryRecipient` resolves aliases inline.** A derived-table
   pattern keeps the prepared statement to a single `?` while
   accepting either the canonical email or any alias address:

   ```sql
   SELECT m.email_cached, m.password_hash
   FROM (SELECT ? AS lookup) input
   JOIN mailboxes m
     ON m.is_disabled = 0
    AND (m.email_cached = input.lookup
         OR m.email_cached = (
           SELECT f.target FROM email_forwarders f
           JOIN domains d ON d.id = f.domain_id
           WHERE f.enabled = 1 AND f.type = 'alias'
             AND CONCAT(f.local_part, '@', d.name) = input.lookup
           LIMIT 1
         ))
   ```

2. **`queryEmailAliases` returns aliases owned by the canonical
   principal.** Input is the canonical email, output is each alias
   address. This is the direction Stalwart's `synchronize_account`
   expects:

   ```sql
   SELECT CONCAT(f.local_part, '@', d.name) AS alias
   FROM email_forwarders f
   JOIN domains   d ON d.id = f.domain_id
   JOIN mailboxes m ON m.id = f.mailbox_id
   WHERE f.enabled = 1 AND f.type = 'alias'
     AND m.email_cached = ?
   ```

3. **Schema evolution converges via post-apply `stalwart-cli update`,
   not via in-plan `update` steps.** install.sh:
   - keeps the `skip_apply` guard (re-running `apply` would create a
     duplicate Directory)
   - adds a separate convergence step that runs unconditionally:
     queries the live SQL Directory id, then patches both query fields
     via `stalwart-cli update Directory <id> --json '{...}'`. The id is
     resolved each run; no template-baked id is needed.

4. **`SELECT` grant on `email_forwarders`** added to the `jabali-stalwart-ro`
   user alongside the existing `mailboxes` + `domains` grants.

## Consequences

- Forwarders + aliases land in Stalwart on the next `jabali update` of
  every existing host — no manual stalwart-cli intervention.
- Future schema evolution of any apply-plan field (Stalwart-side
  Directory, Authentication, etc.) follows the same recipe: convergence
  in install.sh post-apply, not in the plan itself, because the plan
  has no name-based upsert.
- Disclaimer remains broken per ADR-0052 (deferred outbound transform
  mechanism, separate fix).

## Verification

VM smoke transcript on mx post-fix:

```
jabali mailbox forwarder add bob@123123.com --type alias --local sales
python3 sendmail.py alice@... sales@123123.com "alias retry 4" "..." Secret-Alice-1
→ OK
python3 imap.py bob@123123.com Secret-Bob-1
→ INBOX msgs: 1
→ Subject: alias retry 4
```

## Amendment (2026-09-29): one owner per address

### Context

Stalwart 0.16 copies the aliases `queryEmailAliases` returns into its own
registry when an account signs in or is resolved for delivery, and it
never takes one away. It also looks an address up in the registry before
the SQL directory. Verified on the test box with the 2026-09-29 release
binary:

- An alias moved from one mailbox to another kept delivering to the old
  mailbox.
- A mailbox created at the address of a deleted alias signed in to the
  alias's old owner (`auth.success` with the old owner's `accountId`).
- A mailbox created at the address of a live alias did the same.

A tenant could read another mailbox's mail by creating a mailbox at one of
its old alias addresses. Every tenant involved was in the same domain; a
cross-tenant path was not found, but it was not searched exhaustively.

### Decision

1. **The database refuses a shared address.** Migration 000306 adds
   triggers: a mailbox cannot take the address of an alias (enabled or
   not), a mail group or a shared resource in its domain, and none of
   those can take a mailbox's address. An UPDATE is refused only when it
   moves a row onto such an address, so a pair made before the migration
   keeps working. The repositories report the refusal as
   `repository.ErrAddressInUse`, and the API as 409 `address_in_use` (or
   `address_taken` for groups and shared resources).
2. **The directory lets the mailbox win.** The alias branches of
   `queryRecipient` and `queryEmailAliases` ignore an alias at an address
   a mailbox holds, so a pair made before the migration resolves to the
   mailbox and the alias owner is no longer given the address.
3. **Every mailbox create door clears the address first.** Before the row
   is written, the panel takes the address off every Stalwart account that
   holds it as an alias (`mailaddrowner.Releaser`, through the admin
   management API). The API, CLI, relay (`noreply@`), cPanel/Hestia import
   and backup restore doors refuse the mailbox when the mail server cannot
   be reached (fail closed; API 503 `mail_server_unavailable`). The panel's
   own `jabali-notify@` mailbox is the exception: it is created at panel
   start, before Stalwart exists on a fresh install, on the admin's own
   domain, so its release is best effort.
4. **Alias doors move the alias.** After an alias is created, the panel
   takes it off every account but its new mailbox's; after one is deleted,
   off every account. Both are best effort: the row is already saved.
5. **A sweep catches the rest.** Every 10 minutes the reconciler reads
   every account's aliases and takes one off an account when the database
   gives the address to a different principal (`mailaddrowner.Sweep`). It
   logs each removal at WARN.

### Known limits

- The sweep leaves an alias the database gives to no one: it cannot tell a
  deleted alias from an address the panel does not manage. A deleted alias
  whose release failed keeps delivering to its old mailbox until a mailbox,
  alias or group takes the address.
- Only the admin backup restore rebuilds panel rows (`backupmetadata.Apply`);
  the tenant `/me` restore does not, so it has no mailbox door to guard.

## References

- ADR-0045 — Stalwart v0.16 RocksDB + apply-plan bootstrap
- ADR-0051 — M6.5 DB-as-truth for mail features
- ADR-0052 — Disclaimer HTML deferred (still open)
