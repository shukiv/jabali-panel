# Platform — Components

The full third-party inventory shipped or installed by Jabali on a Debian 13 host. Every component listed here is fetched, configured, and started by `install.sh`; nothing requires manual operator install.

Version pins shown are the values in `install.sh` at the time of writing. Run `jabali --version` plus `dpkg-query -l` for the live values on a deployed host.

## Runtime services

| Component | Version | Role | Upstream license |
|---|---|---|---|
| **jabali-panel** | this repo | Go + Gin HTTP panel API; serves the React SPA; the only writer to the panel DB | AGPL-3.0 |
| **jabali-agent** | this repo | Root-privileged process; performs every privileged host operation over `/run/jabali-agent.sock` | AGPL-3.0 |
| **Stalwart Mail** | 0.16.0 | SMTP + IMAP + JMAP + mailbox store (single binary) | AGPL-3.0 |
| **Bulwark** | 1.4.14 | Node + Next.js standalone — SPA fallback, autoconfig / autodiscover, magic-link bridge | own |
| **Ory Kratos** | 26.2.0 | Identity (login, 2FA, recovery); Unix sockets only | Apache-2.0 |
| **nginx** | Debian native | Reverse-proxy + per-vhost server. Sury-nginx purged defensively | BSD-2 |
| **PHP-FPM** (Sury) | 8.1–8.5 | One systemd unit per version; per-user pools | PHP License |
| **MariaDB** | 11.x (pinned in CI) | Panel DB + tenant DBs; `skip-networking` (M25.1) | GPL-2 |
| **PostgreSQL** | Debian 17 | Tenant DBs only; opt-in | PostgreSQL |
| **Redis** | Debian | Notification dispatcher stream + panel cache | BSD-3 |
| **PowerDNS Authoritative** | Debian | Authoritative `:53` for hosted zones, MariaDB backend | GPL-2 |
| **pdns-recursor** | Debian | Loopback recursor `127.0.0.1:53` (split-port, ADR-0047) | GPL-2 |
| **CrowdSec** | packagecloud | IP-trust source + AppSec WAF + bouncers | MIT |
| **certbot** | Debian | Let's Encrypt issuance and renewal | Apache-2.0 |

## Security stack

| Component | Source | Role |
|---|---|---|
| `crowdsec` + `crowdsec-nginx-bouncer` + `cs-firewall-bouncer` + AppSec | packagecloud + hub | IP bans, scenarios, WAF |
| CrowdSec community blocklists | `cscli hub` | Pushed blocklists synced via console |
| **Snuffleupagus** | repo | PHP runtime hardening (Zend extension) |
| **AppArmor** + `apparmor-profiles-extra` + `apparmor-utils` | Debian | Per-process MAC; profiles shipped for every Jabali service |
| **AIDE** + `aide-common` | Debian | Daily host-integrity scan |
| **auditd** + `audispd-plugins` | Debian | Linux audit subsystem |
| **bubblewrap** | Debian | Per-user PHP sandbox + SSH chroot for migrations |
| **UFW** | Debian | Port baseline only (IP decisions live in CrowdSec — M43, ADR-0089) |
| **nftables** | Debian | Per-user egress (cgroup v2 vmap, M34, ADR-0084) |
| **Linux Malware Detect (LMD)** | 2.0.1-rc4 (GitHub) | On-demand malware scanner — native HEX + YARA |
| **YARA-X** (`yr` binary) | 1.15.0 (GitHub) | Pattern matching for LMD + the M33.2 async mail scanner |

## Apps / tooling

| Component | Version | Role |
|---|---|---|
| **phpMyAdmin** | 5.2.3 | MariaDB web UI with single-use SSO (`sso.php`) |
| **Adminer** | 6.0.1 | Lighter DB web UI + `jabali-sso-plugin.php` |
| **WP-CLI** | 2.12.0 | WordPress automation |
| **GoAccess** | Debian | nginx log analyzer |
| **Roundcube** | Debian + install snippet | Webmail (served by Bulwark vhost) |
| **restic** | Debian | Backup engine (deduplicated, encrypted, multi-destination) |
| **Go toolchain** | 1.25.1 | Build agent + panel-api |
| **Node.js** | NodeSource current LTS | Bulwark runtime + UI build |

## OS plumbing

systemd, `systemd-container`, `systemd-resolved` (gated under `JABALI_DNS_FORWARDER`), `auditd`, OpenSSH (`Match Group jabali-sftp`), `tar`, `curl`, `wget`, `gnupg`, `ca-certificates`, `git`, `build-essential`, `libpcre2-dev`, `acl`, `jq`, `socat`.

## Frontend (`panel-ui`)

| Library | Version | Purpose |
|---|---|---|
| **React** | 18.3.1 | SPA |
| **Ant Design** | 6.3.6 | Component library |
| `@ant-design/icons` | 6.1.1 | Icons |
| **TanStack Query** (`@tanstack/react-query`) | 5.99 | API state |
| **react-router** | 7.1.1 | Routing |
| **axios** | 1.7.9 | HTTP client |
| **Monaco Editor** (`@monaco-editor/react`) | 4.7 | In-browser code editor (File Manager) |
| **xterm.js** + addon-fit | 6.0 / 0.11 | Web terminal |
| `@dnd-kit/core` + `sortable` + `utilities` | 6.3 / 10 / 3.2 | Drag-and-drop |
| `lucide-react` + `react-icons` | 1.9 / 5.6 | Extra icons |
| `micro-key-producer` | 0.8.5 | Browser-side key generation |
| `@fontsource/inter` | 5.2.8 | Self-hosted font |
| Vite 6 / TypeScript 5.7 / Vitest 3.2 / Playwright 1.59 / Testing Library | — | Build + test toolchain |

## Go libraries (direct dependencies in `go.mod`)

`gin-gonic/gin` (HTTP), `gorm.io/gorm` + `gorm.io/driver/mysql` (ORM), `golang-migrate/migrate/v4` (migrations), `redis/go-redis/v9` (Redis), `gorilla/websocket` (WebSocket), `spf13/cobra` (CLI), `robfig/cron/v3` (schedules), `oklog/ulid/v2` (IDs), `fxamacker/cbor/v2` (encoding), `SherClockHolmes/webpush-go` (VAPID Web Push), `BurntSushi/toml`, `google/shlex`, `golang.org/x/crypto|net|sync|sys|term|time`.

Test-only: `DATA-DOG/go-sqlmock`, `alicebob/miniredis/v2`, `stretchr/testify`.

## In-house patterns shipped under `install/`

- **`jabali-sso-<43-char-nonce>.php`** — self-deleting magic SSO file (M22, ADR-0040; Installatron / Softaculous style). Used by Roundcube webmail and every application install for one-click admin sign-in. 60 s TTL; 256-bit nonce filename; `flock` + `unlink` on first hit.
- **phpMyAdmin `sso.php`** and **Adminer `jabali-sso-plugin.php`** — adapt the magic-file pattern to the two DB web UIs.
- **`install/snuffleupagus/rules/`** — server-wide Snuffleupagus baseline rules.
- **`install/wp-cli.sha256`** / **`install/phpmyadmin.sha256`** — checksum-pinned upstream tarballs.

## Removed

Components that previously shipped but are not present in the current build. See [Removed Features](../removed-features.md) for the rationale per item.

| Component | Removed in | Replacement |
|---|---|---|
| **Hydra** (OIDC provider) | M16 rollback | Magic-file SSO (M22) for app sign-in |
| **ModSecurity** + libmodsecurity + nginx-modsecurity-connector + OWASP CRS | M27 | CrowdSec AppSec WAF |
| **filebrowser** | M11 | In-panel AntD File Manager |
| **Tetragon** + `jabali-tetragon-relay` | M39 (2026-04-30) | (none — eBPF tripwires retired) |
| **ClamAV daemon + freshclam timer** | M33 | On-demand `clamscan`, now itself superseded by LMD-native + YARA-X |
| **`@refinedev/*`** | M21 | TanStack Query + AntD + react-router |

The installer's `cleanup_*` functions actively purge leftover packages and config from these components on every install, so a host that previously ran an older Jabali generation does not carry a footprint forward.

## Source-of-truth pointers

If you need the live, currently-installed version of any component on a deployed host:

```bash
dpkg-query -W -f='${Package} ${Version}\n' | grep -E 'mariadb|postgresql|redis|nginx|pdns|certbot|crowdsec|stalwart|kratos|auditd|aide|apparmor|nftables|ufw|restic|bubblewrap'
stalwart-mail --version
kratos version
jabali --version
```

To see exactly which versions are pinned for the next install:

```bash
grep -E 'VERSION="|version="|local .+_version=' install.sh | grep -v '^[[:space:]]*#'
```
