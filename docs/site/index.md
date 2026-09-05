# Jabali Panel

Jabali is an open-source Linux web-hosting control panel. Go agent + React UI, MariaDB-as-truth, reconciler-converged host state. Built as a clean rewrite of the original PHP panel; this documentation set covers the current (Go) generation.

## What you can do

**Sites & domains**
- Add hosted domains with per-domain PHP version, listen IP, SSL, DNSSEC, redirects and aliases.
- Per-domain Let's Encrypt issuance (HTTP-01) — reconciler handles renewal.
- One-click WordPress install / delete / clone, plus 14 other apps (Moodle, Drupal, Joomla, NextCloud, MediaWiki, PrestaShop, OpenCart, phpBB, Matomo, MyBB, Pixelfed, Mautic, Mahara, SuiteCRM).

**Email**
- Stalwart-based mail server (SMTP submission/relay/MTA + JMAP + IMAP) with per-user mailboxes, forwarders, autoresponders, catch-all, server-side disclaimer, shared folders, and live logs.
- Per-domain DKIM, SPF, DMARC, MTA-STS.
- Outbound throttles + deliverability dashboard.

**DNS**
- PowerDNS authoritative on :53 with split-port recursor for local lookups.
- Per-domain DNSSEC (KSK + ZSK auto-generated, DS record displayed for parent registrar publication).

**Storage & databases**
- MariaDB and PostgreSQL databases per user, with phpMyAdmin (MariaDB) / Adminer (PostgreSQL) SSO, and per-database Download + Restore-from-file.
- Admin DB Ops: root-password rotation, curated config tune, maintenance, processlist + kill.
- File Manager (AntD-native — no filebrowser process); admin whole-filesystem File Manager, opt-in + deny-listed.
- SFTP via OpenSSH `Match Group` + per-user SSH key vault; FTP/SFTP subaccounts (opt-in) with optional true separate-uid isolation and WebDAV.

**Security**
- CrowdSec is the single IP-trust source (UFW handles port baseline only).
- AppSec WAF (CrowdSec) replaces the removed ModSecurity stack.
- AppArmor profiles, Snuffleupagus PHP hardening, AIDE host-integrity timer.
- Per-user egress firewall via nftables + cgroup v2 vmap.
- Malware scanning: ClamAV on-demand, Linux Malware Detect, YARA, Tetragon eBPF tripwires.

**Operations**
- Per-user resource limits: POSIX quota + cgroup v2 slice drop-ins + nginx `limit_req`.
- Backups: per-account `account_full` + whole-server `system_backup` (panel DB × 3 + OS users + sites), restic-backed, multi-destination (local / sftp / s3 / b2 / azure / gcs / rest), scheduled with retention — plus portable Full Server container backups and tenant self-service restore-from-upload.
- Notifications: in-app bell, email, Slack, Telegram, Discord, ntfy.sh, Web Push (VAPID), SMS, webhook — server-wide and opt-in tenant-owned channels.
- Server Status dashboard with service controls.
- One-click `jabali update` from the panel, encrypted diag bundle for support.
- Migration ingest from cPanel (`.tar.gz`/`cpmove`), DirectAdmin, Hestia, WHM.

## How it's built

- **Panel API** (Go + Gin) — HTTP layer, writes to MariaDB, schedules the reconciler.
- **Reconciler** — in-process loop; the only thing that re-applies derived host state on a schedule.
- **Panel Agent** — root-privileged process; every privileged host operation goes through it over a Unix socket.
- **Kratos** — identity (login / 2FA / recovery). Sockets only; no TCP `:4433/:4434`.
- **MariaDB-as-truth** — schema is authoritative; reconciler converges. No "edit on host, hope the panel notices" mode.

See [platform/stack.md](./platform/stack.md) for the full architecture.

## Audience

- **Operators / sysadmins** — install on a Debian 13 server, manage all users, set quotas, run updates.
- **Hosting customers** — log in to `/jabali-panel`, manage your own domains, mail, DBs, files.

## Where to start

- New install → [installation.md](./installation.md)
- Already installed → [quickstart.md](./quickstart.md)
- Looking for something old → [removed-features.md](./removed-features.md) (OIDC/Hydra, ModSecurity, filebrowser are gone).
