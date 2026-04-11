# Architecture

## Directory Structure

```
jabali-backup/
  bin/
    jabali-backup             Main CLI entry point (Bash)
  lib/
    collectors/               Backup: gather data from live system
      files.sh                /home/{user}/ directory
      mysql.sh                MySQL databases + grants + CREATE USER
      postgres.sh             PostgreSQL databases + roles
      dns.sh                  PowerDNS zone export
      email.sh                Mailboxes, forwards, DKIM, password hashes
      ssl.sh                  SSL certificates + private keys
      cron.sh                 Cron jobs (DB + system)
      php.sh                  PHP-FPM pool config
      wordpress.sh            WP-CLI exports
      nginx.sh                Nginx vhosts + hotlink rules + cache zones
      redis.sh                Redis ACL user credentials
      stalwart.sh             Stalwart JMAP export (emails, Sieve, identities)
      metadata.sh             Account metadata from Jabali DB
      server.sh               Full server: databases, configs, systemd, SSL, packages
    restorers/                Restore: import data back to live system
      metadata.sh             User, domains, aliases (runs first for FK deps)
      files.sh                Home directory via rsync, fixes ownership + ACLs
      dns.sh                  DNS records back to Jabali DB
      ssl.sh                  SSL certs back to DB (re-encrypts private keys)
      email.sh                Email domains, mailboxes, forwarders, autoresponders
      php.sh                  FPM pool configs to pool.d, reloads php-fpm
      nginx.sh                Vhost configs, fixes FPM socket path, reloads nginx
      mysql.sh                Database dumps, CREATE USER from backup, password sync
      postgres.sh             PostgreSQL restore via pg_restore
      cron.sh                 Cron jobs to DB + system crontab
      redis.sh                Redis ACL SETUSER + ACL SAVE
      stalwart.sh             Stalwart JMAP import via CLI or REST
      server.sh               5-phase disaster recovery restore
    restic.sh                 Restic wrapper (multi-backend)
    discover.sh               Account discovery + DB helpers
    config.sh                 INI config parser
    logging.sh                Structured logging
    notify.sh                 Email + webhook notifications
    jabali-decrypt.php        Decrypt Laravel-encrypted DB values
    jabali-encrypt.php        Re-encrypt values for restore
  panel/                      Panel addon source files
    agent/
      jabali-backup.php       Agent RPC routes (installed to /etc/jabali/agent.d/)
    filament/pages/
      Backups.php             Admin backup page
      SnapshotBrowser.php     Admin snapshot file browser
      UserBackups.php         User backup page
    backup/
      BackupServiceProvider.php    Laravel service provider
      Adapters/
        ResticSnapshotAdapter.php  Restic snapshot data adapter
    views/                    Blade templates for admin and user pages
    public/
      backup-download.php     Streaming download endpoint
  etc/
    config.conf.example       Example configuration
  docs/                       Documentation
  plans/                      Implementation blueprints
  install.sh                  Unified installer (install/update/uninstall + panel)
  install-panel.sh            Standalone panel installer (backward compat)
  uninstall-panel.sh          Standalone panel uninstaller (backward compat)
```

## Backup Data Flow

```
jabali-backup run [username]

  1. discover.sh --> Jabali MySQL --> account list
  2. For each account:
     - collectors/ run in parallel, write to staging dir
     - files.sh passes /home/{user}/ directly to restic (no staging)
  3. restic backup: staging dir + home dir --> remote repository
  4. Clean up staging dir
  5. restic forget --prune (retention policy)
  6. Send notification

## MySQL Discovery Flow

Collector discovers databases by examining multiple sources:

  1. mysql_credentials table (e.g., "shuki_admin" user)
     - Query: SELECT mysql_username FROM mysql_credentials WHERE user_id = ?
  2. Username-based prefix discovery
     - Query: SELECT SCHEMA_NAME FROM information_schema.SCHEMATA 
              WHERE SCHEMA_NAME LIKE 'username_%' OR SCHEMA_NAME LIKE 'mysql_user_%'
  3. Also discovers associated MySQL users by username prefix
     - Query: SELECT DISTINCT User FROM mysql.user WHERE User LIKE 'username%'

This ensures databases created outside the credentials table are still backed up.
Exports both databases (as .sql.gz) and grants (as grants.sql) for each MySQL user.
```

## Restore Data Flow

```
jabali-backup restore <username> --snapshot=<id>

  1. restic restore --> extract snapshot to temp dir
  2. Read metadata/account.json for user context
  3. Phase 1 (metadata): Create Linux user + DB record, set RESTORE_USER_CREATED flag
  4. Parallel restorers: dns, ssl, email, nginx, php, mysql, cron
  5. Phase 2 (metadata): Create domains if user was newly created
  6. Files restorer: Respect RESTORE_USER_CREATED flag (skip existence check if flag=1)
  7. Reload services (nginx, php-fpm) if configs changed
  8. Clean up temp dir
```

### Restore Execution Order

Restorers run in strict dependency order because later components need
foreign keys created by earlier ones:

| Order | Restorer | Creates | Needed by | Notes |
|-------|----------|---------|-----------|-------|
| 1 | metadata (phase 1) | user, DB record | everything | Sets RESTORE_USER_CREATED flag |
| 2 | metadata (phase 2) | domains | dns, ssl, email, nginx | Creates domain records + directory structure |
| 3 | files | /home/{user}/ | nginx, php | Syncs via rsync, fixes ACLs (www-data:x on home) |
| 4 | dns | dns_records | - | Merge mode or force-replace |
| 5 | ssl | ssl_certificates | - | Re-encrypts private keys |
| 6 | email | email_domains, mailboxes | - | Restores password hashes when available |
| 7 | php | FPM pool configs | nginx | Restores pool, reloads php-fpm (socket must exist before nginx) |
| 8 | nginx | vhost configs | - | Fixes FPM socket path, reloads nginx |
| 9 | mysql | databases, grants, users | - | Uses CREATE USER from backup, syncs credential passwords |
| 10 | postgres | databases, roles | - | pg_restore in custom format |
| 11 | cron | cron_jobs | - | DB entries + system crontab |
| 12 | redis | ACL user rules | - | ACL SETUSER + ACL SAVE |
| 13 | stalwart | JMAP accounts | - | stalwart-cli import or REST API |

**Key Change: RESTORE_USER_CREATED Flag**

When metadata restorer creates a new Linux user, it sets `RESTORE_USER_CREATED=1`.
The files restorer uses this flag to skip the "target directory exists and is non-empty" check.
This prevents newly created home directories from being skipped.

### Restore Conflict Handling

| Component | Without --force | With --force |
|-----------|----------------|--------------|
| User | Use existing record | Update record (preserve id + password) |
| Domain | Skip if exists | Update record |
| MySQL DB | Create as `{name}_restored` | Drop and recreate |
| DNS records | Merge (add missing only) | Delete all, re-insert |
| Mailboxes | Skip if exists | Update config |
| SSL certs | Skip if exists | Update cert + re-encrypt key |
| Nginx configs | Skip if exists | Overwrite |
| PHP pools | Skip if exists | Overwrite |

## Collector Design

Each collector is a standalone Bash script that:

1. Receives `user_id` (or `username`) and `staging_dir` as arguments
2. Queries the Jabali database for user-specific data
3. Writes output files to `staging_dir/{component}/`
4. Returns exit code 0 on success, non-zero on failure
5. Writes nothing if the component has no data (e.g., no PostgreSQL databases)

Collectors are independent of each other and can run in any order.
The `files.sh` collector is special: it adds paths directly to the restic backup
command instead of staging (home directories are too large to copy to staging).

## Restorer Design

Each restorer is a standalone Bash script that:

1. Receives `extract_dir`, `username`, `user_id`, and `force` flag
2. Reads staged data from `extract_dir/tmp/jabali-backup/{user}/{component}/`
3. Imports data back to the live system (DB inserts, file copies, service reloads)
4. Handles conflicts based on the `force` flag
5. Returns exit code 0 on success, non-zero on failure

Restorers MUST run in dependency order (metadata first).

## Encrypted Column Handling

Jabali uses Laravel's `Crypt::encryptString()` (AES-256-CBC) for sensitive DB fields:

- `ssl_certificates.private_key`
- `email_domains.dkim_private_key`
- `mysql_credentials.mysql_password_encrypted`
- `mailboxes.password_encrypted`
- `imap_sync_tasks.source_password_encrypted`
- `cloudflare_zones.api_token`
- `git_deployments.secret_token`
- `webhook_endpoints.secret_token`

These cannot be decrypted in pure Bash. Two PHP helper scripts handle this:

```
Backup:  DB --> jabali-decrypt.php --> plaintext --> staging file
Restore: staging file --> jabali-encrypt.php --> encrypted --> DB
```

Both helpers are stateless, read APP_KEY from the `JABALI_APP_KEY` environment
variable, and are called by collectors/restorers as needed.

## Error Handling

| Failure type | Severity | Behavior |
|-------------|----------|----------|
| Single collector/restorer fails | Soft | Log warning, skip, continue |
| Single database dump fails | Soft | Log error, skip database, continue |
| restic backup fails for account | Hard | Abort that account, continue others |
| restic repo unreachable | Hard | Abort entire run, notify |
| Staging disk full | Hard | Abort, clean up, notify |
| APP_KEY unavailable | Soft | Export encrypted values with marker |

Each account backup is independent. One failure does not block other accounts.
Staging/temp directories are cleaned up via `trap EXIT` on any exit signal.

## Tagging Strategy

Every restic snapshot is tagged for filtering:

**Per-user snapshots:**
- `account:{username}` — which account
- `type:full` — backup type
- `date:{YYYY-MM-DD}` — backup date
- `run:{run-id}` — links snapshots from the same backup run

**Server snapshots (disaster recovery):**
- `type:server` — server-level backup
- `date:{YYYY-MM-DD}` — backup date
- `hostname:{hostname}` — server hostname

Example queries:
```bash
restic snapshots --tag account:alice --tag date:2026-04-05
restic snapshots --tag type:server   # list server backups only
```

## Panel Architecture

**The panel is a thin wrapper for CLI commands.**

The Filament admin page and agent addon routes do NOT implement backup logic
directly. Every operation goes through `bin/jabali-backup`:

```
Browser --> Filament Page --> AgentClient (unix socket) --> Agent Addon --> jabali-backup CLI
```

- **Agent routes** (`panel/agent/jabali-backup.php`) call `jabali-backup <subcommand>`
  via `jbExec()` or `jbExecBackground()`. They parse CLI output into structured
  JSON for the panel.
- **New features must be implemented as CLI subcommands first**, then wrapped by
  agent routes. The CLI is the source of truth.
- The only PHP-native logic allowed in agent routes is: JSON parsing of CLI output,
  input validation before passing to CLI, and credential file I/O (writing secret
  files that the CLI will read).

This ensures:
- The CLI is always usable standalone (cron, SSH, scripts)
- No logic divergence between panel and CLI
- Single place to fix bugs and add features

## Download Streaming Architecture

### Per-user downloads (named pipe)

Per-user downloads use named pipes (FIFOs) to stream backups directly to the browser
with zero temporary file disk usage:

```
Browser
   ↓ GET /backup-download.php?users=alice&snapshot=latest
backup-download.php
   ↓ Agent RPC: jb.download_pipe
Agent: jbDownloadPipe
   ↓ Creates FIFO: /tmp/jabali-exports/pipe-[random]
   ↓ Spawns background: jabali-backup download alice --output=-
CLI streams tar.gz → Named Pipe → PHP reads 64KB chunks → Browser
   ↓
Cleanup: pipe removed after download
```

### Server backup downloads (temp archive)

Server downloads combine the server snapshot with all per-user snapshots into one
archive. Because this requires restoring multiple snapshots (which can take minutes),
a different approach is used to avoid blocking FrankenPHP workers:

```
Browser
   ↓ GET /backup-download.php?users=__server__&snapshot=ID
backup-download.php
   ↓ Agent RPC: jb.server_download_pipe
Agent: jbServerDownloadPipe
   ↓ Returns: { archive: "/tmp/jabali-server-export-XXX.tar.gz", done: "...tar.gz.done" }
   ↓ Spawns background script:
   │   1. restic restore server snapshot → tmpdir/server/
   │   2. restic restore each user snapshot → tmpdir/users/{user}/
   │   3. tar czf archive.tar.gz -C tmpdir .
   │   4. touch archive.tar.gz.done
backup-download.php polls for .done file (up to 15 min)
   ↓ .done appears
readfile(archive.tar.gz) → Browser
   ↓
Cleanup: archive + .done removed
```

This avoids FrankenPHP worker starvation from blocking `fopen()` on FIFOs
while restic is still restoring data.

## Security Model

- All secrets stored in separate files with `0600` permissions
- Restic password never appears in logs or process listing
- DB password read from file, not passed as CLI argument
- Staging directory created with `0700` permissions
- Restic encrypts all repository data at rest
- No plaintext passwords exported (hashes or encrypted form only)
- Private keys are re-encrypted via `jabali-encrypt.php` before DB insert during restore
