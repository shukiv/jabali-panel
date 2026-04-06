# Architecture

## Directory Structure

```
jabali-backup/
  bin/
    jabali-backup             Main CLI entry point (Bash)
  lib/
    collectors/               Backup: gather data from live system
      files.sh                /home/{user}/ directory
      mysql.sh                MySQL databases + grants
      postgres.sh             PostgreSQL databases + roles
      dns.sh                  PowerDNS zone export
      email.sh                Stalwart mail: mailboxes, forwards, DKIM
      ssl.sh                  SSL certificates + private keys
      cron.sh                 Cron jobs (DB + system)
      php.sh                  PHP-FPM pool config
      wordpress.sh            WP-CLI exports
      nginx.sh                Nginx vhosts + hotlink rules
      metadata.sh             Account metadata from Jabali DB
    restorers/                Restore: import data back to live system
      metadata.sh             User, domains, aliases (runs first for FK deps)
      dns.sh                  DNS records back to Jabali DB
      ssl.sh                  SSL certs back to DB (re-encrypts private keys)
      email.sh                Email domains, mailboxes, forwarders, autoresponders
      nginx.sh                Vhost configs to sites-enabled, reloads nginx
      php.sh                  FPM pool configs to pool.d, reloads php-fpm
      mysql.sh                Database dumps back to MySQL, replays grants
      cron.sh                 Cron jobs to DB + system crontab
      files.sh                Home directory via rsync, fixes ownership
    restic.sh                 Restic wrapper (multi-backend)
    discover.sh               Account discovery + DB helpers
    config.sh                 INI config parser
    logging.sh                Structured logging
    notify.sh                 Email + webhook notifications
    jabali-decrypt.php        Decrypt Laravel-encrypted DB values
    jabali-encrypt.php        Re-encrypt values for restore
  etc/
    config.conf.example       Example configuration
  docs/                       Documentation
  plans/                      Implementation blueprints
  install.sh                  Install to system paths
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
| 2 | dns | dns_records | - | Can run in parallel |
| 3 | ssl | ssl_certificates | - | Can run in parallel |
| 4 | email | email_domains, mailboxes | - | Can run in parallel |
| 5 | nginx | vhost configs | - | Can run in parallel |
| 6 | php | FPM pool configs | - | Can run in parallel |
| 7 | mysql | databases, grants, users | - | Creates/recreates MySQL users |
| 8 | cron | cron_jobs | - | Can run in parallel |
| 9 | files | /home/{user}/ | - | Checks RESTORE_USER_CREATED to skip empty dir check |
| 10 | metadata (phase 2) | domains | - | Runs after files, creates domains if needed |

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

- `account:{username}` -- which account
- `type:full` -- backup type
- `date:{YYYY-MM-DD}` -- backup date

Example query:
```bash
restic snapshots --tag account:alice --tag date:2026-04-05
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

Download operations use named pipes (FIFOs) to stream backups directly to the browser
with zero temporary file disk usage:

```
Browser
   ↓ GET /backup-download.php?username=alice&snapshot=latest
Filament Panel (public/backup-download.php)
   ↓ Agent RPC: jb.download_pipe
Agent Addon (panel/agent/jabali-backup.php:jbDownloadPipe)
   ↓ Creates: /tmp/jabali-exports/pipe-[random]
   ↓ Spawns in background: jabali-backup download alice --output=-
CLI (bin/jabali-backup download)
   ↓ Calls restic restore, pipes tar.gz to stdout
Stdout → Named Pipe
   ↓
Panel reads from pipe, streams to browser
   ↓
Cleanup: Named pipe removed after download complete
```

**Key Points:**

- `--output=-` flag tells CLI to stream to stdout (kept clean of log messages via stderr redirect)
- Named pipe blocks until data is read, preventing the CLI from running ahead
- Agent spawns the CLI in fully detached background (nohup + & + /dev/null)
- Panel endpoint reads from the pipe with 64KB chunks and flushes to browser
- Pipe is auto-removed by the CLI after writing completes

This avoids:
- Temporary files on disk (no space used during streaming)
- Memory buffering of entire archive (streaming 64KB chunks)
- Timeout issues (streaming starts immediately, no wait for full backup)

## Security Model

- All secrets stored in separate files with `0600` permissions
- Restic password never appears in logs or process listing
- DB password read from file, not passed as CLI argument
- Staging directory created with `0700` permissions
- Restic encrypts all repository data at rest
- No plaintext passwords exported (hashes or encrypted form only)
- Private keys are re-encrypted via `jabali-encrypt.php` before DB insert during restore
