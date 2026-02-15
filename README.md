<p align="center">
  <img src="public/images/jabali_logo.svg" alt="Jabali Panel" width="140">
</p>
<h1 align="center">Jabali Panel</h1>

A modern web hosting control panel for WordPress and general PHP hosting. Jabali focuses on clean multi-tenant isolation, safe automation, and a consistent admin/user experience. It ships with a privileged agent for root-level tasks, built-in mail and DNS management, migrations from common panels, and a security center that keeps critical services in check. The UI is designed to be fast, predictable, and easy to operate on a single server.

Version: see `VERSION` (release candidate)

This is a release candidate. Expect rapid iteration and breaking changes until 1.0.

## Demo and Website

- Website: https://jabali-panel.com/
- Demo: https://jabali-panel.com/demo/

## Installation

GitHub install:

```
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash
```

Optional flags:

- `JABALI_MINIMAL=1` for core-only install
- `JABALI_FULL=1` to force all optional components

Debian packages:

```
./scripts/build-jabali-deps-deb.sh
./scripts/build-jabali-panel-deb.sh
sudo dpkg -i ./jabali-deps_<version>_all.deb
sudo apt-get -f install -y
sudo dpkg -i ./jabali-panel_<version>_all.deb
```

After install:

- Admin panel: `https://your-host/jabali-admin`
- User panel: `https://your-host/jabali-panel`
- Webmail: `https://your-host/webmail`

## Highlights

- Per-user Linux accounts and PHP-FPM isolation
- Root agent for DNS, SSL, mail, backups, and migrations
- Health monitor with auto-restarts and alerts
- cPanel and WHM migrations with step-by-step logs
- Built-in mail stack with webmail SSO
- DNS templates with optional DNSSEC
- User and server backups with schedules and retention
- WordPress management (install, updates, scans, and SSO)
- Security center with firewall, Fail2ban, ClamAV, and scanners
- Audit logs and admin notifications

## Feature Map

### Admin Panel

- Dashboard with stats, health, and recent activity
- User management with suspension and quotas
- Service manager for systemd services
- PHP version and pool management
- DNS zones, templates, and DNSSEC
- SSL issuance and renewals
- IP address assignments
- Backups and restores (local + remote)
- Migrations (cPanel restore, WHM downloads)
- Security center (firewall, Fail2ban, ClamAV, scans)
- Audit logs and notifications

### User Panel

- Domains, redirects, and Nginx config
- DNS records editor
- Mail domains, mailboxes, and forwarders
- Webmail SSO (Roundcube)
- WordPress manager (install, scan, SSO)
- File manager plus SFTP/SSH keys
- Databases and permissions
- PHP settings per account
- SSL management
- Cron jobs
- Backups and restore
- Logs and statistics
- Protected directories

### Platform

- Root-level agent for privileged operations
- Queue-backed jobs for long-running tasks
- Health monitor with auto-restarts and alerts
- Redis ACL isolation for WordPress caching
- Multi-language UI

## Architecture

- Control plane: Laravel 12 app with Filament v5 and Livewire v4
- Data plane: root agent handling privileged operations
- Job queue: async tasks and migration steps
- Logging: panel and agent logs for troubleshooting
- Server metrics: sysstat logs via SysstatMetrics

Service stack (single-node default):

- Nginx + PHP-FPM
- MariaDB (user databases)
- SQLite (panel metadata by default)
- Postfix, Dovecot, Rspamd
- BIND9 (DNS)
- Redis
- Fail2ban and ClamAV (optional)

## Requirements

- Fresh Debian 12 or 13 install (no pre-existing web or mail stack)
- A domain for panel and mail (with glue records if hosting DNS)
- PTR (reverse DNS) for mail hostname
- Open ports: 22, 80, 443, 25, 465, 587, 993, 995, 53

## Security Hardening

- `TRUSTED_PROXIES`: comma-separated proxy IPs/CIDRs (or `*` if you intentionally trust all upstream proxies).
- `JABALI_INTERNAL_API_TOKEN`: optional shared token for internal API calls that do not originate from localhost.
- `JABALI_IMPORT_INSECURE_TLS`: optional escape hatch for remote migration discovery. Leave unset for strict TLS verification.
- Git deployment webhooks support signed payloads via `X-Jabali-Signature` / `X-Hub-Signature-256` (HMAC-SHA256).

## Upgrades

```
cd /var/www/jabali
php artisan jabali:upgrade
```

Check for updates only:

```
php artisan jabali:upgrade --check
```

## CLI

```
jabali --help
jabali backup create <user>
jabali backup restore <path> --user=<user>
jabali cpanel analyze <file>
jabali cpanel restore <file> <user>
```

## Development

```
composer dev
php artisan test --compact
./vendor/bin/pint
```

## License

MIT

## Documentation Notes

- cPanel Migration tabs (Domains, Databases, Mailboxes, Forwarders, SSL) only render after a backup is analyzed.
