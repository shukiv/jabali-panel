# jabali-backup

Per-account backup tool for the Jabali hosting panel, powered by [restic](https://restic.net/).

Discovers accounts from Jabali's database, collects all per-account data (files, databases, DNS, email, SSL, cron, PHP), and pushes encrypted, deduplicated snapshots to remote storage.

## Prerequisites

- **restic** >= 0.16
- **mysql** client (for mysqldump)
- **php** >= 8.1 (for encrypted column decryption)
- **pg_dump** (optional, for PostgreSQL backups)
- **wp-cli** (optional, for WordPress exports)
- **rclone** (optional, for Google Drive / other rclone backends)
- Root access on the Jabali server

## Installation

```bash
git clone https://github.com/shukiv/jabali-backup.git
cd jabali-backup
sudo ./install.sh
```

This installs:

| Path | Contents |
|------|----------|
| `/usr/local/bin/jabali-backup` | CLI entry point |
| `/usr/local/lib/jabali-backup/` | Library scripts, collectors, and restorers |
| `/etc/jabali-backup/` | Configuration and secret files |

## Quick Start

```bash
# 1. Copy and edit configuration
sudo cp /etc/jabali-backup/config.conf.example /etc/jabali-backup/config.conf
sudo nano /etc/jabali-backup/config.conf

# 2. Set up secrets
echo "YOUR_DB_PASSWORD" | sudo tee /etc/jabali-backup/db-password
echo "YOUR_RESTIC_PASSWORD" | sudo tee /etc/jabali-backup/restic-password
grep APP_KEY /var/www/jabali/.env | cut -d= -f2 | sudo tee /etc/jabali-backup/app-key
sudo chmod 600 /etc/jabali-backup/{db-password,restic-password,app-key}

# 3. Create read-only DB user
sudo mysql < /etc/jabali-backup/create-backup-user.sql

# 4. Initialize restic repository
sudo jabali-backup init

# 5. Verify setup
sudo jabali-backup doctor
sudo jabali-backup config test

# 6. Run first backup
sudo jabali-backup run

# 7. Install daily schedule
sudo jabali-backup schedule install
```

## Commands

### `jabali-backup run [username]`

Run a backup. Without a username, backs up all accounts.

```bash
jabali-backup run                          # all accounts
jabali-backup run alice                    # single account
jabali-backup run --only=files,mysql       # selected collectors only
jabali-backup run --exclude=wordpress      # skip specific collectors
jabali-backup run --dry-run                # preview without executing
jabali-backup run --parallel=4             # 4 accounts concurrently
jabali-backup run --resume                 # skip already-backed-up accounts
```

### `jabali-backup restore <username>`

Restore an account from a snapshot.

```bash
jabali-backup restore alice --snapshot=latest
jabali-backup restore alice --snapshot=abc123de
jabali-backup restore alice --only=files,mysql
jabali-backup restore alice --file=domains/example.com/wp-config.php  # single file
jabali-backup restore alice --target=/tmp/restore/   # inspect before applying
jabali-backup restore alice --dry-run
jabali-backup restore alice --force                   # overwrite existing data
```

### `jabali-backup download <username> [...]`

Download one or more backup snapshots as a compressed archive.

```bash
jabali-backup download alice                             # latest snapshot
jabali-backup download alice bob                         # multiple users
jabali-backup download alice --snapshot=abc123de        # specific snapshot
jabali-backup download alice --only=files,mysql         # selected components
jabali-backup download alice --output=/tmp/backup.tar.gz # save to path
jabali-backup download alice --output=-                  # stream to stdout (for piping)
```

For streaming via named pipe (panel integration):
```bash
# Creates a FIFO pipe that blocks until data is read, then cleans up
jabali-backup download alice --output=-
```

### `jabali-backup ls <username> [path]`

Browse files in a snapshot without restoring.

```bash
jabali-backup ls alice                                   # list all files
jabali-backup ls alice domains/example.com/public_html   # list subdirectory
jabali-backup ls alice --snapshot=abc123de               # specific snapshot
```

### `jabali-backup list`

List accounts or snapshots.

```bash
jabali-backup list accounts
jabali-backup list accounts --active
jabali-backup list snapshots
jabali-backup list snapshots --user=alice
jabali-backup list domains --user=alice
```

### `jabali-backup init`

Initialize the restic repository on the configured backend.

```bash
jabali-backup init
```

### `jabali-backup check`

Verify repository integrity.

```bash
jabali-backup check
```

### `jabali-backup forget`

Apply retention policy and prune old snapshots.

```bash
jabali-backup forget                       # use config retention
jabali-backup forget --keep-last=14        # override retention
jabali-backup forget --dry-run             # preview only
```

### `jabali-backup schedule`

Manage automated backup schedule.

```bash
jabali-backup schedule install             # daily at 2am (default)
jabali-backup schedule install --cron="0 3 * * *"  # custom time
jabali-backup schedule show
jabali-backup schedule remove
```

### `jabali-backup config`

Validate and display configuration.

```bash
jabali-backup config test                  # test DB connection + repo access
jabali-backup config show                  # print active config (secrets masked)
```

### `jabali-backup doctor`

Check that all dependencies are installed and configured.

```bash
jabali-backup doctor
```

## What Gets Backed Up

Each account backup includes:

| Component | Description | Format |
|-----------|-------------|--------|
| Home directory | `/home/{user}/` with all files | restic snapshot |
| MySQL databases | All databases owned by user | `.sql.gz` dumps |
| MySQL grants | User privileges across all hosts | `.sql` |
| PostgreSQL databases | All databases owned by user | pg_dump custom format |
| PostgreSQL roles | Role definitions and memberships | `.sql` |
| DNS records | PowerDNS zones per domain | BIND zone + JSON |
| Email domains | DKIM keys, catch-all, quotas | JSON |
| Mailboxes | Config, quotas, protocol flags | JSON |
| Mail storage | Stalwart maildir data | restic snapshot |
| Email forwards | Forwarder destinations | JSON |
| Autoresponders | Subject, message, schedule | JSON |
| Mailbox shares | Shared folder ACLs | JSON |
| IMAP sync tasks | Active import jobs | JSON |
| SSL certificates | Cert, key, CA bundle per domain | PEM files |
| Nginx vhosts | Per-domain server configs | `.conf` files |
| Hotlink protection | Per-domain rules | JSON |
| PHP-FPM pools | Per-user pool configuration | `.conf` files |
| WordPress | DB dump, plugins, themes, options | SQL + JSON |
| Cron jobs | Jabali DB entries + system crontab | JSON + crontab |
| Domain aliases | Alias mappings | JSON |
| Domain redirects | Redirect rules | JSON |
| Git deployments | Repo URL, branch, deploy scripts | JSON |
| Cloudflare zones | Zone IDs and API tokens | JSON |
| Webhooks | URLs, events, secrets | JSON |
| User settings | Account preferences | JSON |
| Bandwidth history | Per-domain usage stats | JSON |
| Hosting package | Package name, limits, features | JSON |

## Supported Backends

| Backend | Repository path format | Extra config |
|---------|----------------------|--------------|
| SFTP/SSH | `sftp:user@host:/path` | SSH key auth |
| S3 | `s3:s3.amazonaws.com/bucket` | `aws_access_key_id_file`, `aws_secret_access_key_file` |
| Wasabi | `s3:s3.wasabisys.com/bucket` | Same as S3 |
| Backblaze B2 | `b2:bucket:/path` | `b2_account_id_file`, `b2_account_key_file` |
| Google Cloud | `gs:bucket:/path` | `GOOGLE_APPLICATION_CREDENTIALS` |
| Azure Blob | `azure:container:/path` | `AZURE_ACCOUNT_NAME`, `AZURE_ACCOUNT_KEY` |
| REST server | `rest:https://user:pass@host/` | Built-in auth |
| rclone | `rclone:remote:path` | Pre-configured rclone remote (Google Drive, etc.) |
| Local | `/path/to/repo` | None |

## Collector Reference

Available collectors for `--only` and `--exclude` flags:

`files`, `mysql`, `postgres`, `dns`, `email`, `ssl`, `nginx`, `php`, `wordpress`, `cron`, `metadata`
