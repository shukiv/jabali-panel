# Configuration Reference

## File Location

The config file is searched in order:

1. `/etc/jabali-backup/config.conf`
2. `~/.jabali-backup.conf`

Override with `JABALI_BACKUP_CONFIG` environment variable.

## Format

Shell-compatible INI format. Sections use `[section]` headers. Values use `key=value`.
Lines starting with `#` are comments.

## Full Example

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
type=sftp
path=sftp:backup-server:/backups/jabali

[repository_copy]
# Optional secondary repository (3-2-1 backup rule)
# type=s3
# path=s3:s3.amazonaws.com/jabali-offsite

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

## Section Reference

### `[jabali]`

Connection to the Jabali panel database.

| Key | Required | Description |
|-----|----------|-------------|
| `db_host` | Yes | MySQL host for Jabali's database |
| `db_name` | Yes | Jabali database name |
| `db_user` | Yes | MySQL user (read-only recommended) |
| `db_password_file` | Yes | Path to file containing the DB password |
| `app_key_file` | Yes | Path to file containing Jabali's APP_KEY (for decrypting encrypted DB columns) |
| `jabali_path` | No | Path to Jabali installation (default: `/var/www/jabali`) |

### `[restic]`

Restic engine configuration.

| Key | Required | Description |
|-----|----------|-------------|
| `password_file` | Yes | Path to file containing the restic repository password |
| `cache_dir` | No | Restic cache directory (default: `/var/cache/jabali-backup/restic`) |

### `[repository]`

Primary backup repository.

| Key | Required | Description |
|-----|----------|-------------|
| `type` | Yes | Backend type: `sftp`, `s3`, `b2`, `gcs`, `azure`, `rest`, `local`, `rclone` |
| `path` | Yes | Repository path (format depends on type) |
| `aws_access_key_id_file` | S3 only | Path to file containing AWS access key |
| `aws_secret_access_key_file` | S3 only | Path to file containing AWS secret key |
| `b2_account_id_file` | B2 only | Path to file containing B2 account ID |
| `b2_account_key_file` | B2 only | Path to file containing B2 account key |

### `[repository_copy]`

Optional secondary repository. Same keys as `[repository]`. Used for offsite replication after backup completes.

### `[retention]`

Snapshot retention policy applied during `jabali-backup forget`.

| Key | Default | Description |
|-----|---------|-------------|
| `keep_last` | `7` | Keep the N most recent snapshots |
| `keep_daily` | `30` | Keep one snapshot per day for N days |
| `keep_weekly` | `12` | Keep one snapshot per week for N weeks |
| `keep_monthly` | `24` | Keep one snapshot per month for N months |

### `[staging]`

Temporary storage for collector output before restic ingestion.

| Key | Default | Description |
|-----|---------|-------------|
| `dir` | `/tmp/jabali-backup` | Staging directory path |
| `cleanup` | `true` | Remove staging files after backup |

### `[notifications]`

Notifications on backup completion.

| Key | Default | Description |
|-----|---------|-------------|
| `on_failure` | `true` | Send notification on backup failure |
| `on_success` | `false` | Send notification on backup success |
| `email_to` | (empty) | Email address for notifications (requires `mail` or `sendmail`) |
| `webhook_url` | (empty) | HTTP(S) URL for POST webhook notifications |

### `[stalwart]`

Stalwart mail server JMAP account backup. When enabled, uses `stalwart-cli export account` to capture the full JMAP state (emails, mailbox structure, Sieve scripts, identities, vacation responses). Falls back to REST API if the CLI is unavailable.

| Key | Default | Description |
|-----|---------|-------------|
| `enabled` | `false` | Enable Stalwart account-level backup |
| `url` | `http://localhost:8080` | Stalwart server URL |
| `admin_token_file` | `/etc/jabali-backup/stalwart-token` | Path to file containing the admin API token |

To set up:
```bash
# Get an admin token from Stalwart's web UI or config
echo 'YOUR_STALWART_ADMIN_TOKEN' | sudo tee /etc/jabali-backup/stalwart-token
sudo chmod 600 /etc/jabali-backup/stalwart-token

# Enable in config
sudo sed -i 's/^enabled=false/enabled=true/' /etc/jabali-backup/config.conf
```

### `[paths]`

Server paths for data collection.

| Key | Default | Description |
|-----|---------|-------------|
| `stalwart_data` | `/opt/stalwart-mail/data` | Stalwart mail server data directory |
| `php_fpm_pools` | `/etc/php/*/fpm/pool.d` | PHP-FPM pool config directory (glob) |
| `nginx_sites` | `/etc/nginx/sites-enabled` | Nginx site configs directory |

### `[logging]`

Backup log configuration.

| Key | Default | Description |
|-----|---------|-------------|
| `file` | `/var/log/jabali-backup.log` | Log file path |
| `level` | `info` | Log level: `debug`, `info`, `warn`, `error` |

## Secret Files

All secrets are stored in separate files (never in the config file itself).
Set permissions to `0600` owned by `root`:

```bash
sudo chmod 600 /etc/jabali-backup/db-password
sudo chmod 600 /etc/jabali-backup/restic-password
sudo chmod 600 /etc/jabali-backup/app-key
```

### Getting the APP_KEY

The APP_KEY is required to decrypt SSL private keys, DKIM keys, and other encrypted
columns in Jabali's database. Copy it from Jabali's `.env`:

```bash
grep APP_KEY /var/www/jabali/.env | cut -d= -f2 | sudo tee /etc/jabali-backup/app-key
```

## Backend Setup Examples

### SFTP

```ini
[repository]
type=sftp
path=sftp:backupuser@192.168.1.100:/backups/jabali
```

Requires SSH key authentication. Set up with:
```bash
sudo ssh-keygen -t ed25519 -f /root/.ssh/jabali_backup -N ""
ssh-copy-id -i /root/.ssh/jabali_backup.pub backupuser@192.168.1.100
```

### Wasabi S3

```ini
[repository]
type=s3
path=s3:s3.eu-central-1.wasabisys.com/my-jabali-backups
aws_access_key_id_file=/etc/jabali-backup/wasabi-key
aws_secret_access_key_file=/etc/jabali-backup/wasabi-secret
```

### Google Drive (via rclone)

First configure rclone:
```bash
rclone config  # create a remote named "gdrive"
```

Then:
```ini
[repository]
type=rclone
path=rclone:gdrive:jabali-backups
```

### Local Directory

```ini
[repository]
type=local
path=/mnt/external-drive/jabali-backups
```

<!-- AUTO-GENERATED:env-vars-start -->
## Environment Variables

Variables that influence the CLI at runtime without being written to
`config.conf`. Defined in `bin/jabali-backup` and `lib/collectors/server.sh`.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `JABALI_BACKUP_CONFIG` | No | `/etc/jabali-backup/config.conf` | Path to the main config file |
| `JABALI_DESTINATION` | No | default from `destinations.json` | Override active destination per-invocation (same as `--destination=`) |
| `LIB_DIR` | No | `/usr/local/lib/jabali-backup` (or in-repo `lib/`) | Where to source collectors, restorers, and helpers from |
| `CFG_SERVER_BACKUP_STALWART_DATA` | No | `true` | Set to `false` to skip the `/var/lib/stalwart-mail` snapshot (same as `--skip-stalwart-data`). DKIM keys and mail data will not be restorable |
| `JABALI_BACKUP_VERSION` | No | baked in at install | Self-identification string stamped into `server-manifest.json` |

All `CFG_*` values listed in the section reference above can also be
overridden via environment variables of the same name — they are re-read
after `cfg_load` runs if already set in the environment.
<!-- AUTO-GENERATED:env-vars-end -->
