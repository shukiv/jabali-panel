# JAB-374 — Batch the Account Backup Snapshot builder (kill metadata N+1)

**Priority:** High · **Scope:** `panel-api/internal/backupmetadata` + a handful of repo batch methods + the two duplicate mailbox/inventory loops. No schema/migration change. No behavior change to the emitted metadata (golden-equivalent).

## Problem (characterized)
`backupmetadata.Build(ctx, user, Deps)` is the single producer for the admin, tenant (`/me`), and scheduler backup adapters (all wired identically after JAB-359). Its walk does per-item reads:

| Section | Current call | Fan-out |
|---|---|---|
| DB grants | `DatabaseGrants.ListByDatabaseUserID(du.ID)` per db-user (builder:179) | N = #db-users |
| SSL cert | `SSLCerts.FindByDomainID(dom.ID)` per domain (:266) | N = #domains |
| Mailboxes | `Mailboxes.ListByDomainID(dom.ID)` per domain (:286) | N = #domains |
| Autoresponder | `Autoresponders.FindByMailboxID(mb.ID)` per mailbox (:295) | N = #mailboxes |
| Shares | `MailboxShares.FindByOwnerID(mb.ID)` per mailbox (:310) | N = #mailboxes |
| Forwarders | `Forwarders.ListByDomainID(dom.ID)` per domain (:325) | N = #domains |
| DNSSEC keys | `DNSSECKeys.ListByDomainID(dom.ID)` per domain (:335) | N = #domains |
| DNS zone | `DNSZones.FindByDomainID(dom.ID)` per domain (:347) | N = #domains |
| DNS records | `DNSRecords.ListByZoneID(zone.ID)` per zone (:348) | N = #zones |
| Docker ports | `DockerApps.ListPortsForApp(a.ID)` per app (:91) | N = #docker-apps |

Everything else is already user-scoped (Databases, DatabaseUsers, PHPPools, Domains, AppInstalls, DockerApps, SSHKeys, CronJobs, FtpAccounts, LimitOverrides [JAB-374 #1268], Egress). The same domain→mailbox N+1 is duplicated in the manual (`api/backups.go`) and scheduler (`backupscheduler/scheduler.go`) inventory loops.

Secondary: many per-item reads swallow their error (`_`), so a transient failure silently omits restorable state with no warning.

## Design — `AccountSnapshotStore` behind the existing `Build` interface
Add `backupmetadata.SnapshotStore.Load(ctx, userID) (*AccountSnapshot, []Warning, error)` — a read adapter that batch-loads every owned collection, groups rows in memory by parent id, and hands `Build` a fully-materialized snapshot. `Build`'s signature and its emitted `AccountMetadata` are unchanged; only its internals swap from per-item reads to snapshot lookups. Because all three callers already share `Build`, replacing its internals switches all three adapters at once — the golden test (below) is what guarantees equivalence, so we don't need the ticket's "one adapter at a time" (that assumed three separate implementations).

**Load order (each line = one query per resource type, never per row):**
1. User-scoped base: databases, db-users, domains, app-installs, docker-apps, ssh-keys, cron-jobs, ftp, php-pool(+ini), limit-override, egress. (already single-query)
2. Grants: `ListByDatabaseUserIDs(dbUserIDs)` → group by db-user id.
3. By domain ids: `SSLCerts.FindByDomainIDs`, `Mailboxes.ListByDomainIDs` (NEW), `Forwarders.ListByUserID` (group by domain in memory), `DNSSECKeys.ListByDomainIDs` (NEW), `DNSZones.FindByDomainIDs` (NEW) → group by domain id.
4. By zone ids: `DNSRecords.ListByZoneIDs` (NEW) → group by zone id.
5. By mailbox ids (from step 3): `Autoresponders.ListByMailboxIDs` (NEW), `MailboxShares.ListByUserID` (group by owner-mailbox id).
6. By docker-app ids: `DockerApps.ListPortsForApps` (NEW) → group by app id.

Grouping is in-memory `map[parentID][]row`; no giant JOIN (avoids row multiplication).

### Repo batch methods
**Reuse (exist):** `DatabaseGrants.ListByDatabaseUserIDs`, `SSLCerts.FindByDomainIDs`, `Forwarders.ListByUserID`, `MailboxShares.ListByUserID`, `Mailboxes.FindByIDs`.
**Add (6), each `WHERE parent_id IN (?)` + a repo test:** `Mailboxes.ListByDomainIDs`, `Autoresponders.ListByMailboxIDs`, `DNSSECKeys.ListByDomainIDs`, `DNSZones.FindByDomainIDs`, `DNSRecords.ListByZoneIDs`, `DockerApps.ListPortsForApps`. Each mirrors its existing single-id sibling's projection exactly.

### Failure policy (AC) — equivalence-preserving in v1
v1 keeps **exactly today's tolerance set**, just makes it visible: every read that is swallowed with `_` today becomes a structured `Warning` (naming the section + parent scope) instead of silence, but is still non-fatal. **Nothing currently tolerated becomes "required" in this PR** — turning a swallowed grants-error into a snapshot failure would change behavior (a transient DB blip that produces a backup today would produce *no* backup), which must not ride a perf change. Reclassifying any section as required is a **separate, operator-approved follow-up**, not part of JAB-374. `Build` surfaces warnings via the existing `Deps.warn` sink so callers log identically to today.

## Duplicate inventory (real sites)
These are NOT the snapshot rows — they build `[]string` of mailbox `EmailCached` for the backup content selector, a different shape, so they stay out of `SnapshotStore` but reuse the new batch method:
- `BackupHandlerConfig.allUserMailboxes` (`api/backups.go:1356`, per-domain `ListByDomainID` at :1371) and the second copy at `:1539`.
- The scheduler's equivalent domain→mailbox loop in `backupscheduler/scheduler.go`.

Fix: one `Mailboxes.ListByDomainIDs(domainIDs)` call, then extract `EmailCached`, replacing the per-domain loops. Small, self-contained; can ship in the same PR or a trivial follow-up.

## Tests
- **Golden equivalence — write it FIRST.** Before touching `Build`'s internals: capture the current builder's `AccountMetadata` JSON for a populated fixture (fakes returning fixed rows) as a golden file, and land a test asserting the OLD builder reproduces it. Then swap internals and require byte-identical output for the admin, tenant, and scheduler paths. The golden gate is what makes "switch all three adapters at once" safe.
- **Order preservation (the #1 false-failure trap):** today's output order is *iteration order* — parents in `ListByUserID` order, children in each single-parent query's `ORDER BY`. The batched version must reproduce that exactly: iterate parents in the same list order, and each new `...ByDomainIDs/ByZoneIDs/ByMailboxIDs` batch method must `ORDER BY <parent_id>, <the same key the single-id query used>` (read each existing `ListByDomainID`/`ListByZoneID` for its ORDER BY and replicate it). Do NOT impose a fresh sort — match the old order, or the golden diff fails for a non-reason.
- **Query budget — assert an absolute, locked number.** Counting fake repos (each batch method bumps a per-method counter). Two fixtures (1-of-each vs 100+-of-each) must record the identical count AND that count must be `<=` an explicit locked budget (~≤ 21 batch calls for a fully-populated account; compute the real number and pin it). Equality-between-fixtures alone would pass a refactor that added an unconditional per-user query; the absolute cap catches that. A regression has to consciously bump the number.
- **nil-repo tolerance (explicit test):** a `Deps` with an association repo nil (e.g. `Autoresponders == nil`) → the snapshot skips that section, no panic, and output equals today's with that section omitted. (A missed nil-guard is a 04:30 scheduler panic — the silent-failure class.)
- **Grouping correctness:** rows land on the right parent (cert on its domain, records on their zone, autoresponder on its mailbox, ports on their app).
- **Failure policy:** an association batch returning an error yields a warning + a coherent partial snapshot (still non-fatal — matches today's tolerance).
- **Concurrency (optional, deferred):** the ticket allows a repeatable-read snapshot; v1 does NOT take one (metadata is advisory; a single read-only pass suffices). Flagged as a follow-up so we don't silently drop the AC.

## Staging
One cohesive PR, in this order: **(1) land the golden test against the OLD builder (must pass before any change).** (2) add the 6 batch repo methods + repo tests (ORDER BY matching their single-id siblings). (3) add `SnapshotStore.Load`. (4) swap `Build` internals. (5) repoint the `allUserMailboxes`-style loops. (6) query-budget + nil-repo tests. Rebase-verify + re-run. Output is golden-locked, so the ticket's "switch adapters one at a time" safety comes from the golden test, not separate PRs.

## Risks / watch-items
- **Output drift:** the whole point is zero drift — golden test is the gate; write it BEFORE swapping internals.
- **Ordering:** must *reproduce* today's iteration order, not impose a new sort — see the "Order preservation" test item. This is the most likely non-real golden failure.
- **`nil`-repo tolerance:** `Build` today nil-checks each `Deps.X`; the snapshot store must keep skipping a nil repo (a caller that doesn't wire one), not panic.
- **Query-count infra:** counting fakes are the pragmatic proxy; a real-SQL count (GORM callback / sqlmock) is stronger but heavier — start with counting fakes, escalate only if the AC reviewer wants true SQL counts.
- **Admin server-level docker apps** (`ListAll`) stays a single query (legitimately all rows) — not an N+1, leave as-is.
