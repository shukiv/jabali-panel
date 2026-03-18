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
- `--debug` to show full command output instead of spinner

APT repository (recommended):

```
curl -fsSL https://deb.jabali-panel.com/jabali-panel.gpg | sudo gpg --dearmor -o /usr/share/keyrings/jabali-panel-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/jabali-panel-archive-keyring.gpg] https://deb.jabali-panel.com stable main" | sudo tee /etc/apt/sources.list.d/jabali-panel.list
sudo apt update
sudo apt install jabali-deps jabali-panel
```

Build packages locally:

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

## Container Deployment

Jabali can run as a single container with all 13 services managed by supervisord (MariaDB, Redis, Nginx, PHP-FPM, jabali-agent, queue-worker, cron, BIND9, OpenDKIM, Rspamd, Postfix, Dovecot, fail2ban). The `Containerfile` uses a multi-stage build based on `debian:bookworm-slim`.

### Quick Start (Docker Hub)

```bash
docker pull shukivaknin/jabali-panel:latest

docker run -d --name jabali \
  --hostname panel.example.com \
  -p 80:80 -p 443:443 \
  -p 25:25 -p 587:587 -p 993:993 -p 110:110 \
  -p 53:53/tcp -p 53:53/udp \
  -v jabali-mysql:/var/lib/mysql \
  -v jabali-storage:/var/www/jabali/storage \
  -v jabali-mail:/var/mail \
  -v jabali-home:/home \
  -v jabali-letsencrypt:/etc/letsencrypt \
  -e APP_URL=https://panel.example.com \
  -e SERVER_HOSTNAME=panel.example.com \
  --cap-add NET_BIND_SERVICE \
  --cap-add NET_RAW \
  shukivaknin/jabali-panel:latest
```

The entrypoint handles first-run initialization (database setup, key generation, migrations, self-signed SSL). Persistent data is stored in the named volumes listed above.

After the container starts, create an admin user:

```bash
docker exec -it jabali php /var/www/jabali/artisan tinker --execute="
\$u = new App\Models\User();
\$u->name = 'Admin';
\$u->username = 'admin';
\$u->email = 'admin@example.com';
\$u->password = bcrypt('changeme');
\$u->is_admin = true;
\$u->save();
"
```

Then open `https://panel.example.com/jabali-admin` to log in.

### Build from Source

Requires `auth.json` for Filament packages:

```bash
podman build --secret id=composer_auth,src=auth.json -t jabali-panel:latest .
```

## Highlights

- Per-user Linux accounts and PHP-FPM isolation
- Root agent for DNS, SSL, mail, backups, and migrations
- Health monitor with auto-restarts and alerts
- cPanel and WHM migrations with step-by-step logs
- IMAP sync for migrating mail from external servers
- Built-in mail stack with webmail SSO
- Shared mailbox folders with Dovecot ACL permissions
- `mail.domain.ext` auto-redirects to webmail
- DNS templates with optional DNSSEC
- User and server backups with schedules and retention
- WordPress management (install, updates, scans, and SSO)
- Security center with firewall, Fail2ban, ClamAV, and scanners
- Encrypted diagnostic reports with email and clipboard support
- Passphrase password generator (optional, 3 random words)
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
- Migrations (cPanel restore, WHM downloads, IMAP sync)
- Security center (firewall, Fail2ban, ClamAV, scans)
- Diagnostic report (encrypted, for support)
- Audit logs and notifications

### User Panel

- Domains, redirects, and Nginx config
- DNS records editor
- Mail domains, mailboxes, forwarders, shared folders, and per-domain disclaimers
- IMAP sync (single and bulk mail migration)
- Webmail SSO (Roundcube)
- WordPress manager (install, scan, SSO)
- File manager plus SFTP/SSH keys
- Databases (MySQL and PostgreSQL in tabbed view)
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
- Server metrics: live /proc filesystem reads

Service stack (single-node default):

- Nginx + PHP-FPM
- MariaDB (user databases)
- SQLite (panel metadata by default)
- Postfix, Dovecot, Rspamd
- BIND9 (DNS)
- Redis
- Fail2ban and ClamAV (optional)

## Requirements

- Fresh Debian 13 install (no pre-existing web or mail stack)
- A domain for panel and mail (with glue records if hosting DNS)
- PTR (reverse DNS) for mail hostname
- Open ports: 22, 80, 443, 25, 465, 587, 993, 995, 53

## Security Hardening

See [SECURITY.md](SECURITY.md) for the full security policy, architecture, and audit history.

### Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `TRUSTED_PROXIES` | Comma-separated proxy IPs/CIDRs (or `*` to trust all upstream proxies) | (unset) |
| `JABALI_INTERNAL_API_TOKEN` | Shared token for internal API calls from non-localhost | (unset) |
| `JABALI_IMPORT_INSECURE_TLS` | Disable TLS certificate verification for WHM/cPanel migration API calls | `false` |
| `SESSION_ENCRYPT` | Encrypt session data at rest | `false` |
| `SESSION_SECURE_COOKIE` | Send session cookies only over HTTPS | `false` |

### Key Security Features

- Shell arguments escaped with `escapeshellarg()` to prevent OS command injection
- Admin impersonation uses one-time IP-bound tokens; stop action requires POST + CSRF
- DKIM private keys encrypted at rest via Laravel's `encrypted` cast
- Migration API calls verify TLS certificates by default (opt-out with `JABALI_IMPORT_INSECURE_TLS`)
- Webmail SSO tokens stored in restricted directory with `0600` permissions and 5-minute expiry
- Admin backup downloads restricted to allowed directory prefixes
- WordPress page-cache API uses SHA-256 verification of `AUTH_KEY`
- CSP, HSTS, and other security headers on all panel responses
- Git deployment webhooks support signed payloads via `X-Jabali-Signature` / `X-Hub-Signature-256` (HMAC-SHA256)

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
jabali report
jabali cpanel analyze <file>
jabali cpanel restore <file> <user>
```

## Development

```
composer dev
php artisan test --compact
./vendor/bin/pint
```

### Versioning

The version string in the `VERSION` file must be kept in sync between the panel codebase and the installer (`install.sh`). When the installer clones the repository during a fresh install, it reads `VERSION` to display the installed version. If you bump the version in one place but not the other, the panel footer and installer output will show different versions.

Always update `VERSION` in a single commit that includes both the panel changes and any corresponding `install.sh` changes.

## License

GPL-3.0 — see [LICENSE](LICENSE) for details.

## Mail Subdomain

Visiting `mail.domain.ext` in a browser automatically redirects to webmail (Roundcube). Autoconfig and autodiscover paths are excluded so mail client auto-discovery continues to work.

## Documentation Notes

- cPanel Migration tabs (Domains, Databases, Mailboxes, Forwarders, SSL) only render after a backup is analyzed.
