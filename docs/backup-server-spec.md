# Jabali Backup — Full Server Data Specification

> Reference doc for the jabali-backup team. Describes every component that must be
> captured to perform a complete server restore on a fresh machine.
>
> For per-user backup details, see [backup-data-spec.md](backup-data-spec.md).

## Overview

A full server backup has two layers:

1. **Server layer** — services, configs, panel app, databases, systemd units
2. **User layer** — all user accounts and their data (see backup-data-spec.md)

This document covers layer 1. The restore process installs Jabali on a fresh
server, then restores server-level state, then restores each user account.

---

## Quick Reference

| # | Category | Source | Backup Method | Restore Order |
|---|----------|--------|---------------|---------------|
| 1 | Panel database (`jabali`) | MariaDB | `mysqldump --single-transaction` | 1st |
| 2 | PowerDNS database (`powerdns`) | MariaDB | `mysqldump --single-transaction` | 2nd |
| 3 | Panel application | `/var/www/jabali/` | File copy | 3rd |
| 4 | Panel environment | `/var/www/jabali/.env` | File copy (encrypted) | 4th |
| 5 | Jabali config | `/etc/jabali/` | File copy | 5th |
| 6 | Nginx config | `/etc/nginx/` | File copy (selective) | 6th |
| 7 | PHP-FPM config | `/etc/php/` | File copy (selective) | 7th |
| 8 | FrankenPHP / Caddyfile | `/etc/jabali/Caddyfile` | File copy | 8th |
| 9 | Panel SSL cert | `/etc/ssl/jabali/` | File copy | 9th |
| 10 | Stalwart mail config | `/etc/stalwart-mail/` | File copy | 10th |
| 11 | Redis config + data | `/etc/redis/`, `/var/lib/redis/` | File copy + RDB | 11th |
| 12 | MariaDB config | `/etc/mysql/` | File copy | 12th |
| 13 | Systemd units | `/etc/systemd/system/jabali-*` | File copy | 13th |
| 14 | Restic password | `/etc/jabali/restic-password` | File copy (encrypted) | 14th |
| 15 | Agent addons | `/etc/jabali/agent.d/` | File copy | 15th |
| 16 | Custom templates | `/etc/jabali/welcome.html`, `suspended.html` | File copy | 16th |
| 17 | Let's Encrypt state | `/etc/letsencrypt/` | File copy | 17th |
| 18 | Webmail (Bulwark) | `/opt/bulwark/` | File copy or rebuild | 18th |
| 19 | All user accounts | `/home/*/` + per-user data | Per-user backup | 19th |
| 20 | Package manifest | `dpkg --get-selections` | Text export | metadata |

---

## Category Details

### 1. Panel Database (`jabali`)

The core panel database. Contains all server settings, user records, domains,
email configs, SSL metadata, cron jobs, audit logs, etc.

```bash
mysqldump --single-transaction --quick --routines --triggers jabali | gzip > jabali.sql.gz
```

**Server-level tables** (not tied to a specific user):

| Table | Content |
|-------|---------|
| `settings` | Global server settings (branding, defaults, feature flags) |
| `hosting_packages` | Package definitions (disk, bandwidth, domain limits) |
| `panel_certificates` | Panel SSL cert metadata |
| `dns_settings` | PowerDNS connection and default SOA settings |
| `server_imports` | Migration job history |
| `server_import_accounts` | Migration account details |
| `notification_logs` | Admin notification history |

**User-level tables** (also in this database — backed up as part of per-user):

| Table | FK Chain |
|-------|----------|
| `users` | root table |
| `domains` | `users.id` |
| `email_domains` | `domains.id` |
| `mailboxes` | `email_domains.id` |
| `email_forwarders` | `email_domains.id` |
| `autoresponders` | `email_domains.id` |
| `mailbox_shares` | `email_domains.id` |
| `dns_records` | `domains.id` |
| `ssl_certificates` | `domains.id` |
| `domain_aliases` | `domains.id` |
| `domain_redirects` | `domains.id` |
| `domain_hotlink_settings` | `domains.id` |
| `domain_bandwidth_usage` | `domains.id` |
| `cron_jobs` | `users.id` |
| `mysql_credentials` | `users.id` |
| `git_deployments` | `users.id` |
| `cloudflare_zones` | `users.id` |
| `user_settings` | `users.id` |
| `imap_sync_tasks` | `users.id` |
| `audit_logs` | `users.id` (optional — large) |

**Skip these tables** (ephemeral):

| Table | Reason |
|-------|--------|
| `sessions` | Active login sessions — ephemeral |
| `cache`, `cache_locks` | Laravel cache — rebuilt |
| `jobs`, `job_batches`, `failed_jobs` | Queue state — ephemeral |
| `impersonation_tokens` | Security tokens — short-lived |
| `personal_access_tokens` | Should be regenerated |
| `server_processes` | Runtime process list |
| `password_reset_tokens` | Short-lived |

### 2. PowerDNS Database (`powerdns`)

Separate MySQL database for DNS zone data. Contains all DNS zones and records
managed by PowerDNS.

```bash
mysqldump --single-transaction --quick powerdns | gzip > powerdns.sql.gz
```

This is the authoritative source for DNS — the `dns_records` table in the panel
DB is a mirror. Restoring this database restores all DNS zones for all domains.

Also export DNSSEC keys if any zones are signed:
```bash
pdnsutil list-all-zones > zones.txt
for zone in $(cat zones.txt); do
    pdnsutil show-zone "$zone" >> dnssec-state.txt
done
```

### 3. Panel Application

```
/var/www/jabali/
```

The Laravel application directory. In practice this is a git checkout — you can
restore by cloning the repo and running `composer install` + `npm run build`.

**If backing up as files**, include:
- `app/`, `config/`, `routes/`, `resources/`, `database/`, `bin/`, `stubs/`
- `composer.json`, `composer.lock`, `package.json`, `package-lock.json`
- `public/` (includes `public/storage` symlink)
- `storage/app/` (uploaded files — logos, branding)

**Exclude:**
- `vendor/` (reinstalled via `composer install`)
- `node_modules/` (reinstalled via `npm ci`)
- `storage/framework/cache/`, `storage/framework/views/`
- `bootstrap/cache/`
- `.git/` (if restoring from repo clone)

**Preferred restore method:** `git clone` + `composer install` + `npm run build`
+ `php artisan migrate` + `php artisan storage:link`.

### 4. Panel Environment File

```
/var/www/jabali/.env
```

Contains database credentials, app key, panel hostname, port, mail settings,
and all secrets. **Must be encrypted** in the backup.

Critical keys:
```
APP_KEY=              # Laravel encryption key — losing this breaks all encrypted data
DB_PASSWORD=          # MariaDB password
PANEL_HOSTNAME=       # Server hostname
PANEL_PORT=           # Panel port (default 8443)
SERVER_HOSTNAME=      # Used for DNS and mail
JABALI_AGENT_SOCKET=  # Agent socket path
```

### 5. Jabali Config Directory

```
/etc/jabali/
├── stalwart-api.conf       # Stalwart admin API URL + auth token
├── Caddyfile               # FrankenPHP config (see #8)
├── restic-password         # Restic repo encryption password (see #14)
├── health-monitor.json     # Health monitor thresholds
├── welcome.html            # Custom welcome page template (optional)
├── suspended.html          # Custom suspension page template (optional)
└── agent.d/                # Agent addon PHP files (see #15)
    ├── jabali-backup.php
    └── jabali-security.php
```

Back up the entire directory. The `stalwart-api.conf` and `restic-password`
contain secrets — **encrypt in backup**.

### 6. Nginx Config

```
/etc/nginx/
├── nginx.conf                          # Main config (customized by Jabali)
├── jabali/                             # Jabali-managed includes
│   ├── includes/                       # PHP upstream, security headers
│   └── cache-zones/                    # Per-user fastcgi cache zones
│       └── {user}.conf
├── sites-available/
│   ├── {domain}.conf                   # Per-domain vhosts
│   └── jabali-{hostname}.conf          # Panel reverse proxy vhost
└── sites-enabled/                      # Symlinks to sites-available
```

**Back up:**
- `nginx.conf`
- `jabali/` (entire directory)
- `sites-available/*.conf` (all vhost files)
- Symlink state in `sites-enabled/`

**Skip:** `fastcgi_params`, `mime.types`, `modules-*` — these come from the
nginx package and are restored by reinstalling.

### 7. PHP-FPM Config

```
/etc/php/{version}/fpm/
├── php-fpm.conf                # Global FPM config
├── php.ini                     # PHP settings for FPM
├── pool.d/
│   ├── www.conf                # Default pool
│   ├── admin.conf              # Panel admin pool
│   └── {user}.conf             # Per-user pools
```

Back up per PHP version installed. Detect versions:
```bash
ls /etc/php/
```

Per-user pool files (`{user}.conf`) are also covered in the per-user backup.
The server backup should capture `php-fpm.conf`, `php.ini`, `www.conf`, and
`admin.conf`.

### 8. FrankenPHP / Caddyfile

```
/etc/jabali/Caddyfile
```

FrankenPHP configuration for the panel itself (port 8443, TLS, PHP handler).
This is the panel's web server — separate from nginx which serves user domains.

### 9. Panel SSL Certificate

```
/etc/ssl/jabali/
├── panel.crt       # Panel TLS certificate
└── panel.key       # Panel TLS private key
```

Used by FrankenPHP to serve the admin/user panel over HTTPS.

### 10. Stalwart Mail Server Config

```
/etc/stalwart-mail/
├── config.toml     # Main Stalwart config
├── tls.toml        # TLS settings (cert paths per domain)
└── ...             # Additional config files
```

Also back up Stalwart data if mail is stored in its internal DB:
```
/var/lib/stalwart-mail/         # Mail data, DKIM keys, indexes
```

**DKIM keys** are critical — losing them means email authentication breaks for
all domains until new keys are generated and DNS is updated.

### 11. Redis Config + Data

**Config:**
```
/etc/redis/redis.conf
```

Contains ACL definitions for per-user Redis users (`jabali_{user}`).

**Data (optional):**
```
/var/lib/redis/dump.rdb         # RDB snapshot
/var/lib/redis/appendonly.aof   # AOF log (if enabled)
```

Redis data is mostly cache — skipping it is acceptable. The ACL config (user
passwords and permissions) is the important part and lives in `redis.conf`.

### 12. MariaDB Config

```
/etc/mysql/
├── mariadb.conf.d/
│   └── 50-server.cnf           # Main server config
├── debian.cnf                  # Maintenance credentials
└── my.cnf                      # Symlink to global config
```

The `debian.cnf` file contains the maintenance user password — **encrypt in
backup**.

### 13. Systemd Service Units

Custom systemd units created by the installer:

| Unit | Purpose |
|------|---------|
| `jabali-panel.service` | FrankenPHP panel server |
| `jabali-agent.service` | Root agent (Unix socket) |
| `jabali-queue.service` | Laravel queue worker |
| `jabali-health-monitor.service` | Health check daemon |
| `jabali.slice` | Resource control slice (optional) |
| `stalwart-mail.service` | Stalwart mail server |
| `bulwark.service` | Webmail frontend |
| `php{ver}-fpm-panel.service` | Panel-specific FPM (if separate) |
| `nginx.service.d/ensure-logs.conf` | Nginx override |

```bash
# Export all jabali-related units
ls /etc/systemd/system/jabali-* /etc/systemd/system/jabali.slice \
   /etc/systemd/system/stalwart-mail.service \
   /etc/systemd/system/bulwark.service \
   /etc/systemd/system/nginx.service.d/ \
   2>/dev/null
```

### 14. Restic Backup Password

```
/etc/jabali/restic-password
```

The encryption password for the restic backup repository. Without this, existing
restic snapshots are unrecoverable. **Must be encrypted** in the server backup
and stored separately (e.g., in a password manager).

### 15. Agent Addons

```
/etc/jabali/agent.d/
├── jabali-backup.php       # Backup addon (this tool's own RPC routes)
├── jabali-security.php     # Security addon (firewall, WAF)
└── ...                     # Any other addons
```

These PHP files are loaded by the agent at startup and register additional RPC
routes. Back up the entire directory.

### 16. Custom Templates

```
/etc/jabali/welcome.html        # Custom domain welcome page (optional)
/etc/jabali/suspended.html      # Custom suspension page (optional)
```

Only present if the admin has customized them. Falls back to
`/var/www/jabali/stubs/` defaults if missing.

### 17. Let's Encrypt State

```
/etc/letsencrypt/
├── accounts/               # LE account keys (critical)
├── archive/                # Actual cert files
├── live/                   # Symlinks to current certs
│   ├── {domain}/
│   │   ├── privkey.pem
│   │   ├── cert.pem
│   │   ├── chain.pem
│   │   └── fullchain.pem
│   └── ...
├── renewal/                # Renewal configs per domain
└── cli.ini                 # Certbot defaults
```

The `accounts/` directory is critical — it contains the ACME account private
key. Without it, you cannot manage existing certificates.

Back up the entire `/etc/letsencrypt/` directory. Alternatively, skip it and
re-issue all certs after restore (requires DNS pointing to the new server).

### 18. Webmail (Bulwark)

```
/opt/bulwark/
```

Bulwark is a Next.js JMAP webmail client served under `/webmail/` via nginx
proxy to port 3000. It has Jabali-specific patches applied via `patch_bulwark()`
in `install.sh`.

**Preferred restore:** Reinstall via `install.sh`'s Bulwark section (downloads
upstream, applies patches, builds). This is more reliable than copying the built
files.

**If backing up as files:** include the entire `/opt/bulwark/` directory minus
`node_modules/` and `.next/cache/`.

### 19. All User Accounts

Run the per-user backup (see [backup-data-spec.md](backup-data-spec.md)) for
every user in `/home/`:

```bash
ls /home/ | grep -v lost+found
```

Each user backup captures: home dir files, MySQL databases, nginx vhosts,
FPM pools, mail data, DNS zones, SSL certs, crontabs, Redis ACLs.

### 20. Package Manifest

Record installed packages and versions for reproducibility:

```bash
dpkg --get-selections > packages.txt
apt-mark showmanual > manual-packages.txt
php -v > php-version.txt
nginx -v 2> nginx-version.txt
mysql --version > mysql-version.txt
```

This is metadata only — used to verify the restore target has compatible
versions, not for direct restoration.

---

## Restore Order

Full server restore on a fresh machine:

```
Phase 1: Base Install
─────────────────────
1.  Run install.sh on fresh server (installs all packages, creates dirs)
2.  Restore /var/www/jabali/.env (with correct APP_KEY)
3.  Restore /etc/jabali/ (stalwart-api.conf, Caddyfile, restic-password)

Phase 2: Databases
──────────────────
4.  Import jabali.sql.gz → MySQL `jabali` database
5.  Import powerdns.sql.gz → MySQL `powerdns` database
6.  Run php artisan migrate (applies any new migrations)

Phase 3: Service Configs
────────────────────────
7.  Restore /etc/nginx/nginx.conf + /etc/nginx/jabali/
8.  Restore /etc/php/*/fpm/ configs
9.  Restore /etc/stalwart-mail/ configs
10. Restore /etc/redis/redis.conf
11. Restore /etc/ssl/jabali/ (panel cert)
12. Restore systemd units (if customized)
13. Restore /etc/letsencrypt/ (or re-issue certs later)

Phase 4: User Accounts
───────────────────────
14. For each user: run per-user restore (see backup-data-spec.md)
    - System user (useradd)
    - Home directory files
    - MySQL databases + users
    - Nginx vhosts
    - PHP-FPM pools
    - SSL certs
    - DNS zones
    - Mail data
    - Crontab
    - Redis ACL

Phase 5: Finalize
──────────────────
15. Restore /opt/bulwark/ (or rebuild via install.sh)
16. Restore agent addons (/etc/jabali/agent.d/)
17. systemctl daemon-reload
18. Restart all services
19. Run post-restore checks
```

### Post-Restore Checks

```bash
# Service health
systemctl status jabali-panel jabali-agent jabali-queue nginx \
    stalwart-mail redis-server mariadb pdns

# Panel accessible
curl -sk https://localhost:8443/jabali-admin/login

# Nginx valid
nginx -t

# PHP-FPM running
systemctl status php*-fpm

# Database accessible
mysql -e "SELECT COUNT(*) FROM jabali.users"

# DNS responding
dig @localhost example.com

# Mail server responding
curl -s http://localhost:8080/healthz   # Stalwart health endpoint

# All user domains respond
for conf in /etc/nginx/sites-enabled/*.conf; do
    domain=$(basename "$conf" .conf)
    curl -sk --resolve "$domain:443:127.0.0.1" "https://$domain" -o /dev/null -w "%{http_code} $domain\n"
done
```

---

## Server Manifest Format

Each server backup should include a `server-manifest.json`:

```json
{
  "version": "3.0",
  "type": "server",
  "created_at": "2026-04-11T12:00:00+00:00",
  "hostname": "jabali-prod-1",
  "ip_address": "203.0.113.10",
  "os": "Ubuntu 24.04 LTS",
  "jabali_version": "1.2.3",
  "php_versions": ["8.4"],
  "nginx_version": "1.24.0",
  "mariadb_version": "11.4.2",
  "stalwart_version": "0.10.0",
  "powerdns_version": "4.9.0",
  "users": ["shuki", "demo", "client1"],
  "user_count": 3,
  "domain_count": 12,
  "database_count": 8,
  "databases_backed_up": ["jabali", "powerdns"],
  "components": {
    "panel_app": true,
    "env_file": true,
    "jabali_config": true,
    "nginx_config": true,
    "php_fpm_config": true,
    "frankenphp_config": true,
    "panel_ssl": true,
    "stalwart_config": true,
    "redis_config": true,
    "mariadb_config": true,
    "systemd_units": true,
    "letsencrypt": true,
    "bulwark": true,
    "agent_addons": ["jabali-backup", "jabali-security"],
    "restic_password": true
  },
  "excludes": [
    "vendor/", "node_modules/", "storage/framework/cache/",
    "bootstrap/cache/", ".git/"
  ]
}
```

---

## What NOT to Back Up

| Item | Reason |
|------|--------|
| `/var/www/jabali/vendor/` | Reinstalled via `composer install` |
| `/var/www/jabali/node_modules/` | Reinstalled via `npm ci` |
| `/var/www/jabali/storage/framework/cache/` | Rebuilt by Laravel |
| `/var/www/jabali/storage/framework/views/` | Compiled Blade cache — rebuilt |
| `/var/www/jabali/bootstrap/cache/` | Auto-generated |
| `/home/*/cache/nginx/` | Rebuilt by nginx |
| `/home/*/tmp/` | Ephemeral |
| `/var/log/` | Logs — large, not needed for restore |
| `/opt/bulwark/node_modules/` | Reinstalled on build |
| `/opt/bulwark/.next/cache/` | Build cache |
| Queue/session/cache DB tables | Ephemeral state |

---

## Secrets Inventory

These files contain secrets and **must be encrypted** in the backup:

| File | Secret |
|------|--------|
| `/var/www/jabali/.env` | APP_KEY, DB password, all API keys |
| `/etc/jabali/stalwart-api.conf` | Stalwart admin API token |
| `/etc/jabali/restic-password` | Restic repo encryption password |
| `/etc/mysql/debian.cnf` | MariaDB maintenance password |
| `/etc/redis/redis.conf` | Redis ACL passwords (inline) |
| `/home/*/.redis_credentials` | Per-user Redis passwords |

Never store these unencrypted in a backup. Use restic's built-in encryption
(restic-password) or an additional encryption layer for non-restic backups.
