# Jabali CLI reference

> Generated from the Cobra command tree — do not edit by hand.
> Regenerate with `go test ./panel-api/cmd/server -run TestCLIReferenceGolden -update`.

## `jabali`

Jabali Panel — web hosting control panel

```
jabali
```

**Flags:**

- `--config` — config file path (default: /etc/jabali/config.toml)
- `--json` — output as JSON

### `jabali active-tasks`

Show running/queued tasks (updates, backups, malware scans)

```
jabali active-tasks
```

### `jabali admin`

Operator-only administrative subcommands

```
jabali admin
```

#### `jabali admin backfill-usernames`

Assign a unique username to every user missing one (M54 Wave A)

```
jabali admin backfill-usernames [flags]
```

**Flags:**

- `--apply` — Write the derived usernames to the DB

#### `jabali admin purge-orphan-identities`

Delete Kratos identities with no matching panel user (frees stuck usernames)

```
jabali admin purge-orphan-identities [flags]
```

**Flags:**

- `--apply` — delete the orphan identities (default: dry-run)
- `--username` — only purge the orphan whose username or email matches this value

#### `jabali admin rebuild-kratos`

Recreate Kratos identities from panel users (DB-loss recovery, ADR-0034)

```
jabali admin rebuild-kratos [flags]
```

**Flags:**

- `--dry-run` — List target users and exit without writing
- `--expires-in` — Kratos recovery-code TTL (e.g. 1h, 24h) — operators typically need ≥24h to distribute (default `24h`)
- `--output` — CSV file to emit temp passwords (default: /var/lib/jabali-panel/recovery/, root-only). Contains PLAINTEXT temporary passwords — delete after distribution.
- `--yes` — Skip interactive confirmation prompt

#### `jabali admin relabel-identifiers`

Re-key Kratos login identifiers email -> username (M54 Wave C)

```
jabali admin relabel-identifiers [flags]
```

**Flags:**

- `--apply` — PATCH the identities (default: dry-run)

#### `jabali admin slice-cutover`

Migrate FPM from global master to per-user masters and mask distro units

```
jabali admin slice-cutover [flags]
```

**Flags:**

- `--dry-run` — run preflight only; do not mask global FPM or probe

### `jabali aide`

AIDE file integrity monitor (M42) operator commands

```
jabali aide
```

#### `jabali aide check`

Trigger an on-demand AIDE integrity check

```
jabali aide check
```

#### `jabali aide rebuild`

Re-baseline the AIDE database after a deliberate change

```
jabali aide rebuild [flags]
```

**Flags:**

- `--dry-run` — Preview plan without rebuilding (mutually exclusive with --full)
- `--full` — Confirm full DB re-init
- `--paths` — Partial re-baseline: only promote changes matching this regex (e.g. '^/usr/local/bin/jabali-'); refuses if changes outside the regex are detected

#### `jabali aide status`

Print AIDE DB age + last check summary

```
jabali aide status
```

### `jabali app`

Manage one-click app installs (direct DB — M20-safe)

```
jabali app
```

#### `jabali app cache`

Enable/disable object + page cache for a WordPress app install

```
jabali app cache <install-id> [flags]
```

**Flags:**

- `--disable` — disable object + page cache
- `--enable` — enable object + page cache

#### `jabali app cache-doctor`

Detect (and optionally --repair) drift on cache-enabled WordPress installs

```
jabali app cache-doctor [flags]
```

**Flags:**

- `--json` — emit a JSON report
- `--migrate-acl` — JAB-62: re-provision every cache-enabled install to a per-install Redis ACL user, then reap orphaned legacy shared users (safe/idempotent)
- `--repair` — re-provision drifted installs (re-stamp constants, re-provision ACL, re-verify)

#### `jabali app cache-doctor-run`

Run a recorded fleet cache-doctor sweep (persists a run for the admin GUI history)

```
jabali app cache-doctor-run [flags]
```

**Flags:**

- `--kind` — sweep kind: doctor | repair | refresh (default `doctor`)

#### `jabali app clone`

Clone a WordPress app install onto another domain (files + DB)

```
jabali app clone <src-install-id> [flags]
```

**Flags:**

- `--to-domain` — destination domain (ID or name)

#### `jabali app delete`

Delete an installed app (direct DB + agent teardown — M20-safe)

```
jabali app delete <install-id|domain-name> [flags]
```

**Flags:**

- `--force` — Skip confirmation prompt

#### `jabali app e2e`

Install every app on a domain, report pass/fail, then delete

```
jabali app e2e [flags]
```

**Flags:**

- `--base-subdir` — Subdir prefix; each app installs under <prefix>_<app>_<rand> (default `e2e`)
- `--domain` — Domain name or ULID to install all apps under (required)
- `--keep` — Don't delete installs after the run (debug)
- `--only` — Only run these app_types (comma-separated) (default `[]`)
- `--skip` — Skip these app_types (comma-separated) (default `[]`)
- `--stop-on-fail` — Stop the sweep after the first failure
- `--wait-timeout` — Per-app install timeout in seconds (default `600`)

#### `jabali app get`

Show one installed app (direct DB — M20-safe)

```
jabali app get <install-id|domain-name>
```

#### `jabali app install`

Install an app on a domain (direct service — M20-safe)

```
jabali app install [flags]
```

**Flags:**

- `--app-type` — App descriptor name (see `jabali app registry`)
- `--directory` — Subdirectory under docroot (empty = site root)
- `--domain` — Target domain name or ULID (e.g. example.com or 01KPR…)
- `--param` — Per-app param: --param key=value (value is JSON; repeat for multiple) (default `[]`)
- `--use-www` — Reachable at www.<domain> too
- `--user` — Owner user (email, username, or ULID; default: domain owner)
- `--wait` — Poll until status is ready or failed
- `--wait-timeout` — Seconds to wait when --wait is set (default `600`)

#### `jabali app list`

List installed apps (direct DB — M20-safe)

```
jabali app list
```

#### `jabali app magic-link`

Mint a one-time magic-link login for an installed app (sensitive, ~60s)

```
jabali app magic-link <install-id>
```

#### `jabali app refresh-cache-plugin`

Update the jabali-cache plugin to the latest WordPress.org release on every cache-enabled site

```
jabali app refresh-cache-plugin
```

#### `jabali app registry`

List available app types and their parameter schemas (direct read — no DB)

```
jabali app registry
```

#### `jabali app scan`

Scan a user's homedir for unregistered apps and register them

```
jabali app scan [flags]
```

**Flags:**

- `--admin-email` — admin email for discovered installs (default: user's email)
- `--user` — user (id or username) whose homedir to scan (required)

### `jabali apparmor`

AppArmor profile management (M40) operator commands

```
jabali apparmor
```

#### `jabali apparmor flip-mature`

Flip soak-clean complain-mode profiles to enforce

```
jabali apparmor flip-mature [flags]
```

**Flags:**

- `--dry-run` — show what would change without invoking aa-enforce
- `--force` — flip even if recent denials exist (DANGEROUS — this is how #705 crash-looped mx)
- `--profile` — Flip a single profile only
- `--soak-days` — denial-scan window in days; a profile with any AppArmor DENIED in this window is not flipped (default `7`)

#### `jabali apparmor status`

List jabali AppArmor profiles and modes

```
jabali apparmor status
```

### `jabali appsec`

CrowdSec AppSec config operator subcommands

```
jabali appsec
```

#### `jabali appsec exclusion`

Manage operator CRS false-positive exclusions

```
jabali appsec exclusion
```

##### `jabali appsec exclusion add`

Add an exclusion (host + path + rule are all required)

```
jabali appsec exclusion add [flags]
```

**Flags:**

- `--host` — hostname the exclusion applies to (required)
- `--note` — why this exclusion exists
- `--rule` — CRS rule ID to exclude, e.g. 942100 (required)
- `--uri-prefix` — URI prefix the exclusion applies to, e.g. /wp-json/x/ (required)

##### `jabali appsec exclusion list`

List operator CRS exclusions

```
jabali appsec exclusion list
```

##### `jabali appsec exclusion rm`

Remove an exclusion by id

```
jabali appsec exclusion rm <id>
```

#### `jabali appsec explain`

Show which CRS rules recently blocked requests (AppSec FP triage)

```
jabali appsec explain [flags]
```

**Flags:**

- `--json` — emit raw JSON instead of the grouped report
- `--limit` — how many recent AppSec alerts to inspect (max 200) (default `25`)

#### `jabali appsec render-config`

Write /etc/crowdsec/appsec-configs/jabali-appsec.yaml from internal/appseccfg.Render

```
jabali appsec render-config [flags]
```

**Flags:**

- `--reconcile` — preserve operator jabali-mode/jabali-countries header from existing file (default true) (default `true`)

### `jabali audit`

Query, verify, and prune the unified audit log (M49 / ADR-0106)

```
jabali audit
```

#### `jabali audit prune`

Delete audit rows older than --days (retention; ADR-0106)

```
jabali audit prune [flags]
```

**Flags:**

- `--days` — retention window in days (delete older) (default `365`)

#### `jabali audit query`

List recent audit events (admin/forensics view)

```
jabali audit query [flags]
```

**Flags:**

- `--limit` — max rows (1-1000) (default `50`)
- `--q` — search (action/target/actor_kind/result)

#### `jabali audit verify`

Recompute the hash chain and report tamper-evidence integrity

```
jabali audit verify
```

### `jabali automation-token`

Manage Automation API tokens (headless provisioning)

```
jabali automation-token
```

#### `jabali automation-token list`

List automation tokens (never reveals secrets)

```
jabali automation-token list
```

#### `jabali automation-token mint`

Mint an automation token; reveals the secret once

```
jabali automation-token mint <name> [flags]
```

**Flags:**

- `--scope` — grant a scope (repeatable), e.g. --scope read:status (default `[]`)

#### `jabali automation-token revoke`

Revoke an automation token

```
jabali automation-token revoke <id-or-name>
```

### `jabali backup`

Backup & restore subcommands (M30 — restic-backed; ADR-0075 / 0080)

```
jabali backup
```

#### `jabali backup account-restore`

Restore one account's backup snapshot via the agent (bypass UI)

```
jabali backup account-restore [flags]
```

**Flags:**

- `--apply` — apply home+db onto live system (false = staging-only smoke test) (default `true`)
- `--destination` — destination name (e.g. 'test', 'b2-prod')
- `--force` — required — restore overwrites home tree + reloads databases
- `--interactive` — force interactive prompts even when flags are set
- `--snapshot` — manifest snapshot id (long form preferred)
- `--target-user-id` — disaster recovery: panel row gone; use this ULID directly (pair with --target-username)
- `--target-username` — disaster recovery: system account name to chown into (pair with --target-user-id)
- `--user` — username (e.g. shukivaknin) — looks up panel row
- `--user-id` — user ULID — looks up panel row (alternative to --user)

#### `jabali backup copy`

[RETIRED] superseded by per-destination model (ADR-0080)

```
jabali backup copy
```

#### `jabali backup destination`

Manage backup destinations (local, sftp, s3, b2, azure, gcs, rest)

```
jabali backup destination
```

##### `jabali backup destination create`

Create a backup destination

```
jabali backup destination create [flags]
```

**Flags:**

- `--disabled` — create in disabled state
- `--env` — credential env: KEY=VALUE (repeatable) (default `[]`)
- `--env-stdin` — read additional KEY=VALUE lines from stdin
- `--kind` — destination kind: local|sftp|s3|b2|azure|gcs|rest (required)
- `--name` — destination name (required, unique)
- `--url` — restic repo URL (required)

##### `jabali backup destination delete`

Delete a backup destination

```
jabali backup destination delete <id-or-name> [flags]
```

**Flags:**

- `--force` — skip confirmation

##### `jabali backup destination get`

Show a backup destination

```
jabali backup destination get <id-or-name>
```

##### `jabali backup destination list`

List backup destinations

```
jabali backup destination list
```

##### `jabali backup destination rotate-password`

Rotate a backup destination's restic password (revealed once)

```
jabali backup destination rotate-password <id-or-name>
```

##### `jabali backup destination test`

Test connectivity (auto-inits restic repo if missing)

```
jabali backup destination test <id-or-name>
```

##### `jabali backup destination update`

Update a backup destination

```
jabali backup destination update <id-or-name> [flags]
```

**Flags:**

- `--clear-creds` — delete stored credential env for this destination
- `--disable` — mark destination disabled
- `--enable` — mark destination enabled
- `--env` — rewrite credential env: KEY=VALUE (repeatable; s3/b2/azure/gcs/rest/sftp secrets) (default `[]`)
- `--env-stdin` — read additional KEY=VALUE credential lines from stdin
- `--name` — new name
- `--sftp-auth` — sftp auth: 'key' or 'password'
- `--sftp-host` — sftp host (sftp kind)
- `--sftp-key-path` — absolute path to private key (auth=key)
- `--sftp-password` — sftp password (auth=password; stored as SSHPASS)
- `--sftp-path` — sftp remote path
- `--sftp-port` — sftp port (default 22) (default `0`)
- `--sftp-user` — sftp user
- `--url` — new restic repo URL (validated against existing kind)

#### `jabali backup retention`

Manage restic retention (forget + prune per destination)

```
jabali backup retention
```

##### `jabali backup retention apply`

Run restic forget per (schedule, destination) + prune per destination

```
jabali backup retention apply [flags]
```

**Flags:**

- `--dry-run` — Pass restic --dry-run to forget+prune (lists what would be removed; no destructive ops)

#### `jabali backup schedule`

Manage backup schedules

```
jabali backup schedule
```

##### `jabali backup schedule create`

Create a backup schedule

```
jabali backup schedule create [flags]
```

**Flags:**

- `--cron` — 5-field cron expression (e.g. '0 3 * * *')
- `--destination` — destination id or name (repeatable) (default `[]`)
- `--disabled` — create in disabled state
- `--include-system` — for account_backup: also fire system_backup each tick
- `--keep-daily` — restic forget --keep-daily (default `0`)
- `--keep-monthly` — restic forget --keep-monthly (default `0`)
- `--keep-weekly` — restic forget --keep-weekly (default `0`)
- `--kind` — schedule kind: account_backup|system_backup (required)
- `--preset` — preset: daily|weekly|monthly (mutually exclusive with --cron)
- `--user` — user id|email|username for account_backup fan-out (repeatable; empty=all non-admins) (default `[]`)

##### `jabali backup schedule delete`

Delete a backup schedule

```
jabali backup schedule delete <id> [flags]
```

**Flags:**

- `--force` — skip confirmation

##### `jabali backup schedule get`

Show a backup schedule with destinations + users

```
jabali backup schedule get <id>
```

##### `jabali backup schedule list`

List backup schedules

```
jabali backup schedule list
```

##### `jabali backup schedule run-now`

Trigger a schedule by advancing next_run_at to now (scheduler picks up within ≤60s)

```
jabali backup schedule run-now <id>
```

##### `jabali backup schedule update`

Update a backup schedule

```
jabali backup schedule update <id> [flags]
```

**Flags:**

- `--clear-destinations` — remove all destinations
- `--clear-users` — remove all users (= fan-out to all)
- `--cron` — new cron expression
- `--destination` — replace destinations (repeatable) (default `[]`)
- `--disable` — mark schedule disabled
- `--enable` — mark schedule enabled
- `--include-system` — true|false (account_backup only)
- `--keep-daily` —  (default `0`)
- `--keep-monthly` —  (default `0`)
- `--keep-weekly` —  (default `0`)
- `--preset` — preset: daily|weekly|monthly
- `--user` — replace users (repeatable) (default `[]`)

#### `jabali backup scheduler`

Backup scheduler ops (manual tick / debug)

```
jabali backup scheduler
```

##### `jabali backup scheduler tick`

Run one enqueue + dispatch pass of the backup scheduler synchronously

```
jabali backup scheduler tick
```

### `jabali cron`

Manage user cron jobs (systemd-user timers)

```
jabali cron
```

#### `jabali cron add`

Add a cron job (5-field cron, allowlisted commands only)

```
jabali cron add [flags]
```

**Flags:**

- `--command` — command to run (required, allowlisted)
- `--disabled` — create disabled
- `--name` — job name (required)
- `--schedule` — 5-field cron expression e.g. '*/15 * * * *' (required)
- `--user` — user (id|email|username) (required)

#### `jabali cron delete`

Delete a cron job (reconciler removes the timer on next tick)

```
jabali cron delete <job-id> [flags]
```

**Flags:**

- `--force` — skip confirmation

#### `jabali cron http-trigger`

Fetch a self-domain URL with SSRF guard + rebind-safe IP pinning (internal cron exec helper)

```
jabali cron http-trigger <url>
```

#### `jabali cron list`

List cron jobs (filtered by user, or all)

```
jabali cron list [flags]
```

**Flags:**

- `--user` — filter by user (id|email|username); empty = all

#### `jabali cron run-now`

Run a cron job immediately via the agent (synchronous)

```
jabali cron run-now <job-id>
```

#### `jabali cron update`

Update a cron job

```
jabali cron update <job-id> [flags]
```

**Flags:**

- `--command` — 
- `--disable` — mark job disabled
- `--enable` — mark job enabled
- `--name` — 
- `--schedule` — 

### `jabali crowdsec`

CrowdSec + AppSec operations (decisions, allowlists, hub, alerts, geoblock, captcha)

```
jabali crowdsec
```

#### `jabali crowdsec alerts`

List / inspect alerts

```
jabali crowdsec alerts
```

##### `jabali crowdsec alerts inspect`

Inspect one alert

```
jabali crowdsec alerts inspect <id>
```

##### `jabali crowdsec alerts list`

List recent alerts

```
jabali crowdsec alerts list
```

#### `jabali crowdsec allowlists`

List / add / remove allowlist entries

```
jabali crowdsec allowlists
```

##### `jabali crowdsec allowlists add`

Add an allowlist entry (use --me to allowlist your current SSH IP)

```
jabali crowdsec allowlists add [flags]
```

**Flags:**

- `--me` — allowlist the IP you are SSH'd in from (roaming-admin recovery)
- `--reason` — reason label (default `manual (cli)`)
- `--value` — IP/CIDR to allowlist (required unless --me)

##### `jabali crowdsec allowlists list`

List allowlist entries

```
jabali crowdsec allowlists list
```

##### `jabali crowdsec allowlists remove`

Remove an allowlist entry

```
jabali crowdsec allowlists remove [flags]
```

**Flags:**

- `--value` — allowlist entry to remove (required)

#### `jabali crowdsec blocklists`

List / refresh blocklists

```
jabali crowdsec blocklists
```

##### `jabali crowdsec blocklists list`

List subscribed blocklists

```
jabali crowdsec blocklists list
```

##### `jabali crowdsec blocklists refresh`

Refresh blocklists now

```
jabali crowdsec blocklists refresh
```

#### `jabali crowdsec bouncers`

List CrowdSec bouncers

```
jabali crowdsec bouncers
```

#### `jabali crowdsec captcha`

Captcha remediation get / set

```
jabali crowdsec captcha
```

##### `jabali crowdsec captcha get`

Show captcha config (secret never shown)

```
jabali crowdsec captcha get
```

##### `jabali crowdsec captcha set`

Configure captcha remediation (merge: empty --secret-key keeps existing)

```
jabali crowdsec captcha set [flags]
```

**Flags:**

- `--enabled` — enable captcha remediation
- `--provider` — recaptcha|hcaptcha|turnstile
- `--secret-key` — captcha secret key (write-only; empty keeps existing)
- `--site-key` — captcha site key

#### `jabali crowdsec decisions`

List / add / delete CrowdSec decisions

```
jabali crowdsec decisions
```

##### `jabali crowdsec decisions add`

Add a decision (ban)

```
jabali crowdsec decisions add [flags]
```

**Flags:**

- `--duration` — ban duration (e.g. 4h, 24h) (default `4h`)
- `--reason` — reason label (default `manual (cli)`)
- `--scope` — ip|range|country|as (default `ip`)
- `--value` — target value (required)

##### `jabali crowdsec decisions delete`

Delete a decision by id, or every decision for --ip

```
jabali crowdsec decisions delete [<id>] [flags]
```

**Flags:**

- `--ip` — delete all decisions targeting this IP/CIDR

##### `jabali crowdsec decisions list`

List active decisions

```
jabali crowdsec decisions list [flags]
```

**Flags:**

- `--limit` — max rows (1..1000) (default `0`)
- `--scope` — filter: ip|range|country|as

#### `jabali crowdsec geoblock`

AppSec geoblock get / set

```
jabali crowdsec geoblock
```

##### `jabali crowdsec geoblock get`

Show geoblock mode + countries

```
jabali crowdsec geoblock get
```

##### `jabali crowdsec geoblock set`

Set geoblock mode (off|allow|deny) and countries

```
jabali crowdsec geoblock set [flags]
```

**Flags:**

- `--country` — 2-letter ISO code (repeatable) (default `[]`)
- `--mode` — off|allow|deny (required)

#### `jabali crowdsec hub`

List / install / remove hub items

```
jabali crowdsec hub
```

##### `jabali crowdsec hub install`

Install a hub item

```
jabali crowdsec hub install [flags]
```

**Flags:**

- `--force` — force reinstall
- `--name` — hub item name (required)
- `--type` — collections|parsers|scenarios|appsec-rules (required)

##### `jabali crowdsec hub list`

List hub items (collections/parsers/scenarios/appsec-rules)

```
jabali crowdsec hub list [flags]
```

**Flags:**

- `--type` — filter by type

##### `jabali crowdsec hub remove`

Remove a hub item

```
jabali crowdsec hub remove [flags]
```

**Flags:**

- `--name` — item name (required)
- `--type` — item type (required)

#### `jabali crowdsec metrics`

Show CrowdSec metrics

```
jabali crowdsec metrics
```

#### `jabali crowdsec profiles`

Per-scenario remediation overrides get / set

```
jabali crowdsec profiles
```

##### `jabali crowdsec profiles get`

Show current profile overrides

```
jabali crowdsec profiles get
```

##### `jabali crowdsec profiles set`

Set per-scenario overrides as scenario:action (action=captcha|off)

```
jabali crowdsec profiles set [flags]
```

**Flags:**

- `--override` — scenario:action (repeatable; action=captcha|off) (default `[]`)

#### `jabali crowdsec status`

Show CrowdSec engine status

```
jabali crowdsec status
```

### `jabali db`

Manage user databases (mariadb / postgres)

```
jabali db
```

#### `jabali db backup`

Create a backup of a database (returns the dump path)

```
jabali db backup <db-id|db-name>
```

#### `jabali db config`

View / set database tuning (mariadb/postgres)

```
jabali db config
```

##### `jabali db config get`

Show tuning params + current values (allowlist)

```
jabali db config get [flags]
```

**Flags:**

- `--engine` — mariadb|postgres (default `mariadb`)

##### `jabali db config set`

Set tuning params (allowlist-validated; applies + may restart the engine)

```
jabali db config set <param=value> [<param=value>...] [flags]
```

**Flags:**

- `--engine` — mariadb|postgres (default `mariadb`)

#### `jabali db create`

Create a database for a user

```
jabali db create [flags]
```

**Flags:**

- `--as-admin` — Skip the username prefix (admin-only DB names)
- `--engine` — Engine: mariadb | postgres (default `mariadb`)
- `--name` — Database name (without user prefix) — required
- `--user` — User (email or username) — required

#### `jabali db delete`

Delete a database by ID

```
jabali db delete [flags]
```

**Flags:**

- `--id` — Database ID (ULID)

#### `jabali db kill`

Kill/terminate a database process

```
jabali db kill <process-id> [flags]
```

**Flags:**

- `--engine` — mariadb|postgres (default `mariadb`)
- `--force` — confirm the kill

#### `jabali db list`

List databases (filtered by user, or all)

```
jabali db list [flags]
```

**Flags:**

- `--user` — Filter by user (email or username)

#### `jabali db maintenance`

Run optimize/analyze (mariadb) or vacuum/analyze (postgres)

```
jabali db maintenance [flags]
```

**Flags:**

- `--engine` — mariadb|postgres (default `mariadb`)
- `--scope` — 'all' or a database name (default `all`)

#### `jabali db maintenance-status`

Show a maintenance job's status

```
jabali db maintenance-status <job-id>
```

#### `jabali db postgres`

PostgreSQL lifecycle: status / uninstall

```
jabali db postgres
```

##### `jabali db postgres status`

Show PostgreSQL installed/active/version

```
jabali db postgres status
```

##### `jabali db postgres uninstall`

DESTROY PostgreSQL: purge packages + delete all data dirs

```
jabali db postgres uninstall [flags]
```

**Flags:**

- `--force` — confirm permanent destruction of all PostgreSQL data

#### `jabali db processes`

List database processes/activity

```
jabali db processes [flags]
```

**Flags:**

- `--engine` — mariadb|postgres (default `mariadb`)

#### `jabali db restore`

Restore a database from a .sql dump on the host

```
jabali db restore <db-id|db-name> [flags]
```

**Flags:**

- `--file` — path to a .sql dump on the host (required)
- `--force` — confirm the overwrite

#### `jabali db root-password`

Rotate the database root/superuser password (revealed once)

```
jabali db root-password [flags]
```

**Flags:**

- `--engine` — mariadb|postgres (default `mariadb`)

#### `jabali db sso`

Mint a single-use phpMyAdmin/Adminer SSO login URL for a database

```
jabali db sso --database <id> [--engine mariadb|postgres] [flags]
```

**Flags:**

- `--database` — database id (ULID)
- `--engine` — optional engine guard: mariadb|postgres (defaults to the database's engine)

#### `jabali db user`

Manage database users (mariadb / postgres)

```
jabali db user
```

##### `jabali db user create`

Create a database user (auto-generates password if --password omitted)

```
jabali db user create [flags]
```

**Flags:**

- `--as-admin` — Skip the panel-username prefix (admin-only DB user names)
- `--engine` — Engine: mariadb | postgres (default `mariadb`)
- `--name` — DB user name (without panel-username prefix) — required
- `--password` — Password (auto-generated ULID if omitted; revealed once)
- `--user` — Panel user (email or username) — required

##### `jabali db user delete`

Delete a database user by ID

```
jabali db user delete [flags]
```

**Flags:**

- `--id` — DB user ID (ULID)

##### `jabali db user grant`

Grant a db user privileges on a database

```
jabali db user grant [flags]
```

**Flags:**

- `--db-name` — Database name (with panel-prefix) — required
- `--db-user-id` — DB user ID (ULID) — required
- `--level` — Shortcut: 'rw' or 'ro' (alternative to --privileges)
- `--privileges` — MariaDB privilege list (e.g. SELECT,INSERT,UPDATE) (default `[]`)

###### `jabali db user grant revoke`

Revoke a single grant, keeping the database user

```
jabali db user grant revoke <grant-id>
```

###### `jabali db user grant update`

Change a grant's level (rw|ro)

```
jabali db user grant update <grant-id> [flags]
```

**Flags:**

- `--level` — new grant level: 'rw' or 'ro' (required)

##### `jabali db user list`

List database users (filtered by panel user, or all)

```
jabali db user list [flags]
```

**Flags:**

- `--user` — Filter by panel user (email or username)

##### `jabali db user rotate-password`

Rotate a database user's password (revealed once)

```
jabali db user rotate-password <db-user-id>
```

### `jabali disk-usage`

Inspect / refresh a tenant's disk-usage breakdown (files / email / databases)

```
jabali disk-usage
```

#### `jabali disk-usage refresh`

Recompute a tenant's disk usage live and store the snapshot

```
jabali disk-usage refresh <user-email|username|id>
```

#### `jabali disk-usage show`

Show a tenant's last stored disk-usage snapshot

```
jabali disk-usage show <user-email|username|id>
```

### `jabali dns`

DNS zones and records (dns_zones / dns_records): list / add / update / delete

```
jabali dns
```

#### `jabali dns prune-service-records`

Remove imported cPanel service subdomains (cpanel/webmail/whm/…) from DNS

```
jabali dns prune-service-records [flags]
```

**Flags:**

- `--apply` — actually delete (default is a dry run)
- `--zone` — limit to one zone (default: every zone)

#### `jabali dns record`

DNS records: list / add / update / delete

```
jabali dns record
```

##### `jabali dns record add`

Add a record to a zone (same validation + conflict rules as the UI)

```
jabali dns record add <domain> --name <name> --type <TYPE> --content <content> [flags]
```

**Flags:**

- `--content` — record content (e.g. an IP, hostname, or TXT value)
- `--disabled` — create the record disabled (kept in DB, not served)
- `--name` — record name ('@' for apex)
- `--priority` — priority (MX/SRV) (default `0`)
- `--ttl` — TTL seconds (60-604800; 0 uses default 300) (default `0`)
- `--type` — record type: A, AAAA, CNAME, MX, TXT, NS, SRV, CAA

##### `jabali dns record delete`

Delete a record from a zone

```
jabali dns record delete <domain> <record-id> [flags]
```

**Flags:**

- `--force` — confirm deletion

##### `jabali dns record list`

List records in a zone

```
jabali dns record list <domain>
```

##### `jabali dns record update`

Update a record's content / ttl / priority / enabled flag

```
jabali dns record update <domain> <record-id> [flags]
```

**Flags:**

- `--content` — new content
- `--enabled` — set enabled flag (default `true`)
- `--priority` — new priority (MX/SRV) (default `0`)
- `--ttl` — new TTL seconds (60-604800) (default `0`)

#### `jabali dns zone`

DNS zones: list / show

```
jabali dns zone
```

##### `jabali dns zone list`

List all DNS zones

```
jabali dns zone list
```

##### `jabali dns zone show`

Show a zone and its records

```
jabali dns zone show <domain>
```

### `jabali docker`

Manage the docker engine + app-marketplace host (M48/M49)

```
jabali docker
```

#### `jabali docker disable`

Disable the marketplace toggle (keeps docker installed)

```
jabali docker disable
```

#### `jabali docker enable`

Install docker engine + flip Server Settings toggle

```
jabali docker enable
```

#### `jabali docker enable-tenant`

Enable userns-remap + tenant docker apps on this host (M49, GH #170)

```
jabali docker enable-tenant [flags]
```

**Flags:**

- `--yes` — proceed without the interactive confirmation

#### `jabali docker status`

Show docker engine status (active, marketplace toggle state)

```
jabali docker status
```

### `jabali docker-app`

Manage M48 docker-app catalog installs (admin-only)

```
jabali docker-app
```

#### `jabali docker-app backup-create`

Create a manual backup of the app

```
jabali docker-app backup-create <id>
```

#### `jabali docker-app backups`

List restic backups taken for this install

```
jabali docker-app backups <id>
```

#### `jabali docker-app catalog`

List entries in the installed catalog

```
jabali docker-app catalog
```

#### `jabali docker-app delete`

Uninstall a docker app (stops the stack, removes its row)

```
jabali docker-app delete <id> [flags]
```

**Flags:**

- `--keep-volumes` — keep /var/lib/jabali/docker-apps/<slug> data on disk

#### `jabali docker-app edit`

Edit a Docker app's domain and/or ports (container must be stopped)

```
jabali docker-app edit <id> [flags]
```

**Flags:**

- `--clear-domain` — detach the managed domain
- `--domain` — attach this domain (empty string detaches)
- `--port` — port override (repeatable): name=..,host=..,bind=..,proxy=..,enabled=.. (default `[]`)

#### `jabali docker-app env`

View, edit, or regenerate a Docker app's environment

```
jabali docker-app env
```

##### `jabali docker-app env get`

List the app's environment (reveals secrets)

```
jabali docker-app env get <id>
```

##### `jabali docker-app env regenerate`

Mint a fresh value for a generated secret, then recreate the container

```
jabali docker-app env regenerate <id> <KEY>
```

##### `jabali docker-app env set`

Edit env values, re-render the compose, and recreate the container

```
jabali docker-app env set <id> <KEY=VALUE> [<KEY=VALUE>...]
```

#### `jabali docker-app exec`

Run a one-off command in the app's container

```
jabali docker-app exec <id> -- <command...> [flags]
```

**Flags:**

- `--service` — compose service to exec into (default: primary)

#### `jabali docker-app install`

Install a catalog entry (creates the docker_apps row + dispatches the agent)

```
jabali docker-app install <slug> [flags]
```

**Flags:**

- `--cpu` — cgroup CPU limit (e.g. 1.0). Catalog default when omitted.
- `--domain` — domain the tenant app attaches to (required with --user; must be owned by that user or free)
- `--env` — KEY=VALUE override (repeatable) (default `[]`)
- `--memory` — memory limit (e.g. 512m). Catalog default when omitted.
- `--name` — install name (lowercase, ^[a-z0-9-]{1,32}$)
- `--pids` — pids cgroup limit. Catalog default when omitted. (default `0`)
- `--update-mode` — manual|auto (default `manual`)
- `--user` — install for this tenant (user id or username); enables the tenant-scoped install path

#### `jabali docker-app list`

List installed docker apps

```
jabali docker-app list
```

#### `jabali docker-app logs`

Tail container logs

```
jabali docker-app logs <id> [flags]
```

**Flags:**

- `--lines` — lines to tail (default `200`)
- `--service` — compose service name (default: first service)

#### `jabali docker-app maintenance`

Docker disk-usage report and prune

```
jabali docker-app maintenance
```

##### `jabali docker-app maintenance disk-usage`

Report Docker disk usage

```
jabali docker-app maintenance disk-usage
```

##### `jabali docker-app maintenance prune`

Prune unused Docker resources (reclaims disk)

```
jabali docker-app maintenance prune [flags]
```

**Flags:**

- `--all` — remove all unused images, not just dangling
- `--force` — confirm the prune
- `--volumes` — also prune unused volumes

#### `jabali docker-app patch`

Patch app settings (update mode, CPU/memory/PID limits)

```
jabali docker-app patch <id> [flags]
```

**Flags:**

- `--cpu` — CPU limit (e.g. 1.5; empty to clear)
- `--memory` — memory limit (e.g. 512m; empty to clear)
- `--pids` — PIDs limit (default `0`)
- `--update-mode` — update mode: manual|auto

#### `jabali docker-app rebuild`

Force-recreate (docker compose up --force-recreate)

```
jabali docker-app rebuild <id>
```

#### `jabali docker-app restart`

Restart an install

```
jabali docker-app restart <id>
```

#### `jabali docker-app restore`

Restore a selected backup snapshot (replaces the app's data)

```
jabali docker-app restore <id> <snapshot-id> [flags]
```

**Flags:**

- `--force` — confirm the destructive restore

#### `jabali docker-app start`

Start a stopped install

```
jabali docker-app start <id>
```

#### `jabali docker-app status`

Show full status of an installed app (DB row + agent status)

```
jabali docker-app status <id>
```

#### `jabali docker-app stop`

Stop a running install

```
jabali docker-app stop <id>
```

#### `jabali docker-app update`

Pull the latest image and re-create the stack

```
jabali docker-app update <id>
```

### `jabali domain`

Manage hosted domains

```
jabali domain
```

#### `jabali domain bandwidth`

Per-domain bandwidth report (bytes + requests, daily series)

```
jabali domain bandwidth <domain-name|domain-id> [flags]
```

**Flags:**

- `--days` — lookback window in days (1..365) (default `30`)

#### `jabali domain browse`

List a path under the domain's docroot (as the domain owner)

```
jabali domain browse <domain-name|domain-id> [relative-path]
```

#### `jabali domain catchall`

Manage per-domain catch-all routing

```
jabali domain catchall
```

##### `jabali domain catchall clear`

Remove the catch-all rule for a domain

```
jabali domain catchall clear <domain-name-or-id>
```

##### `jabali domain catchall set`

Route mail to unknown@<domain> to --target

```
jabali domain catchall set <domain-name-or-id> [flags]
```

**Flags:**

- `--target` — Destination email address (required)

##### `jabali domain catchall show`

Print the current catch-all target

```
jabali domain catchall show <domain-name-or-id>
```

#### `jabali domain create`

Create a new domain (direct DB; bypasses HTTP auth — M20-safe)

```
jabali domain create [flags]
```

**Flags:**

- `--doc-root` — Document root (optional, auto-generated if not provided)
- `--name` — Domain name (required)
- `--user` — User email, username, or ULID (required)

#### `jabali domain delete`

Delete a domain (direct DB; reconciler tears down nginx — M20-safe)

```
jabali domain delete <domain-name|domain-id> [flags]
```

**Flags:**

- `--force` — Skip confirmation prompt

#### `jabali domain dir-privacy`

Manage password-protected directories (auth_basic) (JAB-130)

```
jabali domain dir-privacy
```

##### `jabali domain dir-privacy add`

Protect a directory

```
jabali domain dir-privacy add <domain> --path /dir [--realm ...] [flags]
```

**Flags:**

- `--path` — directory path under the docroot (e.g. /admin)
- `--realm` — auth realm shown in the browser prompt (default Restricted)

##### `jabali domain dir-privacy cred-add`

Add a credential to a protected directory

```
jabali domain dir-privacy cred-add <domain> <rule-id> --user <name> (--password <p> | --password-stdin) [flags]
```

**Flags:**

- `--password` — basic-auth password (8-128 chars; prefer --password-stdin)
- `--password-stdin` — read the password from stdin (no argv leak)
- `--user` — basic-auth username

##### `jabali domain dir-privacy cred-list`

List credentials for a protected directory

```
jabali domain dir-privacy cred-list <domain> <rule-id>
```

##### `jabali domain dir-privacy cred-remove`

Remove a credential from a protected directory

```
jabali domain dir-privacy cred-remove <domain> <rule-id> <cred-id>
```

##### `jabali domain dir-privacy list`

List protected directories for a domain

```
jabali domain dir-privacy list <domain>
```

##### `jabali domain dir-privacy remove`

Remove a protected directory (and its credentials)

```
jabali domain dir-privacy remove <domain> <rule-id>
```

#### `jabali domain disable`

Disable a domain (direct DB — M20-safe)

```
jabali domain disable <domain-name|domain-id>
```

#### `jabali domain disclaimer`

Manage per-domain outbound disclaimer

```
jabali domain disclaimer
```

##### `jabali domain disclaimer clear`

Disable + remove the outbound disclaimer for a domain

```
jabali domain disclaimer clear <domain-name-or-id>
```

##### `jabali domain disclaimer set`

Set + enable the outbound disclaimer for a domain

```
jabali domain disclaimer set <domain-name-or-id> [flags]
```

**Flags:**

- `--file` — Read disclaimer from this file (overrides --text)
- `--text` — Disclaimer text (UTF-8, plain or HTML)

##### `jabali domain disclaimer show`

Print the current disclaimer for a domain

```
jabali domain disclaimer show <domain-name-or-id>
```

#### `jabali domain email-disable`

Disable email for a domain (keeps DKIM key per ADR-0043)

```
jabali domain email-disable <domain-name-or-id>
```

#### `jabali domain email-dkim-rotate`

Rotate the domain's DKIM keypair (ADR-0043; operator-driven, not automatic)

```
jabali domain email-dkim-rotate <domain-name-or-id>
```

#### `jabali domain email-enable`

Enable email for a domain (generates DKIM + publishes DNS records)

```
jabali domain email-enable <domain-name-or-id>
```

#### `jabali domain enable`

Enable a domain (direct DB — M20-safe)

```
jabali domain enable <domain-name|domain-id>
```

#### `jabali domain fix-perms`

Repair docroot group/setgid (www-data) for all of a user's domains

```
jabali domain fix-perms <username>
```

#### `jabali domain htaccess-preview`

Preview the nginx translation of a .htaccess file (stateless; nothing applied)

```
jabali domain htaccess-preview <domain-name|domain-id> [flags]
```

**Flags:**

- `--file` — path to a .htaccess file to convert (required)

#### `jabali domain ip-acl`

Manage a domain's IP allow/deny ACL

```
jabali domain ip-acl
```

##### `jabali domain ip-acl add`

Add an IP ACL entry to a domain

```
jabali domain ip-acl add <domain> [flags]
```

**Flags:**

- `--action` — allow|deny (default `deny`)
- `--cidr` — IP or CIDR (required)
- `--comment` — optional comment
- `--priority` — rule priority (lower = first) (default `0`)

##### `jabali domain ip-acl delete`

Delete an IP ACL entry by id

```
jabali domain ip-acl delete <acl-id>
```

##### `jabali domain ip-acl list`

List a domain's IP ACL entries

```
jabali domain ip-acl list <domain>
```

#### `jabali domain list`

List domains (direct DB — M20-safe)

```
jabali domain list
```

#### `jabali domain mta-sts`

Enable/disable MTA-STS for a mail domain (ADR-0109)

```
jabali domain mta-sts <domain-name|domain-id> [flags]
```

**Flags:**

- `--disable` — disable MTA-STS
- `--enable` — enable MTA-STS

#### `jabali domain php-settings`

Get/set a domain's php.ini directives (JAB-129)

```
jabali domain php-settings
```

##### `jabali domain php-settings get`

Show a domain's php.ini directives

```
jabali domain php-settings get <domain-name-or-id>
```

##### `jabali domain php-settings set`

Set php.ini directives (only the flags you pass change; reconciler converges)

```
jabali domain php-settings set <domain-name-or-id> [flags]
```

**Flags:**

- `--max-execution-time` — max_execution_time seconds (1..86400) (default `0`)
- `--max-input-time` — max_input_time seconds (1..86400) (default `0`)
- `--max-input-vars` — max_input_vars (1..86400) (default `0`)
- `--memory-limit` — memory_limit (e.g. 256M)
- `--post-max-size` — post_max_size (e.g. 64M)
- `--upload-max-filesize` — upload_max_filesize (e.g. 64M)

#### `jabali domain php-version`

Manage a domain's PHP version (per-domain FPM pool, GH #329)

```
jabali domain php-version
```

##### `jabali domain php-version set`

Bind a domain to a PHP version (find-or-create the pool; reconciler converges)

```
jabali domain php-version set <domain-name-or-id> --version X.Y [flags]
```

**Flags:**

- `--version` — PHP version X.Y (e.g. 8.2); required

##### `jabali domain php-version show`

Show a domain's bound PHP version

```
jabali domain php-version show <domain-name-or-id>
```

#### `jabali domain prune-orphans`

List sites in nginx sites-enabled that have no panel DB row (and optionally delete them)

```
jabali domain prune-orphans [flags]
```

**Flags:**

- `--apply` — Actually delete orphans (default: dry-run)

#### `jabali domain set`

Set advanced domain settings (redirect / nginx directives / index / ssl mode / cache)

```
jabali domain set <domain-name|domain-id> [flags]
```

**Flags:**

- `--cache` — on|off — nginx fastcgi page cache
- `--index-priority` — directory index priority (e.g. html_first, php_first)
- `--nginx-directives` — raw custom nginx directives for the server block
- `--redirect-all-to` — redirect the whole domain to this URL ('' clears)
- `--redirect-type` — permanent|temporary
- `--ssl-mode` — le|self|none (custom = install a cert)

#### `jabali domain show`

Show a domain's full advanced-settings state (JSON)

```
jabali domain show <domain-name|domain-id>
```

#### `jabali domain skip-auto-san`

Opt a domain in/out of the panel-cert auto-SAN (M50)

```
jabali domain skip-auto-san <domain-name|domain-id> [flags]
```

**Flags:**

- `--disable` — include this domain in the panel cert SAN
- `--enable` — exclude this domain from the panel cert SAN

#### `jabali domain whois`

WHOIS lookup for a domain

```
jabali domain whois <domain-name|domain-id>
```

### `jabali files`

Scoped tenant file manager (list/read/mkdir/move/chmod/archive/…) — same policy as the GUI

```
jabali files
```

#### `jabali files archive`

tar.gz one or more paths to a local file (--out)

```
jabali files archive <path...> [flags]
```

**Flags:**

- `--out` — local .tar.gz destination (required)
- `--user` — target tenant (email|username|id, required)

#### `jabali files chmod`

Change a path's permissions (e.g. 0644)

```
jabali files chmod <path> <octal-mode> [flags]
```

**Flags:**

- `--user` — target tenant (email|username|id, required)

#### `jabali files copy`

Copy into a destination directory (--to)

```
jabali files copy <path> [flags]
```

**Flags:**

- `--to` — destination directory (required)
- `--user` — target tenant (email|username|id, required)

#### `jabali files delete`

Delete a file or directory

```
jabali files delete <path> [flags]
```

**Flags:**

- `--force` — confirm deletion
- `--recursive` — recurse into directories
- `--user` — target tenant (email|username|id, required)

#### `jabali files download`

Download a file to a local path (--out)

```
jabali files download <path> [flags]
```

**Flags:**

- `--out` — local destination path (required)
- `--user` — target tenant (email|username|id, required)

#### `jabali files extract`

Extract an archive inside the tenant tree (--dest)

```
jabali files extract <archive-path> [flags]
```

**Flags:**

- `--dest` — destination directory (default: archive's directory)
- `--user` — target tenant (email|username|id, required)

#### `jabali files list`

List a directory

```
jabali files list <path> [flags]
```

**Flags:**

- `--user` — target tenant (email|username|id, required)

#### `jabali files mkdir`

Create a directory

```
jabali files mkdir <path> [flags]
```

**Flags:**

- `--user` — target tenant (email|username|id, required)

#### `jabali files move`

Move into a destination directory (--to)

```
jabali files move <path> [flags]
```

**Flags:**

- `--to` — destination directory (required)
- `--user` — target tenant (email|username|id, required)

#### `jabali files read`

Print a file's contents

```
jabali files read <path> [flags]
```

**Flags:**

- `--user` — target tenant (email|username|id, required)

#### `jabali files rename`

Rename within the same directory

```
jabali files rename <path> <new-name> [flags]
```

**Flags:**

- `--user` — target tenant (email|username|id, required)

#### `jabali files stat`

Stat a path

```
jabali files stat <path> [flags]
```

**Flags:**

- `--user` — target tenant (email|username|id, required)

#### `jabali files upload`

Upload a local file into the tenant tree

```
jabali files upload <local-file> <dest-path> [flags]
```

**Flags:**

- `--overwrite` — overwrite if the destination exists
- `--user` — target tenant (email|username|id, required)

#### `jabali files write`

Write text to a file (--content or --from <local>)

```
jabali files write <path> [flags]
```

**Flags:**

- `--content` — inline text content
- `--from` — read content from this local file
- `--user` — target tenant (email|username|id, required)

### `jabali impersonation`

Manage admin act-as impersonation grants (list / create / end)

```
jabali impersonation
```

#### `jabali impersonation create`

Create a 60-minute impersonation grant for an admin to act as a target user

```
jabali impersonation create --admin <admin> --target <user> [flags]
```

**Flags:**

- `--admin` — admin who will act-as (email|username|id, required)
- `--target` — target user to impersonate (email|username|id, required)

#### `jabali impersonation end`

End an active impersonation grant

```
jabali impersonation end <grant-id>
```

#### `jabali impersonation list`

List active impersonation grants (all, or for one admin)

```
jabali impersonation list [flags]
```

**Flags:**

- `--admin` — scope to one admin (email|username|id); default = all admins

### `jabali ip`

Admin IP address pool (managed_ips): list / add / update / delete

```
jabali ip
```

#### `jabali ip add`

Add an IP to the managed pool

```
jabali ip add <address> [flags]
```

**Flags:**

- `--label` — human label
- `--user-selectable` — tenants may pick this IP

#### `jabali ip delete`

Delete an IP from the pool

```
jabali ip delete <id> [flags]
```

**Flags:**

- `--force` — confirm deletion

#### `jabali ip list`

List managed IP addresses

```
jabali ip list
```

#### `jabali ip update`

Update an IP's label / user-selectable / default flag

```
jabali ip update <id> [flags]
```

**Flags:**

- `--default` — promote to per-family default
- `--label` — new label
- `--user-selectable` — tenants may pick this IP

### `jabali limits`

Per-user resource limits (cgroups v2 + POSIX quota + nginx)

```
jabali limits
```

#### `jabali limits apply`

Re-apply effective limits for one user

```
jabali limits apply <username>
```

#### `jabali limits check`

Probe host for M18 prerequisites (cgroups v2, /home fs, nginx modules)

```
jabali limits check
```

#### `jabali limits override`

Per-user limit overrides (set / clear)

```
jabali limits override
```

##### `jabali limits override clear`

Remove a user's override (revert to package)

```
jabali limits override clear <username|user-id>
```

##### `jabali limits override set`

Set/replace a user's limit override

```
jabali limits override set <username|user-id> [flags]
```

**Flags:**

- `--cpu` — CPU quota percent (default `0`)
- `--disk-mb` — disk quota MB (default `0`)
- `--io-read-mbps` — IO read MB/s (default `0`)
- `--io-write-mbps` — IO write MB/s (default `0`)
- `--max-tasks` — max tasks (default `0`)
- `--memory-mb` — memory limit MB (default `0`)

#### `jabali limits package`

Bulk limits operations across every user of a package

```
jabali limits package
```

##### `jabali limits package apply`

Re-apply limits to every user of the given package

```
jabali limits package apply <package_id> [flags]
```

**Flags:**

- `--dry-run` — print what would be applied without making agent calls

#### `jabali limits status`

Show live resource usage for one user

```
jabali limits status <username>
```

#### `jabali limits usage`

Show a user's live limit usage (disk/cpu/mem/io/tasks)

```
jabali limits usage <username|user-id>
```

### `jabali log`

Inspect log types and mint/revoke log-stream access grants

```
jabali log
```

#### `jabali log access`

Mint or revoke log-stream access grants

```
jabali log access
```

##### `jabali log access create`

Mint a 15-minute log-stream access grant for a user

```
jabali log access create --user <user> --type <access|error|goaccess> [--domain <domain>] [flags]
```

**Flags:**

- `--domain` — scope to one domain (optional)
- `--type` — log type: access|error|goaccess (required)
- `--user` — grant owner (email|username|id, required)

##### `jabali log access revoke`

Revoke a log-stream access grant

```
jabali log access revoke <stream-key>
```

#### `jabali log types`

List the supported log types

```
jabali log types
```

### `jabali mail`

Admin mail queue + outbound throttle management

```
jabali mail
```

#### `jabali mail deliverability`

Show the mail deliverability score (server-wide or per-domain)

```
jabali mail deliverability [flags]
```

**Flags:**

- `--domain` — scope to one domain (default: server-wide)

#### `jabali mail logs`

Search / inspect mail delivery logs

```
jabali mail logs
```

##### `jabali mail logs detail`

Show a mail log entry's detail

```
jabali mail logs detail <id>
```

##### `jabali mail logs query`

Search mail logs

```
jabali mail logs query [flags]
```

**Flags:**

- `--from` — from date (YYYY-MM-DD)
- `--limit` — max entries (default `50`)
- `--offset` — offset (default `0`)
- `--recipient` — recipient prefix filter
- `--sender` — sender prefix filter
- `--to` — to date (YYYY-MM-DD)

#### `jabali mail queue`

Inspect / retry / delete queued mail

```
jabali mail queue
```

##### `jabali mail queue delete`

Delete a queued message

```
jabali mail queue delete <id> [flags]
```

**Flags:**

- `--force` — confirm deletion

##### `jabali mail queue list`

List queued mail

```
jabali mail queue list
```

##### `jabali mail queue retry`

Retry a queued message

```
jabali mail queue retry <id>
```

#### `jabali mail throttle`

Outbound mail throttle policies

```
jabali mail throttle
```

##### `jabali mail throttle create`

Create an outbound throttle policy

```
jabali mail throttle create [flags]
```

**Flags:**

- `--disabled` — create disabled
- `--max-per-day` — max messages/day (0 = unlimited) (default `0`)
- `--max-per-hour` — max messages/hour (0 = unlimited) (default `0`)
- `--scope` — user|domain|global (required)
- `--scope-ref` — email (user) or FQDN (domain); omit for global

##### `jabali mail throttle delete`

Delete a throttle policy

```
jabali mail throttle delete <id>
```

##### `jabali mail throttle list`

List outbound throttle policies

```
jabali mail throttle list
```

##### `jabali mail throttle update`

Update a throttle policy's limits / enabled flag

```
jabali mail throttle update <id> [flags]
```

**Flags:**

- `--disable` — disable the policy
- `--enable` — enable the policy
- `--max-per-day` — max messages/day (default `0`)
- `--max-per-hour` — max messages/hour (default `0`)

### `jabali mail-cert`

Manage a domain's mail TLS certificate (mail.<domain> SAN)

```
jabali mail-cert
```

#### `jabali mail-cert disable`

Disable the mail certificate for a domain

```
jabali mail-cert disable <domain-name|domain-id>
```

#### `jabali mail-cert enable`

Enable the mail certificate for a domain (issues on the next reconcile pass)

```
jabali mail-cert enable <domain-name|domain-id>
```

#### `jabali mail-cert reissue`

Clear backoff and re-issue a domain's mail certificate

```
jabali mail-cert reissue <domain-name|domain-id>
```

#### `jabali mail-cert show`

Show a domain's mail certificate state

```
jabali mail-cert show <domain-name|domain-id>
```

### `jabali mail-group`

Manage mail groups (distribution lists + shared-resource groups) and members

```
jabali mail-group
```

#### `jabali mail-group add-member`

Add one member to a mail group

```
jabali mail-group add-member <group-email|group-id> <member-email-or-id>
```

#### `jabali mail-group create`

Create a mail group on a domain

```
jabali mail-group create <domain-name|domain-id> <local-part> [flags]
```

**Flags:**

- `--description` — description
- `--display-name` — display name (the From name)
- `--internal-only` — reject mail from senders outside the group's domain (GH #348)
- `--kind` — group kind: resource | distribution (default `resource`)

#### `jabali mail-group delete`

Delete a mail group (strips the Stalwart principal first)

```
jabali mail-group delete <group-email|group-id> [flags]
```

**Flags:**

- `--force` — confirm deletion

#### `jabali mail-group list`

List mail groups (all, or scoped to one domain)

```
jabali mail-group list [domain-name|domain-id]
```

#### `jabali mail-group remove-member`

Remove one member from a mail group

```
jabali mail-group remove-member <group-email|group-id> <member-email-or-id>
```

#### `jabali mail-group set-members`

Replace the full member set of a mail group

```
jabali mail-group set-members <group-email|group-id> [member-email-or-id ...]
```

#### `jabali mail-group set-resources`

Toggle a resource group's shared collections (mailbox/calendar/addressbook/files)

```
jabali mail-group set-resources <group-email|group-id> [flags]
```

**Flags:**

- `--addressbook` — shared addressbook (true|false)
- `--calendar` — shared calendar (true|false)
- `--files` — shared files (true|false)
- `--mailbox` — shared mailbox (true|false)

#### `jabali mail-group show`

Show a mail group and its members

```
jabali mail-group show <group-email|group-id>
```

### `jabali mailbox`

Manage mailboxes (M6 Email via Stalwart)

```
jabali mailbox
```

#### `jabali mailbox autoresponder`

Manage per-mailbox vacation responders

```
jabali mailbox autoresponder
```

##### `jabali mailbox autoresponder clear`

Disable + delete the autoresponder for a mailbox

```
jabali mailbox autoresponder clear <email>
```

##### `jabali mailbox autoresponder set`

Enable an autoresponder for a mailbox

```
jabali mailbox autoresponder set <email> [flags]
```

**Flags:**

- `--body` — Plain text body (optional if --html-body set)
- `--from` — Start date (RFC3339, e.g. 2026-05-01T00:00:00Z)
- `--html-body` — HTML body (optional)
- `--subject` — Subject line (required)
- `--to` — End date (RFC3339)

##### `jabali mailbox autoresponder show`

Print the current autoresponder for a mailbox

```
jabali mailbox autoresponder show <email>
```

#### `jabali mailbox create`

Create a mailbox (password shown once if auto-generated)

```
jabali mailbox create [flags]
```

**Flags:**

- `--domain` — Domain name or ID (required)
- `--local` — Local part, e.g. "alice" (required)
- `--password` — Explicit password (omit to auto-generate)
- `--quota-mb` — Disk quota in MiB (default 1024) (default `0`)

#### `jabali mailbox delete`

Delete a mailbox (agent destroys Stalwart account first)

```
jabali mailbox delete <email> [flags]
```

**Flags:**

- `--force` — Skip confirmation prompt

#### `jabali mailbox forwarder`

Manage per-mailbox aliases + external forwards

```
jabali mailbox forwarder
```

##### `jabali mailbox forwarder add`

Add an alias or external forwarder to a mailbox

```
jabali mailbox forwarder add <email> [flags]
```

**Flags:**

- `--keep-copy` — type=external: keep a copy in the mailbox (Sieve redirect :copy)
- `--local` — Alias local part (required for type=alias)
- `--target` — External destination email (required for type=external)
- `--type` — alias | external (required)

##### `jabali mailbox forwarder list`

List forwarders attached to a mailbox

```
jabali mailbox forwarder list <email>
```

##### `jabali mailbox forwarder remove`

Delete a forwarder by ID (find via 'forwarder list')

```
jabali mailbox forwarder remove <forwarder-id>
```

#### `jabali mailbox list`

List mailboxes in a domain

```
jabali mailbox list [flags]
```

**Flags:**

- `--domain` — Domain name or ID (required)

#### `jabali mailbox passwd`

Rotate a mailbox password (auto-generated and shown once if --password omitted)

```
jabali mailbox passwd <email> [flags]
```

**Flags:**

- `--password` — Explicit new password (omit to auto-generate)

#### `jabali mailbox set-quota`

Update a mailbox disk quota (in MiB)

```
jabali mailbox set-quota <email> <mb>
```

#### `jabali mailbox shares`

Manage shared mailbox folders (M6.5)

```
jabali mailbox shares
```

##### `jabali mailbox shares add`

Grant a target mailbox shared access to the owner's mailbox

```
jabali mailbox shares add [flags]
```

**Flags:**

- `--owner` — Owner mailbox email (required)
- `--rights` — Preset: ro | rw | admin (default rw) (default `rw`)
- `--shared-with` — Mailbox to grant share to (required)

##### `jabali mailbox shares list`

List shares for a given owner email

```
jabali mailbox shares list [flags]
```

**Flags:**

- `--owner` — Owner mailbox email (required)

##### `jabali mailbox shares remove`

Revoke a share by ID

```
jabali mailbox shares remove [flags]
```

**Flags:**

- `--id` — Share ID (ULID, from `jabali mailbox shares list`)

### `jabali malware`

Malware scan / quarantine / YARA / settings (incident response)

```
jabali malware
```

#### `jabali malware events`

List malware detection events

```
jabali malware events [flags]
```

**Flags:**

- `--severity` — filter: info|warn|critical
- `--source` — filter: maldet|clamav|yara
- `--user` — filter by user id

#### `jabali malware quarantine`

List / restore / delete quarantined files

```
jabali malware quarantine
```

##### `jabali malware quarantine delete`

Permanently delete a quarantined file

```
jabali malware quarantine delete <id> [flags]
```

**Flags:**

- `--force` — confirm permanent deletion

##### `jabali malware quarantine list`

List active quarantine entries

```
jabali malware quarantine list [flags]
```

**Flags:**

- `--limit` — max rows (default `100`)

##### `jabali malware quarantine restore`

Restore a quarantined file to its original path

```
jabali malware quarantine restore <id> [flags]
```

**Flags:**

- `--reason` — why the file is being restored (required)

#### `jabali malware scan`

Start a malware scan of a path or a user's home

```
jabali malware scan [flags]
```

**Flags:**

- `--path` — absolute path to scan
- `--user` — username whose home to scan

#### `jabali malware scan-status`

Poll a scan session's status

```
jabali malware scan-status <session-id>
```

#### `jabali malware settings`

Get / set malware settings

```
jabali malware settings
```

##### `jabali malware settings get`

Show malware settings

```
jabali malware settings get
```

##### `jabali malware settings set`

Update malware settings (same validation as the UI)

```
jabali malware settings set [flags]
```

**Flags:**

- `--max-scan-mb` — max scan file size MB (1..10240) (default `0`)
- `--notify-threshold` — info|warn|critical
- `--realtime` — on|off (real-time monitor)
- `--retain-days` — quarantine retention days (1..365) (default `0`)

#### `jabali malware status`

Show malware engine status

```
jabali malware status
```

#### `jabali malware update-signatures`

Update malware signatures (freshclam/maldet)

```
jabali malware update-signatures
```

#### `jabali malware yara`

Manage custom YARA rules

```
jabali malware yara
```

##### `jabali malware yara delete`

Delete a custom YARA rule

```
jabali malware yara delete <filename>
```

##### `jabali malware yara list`

List custom YARA rules

```
jabali malware yara list
```

##### `jabali malware yara toggle`

Enable or disable a custom YARA rule

```
jabali malware yara toggle <filename> [flags]
```

**Flags:**

- `--disable` — disable the rule
- `--enable` — enable the rule

##### `jabali malware yara upload`

Upload a custom YARA rule file

```
jabali malware yara upload <file.yar>
```

### `jabali malware-purge`

Hard-delete terminated malware quarantine rows past retention (M33)

```
jabali malware-purge
```

### `jabali migrate`

Database migration commands

```
jabali migrate
```

#### `jabali migrate force`

Clear the dirty flag by asserting the schema is at <version> (runs no SQL)

```
jabali migrate force <version>
```

#### `jabali migrate imap`

Migrate a remote IMAP mailbox into a jabali mailbox (GH #390/#374)

```
jabali migrate imap [flags]
```

**Flags:**

- `--allow-private` — permit RFC1918/loopback targets (migrating from a LAN server)
- `--csv` — batch: CSV with header host,user,password,to[,port,starttls]
- `--host` — remote IMAP host (e.g. imap.gmail.com)
- `--password-file` — read the app-password from a file (single account)
- `--password-stdin` — read the app-password from stdin (single account)
- `--port` — remote IMAP port (0 = 993 implicit-TLS, or 143 with --starttls) (default `0`)
- `--probe` — list the remote folder layout and exit without migrating
- `--starttls` — use STARTTLS on 143 instead of implicit TLS on 993
- `--to` — destination jabali mailbox (must already exist)
- `--user` — remote IMAP login (full address)

#### `jabali migrate import`

Run (or resume) a migration job through the four-stage pipeline

```
jabali migrate import [flags]
```

**Flags:**

- `--allow-degraded` — exit 0 even if the restore ends degraded (a core area — DB/mail/health — failed)
- `--job-id` — migration_jobs.id (ULID) — required
- `--keep-staging` — do NOT delete /var/lib/jabali-migrations/<job-id>/ after run (debug aid)
- `--preserve-source-state` — keep imported source state ACTIVE (mail forwarders/catchalls/filters/autoresponders, and — as later PRs land — credentials/SSL/SSH). Default OFF: imported artifacts land inert and the operator re-activates trusted ones in the panel (JAB-46). Only use for trusted same-owner migrations.
- `--target-email` — destination user email (only used when auto-creating)
- `--target-package-id` — hosting package ULID (only used when auto-creating)
- `--target-password` — destination user password (only used when auto-creating; ≥10 chars)
- `--target-user` — destination jabali username — auto-created if --target-email + --target-password supplied

#### `jabali migrate import-wp`

Import a staged wordpress_ssh migration into a destination (GH #647)

```
jabali migrate import-wp [flags]
```

**Flags:**

- `--dest-domain` — destination domain (docroot = /home/<user>/domains/<domain>/public_html)
- `--dest-user` — destination OS username (owns the docroot)
- `--job-id` — wordpress_ssh migration_jobs.id (staged)

#### `jabali migrate pull-source`

Connect to source via SSH, run kind-appropriate backup, pull + extract tarball

```
jabali migrate pull-source [flags]
```

**Flags:**

- `--job-id` — migration_jobs.id (ULID) — required
- `--ssh-user` — SSH login on the source (default 'root') (default `root`)

#### `jabali migrate reap-secrets`

Wipe per-job migration-secrets env files + stale tarball/extracted dirs

```
jabali migrate reap-secrets [flags]
```

**Flags:**

- `--dry-run` — List would-delete paths without removing them
- `--staging-max-age` — Reap /var/lib/jabali-migrations/<id>/ only when the job has been terminal at least this long (default 168h = 7d; pass 0 to wipe immediately) (default `168h0m0s`)

#### `jabali migrate refresh`

Force re-pull (refresh) an already-migrated account from a staged source

```
jabali migrate refresh [flags]
```

**Flags:**

- `--db` — Dest DB name (identity unchanged)
- `--docroot` — Dest docroot (overwritten)
- `--domain` — Dest domain (for cache purge)
- `--force` — Required — refresh overwrites a live account
- `--new-url` — New site URL (search-replace)
- `--old-url` — Old site URL (search-replace)
- `--os-user` — Dest Linux user
- `--source-docroot` — Staged source docroot
- `--source-sql` — Staged source SQL dump

#### `jabali migrate restore`

One-shot offline restore from a cpmove tarball (create job + stage + import)

```
jabali migrate restore [flags]
```

**Flags:**

- `--cpanel` — source is a cPanel cpmove / WHM pkgacct tarball
- `--file` — path to the cpmove tarball (cpmove-<user>.tar.gz) — required
- `--fresh` — alias of --retry-from-scratch
- `--hestiacp` — source is a HestiaCP v-backup-user tarball (<user>.<YYYY-MM-DD_HH-MM-SS>.tar[.gz])
- `--keep-staging` — keep /var/lib/jabali-migrations/<job-id>/ after the run (debug)
- `--preserve-source-state` — keep imported source state ACTIVE + carry source credentials where safe: preserves the mailbox password (Stalwart-verifiable bcrypt only), keeps mail forwarders/catchalls/filters/autoresponders active, restores DB user creds + source SSL. Default OFF (secure): mail gets a fresh password (tenant must reset), routing artifacts land inert. Parity with `jabali migrate import`. Only use for a trusted same-owner migration.
- `--restore-file` — alias of --file
- `--retry-from-scratch` — reuse the source + options but wipe the existing job's stages and re-run the whole pipeline from analyze (recreates the target user, replaces stale manifest); default is a gentle resume
- `--source-host` — informational source host (offline restore leaves this empty)
- `--source-user` — cPanel account (default: derived from the cpmove filename)
- `--target-email` — destination email (only used when auto-creating the user)
- `--target-package-id` — hosting package ULID (auto-create only)
- `--target-password` — destination password (auto-create only; ≥10 chars)
- `--target-user` — destination jabali username (default: the source account)

#### `jabali migrate status`

Show the schema version and whether it is dirty

```
jabali migrate status
```

#### `jabali migrate up`

Run pending database migrations

```
jabali migrate up
```

### `jabali notification`

Inspect notification channels and toggle event notifications

```
jabali notification
```

#### `jabali notification broadcast`

Broadcast a notification to every enabled channel

```
jabali notification broadcast --title <t> [--body <b>] [--severity info|warning|error|critical] [flags]
```

**Flags:**

- `--body` — notification body (<=2000)
- `--deeplink` — optional in-panel deeplink
- `--severity` — info|warning|error|critical (default info)
- `--title` — notification title (required, <=200)

#### `jabali notification channels`

Notification channels

```
jabali notification channels
```

##### `jabali notification channels create`

Create a notification channel

```
jabali notification channels create --name <n> --kind <email|slack|discord|ntfy|webhook|webpush|sms> --config <json> [flags]
```

**Flags:**

- `--config` — per-kind config JSON (e.g. '{"url":"https://…"}')
- `--disabled` — create in the disabled state
- `--kind` — email|slack|discord|ntfy|webhook|webpush|sms (required)
- `--name` — channel name (required)
- `--user` — owner user id (tenant-owned channel; omit for server-wide)

##### `jabali notification channels delete`

Delete a notification channel

```
jabali notification channels delete <id> [flags]
```

**Flags:**

- `--force` — confirm deletion

##### `jabali notification channels list`

List configured notification channels

```
jabali notification channels list
```

##### `jabali notification channels seal-secrets`

Encrypt any plaintext channel secrets at rest (JAB-171 one-time backfill)

```
jabali notification channels seal-secrets
```

##### `jabali notification channels test`

Send a synthetic test notification to one channel

```
jabali notification channels test <id>
```

#### `jabali notification dlq`

Inspect and operate the notification dead-letter queue

```
jabali notification dlq
```

##### `jabali notification dlq clear`

Delete ALL DLQ entries (does not replay)

```
jabali notification dlq clear [flags]
```

**Flags:**

- `--force` — confirm clearing the entire DLQ

##### `jabali notification dlq drop`

Delete one DLQ entry without replaying

```
jabali notification dlq drop <id>
```

##### `jabali notification dlq list`

List dead-lettered notifications (newest first)

```
jabali notification dlq list [flags]
```

**Flags:**

- `--limit` — max entries to show (1-500) (default `100`)

##### `jabali notification dlq replay`

Re-publish one DLQ entry to the main queue

```
jabali notification dlq replay <id>
```

##### `jabali notification dlq replay-all`

Re-publish every replayable DLQ entry to the main queue

```
jabali notification dlq replay-all
```

#### `jabali notification events`

Per-event notification toggles

```
jabali notification events
```

##### `jabali notification events list`

List notification event kinds and whether each is enabled

```
jabali notification events list
```

##### `jabali notification events set`

Enable or disable notifications for an event kind

```
jabali notification events set <event-kind> --enabled <true|false> [flags]
```

**Flags:**

- `--enabled` — true|false (required)

#### `jabali notification inbox`

Manage a user's notification bell inbox

```
jabali notification inbox
```

##### `jabali notification inbox clear`

Delete all of a user's notifications

```
jabali notification inbox clear --user <user> [flags]
```

**Flags:**

- `--force` — confirm clearing
- `--include-broadcast` — also remove broadcast rows (admin)
- `--user` — target user (required)

##### `jabali notification inbox list`

List a user's recent notifications

```
jabali notification inbox list --user <user> [flags]
```

**Flags:**

- `--limit` — max rows (1-200) (default `50`)
- `--user` — target user (email|username|id, required)

##### `jabali notification inbox read`

Mark one notification read

```
jabali notification inbox read <notification-id>
```

##### `jabali notification inbox read-all`

Mark all of a user's notifications read

```
jabali notification inbox read-all --user <user> [flags]
```

**Flags:**

- `--user` — target user (required)

### `jabali nspawn`

Manage SSH sandbox nspawn images (M13)

```
jabali nspawn
```

#### `jabali nspawn build`

debootstrap a deterministic, immutable nspawn rootfs

```
jabali nspawn build [flags]
```

**Flags:**

- `--codename` — image family (e.g. debian-13) (default `debian-13`)
- `--includes` — comma-separated debootstrap --include list (default `bash,coreutils,procps,findutils,grep,sed,gawk,less,nano,ca-certificates,git,curl,wget,vim-tiny,php-cli,php-mysql,php-curl,php-xml,php-mbstring,php-zip,php-gd,unzip,rsync,mariadb-client`)
- `--snapshot` — snapshot.debian.org timestamp YYYYMMDDTHHMMSSZ (mandatory)
- `--suite` — debootstrap suite (trixie, bookworm, ...) (default `trixie`)
- `--version` — image version label (e.g. v1, v2)

#### `jabali nspawn list`

List sealed nspawn images

```
jabali nspawn list
```

#### `jabali nspawn prune`

Remove sealed images that no user is pinned to

```
jabali nspawn prune [flags]
```

**Flags:**

- `--dry-run` — explicit dry-run (default; mutually exclusive with --yes)
- `--yes` — actually delete (default: dry-run)

### `jabali package`

Manage hosting packages

```
jabali package
```

#### `jabali package create`

Create a hosting package (direct DB — M20-safe)

```
jabali package create [flags]
```

**Flags:**

- `--bw-mb` — bandwidth quota in MB (0=unlimited) (default `0`)
- `--cgi` — enable CGI
- `--cpu` — CPU quota percent across all cores (0=unlimited) (default `0`)
- `--databases` — max databases (0=unlimited) (default `0`)
- `--disk-mb` — disk quota in MB (0=unlimited) (default `0`)
- `--domains` — max domains (0=unlimited) (default `0`)
- `--emails` — max email accounts (0=unlimited) (default `0`)
- `--io-read-mbps` — disk read bandwidth limit in MB/s (0=unlimited) (default `0`)
- `--io-write-mbps` — disk write bandwidth limit in MB/s (0=unlimited) (default `0`)
- `--max-tasks` — max processes/threads per user slice (0=unlimited) (default `0`)
- `--memory-mb` — memory limit in MB (0=unlimited) (default `0`)
- `--name` — package name (required)
- `--ssh` — enable SSH access

#### `jabali package delete`

Delete a hosting package (direct DB — M20-safe)

```
jabali package delete <package-id> [flags]
```

**Flags:**

- `--force` — skip confirmation

#### `jabali package edit`

Edit a hosting package (direct DB — M20-safe)

```
jabali package edit <package-id> [flags]
```

**Flags:**

- `--bw-mb` — bandwidth MB (default `0`)
- `--cgi` — CGI access (true/false)
- `--cpu` — CPU quota percent (default `0`)
- `--databases` — max databases (default `0`)
- `--disk-mb` — disk quota MB (default `0`)
- `--domains` — max domains (default `0`)
- `--emails` — max emails (default `0`)
- `--io-read-mbps` — io read MB/s (default `0`)
- `--io-write-mbps` — io write MB/s (default `0`)
- `--max-tasks` — max processes/threads (default `0`)
- `--memory-mb` — memory limit MB (default `0`)
- `--name` — package name
- `--ssh` — SSH access (true/false)

#### `jabali package list`

List hosting packages (direct DB — M20-safe)

```
jabali package list
```

### `jabali page-template`

Manage error/index page templates

```
jabali page-template
```

#### `jabali page-template get`

Print a page template's content

```
jabali page-template get <key>
```

#### `jabali page-template list`

List page templates

```
jabali page-template list
```

#### `jabali page-template reset`

Reset a page template to the built-in default

```
jabali page-template reset <key>
```

#### `jabali page-template set`

Set a page template's content from a file

```
jabali page-template set <key> [flags]
```

**Flags:**

- `--file` — path to the template content (required)

### `jabali panel-cert`

Manage the panel's own TLS certificates (hostname + mail)

```
jabali panel-cert
```

#### `jabali panel-cert issue`

Request (re)issuance of a panel certificate on the next reconcile pass

```
jabali panel-cert issue [hostname|mail]
```

#### `jabali panel-cert status`

Show both panel certificates (hostname + mail)

```
jabali panel-cert status
```

#### `jabali panel-cert toggle`

Enable/disable Let's Encrypt (or staging) for a panel certificate

```
jabali panel-cert toggle [hostname|mail] [flags]
```

**Flags:**

- `--le` — enable Let's Encrypt issuance (true|false)
- `--staging` — use the LE staging environment (true|false)

### `jabali panel-primary`

Manage the panel's primary mail domain row (ADR-0048)

```
jabali panel-primary
```

#### `jabali panel-primary ensure`

Ensure a panel-primary domain row exists for the given hostname

```
jabali panel-primary ensure [flags]
```

**Flags:**

- `--hostname` — panel hostname (e.g. jabali-panel.local)

### `jabali pdns`

PowerDNS helpers (recursor forwarders, etc.)

```
jabali pdns
```

#### `jabali pdns backfill`

Converge /etc/powerdns/recursor.forwards with the panel database

```
jabali pdns backfill [flags]
```

**Flags:**

- `--dry-run` — explicit dry-run (default; mutually exclusive with --yes)
- `--no-confirm` — skip the y/N confirmation when --yes is used (for scripted runs; otherwise set JABALI_PDNS_BACKFILL_NONINTERACTIVE=1)
- `--verbose` — print per-zone detail
- `--yes` — apply the plan (default is dry-run)

#### `jabali pdns dnssec`

Per-domain DNSSEC management (ADR-0057)

```
jabali pdns dnssec
```

##### `jabali pdns dnssec disable`

Disable DNSSEC (removes keys + rectifies)

```
jabali pdns dnssec disable <domain> [flags]
```

**Flags:**

- `--force` — skip confirmation

##### `jabali pdns dnssec ds`

Print DS records to publish at the parent registrar

```
jabali pdns dnssec ds <domain>
```

##### `jabali pdns dnssec enable`

Enable DNSSEC for a zone (creates KSK+ZSK, rectifies, persists keys)

```
jabali pdns dnssec enable <domain>
```

##### `jabali pdns dnssec status`

Show cached DNSSEC keys for a domain

```
jabali pdns dnssec status <domain>
```

### `jabali per-user-egress`

Per-user PHP-FPM egress firewall (M34) operator commands

```
jabali per-user-egress
```

#### `jabali per-user-egress approve`

Approve a pending egress request

```
jabali per-user-egress approve <request-id>
```

#### `jabali per-user-egress deny`

Deny a pending egress request

```
jabali per-user-egress deny <request-id>
```

#### `jabali per-user-egress flip-mature`

Flip mature LEARNING policies to ENFORCED

```
jabali per-user-egress flip-mature [flags]
```

**Flags:**

- `--dry-run` — show what would change without writing to DB
- `--soak-days` — minimum LEARNING age before auto-flip to ENFORCED (default `7`)

#### `jabali per-user-egress get`

Show a user's egress policy

```
jabali per-user-egress get <email-or-id>
```

#### `jabali per-user-egress requests`

List pending egress requests (the admin queue)

```
jabali per-user-egress requests
```

#### `jabali per-user-egress set-policy`

Set a user's egress state + allowed destinations (replaces the list)

```
jabali per-user-egress set-policy <email-or-id> [flags]
```

**Flags:**

- `--allow` — allowed destination CIDR[,PORT[,PROTO]] (repeatable; replaces the list) (default `[]`)
- `--state` — egress state: off|learning|enforced (required)

#### `jabali per-user-egress summary`

Show egress policy state counts + pending queue depth

```
jabali per-user-egress summary
```

### `jabali php`

PHP version + extension + per-user pool management

```
jabali php
```

#### `jabali php ext`

Manage PHP extensions (server-wide per PHP version)

```
jabali php ext
```

##### `jabali php ext disable`

Disable an installed extension via phpdismod

```
jabali php ext disable <ext> [flags]
```

**Flags:**

- `--version` — PHP version (required)

##### `jabali php ext enable`

Enable an installed extension via phpenmod

```
jabali php ext enable <ext> [flags]
```

**Flags:**

- `--version` — PHP version (required)

##### `jabali php ext install`

Install (apt) an extension package

```
jabali php ext install <ext> [flags]
```

**Flags:**

- `--version` — PHP version (required)

##### `jabali php ext list`

List PHP extensions and their installed/enabled state for a version

```
jabali php ext list [flags]
```

**Flags:**

- `--version` — PHP version (e.g. 8.4) (required)

##### `jabali php ext remove`

Remove (apt) an extension package

```
jabali php ext remove <ext> [flags]
```

**Flags:**

- `--version` — PHP version (required)

#### `jabali php pool`

Per-user PHP-FPM pool

```
jabali php pool
```

##### `jabali php pool create`

Create a PHP-FPM pool for a user

```
jabali php pool create [flags]
```

**Flags:**

- `--idle-timeout` — process idle timeout (seconds) (default `60`)
- `--php-version` — PHP version e.g. 8.3 (required)
- `--pm-max-children` — pm.max_children (default `20`)
- `--pm-mode` — ondemand|dynamic|static (default `ondemand`)
- `--user` — owner (id or username) (required)

##### `jabali php pool delete`

Delete a PHP-FPM pool and its ini overrides

```
jabali php pool delete <pool-id> [flags]
```

**Flags:**

- `--force` — confirm deletion

##### `jabali php pool get`

Show a user's PHP pool

```
jabali php pool get <user>
```

##### `jabali php pool ini`

Manage a pool's php.ini overrides

```
jabali php pool ini
```

###### `jabali php pool ini add`

Add an ini override to a pool

```
jabali php pool ini add <pool-id> [flags]
```

**Flags:**

- `--directive` — php.ini directive (required)
- `--kind` — value|flag (default `value`)
- `--value` — value (for kind=value) or on/off (for kind=flag)

###### `jabali php pool ini list`

List a pool's ini overrides

```
jabali php pool ini list <pool-id>
```

###### `jabali php pool ini remove`

Remove an ini override

```
jabali php pool ini remove <override-id>
```

###### `jabali php pool ini update`

Update an ini override's value

```
jabali php pool ini update <override-id> [flags]
```

**Flags:**

- `--value` — new value

##### `jabali php pool list`

List all PHP-FPM pools

```
jabali php pool list
```

##### `jabali php pool reapply-all`

Mark all active pools pending so the reconciler re-renders them from the current template

```
jabali php pool reapply-all
```

##### `jabali php pool set`

Set a user's PHP version (reconciler regenerates pool conf)

```
jabali php pool set [flags]
```

**Flags:**

- `--user` — user (id|email|username) (required)
- `--version` — PHP version e.g. 8.4 (required)

#### `jabali php version`

Manage installed PHP versions

```
jabali php version
```

##### `jabali php version install`

Install a PHP version (e.g. 8.4) — installs base + required extensions, starts php<v>-fpm

```
jabali php version install <version>
```

##### `jabali php version list`

List installed PHP versions

```
jabali php version list
```

##### `jabali php version reload`

Reload php<v>-fpm.service (zero-downtime SIGUSR2)

```
jabali php version reload <version>
```

### `jabali php-defense`

PHP-defense (Snuffleupagus) status / mode / rules / incidents

```
jabali php-defense
```

#### `jabali php-defense incidents`

List Snuffleupagus incidents

```
jabali php-defense incidents [flags]
```

**Flags:**

- `--limit` — max rows (default `50`)
- `--rule` — filter by rule name

#### `jabali php-defense mode`

Set the Snuffleupagus mode (renders + applies active.rules)

```
jabali php-defense mode <off|simulation|enforce>
```

#### `jabali php-defense rule-toggle`

Enable or disable a Snuffleupagus rule (renders + applies)

```
jabali php-defense rule-toggle <rule-name> [flags]
```

**Flags:**

- `--disable` — disable the rule
- `--enable` — enable the rule
- `--reason` — optional reason for the override

#### `jabali php-defense rules`

List rule overrides + current mode (bundle catalog is GUI-only)

```
jabali php-defense rules
```

#### `jabali php-defense status`

Show Snuffleupagus status

```
jabali php-defense status
```

### `jabali python-app`

Manage Python apps (ADR-0131; admin-only)

```
jabali python-app
```

#### `jabali python-app create`

Create a Python app (same validation + port/proxy allocation as the UI)

```
jabali python-app create [flags]
```

**Flags:**

- `--app-root` — app root under the owner's home (required)
- `--app-type` — wsgi|asgi (default `wsgi`)
- `--base-uri` — mount path on the domain (default `/`)
- `--domain` — domain name to mount on (resolved to its id)
- `--domain-id` — domain id to mount on (or use --domain)
- `--entrypoint` — module:callable, e.g. myapp.wsgi:application (required unless --framework)
- `--env` — KEY=VALUE (repeatable) (default `[]`)
- `--framework` — marketplace slug (e.g. django, flask, fastapi); derives app-type/entrypoint and scaffolds the starter
- `--name` — app name (required)
- `--python-version` — Python version, e.g. 3.12 (required)

#### `jabali python-app delete`

Stop + remove an app (app files are kept)

```
jabali python-app delete <app-id>
```

#### `jabali python-app env`

View or update a Python app's environment variables

```
jabali python-app env
```

##### `jabali python-app env get`

List the app's environment variables (values are not masked — python_app_env has no secret flag)

```
jabali python-app env get <app-id>
```

##### `jabali python-app env set`

Set env vars (merge by default; --replace swaps the whole set)

```
jabali python-app env set <app-id> <KEY=VALUE> [<KEY=VALUE>...] [flags]
```

**Flags:**

- `--replace` — replace the entire env set instead of merging

#### `jabali python-app frameworks`

List the Python frameworks installable via --framework

```
jabali python-app frameworks
```

#### `jabali python-app list`

List Python apps

```
jabali python-app list
```

#### `jabali python-app logs`

Show an app's recent logs

```
jabali python-app logs <app-id> [flags]
```

**Flags:**

- `--lines` — number of log lines (default `200`)

#### `jabali python-app restart`

Restart an app

```
jabali python-app restart <app-id>
```

#### `jabali python-app start`

Start an app

```
jabali python-app start <app-id>
```

#### `jabali python-app stop`

Stop an app

```
jabali python-app stop <app-id>
```

### `jabali release`

Release-channel management (stable/development)

```
jabali release
```

#### `jabali release promote`

Promote a reviewed build to the stable channel (move the `stable` tag)

```
jabali release promote [<commit-ish>]
```

### `jabali repair`

Detect and fix known deployment-host issues

```
jabali repair [flags]
```

**Flags:**

- `--all` — Fix every issue including destructive ones
- `--apparmor-profiles-disabled` — Fix only: jabali AppArmor profiles exist but are disabled
- `--apparmor-profiles-missing` — Fix only: jabali AppArmor profiles absent from /etc/apparmor.d/
- `--auto` — Fix every non-destructive (safe) issue
- `--bulwark-jwt-secret` — Fix only: Bulwark webmail-SSO secret poisoned / out of sync with bulwark.env (mail impersonation 'Invalid signature')
- `--crowdsec-bouncer-key` — Fix only: crowdsec-firewall-bouncer crash-loops with stale LAPI key
- `--daemon-reload` — Fix only: systemd has unloaded unit-file changes on disk
- `--diagnose` — Report broken conditions without fixing
- `--dirty-migration` — Fix only: database schema is dirty — panel-api cannot start
- `--docroot-www-data-group` — Fix only: web docroot files not group www-data / dirs not setgid (nginx 403 on newly uploaded media)
- `--etc-jabali-perms` — Fix only: /etc/jabali not traversable by hosting users (SSH/SFTP locked out — sandbox-mode unreadable)
- `--git-ownership` — Fix only: /opt/jabali-panel/.git owned by wrong user
- `--git-pointer` — Fix only: /opt/jabali-panel/.git is a corrupted worktree pointer
- `--git-stale-worktrees` — Fix only: /opt/jabali-panel/.git/worktrees has stale entries
- `--nginx-config-invalid` — Fix only: jabali-default/jabali-panel.conf has `http2 on;` on nginx<1.25.1 (nginx -t fails, reloads rejected)
- `--nginx-missing-includes` — Fix only: panel :8443 vhost includes a missing optional snippet (phpMyAdmin/Adminer) — nginx -t fails, nothing on 8443 (GH #217)
- `--node-modules` — Fix only: panel-ui/node_modules partial (missing .bin/tsc)
- `--ondrej-nginx-ppa` — Fix only: stale ondrej/nginx PPA in apt sources (404 on noble)
- `--orphan-migration-staging` — Fix only: /var/lib/jabali-migrations/* dirs for jobs already terminal in DB
- `--orphan-slices` — Fix only: jabali-user-*.slice units exist for deleted unix users
- `--uploads-dir` — Fix only: /var/lib/jabali-uploads missing or wrong perms
- `--yes` — Skip interactive confirmation for destructive repairs

### `jabali retention-sweep`

Prune expired rows from unbounded log/report tables

```
jabali retention-sweep [flags]
```

**Flags:**

- `--dry-run` — count would-delete rows without removing them

### `jabali serve`

Start the Jabali Panel HTTP(S) server

```
jabali serve
```

### `jabali service`

Admin service control (start/stop/restart/reload/enable/disable)

```
jabali service
```

#### `jabali service action`

Run a service action on an allowlisted unit

```
jabali service action <name> <restart|start|stop|reload|enable|disable> [flags]
```

**Flags:**

- `--force` — confirm a disruptive action (stop/restart/disable)

#### `jabali service status`

Show service status details

```
jabali service status
```

### `jabali session`

Audit and revoke active login sessions (Kratos)

```
jabali session
```

#### `jabali session list`

List active login sessions (optionally filtered by --user email)

```
jabali session list [flags]
```

**Flags:**

- `--user` — only show sessions for this user email

#### `jabali session revoke`

Revoke a single login session

```
jabali session revoke <session-id>
```

#### `jabali session revoke-user`

Revoke ALL login sessions for a user (sign out everywhere)

```
jabali session revoke-user <email-or-id>
```

### `jabali settings`

Inspect and patch server settings (headless equivalent of /admin/settings)

```
jabali settings
```

#### `jabali settings get`

Print current server settings (JSON or key=value)

```
jabali settings get
```

#### `jabali settings keys`

List the settable keys for `settings set`

```
jabali settings keys
```

#### `jabali settings set`

Patch one or more server settings (same validation + side effects as the UI)

```
jabali settings set <key>=<value> [<key>=<value>...]
```

### `jabali shared-resource`

Manage shared mail resources — calendars, contacts, files (M52)

```
jabali shared-resource
```

#### `jabali shared-resource create`

Create a shared resource

```
jabali shared-resource create [flags]
```

**Flags:**

- `--display-name` — display name
- `--domain` — domain ID (ULID)
- `--kind` — mailbox|calendar|addressbook|files
- `--name` — host address local part

#### `jabali shared-resource grant`

Add or update a grant (upsert into the grant set)

```
jabali shared-resource grant [flags]
```

**Flags:**

- `--grantee` — grantee mailbox/group ID
- `--grantee-kind` — mailbox|group (default `mailbox`)
- `--resource` — shared resource ID
- `--rights` — read|readwrite|admin (default `read`)

#### `jabali shared-resource grants`

List grants on a shared resource

```
jabali shared-resource grants [flags]
```

**Flags:**

- `--resource` — shared resource ID

#### `jabali shared-resource list`

List shared resources in a domain

```
jabali shared-resource list [flags]
```

**Flags:**

- `--domain` — domain ID (ULID)

#### `jabali shared-resource remove`

Delete a shared resource (reconciler tears down the host principal)

```
jabali shared-resource remove [flags]
```

**Flags:**

- `--resource` — shared resource ID

#### `jabali shared-resource revoke`

Remove a grantee's grant from a shared resource

```
jabali shared-resource revoke [flags]
```

**Flags:**

- `--grantee` — grantee ID to revoke
- `--resource` — shared resource ID

### `jabali ssh-key`

Manage user SSH authorized keys

```
jabali ssh-key
```

#### `jabali ssh-key add`

Add an SSH public key for a user

```
jabali ssh-key add [flags]
```

**Flags:**

- `--name` — key label (required)
- `--pub-key` — raw public key (e.g. 'ssh-ed25519 AAAA... user@host')
- `--pub-key-file` — path to public key file
- `--pub-key-stdin` — read public key from stdin
- `--user` — user (id|email|username) (required)

#### `jabali ssh-key delete`

Delete an SSH key

```
jabali ssh-key delete <key-id> [flags]
```

**Flags:**

- `--force` — skip confirmation

#### `jabali ssh-key list`

List SSH keys (filtered by --user, or all without filter)

```
jabali ssh-key list [flags]
```

**Flags:**

- `--all` — list every user's keys (default when --user is omitted)
- `--user` — filter by user (id|email|username); empty = all

### `jabali ssl`

Manage Let's Encrypt SSL certificates

```
jabali ssl
```

#### `jabali ssl disable`

Disable SSL for a domain (reconciler will revoke cert)

```
jabali ssl disable <domain>
```

#### `jabali ssl enable`

Enable SSL for a domain and wait until the vhost serves the real certificate

```
jabali ssl enable <domain> [flags]
```

**Flags:**

- `--nginx-dir` — directory holding the enabled nginx vhosts (default `/etc/nginx/sites-enabled`)
- `--no-wait` — return as soon as the domain is marked, without waiting for the certificate
- `--wait-timeout` — how long to wait for the vhost to serve the real certificate (default `3m0s`)

#### `jabali ssl list`

List SSL certificates (optionally filtered by user)

```
jabali ssl list [flags]
```

**Flags:**

- `--user` — filter by user (id|email|username)

#### `jabali ssl readiness`

Report domains whose origin certificate would fail Cloudflare Full (strict)

```
jabali ssl readiness [flags]
```

**Flags:**

- `--all` — list every domain, not only the ones that would fail
- `--nginx-dir` — directory holding the enabled nginx vhosts (default `/etc/nginx/sites-enabled`)

#### `jabali ssl renew`

Renew SSL cert via certbot (synchronous, calls agent)

```
jabali ssl renew <domain> [flags]
```

**Flags:**

- `--force` — force renewal even if cert is not due

#### `jabali ssl set-custom`

Install an operator-supplied SSL cert + key (JAB-128)

```
jabali ssl set-custom [flags]
```

**Flags:**

- `--cert` — path to the certificate PEM file (leaf + chain) (required)
- `--domain` — domain name or id (required)
- `--key` — path to the private key PEM file (required)

#### `jabali ssl shared`

Manage shared wildcard/multi-SAN certificates (upload once, serve many domains)

```
jabali ssl shared
```

##### `jabali ssl shared attach`

Attach a domain to a shared certificate (must cover the domain)

```
jabali ssl shared attach --domain <name> --cert-id <id> [flags]
```

**Flags:**

- `--cert-id` — shared certificate id
- `--domain` — domain name

##### `jabali ssl shared delete`

Delete a shared certificate (fails if domains are still attached)

```
jabali ssl shared delete --id <id> [flags]
```

**Flags:**

- `--id` — shared certificate id

##### `jabali ssl shared detach`

Detach a domain from its shared certificate (reverts to LE)

```
jabali ssl shared detach --domain <name> [flags]
```

**Flags:**

- `--domain` — domain name

##### `jabali ssl shared list`

List shared certificates + their attached-domain counts

```
jabali ssl shared list
```

##### `jabali ssl shared upload`

Upload a server-wide shared certificate (agent validates + writes it)

```
jabali ssl shared upload --name <name> --cert <fullchain.pem> --key <privkey.pem> [flags]
```

**Flags:**

- `--cert` — path to fullchain PEM
- `--key` — path to private key PEM
- `--name` — human-readable name

### `jabali sso`

SSO (Single Sign-On) management commands

```
jabali sso
```

#### `jabali sso prune-tokens`

Manually purge expired SSO tokens

```
jabali sso prune-tokens
```

#### `jabali sso rotate-key`

Rotate the SSO encryption key

```
jabali sso rotate-key [flags]
```

**Flags:**

- `--current-key` — path to current encryption key (default `/etc/jabali/sso_key.txt`)
- `--new-key` — path to new encryption key (required)

### `jabali sso-reap`

Sweep stranded jabali-sso-<nonce>.php files (M22 reaper)

```
jabali sso-reap
```

### `jabali system`

System information and services

```
jabali system
```

#### `jabali system diagnostic`

Generate a redacted, encrypted diagnostic bundle for support handoff

```
jabali system diagnostic [flags]
```

**Flags:**

- `--json` — print the raw JSON result (for scripting)

#### `jabali system info`

Show system info (hostname, uptime, CPU, memory, disk)

```
jabali system info
```

#### `jabali system process`

List / kill host processes (admin)

```
jabali system process
```

##### `jabali system process kill`

Kill a process (TERM, or KILL with --force)

```
jabali system process kill <pid> [flags]
```

**Flags:**

- `--force` — confirm the kill (and use SIGKILL)

##### `jabali system process list`

List host processes

```
jabali system process list
```

#### `jabali system resolver`

Show / set system DNS resolvers

```
jabali system resolver
```

##### `jabali system resolver get`

Show current resolver source + addresses

```
jabali system resolver get
```

##### `jabali system resolver set`

Set resolver addresses (validated; same rules as the UI)

```
jabali system resolver set [flags]
```

**Flags:**

- `--addr` — resolver IP (repeatable, IPv4 or IPv6) (default `[]`)
- `--search-domain` — optional search domain

#### `jabali system restore`

Restore a system backup onto this host (CLI; ADR-0080)

```
jabali system restore [flags]
```

**Flags:**

- `--apply` — after staging, apply selected stages onto live host (default true) (default `true`)
- `--apply-stage` — restrict apply to named stages (repeatable). Empty = panel_db + panel_config + tls (the safe defaults) (default `[]`)
- `--credentials-ref` — absolute path to env file with backend creds (root:root 0600)
- `--force` — required — restore overwrites the running panel
- `--include-accounts` — also restore each linked account
- `--interactive` — force interactive prompts even when --remote-url is set
- `--password` — restic password (literal; overrides --password-file). Avoid in shell history; prefer --interactive
- `--password-file` — restic password file (default: /etc/jabali-panel/restic-repo.password)
- `--remote-url` — restic repo URL or local path (e.g. sftp:user@host:/path)
- `--sftp-auth` — SFTP auth mode: key | password (default: ssh config)
- `--sftp-key` — absolute path to the SSH private key for SFTP
- `--sftp-port` — SFTP port when not 22 (default `0`)
- `--snapshot` — system_manifest snapshot ID, or 'latest' to auto-pick newest

#### `jabali system services`

Show systemd service status

```
jabali system services
```

#### `jabali system ssh-keys`

System SSH keys for backup destinations (list/generate)

```
jabali system ssh-keys
```

##### `jabali system ssh-keys generate`

Generate a system SSH keypair (for sftp backup destinations)

```
jabali system ssh-keys generate [flags]
```

**Flags:**

- `--name` — key name (required)
- `--type` — key type (ed25519|rsa) (default `ed25519`)

##### `jabali system ssh-keys list`

List system SSH keys

```
jabali system ssh-keys list
```

### `jabali ufw`

UFW utilities (M43 — port baseline only; IP decisions live in CrowdSec)

```
jabali ufw
```

#### `jabali ufw default`

Set the default policy for a chain

```
jabali ufw default <incoming|outgoing> <allow|deny|reject> [flags]
```

**Flags:**

- `--force` — confirm a lockout-prone default policy

#### `jabali ufw disable`

Disable UFW (lockout risk)

```
jabali ufw disable [flags]
```

**Flags:**

- `--force` — confirm this destructive action

#### `jabali ufw enable`

Enable UFW

```
jabali ufw enable [flags]
```

**Flags:**

- `--force` — confirm this destructive action

#### `jabali ufw migrate-ip-bans`

Migrate UFW `from <IP>` deny rules to CrowdSec decisions (M43 Step 4)

```
jabali ufw migrate-ip-bans [flags]
```

**Flags:**

- `--dry-run` — Show what would migrate; make no changes
- `--no-cdn` — Confirm panel is not behind a CDN; bypasses the trusted_ips hard guard
- `--revert` — Restore UFW rules from snapshot and remove matching CrowdSec decisions
- `--yes` — Required for any destructive operation (migrate or revert)

#### `jabali ufw rule`

Add or delete UFW rules

```
jabali ufw rule
```

##### `jabali ufw rule add`

Add a UFW rule

```
jabali ufw rule add [flags]
```

**Flags:**

- `--action` — allow|deny|reject|limit (default `allow`)
- `--from` — source address/CIDR (optional)
- `--port` — port or app name (e.g. 443, OpenSSH) (required)
- `--proto` — tcp|udp (optional)

##### `jabali ufw rule delete`

Delete a UFW rule by its numbered position (see `ufw status`)

```
jabali ufw rule delete <num>
```

#### `jabali ufw status`

Show UFW status and numbered rules

```
jabali ufw status
```

### `jabali update`

Pull latest code, rebuild, migrate, and restart services

```
jabali update [flags]
```

**Flags:**

- `--force` — Run the full rebuild/restart cycle even when git pull found no new commits
- `--from-source` — Build binaries on this host instead of downloading the release tarball from Gitea Releases. Default is to download the tarball (90s update vs 5-10min source build). Use --from-source when offline, on a private fork, or to test uncommitted changes.

### `jabali user`

Manage panel users

```
jabali user
```

#### `jabali user 2fa-reset`

Strip TOTP + recovery codes from a user (CLI escape hatch when locked out)

```
jabali user 2fa-reset <email|username|user-id>
```

#### `jabali user create`

Create a new user (direct DB + Kratos; bypasses HTTP auth — M20-safe)

```
jabali user create [username] [flags]
```

**Flags:**

- `--admin` — grant admin role
- `--email` — user email (optional; a placeholder is synthesized from the username if omitted)
- `--name-first` — first name
- `--name-last` — last name
- `--password` — user password (required, min 10 chars)
- `--password-stdin` — read password from stdin (no prompt, no echo)
- `--username` — login username — the identifier users sign in with (required unless --email is given to derive one)

#### `jabali user delete`

Delete a user — destructive: domains, databases, mailboxes, OS account, /home, all related rows.

```
jabali user delete <email|username|user-id> [flags]
```

**Flags:**

- `--force` — skip confirmation prompt

#### `jabali user list`

List all users (direct DB — M20-safe)

```
jabali user list
```

#### `jabali user password`

Reset a user's password (auto-generates one if --password is omitted)

```
jabali user password <email|username|user-id> [flags]
```

**Flags:**

- `--expires-in` — TTL for recovery link (only with --link) (default `24h`)
- `--link` — emit a one-click recovery URL instead of setting the password directly
- `--password` — explicit new password (omit to auto-generate)
- `--password-stdin` — read new password from stdin (no prompt, no echo)

#### `jabali user reprovision`

Re-run OS provisioning for a user + sync the panel password

```
jabali user reprovision <email|username|user-id> [flags]
```

**Flags:**

- `--password` — new password (min 10 chars; omit to auto-generate)
- `--password-stdin` — read password from stdin (no prompt, no echo)

#### `jabali user suspend`

Suspend a user (same cascade as the admin GUI/API)

```
jabali user suspend <id> [flags]
```

**Flags:**

- `--reason` — operator-facing suspend reason (shown in the admin user list)

#### `jabali user unsuspend`

Unsuspend a user (reverse the cascade)

```
jabali user unsuspend <id>
```

### `jabali user-token`

Manage a user's API tokens (mint / list / revoke)

```
jabali user-token
```

#### `jabali user-token list`

List a user's API tokens (active + revoked)

```
jabali user-token list <user-email|username|id>
```

#### `jabali user-token mint`

Mint a new API token for a user (secret shown once)

```
jabali user-token mint <user-email|username|id> --name <name> [--scope read:dns ...] [flags]
```

**Flags:**

- `--expires-in` — token lifetime (e.g. 720h); 0 = no expiry; max 8760h (default `0s`)
- `--name` — token name (required)
- `--scope` — scope to grant (repeatable; empty = full owner access) (default `[]`)

#### `jabali user-token revoke`

Revoke a user's API token

```
jabali user-token revoke <token-id>
```

### `jabali version`

Print Jabali version, commit, and runtime info

```
jabali version
```

