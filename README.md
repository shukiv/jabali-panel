<div align="center">

  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="panel-ui/public/images/jabali_logo_dark.png">
    <img src="panel-ui/public/images/jabali_logo.png" alt="Jabali Panel" width="120">
  </picture>

# Jabali Panel

**A modern hosting control panel for WordPress and PHP hosting, built with Go and React.**

<p>
  <a href="https://jabali-panel.com/">Website</a>
  &nbsp;|&nbsp;
  <a href="https://demo.jabali-panel.com">Live demo</a>
  &nbsp;|&nbsp;
  <a href="#installation">Install</a>
  &nbsp;|&nbsp;
  <a href="#architecture">Architecture</a>
  &nbsp;|&nbsp;
  <a href="#cli">CLI</a>
</p>

<p>
  <img src="https://img.shields.io/badge/status-release_candidate-f59e0b" alt="Release candidate">
  <img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go 1.25">
  <img src="https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black" alt="React 19">
  <img src="https://img.shields.io/badge/Ant_Design-5-0170FE?logo=antdesign&logoColor=white" alt="Ant Design 5">
  <img src="https://img.shields.io/badge/MariaDB-11-003545?logo=mariadb&logoColor=white" alt="MariaDB 11">
  <img src="https://img.shields.io/badge/Debian-13-A81D33?logo=debian&logoColor=white" alt="Debian 13">
  <img src="https://img.shields.io/badge/License-AGPL--3.0-blue" alt="AGPL-3.0">
</p>

<p>
  Multi-tenant isolation. Root-safe automation. Database-driven reconciliation.
  A single panel binary serves the API and embedded SPA, while privileged host
  operations are delegated to a root-owned Unix-socket agent.
</p>

</div>

> [!NOTE]
> Jabali Panel is currently a release candidate. Expect rapid iteration and
> breaking changes until 1.0.

## Demo and Website

- Website: https://jabali-panel.com/
- Demo: https://demo.jabali-panel.com

### Demo mode (build-tag gated, on `main`)

The public demo at `https://demo.jabali-panel.com` runs **demo mode**. As of
JAB-159 it lives on `main`, gated **out of production artifacts at compile time**
(see [ADR-0160](docs/adr/0160-build-tag-demo-mode.md)). It is not a runtime toggle
and there is no `feat/demo-mode` branch to rebase.

What demo mode adds (present only in a demo build):

- write-blocking middleware — every non-idempotent `/api/v1/*` request
  (POST/PUT/PATCH/DELETE) returns `403 {"error":"demo_mode"}`, so visitors can
  browse every read endpoint without ever reaching the agent or a DB write;
- a `/info` endpoint that **exposes the seeded demo credentials**;
- a fixed **DEMO banner** + "Enter as admin / Enter as user" buttons.

**Why this is safe on `main`.** The demo Go code carries `//go:build demo` and the
demo UI is gated behind `import.meta.env.VITE_DEMO === "1"`, so a **production build
contains none of it** — the write-block, the credential-exposing `/info`, and the
banner/login override are physically absent from a non-demo binary and SPA bundle.
CI proves it on every PR (`make demo-guard` + the `demo-guard` / `ui-unit` jobs
assert a production build has no `demo_mode` / `jabali-demo-banner` markers while a
demo build does). This is strictly stronger than the old "keep it off a branch and
hope no one flips a flag" model.

**Operating the demo:** mark the host with `echo demo > /etc/jabali/deploy-profile`.
`install.sh` and every `jabali update` then build the panel with `-tags demo` and the
SPA with `VITE_DEMO=1` automatically — the demo tracks `main` with no rebasing.
Production hosts leave the file absent (the default) and never carry demo code.

## Installation

One-line install on a fresh Debian 13 box — launches the **TUI installer** by
default (pick a deploy profile + optional modules, then watch a live progress
pane):

```
curl -fsSL https://get.jabali-panel.com | sudo bash
```

`get.jabali-panel.com` serves [`bootstrap.sh`](bootstrap.sh): it downloads the
latest sha256-verified release tarball, extracts the prebuilt `jabali-installer`
binary + `install.sh`, and runs the installer against the real terminal (works
even piped through `curl | bash`). Add args after `-s --`, e.g. `--dry-run`.

Classic / scripted install (no TUI — the proven bash installer directly):

```
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-panel/main/install.sh | sudo bash
```

Both run the same engine. `install.sh` fetches Go 1.25, builds the panel + agent
binaries, builds the SPA with Vite, writes systemd units, provisions MariaDB +
Redis + PowerDNS + Stalwart + Bulwark + CrowdSec (per the selected modules), and
smoke-tests `/health`. Idempotent — re-run to upgrade. Set `JABALI_MODULES=…`
(comma list) or leave the TUI to build it for a minimal / modular install.

Optional flags:

- `--debug` show full output instead of spinner
- `--hostname <fqdn>` override auto-detected hostname
- `JABALI_HOSTNAME=<fqdn>` env-var equivalent for unattended installs

Uninstall (rolls back system packages, optionally keeps `/home`):

```
curl -fsSL https://get.jabali-panel.com | sudo bash -s -- --uninstall
```

After install:

- Admin panel: `https://your-host:8443/jabali-admin`
- User panel: `https://your-host:8443/jabali-panel`
- Webmail: `https://mail.your-domain/`

The panel API listens on a Unix socket; nginx terminates TLS on `:8443` and
proxies upstream. If nginx goes down, the panel and agent stay running so
operators can recover via `jabali` CLI without losing in-flight state.

## Highlights

- Per-user Linux accounts with per-user PHP-FPM master + cgroup v2 + POSIX quota
- SSH shell access via nspawn containers with auto-start and idle timeout
- Root agent for SSL, mail, DNS, backups, migrations — fronted by a typed
  NDJSON RPC contract over `/run/jabali-agent/agent.sock` (no shelling out
  from the panel)
- DB-as-truth model: a 60s reconciler reads the panel DB and converges nginx
  vhosts, PHP pools, mailboxes, DKIM, DNS, SSL, mta-sts, and per-user limits
- cPanel and WHM migrations (analyse → fix-perms → validate → restore) with
  preserved MySQL users + password hashes
- IMAP sync for migrating mail from external servers
- Stalwart Mail Server with browser-trusted IMAPS/465/587 (LE cert pushed
  into Stalwart Certificate object) and self-deleting SSO file (Installatron
  pattern) for one-click webmail
- Per-mailbox forwarders, autoresponders, shared folders, disclaimers
- Bulwark (Next.js JMAP) webmail with same-origin per-tenant routing
  (nginx `sub_filter` rewrites panel hostname → `$host` so the SPA stays
  same-origin on `mail.<tenant>`)
- PowerDNS authoritative + recursor with native DNSSEC (per-domain toggle)
- Per-domain listen-IP binding (M24 IP Manager) with reserved-word-safe
  migrations against MariaDB 11.x
- Per-domain opt-in nginx FastCGI micro-cache with safe-bypass for cart /
  admin / authenticated cookies
- Restic backups (account_full + system_backup) with encryption, dedup,
  SFTP / S3 destinations, scheduled + on-demand
- WordPress 1-click install / delete / clone (M10) — 15-app catalogue (M19)
  incl. Moodle / Joomla / NextCloud / OpenCart / Mautic / Drupal
- Per-user resource limits: cgroups v2 slice drop-in, nginx limit_req,
  POSIX quota — admin toggleable, reconciler-converged
- Integrated security suite: CrowdSec parsers + AppSec WAF + per-user
  egress firewall (nftables + cgroupv2-vmap) + ModSecurity replaced by
  CrowdSec AppSec (ADR-0060) + LMD + ClamAV-on-demand + YARA + Tetragon
  for malware detection + jabali quarantine + M14 notifications dispatch
- 6-channel notifications: Discord, ntfy, Web Push (VAPID), SMS,
  Email, Webhook, Slack, in-app bell — 4 event sources incl. cert renew,
  disk full, service down, CrowdSec spike
- One-time login tokens (CLI + dashboard) with IP binding
- Magic-link-free SSO between panel and webmail (no Hydra / OIDC overhead —
  ADR-0040 supersedes the M16 Hydra rollback)
- Audit logs, account activity feed, encrypted diagnostic-log sharing to
  support, in-app updates + support tabs

## Feature Map

### Admin Panel

- Dashboard with stats, health, recent activity, notifications bell
- User management with suspension, packages, quotas, impersonation
- Server settings (hostname, nameservers, public IPs, panel cert)
- Service manager for systemd services + start/stop/restart
- PHP version and per-user pool management (server-wide extensions tab)
- DNS zones, templates, DNSSEC, secondary NS
- SSL issuance and renewals (panel cert + per-domain)
- IP address assignments (managed IP pool, per-domain bind)
- Backups: account_full + system_backup, local + remote (SFTP/S3),
  schedules, encrypted destinations
- Migrations (cPanel restore, WHM downloads, IMAP sync)
- Security: CrowdSec allowlists / alerts / console / captcha + UFW + AppSec
  geoblock + per-user egress firewall + malware quarantine
- Updates + Support tabs: live `jabali update` with transient systemd units,
  enclosed-encrypted diagnostic sharing to webmaster
- Server status (CPU / mem / disk / queues / 5s polling)
- Database admin ops (curated tuner, root password, processlist,
  pmaAdmin SSO)
- Email queue, throttles, MTA-STS, outbound reports
- Audit logs, notifications dispatcher, jabali-isolator events
- Notification channel admin (test-send, scopes, throttles)

### User Panel

- Domains, redirects, custom nginx rules, listen-IP, FastCGI micro-cache
- DNS records editor with conflict detection (CNAME exclusivity per
  RFC 1034 §3.6.2) and dedup
- Mail: domains, mailboxes, forwarders, autoresponders, catch-all,
  disclaimers (HTML), shared folders, mail logs
- IMAP sync (single + bulk)
- Webmail SSO (Bulwark, Next.js JMAP)
- WordPress (install, update, scan, SSO) + 14 other 1-click apps
- File manager (AntD-native) + SFTP + SSH keys
- SSH shell access via nspawn containers (idle timeout)
- Databases (MariaDB + Postgres in tabbed view) with phpMyAdmin SSO
- PHP settings per account
- SSL management
- Cron jobs (systemd-user timers + allowlist)
- Backups + restore (account_full)
- Logs, statistics, bandwidth usage (daily nginx-log sync)
- Support access link generator (one-time IP-bound tokens)
- Notification preferences (Discord, ntfy, Web Push, SMS, Email)

### Platform

- Root-level agent (`panel-agent`) with typed NDJSON RPC handler registry
- Reconciler (60s tick) converges domain.create / SSL / DKIM / vhosts /
  PHP pools / nginx rate-limits / mailboxes / mtasts / ssh keys / cron
- Job queue: async backup + migration steps + WordPress install
- Health monitor with notification dispatch on service down / cert near-
  expiry / disk-full / CrowdSec spike / queue depth
- Redis (Unix socket, ACL-scoped) for cache, sessions, notifications
  dispatcher streams
- Per-domain opt-in FastCGI micro-cache + manual purge
- Multi-language UI (en default; i18n harness ready)

## Architecture

- **Control plane**: Go binary `panel-api` (Gin) listening on
  `/run/jabali-panel/api.sock`. Embeds the SPA and serves it from `/`
- **Data plane**: Go binary `panel-agent` running as root, listening on
  `/run/jabali-agent/agent.sock` (0660, group `jabali`). Typed NDJSON RPC
  registry — every privileged op (nginx reload, certbot, systemctl,
  mysql DDL, file ops) is a named handler the panel calls by name. No
  shelling out from the panel itself
- **State plane**: MariaDB `jabali_panel` (single DB, single writer = panel-
  api). Reconciler reads the DB every 60s and converges the host
- **Job plane**: Redis Streams dispatcher (notifications, backups, mail-
  scan)
- **Frontend**: React 19 + Ant Design 5 + TanStack Query, built by Vite,
  served from the Go binary's embedded FS — single deploy unit, no Node
  runtime on the host
- **Webmail**: Bulwark (Next.js JMAP) at `/opt/jabali-webmail`, served on
  `mail.<tenant>` per-domain via nginx → Unix socket
- **SSH shell**: nspawn containers (debian-13-v1 image) for SSH access
  isolation; jabali-isolator handles container lifecycle
- **Security**: CrowdSec parsers + AppSec (nginx-bouncer Lua, WAF) +
  per-user egress firewall (nftables + cgroupv2-vmap, ADR-0084) + LMD +
  ClamAV-on-demand + YARA + Tetragon
- **Logging**: structured JSON via slog; nginx access logs feed CrowdSec
- **Server metrics**: live `/proc` reads, no Prometheus exporter dependency

Service stack (single-node default):

- **panel-api** (Go, Unix socket, embedded SPA)
- **panel-agent** (Go, Unix socket, root)
- **nginx** (TLS terminator on `:8443`, user vhosts on `:80`/`:443`,
  per-domain mail vhost on `:443`, FastCGI cache keyzone shared, AppSec
  bouncer Lua)
- **MariaDB** (Unix socket only — `skip-networking`)
- **Redis** (Unix socket, mode 0660, group `jabali-sockets`)
- **PowerDNS authoritative** (split-port :5300, MySQL backend) +
  **pdns-recursor** (loopback :53, resolver chain)
- **Stalwart Mail Server** (SMTP / IMAP / 465 / 587 / 993 / JMAP /
  ManageSieve, LE-cert pushed into Certificate object)
- **Bulwark** (Next.js JMAP webmail, Unix socket, served per-tenant)
- **Kratos** (Unix socket admin + public, sole auth source — M20)
- **CrowdSec** (LAPI socket + AppSec :7422 + nginx-bouncer Lua)
- **Restic** (encrypted, dedup, backup destinations)
- **jabali-isolator** (nspawn container lifecycle)
- **systemd-user** (cron jobs as user-scope timers)

## Requirements

- Fresh Debian 13 install (no pre-existing web or mail stack)
- 2 GB RAM minimum, 4 GB recommended — the same guidance as cPanel (on a ≤4 GB host the installer adds swap and caps the on-box SPA/Go build memory so it doesn't OOM; on 2 GB the build is slower but completes)
- A domain for panel + mail (with glue records if hosting DNS)
- PTR (reverse DNS) for mail hostname
- Open ports: 22, 80, 443, 8443, 25, 465, 587, 993, 995, 53

## Security Hardening

See [`docs/adr/`](docs/adr/) for the full architectural-decision record
(110+ ADRs covering every load-bearing design choice). Highlights:

### Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `JABALI_HOSTNAME` | Override auto-detected panel hostname during install | (auto) |
| `JABALI_PANEL_BIND` | Override panel-api listen socket | `/run/jabali-panel/api.sock` |
| `JABALI_AGENT_SOCKET` | Override agent RPC socket | `/run/jabali-agent/agent.sock` |
| `JABALI_TEST_DATABASE_URL` | Real MariaDB DSN for integration tests | (unset) |
| `JABALI_LOG_LEVEL` | Slog level (debug / info / warn / error) | `info` |
| `TLS_CERT` / `TLS_KEY` | Cleaned from panel.env on update — nginx terminates | (auto-cleaned) |

### Key Security Features

- Panel never runs as root. Every privileged op crosses the agent Unix
  socket as a typed RPC call; agent verifies caller via `SO_PEERCRED`
- Shell arguments validated + escaped per-handler (no `sh -c $arg`
  patterns); domain names validated against `validateDomainNameForShell`
- DKIM private keys + SSO tokens + mailbox plaintexts encrypted at rest
  via AES-GCM with a per-host SSO key (`/etc/jabali-panel/sso.key`)
- One-time admin SSO tokens are 256-bit, single-use, 5-minute TTL,
  reaped every 30s by systemd timer (ADR-0040 webmail SSO file pattern)
- Stalwart Certificate object pushed from LE-renewed cert on each
  certbot deploy-hook — IMAPS / 465 / 587 always serve browser-trusted
  cert (no rcgen self-signed fallback)
- CrowdSec AppSec WAF + per-user egress firewall (cgroupv2-vmap, ADR-0084)
- Self-healing reconciler — config drift on disk is reverted on next
  tick; operator hand-edits to nginx vhosts are lost-by-design
- CSP, HSTS, SameSite cookies, X-Forwarded-Proto handled by nginx
- Migrations are schema-only (no app-populated tables seeded by SQL)
- Audit log on every admin write + impersonation start/stop
- Pre-commit + CI gates: `go vet`, `go test -race ./...`, `npx tsc -b`,
  `bash -n install.sh`, Playwright E2E, AppSec geoblock golden tests

## Updates

Update the panel (code, dependencies, DB migrations, infrastructure):

```
jabali update
```

This pulls the latest code, rebuilds the panel + agent binaries, rebuilds
the SPA, applies golang-migrate migrations, syncs nginx vhosts + systemd
units + PHP config + CrowdSec acquis, and restarts the panel + agent.
Safe to run on a live server — the reconciler tolerates a brief panel
restart and converges state on the next tick.

Self-heal a broken install (7 detectors, --diagnose default,
--auto safe, --all --yes destructive):

```
jabali repair --diagnose       # report only
jabali repair --auto           # fix safe issues
jabali repair --all --yes      # destructive recovery
```

## CLI

The `jabali` command uses a noun:verb pattern. All commands support
`--json` for machine-readable output and `--yes` to skip confirmations.

```
jabali user        list|create|delete|show|password|suspend|unsuspend|admin
jabali domain      list|create|delete|show|enable|disable|email-enable|email-disable
jabali db          list|create|delete|users|user-create|user-delete|tune|root-password
jabali mailbox     list|create|delete|passwd|set-quota|forwarder|autoresponder|shares
jabali ssl         list|status|check|issue|renew|panel|panel-issue
jabali dns         list|records|add|delete-record|sync|dnssec-enable|dnssec-disable
jabali backup      list|create|delete|info|restore|password|destinations|schedules
jabali cron        list|create|delete|toggle|run
jabali php         list|install|uninstall|default|extensions
jabali service     list|status|start|stop|restart|enable|disable
jabali system      info|status|disk|memory|hostname|kill
jabali wp          list|install|delete|update|scan|import
jabali agent       ping|status|restart|log
jabali cpanel      analyze|restore|fix-permissions
jabali login       token [--user=] [--ttl=15] [--panel=]
jabali logs        share [--raw] [--ttl=86400]
jabali ufw         migrate-ip-bans            # M43 CrowdSec single IP-trust
jabali repair      --diagnose|--auto|--all
jabali panel-primary set|show                 # ADR-0048 primary mail domain
jabali nspawn      list|build|update|delete
jabali malware-purge                          # M33 retention sweep
jabali update      [--force]
```

See [`docs/CONVENTIONS.md`](docs/CONVENTIONS.md) for the full repo-wide
patterns (route families, SearchableTable, Drawer-for-CRUD, list envelope,
rate limits) and [`docs/adr/`](docs/adr/) for every load-bearing decision.

## Development

```
make build              # compile panel-api + panel-agent
make run                # run panel-api (dev, embedded SPA)
make test               # all Go tests, race detector on
make test-coverage      # coverage report (internal packages)
make test-integration   # needs JABALI_TEST_DATABASE_URL + real MariaDB
make coverage-check     # fail if combined coverage < 80%
make lint               # golangci-lint v2
make fmt                # go fmt + vet
```

Frontend dev (from `panel-ui/`):

```
npm install
npm run dev             # Vite on http://localhost:5173
                        # proxies /api and /health to 127.0.0.1:8443
```

E2E (from `panel-ui/`):

```
npm run test:e2e        # Playwright against the dev server
```

See [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) for the full feature
development workflow (research → plan → TDD → review → ship).

### Versioning

The version string lives in `VERSION` (read at build time and exposed
via `/health`). When the installer clones the repo for a fresh install,
it reads `VERSION` to display the installed version. Always bump `VERSION`
in the same commit as the corresponding `install.sh` changes — drift
shows up as a mismatched footer and installer banner.

## Translations

[![Translation status](https://translate.jabali-panel.com/widget/jabali-panel/panel-ui/svg-badge.svg)](https://translate.jabali-panel.com/engage/jabali-panel/)

The panel UI is translated on our self-hosted Weblate at
**<https://translate.jabali-panel.com/>**. English is the source language; every
other locale is written by translators there and merged back into this repo.

Shipping locales: English (source), Arabic, Simplified Chinese, French, German,
Hebrew, Italian, Japanese, Brazilian Portuguese, Russian, Spanish, Turkish and
Ukrainian. Arabic and Hebrew render right-to-left.

### For translators

Sign up at [translate.jabali-panel.com](https://translate.jabali-panel.com/engage/jabali-panel/)
and start translating — no coding, no git, no local setup. Read the project's
translation instructions there first; the short version:

- Do not translate product or protocol names (`nginx`, `MariaDB`, `Stalwart`,
  `PHP-FPM`, `DNS`, `SSL`, ...) or anything in `backticks`.
- Keep placeholders such as `{{count}}` exactly as they appear. You may move
  one to fit your word order, but never rename or drop it.
- Keep strings short. Most are buttons, menu items, table headers and form
  labels, and a long translation breaks the layout.
- Hebrew and Arabic render right-to-left. If a screen looks broken, report it
  rather than shortening the text to work around it.
- If an English string is ambiguous or wrong, comment on it instead of
  guessing — it gets fixed at the source, once, for every language.

Missing your language? Open an issue and we will add it.

### For developers

- **`panel-ui/src/locales/en/common.json` is the only catalog edited by hand.**
  Weblate owns every other locale file; hand edits there will be overwritten.
- User-facing text goes through `t("some.key")` (`react-i18next`), never a
  hardcoded literal. Add the key and its English text to `en/common.json` in the
  same commit that introduces it.
- `panel-ui/src/i18n.ts` holds the locale list plus the AntD and dayjs locale
  maps and the RTL set. Adding a locale means touching all three.
- A key with no translation yet falls back to the English source, so a partially
  translated locale never renders blank labels.

## Repository Layout

```
jabali-panel/
├── panel-api/          # Go HTTP server (Gin) + reconciler + agent RPC client
│   ├── cmd/server/     # main entry
│   ├── internal/       # api/, auth/, repository/, reconciler/, config/, ...
│   └── migrations/     # golang-migrate SQL (000xxx_*.up/.down.sql)
├── panel-agent/        # Go binary running as root; typed NDJSON RPC handlers
│   ├── cmd/jabali-agent/
│   └── internal/commands/
├── panel-ui/           # React SPA (AntD + TanStack Query)
│   ├── src/            # shells/, components/, theme/, pages/, ...
│   └── public/
├── agentwire/          # NDJSON RPC types shared by panel-api + panel-agent
├── internal/           # shared Go libs (cronvalidate, dbtuning, phpext, ...)
├── install/            # install.sh assets (nginx tmpl, stalwart plan,
│                       # letsencrypt deploy hooks, bulwark env, ...)
├── docs/               # CONVENTIONS, BLUEPRINT, adr/, runbooks/, KNOWN_ISSUES
├── plans/              # per-milestone implementation blueprints
├── .github/workflows/  # CI (Go + vitest + E2E)
├── install.sh          # single-supported install path (curl | sudo bash)
├── config.example.toml # reference config (copied to /etc/jabali-panel/)
├── Makefile            # build / test / lint targets
└── go.mod              # Go workspace root
```

## License

AGPL-3.0 — see [LICENSE](LICENSE).

## Mail Subdomain

Visiting `mail.<your-domain>` in a browser routes to webmail (Bulwark) via
the per-domain nginx vhost. The vhost installs an nginx `sub_filter` that
rewrites the panel hostname to the requested `$host` in Bulwark's
`/api/config` and Stalwart's `/.well-known/jmap` responses, so the SPA
stays same-origin on `mail.<tenant>` and Stalwart's JMAP Session URLs
never leak the panel hostname.

`autodiscover` / `autoconfig` paths are excluded so mail client
auto-discovery (Thunderbird, Outlook) keeps working.

## Documentation

See the [`docs/`](docs/) directory for detailed guides:

- [Conventions](docs/CONVENTIONS.md) — repo-wide patterns (route families,
  SearchableTable, Drawer for create+edit, icon shim, list envelope, rate
  limits) + anti-patterns learnt the hard way
- [Blueprint](docs/BLUEPRINT.md) — full feature map + milestone roadmap
- [ADRs](docs/adr/) — every load-bearing architectural decision (110+)
- [Plans](plans/) — per-milestone implementation blueprints
- [Runbooks](plans/) — operational guides for SSL, mail, M16 rollback,
  M22 SSO rework, M27 CrowdSec extensions, M30/M30.1 backups
- [Known Issues](docs/KNOWN_ISSUES.md) — caveats + workarounds
- [Contributing](docs/CONTRIBUTING.md) — feature development workflow
- [Translations](https://translate.jabali-panel.com/) — Weblate instance; English
  is the source catalog (`panel-ui/src/locales/en/common.json`)
- [Environment](docs/ENV.md) — full env-var reference
