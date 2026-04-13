# jabali-backup

Per-account backup tool for the Jabali hosting panel, powered by [restic](https://restic.net/).

Discovers accounts from Jabali's database, collects all per-account data (files, databases, DNS, email, SSL, cron, PHP), and pushes encrypted, deduplicated snapshots to remote storage.

## Prerequisites

- **restic** >= 0.16
- **mysql** client (for mysqldump)
- **jq** (for destination management)
- **php** >= 8.1 (for encrypted column decryption)
- **pg_dump** (optional, for PostgreSQL backups)
- **wp-cli** (optional, for WordPress exports)
- **rclone** (optional, for Google Drive / other rclone backends)
- Root access on the Jabali server

<!-- AUTO-GENERATED:install-start -->
## Installation

### Quick install

```bash
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-backup/main/install.sh | sudo bash
```

### Manual install

```bash
git clone https://github.com/shukiv/jabali-backup.git /opt/jabali-backup
cd /opt/jabali-backup
sudo ./install.sh
```

### Update

```bash
sudo jabali-backup update
```

Checks for new commits on GitHub. If updates are available, pulls latest and runs
the full upgrade. If already up to date, exits immediately.

Alternatively, from a fresh install or remote pipe:

```bash
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-backup/main/install.sh | sudo bash -s -- update
```

Preserves config and secrets.

### Uninstall

```bash
sudo jabali-backup uninstall
```

Removes CLI, panel addon, systemd timer, and bash completions.
Keeps `/etc/jabali-backup/` (config and secrets) for manual cleanup.

### What gets installed

| Path | Contents |
|------|----------|
| `/usr/local/bin/jabali-backup` | CLI entry point |
| `/usr/local/lib/jabali-backup/` | Library scripts, collectors, restorers, PHP helpers |
| `/etc/jabali-backup/` | Configuration, secrets (auto-configured from Jabali `.env`) |
| `/etc/bash_completion.d/jabali-backup` | Bash tab completions |
| `/etc/systemd/system/jabali-backup.timer` | Daily backup timer (02:00 with 15min jitter) |
| `/etc/jabali/agent.d/jabali-backup.php` | Agent RPC routes (panel integration) |
| Jabali panel `app/Filament/Admin/Pages/` | Admin backup + snapshot browser pages |
| Jabali panel `app/Filament/Jabali/Pages/` | User backup page |
| Jabali panel `app/Backup/` | Service provider + restic snapshot adapter |

The panel addon is installed automatically when Jabali is detected at `/var/www/jabali`.
Set `JABALI_PATH` to override the path.

### Auto-configured secrets

The installer extracts from `/var/www/jabali/.env`:

| Secret | Source | File |
|--------|--------|------|
| DB password | `DB_PASSWORD` | `/etc/jabali-backup/db-password` |
| APP_KEY | `APP_KEY` | `/etc/jabali-backup/app-key` |
| Restic password | Auto-generated (`openssl rand`) | `/etc/jabali-backup/restic-password` |

### Auto-installed dependencies

Missing packages are installed via `apt-get`: `restic`, `mysql-client`, `jq`, `tar`, `gzip`.
<!-- AUTO-GENERATED:install-end -->

## Quick Start

After installation, secrets and config are auto-configured. You only need to:

```bash
# 1. Add a backup destination (via panel or CLI)
sudo jabali-backup destination add

# 2. Verify setup
sudo jabali-backup doctor
sudo jabali-backup config test

# 3. Run first backup
sudo jabali-backup run

# Daily backups are already enabled (02:00 with 15min jitter)
```

If you need to manually configure secrets (e.g., Jabali is not at `/var/www/jabali`):

```bash
echo 'YOUR_DB_PASSWORD' | sudo tee /etc/jabali-backup/db-password > /dev/null
echo 'YOUR_RESTIC_PASSWORD' | sudo tee /etc/jabali-backup/restic-password > /dev/null
grep APP_KEY /path/to/jabali/.env | cut -d= -f2 | sudo tee /etc/jabali-backup/app-key > /dev/null
sudo chmod 600 /etc/jabali-backup/{db-password,restic-password,app-key}
```

<!-- AUTO-GENERATED:commands-start -->
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
jabali-backup run --destination=offsite    # use a specific destination
```

### `jabali-backup server-backup`

Full server backup for **disaster recovery**. Captures everything needed to
rebuild the box on a fresh Ubuntu install — databases, service configs,
Stalwart DKIM keys, Let's Encrypt state, every user account — per the
authoritative spec at `jabali/docs/backup-server-spec.md`.

```bash
jabali-backup server-backup                       # full server + all users
jabali-backup server-backup --skip-users          # server configs only (fast)
jabali-backup server-backup --skip-stalwart-data  # skip /var/lib/stalwart-mail RocksDB (faster, but loses DKIM keys)
jabali-backup server-backup --dry-run             # preview what would be backed up
```

Snapshots are tagged `type:server,date:YYYY-MM-DD,hostname:<host>` so they
stay separate from per-user snapshots.

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

### `jabali-backup server-restore`

Restore entire server from a disaster-recovery backup (5-phase restore: base
install → databases → service configs → users → finalize).

```bash
jabali-backup server-restore                # full server restore (latest DR snapshot)
jabali-backup server-restore --skip-users   # server configs only
jabali-backup server-restore --force        # overwrite all existing data
jabali-backup server-restore --snapshot=ID  # specific server snapshot
```

See [DISASTER-RECOVERY.md](DISASTER-RECOVERY.md) for the full runbook.

### `jabali-backup server-download`

Download a disaster-recovery snapshot as a single tar.gz — for off-site archival
or rehydrating onto a different host.

```bash
jabali-backup server-download                         # full (server + all users)
jabali-backup server-download --configs-only          # server layer only (fast, small)
jabali-backup server-download --output=/tmp/dr.tgz    # explicit path
jabali-backup server-download --snapshot=abc123de     # specific snapshot
```

The panel's "Download Server Backup" button produces a byte-identical
archive via the same shared helper (`lib/server-download.sh`).

### `jabali-backup download <username> [...]`

Download one or more backup snapshots as a compressed archive.

```bash
jabali-backup download alice                             # latest snapshot
jabali-backup download alice bob                         # multiple users
jabali-backup download alice --snapshot=abc123de         # specific snapshot
jabali-backup download alice --only=files,mysql          # selected components
jabali-backup download alice --output=/tmp/backup.tar.gz # save to path
jabali-backup download alice --output=-                  # stream to stdout
```

### `jabali-backup ls <username|server> [path]`

Browse files in a snapshot without restoring.

```bash
jabali-backup ls alice                                   # list alice's home
jabali-backup ls alice domains/example.com/public_html   # list subdirectory
jabali-backup ls alice --snapshot=abc123de               # specific snapshot
jabali-backup ls server                                  # browse latest DR snapshot
jabali-backup ls server config/jabali                    # /etc/jabali contents in DR snapshot
```

### `jabali-backup list`

List accounts or snapshots.

```bash
jabali-backup list accounts
jabali-backup list snapshots
jabali-backup list snapshots --user=alice
jabali-backup list snapshots --type=server
jabali-backup list server-snapshots           # just disaster-recovery snapshots
jabali-backup list server-snapshots --json
jabali-backup list domains --user=alice
```

### `jabali-backup destination`

Manage backup destinations (multiple storage backends).

```bash
jabali-backup destination add         # interactive setup
jabali-backup destination list        # show all destinations
jabali-backup destination test NAME   # verify connectivity
jabali-backup destination remove NAME
jabali-backup destination default NAME
```

### `jabali-backup init`

Initialize the restic repository on the configured backend.

### `jabali-backup check`

Verify repository integrity.

### `jabali-backup forget`

Apply retention policy and prune old snapshots.

```bash
jabali-backup forget                       # use config retention
jabali-backup forget --keep-last=14        # override retention
jabali-backup forget --dry-run             # preview only
```

### `jabali-backup schedule`

Manage automated backup schedules. Multiple independent jobs are supported;
each writes its own crontab entry. Jobs live in
`/etc/jabali-backup/schedules.json`. Use `--server-backup` on any job to run a
**full disaster-recovery backup** (server layer + every user) instead of the
per-account `run` path.

```bash
jabali-backup schedule list                                    # show all jobs
jabali-backup schedule list --json
jabali-backup schedule add --name="Daily All" --destination=rasp \
    --cron="0 2 * * *"                                         # daily per-user
jabali-backup schedule add --name="Weekly DR" --destination=rasp \
    --cron="0 3 * * 0" --server-backup                         # weekly DR
jabali-backup schedule add --name="Alice only" --destination=rasp \
    --cron="0 4 * * *" --accounts=alice                        # single user
jabali-backup schedule update --id=<id> --cron="0 5 * * *"
jabali-backup schedule enable  --id=<id>                       # add to crontab
jabali-backup schedule disable --id=<id>                       # remove from crontab
jabali-backup schedule remove  --id=<id>
jabali-backup schedule show    --id=<id>                       # JSON detail
jabali-backup schedule sync                                    # rebuild crontab
```

The panel's **Schedule** tab writes the same JSON — each row there maps 1-to-1
to a CLI job. The "Server Backup (Disaster Recovery)" toggle on the form sets
`server_backup: true`.

### `jabali-backup config`

Validate and display configuration.

```bash
jabali-backup config test                  # test DB connection + repo access
jabali-backup config show                  # print active config (secrets masked)
```

### `jabali-backup doctor`

Check that all dependencies are installed and configured.

### `jabali-backup update`

Check for updates and upgrade to the latest version. Checks GitHub for new commits before running the full upgrade.

### `jabali-backup uninstall`

Remove jabali-backup CLI and panel addon. Preserves config and secrets at `/etc/jabali-backup/`.

### `jabali-backup version`

Show version.
<!-- AUTO-GENERATED:commands-end -->

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
| Redis ACLs | Per-user ACL rules and credentials | Text files |
| Stalwart accounts | JMAP emails, mailboxes, Sieve scripts, identities, vacation responses | JSON (via `stalwart-cli`) |

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

<!-- AUTO-GENERATED:collectors-start -->
## Collector Reference

Per-user collectors (usable with `--only` and `--exclude` on `run`, `restore`,
and `download`):

`files`, `mysql`, `postgres`, `dns`, `email`, `ssl`, `nginx`, `php`,
`wordpress`, `cron`, `stalwart`, `redis`, `metadata`

Server backup (`server-backup`) runs the `server` collector in addition to a
per-user pass over every account. The `server` collector captures all 20
categories of [`backup-server-spec.md`](../../jabali/docs/backup-server-spec.md),
including the Stalwart RocksDB/DKIM snapshot (opt out with
`--skip-stalwart-data`), Bulwark metadata-only record, and the panel app tree
(git info + dirty patch or tarball fallback). See
[`ARCHITECTURE.md`](ARCHITECTURE.md#server-backup-layout-disaster-recovery)
for the full 20-row map.
<!-- AUTO-GENERATED:collectors-end -->

## Configuration

See [CONFIGURATION.md](CONFIGURATION.md) for full config file reference.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for system design and data flow.

## Architecture Decision Records

See [adr/](adr/README.md) for recorded architectural decisions.

## Panel Integration

See [PANEL-INTEGRATION.md](PANEL-INTEGRATION.md) for details on the Filament admin/user pages.

## License

Proprietary. All rights reserved.
