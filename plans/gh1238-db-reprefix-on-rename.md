# GH #1238 — Re-prefix tenant DBs / DB users / shadow roles on user rename

**Why:** rename alice→bob keeps every DB artifact on the `alice_` prefix (v1 kept
them because MariaDB has no `RENAME DATABASE`). johnnyq: a later new user "alice"
then collides — and the `mysqladmin` GRANT is a `alice_%` wildcard, so a fresh
`alice_mysqladmin` would reach bob's retained `alice_*` DBs (tenant-isolation
gap). Operator picked the full re-prefix.

**Contract change:** this reverses the original "DBs keep old prefix" decision.
It MOVES tenant data and BREAKS every affected app's config (wp-config.php DB name
+ DB user) until the admin updates it — a loud warning is mandatory in the modal
+ CLI output + the issue reply.

## Mechanism (the safety-critical choice)

**MariaDB DB rename = `RENAME TABLE` across schemas, NOT dump/restore.**
Per DB `alice_wp_1` → `bob_wp_1`:
1. `CREATE DATABASE \`bob_wp_1\``
2. For each base table: `RENAME TABLE \`alice_wp_1\`.\`t\` TO \`bob_wp_1\`.\`t\``
   — a metadata move on the same filesystem: instant, no data copy, atomic.
3. Recreate views (their definitions bind the old schema name), and MOVE any
   triggers/events/routines (schema-bound).
4. `DROP DATABASE \`alice_wp_1\`` (now empty).

- **v1 boundary (open decision, below):** base tables + views + triggers are the
  common WP/app shapes. Stored **routines/events** are rare; v1 either moves them
  or REFUSES the rename with a clear message if present.
- **Postgres is easier:** native `ALTER DATABASE x RENAME TO y` (requires no open
  connections → terminate first, we already have db.postgres.terminate) and
  `ALTER ROLE … RENAME TO`. No table shuffling.

**DB users:** MariaDB `RENAME USER`, PG `ALTER ROLE … RENAME`; both preserve
grants+hash (PG MD5 breaks → reset). Re-point per-DB grants at the new DB names.

**Shadow roles (mysqladmin/pgadmin):** `RENAME USER`/`ALTER ROLE RENAME`, then
re-grant on the NEW `bob_%` wildcard (the old `alice_%` grant no longer matches
the moved DBs), and reset password if needed.

## New agent verbs (none exist today)

- `db.rename_db` (MariaDB): CREATE new + RENAME TABLE each + recreate views/
  triggers + DROP old. Enumerate objects via information_schema.
- `db.rename_user` (MariaDB): RENAME USER + optional re-grant.
- `db.postgres.rename_db`, `db.postgres.rename_role` (native ALTER).
- Re-grant reuses db.mysqladmin.ensure / db.postgres.grant with the new prefix.

## Panel side

- `userops.RenameUser` (shared by CLI #1305 + WebUI #1346): after the OS rename +
  docroot repoint, iterate the owner's `databases` + `database_users`, rename each
  (engine-aware), then the shadow roles; update `databases.name`,
  `database_users.username`, `users.mysqladmin_username`/`pgadmin_username` via
  dedicated setters (the `Update` allowlist drops these cols — see
  [[feedback_domain_update_allowlist_silent_drop]]).
- Idempotent + abort-with-resume: each rename checks "already at new name?" and
  skips; a partial failure is recovered by re-running (the existing rename
  contract). Order: DBs → DB users → shadow roles, so grants re-point correctly.
- Preflight: keep the FTP/Python refusals; ADD a refusal if a MariaDB tenant DB
  holds stored routines/events AND v1 doesn't move them (decision below).

## UI / CLI

- Rename modal + `jabali user rename` output: a prominent warning listing the old
  → new DB name(s) + DB user(s), and "update your app config (wp-config.php etc.)
  to the new database name and user — the panel can't rewrite app files."

## Tests + box drill (MANDATORY — moves real tenant data)

- Unit: userops orchestration with fake agent+repos (renames called in order,
  panel rows updated, idempotent re-run no-ops).
- Agent: RENAME TABLE enumeration/move (sqlmock or a scratch DB).
- **Box drill on .60**: a tenant with a real WP DB → rename → verify the tables
  moved into the new DB, the DB user + shadow role renamed, grants work under the
  new names, and the panel rows updated. Confirm the app is reachable once its
  config points at the new DB name/user.

## OPEN decision to confirm before building

MariaDB **stored routines/events** in a tenant DB (rare): v1 **moves** them, or
**refuses** the whole rename with "this DB has stored routines; not yet supported"?
Refusing is safer for v1; moving is more complete.
