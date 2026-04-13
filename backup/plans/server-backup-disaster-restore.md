# Plan: Full Server Backup & Disaster Restore

> Objective: Add `jabali-backup server-backup` and `jabali-backup server-restore`
> commands for full-server disaster recovery, plus schedule toggle in CLI and panel.

## Source Spec

`/home/shuki/projects/jabali/docs/backup-server-spec.md` — 20 categories covering
databases, configs, services, and all user accounts.

## Architecture

```
jabali-backup server-backup [--destination=NAME]
  → lib/collectors/server.sh    (new: collects all 20 server-level categories)
  → restic backup with tag type:server (separate from per-user snapshots)
  → generates server-manifest.json

jabali-backup server-restore --snapshot=ID [--target=DIR] [--skip-users]
  → 5-phase restore: base install → databases → configs → users → finalize
  → post-restore validation

jabali-backup schedule add --name=... --server-backup
  → schedule entry with server_backup=true flag
  → runs `jabali-backup server-backup` instead of `jabali-backup run`
```

Server snapshots are tagged `type:server` (vs `type:full` for per-user) so they
don't mix with user snapshots in listings.

---

## Step 1: Server collector (`lib/collectors/server.sh`)

**Branch:** `feat/server-backup-collector`
**Depends on:** nothing
**Files:** `lib/collectors/server.sh` (new)

### Context

The spec defines 20 categories. Categories 1-18 + 20 are server-level.
Category 19 (user accounts) is handled by the existing per-user backup.
The collector writes everything to a staging dir: `{staging}/server/`.

### Tasks

1. Create `lib/collectors/server.sh` with function `collect_server()`:
   - `{staging}/server/databases/jabali.sql.gz` — `mysqldump --single-transaction --quick --routines --triggers jabali` (skip ephemeral tables: sessions, cache, cache_locks, jobs, job_batches, failed_jobs, impersonation_tokens, personal_access_tokens, server_processes, password_reset_tokens)
   - `{staging}/server/databases/powerdns.sql.gz` — `mysqldump --single-transaction --quick powerdns`
   - `{staging}/server/databases/dnssec-state.txt` — loop `pdnsutil show-zone` for each zone
   - `{staging}/server/panel/env` — copy `/var/www/jabali/.env`
   - `{staging}/server/panel/storage/` — copy `/var/www/jabali/storage/app/` (uploaded branding/logos)
   - `{staging}/server/panel/composer.json` + `composer.lock` — for `composer install` on restore
   - `{staging}/server/panel/git-info.txt` — current commit hash + remote URL (for `git clone` restore)
   - Note: Panel app restored via `git clone` + `composer install` + `npm run build`, NOT file copy
   - `{staging}/server/config/jabali/` — copy entire `/etc/jabali/`
   - `{staging}/server/config/nginx/nginx.conf` — main nginx config
   - `{staging}/server/config/nginx/jabali/` — copy `/etc/nginx/jabali/`
   - `{staging}/server/config/nginx/sites-available/` — all vhost confs
   - `{staging}/server/config/nginx/sites-enabled.txt` — list of enabled symlinks
   - `{staging}/server/config/php/` — for each PHP version: php-fpm.conf, php.ini, www.conf, admin.conf
   - `{staging}/server/config/stalwart/` — copy `/etc/stalwart-mail/`
   - `{staging}/server/config/redis/redis.conf` — copy redis config
   - `{staging}/server/config/mysql/` — copy `/etc/mysql/mariadb.conf.d/`, `debian.cnf`
   - `{staging}/server/ssl/jabali/` — copy `/etc/ssl/jabali/`
   - `{staging}/server/letsencrypt/` — copy `/etc/letsencrypt/` (full, resolving symlinks in live/)
   - `{staging}/server/systemd/` — copy all `jabali-*`, `stalwart-mail`, `bulwark`, nginx override units
   - `{staging}/server/packages/` — `dpkg --get-selections`, `apt-mark showmanual`, version files
   - `{staging}/server/manifest.json` — server manifest per spec

2. Use `_mysql_root` for database dumps (same pattern as user MySQL collector)
3. All secret files (`.env`, `stalwart-api.conf`, `restic-password`, `debian.cnf`, `redis.conf`) are included as-is — restic encrypts the entire snapshot

### Verification

```bash
jabali-backup server-backup --dry-run
# Should list all 18 server categories with paths
```

### Exit Criteria

- `collect_server()` function exists and writes all 18 categories to staging
- Secret files included (restic encryption handles security)
- server-manifest.json generated with all fields per spec

---

## Step 2: Server backup CLI command

**Branch:** `feat/server-backup-cli`
**Depends on:** Step 1
**Files:** `bin/jabali-backup`

### Context

Add `cmd_server_backup()` to the main CLI. This command:
- Calls `collect_server()` to gather all server-level data
- Runs per-user backup for ALL accounts (reuses existing `cmd_run`)
- Tags snapshots with `type:server,date:YYYY-MM-DD`
- Supports `--dry-run`, `--destination=NAME`, `--skip-users` (server-only, no user backups)

### Tasks

1. Add `server-backup` to `usage()` help text:
   ```
   server-backup       Full server backup (disaster recovery)
   server-restore      Restore full server from backup
   ```
2. Add `cmd_server_backup()` function:
   - `cfg_load`, `cfg_validate`, `restic_env`
   - Create staging dir `/tmp/jabali-server-backup-$$`
   - Call `collect_server "$staging"`
   - `restic backup` the staging dir with tags `type:server`
   - Unless `--skip-users`, call `cmd_run` (backs up all users)
   - Apply retention, cleanup
3. Add `--skip-users` flag to skip per-user backups (for testing or when only server config changed)
4. Add `--type=server|user|all` filter to `cmd_list snapshots` so panel and CLI can filter server snapshots
5. Add route in main `case` statement: `server-backup) cmd_server_backup "$@" ;;`
6. Source `server.sh` collector in the startup section

### Verification

```bash
jabali-backup server-backup --dry-run
jabali-backup server-backup --skip-users   # server config only
jabali-backup server-backup                # full server + all users
jabali-backup list snapshots --type=server  # should show server snapshots
```

### Exit Criteria

- `jabali-backup server-backup` runs and creates a restic snapshot tagged `type:server`
- `jabali-backup server-backup --skip-users` backs up only server configs
- `jabali-backup server-backup --dry-run` previews all categories

---

## Step 3: Server restore CLI command

**Branch:** `feat/server-restore-cli`
**Depends on:** Step 2
**Files:** `bin/jabali-backup`, `lib/restorers/server.sh` (new)

### Context

`cmd_server_restore()` restores a full server from a `type:server` snapshot.
Follows the 5-phase restore order from the spec.

### Tasks

1. Create `lib/restorers/server.sh` with `restore_server()`:
   - **Phase 1 (Base):** Restore `.env`, `/etc/jabali/`, verify APP_KEY
   - **Phase 2 (Databases):** Import jabali.sql.gz and powerdns.sql.gz, run migrations
   - **Phase 3 (Configs):** Restore nginx, PHP-FPM, Stalwart, Redis, MariaDB configs, panel SSL, systemd units, Let's Encrypt state
   - **Phase 4 (Users):** Unless `--skip-users`, restore each user via existing `cmd_restore`
   - **Phase 5 (Finalize):** Restore Bulwark webmail (rebuild via install.sh preferred), restore agent addons (`/etc/jabali/agent.d/`), `systemctl daemon-reload`, restart all services, run post-restore checks (per spec: service status, nginx -t, panel HTTP, DB access, DNS dig, mail healthz, per-domain HTTP checks, PowerDNS status, Redis ACL validation)
2. Add `cmd_server_restore()` to CLI:
   - Resolves `--snapshot=ID` (or latest `type:server` snapshot)
   - Extracts to temp dir
   - Calls `restore_server()`
   - Supports `--target=DIR` (extract for inspection)
   - Supports `--skip-users` (server config only)
   - Supports `--force` (overwrite existing configs)
   - Supports `--dry-run` (preview phases)
3. Add post-restore checks: service status, nginx -t, panel HTTP, DB access, DNS, mail
4. Add route: `server-restore) cmd_server_restore "$@" ;;`

### Verification

```bash
jabali-backup server-restore --snapshot=latest --dry-run
jabali-backup server-restore --snapshot=latest --target=/tmp/inspect/
jabali-backup server-restore --snapshot=latest --skip-users --force
```

### Exit Criteria

- Server restore follows the 5-phase order from spec
- Each phase logs progress and handles errors (soft fail per component)
- Post-restore checks run and report results
- `--dry-run` shows the full plan without making changes

---

## Step 4: Schedule toggle for server backup

**Branch:** `feat/server-backup-schedule`
**Depends on:** Step 2
**Files:** `bin/jabali-backup` (schedule section)

### Context

The schedule system stores jobs in `/etc/jabali-backup/schedules.json`. Each job
has: id, name, cron, accounts, destination, enabled. Add a `server_backup` boolean
flag. When true, the cron job runs `jabali-backup server-backup` instead of
`jabali-backup run`.

### Tasks

1. Update `_cmd_sched_add()` to accept `--server-backup` flag:
   - Sets `"server_backup": true` in the job JSON
   - When server_backup is true, ignores `--accounts` (backs up everything)
2. Refactor `_sched_build_cron_cmd()` to accept the full job object (or add `server_backup` parameter):
   - Normal jobs: `jabali-backup run --destination=...`
   - Server backup jobs: `jabali-backup server-backup --destination=...`
3. Update `_sched_sync_crontab()` to pass `server_backup` flag to `_sched_build_cron_cmd()`
4. Update `_cmd_sched_update()` to accept `--server-backup` / `--no-server-backup` toggle
5. Update `_cmd_sched_list()` to show server backup jobs with `[Server]` indicator
6. Update `_cmd_sched_show()` to include `server_backup` field in JSON output
7. Update schedule help text with example:
   ```
   jabali-backup schedule add --name="Disaster Recovery" --cron="0 3 * * 0" --server-backup --destination=offsite
   ```

### Verification

```bash
jabali-backup schedule add --name="DR Weekly" --cron="0 3 * * 0" --server-backup
jabali-backup schedule list
# Should show: DR Weekly | 0 3 * * 0 | [Server] | enabled
jabali-backup schedule show --id=...
# Should show: "server_backup": true
```

### Exit Criteria

- `--server-backup` flag creates a server backup schedule
- Cron command is `jabali-backup server-backup` (not `jabali-backup run`)
- Schedule list clearly indicates server backup jobs

---

## Step 5: Panel schedule page — server backup toggle

**Branch:** `feat/panel-server-backup-toggle`
**Depends on:** Step 4
**Files:** `panel/filament/pages/Backups.php`, `panel/agent/jabali-backup.php`

### Context

The Backups admin page has a Schedule tab with a table of schedule jobs.
Add a "Server Backup (Disaster Recovery)" toggle when creating/editing a schedule.
When enabled, show a description explaining this is a full-server backup for disaster recovery.

### Tasks

1. Update `jbScheduleAdd()` agent handler to pass `--server-backup` flag when `server_backup` param is true
2. Update `jbScheduleList()` to include `server_backup` field in returned data
3. Update the Filament schedule add/edit action form:
   - Add `Toggle::make('server_backup')->label(__('Server Backup (Disaster Recovery)'))->helperText(__('Backs up all server configs, databases, and user accounts for full disaster recovery.'))->default(false)`
   - When toggle is on, hide the accounts selector (server backup covers everything)
4. Update the schedule table to show a badge for server backup jobs
5. Use Context7 to look up correct Filament components before coding

### Verification

- Create a server backup schedule via panel → schedule table shows `[Server]` badge
- Edit the schedule → toggle is on, accounts field hidden
- The underlying crontab entry uses `server-backup` command

### Exit Criteria

- Panel schedule page has server backup toggle
- Toggle hides accounts selector when enabled
- Agent routes correctly pass the flag to CLI

---

## Step 6: Panel server restore page/action

**Branch:** `feat/panel-server-restore`
**Depends on:** Step 3, Step 5
**Files:** `panel/filament/pages/Backups.php`, `panel/agent/jabali-backup.php`

### Context

Add ability to trigger server restore from the admin panel. This should be in
the Restore/Download tab or a dedicated "Disaster Recovery" section. The action
shows server snapshots (tagged `type:server`) and lets the admin restore with options.

### Tasks

1. Add `jb.server_snapshots` agent route — lists snapshots with `type:server` tag
2. Add `jb.server_restore` agent route — runs `jabali-backup server-restore`
3. Add a "Server Backups" section in the Restore/Download tab or Settings tab:
   - List server snapshots (date, hostname, size)
   - "Restore" action with wizard: select snapshot → choose phases → confirm
   - `--skip-users` toggle
   - `--force` toggle
4. Use Context7 for all Filament component lookups

### Verification

- Server snapshots appear in the panel
- Restore wizard shows phases from spec
- Restore runs in background with log streaming

### Exit Criteria

- Admin can view server snapshots in panel
- Admin can trigger server restore with phase selection
- Restore progress visible in logs tab

---

## Step 7: Documentation and update README

**Branch:** `feat/server-backup-docs`
**Depends on:** Steps 1-6
**Files:** `docs/README.md`, `docs/DISASTER-RECOVERY.md` (new)

### Tasks

1. Create `docs/DISASTER-RECOVERY.md` with:
   - What a server backup includes (all 20 categories)
   - How to create a server backup (CLI + panel)
   - How to schedule weekly disaster recovery backups
   - Full server restore procedure (step-by-step)
   - Post-restore checklist
2. Update `docs/README.md`:
   - Add `server-backup` and `server-restore` to commands section
   - Add link to DISASTER-RECOVERY.md
3. Update collector reference to include `server` collector

### Exit Criteria

- Complete disaster recovery guide exists
- README reflects new commands

---

## Dependency Graph

```
Step 1 (collector) ──→ Step 2 (CLI backup) ──→ Step 3 (CLI restore)
                                            ↘
                                             Step 4 (schedule) ──→ Step 5 (panel schedule)
                                                                          ↓
                                                               Step 6 (panel restore)
                                                                          ↓
                                                               Step 7 (docs)
```

**Parallel:** Steps 3 and 4 can run in parallel after Step 2.
**Sequential:** Step 5 needs Step 4. Step 6 needs Steps 3 + 5.

## Invariants (verified after every step)

- All existing per-user backup tests still pass
- `jabali-backup run` (per-user) is unaffected
- `jabali-backup doctor` still passes
- No secrets are logged or printed to stdout
