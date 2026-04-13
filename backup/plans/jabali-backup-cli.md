# Blueprint: jabali-backup CLI Tool

> A standalone CLI tool that backs up Jabali hosting panel accounts using restic.
> Each account's data is collected, staged, and pushed to configurable remote repositories.

---

## Objective

Build `jabali-backup` — a Bash CLI tool that discovers Jabali hosting accounts from the panel database, collects all per-account data (files, databases, DNS, email, SSL, cron, PHP config), and backs them up to restic repositories on remote storage (SSH/SFTP, S3, Wasabi, Google Drive, Backblaze B2, etc.).

---

## Architecture Overview

```
jabali-backup (main entry point)
├── jabali-backup init          # Initialize restic repo + generate config
├── jabali-backup run [user]    # Run backup (all accounts or one)
├── jabali-backup restore       # Restore account from snapshot
├── jabali-backup list          # List snapshots / accounts
├── jabali-backup schedule      # Install/manage cron schedule
├── jabali-backup check         # Verify repo integrity
├── jabali-backup forget        # Apply retention policy
└── jabali-backup config        # Show/edit configuration

lib/
├── collectors/
│   ├── files.sh          # /home/{user}/ directory
│   ├── mysql.sh          # MySQL databases + user grants
│   ├── postgres.sh       # PostgreSQL databases + roles
│   ├── dns.sh            # PowerDNS zone export
│   ├── email.sh          # Stalwart mailboxes, forwards, autoresponders, DKIM
│   ├── ssl.sh            # SSL certificates + private keys
│   ├── cron.sh           # User crontab
│   ├── php.sh            # PHP-FPM pool config + version
│   ├── wordpress.sh      # wp-config.php, WP-CLI site export
│   ├── nginx.sh          # Per-domain nginx vhost configs
│   └── metadata.sh       # Account metadata from Jabali DB
├── restic.sh             # Restic wrapper (init, backup, restore, forget, check)
├── discover.sh           # Account discovery from Jabali MySQL
├── config.sh             # Config file parser (INI format)
├── logging.sh            # Structured logging
├── notify.sh             # Notification (email, webhook) on success/failure
├── jabali-decrypt.php    # PHP helper: decrypt Laravel-encrypted DB values
└── jabali-encrypt.php    # PHP helper: re-encrypt values for restore
```

### Data Flow

```
1. discover.sh → queries Jabali DB → list of accounts
2. For each account:
   a. Create staging dir: /tmp/jabali-backup/{username}/
   b. Run each collector → dumps into staging dir
   c. restic backup /home/{user} + staging dir → remote repo
   d. Clean up staging dir
3. Apply retention policy (restic forget --prune)
4. Send notification
```

---

## Jabali Data Model (from codebase analysis)

### Per-Account Data to Back Up

| Component | Source | Collector | Format |
|-----------|--------|-----------|--------|
| Home directory | `/home/{username}/` | files.sh | restic native (no staging) |
| MySQL databases | `mysql_credentials` table → `mysql_username` | mysql.sh | `.sql.gz` dumps |
| MySQL grants | `SHOW GRANTS FOR '{user}'@'%'` | mysql.sh | `.sql` |
| PostgreSQL DBs | pg databases owned by user | postgres.sh | `.sql.gz` dumps |
| PostgreSQL roles | `pg_dumpall --roles-only` filtered | postgres.sh | `.sql` |
| DNS records | `dns_records` table joined to `domains` | dns.sh | BIND zone files |
| Email domains | `email_domains` table (DKIM keys, catch-all) | email.sh | JSON |
| Mailboxes | `mailboxes` table (settings, quotas) | email.sh | JSON |
| Mailbox data | Stalwart mail storage dir | email.sh | restic native |
| Email forwards | `email_forwarders` table | email.sh | JSON |
| Autoresponders | `autoresponders` table | email.sh | JSON |
| Mailbox shares | `mailbox_shares` table (ACL permissions) | email.sh | JSON |
| IMAP sync tasks | `imap_sync_tasks` table | email.sh | JSON |
| SSL certs | `ssl_certificates` table | ssl.sh | PEM files |
| Cron jobs | `cron_jobs` table | cron.sh | JSON + crontab |
| PHP-FPM pools | `/etc/php/*/fpm/pool.d/{user}.conf` | php.sh | conf files |
| WordPress | wp-config, `wp db export`, `wp option list` | wordpress.sh | SQL + JSON |
| Nginx vhosts | `/etc/nginx/sites-enabled/{domain}*` | nginx.sh | conf files |
| Hotlink protection | `domain_hotlink_settings` table | nginx.sh | JSON |
| Domain aliases | `domain_aliases` table | metadata.sh | JSON |
| Domain redirects | `domain_redirects` table | metadata.sh | JSON |
| Hosting package | `hosting_packages` + `users` table | metadata.sh | JSON |
| SSH keys | `/home/{user}/.ssh/authorized_keys` | files.sh | included in home |
| Git deployments | `git_deployments` table (incl. secret tokens) | metadata.sh | JSON |
| User settings | `user_settings` table | metadata.sh | JSON |
| Cloudflare zones | `cloudflare_zones` table (zone IDs, API tokens) | metadata.sh | JSON |
| Webhooks | `webhook_endpoints` table (URLs, events, secrets) | metadata.sh | JSON |
| Bandwidth history | `domain_bandwidth_usage` table (optional) | metadata.sh | JSON |

### Key Jabali DB Tables (for discovery queries)

```sql
-- Account list
SELECT id, username, home_directory, hosting_package_id, is_active FROM users;

-- Domains per user
SELECT id, domain, document_root, ssl_enabled, custom_nginx_directives FROM domains WHERE user_id = ?;

-- MySQL credentials per user
SELECT mysql_username, mysql_password_encrypted FROM mysql_credentials WHERE user_id = ?;

-- Email domains per user's domains
SELECT ed.* FROM email_domains ed JOIN domains d ON ed.domain_id = d.id WHERE d.user_id = ?;

-- Mailboxes
SELECT m.* FROM mailboxes m JOIN email_domains ed ON m.email_domain_id = ed.id JOIN domains d ON ed.domain_id = d.id WHERE d.user_id = ?;

-- DNS records
SELECT dr.* FROM dns_records dr JOIN domains d ON dr.domain_id = d.id WHERE d.user_id = ?;

-- SSL certificates
SELECT sc.* FROM ssl_certificates sc JOIN domains d ON sc.domain_id = d.id WHERE d.user_id = ?;
```

---

## Encrypted Column Strategy

Jabali encrypts sensitive fields using Laravel's `Crypt::encryptString()` (AES-256-CBC with APP_KEY).
These fields **cannot be decrypted in pure Bash**. Strategy:

**Encrypted fields in the DB**:
- `ssl_certificates.private_key`
- `email_domains.dkim_private_key`
- `mysql_credentials.mysql_password_encrypted`
- `mailboxes.password_encrypted`
- `imap_sync_tasks.source_password_encrypted`
- `cloudflare_zones.api_token`
- `git_deployments.secret_token`
- `webhook_endpoints.secret_token`

**Approach**: Ship a small PHP helper script (`lib/jabali-decrypt.php`) that uses Jabali's own
APP_KEY to decrypt values. The backup tool calls it via `php lib/jabali-decrypt.php <encrypted_value>`.
This avoids reimplementing Laravel crypto in Bash.

- APP_KEY is read from `/etc/jabali-backup/app-key` (copied from Jabali's `.env`)
- The PHP helper is ~20 lines, stateless, reads APP_KEY from env
- For restore: a matching `lib/jabali-encrypt.php` re-encrypts values with the target APP_KEY
- If APP_KEY is unavailable: export encrypted values AS-IS with a marker, and restore requires
  manual re-encryption or providing the original key

---

## Configuration File

**Location**: `/etc/jabali-backup/config.conf` (fallback: `~/.jabali-backup.conf`)

> **Note**: Uses shell-compatible INI format (not TOML) for reliable parsing in Bash.
> Sections use `[section]` headers, values use `key=value` with no quoting ambiguity.

```ini
[jabali]
db_host=localhost
db_name=jabali
db_user=jabali_backup
db_password_file=/etc/jabali-backup/db-password
app_key_file=/etc/jabali-backup/app-key
jabali_path=/var/www/jabali

[restic]
password_file=/etc/jabali-backup/restic-password
cache_dir=/var/cache/jabali-backup/restic

[repository]
# sftp | s3 | b2 | gcs | azure | rest | local | rclone
type=sftp
path=sftp:backup-server:/backups/jabali
# For S3/Wasabi:
# type=s3
# path=s3:s3.wasabisys.com/jabali-backups
# aws_access_key_id_file=/etc/jabali-backup/s3-key
# aws_secret_access_key_file=/etc/jabali-backup/s3-secret

[repository_copy]
# Optional secondary repo for 3-2-1 rule
# type=s3
# path=s3:s3.amazonaws.com/jabali-backups-offsite

[retention]
keep_last=7
keep_daily=30
keep_weekly=12
keep_monthly=24

[staging]
dir=/tmp/jabali-backup
cleanup=true

[notifications]
on_failure=true
on_success=false
email_to=
webhook_url=

[paths]
stalwart_data=/opt/stalwart-mail/data
php_fpm_pools=/etc/php/*/fpm/pool.d
nginx_sites=/etc/nginx/sites-enabled

[logging]
file=/var/log/jabali-backup.log
level=info
```

---

## Steps

### Step 0: Project Scaffolding

**Branch**: `step-0-scaffolding`
**Depends on**: nothing
**Model tier**: default
**Parallel with**: nothing

**Context**: Create the project structure, install script, and make the tool installable on Jabali servers.

**Tasks**:
1. Create directory structure: `bin/`, `lib/collectors/`, `etc/`, `completions/`
2. Create `bin/jabali-backup` — main entry point with subcommand routing
3. Create `lib/config.sh` — INI config parser (shell-compatible, grep/awk based)
4. Create `lib/logging.sh` — structured logging with timestamps and levels
5. Create `etc/config.toml.example` — example configuration file
6. Create `install.sh` — copies bin to `/usr/local/bin/`, lib to `/usr/local/lib/jabali-backup/`, etc to `/etc/jabali-backup/`
7. Create `uninstall.sh`
8. Add bash-completion script in `completions/jabali-backup.bash`

**Verification**:
```bash
shellcheck bin/jabali-backup lib/*.sh
./bin/jabali-backup --help  # shows usage
./bin/jabali-backup version # shows version
```

**Exit criteria**: `jabali-backup --help` runs and shows all subcommands. All scripts pass shellcheck.

---

### Step 1: Configuration & Account Discovery

**Branch**: `step-1-config-discovery`
**Depends on**: Step 0
**Model tier**: default
**Parallel with**: nothing

**Context**: Parse the config file, connect to Jabali's MySQL database, and discover all accounts with their associated resources.

**Tasks**:
1. Implement `lib/config.sh` — parse TOML config, resolve `*_file` references (read secrets from files, never store in config)
2. Implement `lib/discover.sh`:
   - `discover_accounts()` — list all active users with home dirs
   - `discover_domains(user_id)` — domains for a user
   - `discover_mysql_credentials(user_id)` — MySQL users for account
   - `discover_email_domains(user_id)` — email domains + mailboxes
   - `discover_dns_records(user_id)` — DNS records per domain
   - `discover_ssl_certs(user_id)` — SSL certificates per domain
   - `discover_cron_jobs(user_id)` — cron jobs
   - `discover_email_forwarders(user_id)` — email forwards
   - `discover_autoresponders(user_id)` — autoresponders
3. Implement `jabali-backup config` subcommand — validate config, test DB connection
4. Create read-only MySQL user SQL script: `etc/create-backup-user.sql`

**Verification**:
```bash
jabali-backup config test     # tests DB connection
jabali-backup list accounts   # lists all accounts from DB
jabali-backup list domains --user=testuser  # lists domains
```

**Exit criteria**: `jabali-backup list accounts` outputs a table of Jabali users. `jabali-backup config test` validates DB connectivity and all required config fields.

---

### Step 2: Restic Repository Management

**Branch**: `step-2-restic-wrapper`
**Depends on**: Step 0
**Model tier**: default
**Parallel with**: Step 1

**Context**: Wrap restic operations with proper environment setup, error handling, and multi-backend support.

**Tasks**:
1. Implement `lib/restic.sh`:
   - `restic_init()` — initialize repo with proper env vars per backend type
   - `restic_backup(paths[], tags[], exclude[])` — run backup with tagging
   - `restic_restore(snapshot_id, target_dir)` — restore to directory
   - `restic_snapshots(tags[])` — list snapshots filtered by tags
   - `restic_forget(retention_policy)` — apply retention + prune
   - `restic_check()` — verify repo integrity
   - `restic_copy(source_repo, dest_repo)` — copy to secondary repo
2. Backend-specific env setup:
   - SFTP/SSH: `RESTIC_REPOSITORY=sftp:user@host:/path`
   - S3/Wasabi: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `RESTIC_REPOSITORY=s3:endpoint/bucket`
   - B2: `B2_ACCOUNT_ID`, `B2_ACCOUNT_KEY`
   - GCS: `GOOGLE_APPLICATION_CREDENTIALS`
   - REST: `RESTIC_REPOSITORY=rest:https://user:pass@host/`
   - Local: `RESTIC_REPOSITORY=/path/to/repo`
   - rclone (Google Drive, etc.): `RESTIC_REPOSITORY=rclone:remote:path`
3. Implement `jabali-backup init` — initialize restic repo
4. Implement `jabali-backup check` — run `restic check`
5. Implement `jabali-backup forget` — apply retention from config

**Verification**:
```bash
jabali-backup init             # initializes repo
jabali-backup check            # checks repo integrity
restic -r <repo> snapshots     # confirms repo works
```

**Exit criteria**: `jabali-backup init` creates a restic repo on the configured backend. `jabali-backup check` succeeds.

---

### Step 3: File & Home Directory Collector

**Branch**: `step-3-file-collector`
**Depends on**: Step 2
**Model tier**: default
**Parallel with**: Steps 4, 5, 6, 7, 8, 9

**Context**: Back up the user's home directory, which contains website files, application code, uploads, and SSH keys. This is the largest component — restic backs it up directly (no staging) with deduplication.

**Tasks**:
1. Implement `lib/collectors/files.sh`:
   - `collect_files(username, home_dir)` — backs up `/home/{username}/` directly via restic
   - Exclude patterns: `*.log`, `cache/`, `tmp/`, `.cache/`, `node_modules/`, `vendor/` (configurable)
   - Tag snapshots: `--tag account:{username} --tag type:files`
2. Handle missing/empty home dirs gracefully
3. Respect file permissions (run as root, preserve ownership)

**Verification**:
```bash
jabali-backup run testuser --only=files
restic -r <repo> snapshots --tag type:files  # shows snapshot
```

**Exit criteria**: Home directory snapshot exists in restic repo with correct tags. Excludes are applied.

---

### Step 4: MySQL Collector

**Branch**: `step-4-mysql-collector`
**Depends on**: Step 1
**Model tier**: default
**Parallel with**: Steps 3, 5, 6, 7, 8, 9

**Context**: Each Jabali user has a MySQL username (from `mysql_credentials` table). Discover all databases owned by that MySQL user, dump them, and export grants.

**Tasks**:
1. Implement `lib/collectors/mysql.sh`:
   - `collect_mysql(user_id, staging_dir)`:
     - Query `mysql_credentials` for the user's MySQL username
     - List databases: `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA` where owner matches
     - For each database: `mysqldump --single-transaction --routines --triggers --events {db} | gzip > staging/mysql/{db}.sql.gz`
     - Discover all hosts: `SELECT DISTINCT Host FROM mysql.user WHERE User='{mysql_user}'`
     - Export grants for ALL hosts: `SHOW GRANTS FOR '{mysql_user}'@'{host}'` for each host → `staging/mysql/grants.sql`
     - Export user record: `SELECT * FROM mysql.user WHERE User='{mysql_user}'` → `staging/mysql/user-record.sql`
2. Handle: user has no MySQL credentials, empty databases, locked databases
3. Skip system databases (information_schema, performance_schema, mysql, sys)

**Verification**:
```bash
jabali-backup run testuser --only=mysql
ls /tmp/jabali-backup/testuser/mysql/  # shows .sql.gz files
```

**Exit criteria**: All user databases are dumped to staging. Grants are exported. Empty/missing credentials are handled without error.

---

### Step 5: PostgreSQL Collector

**Branch**: `step-5-postgres-collector`
**Depends on**: Step 1
**Model tier**: default
**Parallel with**: Steps 3, 4, 6, 7, 8, 9

**Context**: Some Jabali users may have PostgreSQL databases. Discover PG databases and roles owned by the user.

**Tasks**:
1. Implement `lib/collectors/postgres.sh`:
   - `collect_postgres(username, staging_dir)`:
     - List databases owned by user: `SELECT datname FROM pg_database JOIN pg_authid ON datdba = oid WHERE rolname = '{username}'`
     - For each database: `pg_dump -Fc {db} > staging/postgres/{db}.dump`
     - Export full role definition: `pg_dumpall --roles-only` filtered to user's role → `staging/postgres/role.sql`
     - Export role memberships: `SELECT * FROM pg_auth_members` for user's role
     - Export per-database extension list: `SELECT extname FROM pg_extension` → `staging/postgres/{db}.extensions`
     - Export per-database schema-only dump: `pg_dump --schema-only {db}` → `staging/postgres/{db}.schema.sql`
2. Handle: PostgreSQL not installed, user has no PG databases
3. Use `-Fc` (custom format) for efficient restore with `pg_restore`

**Verification**:
```bash
jabali-backup run testuser --only=postgres
```

**Exit criteria**: PostgreSQL databases dumped if they exist. Graceful skip if PG is not installed or user has no databases.

---

### Step 6: DNS Collector

**Branch**: `step-6-dns-collector`
**Depends on**: Step 1
**Model tier**: default
**Parallel with**: Steps 3, 4, 5, 7, 8, 9

**Context**: DNS records are stored in Jabali's `dns_records` table linked to domains. Export as BIND zone files for portability.

**Tasks**:
1. Implement `lib/collectors/dns.sh`:
   - `collect_dns(user_id, staging_dir)`:
     - Query all domains for user
     - For each domain, query `dns_records` (name, type, content, ttl, priority)
     - Generate BIND-format zone file: `staging/dns/{domain}.zone`
     - Also export raw JSON for lossless restore: `staging/dns/{domain}.json`
2. Handle SOA record generation (serial, refresh, retry, expire, minimum)
3. Handle all record types: A, AAAA, CNAME, MX, TXT, NS, SRV, CAA

**Verification**:
```bash
jabali-backup run testuser --only=dns
cat /tmp/jabali-backup/testuser/dns/example.com.zone  # valid zone file
```

**Exit criteria**: BIND zone files are generated for all user domains. Zone files are syntactically valid.

---

### Step 7: Email Collector

**Branch**: `step-7-email-collector`
**Depends on**: Step 1
**Model tier**: strongest (most complex collector)
**Parallel with**: Steps 3, 4, 5, 6, 8, 9

**Context**: Email is the most complex component. Must export Stalwart mailbox data, email domain config (DKIM), forwarders, autoresponders, and actual mail storage.

**Tasks**:
1. Implement `lib/collectors/email.sh`:
   - `collect_email(user_id, staging_dir)`:
     - **Email domains**: Query `email_domains` → decrypt DKIM keys via `jabali-decrypt.php`, export catch-all config, quotas → `staging/email/domains/{domain}.json`
     - **Mailboxes**: Query `mailboxes` → export config (local_part, quota, imap/pop3/smtp enabled, system_uid, system_gid) → `staging/email/mailboxes/{email}.json`
     - **Mailbox data**: Read `maildir_path` from each Mailbox record → resolve to `{stalwart_data}/{maildir_path}` → add to restic backup paths
     - **Mailbox shares**: Query `mailbox_shares` → export ACL (mailbox_id, shared_with, folder, acl_rights) → `staging/email/shares.json`
     - **Forwarders**: Query `email_forwarders` → `staging/email/forwarders.json`
     - **Autoresponders**: Query `autoresponders` → `staging/email/autoresponders.json`
     - **IMAP sync tasks**: Query `imap_sync_tasks` → export config (source_host, folders, status; encrypt passwords) → `staging/email/imap-sync.json`
     - **Mailing lists**: If present, export list configuration
2. DKIM private keys are decrypted via `lib/jabali-decrypt.php` using APP_KEY, then stored as PEM in backup
3. Do NOT export plaintext passwords — export password hashes only
4. Mailbox storage paths are read from the DB `maildir_path` field (not hardcoded)

**Verification**:
```bash
jabali-backup run testuser --only=email
ls /tmp/jabali-backup/testuser/email/  # domains/, mailboxes/, forwarders.json
```

**Exit criteria**: All email configuration exported. Mail storage directories identified and backed up. No plaintext passwords in exports.

---

### Step 8: SSL & Nginx Collector

**Branch**: `step-8-ssl-nginx-collector`
**Depends on**: Step 1
**Model tier**: default
**Parallel with**: Steps 3, 4, 5, 6, 7, 9

**Context**: SSL certificates (cert, key, CA bundle) and nginx vhost configurations per domain.

**Tasks**:
1. Implement `lib/collectors/ssl.sh`:
   - `collect_ssl(user_id, staging_dir)`:
     - Query `ssl_certificates` → for each: export cert, CA bundle → `staging/ssl/{domain}/`
     - Private keys are encrypted in DB — decrypt with APP_KEY or note encrypted status
     - Export metadata: issuer, type, expiry, auto_renew status
2. Implement `lib/collectors/nginx.sh`:
   - `collect_nginx(user_id, staging_dir)`:
     - For each domain: copy nginx config from `/etc/nginx/sites-enabled/{domain}*`
     - Export custom nginx directives from `domains.custom_nginx_directives`
     - Export hotlink protection: query `domain_hotlink_settings` → `staging/nginx/{domain}.hotlink.json`
     - → `staging/nginx/{domain}.conf`

**Verification**:
```bash
jabali-backup run testuser --only=ssl,nginx
ls /tmp/jabali-backup/testuser/ssl/
ls /tmp/jabali-backup/testuser/nginx/
```

**Exit criteria**: SSL certs and nginx configs exported for all user domains.

---

### Step 9: PHP, WordPress, Cron & Metadata Collectors

**Branch**: `step-9-remaining-collectors`
**Depends on**: Step 1
**Model tier**: default
**Parallel with**: Steps 3, 4, 5, 6, 7, 8

**Context**: Remaining smaller collectors for PHP-FPM config, WordPress settings, cron jobs, and account metadata.

**Tasks**:
1. Implement `lib/collectors/php.sh`:
   - Copy PHP-FPM pool config: `/etc/php/*/fpm/pool.d/{username}.conf` → `staging/php/`
   - Detect PHP version in use
2. Implement `lib/collectors/wordpress.sh`:
   - Scan home dir for `wp-config.php` files
   - For each WP install: `wp db export` (if wp-cli available), `wp option list --format=json`
   - Export WP version, active plugins/themes list
   - → `staging/wordpress/{domain}/`
3. Implement `lib/collectors/cron.sh`:
   - Query `cron_jobs` table → `staging/cron/jobs.json`
   - Also dump system crontab: `crontab -u {username} -l` → `staging/cron/crontab`
4. Implement `lib/collectors/metadata.sh`:
   - Export from Jabali DB:
     - User record (id, username, email, package, quotas, settings)
     - Domain list with all config
     - Domain aliases
     - Domain redirects
     - Git deployments config (incl. decrypted secret tokens via `jabali-decrypt.php`)
     - Hosting package details
     - Cloudflare zones (zone_id, account_id; decrypt API tokens via helper)
     - Webhook endpoints (url, events, is_active; decrypt secret tokens)
     - User settings from `user_settings` table
     - Bandwidth usage history from `domain_bandwidth_usage` (optional, enabled by default)
   - → `staging/metadata/account.json`
   - Include backup manifest: timestamp, jabali version, backup tool version, collector versions

**Verification**:
```bash
jabali-backup run testuser --only=php,wordpress,cron,metadata
cat /tmp/jabali-backup/testuser/metadata/account.json | jq .
```

**Exit criteria**: All remaining collectors produce valid output. WordPress detection works for multi-site home dirs.

---

### Step 10: Backup Orchestrator (`jabali-backup run`)

**Branch**: `step-10-orchestrator`
**Depends on**: Steps 1–9
**Model tier**: strongest
**Parallel with**: nothing

**Context**: Wire everything together. The `run` subcommand discovers accounts, runs all collectors, stages data, calls restic, and cleans up.

**Tasks**:
1. Implement the main `run` subcommand in `bin/jabali-backup`:
   - `jabali-backup run` — backup ALL accounts
   - `jabali-backup run {username}` — backup single account
   - `jabali-backup run --only=files,mysql,dns` — selective collectors
   - `jabali-backup run --exclude=wordpress` — skip certain collectors
   - `jabali-backup run --dry-run` — show what would be backed up
2. Per-account backup flow:
   ```
   a. Create staging dir
   b. Run each collector (respecting --only/--exclude)
   c. restic backup:
      - Direct paths: /home/{user}/, mail storage dirs
      - Staging dir: /tmp/jabali-backup/{user}/
      - Tags: account:{user}, type:full, date:{YYYY-MM-DD}
   d. Clean up staging dir
   e. Log result (success/failure, duration, size)
   ```
3. Global post-backup:
   - Apply retention policy (`restic forget --prune`)
   - Copy to secondary repo if configured
   - Send notification
4. Implement locking — prevent concurrent backups of same account
5. Implement `--parallel=N` — backup N accounts concurrently

**Verification**:
```bash
jabali-backup run --dry-run                    # shows plan
jabali-backup run testuser                     # full backup
jabali-backup list snapshots --user=testuser   # shows snapshot
jabali-backup run                              # all accounts
```

**Exit criteria**: Full backup of a test account completes. Snapshot visible in restic. Retention policy applied. Notifications sent.

---

### Step 11: Restore Functionality

**Branch**: `step-11-restore`
**Depends on**: Step 10
**Model tier**: strongest
**Parallel with**: nothing

**Context**: Restore an account from a restic snapshot. This is critical for disaster recovery and account migration.

**Tasks**:
1. Implement `jabali-backup restore`:
   - `jabali-backup restore {username} --snapshot=latest` — restore latest
   - `jabali-backup restore {username} --snapshot={id}` — restore specific
   - `jabali-backup restore {username} --only=files,mysql` — selective
   - `jabali-backup restore {username} --target=/tmp/restore/` — restore to alternate location (for inspection)
2. Restore logic per component:
   - **Files**: `restic restore` to `/home/{username}/` (or target), preserve ownership
   - **MySQL**: `gunzip | mysql` for each database, replay grants for all hosts
   - **PostgreSQL**: recreate role, `pg_restore` for each database, reinstall extensions
   - **DNS**: Import JSON records back to Jabali DB (insert into `dns_records` via SQL)
   - **Email**:
     a. Re-import email domain config (DKIM keys re-encrypted via `jabali-encrypt.php`)
     b. Re-import mailbox records (skip password — user must reset)
     c. Restore Stalwart mail storage to target paths
     d. Fix ownership: `chown -R {system_uid}:{system_gid}` on restored mail dirs
     e. Re-apply mailbox shares (ACL)
     f. Re-import forwarders, autoresponders, IMAP sync tasks
   - **SSL**: Re-import certificates to DB (private keys re-encrypted via helper)
   - **Cron**: Re-import cron jobs to DB + write system crontab
   - **Nginx/PHP**: Copy configs back to system paths, reload nginx/php-fpm
   - **Metadata**: Restore account record, domains, aliases, redirects, webhooks, Cloudflare zones
3. Safety: `--dry-run` flag shows what would be restored without executing
4. Conflict handling per component:
   - **User exists**: Skip (default) or `--force` overwrites user record preserving ID
   - **Domain exists**: Skip (default) or `--force` overwrites domain config
   - **Database exists**: Create with `_restored` suffix (default) or `--force` drops and recreates
   - **Mailbox exists**: Skip (default) or `--force` overwrites config + mail storage
   - **DNS records exist**: Merge (add missing, skip duplicates) or `--force` replaces all
5. Restore order (respects foreign keys):
   a. User + hosting package → b. Domains → c. DNS, SSL, nginx → d. Email domains → e. Mailboxes → f. Databases → g. Files

**Verification**:
```bash
jabali-backup restore testuser --snapshot=latest --dry-run
jabali-backup restore testuser --snapshot=latest --target=/tmp/restore-test/
ls /tmp/restore-test/  # verify contents
```

**Exit criteria**: Account can be fully restored from backup. Selective restore works. Dry-run shows accurate plan.

---

### Step 12: Scheduling & Notifications

**Branch**: `step-12-schedule-notify`
**Depends on**: Step 10
**Model tier**: default
**Parallel with**: Step 11

**Context**: Set up automatic backup schedules via cron and notification on success/failure.

**Tasks**:
1. Implement `jabali-backup schedule`:
   - `jabali-backup schedule install` — installs cron entry (default: daily at 2am)
   - `jabali-backup schedule remove` — removes cron entry
   - `jabali-backup schedule show` — shows current schedule
   - `jabali-backup schedule --cron="0 2 * * *"` — custom schedule
2. Implement `lib/notify.sh`:
   - Email notification via `mail` or `sendmail`
   - Webhook notification (POST JSON to URL)
   - Notification payload: accounts backed up, duration, size, errors
   - Configurable: on_failure (default: true), on_success (default: false)
3. Add systemd timer as alternative to cron: `etc/jabali-backup.timer` + `jabali-backup.service`

**Verification**:
```bash
jabali-backup schedule install
crontab -l | grep jabali-backup  # shows entry
jabali-backup schedule show
```

**Exit criteria**: Cron entry installed. Notifications sent on backup completion (if configured). Systemd timer files generated.

---

### Step 13: Documentation & Testing

**Branch**: `step-13-docs-testing`
**Depends on**: Steps 10, 11, 12
**Model tier**: default
**Parallel with**: nothing

**Tasks**:
1. Create `README.md`:
   - Installation instructions
   - Quick start guide
   - Configuration reference
   - All subcommands with examples
   - Supported backends with setup instructions
   - Restore procedures
   - Troubleshooting guide
2. Create integration test suite (Bash-based):
   - Test config parsing
   - Test account discovery (with mock DB)
   - Test each collector in isolation
   - Test restic wrapper with local repo
   - Test full backup + restore cycle
3. Add `jabali-backup doctor` — checks all dependencies (restic, mysql, pg_dump, wp-cli, etc.)

**Verification**:
```bash
jabali-backup doctor                  # all green
./tests/run-all.sh                    # all tests pass
jabali-backup run testuser && jabali-backup restore testuser --target=/tmp/test-restore/
diff -r /home/testuser/ /tmp/test-restore/home/testuser/  # files match
```

**Exit criteria**: Doctor reports all dependencies satisfied. Test suite passes. Full backup-restore cycle verified on test account.

---

## Dependency Graph

```
Step 0 (scaffolding)
├── Step 1 (config + discovery)
│   ├── Step 4 (mysql) ─────────┐
│   ├── Step 5 (postgres) ──────┤
│   ├── Step 6 (dns) ───────────┤
│   ├── Step 7 (email) ─────────┤ All parallel
│   ├── Step 8 (ssl + nginx) ───┤
│   └── Step 9 (php/wp/cron) ───┘
│                                │
└── Step 2 (restic wrapper) ─────┤
    └── Step 3 (files) ─────────┘
                                 │
                          Step 10 (orchestrator)
                          ├── Step 11 (restore)
                          └── Step 12 (schedule + notify)
                                 │
                          Step 13 (docs + testing)
```

**Maximum parallelism**: Steps 3–9 can all run in parallel (7 concurrent steps).

---

## Invariants (verified after every step)

1. All `.sh` files pass `shellcheck` with zero warnings
2. `jabali-backup --help` works at every step
3. No secrets in source code — all credentials read from files at runtime
4. All SQL queries use parameterized values (no injection via username/domain)
5. Staging directory is always cleaned up, even on failure (trap EXIT)
6. Restic password never appears in logs or process listing

---

## Technology Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Language | Bash + PHP helper | Bash for orchestration (native to servers), PHP helper for Laravel encryption/decryption |
| Backup engine | restic | Deduplication, encryption, multi-backend, incremental, well-maintained |
| Config format | INI (shell-compatible) | Reliably parseable with grep/awk in Bash, no external deps |
| DB dumps | mysqldump / pg_dump | Standard, reliable, widely understood |
| DNS export | BIND zone + JSON | Zone files for portability, JSON for lossless restore to Jabali |
| Email/metadata export | JSON | Structured, queryable, easy to restore programmatically |
| Scheduling | cron + systemd timer | Standard Linux scheduling, no external dependencies |
| Encryption bridge | PHP helper script | Reuses Laravel's Crypt class for encrypted column handling |

---

## Error Handling

### Collector Failure Modes

| Failure | Severity | Action |
|---------|----------|--------|
| Collector throws error | Soft | Log warning, skip collector, continue backup |
| mysqldump fails | Soft | Log error, skip that database, continue |
| restic backup fails | Hard | Abort account backup, log error, continue next account |
| restic repo unreachable | Hard | Abort entire backup run, send failure notification |
| Staging dir out of space | Hard | Abort, clean up, send notification |
| APP_KEY unavailable | Soft | Export encrypted values with `_ENCRYPTED` marker |

### Partial Failure Recovery

- Each account backup is independent — one account failure does not stop others
- Staging dir cleanup uses `trap EXIT` — always runs on any exit signal
- Lock files use `flock` with timeout — stale locks auto-expire
- Backup manifest records which collectors succeeded/failed per account
- `jabali-backup run --resume` skips accounts with successful snapshots from today

### Disk Space Pre-check

Before backup: check available space in staging dir >= 2x largest account's DB dumps (estimated).
Warn if < 5GB free. Abort if < 1GB free.

---

## Rollback Strategy

Each step is a separate branch. If a step introduces a regression:
1. `git revert` the merge commit
2. Fix on the step branch
3. Re-merge

The tool itself is side-effect-free until `jabali-backup run` is called — development never touches production data.

---

## Security Considerations

- **DB credentials**: Read from files (`db_password_file`), never from config directly
- **Restic password**: Read from file, never in env vars visible via `/proc`
- **Encrypted columns**: Decrypted via `jabali-decrypt.php` using APP_KEY from secure file; on restore, re-encrypted via `jabali-encrypt.php`
- **APP_KEY protection**: Stored in `/etc/jabali-backup/app-key` with mode 0600, owned by root
- **Staging cleanup**: `trap` ensures staging dirs are removed on any exit
- **No passwords exported**: Email/MySQL passwords are NOT exported in plaintext — only hashes or encrypted form
- **File permissions**: Backup runs as root; staging dir permissions set to 0700
- **Repo encryption**: restic encrypts all data at rest by default
