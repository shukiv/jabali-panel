# Blueprint — Google Workspace / M365 migration program (GH #390 / #374)

**Status:** scoped, not started. **Full scope confirmed** by operator: email +
aliases + contacts + calendars (incl. shared) + shared-mailbox permissions.
Auth: **app-password first**, OAuth2/XOAUTH2 a later phase. This is a multi-
protocol **program** delivered in sequential, independently-shippable phases —
IMAP email is the foundation everything else layers on. Both GWS and M365 expose
standard IMAP + CardDAV/CalDAV (M365 also EWS/Graph), so one importer family
serves both providers.

## Program phases (each = its own set of PRs)

| Phase | Scope | Protocol | Depends on |
|-------|-------|----------|-----------|
| **A. Email** (this blueprint's detail below) | messages, folders, flags | IMAP | — |
| **B. Aliases** | send-as / proxy addresses → jabali forwarders/aliases | Gmail settings API / M365 proxyAddresses (or manual CSV) | A (account exists) |
| **C. Contacts** | personal contacts → Stalwart CardDAV | CardDAV (Google People / M365) | A |
| **D. Calendars** | events + shared calendars → Stalwart CalDAV | CalDAV / EWS | A |
| **E. Shared mailboxes + perms** | shared mailbox + delegate ACLs → jabali `mailbox_shares` | provider directory + IMAP ACL | A, and jabali mailbox-share model |

Phases B–E each get their own blueprint when reached; the sections below detail
**Phase A (IMAP email)**, which is what gets built first. The source model +
job framework below are designed so B–E slot in as additional stages/fetchers on
the same job, not a rewrite.

## Design in one line

Add an **IMAP source** that FETCHes a remote account's messages into a staging
Maildir, then hand that Maildir to the **existing** agent verb
`migration.import_mailboxes` (JMAP `Blob/upload` + `Email/import` into
Stalwart). Reuse maximised: no new Stalwart-ingest path, no new dedup/resume
logic — those already exist and are battle-tested by the cPanel restore.

## Why this shape

- `panel-agent .../migration_import_mailboxes.go` already imports a **Maildir on
  disk** → Stalwart via JMAP, dedups explicitly (Stalwart `Email/import` does
  NOT dedup on Message-ID — [[feedback_stalwart_jmap_import_gotchas]]), and is
  idempotent on resume.
- The migrate framework is source-plugin based: `registry.go` maps a source
  `kind` → Discoverer; `runner.go` walks ordered stages with idempotent resume;
  `ssrf.go` already guards outbound connect (reuse for the IMAP dial).
- `github.com/emersion/go-message` is already a dep (MIME). Its sibling
  `github.com/emersion/go-imap/v2` is the natural IMAP client (verify API via
  Context7 in phase 1 — [[feedback_verify_capability_via_context7]]).
- The daemon can now write `/var/lib/jabali-migrations/<job>/` staging (the
  AppArmor fix just shipped, PR #419), so the fetch stage can stage there.

## What's fundamentally different from cPanel/DA/Hestia

Those are **per-panel-account** SSH/tarball pulls. IMAP is **per-mailbox**: each
migrated account carries its own remote IMAP login (host, port, TLS, username,
password). So the job model is a **list of mailbox migrations**, not one account
tarball. A single job may carry many accounts (bulk CSV) or one.

## Source model

New source kind `models.MigrationSourceIMAP = "imap"`. Per-mailbox spec:

| field | notes |
|-------|-------|
| remote_host, remote_port | e.g. imap.gmail.com:993 / outlook.office365.com:993 |
| tls | implicit TLS (993) default; STARTTLS optional |
| remote_username | full remote address |
| remote_secret_ref | password (or OAuth token — see decision) stored in the encrypted migration-secrets store, agent-written |
| target_mailbox | jabali mailbox the mail lands in (must exist, or create — decision) |
| folder_map | remote folder → Stalwart mailbox; auto via SPECIAL-USE (INBOX/Sent/Drafts/Trash/Junk/Archive), custom folders created as-is |

## Stages (plug into the existing runner)

1. **connect / validate** — dial remote IMAP through `migrate/ssrf.go` guard,
   LOGIN, LIST folders, capability probe. Fail fast on auth error with a clear,
   non-leaking message. (Gmail/M365 require an **app password** or OAuth — see
   decision; a normal password with 2FA on will fail.)
2. **fetch → staging Maildir** — per folder, FETCH `RFC822`/`BODY[]` + flags +
   INTERNALDATE, write each message as a Maildir file under
   `/var/lib/jabali-migrations/<job>/<account>/Maildir/<folder>/{cur,new}` with
   the flag suffix. Resumable: track last-seen UID per folder (UIDVALIDITY
   guarded) so a re-run continues, not restarts.
3. **import → Stalwart** — call the **existing** `migration.import_mailboxes`
   agent verb pointed at the staging Maildir + target account. Dedup + idempotent
   resume come for free.
4. **verify / cleanup** — counts (fetched vs imported), record per-folder
   warnings, then reap the staging Maildir (the #425 reaper already ages
   `/var/lib/jabali-migrations`).

## Phased PRs

1. **Model + registry + IMAP connect/validate stage** (no fetch yet):
   `MigrationSourceIMAP`, per-mailbox job payload, `go-imap/v2` dial through the
   SSRF guard, LOGIN + folder LIST + SPECIAL-USE detection, returned as a
   discovery/preview. Unit tests with a fake IMAP server (go-imap ships a test
   server) — no network. **Box-verify** connect against a throwaway account.
2. **Fetch → staging Maildir stage**: streaming FETCH, Maildir writer (reuse
   Maildir conventions the cpanel importer already reads), UID-resume state.
   Tests: fetch from the fake server, assert Maildir tree + flags.
3. **Wire import stage** to `migration.import_mailboxes` + **verify/cleanup**.
   Box end-to-end: throwaway IMAP account → jabali mailbox on .86, assert message
   count + folders in Bulwark webmail.
4. **UI wizard**: source=IMAP form (single + bulk CSV), folder-map preview,
   progress via `migration_stages`; **CLI**: `jabali migrate imap --host … --user
   … --to <mailbox> [--password-stdin]` / `--csv accounts.csv`.

## Decisions

- **Auth**: DECIDED — app-password first (operator generates one per account);
  OAuth2/XOAUTH2 is a later phase.
- **Scope**: DECIDED — full suite (email + aliases + contacts + calendars +
  shared perms), delivered as phases A–E above; Phase A (email) first.
- **Target mailbox** (still open, gates Phase A step 3): require the jabali
  mailbox to pre-exist, or create-on-migrate (needs a password for the new local
  mailbox — same reveal-once flow as mailbox-create)? Default proposal: **require
  pre-exist** for phase-A step 1–3; add create-on-migrate in the phase-A UI (step
  4) where the reveal-once modal already lives.
- **Bulk** (open, gates Phase A step 4 UI): single-account first, CSV bulk in a
  follow-up.

## Scars to respect

- [[feedback_stalwart_jmap_import_gotchas]] — Email/import doesn't dedup; the
  agent verb already handles it. Don't re-import without the UID-resume guard.
- [[feedback_verify_capability_via_context7]] — pin the go-imap/v2 + JMAP API via
  Context7 before writing calls.
- [[feedback_migration_data_seed_ordering]] — schema-only migration for the new
  source kind / per-mailbox rows; data moves in app code.
- [[feedback_security_highest_priority]] — remote IMAP creds are secrets: store
  in the encrypted migration-secrets store (agent-written, root:jabali 0750),
  never in the job row or logs; SSRF-guard the dial; audit each migration.
- Reuse the existing Maildir reader contract so the importer sees a tree
  identical to what cpanel produces.

## Not in this slice (separate features)

Calendars + shared calendars (CalDAV/EWS → Stalwart CalDAV), contacts (CardDAV),
aliases, shared-mailbox membership + permissions. Each is its own blueprint;
#390/#374 as written ask for all of them.
