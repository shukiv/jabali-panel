# M35 Migration importers — operator runbook

**Scope:** Day-to-day runbook for operators running account migrations
into jabali2. v1 covers cPanel, DirectAdmin, HestiaCP, and WHM-pkgacct
via the cobra CLI (`jabali migrate import`) and the admin SPA at
`/jabali-admin/migrations`. IMAP-only source requires imapsync manually
(operator path documented in § 2.x below).

**Related:** ADR-0094, `plans/m35-migration-importers.md` (blueprint).

---

## 1. Pre-requisites

On the source cPanel host:
- WHM root SSH access (or per-account SSH for single-user mode).
- `pkgacct` available (cPanel ships it; verify `which pkgacct`).
- Disk headroom in `/home/` for the produced `cpmove-<user>.tar.gz`
  (typically 50-100% of the source account's `/home/<user>` size).

On the jabali destination host:
- `server_settings.migrations_enabled = 1` (default off — flip via
  SQL or future admin UI).
- `/var/lib/jabali-migrations/` exists 0750 root:jabali (install.sh
  creates it).
- Network reachability to the source SSH port from the destination.

## 2. Per-account migration workflow

### 2.1 Pre-create the destination user

The runner refuses to run if the destination jabali user doesn't
already exist (validate stage's ConflictTargetUserExists is
intentional inverse — we don't want migration silently overwriting
an existing account, AND we don't want migration creating a partial
user without the kratos identity that the v1 CreateUser orchestrator
gap leaves on the table).

Pre-create via the admin UI at `/jabali-admin/users` or via
`jabali user create --email ... --username ...`. Pick a username
that doesn't collide with the source-side cPanel name if you want
the source name preserved (e.g. cPanel `bob` → jabali `bob`); the
runner accepts any valid POSIX username.

### 2.2 Create a migration job

**Via admin SPA** (preferred): open `/jabali-admin/migrations`, click
**New Migration**, select source kind, fill host / credentials / source
username, and submit. The drawer walks through the multi-step workflow.

**Via CLI** (alternative for scripting / headless hosts): the API
creates the job on the first `jabali migrate import` run when passed
`--source-kind`, `--source-host`, `--source-user`, and `--target-user`.

**Via SQL** (break-glass only):
```sql
INSERT INTO migration_jobs
  (id, source_kind, source_host, source_user, state, started_at, created_at, updated_at)
VALUES
  ('<NEW_ULID>', 'cpanel', 'src.example.com', 'bob', 'pending', NOW(6), NOW(6), NOW(6));
```
The ULID can be generated via `jabali ulid` or any online ULID
generator. Note the value — every subsequent step needs it.

### 2.3 Pull the cpmove tarball

Two paths:

**Path A — if you have root SSH on the source:**

The cobra CLI is half-wired for this — it runs the per-area
writers but assumes the tarball is already extracted. Run pkgacct
manually for v1:

```sh
ssh root@src.example.com "/scripts/pkgacct bob"
# Produces /home/cpmove-bob.tar.gz on the source

scp root@src.example.com:/home/cpmove-bob.tar.gz \
    /var/lib/jabali-migrations/<JOB_ULID>/

# Extract
mkdir -p /var/lib/jabali-migrations/<JOB_ULID>/extracted
tar -xzf /var/lib/jabali-migrations/<JOB_ULID>/cpmove-bob.tar.gz \
    -C /var/lib/jabali-migrations/<JOB_ULID>/extracted/
```

**Path B — operator-supplied tarball:**

If you already have a cpmove tarball (delivered by the source
operator), drop it under `/var/lib/jabali-migrations/<JOB_ULID>/`
named `cpmove-<source-user>.tar.gz` and run the extract step.

### 2.4 Run the importer

```sh
jabali migrate import \
  --job-id <JOB_ULID> \
  --target-user bob
```

The runner walks four stages:

```
analyze   → no-op for v1 (Discoverer ran during the pre-fill;
             callback unwired)
fix_perms → no-op for cPanel
validate  → checks for blockers (target user must exist,
             every domain in the manifest must NOT already be
             registered in jabali, target username must be valid
             POSIX). Halts BEFORE restore on any blocker.
restore   → ssh keys → cron → databases → DNS → home
```

Progress streams to stdout. Final state lands in `migration_jobs.state`
(done / failed / cancelled) and warnings list in
`migration_jobs.manifest_json`.

### 2.5 Resume after partial failure

Re-run the same command:

```sh
jabali migrate import --job-id <JOB_ULID> --target-user bob
```

The runner skips already-done stages (state='done' in
migration_stages) and picks up at the first failed/pending one.
Idempotent on every per-area writer — ssh keys dedupe by
fingerprint, cron writes Enabled=false rows so the operator can
review-then-flip, databases skip already-imported (UNIQUE on
user_id+name), DNS upserts (pdns idempotent), home rsync
--delete-after.

### 2.6 Post-migration handoff to the user

1. Send a password-reset link (Kratos identity creation deferred
   per v1 CreateUser gap — operator pre-created the panel user
   with no kratos identity; first password set creates the
   identity).
2. Cron jobs all land Enabled=false. Operator reviews via
   `/jabali-panel/cron` and flips on the ones that pass the wp /
   php allowlist; rest need rewriting.
3. Mailboxes ARE imported via JMAP Email/import as of commit
   `a5d2dc25` (per-area writer in cpanel/restore_mail.go +
   panel-agent migration.import_mailboxes). Pre-requisites: the
   destination user's mail domain must already exist in Stalwart
   (run domain.email.enable on the destination domain BEFORE the
   restore stage; the panel-side admin Mail tab does this). The
   restore-stage runner reports messages_pushed + bytes_pushed in
   the manifest_json warnings list. Idempotent: Stalwart's
   Email/import dedupes on Message-ID, so resuming a partial
   import is a no-op for already-ingested messages.

## 3. Inspection + cancellation

### 3.1 List jobs

```sh
curl -k -H "Cookie: ory_kratos_session=..." \
  https://<vm-host>:8443/api/v1/admin/migrations | jq
```

Returns `{data: [...], total: N, page: 1, page_size: 50}`.

### 3.2 One job + stages

```sh
curl ... /api/v1/admin/migrations/<JOB_ULID> | jq
```

Returns `{job: {...}, stages: [{stage_name, state, bytes_processed,
last_error, started_at, ended_at}]}`.

### 3.3 Cancel a job

```sh
curl -X DELETE ... /api/v1/admin/migrations/<JOB_ULID>
```

Soft-cancel: sets state='cancelled' if the current state allows
the transition. Already-terminal jobs (done/failed/cancelled)
return 409. Does NOT kill an in-flight `jabali migrate import`
process — operator's responsibility to Ctrl-C the cobra cmd if
needed.

## 4. Acknowledged limitations

- **Mailboxes** are imported via JMAP Email/import (a5d2dc25).
  Per-mailbox failures (Stalwart auth, missing INBOX) record in
  manifest_json and skip rather than failing the whole restore —
  operator can re-run mail import in isolation later by re-running
  the same `jabali migrate import` command (Stalwart dedupes on
  Message-ID; already-imported messages no-op).
- **PostgreSQL** databases skipped per ADR-0094 §5; warnings
  recorded in manifest. M37 importer integration imports them
  later.
- **Kratos identity** not created by the runner (CreateUser
  orchestrator gap). Operator pre-creates user; password reset
  on first login lazy-creates the identity.
- **IMAP-only** source is a v1 stub — runner returns a clear error
  directing the operator to run imapsync manually (see § 2.x).
  Native imapsync integration is a v1 follow-up.
- **Resume loses agent-side mid-rsync state.** If `home` stage
  dies mid-rsync, resume re-runs rsync from scratch (rsync's
  own `--partial` would help but isn't on the agent invocation
  yet — v1 follow-up).

## 5. Recovery scenarios

### 5.1 validate stage fails with target_user_exists
Operator pre-created the wrong user OR the source migration
already ran. Inspect:
```sql
SELECT id, username, email FROM users WHERE username = 'bob';
```
If duplicate from an earlier failed run: delete via
`jabali user delete --username bob` then re-run.

### 5.2 validate stage fails with domain_taken
Some other jabali user already owns one of the source's domains.
Free it (admin UI domains list → delete) or skip in the manifest
(future v2 — for now, manually `DELETE FROM migration_jobs ...`
and start over with a smaller manifest).

### 5.3 restore stage fails on databases
Most common cause: source dump references a DEFINER='cpuser_x'
that doesn't exist on the destination MariaDB. Inspect the dump
+ strip DEFINER lines via `sed -i 's|DEFINER=`[^`]*`@`[^`]*` ||g'`
on the .sql before resume.

### 5.4 restore stage fails on home
rsync error. `journalctl -u jabali-agent | grep migration.import_home`
shows the rsync exit code + truncated stderr. Common causes:
- Target homedir doesn't exist (CreateUser gap — pre-create user
  fully via admin UI, not just the panel row)
- Permissions on /var/lib/jabali-migrations/<id>/extracted/ (must
  be readable by jabali user; install.sh provisions 0750
  root:jabali so the agent service running as root can read)

### 5.5 Docroot perm damage after migration (nginx 403 on uploads)
If a restored docroot ends up group-owned by the user instead of
`www-data` (or its directories miss the setgid bit), nginx (www-data)
can't read files the PHP-FPM pool writes -> 403 on newly uploaded media.
Note: any account migrated in the 19->21 importer window can carry this
residual perm damage (only one account was found on the reference host, but
re-run the fix on any account from that window to be safe). Remediate:
```
jabali domain fix-perms <username>
```
(symlink-safe; equivalent to `chgrp -R www-data <docroot> && find <docroot>
-type d -exec chmod g+s {} +`, idempotent -- safe to re-run).

## 6. Where to look

| Concern | Path |
|---|---|
| Wire-format types | `panel-api/internal/migrate/discover.go` + `manifest.go` + `stage.go` |
| Per-source scaffold | `panel-api/internal/migrate/cpanel/` |
| Restore writers | `cpanel/restore_*.go` (ssh / cron / dbs / dns / home) |
| Tarball parser | `cpanel/tarball.go` |
| pkgacct + pull | `cpanel/pkgacct.go` |
| Stage runner | `panel-api/internal/migrate/runner.go` |
| Cobra CLI | `panel-api/cmd/server/migrate_run_cmd.go` |
| Agent home-import | `panel-agent/internal/commands/migration_import_home.go` |
| Admin REST | `panel-api/internal/api/admin_migrations.go` |
| Schema | migrations 000120-122 |
| ADR | `docs/adr/0094-migration-importers.md` |
| Blueprint | `plans/m35-migration-importers.md` |

## 7. Smoke test (DA + cPanel + WHM)

Validates the direct-rsync flow shipped 2026-05-14 (homedir excluded
from the cpmove tarball; restore stage rsyncs each domain's docroot
source→dest over SSH). Run on the jabali destination box.

### 7.1 Common prep

```sh
# Enable migrations (default off)
sudo mariadb -e "UPDATE jabali.server_settings SET migrations_enabled=1"

# Pre-create destination user (runner refuses if absent)
jabali user create --email bob@dest.test --username bob

# Stage SSH secret for the source box (root:jabali 0640)
sudo mkdir -p /etc/jabali-panel/migration-secrets
JOB=$(jabali ulid)
echo "SSH_PASSWORD=<source-root-pw>" \
  | sudo tee /etc/jabali-panel/migration-secrets/$JOB.env
sudo chown root:jabali /etc/jabali-panel/migration-secrets/$JOB.env
sudo chmod 0640 /etc/jabali-panel/migration-secrets/$JOB.env
echo "JOB=$JOB"
```

Key-based auth alternative: `SSH_PRIVATE_KEY_B64=<base64-PEM>` instead
of `SSH_PASSWORD`.

### 7.2 Run — one per source kind

```sh
# DirectAdmin
jabali migrate import --job-id $JOB \
  --source-kind directadmin --source-host da.source.test \
  --source-user dauser --target-user bob --wait

# cPanel
jabali migrate import --job-id $JOB \
  --source-kind cpanel --source-host cp.source.test \
  --source-user cpuser --target-user bob --wait

# WHM (pkgacct --skiphomedir)
jabali migrate import --job-id $JOB \
  --source-kind whm --source-host whm.source.test \
  --source-user whmuser --target-user bob --wait
```

### 7.3 Verify

```sh
SESS=<ory_kratos_session>
curl -k -H "Cookie: ory_kratos_session=$SESS" \
  https://localhost:8443/api/v1/admin/migrations/$JOB \
  | jq '.state, .stages, .manifest_json'

# Backup tarball must NOT carry the homedir bulk (the fix)
ls -la /var/lib/jabali/migrations/$JOB/

# Each domain docroot rsynced direct source->dest
ls /home/bob/domains/<domain>/public_html/

# DB created + app config rewritten with the new triple
sudo mariadb -e "SHOW DATABASES" | grep bob
grep DB_NAME /home/bob/domains/<domain>/public_html/wp-config.php
```

### 7.4 Pass criteria

- `migration_jobs.state = done`, zero `failed` stages.
- cpmove tarball excludes `homedir/` bulk — only the
  `domains-paths.txt` manifest + `.ssh/authorized_keys` inside.
- `manifest_json` warnings contain a line of the form
  `home: bytes=<N> domains=<N> (direct-rsync source→dest, no tar
  middleware)`.
- Each migrated domain's `public_html` is populated.
- App config files (wp-config.php / configuration.php /
  settings.php) point at the freshly-created jabali DB triple.
- SSH keys / cron / SSL / forwarders imported. Cron rows land
  `Enabled=false` — operator reviews then flips.

### 7.5 Source-kind specifics

| Kind | Homedir-skip mechanism | Docroot manifest |
|---|---|---|
| DirectAdmin | `BackupUser` script omits homedir rsync | `cpmove-<u>/domains-paths.txt` (absolute source paths) |
| cPanel | n/a (tarball, but restore reads userdata) | `cpmove-<u>/userdata/<dom>` YAML `documentroot:` |
| WHM | `/scripts/pkgacct --skiphomedir` | same userdata YAML path |
