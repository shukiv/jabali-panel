# User Shell

Hosting customers log in at `/jabali-panel`. Pages:

| Page | Path | Purpose |
|---|---|---|
| Dashboard | `/jabali-panel/dashboard` | Disk/bandwidth/quota usage cards, recent activity, primary domain. |
| Profile | `/jabali-panel/profile` | Name, email, password, TOTP 2FA, recovery codes, backup card, usage card. |
| Domains | `/jabali-panel/domains` | Your hosted domains; per-domain edit (PHP version, SSL, DNSSEC, redirects, aliases). |
| DNS | `/jabali-panel/dns` | Records for your zones; per-domain DNSSEC toggle + DS record. |
| SSL | `/jabali-panel/ssl` | Cert state, force re-issue. |
| Mail | `/jabali-panel/mail` | Mail Domains list → per-domain drill-down: Mailboxes, Forwarders, Autoresponders, Catch-all, Disclaimer, Shared Folders, CalDAV/CardDAV, Logs + Statistics. |
| Databases | `/jabali-panel/databases` | Your MariaDB / PostgreSQL DBs + DB users (tabbed). SSO into phpMyAdmin (MariaDB) / Adminer (PostgreSQL); per-database Download + Restore-from-file. |
| PHP Settings | `/jabali-panel/php-settings` | Per-user + per-domain `memory_limit`, `upload_max_filesize`, `display_errors`, OPcache/JIT, Xdebug, Composer version, extra extensions (within your package's allowed range). |
| Files | `/jabali-panel/files` | AntD-native file manager (no separate filebrowser daemon); chunked upload with cancel + live speed, async copy/move/extract progress. |
| FTP Accounts | `/jabali-panel/ftp` | Create / reset / delete FTP/SFTP subaccounts scoped to a subdirectory (if admin enabled it for your package). |
| SSH Keys | `/jabali-panel/ssh-keys` | Add / revoke SSH public keys (used for SFTP via `Match Group`). |
| Cron | `/jabali-panel/cron` | Schedule allowlisted commands (`php`/`wp`/`python`/`node`, or an ordered sequence) as systemd-user timers. |
| Applications | `/jabali-panel/applications` | One-click install for WP + 14 others on a domain or subdir. |
| Backups | `/jabali-panel/backups` | Download your `account_full` backup; restore from a snapshot, or upload a backup archive and restore selected legs (files / DB / mail) yourself. |
| Notifications | `/jabali-panel/notifications` | Your own notification channels + routing (when the admin has enabled tenant notifications). |
| Logs | `/jabali-panel/logs` | nginx access + error tails for your domains; FPM error log. |
| Activity | `/jabali-panel/activity` | Read-only view of audit rows owned by you. |

## What you can change vs. what the admin controls

You can change (within package limits): PHP version, php.ini values, SSL enable/disable per domain, DNSSEC per domain, DNS records, mailboxes, forwarders, autoresponders, catch-all, disclaimer, shared folders, DB + DB user creation, file contents, cron jobs, SSH keys, app installs.

Admin controls (and you cannot override): disk + bandwidth quota, max mailboxes / DBs / domains, per-user `limit_req` rate, per-user egress firewall scope, allowed PHP version range, allowed cron command allowlist, package assignment.

## Mailbox login

Webmail is **Bulwark** (Next.js JMAP) at `https://mail.<domain>/`. One-click SSO from a mailbox in `/jabali-panel/mail` → "Open Webmail" — uses the M22 self-deleting `jabali-sso-*.php` file (Installatron/Softaculous pattern; 60 s TTL, 256-bit nonce filename, `flock` + `unlink`).

## App login

For app installs the panel writes a single-use, self-deleting magic SSO file inside the install directory; clicking "Open Admin" in the Applications tab redirects to it. The file deletes itself on first use or after 60 s.
