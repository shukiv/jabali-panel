# Quickstart

For brand-new hosts. Assumes Debian 13 (Trixie), root, public IPv4, and DNS pointed at the box.

## 1. Install

```bash
curl -fsSL https://get.jabali-panel.com | bash
```

The installer brings up MariaDB, PowerDNS (auth + recursor), PHP-FPM (all current Sury versions), Stalwart mail, CrowdSec, Kratos, and the Jabali panel itself. End-to-end takes 8–15 minutes on a fresh VPS.

Override the upstream resolver if `:53/udp` outbound is blocked at the lab firewall:

```bash
JABALI_DNS_FORWARDER=192.168.1.1 curl -fsSL https://get.jabali-panel.com | bash
```

## 2. First login

The installer prints the panel URL, the admin username and a generated password at the end. Sign in with them; you land in `/jabali-admin/dashboard`.

If you missed them, set a new password (or print a recovery URL with `--link`):

```bash
jabali user password <username>
```

## 3. Set the panel hostname

Server Settings → General → **Panel Hostname**. Pick the FQDN you want for the panel itself (e.g. `panel.example.com`). The reconciler will issue a Let's Encrypt cert for it within ≤60 seconds and reload nginx + panel + Bulwark.

## 4. Create your first hosted user

Users → **Create User**. Pick a username, email, package (or "default"), and primary domain. The system creates:

- A Linux account (login disabled — SFTP only).
- A per-user PHP-FPM pool socket.
- An nginx vhost for the primary domain.
- A home directory under `/home/<user>` with quota.
- Mail account + DKIM keys (if the domain runs mail).

## 5. Issue SSL for the domain

Domains → **Edit** → SSL → toggle on. The reconciler does HTTP-01 over the existing port-80 vhost within ≤60 seconds.

## 6. Add a database (optional)

Databases → **Create**. Pick MariaDB or PostgreSQL, set a DB user, get a password (shown once). Click "Open phpMyAdmin" to SSO into the DB UI.

## 7. Install WordPress (optional)

Applications → pick the domain → **WordPress** → Install. The wizard provisions the DB, downloads the latest WP, runs `wp core install`, and writes the magic SSO file so the user can one-click into `/wp-admin/` from the panel.

You're live.

## Next

- [admin.md](./admin.md) — the admin shell tour.
- [user.md](./user.md) — what your hosting customers see.
- [security.md](./security.md) — CrowdSec baseline, AppSec WAF, malware scanning.
- [backups.md](./backups.md) — destinations + schedules.
