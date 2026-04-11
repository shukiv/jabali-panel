# Jabali Backup — Per-User Data Specification

> Reference doc for the jabali-backup team. Describes every data category that must
> be captured (and restored) for a complete, lossless user backup.

## Quick Reference

| # | Category | Source | Backup Method | Restore Order |
|---|----------|--------|---------------|---------------|
| 1 | Panel DB rows | MySQL `jabali` database | SQL dump (filtered) | 1st |
| 2 | System user | `/etc/passwd`, `/etc/shadow`, `/etc/group` | `getent` export | 2nd |
| 3 | Home directory | `/home/{user}/domains/` | File copy (restic) | 3rd |
| 4 | MySQL databases | `{user}_*` databases | `mysqldump --single-transaction` | 4th |
| 5 | MySQL users & grants | `mysql.user` where User LIKE `{user}\_%` | `SHOW CREATE USER` + `SHOW GRANTS` | 5th |
| 6 | Nginx vhosts | `/etc/nginx/sites-available/{domain}.conf` | File copy | 6th |
| 7 | PHP-FPM pool | `/etc/php/{ver}/fpm/pool.d/{user}.conf` | File copy | 7th |
| 8 | Nginx cache zone | `/etc/nginx/jabali/cache-zones/{user}.conf` | File copy | 8th |
| 9 | SSL certificates | `/etc/letsencrypt/live/{domain}/` | File copy (or re-issue) | 9th |
| 10 | DNS zones | PowerDNS | `pdnsutil list-zone {domain}` | 10th |
| 11 | Mail data | Stalwart / `/var/mail/vhosts/{domain}/` | File copy | 11th |
| 12 | Crontab | `crontab -u {user} -l` | Text export | 12th |
| 13 | Redis ACL + data | Redis server | `ACL GETUSER` + key dump | 13th |
| 14 | WordPress metadata | `/home/{user}/.wordpress_sites` | File copy (included in #3) | — |
| 15 | User dotfiles | `/home/{user}/.domains`, `.redis_credentials`, `.wp-cli/` | File copy (included in #3) | — |

---

## Category Details

### 1. Panel Database Rows

The `jabali` MySQL database holds the user's configuration state. Export rows
from these tables filtered by `user_id` (or via foreign keys through `domains`).

**Direct user_id tables:**

| Table | Filter | Notes |
|-------|--------|-------|
| `users` | `id = {user_id}` | Core user record, hosting_package_id, disk_quota, is_admin, locale, etc. |
| `domains` | `user_id = {user_id}` | All hosted domains |
| `cron_jobs` | `user_id = {user_id}` | Scheduled tasks |
| `mysql_credentials` | `user_id = {user_id}` | Encrypted DB passwords |
| `git_deployments` | `user_id = {user_id}` | Git auto-deploy configs |
| `cloudflare_zones` | `user_id = {user_id}` | CF integration |
| `user_settings` | `user_id = {user_id}` | Per-user preferences |
| `audit_logs` | `user_id = {user_id}` | Optional — can be large, consider skipping |
| `imap_sync_tasks` | `user_id = {user_id}` | Mail migration tasks |

**Via domain_id (from `domains` table):**

| Table | Filter | Notes |
|-------|--------|-------|
| `email_domains` | `domain_id IN (...)` | Mail domain configs |
| `dns_records` | `domain_id IN (...)` | Custom DNS entries |
| `ssl_certificates` | `domain_id IN (...)` | Cert metadata (issued_at, expires_at, status) |
| `domain_aliases` | `domain_id IN (...)` | Alias/pointer domains |
| `domain_redirects` | `domain_id IN (...)` | HTTP redirects |
| `domain_hotlink_settings` | `domain_id IN (...)` | Hotlink protection rules |
| `domain_bandwidth_usage` | `domain_id IN (...)` | Historical bandwidth — optional |

**Via email_domain_id (from `email_domains` table):**

| Table | Filter | Notes |
|-------|--------|-------|
| `mailboxes` | `email_domain_id IN (...)` | Email accounts (password_hash, quota, flags) |
| `email_forwarders` | `email_domain_id IN (...)` | Forwarding rules |
| `autoresponders` | `email_domain_id IN (...)` | Auto-reply configs |
| `mailbox_shares` | `email_domain_id IN (...)` | Shared mailbox ACLs |

**Export format:** JSON (one file per table) or SQL inserts. JSON preferred for
portability and selective restore.

### 2. System User

```bash
getent passwd {user}   # UID, GID, home dir, shell
getent group  {user}   # Primary group
getent group sftpusers # Verify membership
```

On restore: `useradd` with same UID/GID if possible, set shell, add to
`sftpusers` group. Home dir ownership must be `root:{user}` with `0750`
(SFTP chroot requirement).

### 3. Home Directory

```
/home/{user}/
├── domains/{domain}/
│   ├── public_html/     ← web files (the bulk of the backup)
│   └── logs/            ← access/error logs (optional, can be large)
├── backups/             ← user's own backups (optional)
├── cache/nginx/         ← fastcgi cache (skip — rebuilt automatically)
├── ssl/                 ← user SSL files (if any custom certs)
├── tmp/                 ← temp files (skip)
├── .domains             ← domain registry JSON
├── .wordpress_sites     ← WP install metadata JSON
├── .redis_credentials   ← Redis ACL username + password
├── .wp-cli/             ← WP-CLI config
└── mail/                ← local mail (if Stalwart stores here)
```

**Include:** `domains/`, `.domains`, `.wordpress_sites`, `.redis_credentials`,
`.wp-cli/`, `ssl/`, `mail/`

**Exclude:** `cache/`, `tmp/`, `logs/` (optional), `node_modules/`, `vendor/`,
`.cache/`, `*.log`

### 4. MySQL Databases

All databases with the `{user}_` prefix belong to this user.

```bash
# Discover
mysql -e "SHOW DATABASES LIKE '{user}\\_%'"

# Dump each
mysqldump --single-transaction --quick {user}_wp0a2bc7 | gzip > {user}_wp0a2bc7.sql.gz
```

Use `--single-transaction` for InnoDB consistency without locking.

### 5. MySQL Users & Grants

All MySQL users with the `{user}_` prefix.

```bash
mysql -e "SELECT User, Host FROM mysql.user WHERE User LIKE '{user}\\_%'"
```

For each user, capture:
```sql
SHOW CREATE USER '{user}_wp0a2bc7'@'localhost';
SHOW GRANTS FOR '{user}_wp0a2bc7'@'localhost';
```

Store as a single `users.sql` file with `DROP USER IF EXISTS` + `CREATE USER` +
`GRANT` statements.

### 6. Nginx Virtual Host Configs

One `.conf` file per domain:

```
/etc/nginx/sites-available/{domain}.conf
```

Also capture the symlink state (enabled/disabled):

```
/etc/nginx/sites-enabled/{domain}.conf → ../sites-available/{domain}.conf
```

On restore: write the conf file, create the symlink, run `nginx -t`, reload.

### 7. PHP-FPM Pool

```
/etc/php/{version}/fpm/pool.d/{user}.conf
```

Detect PHP version dynamically — the server may have multiple PHP versions.
The pool file contains `user`, `group`, `listen` socket path, `pm` settings.

On restore: write the pool file, reload PHP-FPM.

### 8. Nginx Cache Zone

```
/etc/nginx/jabali/cache-zones/{user}.conf
```

Contains the `fastcgi_cache_path` directive. On restore: write the file and
ensure `/home/{user}/cache/nginx/` exists.

### 9. SSL Certificates

```
/etc/letsencrypt/live/{domain}/
├── privkey.pem
├── cert.pem
├── chain.pem
└── fullchain.pem
```

These are symlinks to `/etc/letsencrypt/archive/{domain}/`. Back up the actual
files (resolve symlinks), not the symlinks themselves.

**Alternative:** Skip backing up certs and re-issue via `jabali ssl:issue
{domain}` after restore. This is simpler and avoids stale certs, but requires
DNS to already point to the new server.

### 10. DNS Zones

Export from PowerDNS:

```bash
pdnsutil list-zone {domain}
```

Store as `{domain}.zone` text files. On restore: `pdnsutil create-zone {domain}`
then `pdnsutil load-zone {domain} {file}`.

**Note:** Also export DNSSEC keys if the zone is signed:
```bash
pdnsutil show-zone {domain}   # shows DNSSEC status + keys
pdnsutil export-zone-dnskey {domain}
```

### 11. Mail Data

Stalwart stores mail in one of:

- `/var/mail/vhosts/{domain}/{localpart}/` (Maildir)
- `/var/lib/stalwart-mail/` (Stalwart's internal storage, user-partitioned)

Back up the panel DB rows (`mailboxes`, `email_forwarders`, `autoresponders`)
for account metadata (password hashes, quotas, flags). Back up the actual mail
files from disk.

On restore: recreate mail accounts via panel DB + Stalwart API, then rsync mail
files.

### 12. Crontab

```bash
crontab -u {user} -l > {user}.crontab
```

On restore:

```bash
crontab -u {user} {user}.crontab
```

### 13. Redis ACL & Data

**ACL user:**
```bash
redis-cli ACL GETUSER jabali_{user}
```

**Credentials** are in `/home/{user}/.redis_credentials` (backed up with home dir).

**Cached data** (keys prefixed `{user}:`): Usually not worth backing up — cache
rebuilds naturally. Include only if user has persistent data in Redis.

On restore: `redis-cli ACL SETUSER jabali_{user} ...` with the saved password.

---

## Restore Order

Restore must follow this order to satisfy dependencies:

```
1.  Panel DB: users row              (creates user_id for FK references)
2.  System user: useradd             (creates home dir, UID/GID)
3.  Panel DB: hosting_packages link  (if user references a package)
4.  Panel DB: domains                (creates domain_id for FK references)
5.  Panel DB: email_domains          (creates email_domain_id for FKs)
6.  Panel DB: all child tables       (mailboxes, dns_records, ssl_certificates, etc.)
7.  Home directory files             (rsync domains/, dotfiles)
8.  MySQL databases + users          (import dumps, recreate grants)
9.  Nginx vhosts + cache zone        (write configs, nginx -t, reload)
10. PHP-FPM pool                     (write config, reload fpm)
11. SSL certificates                 (copy or re-issue)
12. DNS zones                        (pdnsutil load-zone)
13. Mail data                        (rsync mail files)
14. Crontab                          (crontab -u {user} restore)
15. Redis ACL                        (ACL SETUSER)
16. Fix permissions                  (chown -R, setfacl for www-data)
```

### Post-Restore Checks

After restoring, verify:

- [ ] `nginx -t` passes
- [ ] PHP-FPM pool is running (`systemctl status php*-fpm`)
- [ ] Each domain responds to HTTP requests
- [ ] MySQL databases are accessible with restored credentials
- [ ] WordPress sites load (if applicable — check `.wordpress_sites`)
- [ ] Mail accounts can authenticate
- [ ] DNS resolves correctly
- [ ] SSL certs are valid (or re-issued)
- [ ] Cron jobs are scheduled (`crontab -u {user} -l`)

---

## Manifest Format

Each backup should include a `manifest.json` at the root:

```json
{
  "version": "3.0",
  "created_at": "2026-04-11T12:00:00+00:00",
  "username": "shuki",
  "user_id": 42,
  "server_hostname": "jabali-prod-1",
  "domains": ["example.com", "mysite.org"],
  "databases": ["shuki_wp0a2bc7", "shuki_app1"],
  "mailboxes": ["info@example.com", "admin@mysite.org"],
  "dns_zones": ["example.com", "mysite.org"],
  "ssl_certificates": ["example.com"],
  "cron_jobs": 3,
  "has_redis": true,
  "panel_db_tables": [
    "users", "domains", "email_domains", "mailboxes",
    "email_forwarders", "dns_records", "ssl_certificates",
    "cron_jobs", "mysql_credentials"
  ],
  "excludes": ["cache/", "tmp/", "logs/", "node_modules/", "vendor/"]
}
```

---

## What NOT to Back Up

| Item | Reason |
|------|--------|
| `cache/nginx/` | Rebuilt automatically by nginx |
| `tmp/` | Ephemeral |
| `node_modules/`, `vendor/` | Reinstalled via package managers |
| `*.log` files | Large, not needed for restore |
| `sessions` table | Ephemeral login sessions |
| `failed_jobs`, `jobs`, `job_batches` | Queue state, not user data |
| `impersonation_tokens` | Security-sensitive, short-lived |
| `personal_access_tokens` | Should be regenerated |
| `server_imports`, `server_import_accounts` | Migration state, not user data |
| `settings` (global) | Server-level, not per-user |
| `panel_certificates` | Server-level panel cert |
