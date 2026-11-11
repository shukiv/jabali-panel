# Blueprint — M35 Re-Migration / Refresh Mode (GH #646)

Add a guard-railed **refresh** mode to the M35 migration importer: re-pull a
live account (files + DB, optionally mail) from the source into an account
already migrated into jabali. Today validate blocks (`ConflictDomainTaken`) and
the idempotent writers skip existing DBs/mail — correct for *resume*, wrong for
*refresh*. This encodes the manual procedure run on 2026-07-02
(`secureiskfuture` / `sec.example-q.co.il`, 5.1 GB WP, cPanel→jabali) as a
supported, backed-up, `--force`-gated operation.

## Load-bearing invariants (do not violate)

1. **Backup before overwrite is MANDATORY, not optional.** Every refresh takes a
   dest files snapshot (hardlink) + DB dump FIRST; if the backup fails, the
   refresh aborts before touching anything. Mirrors `account_restore_cmd.go`.
2. **Never clobber jabali-managed files.** The file mirror excludes
   `wp-config.php`, `wp-content/object-cache.php`, `wp-content/advanced-cache.php`,
   `.user.ini`, and the jabali-cache mu-plugin. The source's DB creds + Redis/PHP
   integration must survive — importing the source wp-config would break the dest.
3. **DB name + table prefix stay the dest's.** Reimport source→dest into the
   SAME dest DB name + prefix so the preserved dest wp-config remains valid. If
   the source prefix differs, rewrite it during import (documented edge case).
4. **`--force` gate.** Refresh on a live host refuses without `--force`
   (precedent: `jabali backup account-restore --force`).
5. **Re-stamp jabali cache constants after refresh** (GH #621 path) — the rsync
   excludes wp-config, but a DB reimport can carry source cache options; re-enable
   cache to re-stamp + strip any source `JABALI_CACHE_*`.

## Dependency graph

```
A validate override + mode plumbing (gate)
        │
        ▼
B mandatory pre-overwrite backup ── C force-refresh writers (files + DB)
                                            │
                                            ▼
                                     D post-refresh reconcile + verify
```

A gates all. B before C (backup precedes overwrite). D after C.

---

## Wave A — Validate override + `mode=refresh` plumbing  [GATE]

**Context brief.** `migration_jobs` has no `mode`. `validate.go` raises
`ConflictDomainTaken` for an existing domain with only an `acceptExistingUserID`
escape. Add a `refresh` mode + an accept-existing-domain override so a job can
legitimately target its own live account.

**Tasks.**
1. Migration: `migration_jobs.mode ENUM('import','refresh') NOT NULL DEFAULT 'import'`.
2. `validate.go`: add `acceptExistingDomain bool` (parallel to
   `acceptExistingUserID`); when set + `mode=refresh`, downgrade
   `ConflictDomainTaken` to a warning AND assert the existing domain belongs to
   the SAME target user (never let a refresh cross-tenant).
3. CLI: `jabali migrate import --refresh --force`; API/drawer: a "Refresh
   (re-pull)" toggle that sets `mode=refresh` + requires an explicit confirm.
4. Wire `mode` through the job repo + runner.

**Verify.** A refresh job for an existing domain owned by the target user passes
validate; a refresh targeting another tenant's domain still hard-fails; a plain
import still blocks on an existing domain.

**Exit.** A refresh job validates against its own live account only; `--force`
required; `mode` persisted + threaded to the runner.

---

## Wave B — Mandatory pre-overwrite backup  [before C]

**Context brief.** Encodes the manual safety step: hardlink snapshot of the dest
docroot + `mysqldump` of the dest DB, taken BEFORE any overwrite, abort on
failure.

**Tasks.**
1. Agent verb `migration.refresh_backup` (or reuse `backup.create` account scope):
   `cp -al <docroot> <docroot>.pre-refresh-<ts>` (hardlink, near-instant, cheap)
   + `mysqldump <destdb> > <staging>/pre-refresh-<ts>.sql`.
2. Runner: a `refresh_backup` stage that runs FIRST when `mode=refresh`; a
   failure aborts the job (no partial overwrite).
3. Record the snapshot + dump paths on the job for rollback + operator reference;
   retention/cleanup after N successful refreshes.

**Verify.** A refresh with an unwritable staging dir aborts at the backup stage
with the dest untouched; a successful backup leaves a restorable snapshot + dump.

**Exit.** No refresh proceeds without a durable dest backup.

---

## Wave C — Force-refresh writers (files + DB)  [after B]

**Context brief.** Replace the idempotent "skip existing" behavior with a forced
overwrite for `mode=refresh`, preserving jabali-managed files.

**Tasks.**
1. Files: `rsync -rlptD --delete` source→dest **excluding**
   `wp-config.php object-cache.php advanced-cache.php .user.ini` + the jabali-cache
   mu-plugin (invariant 2). The exclude list is a shared constant, unit-tested.
2. DB: for `mode=refresh`, DROP the dest tables (same DB, same prefix) and reimport
   source→dest — do NOT create a new DB row (invariant 3). If source prefix ≠ dest
   prefix, rewrite during import.
3. `runner.go`: gate the "skip already-imported" branches on `mode != refresh`;
   for refresh, run the forced writers. Mail refresh is opt-in (a flag).

**Verify.** After a refresh, dest files match source (minus the excluded set), the
dest wp-config + drop-ins are byte-identical to before, and the dest DB content
equals source; a golden test asserts the exclude list.

**Exit.** Files + DB force-refreshed; jabali integration files preserved; DB
identity unchanged.

---

## Wave D — Post-refresh reconcile + verify  [after C]

**Context brief.** The manual tail: ownership, FPM/opcache reload, URL rewrite,
cache purge, health check.

**Tasks.**
1. `chown -R <user>:www-data <docroot>`; re-fix docroot group (restore convention).
2. FPM reload (`jabali-fpm@<user>`, opcache), `wp cache flush`, purge the nginx
   fastcgi micro-cache for the domain (`nginx.cache.purge`), re-stamp jabali cache
   constants (invariant 5 / GH #621).
3. `wp search-replace <old-url> <new-url>` when the domain/path changed.
4. Health check: HTTP 200 on `/`, `wp db check`, wp-login reachable — surfaced in
   the job result. On failure, point the operator at the Wave-B snapshot for
   rollback.

**Verify.** Live E2E on a test VM: refresh a real WP account, confirm 200 + DB
content refreshed + wp-config/cache integration intact + old snapshot restorable.

**Exit.** A refresh leaves a working, correctly-owned, cache-consistent site with
a one-command rollback path.

---

## Cross-cutting

- **ADR:** amend ADR-0094 (M35) with the refresh-mode semantics + the
  backup-before-overwrite + jabali-file-preservation invariants.
- **Runbook:** extend `plans/m35-migration-importers-runbook.md` §2.5 with the
  refresh flow + the rollback procedure (restore from the pre-refresh snapshot).
- **Safety tests:** exclude-list golden test; validate cross-tenant-refuse test;
  backup-failure-aborts test; live-VM refresh E2E (the 2026-07-02 case as the
  fixture).
- **Never** run refresh writers before Wave B's backup succeeds — enforce with a
  runner stage ordering assertion, not just convention.
