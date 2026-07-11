# M374 — Microsoft 365 → Jabali Mail Migration (GH #374)

**Objective:** let an admin migrate mailboxes off Microsoft 365 (Exchange
Online) into Jabali/Stalwart — mail first, then aliases, calendars, contacts,
and shared mailboxes + permissions — via an **app-only OAuth2** connection to
the customer's M365 tenant.

**Status:** blueprint (2026-07-11). Milestone-scale, phased. Phase 1 (mail +
aliases) is the shippable MVP; Phases 2–3 layer on calendars/contacts and
shared-mailbox permissions.

**Issue scope (verbatim):** "all selectable emails, calendars (including
mapping out shared calendars), aliases, Shared Mailboxes and their
permissions."

---

## Context (read once — applies to every step)

### What already exists (reuse, do NOT reinvent)

- **Migration orchestration framework** — `panel-api/internal/migrate/`:
  `Runner` (`runner.go`) advances a `MigrationJob` through ordered stages
  (`analyze` / `fix_perms` / `validate` / `restore`, see `AllStages`) by
  dispatching a per-kind `StageCallback` map. **The `Runner` does NOT require
  the SSH `Discoverer`** — that interface (`discover.go`, `Session`,
  `ExpectedHostKey`) is used only *inside* the tarball kinds' callbacks
  (cpanel/hestiacp/directadmin/whm). A new kind supplies its own callbacks and
  connector. **This is the integration seam for M365.**
- **Job + stage models** — `models.MigrationJob` (`migration_job.go`,
  `MigrationSourceKind` consts + `MigrationState*` lifecycle) and
  `models.MigrationStage`. Add `MigrationSourceM365 = "m365"`.
- **Mail-landing primitive** — agent `migration.import_mailboxes`
  (`panel-agent/internal/commands/migration_import_mailboxes.go`): imports a
  `<domain>/<local>/{cur,new,.Sub}` **Maildir tree** into the matching Stalwart
  account via JMAP `Blob/upload` + `Email/import`, **Message-ID dedup →
  idempotent on resume**. If the M365 pull writes a Maildir, this lands it
  unchanged. Same primitive M35 cPanel restore uses.
- **Admin migration UI** — `panel-ui/src/shells/admin/migrations/`
  (`CreateMigrationWizard.tsx`, `AdminMigrationsPage.tsx`,
  `AdminMigrationDetailPage.tsx`) + API `admin_migrations.go`,
  `admin_migrations_testconn.go`. A new kind adds a wizard branch + a
  test-connection path.
- **Mail groups (M51 / ADR-0132)** — a shared mailbox maps naturally to a
  jabali **mail group** (`mail_groups` + `mail_group_members`), which already
  projects shared inbox + calendar + address book and member send-as. Phase 3
  reuses this instead of inventing shared-mailbox permissions.
- **Forwarders** — `email_forwarders` (type `alias`) is how jabali models
  aliases; M365 `proxyAddresses` map onto it. Send-only mailboxes (GH #371)
  exist if a migrated account should be submission-only.

### M365 access model (grounded — Microsoft Learn + Exchange team blog, 2023→2025)

- **Basic auth is retired** for IMAP/POP/SMTP in Exchange Online. Programmatic
  access is **OAuth2 only**.
- **App-only (client-credentials) IMAP access is supported and is the
  migration path:** the customer's tenant admin (1) registers an Entra app,
  (2) adds the **`IMAP.AccessAsApp`** *application* permission, (3) grants
  **admin consent**, (4) runs Exchange Online PowerShell
  `New-ServicePrincipal -AppId … -ServiceId …` then
  `Add-MailboxPermission -Identity <mbx> -User <sp> -AccessRights FullAccess`
  for each mailbox (and each **shared** mailbox) to migrate.
- **jabali receives** `tenant_id`, `client_id`, `client_secret`. It fetches an
  app-only token via client-credentials (`POST
  https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token`, scope
  **`https://outlook.office365.com/.default`**), then authenticates each IMAP
  connection with **SASL XOAUTH2** (`user=<mbx>\x01auth=Bearer <token>\x01\x01`).
- **Enumeration** (mailbox list, `proxyAddresses`/aliases, shared mailboxes,
  calendars, contacts) uses **Microsoft Graph** application permissions
  (`https://graph.microsoft.com/.default`; `User.Read.All`,
  `Mail.Read`/`MailboxSettings.Read`, `Calendars.Read`, `Contacts.Read` as each
  phase needs them). Graph token is a *separate* audience from the Outlook IMAP
  token — request both.
- **Throttling** is real (Graph + EXO per-app concurrency limits). The
  connector must honor `Retry-After` and cap IMAP concurrency (start ≤4 parallel
  mailboxes).

### Non-negotiables / scars to respect

- **Secrets:** `client_secret` is a tenant credential. Store encrypted at rest
  with the existing `sso.key`/secrets mechanism (see `migrate/secrets.go`);
  never log it; redact in job payloads and audit rows. Support secret rotation
  (a job may outlive a secret's expiry — surface a clear re-auth error, don't
  silently stall).
- **SSRF / egress:** unlike SSH sources there is no arbitrary host — endpoints
  are fixed Microsoft hosts (`login.microsoftonline.com`,
  `outlook.office365.com`, `graph.microsoft.com`). Pin them; do not accept a
  user-supplied host. (Sidesteps the `ssrf.go` concern that governs tarball
  pulls.)
- **DB-as-truth (ADR-0045):** the panel creates the jabali mailbox rows; the
  agent lands mail via JMAP. Migration must **create the destination mailbox**
  (or map to an existing one) before import — reuse the mailbox-create path,
  not a raw INSERT.
- **Migration = schema-only migrations; data/seeds in app** (scar
  `migration_data_seed_ordering`).
- **`gorm_column_tags`:** pin `column:` on initialism/ID fields (TenantID,
  ClientID).
- **Idempotent/resumable:** every stage must resume cleanly — `Email/import`
  dedups on Message-ID; alias/group creation must be upsert.
- **Verify wire contract** (`verify_wire_contract`): UI hooks read the real
  handler envelope, not assumed shapes.
- **install.sh is truth:** any new system dependency (e.g. a Go IMAP/MSAL
  library is vendored, not a system package — so likely none) still gets added
  if introduced.

### Architecture decision to record

Target **ADR (next free number)** — "M365 migration via app-only OAuth2 +
IMAP XOAUTH2 + Graph enumeration." Records: app-only over delegated (no
interactive per-user consent at migration time); IMAP for message bodies
(fast, complete, reuses Maildir import) over Graph `/messages` (throttled,
MIME reconstruction lossy); Graph for metadata only; shared mailbox → mail
group mapping; fixed-host egress (no SSRF surface); phased delivery.

---

## ⛑ STEP 1 GATE — live spike (BLOCKS all build steps)

**Goal:** prove the load-bearing unknowns end-to-end against a **real M365
tenant** (a Microsoft 365 Developer Program E5 trial tenant is free and
sufficient — seed it with 2–3 mailboxes, a shared mailbox, an alias, a couple
of calendar events).

**Prove, in order:**
1. **App-only token mint** — client-credentials against the trial tenant yields
   a valid token for scope `https://outlook.office365.com/.default` AND a
   separate token for `https://graph.microsoft.com/.default`.
2. **Graph enumeration** — `GET /users` lists mailboxes; `proxyAddresses`
   yields aliases; shared mailboxes are distinguishable
   (`GET /users?$filter=…` / mailbox type); a calendar + its events are
   readable.
3. **IMAP XOAUTH2 pull** — connect to `outlook.office365.com:993`, SASL
   XOAUTH2 with the app token + a mailbox that has the `FullAccess` service-
   principal grant; LIST folders; FETCH a message's RFC822 body. Confirm the
   `Add-MailboxPermission` grant is the gate (a mailbox without it → auth
   fails, proving least-privilege scoping works).
4. **Round-trip into Stalwart** — write the fetched messages as a Maildir tree
   and run agent `migration.import_mailboxes` against a throwaway jabali
   mailbox on `.86`; confirm messages appear via JMAP and a re-run dedups
   (0 new).
5. **Throttling behavior** — observe whether the trial tenant returns
   `Retry-After` under a tight loop; record the backoff the connector must
   implement.
6. **Scoping mechanism** — compare **per-mailbox `Add-MailboxPermission
   FullAccess`** (works, but O(mailboxes) PowerShell — heavy for big tenants)
   against **RBAC for Applications** (a single management-role assignment
   scoping the app to all / a mailbox subset). Prefer RBAC-for-Applications in
   the runbook if the trial tenant supports it; fall back to the per-mailbox
   loop. Record which the runbook will document as primary.

**Deliverable:** a short spike note appended here recording: the exact token
endpoints/scopes that worked, the XOAUTH2 SASL string format Stalwart-import
round-trip proved, the Graph fields used for alias/shared-mailbox/calendar
discovery, and the concurrency/backoff limits observed. **Pick the Go
libraries** (MSAL-Go `github.com/AzureAD/microsoft-authentication-library-for-go`
for tokens; an IMAP client e.g. `github.com/emersion/go-imap/v2`;
`github.com/emersion/go-message` for MIME→Maildir). If any of (1)–(4) fails,
STOP and redesign before building.

**Exit criteria:** all five proven on a live tenant; libraries chosen; spike
note written; ADR drafted.

---

## Phase 1 — Mail + Aliases (MVP, shippable)

### Step 2 — Data model + source kind (schema-only migration)

- New migration `0002xx_m365_migration.up.sql` (schema only): a
  `m365_migration_credentials` table (job_id FK, tenant_id, client_id,
  `client_secret_enc` VARBINARY, created_at) — OR reuse the existing migration
  secrets store if it already encrypts per-job blobs (check `migrate/secrets.go`
  first; prefer it). Add columns to `migration_jobs` only if the framework
  can't carry `kind`-specific config in its existing payload/JSON column.
- `models`: add `MigrationSourceM365 = "m365"`; a `M365Config` struct
  (TenantID, ClientID, mailbox-selection list) with pinned `column:` tags.
- **Exit:** migration applies clean on MariaDB 11.x (scar
  `mariadb_reserved_words` — avoid reserved words); `go build` green.

### Step 3 — M365 connector package (`internal/migrate/m365/`)

- `token.go` — MSAL-Go client-credentials; caches per-audience tokens; refreshes
  before expiry. Two audiences (Outlook + Graph).
- `graph.go` — `ListMailboxes`, `ListAliases(mbx)` (proxyAddresses),
  `ListSharedMailboxes`. Honors `Retry-After`.
- `imap.go` — XOAUTH2 dial to `outlook.office365.com:993`; `FetchMailbox(mbx,
  dstMaildir)` walks all folders → Maildir (`cur/`, folder→`.Subfolder`
  naming that `migration.import_mailboxes` expects). Concurrency-capped.
- Fixed-host allowlist; no user-supplied endpoints.
- **Exit:** unit tests with a mocked token endpoint + a fake IMAP server
  (go-imap has a memory backend) covering: token refresh, folder→Maildir
  mapping, `Retry-After` backoff, XOAUTH2 SASL string. `go test` green.

### Step 4 — Stage callbacks + Runner wiring

- Register `m365` `StageCallback`s on the `Runner`:
  - `analyze` — Graph enumerate → build the account manifest (mailboxes +
    sizes + aliases); populate the size cache; produce the per-mailbox plan.
  - `restore` — for each selected mailbox: ensure the destination jabali
    mailbox exists (reuse the mailbox-create path, DB-as-truth), IMAP-pull to a
    staging Maildir, dispatch agent `migration.import_mailboxes`, then create
    `email_forwarders(type=alias)` rows for each proxyAddress. Idempotent/
    resumable (import dedups; alias upsert).
  - `validate` — JMAP message-count sanity per mailbox (source FETCH count vs
    imported), surface a per-mailbox report; degrade (not fail) on partial per
    ADR-0095 `degraded` semantics.
- **Exit:** a job runs end-to-end on `.86` against the trial tenant; messages +
  aliases land; re-run is a no-op; a revoked mailbox grant surfaces a clean
  per-mailbox error without failing the whole job.

### Step 5 — API + test-connection

- `admin_migrations.go`: accept `kind=m365` create with `{tenant_id, client_id,
  client_secret, mailboxes[]}`; encrypt the secret; never echo it back.
- `admin_migrations_testconn.go`: an M365 branch that mints both tokens +
  does a Graph `GET /users?$top=1` + one IMAP XOAUTH2 login, returning
  `{ok, mailbox_count, sample_mailbox}` or a precise error (bad secret /
  missing consent / mailbox not granted).
- **Exit:** wire contract verified against the real handler; test-connection
  returns actionable errors for each misconfiguration class.

### Step 6 — Admin UI wizard branch

- `CreateMigrationWizard.tsx`: an "Microsoft 365" source with fields tenant_id
  / client_id / client_secret, a **Test connection** button, then a mailbox
  picker populated from the test-connection enumeration (select all / subset),
  and destination-domain mapping. Progress + per-mailbox result surface in
  `AdminMigrationDetailPage.tsx` (reuse existing stage/progress components).
- **Exit:** `npx tsc -b` + vitest green; Playwright happy-path (mock the API)
  covering wizard → progress → done.

### Step 7 — Runbook + ADR + release

- **Runbook** (`plans/m374-m365-migration-runbook.md`): the exact tenant-admin
  prerequisites — Entra app registration, `IMAP.AccessAsApp` permission, admin
  consent, `New-ServicePrincipal`, then **either** an RBAC-for-Applications
  role assignment scoping the app to all/subset mailboxes (preferred if the
  spike proved it) **or** the per-mailbox `Add-MailboxPermission FullAccess`
  PowerShell loop (fallback), IMAP-enabled check, and the 20–30 min propagation
  caveat. This operator lift is inherent to M365 and must be copy-pasteable.
- Accept the ADR. Merge Phase 1.
- **Exit:** live migration of the trial tenant's mailboxes + aliases verified
  on `.86`; runbook validated by following it from scratch.

---

## Phase 2 — Calendars + Contacts (follow-up)

### Step 8 — Graph calendar/contact pull → JMAP import

- `graph.go`: `ListCalendars(mbx)`, `ListEvents(cal)`, `ListContacts(mbx)`.
- Map events → iCalendar → JMAP `CalendarEvent/set` (or a new agent
  `migration.import_calendar`); contacts → JMAP `ContactCard`/AddressBook.
- **Shared calendars** (issue's explicit ask): resolve the calendar owner +
  the members it's shared with; project onto the destination mail group's
  shared calendar (Phase 3 dependency for the group, or a direct share map).
- **Exit:** a mailbox's primary calendar + one shared calendar migrate with
  events intact; contacts land in the address book. Live-verified.

---

## Phase 3 — Shared mailboxes + permissions (follow-up)

### Step 9 — Shared mailbox → jabali mail group + member permissions

- For each M365 shared mailbox: create a jabali **mail group** (M51), migrate
  its mail (IMAP pull, same primitive) into the group's shared inbox, and map
  the shared-mailbox delegates (`Add-MailboxPermission` / `FullAccess` +
  `SendAs` from Graph) onto **group membership** + send-as (GH #347
  delegation). Calendar/contacts of the shared mailbox follow via Phase 2.
- **Exit:** a shared mailbox with two delegates migrates to a mail group whose
  two members see the shared inbox + can send-as; permissions preserved.
  Live-verified. ADR amended with the shared-mailbox mapping decisions.

---

## Dependency / parallelism summary

- **Step 1 (spike) blocks everything.** Do it first, on a live trial tenant.
- Phase 1 steps are largely serial: 2 → 3 → 4 → (5 ∥ 6) → 7. Steps 5 and 6 can
  overlap once 4's callbacks + envelope are fixed (verify the wire contract at
  the 4/5 boundary).
- Phases 2 and 3 are independent follow-ups after Phase 1 ships; Phase 3's
  calendar piece depends on Phase 2.

## Rollback

- Phases are additive: the `m365` kind + its table/columns are inert until an
  admin creates an M365 job. Rolling back = don't offer the kind (feature-flag
  the wizard branch) / `down.sql` drops the M365 table. No impact on existing
  migration kinds (separate `StageCallback`s, separate connector).

## Out of scope (name it, per no-silent-caps)

- Public folders, Teams/SharePoint data, Exchange transport rules, retention
  policies, in-place archives / online archive mailboxes. Mail (Phase 1),
  calendars/contacts (Phase 2), shared mailboxes + delegate permissions
  (Phase 3) are the committed surface. Archives and public folders are
  explicitly deferred.
