# Restore Guide

## Overview

`jabali-backup restore` recovers a Jabali hosting account from a restic snapshot.
It can restore to the original location or to an alternate directory for inspection.

## Listing Available Snapshots

```bash
# All snapshots
jabali-backup list snapshots

# Snapshots for a specific user
jabali-backup list snapshots --user=alice
```

## Downloading Backups

Download snapshots as compressed archives without restoring to the live system.

### Download to File

```bash
sudo jabali-backup download alice --snapshot=latest --output=/tmp/alice-backup.tar.gz
```

### Download Multiple Users

```bash
sudo jabali-backup download alice bob charlie --snapshot=latest
```

### Download Specific Components

```bash
# Files only
sudo jabali-backup download alice --only=files

# Databases only
sudo jabali-backup download alice --only=mysql,postgres

# Email data
sudo jabali-backup download alice --only=email
```

### Stream to Stdout

For integration with panel or piping to other tools:

```bash
sudo jabali-backup download alice --snapshot=latest --output=- | gzip > backup.tar
```

The `--output=-` flag streams the tar.gz archive to stdout while keeping all log
messages on stderr, making the output suitable for piping.

### Panel Browser Download

The panel provides a download button that uses named pipe streaming:

```
GET /backup-download.php?username=alice&snapshot=latest
```

This creates a temporary FIFO at `/tmp/jabali-exports/pipe-[random]`, spawns
the CLI to write to it, and streams directly to the browser with zero disk usage.

## Basic Restore

### Restore to Original Location

```bash
sudo jabali-backup restore alice --snapshot=latest
```

This restores all components (files, databases, DNS, email, SSL, etc.) to their
original system locations.

### Restore a Specific Snapshot

```bash
sudo jabali-backup restore alice --snapshot=abc123de
```

### Restore to Alternate Directory (Inspection)

```bash
sudo jabali-backup restore alice --snapshot=latest --target=/tmp/restore-alice/
```

Files and staged data are extracted to the target directory without modifying
the live system. Use this to inspect backups before applying.

Output shows what was extracted:
```
Extracted to: /tmp/restore-alice
  home/       3369 files
  mysql/      1 database(s)
  dns/        1 zone(s)
  email/      1 mailbox(es)
  ssl/        2 cert(s)
  nginx/      1 vhost(s)
  php/        1 pool(s)
  wordpress/  1 site(s)
  metadata/   account.json

Files extracted for inspection. No changes applied to live system.
```

## Selective Restore

Restore only specific components using `--only` or skip components with `--exclude`:

```bash
# Files only (website content)
sudo jabali-backup restore alice --snapshot=latest --only=files

# Databases only
sudo jabali-backup restore alice --snapshot=latest --only=mysql,postgres

# Email only
sudo jabali-backup restore alice --snapshot=latest --only=email

# DNS + SSL
sudo jabali-backup restore alice --snapshot=latest --only=dns,ssl

# Everything except home directory files (faster for config-only restore)
sudo jabali-backup restore alice --snapshot=latest --exclude=files
```

Available components: `files`, `mysql`, `postgres`, `dns`, `email`, `ssl`, `nginx`, `php`, `cron`, `metadata`

## Dry Run

Preview what would be restored without making changes:

```bash
sudo jabali-backup restore alice --snapshot=latest --dry-run
```

## Conflict Handling

When restoring to a server where the account already exists:

| Component | Default behavior | With `--force` |
|-----------|-----------------|----------------|
| User account | Use existing record | Update record (preserves ID + password) |
| Domains | Skip if exists | Update domain config |
| MySQL databases | Create with `_restored` suffix | Drop and recreate |
| Mailboxes | Skip if exists | Update config |
| DNS records | Merge (add missing, skip duplicates) | Delete all, re-insert |
| SSL certificates | Skip if exists | Update cert + re-encrypt key |
| Nginx configs | Skip if exists | Overwrite file |
| PHP-FPM pools | Skip if exists | Overwrite file |
| Email domains | Skip if exists | Update config |
| Cron jobs | Skip if exists | Delete + re-insert |

Use `--force` to overwrite existing data:

```bash
sudo jabali-backup restore alice --snapshot=latest --force
```

## Restore Order

Components are restored in strict dependency order. Metadata runs in two phases:

1. **Metadata Phase 1** (user account creation)
   - Creates Linux system user if needed
   - Creates/updates Jabali DB user record
   - Sets RESTORE_USER_CREATED flag if user was newly created
2. **Parallel Restorers** (no inter-dependencies)
   - DNS records
   - SSL certificates
   - Email (domains, mailboxes, forwarders, autoresponders)
   - Nginx vhost configs
   - PHP-FPM pool configs
   - MySQL databases + grants + users
   - Cron jobs
3. **Files** (respects RESTORE_USER_CREATED flag)
   - Home directory files (largest component, restored last)
   - If user was just created, skips existence check
4. **Metadata Phase 2** (domain setup)
   - Creates domains in DB and filesystem
   - Only runs if user was newly created in Phase 1

## Per-Component Details

### Files

- Restores `/home/{user}/` with original ownership and permissions
- SSH keys in `.ssh/authorized_keys` are included
- If the user was just created by metadata restorer, skips the "already exists" check
  and restores immediately
- If user existed before restore, skips restore unless `--force` flag is used

### MySQL

- MySQL users are recreated from backup (users.txt)
- If user doesn't exist: creates it with a random password
- If user_id is known: records credentials in mysql_credentials table
- Each database is restored via `gunzip | mysql`
- User grants are replayed for all hosts
- If database exists (without `--force`): creates `{dbname}_restored`
- Default grants assigned to first MySQL user in users.txt

### PostgreSQL

- Role is recreated with original privileges
- Databases are restored via `pg_restore`
- Extensions are reinstalled per database

### DNS

- Records are inserted into Jabali's `dns_records` table
- Uses the JSON export (not the BIND zone file) for lossless restore

### Email

- Email domain config is re-imported (DKIM keys re-encrypted with current APP_KEY)
- Mailbox records are restored (passwords are NOT restored; users must reset)
- Mail storage files are restored to Stalwart data directory
- Ownership is set to the correct `system_uid:system_gid` per mailbox
- Forwarders, autoresponders, shares (ACL), and IMAP sync tasks are re-imported

### SSL

- Certificates and CA bundles are re-imported to the database
- Private keys are re-encrypted with the current APP_KEY

### Cron

- Cron jobs are re-imported to Jabali's database
- System crontab is restored via `crontab -u {user}`

### Nginx / PHP

- Config files are copied back to system paths
- Services are reloaded (`nginx -s reload`, `systemctl reload php*-fpm`)

### Metadata

- Domain aliases, redirects, git deployments, Cloudflare zones, webhooks,
  and user settings are restored to the database

## APP_KEY Considerations

Encrypted fields (SSL private keys, DKIM keys, API tokens) are stored decrypted
in the backup. On restore, they are re-encrypted using the current server's APP_KEY.

If restoring to a different server:
- The target server's APP_KEY will be used for re-encryption
- No access to the original APP_KEY is needed
- Provide the target APP_KEY in `/etc/jabali-backup/app-key`

## Troubleshooting

### "Snapshot not found"

Check available snapshots:
```bash
jabali-backup list snapshots --user=alice
```

### "Database already exists"

Use `--force` to drop and recreate, or restore to a different name (default behavior
adds `_restored` suffix).

### "Permission denied on mail storage"

Ensure you are running as root. Mail storage requires correct UID/GID ownership.

### "APP_KEY mismatch"

If encrypted fields fail to decrypt, verify the APP_KEY in `/etc/jabali-backup/app-key`
matches the one used when the backup was created.

## Stalwart Mail Server

### What Gets Backed Up

When Stalwart backup is enabled (`[stalwart] enabled=true`), the `stalwart` collector
exports via `stalwart-cli export account`:

| Component | Description |
|-----------|-------------|
| Emails | All messages with folder structure |
| Mailboxes | Mailbox hierarchy and metadata |
| Sieve scripts | Server-side mail filters |
| Identities | User identities / send-as addresses |
| Vacation responses | Auto-reply settings |

The export uses Stalwart's native JMAP format. If `stalwart-cli` is not available,
the collector falls back to the REST API for principal metadata only.

### Same-Server Restore (Account Recovery)

```bash
# Restore Stalwart data from the latest snapshot
sudo jabali-backup restore alice --snapshot=latest --only=stalwart
```

This re-imports the JMAP account data into Stalwart. The import is additive —
existing emails are not deleted. Use `--force` to update the principal metadata
(description, quota) even if the account exists.

### Cross-Server Migration

To migrate a mail account from one Jabali server to another:

```bash
# 1. Source server — run backup with stalwart collector
sudo jabali-backup run alice --only=email,stalwart

# 2. Transfer — either use a shared backup destination, or copy the snapshot
#    Both servers must have the same destination configured, OR:
sudo jabali-backup download alice --output=/tmp/alice-backup.tar.gz
#    Then copy to the target server

# 3. Target server — restore the account
sudo jabali-backup restore alice --only=email,stalwart --force
```

Include `email` in both backup and restore to ensure the Jabali DB metadata
(email domains, mailbox records, forwarders, autoresponders) is also migrated.
The `stalwart` collector handles mail content; the `email` collector handles
Jabali panel config.

### What Does NOT Migrate

- **Passwords** — Stalwart passwords are not exported. Users must reset their
  passwords after migration.
- **APP_KEY** — Not needed for Stalwart. Stalwart manages its own encryption
  independently of Laravel's APP_KEY.
- **Active sessions** — IMAP/JMAP sessions are not transferred.

### Selective Account Restore

The panel's restore wizard shows a "Stalwart Mail" tab listing all backed-up
accounts (e.g., `alice@example.com`, `alice@other.com`). You can deselect
individual accounts to skip them.

### Troubleshooting

#### "stalwart-cli not found"

Install stalwart-cli from the Stalwart package, or ensure the REST API fallback
is configured. Without stalwart-cli, only principal metadata is backed up — not
full JMAP account data.

#### "Connection failed"

Check that the Stalwart URL and admin token are correct:
```bash
sudo jabali-backup config test
# Shows: Stalwart: ✓ connected (http://localhost:8080)
# Or:   Stalwart: ✗ connection failed
```

#### "Stalwart backup not enabled"

Enable in the panel (Settings tab → Stalwart Mail Backup → Enable) or in the config:
```ini
[stalwart]
enabled=true
```
