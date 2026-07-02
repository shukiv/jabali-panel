# Blueprint — WordPress SSH Migration · GH #647

**Objective:** migrate a WordPress site into Jabali from **any SSH-capable source** (Cloudways, generic VPS, RunCloud/Ploi, old shared hosts) — no cPanel/DA/Hestia account backup required. Jabali connects by SSH, discovers the WP install, exports the DB (`wp db export`/`mysqldump`), rsyncs the files, then imports + rewrites config + verifies.

**Relationship to #648 (READ FIRST — they are ONE feature, two transports).** #647 = **SSH** transport; #648 = **REST-pull** transport (jabali-migrator plugin). Both discover the same WP facts (home/siteurl/prefix/size), both land WP **files + a SQL dump** in staging, and both then run the **same WordPress import** (create Jabali DB → import SQL → files→docroot → rewrite `wp-config.php` → serialized-safe search-replace → perms → cache flush → health). **Build the shared WP import ONCE (S4 here) and #648 reuses it.** Don't fork two WP imports.

**Grounding (verified via the graph — reuse, don't reinvent):**
- **Pull runner dispatches by `job.SourceKind`** (`panel-api/cmd/server/migrate_pull_cmd.go` `newMigratePullSourceCmd`): `switch job.SourceKind { cpanel/whm → pullCpanel; directadmin → pullDirectAdmin; hestia → pullHestia; default → error }`. Pre-switch is SHARED: secret load (`migrate.SecretsDir/<job>.env`), SSRF gate (`s.MigrationAllowPrivateHosts`, default deny-private), local staging `/var/lib/jabali-migrations/<job>/`, `markPullFailed`. **Add a `wordpress_ssh` arm.** NOTE: every existing arm produces a **tarball → `extractTar`**; the WP arm does NOT (rsync files + a separate SQL dump), so it needs its own stage/import, not the tarball-extract path.
- SSH primitives: `internal/migrate/sshpull.go` `PullFileViaSSH`; SSH host-key pinning (`job.ExpectedHostKey`, GH #461); the migration-secret store + `jabali-migration-secrets-reap.timer`; `preflightDAPivot`-style remote command exec over the pinned session.
- `internal/migrate/discover.go` `Discoverer` (Connect/ListAccounts/DescribeAccount/Close) + `cpanel/directadmin/hestiacp` implementations to mirror. Add `internal/migrate/wordpressssh/`.
- `migration_jobs` model + stages (`MigrationStage.BytesProcessed` for progress) + `RegisterAdminMigrationRoutes` + `isKnownSourceKind` (add the kind).
- WP config rewrite / serialized search-replace precedent: `wordpress_clone.go` (openat2 no-symlink `wp-config.php` write) + `wp search-replace`.
- SSRF: `MigrationAllowPrivateHosts` + the SSRF floor — same gate #648 needs (A4); **pin the resolved public IP** for the SSH connect too, not just a name check.

**Workflow:** git + gh; branch per step; ADR target: **ADR-00NN (wordpress_ssh + shared WordPress import)**.

---

## Architecture decisions

**A1 — Shared WordPress import (S4) is the spine.** A new `migration.import_wp` agent verb: given a staging dir holding `{wp files tree, dump.sql, discovered facts}` + a dest {user, domain, docroot}, it creates the Jabali DB/user, imports the SQL, moves files→docroot, rewrites `wp-config.php` to Jabali creds, runs serialized-safe search-replace on domain change, fixes perms, flushes cache/opcache, health-checks. **#647 and #648 both call it.** The existing `migration.import_run` is cPanel-account-shaped — do NOT reuse it for WP.

**A2 — SSH source runs remote commands = full RCE on the source.** The operator's SSH credential can run arbitrary commands on the source (that's the point — `wp db export`). Admin UI must warn clearly. Tenants: destination is owner-scoped; the SOURCE is the tenant's own external site (their credential), but Jabali still executes remote commands with it — surface that.

**A3 — Tenant ownership on the DEST is hard-enforced** (a tenant must never rsync into another user's home). Mirror the #648 owner-coherence check; the agent import writes ONLY under the resolved dest docroot (containment, like `migrationImportHome`).

**A4 — Secrets + remote temp are reaped on terminal state** (existing secret reaper) AND the remote `/tmp/jabali-wp-migrate-<job>.sql` is deleted on success AND failure (defer on every path).

---

## Dependency graph

```
S1 (kind + job/secret shape) ──> S2 (WP-SSH Discoverer: connect, find wp-config, verify, read home/siteurl, size) ──> S3 (SSH pull stage: rsync files + wp db export → staging) ──> S4 (SHARED WordPress import ⋆) ──> S5 (health + cleanup)
S6 (mint/create API, owner-scoped) ──> S2 ;  S7 (progress) cross-cuts S2–S5 ;  S8 (UI) ∥ after S6 ;  S9 (SSRF + adversarial gate) gates merge of S2–S4.
⋆ S4 is the shared spine reused by #648.
```

## S1 — `wordpress_ssh` kind + job/secret shape

**Tasks**
- Add `MigrationSourceWordPressSSH = "wordpress_ssh"` + `isKnownSourceKind`. A `wordpress_ssh` job uses the EXISTING job fields: `source_host`, `source_user` (SSH login), `expected_host_key`. SSH password / private key → the existing migration-secret store (`<job>.env`, reaped) — NOT a new table (#647 needs no key model like #648).
- **WP root path → a new nullable `source_path` column** (one additive, schema-only migration). Do NOT overload `manifest_json`: that field is the Discoverer's `AccountManifest` *output*; `source_path` is control-*input*, modeled like `source_host`/`source_user`. The discovered facts (home/siteurl/prefix/size) go in `manifest_json` after S2, as the existing kinds do.

**Verify:** build; kind allowlisted. **Exit:** a `wordpress_ssh` job can be created + carry SSH creds via the secret store. **Rollback:** revert the kind.

## S2 — WordPress-SSH Discoverer  ·  ⚠ SSRF (A4/S9)

**Tasks**
- `internal/migrate/wordpressssh/` Discoverer. `Connect(host, user, secret, expectedHostKey)` over SSH (reuse the pinned-host-key session), **SSRF-gate the host** (resolve → public-IP only unless `MigrationAllowPrivateHosts`; pin the IP).
- Discover WP root: try the user path first, else auto-detect `~/public_html`, `/home/*/public_html`, **Cloudways `/home/master/applications/*/public_html`**. **Require the chosen root to contain `wp-config.php`** and verify with `wp core is-installed --path=<root>` when WP-CLI exists (fallback: parse `wp-config.php` for `DB_*`).
- Read `home` + `siteurl` (`wp option get` or parse), table prefix, estimate size (`du -sb <root>`). Return the manifest.

**Verify:** unit/integration against a fixture WP over SSH: finds root, verifies install, reads home/siteurl; a path without `wp-config.php` is refused; a private/metadata host is refused. **Exit:** verified WP + facts before any transfer. **Rollback:** don't register.

## S3 — SSH pull stage: rsync files + DB export → staging

**Tasks**
- New `pullWordPressSSH(ctx, sshUser, job, secret, localDir, allowPrivate)` arm in `newMigratePullSourceCmd`'s switch. Steps (each a `migration_stage`): (a) **DB export** on the source — `wp db export /tmp/jabali-wp-migrate-<job>.sql --path=<root> --single-transaction --quick --skip-lock-tables`, fallback parse `wp-config.php` + `mysqldump` (**never put the DB password in argv/log** — use a temp `--defaults-extra-file` on the source, deleted after); pull the SQL into `<staging>/dump.sql`; **delete the remote temp SQL on success AND failure**. (b) **rsync** the WP root → `<staging>/files/`, excluding transient/cache dirs (`wp-content/cache`, object-cache/advanced-cache drop-ins, `*.log`), preserving nothing Jabali-managed. **The exclude list is one shared source of truth with #648's file walk** — define it once (a shared constant) so the two transports don't drift. (c) hand to S4 (do NOT auto-import destructively without the operator gate — mirror #648 A3; auto-kick is acceptable only for a fresh empty dest).

**Verify:** integration — a fixture source yields `<staging>/{dump.sql, files/}`; remote temp SQL gone; DB password not in any log/argv; excludes applied. **Exit:** WP files + SQL staged, source clean. **Rollback:** reaper clears staging.

## S4 — ⋆ CANONICAL SHARED SPINE: `migration.import_wp` (reused by #648) · ⚠ containment

> **This step is the shared prerequisite for BOTH #647 and #648, not a branch of #647.** Whichever ships first BUILDS it; the other REUSES it (the #648 plan S7 points here). Do not fork a second WP import. If executing in separate sessions, treat this section as the single source of truth for the WP import contract.

**Tasks**
- New agent verb `migration.import_wp`: input `{staging dir (holding `files/` + `dump.sql`), dest user/domain/docroot, domain_change, old_url, new_url, prefix}`. Create the Jabali DB + user; import `dump.sql`; move `files/` → dest docroot (**containment**: write ONLY under docroot, reuse `TestMigrationImportHomeContainment` logic, reject symlink/`..`); rewrite `wp-config.php` to Jabali DB creds/socket (reuse `wordpress_clone.go` openat2 no-symlink write); **serialized-safe `wp search-replace <old> <new>`** when `domain_change`; fix perms (dest-user:www-data); flush object/opcache; strip foreign source cache drop-ins (mirror #621).
- **Confirm dest-side WP-CLI (do not assume):** `search-replace` runs `wp` against the imported dest DB. The cache/clone paths plausibly already run `wp` dest-side — verify it's guaranteed. If NOT, the serialized-safe replace needs a **bundled PHP unserialize-walk fallback** (real work, not a given) so PHP-serialized option data isn't corrupted by a naive string replace.
- Register kind-dispatched alongside `import_run`: `wordpress_ssh`/`wordpress_plugin` → `import_wp`.

**Verify:** on a real source→dest: site serves; domain-change rewrites serialized option data; wp-config points at Jabali; no write escapes docroot. **Exit:** a staged WP payload imports correctly, from EITHER transport. **Rollback:** dest is a fresh app; delete + retry.

## S5 — Health verify + cleanup + secret/temp reap

**Tasks**
- WP/HTTP health probe (reuse `migration.http_probe` / `--resolve`); success → job `done`. Terminal cleanup (any outcome): wipe staging, delete the secret (reaper + explicit), confirm no remote temp SQL survives.

**Verify:** success → healthy + staging + secret gone; failure likewise. **Exit:** nothing sensitive outlives terminal state. **Rollback:** n/a.

## S6 — Create/mint API (AUTHENTICATED, owner-scoped)

**Tasks**
- `POST /admin/migrations/wordpress-ssh` (admin) + a tenant-owner-scoped route: body {source host/port/user, password|key, expected_host_key?, source_path?, dest user/domain, domain_change}. Owner-coherence on dest (mirror #648 S2). Store SSH creds in the secret store. Kicks discover→pull→import via the existing agent/transient-unit path. Revoke/cancel + status.
- Do the migration **handler-mock scaffolding** here (the migration API has zero handler tests today) — shared with #648's deferred S2 test.

**Verify:** handler tests: tenant unowned dest → 404; creds land in the secret store (not the DB row); host-key pinning honored. **Exit:** authed create, owner-scoped, tested. **Rollback:** routes off.

## S7 — Progress stages (cross-cuts S2–S5)

**Tasks**
- Emit `migration_stages` per the issue's stages: connect, discover, export-db, sync-files, import-db, rewrite-config, search-replace, verify. `GET /admin/migrations/:id` returns them (existing model). M14 events on state change. Actionable stage errors + safe retry (existing resume scans failed stages).

**Verify:** stages advance + a failed stage shows an actionable error + retry re-runs from the failed stage. **Exit:** live, retry-safe progress. **Rollback:** read-only.

## S8 — Panel UI: Applications → WordPress → Migrate existing site (SSH)  ∥ after S6

**Tasks**
- Migration entry in Applications (admin + user). SSH source form (host/port/user/password-or-key/host-key), WP path (or auto-detect note incl. the Cloudways preset), dest select (tenant own only; admin any), domain same/change with detected home/siteurl confirm, **RCE warning (A2)**, live stage progress (S7). Repo Drawer/SearchableTable conventions; `npm run build` + a component test + a browser drive. Shares the WordPress-migrate shell with #648's plugin mode (source-type tab: SSH | plugin).

**Verify:** `npm run build` clean; component test; manual drive of connect→discover→migrate→verify. **Exit:** operator drives it from the UI. **Rollback:** UI-only.

## S9 — SSRF + adversarial gate (GATES merge of S2–S4)

**Tasks**
- Adversarial: source host = loopback/RFC1918/link-local/metadata/DNS-rebind → refused (unless the admin toggle); a malicious source serving a huge/looping DB dump or a files tree with `..`/symlink → bounded + write-refused; DB password never in argv/log (grep the process table + logs in the test); remote temp SQL cleaned even when a later stage fails; cross-tenant dest refused.
- **Non-negotiable:** a real end-to-end SSH migration on the test server (a fixture WP source → dest) + at least one adversarial case (SSRF host AND a traversal file path) refused at runtime (unit tests lie at trust boundaries — cf. #601/#637 runtime-caught bugs).

**Verify:** adversarial suite green AND a live source→dest migration succeeds. **Exit:** SSH-source path is hostile-input-safe at runtime. **Rollback:** if any adversarial case fails, S2–S4 don't merge.

---

## Anti-patterns (learned)
- Don't reuse `migration.import_run` (cPanel-account-shaped) for WP — build the shared `import_wp` (A1).
- Don't put the DB password in argv/logs — temp defaults-file on the source, deleted (S3/S9).
- Don't leave the remote temp SQL on failure — defer-delete on every path (A4/S3).
- Don't rsync outside the dest docroot / trust the source's paths — containment (A3/S4).
- Don't SSRF-check the name then connect — pin the resolved IP (A4/S2/S9), same as #648.
- Don't skip the runtime adversarial test — trust boundaries lie in unit tests (S9).
- Migration = schema only; no seeding from app-populated tables ([[feedback_migration_data_seed_ordering]]).
