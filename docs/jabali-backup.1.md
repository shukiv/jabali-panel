# JABALI-BACKUP(1) - Jabali Hosting Panel Backup Tool

## NAME

**jabali-backup** - per-account backup and restore for the Jabali hosting panel

## SYNOPSIS

```
jabali-backup run [username] [--only=COMPONENTS] [--exclude=COMPONENTS] [--dry-run]
jabali-backup restore <username> [--snapshot=ID] [--only=COMPONENTS] [--file=PATH] [--target=DIR] [--force] [--dry-run]
jabali-backup ls <username> [path] [--snapshot=ID]
jabali-backup list <accounts|snapshots|domains> [--user=username]
jabali-backup init
jabali-backup check
jabali-backup forget [--dry-run]
jabali-backup schedule <install|remove|show> [--cron=EXPR]
jabali-backup config <test|show>
jabali-backup doctor
jabali-backup version
```

## DESCRIPTION

**jabali-backup** discovers hosting accounts from the Jabali panel database,
collects all per-account data (files, databases, DNS, email, SSL, web server
config, PHP, WordPress, cron), and stores encrypted, deduplicated snapshots
in a restic repository on local or remote storage.

It supports granular restore at the component level (e.g. just DNS records)
or at the individual file level (e.g. a single wp-config.php).

The tool runs as root on the Jabali server and reads its configuration from
`/etc/jabali-backup/config.conf`.

## COMMANDS

### run

```
jabali-backup run [username] [OPTIONS]
```

Back up one or all Jabali hosting accounts.

Without **username**, backs up every active account. With a username, backs up
only that account.

Each backup creates a restic snapshot tagged with `account:<username>`,
`type:full`, and `date:<YYYY-MM-DD>`. After all accounts are backed up,
the configured retention policy is applied automatically.

**Options:**

| Flag | Description |
|------|-------------|
| `--only=LIST` | Comma-separated list of collectors to run (see COMPONENTS) |
| `--exclude=LIST` | Comma-separated list of collectors to skip |
| `--dry-run` | Show what would be backed up without executing |
| `--parallel=N` | Back up N accounts concurrently (default: 1) |

**Examples:**

```
jabali-backup run                       # all accounts
jabali-backup run alice                 # single account
jabali-backup run --only=files,mysql    # only files and MySQL
jabali-backup run --exclude=wordpress   # everything except WordPress
jabali-backup run --dry-run             # preview
```

---

### restore

```
jabali-backup restore <username> [OPTIONS]
```

Restore a Jabali hosting account from a restic snapshot. Supports three modes:

1. **Full restore** - restore all components to the live system
2. **Selective restore** - restore specific components with `--only`
3. **File-level restore** - restore individual files with `--file`

Components are restored in dependency order: metadata first (creates the user
and domains that other components reference), then DNS, SSL, email, nginx,
PHP, MySQL, cron, and files last.

**Options:**

| Flag | Description |
|------|-------------|
| `--snapshot=ID` | Snapshot to restore from. Use a short ID or `latest` (default: `latest`) |
| `--only=LIST` | Restore only these components (see COMPONENTS) |
| `--exclude=LIST` | Skip these components |
| `--file=PATH` | Restore a specific file or directory. Path is relative to `/home/<user>/`. Can be specified multiple times |
| `--target=DIR` | Extract to this directory instead of restoring to the live system. No database imports, no service reloads |
| `--force` | Overwrite existing data. Without this flag, existing records are skipped and databases are created with a `_restored` suffix |
| `--dry-run` | Show what would be restored without making changes |

**Conflict handling (without --force):**

| Component | Behavior |
|-----------|----------|
| User | Use existing record |
| Domain | Skip |
| MySQL database | Create as `<name>_restored` |
| DNS records | Merge (add missing, skip duplicates) |
| Mailbox | Skip |
| SSL certificate | Skip |
| Nginx config | Skip |
| PHP-FPM pool | Skip |
| Cron job | Skip |

With `--force`, all existing data is overwritten (databases are dropped and
recreated, DNS records are deleted and re-inserted, configs are replaced).

**Restore order:**

```
1. metadata   (user, domains, aliases, redirects, webhooks)
2. dns        (DNS records to Jabali DB)
3. ssl        (certificates to DB, private keys re-encrypted)
4. email      (email domains, mailboxes, forwarders, autoresponders)
5. nginx      (vhost configs to sites-enabled, reloads nginx)
6. php        (FPM pool configs to pool.d, reloads php-fpm)
7. mysql      (database dumps imported, grants replayed)
8. cron       (cron jobs to DB + system crontab)
9. files      (home directory via rsync, ownership fixed)
```

**Examples:**

```
# Full restore from latest snapshot
jabali-backup restore alice --snapshot=latest

# Restore to a directory for inspection (no live changes)
jabali-backup restore alice --target=/tmp/inspect/

# Restore only DNS and SSL
jabali-backup restore alice --only=dns,ssl --force

# Restore everything except files (fast config-only restore)
jabali-backup restore alice --exclude=files --force

# Restore a single file
jabali-backup restore alice --file=domains/example.com/public_html/wp-config.php

# Restore a directory to a target
jabali-backup restore alice --file=domains/example.com/public_html/wp-content/ --target=/tmp/

# Restore multiple files at once
jabali-backup restore alice --file=.bashrc --file=.ssh/ --file=domains/example.com/public_html/wp-config.php

# Preview file restore
jabali-backup restore alice --file=domains/example.com/ --dry-run
```

---

### ls

```
jabali-backup ls <username> [path] [--snapshot=ID]
```

Browse files inside a backup snapshot. Paths are relative to `/home/<user>/`.
Use this to find the exact path before using `--file` to restore.

**Examples:**

```
jabali-backup ls alice                                    # list home dir root
jabali-backup ls alice domains/                           # list domains
jabali-backup ls alice domains/example.com/public_html/   # list website files
jabali-backup ls alice .ssh/                              # list SSH keys
jabali-backup ls alice --snapshot=abc123                  # browse specific snapshot
```

---

### list

```
jabali-backup list <accounts|snapshots|domains> [--user=username]
```

List resources.

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `accounts` | List all Jabali hosting accounts (ID, username, home dir, active status) |
| `snapshots` | List restic snapshots. Use `--user=` to filter by account |
| `domains` | List domains for a user. Requires `--user=` |

**Examples:**

```
jabali-backup list accounts
jabali-backup list snapshots
jabali-backup list snapshots --user=alice
jabali-backup list domains --user=alice
```

---

### init

```
jabali-backup init
```

Initialize the restic repository on the configured backend. Must be run once
before the first backup. Creates the encrypted repository structure.

---

### check

```
jabali-backup check
```

Verify the integrity of the restic repository. Checks pack files, index, and
snapshot consistency.

---

### forget

```
jabali-backup forget [--dry-run]
```

Apply the retention policy from the configuration and prune unreferenced data.
The retention policy is also applied automatically after each `run`.

The `--dry-run` flag shows which snapshots would be removed without actually
deleting them.

**Retention defaults (configurable in config.conf):**

| Setting | Default | Description |
|---------|---------|-------------|
| `keep_last` | 7 | Keep the N most recent snapshots |
| `keep_daily` | 30 | Keep one per day for N days |
| `keep_weekly` | 12 | Keep one per week for N weeks |
| `keep_monthly` | 24 | Keep one per month for N months |

---

### schedule

```
jabali-backup schedule <install|remove|show> [--cron=EXPR]
```

Manage the automated backup cron schedule.

| Subcommand | Description |
|------------|-------------|
| `install` | Install a cron entry. Default: `0 2 * * *` (daily at 2:00 AM) |
| `remove` | Remove the jabali-backup cron entry |
| `show` | Display the current schedule |

**Examples:**

```
jabali-backup schedule install                  # daily at 2am
jabali-backup schedule install --cron="0 3 * * *"  # daily at 3am
jabali-backup schedule install --cron="0 */6 * * *"  # every 6 hours
jabali-backup schedule show
jabali-backup schedule remove
```

---

### config

```
jabali-backup config <test|show>
```

| Subcommand | Description |
|------------|-------------|
| `test` | Validate config, test database connection, verify repo access, check APP_KEY |
| `show` | Display active configuration (secrets masked) |

---

### doctor

```
jabali-backup doctor
```

Check that all required and optional dependencies are installed. Reports the
path of each found binary and flags missing required tools.

**Required:** restic, mysql, mysqldump, php, gzip, curl

**Optional:** psql, pg_dump, wp-cli, rclone, jq, mail

---

### version

```
jabali-backup version
```

Print the version string.

## COMPONENTS

The following component names are used with `--only` and `--exclude` for both
backup (`run`) and restore (`restore`) commands:

| Component | Backup (collector) | Restore (restorer) |
|-----------|--------------------|--------------------|
| `files` | `/home/{user}/` directory via restic | rsync from snapshot, fix ownership |
| `mysql` | mysqldump of all user databases + grants | gunzip + mysql import, replay grants |
| `postgres` | pg_dump of user databases + roles | pg_restore + role recreation |
| `dns` | DNS records from Jabali DB as JSON + BIND zone | Insert records back to `dns_records` table |
| `email` | Email domains, mailboxes, forwarders, autoresponders, shares, DKIM | Re-import to DB, copy mail storage |
| `ssl` | SSL certs from DB (private keys decrypted) | Re-import to DB (private keys re-encrypted) |
| `nginx` | Nginx vhost configs + hotlink protection | Copy to sites-enabled, reload nginx |
| `php` | PHP-FPM pool configs + version | Copy to pool.d, reload php-fpm |
| `wordpress` | WP-CLI exports (plugins, themes, version, options) | Informational (files restored via `files`) |
| `cron` | Cron jobs from DB + system crontab | Insert to DB + crontab restore |
| `metadata` | User, domains, aliases, redirects, git deploys, webhooks, Cloudflare, bandwidth | Insert/update DB records |

## CONFIGURATION

Configuration file: `/etc/jabali-backup/config.conf` (override with `JABALI_BACKUP_CONFIG` env var).

Shell-compatible INI format with `[section]` headers and `key=value` pairs.

### Sections

**[jabali]** - Jabali database connection

| Key | Description |
|-----|-------------|
| `db_host` | MySQL host (default: localhost) |
| `db_name` | Jabali database name (default: jabali) |
| `db_user` | MySQL user |
| `db_password_file` | Path to file containing the DB password |
| `app_key_file` | Path to file containing the Laravel APP_KEY |
| `jabali_path` | Path to Jabali installation (default: /var/www/jabali) |

**[restic]** - Restic engine

| Key | Description |
|-----|-------------|
| `password_file` | Path to file containing the restic repository password |
| `cache_dir` | Restic cache directory (default: /var/cache/jabali-backup/restic) |

**[repository]** - Backup destination

| Key | Description |
|-----|-------------|
| `type` | Backend: `sftp`, `s3`, `b2`, `gcs`, `azure`, `rest`, `local`, `rclone` |
| `path` | Repository path (format depends on type) |
| `aws_access_key_id_file` | (S3/Wasabi) Path to access key file |
| `aws_secret_access_key_file` | (S3/Wasabi) Path to secret key file |
| `b2_account_id_file` | (B2) Path to account ID file |
| `b2_account_key_file` | (B2) Path to account key file |

**[retention]** - Snapshot retention policy

| Key | Default | Description |
|-----|---------|-------------|
| `keep_last` | 7 | Keep N most recent snapshots |
| `keep_daily` | 30 | Keep one per day for N days |
| `keep_weekly` | 12 | Keep one per week for N weeks |
| `keep_monthly` | 24 | Keep one per month for N months |

**[staging]** - Temporary storage

| Key | Default | Description |
|-----|---------|-------------|
| `dir` | /tmp/jabali-backup | Staging directory |
| `cleanup` | true | Remove staging files after backup |

**[notifications]** - Alerts

| Key | Default | Description |
|-----|---------|-------------|
| `on_failure` | true | Send notification on failure |
| `on_success` | false | Send notification on success |
| `email_to` | (empty) | Email address for notifications |
| `webhook_url` | (empty) | URL for POST webhook notifications |

**[paths]** - Server paths

| Key | Default | Description |
|-----|---------|-------------|
| `stalwart_data` | /opt/stalwart-mail/data | Stalwart mail data directory |
| `php_fpm_pools` | /etc/php/*/fpm/pool.d | PHP-FPM pool config directory |
| `nginx_sites` | /etc/nginx/sites-enabled | Nginx site configs directory |

**[logging]** - Log output

| Key | Default | Description |
|-----|---------|-------------|
| `file` | /var/log/jabali-backup.log | Log file path |
| `level` | info | Log level: `debug`, `info`, `warn`, `error` |

## SUPPORTED BACKENDS

| Backend | type= | path= format | Required config |
|---------|-------|--------------|-----------------|
| Local | `local` | `/path/to/repo` | none |
| SFTP/SSH | `sftp` | `sftp:user@host:/path` | SSH key auth |
| Amazon S3 | `s3` | `s3:s3.amazonaws.com/bucket` | key + secret files |
| Wasabi | `s3` | `s3:s3.wasabisys.com/bucket` | key + secret files |
| Backblaze B2 | `b2` | `b2:bucket:/path` | account ID + key files |
| Google Cloud | `gcs` | `gs:bucket:/path` | `GOOGLE_APPLICATION_CREDENTIALS` |
| Azure Blob | `azure` | `azure:container:/path` | `AZURE_ACCOUNT_NAME` + key |
| REST server | `rest` | `rest:https://user:pass@host/` | credentials in URL |
| rclone | `rclone` | `rclone:remote:path` | pre-configured rclone remote |

## FILES

| Path | Description |
|------|-------------|
| `/usr/local/bin/jabali-backup` | CLI entry point |
| `/usr/local/lib/jabali-backup/` | Library scripts, collectors, restorers |
| `/etc/jabali-backup/config.conf` | Configuration file |
| `/etc/jabali-backup/db-password` | Database password (mode 0600) |
| `/etc/jabali-backup/restic-password` | Restic repository password (mode 0600) |
| `/etc/jabali-backup/app-key` | Laravel APP_KEY for decryption (mode 0600) |
| `/var/log/jabali-backup.log` | Log file |
| `/var/cache/jabali-backup/restic/` | Restic cache directory |

## ENVIRONMENT

| Variable | Description |
|----------|-------------|
| `JABALI_BACKUP_CONFIG` | Override config file path |
| `LOG_LEVEL` | Override log level |
| `LOG_FILE` | Override log file path |

## EXIT STATUS

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error (config invalid, snapshot not found, etc.) |
| N | Number of failed accounts (for `run` and `restore` commands) |

## SECURITY

All secrets (database password, restic password, APP_KEY) are stored in
separate files with mode 0600, never in the config file or command-line
arguments.

Restic encrypts all repository data at rest. The restic password never
appears in logs or process listings.

Encrypted database columns (SSL private keys, DKIM keys, API tokens) are
decrypted during backup via `jabali-decrypt.php` and re-encrypted during
restore via `jabali-encrypt.php`, both using the Laravel APP_KEY.

Email and MySQL passwords are never exported in plaintext. Only password
hashes are stored in backups.

## SEE ALSO

restic(1), mysql(1), mysqldump(1), pg_dump(1), nginx(8), crontab(1)

## VERSION

0.1.0

## AUTHORS

Jabali Project
