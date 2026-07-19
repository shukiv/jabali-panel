# Jabali Panel — Feature Blueprint

A complete catalogue of what Jabali Panel does, how the pieces fit together, and
which code owns each capability. Use this as a map when onboarding, scoping new
features, or deciding where existing logic already lives.

> **Sources of truth.** This blueprint reflects what's shipped on `main` as of
> 2026-04-20 (M16 Wave E). Architecture rules and conventions live in `docs/adr/`. Cross-repo
> coordination with sibling projects lives in `~/projects/jabali-shared/CONTEXT.md`.

---

## 1. What Jabali Panel is

Jabali Panel is a web hosting control panel for Linux servers, built with a
**React + Refine + Ant Design** frontend and a **Go (Gin)** backend. It runs on
a single Debian/Ubuntu host and manages domains, users, hosting packages, DNS,
and (planned) email, databases, PHP pools, WordPress, and SSL certificates.

The Go backend (`panel-api/`) speaks to a privileged Go agent (`panel-agent/`)
over a Unix socket at `/run/jabali/agent.sock` (NDJSON protocol). The backend
serves the React SPA on port `8443` and exposes a REST API. A reconciler
goroutine converges database state to agent operations (domains, DNS zones,
services). The database is the single source of truth.

### Key components

- **panel-api/** — Go HTTP server (Gin) + WebSocket hub + reconciler + agent RPC client
- **panel-agent/** — Go binary running as root; executes privileged commands from panel-api
- **panel-ui/** — React SPA (Refine + Ant Design + TanStack Query)
- **install.sh** — single install path; bootstraps database, panel-api, panel-agent, PowerDNS, nginx
- **docs/adr/** — architectural decision records justifying Go agent, DB-as-truth, one-write-path, etc.

### Two personas, two route namespaces

| Role    | URL mount        | Audience                     | JWT claim |
|---------|------------------|------------------------------|-----------|
| `admin` | `/jabali-admin/` | Server operator              | `is_admin=true` |
| `user`  | `/jabali-panel/` | Hosting users (per-domain)   | `is_admin=false` |

Both authenticate via `/api/v1/auth/login` (JWT access + refresh tokens). User
records are never elevated via public API — `is_admin` is only set directly in
the database.

---

## 2. Architecture (locked-in)

Reference the ADRs for rationale; this section locks in the decisions:

1. **Go agent** — NDJSON over Unix socket, root-only, executes privileged ops.
   No PHP agent. Ever. (ADR #0001-go-agent-over-ndjson-unix-socket.md)

2. **Database is source of truth** — one flow: `Database → Generator → Config files → service reload`.
   Never describe nginx.conf, pdns.conf, etc. as truth. (ADR #0002-database-source-of-truth.md)

3. **One write path = the API** — admin actions, CLI commands, manager integrations
   all go through panel-api handlers. No direct DB writes from CLI/agent. (ADR #0003-one-write-path-the-api.md)

4. **Reconciler pattern** — in-process goroutine inside panel-api converges
   DB state to agent operations. No separate worker service. (ADR #0004-reconciler-driven-convergence.md)

5. **English-only UI** — no Refine i18n provider, no translation files.
   (ADR #0007-english-only-no-i18n.md)

6. **Sibling repos out-of-scope** — jabali-manager, jabali-security, jabali-isolator,
   jabali-tunnel, Bulwark webmail live in separate repos. (ADR #0008-sibling-repos-out-of-scope.md)

7. **Nginx file-per-vhost** — each domain has its own `/etc/nginx/sites-available/<domain>.conf`,
   regenerated from DB by reconciler + `jabali nginx regenerate --force` CLI. (ADR #0009-nginx-file-per-vhost.md)

8. **PANEL_PORT 8443** — panel listens on `0.0.0.0:8443` (configurable via `JABALI_PANEL_ADDR`).
   User sites on nginx port `443`. (ADR #0014-panel-port-8443-user-443.md)

9. **PowerDNS with MySQL backend** — separate `jabali_pdns` database, zone/record CRUD via REST API.
   (ADR #0011-powerdns-mysql-backend.md)

### Architecture diagram

```
┌──────────────────────────────────────────────────────────────┐
│  Browser (React SPA: Refine + Ant Design + TanStack Query)   │
│   ├── Admin routes: /jabali-admin/*                          │
│   └── User routes: /jabali-panel/*                           │
└──────────────────────────────────────────────────────────────┘
                 │ HTTPS :8443 (JWT in Authorization header)
                 ▼
┌──────────────────────────────────────────────────────────────┐
│  Go backend (Gin router) - panel-api/                        │
│   ├── /api/v1/*       — REST API (users, domains, dns, etc.) │
│   ├── /ws/*           — WebSocket hub (logs, stats, tasks)   │
│   ├── /reconciler/*   — admin reconcile trigger endpoints    │
│   ├── static          — compiled React build (/)             │
│   └── [Reconciler goroutine running in background]           │
└──────────────────────────────────────────────────────────────┘
                 │ every privileged op
                 ▼
┌──────────────────────────────────────────────────────────────┐
│  AgentClient → /run/jabali/agent.sock (Unix socket, root)    │
│  NDJSON RPC protocol, UTF-8 sanitization, timeout guards     │
└──────────────────────────────────────────────────────────────┘
                 │ AgentClient.Send(cmd, args)
                 ▼
┌──────────────────────────────────────────────────────────────┐
│  bin/jabali-agent (Go, root, panel-agent/)                   │
│   Handler registry: 14 commands (domain.*, dns.*, user.*,    │
│   nginx, service.*, system.*) with argument sanitization     │
└──────────────────────────────────────────────────────────────┘
                 │ exec + shell escaping
                 ▼
┌──────────────────────────────────────────────────────────────┐
│  System services: nginx, php-fpm, mariadb, postgresql,       │
│  stalwart-mail (planned), powerdns, certbot, goaccess,       │
│  redis, (optional) jabali-security, jabali-isolator          │
└──────────────────────────────────────────────────────────────┘
```

**Golden rules:**

1. React frontend never touches shell or protected files; all privileged ops flow API → agent.
2. Agent socket never exposed over network.
3. All agent command arguments use safe escaping.
4. Go API enforces JWT auth + RBAC middleware on every route.
5. React SPA holds no secrets (tokens stored in memory/sessionStorage only).

---

## 3. Repository layout

| Path | Purpose |
|------|---------|
| `panel-api/` | Go HTTP server (Gin), database models (GORM), reconciler, agent RPC client, WebSocket hub, API handlers |
| `panel-agent/` | Go binary running as root via systemd socket activation; command handler registry (14 commands) |
| `panel-ui/` | React SPA (Refine + Ant Design + TanStack Query); admin + user shells and pages |
| `install.sh` | Single supported install path; prompts + idempotency, bootstraps all services |
| `docs/adr/` | 14 architectural decision records explaining locked-in choices |
| `Makefile` | Build targets: `make server`, `make agent`, `make ui`, `make install` |
| `go.mod` / `go.sum` | Go dependencies |
| `package.json` | Node dependencies (React build) |

---

## 4. What's shipped (as of 2026-04-16)

Organized by domain. All paths verified; all commits available via `git log`.

### 4.1 Authentication & Authorization

**Shipped:** JWT access + refresh tokens, bcrypt password hashing, auth middleware,
RBAC guards (`RequireAdmin`, `RequireOwner`), bootstrap seed (admin user + package).

- **Files:** `panel-api/internal/auth/`, `panel-api/internal/middleware/`
- **Migrations:** `000001_init_users.sql`, `000002_init_refresh_tokens.sql`
- **API:** `/api/v1/auth/login`, `/api/v1/auth/refresh`, `/api/v1/auth/logout`
- **Evidence:** Commits `f7464a2`, `177bc3a` (recent); earlier: `6b5dea4`, `fb3abea`

### 4.2 Users & Hosting Packages

**Shipped:** User CRUD (create/read/update/delete), password reset, hosting packages
(CRUD), package assignment to users. Username field on users.

- **Files:** `panel-api/internal/api/users.go`, `panel-api/internal/api/packages.go`
- **Migrations:** `000003_init_hosting_packages.sql`, `000004_add_package_id_to_users.sql`,
  `000007_add_username_to_users.sql`
- **Agent commands:** `user.create`, `user.delete`, `user.password`
- **API:** `/api/v1/users`, `/api/v1/packages`, `/api/v1/me`
- **Evidence:** Commits `fb3abea`, `6b5dea4`

### 4.3 Domains & Nginx

**Shipped:** Domain CRUD, document root per domain, host-level redirects, page-level
redirects (HTTP → redirect or frame inject), nginx custom rules (6 types: allow-deny,
auth-basic, gzip, headers, rewrite, proxy-pass), enable/disable toggle, index file
priority, vhost regeneration.

- **Files:** `panel-api/internal/api/domains.go`, `panel-api/internal/redirects/`,
  `panel-api/internal/nginxrules/`, `panel-agent/internal/commands/domain_*.go`,
  `panel-agent/internal/commands/nginx.go`
- **Migrations:** `000005_create_domains.sql`, `000006_hard_delete_domains_users_packages.sql`,
  `000008_rewrite_domain_docroots.sql`, `000009_add_redirects_to_domains.sql`,
  `000010_add_index_priority_to_domains.sql`, `000011_add_nginx_rules_to_domains.sql`
- **Agent commands:** `domain.create`, `domain.delete`, `domain.list`, `domain.toggle`, `nginx`
- **API:** `/api/v1/domains`, `/api/v1/domains/{id}/redirects`, `/api/v1/domains/{id}/nginx-rules`
- **UI pages:** `AdminLayout` (domain list stub), `ServerSettingsPage` (hostname field only),
  `UserLayout`, user domain list/create pages (React routes wired but minimal)
- **Evidence:** Commits `fb3abea` (nginx rules), `0e92a04`, earlier commits down to `21e4834`

### 4.4 DNS (PowerDNS integration)

**Shipped:** DNS zones CRUD (create/read/update/delete/purge), DNS records CRUD
(type-aware form builder: A, AAAA, CNAME, MX, TXT, NS, SOA, etc.), per-zone AXFR
allow-list, per-zone NOTIFY target for secondary NS, PowerDNS MySQL backend compiler,
reconciler auto-bootstrap zones on domain create.

- **Files:** `panel-api/internal/api/dns.go`, `panel-api/internal/dnscompile/`,
  `panel-api/internal/reconciler/reconciler.go` (DNS zone reconciliation),
  `panel-agent/internal/commands/dns_zone_*.go`
- **Migrations:** `000013_create_dns_tables.sql` (zones + records + axfr_allow_list)
- **Agent commands:** `dns.zone.upsert`, `dns.zone.delete`
- **API:** `/api/v1/dns/zones`, `/api/v1/dns/zones/{id}/records`, `/api/v1/dns/zones/{id}/axfr-allow-list`,
  `/api/v1/dns/zones/{id}/notify-target`
- **UI pages:** `DNSRecordsPage` (type-aware add/edit form with managed-record safeguards)
- **Evidence:** Commits `f7464a2`, `4b87395`, `0ae7213`, `3037db1`, `cb4ae39`

### 4.5 Server Settings & System

**Shipped:** Hostname, NS1/NS2 nameservers (for zone SOA), public IPv4/IPv6 (auto-detected
during install), server-info endpoint (system uptime, Go version, agent version), agent
command to set hostname.

- **Files:** `panel-api/internal/api/system.go`, `panel-agent/internal/commands/system_*.go`,
  `panel-ui/src/shells/admin/settings/ServerSettingsPage.tsx`
- **Migrations:** `000012_create_server_settings.sql`
- **Agent commands:** `system.info`, `system.set_hostname`, `agent.version`
- **API:** `/api/v1/system/settings`, `/api/v1/system/info`, `/api/v1/system/health`
- **UI pages:** `ServerSettingsPage` (hostname, ns1/ns2, public IPs read-only)
- **Evidence:** Commits `6b5dea4`, `0e92a04`, `ab47b7d`, `bcc5f93`

### 4.6 Agent & RPC

**Shipped:** NDJSON over Unix socket (`/run/jabali/agent.sock`), UTF-8 sanitization,
configurable timeout (5s default), MockAgentClient for testing, 14 command handlers
registered, error propagation.

- **Files:** `panel-api/internal/agent/` (AgentClient, AgentError, MockAgentClient)
- **Agent registry:** `panel-agent/internal/commands/registry.go` + 14 command files
  (agent_version, domain_create, domain_delete, domain_list, domain_toggle, dns_zone_upsert,
  dns_zone_delete, nginx, service_list, system_info, system_set_hostname, user_create,
  user_delete, user_password)
- **Evidence:** Commits throughout; registry `panel-agent/internal/commands/registry.go`

### 4.7 SSL / Let's Encrypt (M5a/b/c)

**Shipped (Phase 2):** Per-domain SSL toggle, Let's Encrypt issuance with try-ACME-first
strategy, fallback to 365-day self-signed certificates on ACME failure, exponential backoff retry
scheduling (5m → 15m → 45m → 135m, capped at 4h), manual retry endpoint, SSL Manager UI pages (admin + user),
and stopgap `ssl.self_sign` agent command for emergency fallback.

- **Files:** `panel-api/internal/api/ssl.go`, `panel-agent/internal/commands/ssl_*.go`
- **Migrations:** `000014_add_ssl_enabled_to_domains.sql`, `000015_create_ssl_certificates.sql`,
  `000017_ssl_enabled_default_true.sql`, `000018_add_next_retry_at_to_ssl_certificates.sql`
- **Agent commands:** `ssl.issue`, `ssl.renew`, `ssl.revoke`, `ssl.self_sign`
- **API:** `/api/v1/domains/{id}/ssl` (GET status, POST issue, DELETE revoke), `/api/v1/domains/:id/ssl/retry` (admin-only manual retry)
- **UI pages:** SSL Manager pages for both admin and user shells; per-domain toggle + status display
- **Evidence:** Commits `ba54273`, `66ae6d2`, `34379db`, `786079a`, `fc8ff7d`
- **Related ADRs:** ADR-0017 (try-ACME-then-selfsigned-with-backoff)

### 4.9 Reconciler

**Shipped:** In-process goroutine inside panel-api that listens for domain DB changes,
issues agent commands to create/update/delete domains on agent, reconciles DNS zones
to PowerDNS MySQL backend, reconciles SSL certificates (try-ACME-first, fallback to self-signed),
runs every 30s by default (configurable), manual trigger endpoints for admin. 
Separate 1-minute ticker processes SSL ACME retries (certs with next_retry_at <= now).

- **Files:** `panel-api/internal/reconciler/reconciler.go`
- **API:** `/api/v1/reconcile/all` (admin-only), `/api/v1/reconcile/{domainID}` (admin-only)
- **Background tickers:** Main reconciler (30s), SSL retry ticker (1m)
- **Evidence:** Commits `cb4ae39`, `0ae7213`, `3037db1`, `ba54273`

### 4.9.1 Frontend UI Chrome Refactor

**Shipped:** Admin and user shells now use Refine's `<ThemedLayoutV2>` + `<ThemedSiderV2>` + `<ThemedTitleV2>` 
components out of the box (light-background default Refine theme). Per-shell sidebar filtering via 
`meta.shell: "admin" | "user"` on resources list in `App.tsx` — sidebar items render only for matching shell context.

- **Files:** `panel-ui/src/App.tsx` (resource meta), `panel-ui/src/shells/AdminLayout.tsx`, `panel-ui/src/shells/UserLayout.tsx`
- **Evidence:** Commit `e9fc63c`

### 4.8 Install script

**Shipped:** Prompts for hostname, ns1/ns2, public IPv4/IPv6 (via `/dev/tty` for curl|bash),
auto-detects main interface IP, creates /etc/jabali-panel, database setup, panel-api
binary install, panel-agent binary + systemd socket activation, PowerDNS MySQL backend
setup (merges local-address + local-ipv6 into pdns.conf), nginx reload, idempotency
guards (checks for existing config files before overwriting).

- **Files:** `/home/shuki/projects/jabali2/install.sh` (single entry point)
- **Evidence:** Commits `f8b02a5`, `98e9bb6`, `4a8487c`, `7490b4f`, `113f639`, `177bc3a`

### 4.9 Database schema (GORM migrations)

18 migrations:
1. `000001_init_users.sql` — users table
2. `000002_init_refresh_tokens.sql` — refresh_tokens table
3. `000003_init_hosting_packages.sql` — hosting_packages table
4. `000004_add_package_id_to_users.sql` — FK to packages
5. `000005_create_domains.sql` — domains table (doc_root, enabled, etc.)
6. `000006_hard_delete_*` — removes soft-delete triggers
7. `000007_add_username_to_users.sql` — username field
8. `000008_rewrite_domain_docroots.sql` — schema change for doc root paths
9. `000009_add_redirects_to_domains.sql` — redirect fields
10. `000010_add_index_priority_to_domains.sql` — index file priority
11. `000011_add_nginx_rules_to_domains.sql` — nginx_rules JSONB field
12. `000012_create_server_settings.sql` — server_settings table (hostname, ns1, ns2, public_ipv4, public_ipv6)
13. `000013_create_dns_tables.sql` — dns_zones, dns_records, dns_axfr_allow_list tables
14. `000014_add_ssl_enabled_to_domains.sql` — ssl_enabled boolean field on domains
15. `000015_create_ssl_certificates.sql` — ssl_certificates table (cert_path, key_path, issued_at, expires_at, status, last_error, renewal_count, staging, created_at, updated_at)
16. `000017_ssl_enabled_default_true.sql` — SSL enabled by default (=1) on new domains
18. `000018_add_next_retry_at_to_ssl_certificates.sql` — next_retry_at timestamp + retry_count INT for exponential backoff retry scheduling
19. `000028_create_php_pools.sql`, `000029_create_php_pool_ini_overrides.sql`, `000030_add_php_pool_id_to_domains.sql` — M9 PHP/FPM pool manager tables + domain FK

### 4.10 PHP/FPM pools (M9)

**Shipped:** Per-user PHP-FPM pools via Sury multi-version install, pool CRUD (admin), per-domain PHP version binding (user), ini overrides (allowlisted directives), reconciler ensures default pool per user + applies pending changes, nginx vhost renders PHP block from `domain.php_pool_id`.

**Placement:** see ADR-0025 for per-user systemd slice design (in-flight via plans/per-user-systemd-slices.md).

**Files:**
- API: `panel-api/internal/api/php_pools.go`, `domain_php_pool.go`
- Models: `panel-api/internal/models/php_pool.go`, `php_pool_ini_override.go`
- Repository: `panel-api/internal/repository/php_pool_repository.go`, `php_pool_ini_override_repository.go`
- Agent: `panel-agent/internal/commands/php_pool_apply.go`, `php_pool_remove.go`, `php_version_list.go`
- Install: `install/php/jabali-php-pool.conf.tmpl`
- UI: `panel-ui/src/shells/admin/php-pools/PHPPoolsList.tsx`, `PHPPoolEdit.tsx`; `DomainSettingsButton.tsx` (PHP section)

**Migrations:** `000028_create_php_pools.sql`, `000029_create_php_pool_ini_overrides.sql`, `000030_add_php_pool_id_to_domains.sql`

**Agent commands:** `php.pool.apply`, `php.pool.remove`, `php.version.list`

**API endpoints:**
- `GET /api/v1/php-pools` (admin: all; user: self)
- `GET /api/v1/php-pools/:id`
- `POST /api/v1/php-pools` (admin: all; user: self)
- `PUT /api/v1/php-pools/:id` (admin: all; user: self)
- `DELETE /api/v1/php-pools/:id` (admin only)
- `GET /api/v1/php-pools/:id/ini-overrides`
- `POST /api/v1/php-pools/:id/ini-overrides`
- `PUT /api/v1/php-pools/:id/ini-overrides/:override_id`
- `DELETE /api/v1/php-pools/:id/ini-overrides/:override_id`
- `POST /api/v1/domains/:id/php-pool` (accepts `pool_id` or `php_version`)
- `DELETE /api/v1/domains/:id/php-pool`
- `GET /api/v1/php/versions`

**ADR:** ADR-0023 (13 decisions: per-user pools, reconciler default, admin-only deletion, domain binding, ini allowlist, version immutability, uid collision handling, etc.)

---

### 4.10.2 PHP extensions management (M9.6)

**Shipped:** Server-wide extension management per PHP version. Admin PHP page now has two tabs: the existing "PHP Versions" (install/reload/set-default) and a new "PHP Extensions" (pick a version → install/remove/enable/disable any of 63 allowlisted extensions). No per-user or per-pool scope — changes affect every `php<v>-fpm` master and every `jabali-fpm@<user>` pinned to `<v>`.

**State model:** live from dpkg + `/etc/php/<v>/fpm/conf.d/*.ini` on every list call. No migration, no DB persistence, zero drift risk.

**Allowlist:** 63 entries at `internal/phpext/phpext.go`, shared by panel-api + panel-agent. Built-ins (opcache, posix, pdo, mysqlnd, etc.) expose only enable/disable. Bundled packages (xml family → `php<v>-xml`) are collapsed by the resolver. The `mysql` entry is a meta-install for `php<v>-mysql` (mysqli + pdo_mysql); enable/disable rejected as ambiguous.

**Files:**
- Allowlist + resolver: `internal/phpext/phpext.go`
- Agent: `panel-agent/internal/commands/php_ext_list.go`, `php_ext_apply.go`, `php_ext_shell.go`
- API: `panel-api/internal/api/php_extensions.go`
- Contract lock: `panel-api/internal/agent/php_ext_contract_test.go` + `testdata/*.json` (6 golden fixtures, round-tripped)
- UI: `panel-ui/src/shells/admin/php/PHPVersionsPage.tsx` (Tabs container), `VersionsTab.tsx` (renamed from `PHPPoolsList.tsx`), `PHPExtensionsTab.tsx`

**Agent commands:** `php.ext.list {version} → {version, extensions: [{name, installed, enabled, built_in}]}`, `php.ext.apply {version, ext, action} → {version, ext, installed, enabled}` where action ∈ `{install, remove, enable, disable}`. Serialized by `aptMu sync.Mutex` around apt calls.

**API:**
- `GET  /api/v1/admin/php/versions/:version/extensions`
- `POST /api/v1/admin/php/versions/:version/extensions/:ext/apply` (body `{"action":"install|remove|enable|disable"}`)

**Wire addition:** `agentwire.CodeFailedPrecondition` + 409 Conflict mapping in `translateAgentError` — used for "version not installed", "ext not installed for enable", "shared-package remove conflict", "apt/phpenmod non-zero exit".

**ADR:** [ADR-0031](adr/0031-php-extensions-management.md)
**Runbook:** [docs/runbooks/php-extensions.md](runbooks/php-extensions.md)
**Plan:** [plans/php-extensions-tab.md](../plans/php-extensions-tab.md)

---

### 4.11 Per-user systemd slices (M9.5)

**Shipped:** Every hosting user now runs inside a nested systemd slice — PHP-FPM master, login shells, and systemd-user timers all land in the same cgroup. The distro's global `php<v>-fpm.service` is stopped, disabled, and masked after cutover.

**Hierarchy:**
```
jabali.slice
└─ jabali-user.slice              (MemoryHigh=80% of host RAM)
   └─ jabali-user-<user>.slice    (per-user container)
      ├─ jabali-fpm@<user>.service (PHP-FPM master as <user>)
      └─ user@<uid>.service       (login manager for shells + timers)
```

**Files:**
- Systemd units: `install/systemd/jabali.slice`, `jabali-user.slice`, `jabali-fpm@.service`
- Shims: `install/systemd/fpm-pre-start`, `fpm-exec` (run as root via `+` prefix for setup, FPM itself as `<user>`)
- Agent: `panel-agent/internal/commands/user_slice_ensure.go`, `user_slice_remove.go`, `user_slice_status.go`
- Reconciler: `panel-api/internal/reconciler/reconciler.go` (writeHealthcheckForDomain backfill, user-slice provisioning)
- Admin CLI: `panel-api/cmd/server/admin_slice_cutover.go` (`jabali-panel admin slice-cutover [--dry-run]`)
- UI: `panel-ui/src/shells/admin/users/UserSliceStatus.tsx`

**Agent commands:** `user.slice.ensure`, `user.slice.remove`, `user.slice.status`

**API endpoints:**
- `GET /api/v1/admin/users/:id/slice-status` (admin-only; returns memory/tasks/active state)

**Runtime paths:**
- Socket: `/run/php/jabali-<user>/fpm.sock` (owner=`<user>`, group=`www-data`, mode 0660)
- PID file: `/run/php/jabali-<user>/fpm.pid`
- Per-user FPM config: `/etc/jabali-panel/fpm/<user>.conf`
- Version pin: `/etc/jabali-panel/user-phpver/<user>`
- Healthcheck: `/home/<user>/<domain-docroot>/jabali-healthcheck.php` (probed by cutover)
- Runtime dir mode: `0750 <user>:www-data` (user writes, nginx traverses)

**Admin command:**
- `jabali-panel admin slice-cutover [--dry-run]` — preflight every user has an active FPM master, mask global `php<v>-fpm.service`, probe `jabali-healthcheck.php` through nginx, auto-rollback on probe failure.

**Linger:** `user.slice.ensure` calls `loginctl enable-linger <user>` so the user manager persists across logouts; without it, the `user@<uid>.service.d/jabali.conf` drop-in doesn't take effect until next login.

**ADR:** [ADR-0025](adr/0025-per-user-systemd-slices.md) — amends ADR-0023's placement decision.

**Runbook:** [docs/runbooks/per-user-slices.md](runbooks/per-user-slices.md) — what's captured, what's not (traditional crontabs), cron → systemd-user-timer migration recipe, troubleshooting.

**Plan:** [plans/per-user-systemd-slices.md](../plans/per-user-systemd-slices.md) (9 steps, all shipped).

---

## 5. What's out-of-scope

These live in separate git repositories. `install.sh` may install them as optional
addons, but this blueprint covers only panel-local code:

- **jabali-manager** — multi-node central control plane (separate repo)
- **jabali-security** — WAF, GeoIP, CrowdSec plugin (separate repo)
- **jabali-isolator** — nspawn container manager for SSH shell access (separate repo)
- **jabali-tunnel** — WSS sidecar for manager↔node encrypted tunnel (separate repo)
- **Bulwark webmail** — separate Next.js app (separate repo)

See `~/projects/jabali-shared/CONTEXT.md` for cross-repo coordination.

---

## 6. Milestone roadmap

Milestones describe locked-in delivery order. Status: Shipped, In-flight, or Planned.

### M1: Foundations (SHIPPED)

**Goal:** Bootstrap auth, users, and hosting packages so admins can log in and assign
  hosting users to resource tiers.

**Deliverables:**
- JWT access + refresh tokens
- Admin bootstrap seed (one admin user, one default package)
- User CRUD (create/read/update/delete/password-reset)
- Hosting package CRUD
- RBAC guards (RequireAdmin, RequireOwner)

**Status:** Shipped (commits `6b5dea4` and earlier)

**Depends on:** —

### M2: Domain lifecycle + Nginx rules + Redirects (SHIPPED)

**Goal:** Admins and users can create/manage domains with custom nginx rules,
  host-level + page-level redirects, and index file priority.

**Deliverables:**
- Domain CRUD (create/read/update/delete)
- Document root per domain
- Host-level redirects (HTTP 301/frame inject)
- Page-level redirects (regex pattern matching)
- Nginx custom rules: allow-deny, auth-basic, gzip, headers, rewrite, proxy-pass (6 types)
- Domain enable/disable toggle
- Index file priority
- Nginx vhost regeneration (agent command + reconciler)
- Domain deletion cascade (nginx vhost cleanup)

**Status:** Shipped (commits `fb3abea`, `0e92a04`, earlier commits)

**Depends on:** M1

### M3: Server Settings + Hostname + Public IPs (SHIPPED)

**Goal:** Admins can view and configure server hostname, nameserver IPs (for DNS SOA),
  and auto-detected public IPv4/IPv6.

**Deliverables:**
- Server settings table (hostname, ns1, ns2, public_ipv4, public_ipv6)
- Bootstrap seed hostname from install prompt
- Public IP auto-detect from external service
- NS1/NS2 auto-derive from hostname
- Admin page to read/update hostname (agent command to persist)
- Agent command: `system.set_hostname`, `system.info`

**Status:** Shipped (commits `6b5dea4`, `0e92a04`, `ab47b7d`, `bcc5f93`)

**Depends on:** M1

### M4: DNS zones + records + Secondary NS support (SHIPPED)

**Goal:** Admins can create authoritative DNS zones, manage records (type-aware forms),
  and configure secondary nameserver AXFR + NOTIFY.

**Deliverables:**
- DNS zone CRUD (create/read/update/delete/purge)
- DNS record CRUD (A, AAAA, CNAME, MX, TXT, NS, SOA, etc.)
- Type-aware form builder (per-record-type validation)
- Per-zone AXFR allow-list (IP addresses allowed to AXFR from this zone)
- Per-zone NOTIFY target (secondary NS IP to notify on zone changes)
- PowerDNS MySQL backend compiler (zone+record→pdns DB)
- Reconciler auto-bootstrap zones on domain create
- Agent command: `dns.zone.upsert`, `dns.zone.delete`
- UI: DNSRecordsPage (type-aware add/edit, safeguards for managed records)
- Managed record detection (SOA, NS auto-created, user-editable MX/A/AAAA)

**Status:** Shipped (commits `f7464a2`, `4b87395`, `0ae7213`, `3037db1`, `cb4ae39`)

**Depends on:** M2 (domains must exist before zones)

### M5: SSL / Let's Encrypt (SHIPPED)

**Goal:** Admins and users can issue and renew SSL certificates via Let's Encrypt
  on a per-domain basis, with resilient ACME issuance and automatic fallback.

**Phase 1 (M5a): Core SSL + UI (Shipped)**
- Per-domain opt-in SSL toggle (ssl_enabled boolean; default=true on new domains)
- SSL CRUD (read-only for users; admin can manually issue/revoke)
- Certbot webroot issuer (agent command places acme-challenge on filesystem)
- Agent commands: `ssl.issue`, `ssl.renew`, `ssl.revoke`
- API: `/api/v1/domains/{id}/ssl` (GET status, POST issue, DELETE revoke)
- UI: SSL Manager pages (admin + user shells) with per-domain toggle + status display

**Phase 2 (M5b): Resilient ACME + Fallback (Shipped)**
- Try-ACME-first strategy with 60s timeout
- Fallback to 365-day self-signed if ACME fails
- Exponential backoff retry scheduling: retry_count 1→5m, 2→15m, 3→1h, 4+→4h (capped)
- next_retry_at timestamp + retry_count tracking in ssl_certificates table
- Max 20 retries; mark cert as permanently failed if exceeded
- Manual retry endpoint: `/api/v1/domains/:id/ssl/retry` (admin-only)
- Agent command: `ssl.self_sign` (stopgap emergency fallback)
- Reconciler: syncs SSL state for all domains every 30s, respects retry backoff

**Status:** Shipped (commits `ba54273`, `66ae6d2`, `34379db`, `786079a`, `fc8ff7d`)

**Depends on:** M2 (domains must exist)

### M5c: Two-factor authentication (TOTP + backup codes) (SHIPPED)

**Goal:** Password + TOTP (RFC 6238) for every login, 10 single-use bcrypt-hashed backup codes per user, admin-only CLI disable-2FA escape hatch.

**Deliverables:**
- Migration `000041_add_2fa_totp`: `users.totp_{secret_encrypted,enabled,enabled_at}` + `totp_backup_codes` table
- `internal/twofa` service: enrolment via `github.com/pquerna/otp/totp`, 10×8-digit backup codes, bcrypt (cost 12), constant-time compare helper
- Secret at rest: AES-256-GCM via existing `internal/ssokey` (no new key material, same rotation surface)
- Login flow: when `totp_enabled=true`, `/auth/login` returns `{twofa_pending: true, twofa_pending_token}` with a 5-min JWT (`purpose="2fa_pending"`) instead of access+refresh; client exchanges it at `/auth/2fa/challenge` with a 6-digit TOTP or 8-digit backup code
- API: `POST /api/v1/auth/2fa/{enroll,verify,disable,regen-backup}` under RequireAuth; challenge rides the strict rate limiter
- UI: MyProfile 2FA card (enable/regen/disable modals, AntD `<QRCode>`, backup-codes-shown-once + save-confirmation gate); Login page two-stage state machine (password → challenge, swap to 8-digit backup-code input via "Use backup code" link)
- CLI escape hatch: `jabali admin disable-2fa --email <target>` — shell-access only, no API equivalent; wipes backup codes first, then `DisableTOTP` so a mid-failure still leaves the user unlocked
- Tests: 6 service unit tests, 4 handler tests, 4 login-flow integration tests (real `auth.Service` + real `JWTIssuer`, fakes only storage), 4 UI state tests + 3 Login stage-transition tests, 3 CLI unit tests

**Status:** Shipped (7 commits on `feat-2fa-totp`: `0b68048`, `afc3839`, `009ccf5`, `e2c1f62`, `65536ce`, `27059b1`, `7b1ac16`)

**Depends on:** M1 (auth)

**Out-of-scope (phase 2):** WebAuthn, SMS, push-based 2FA.

### M6: Email (Stalwart integration) (SHIPPED)

**Goal:** Admins and users can enable email per-domain, create mailboxes
  with per-mailbox quotas, and open Bulwark webmail already logged in
  with one click. Automatic DKIM key generation; MX/SPF/DMARC/DKIM/
  autoconfig DNS records injected on enable.

**Deliverables (v1 scope — ADR-0041 through ADR-0045):**

- Storage: Stalwart v0.16 with RocksDB mail storage (ADR-0041) + MariaDB
  SqlDirectory (ADR-0042) — panel owns `jabali_panel.mailboxes`,
  Stalwart re-reads on every auth (ADR-0045, no cache TTL).
- Migrations:
  - `000054_create_mailboxes` — `mailboxes` table + BEFORE INSERT/UPDATE
    trigger maintaining `email_cached = local_part || '@' || domain.name`.
  - `000055_dns_records_add_managed_by` — `dns_records.managed_by` column
    so `domain.email_disable` only tears down M6-owned rows, leaving
    M4 bootstrap + user-edited records alone.
  - `000056_mailbox_sso` — `mailboxes.password_enc` VARBINARY(512) +
    `mailbox_sso_tokens` table for one-shot webmail SSO.
- Per-domain lifecycle: `jabali domain email-enable <domain>` (Ed25519
  DKIM keypair under `/etc/jabali-panel/dkim/`, DKIM+autoconfig+
  autodiscover DNS rows, Stalwart reload) and `jabali domain email-disable`
  (reload Stalwart, delete M6 DNS rows, keep DKIM key per ADR-0043).
- Mailbox CRUD: `jabali mailbox {list,create,delete,set-quota,passwd}`
  — bcrypt hash for Stalwart auth + ssokey-sealed plaintext for webmail
  SSO, both written atomically.
- M6.5 mailbox extras (CLI mirrors of the HTTP handlers):
  `jabali mailbox autoresponder {set,clear,show} <email>` (subject + plain
  / HTML body + optional from/to RFC3339 dates),
  `jabali mailbox forwarder {add,list,remove}` (alias OR external).
- M6.5 domain extras: `jabali domain catchall {set,clear,show}` and
  `jabali domain disclaimer {set,clear,show}` (`--text` literal or
  `--file <path>` for HTML).
- Panel-API: `GET/POST/DELETE /domains/:id/email`, `GET/POST /domains/:id/mailboxes`,
  `PATCH/DELETE /mailboxes/:id`, `POST /mailboxes/:id/rotate-password`,
  `POST /mailboxes/:id/sso` (mint) + `GET /sso/webmail?token=…` (landing).
- Agent commands: `mailbox.{create,delete,set_quota,set_password,usage}`,
  `domain.email_{enable,disable}`, `webmail.vhost_{apply,remove}`.
- Webmail: Bulwark v1.4.14 (Next.js standalone, JMAP-native) behind
  per-domain `mail.<domain>` nginx vhost. Reconciler toggles the
  vhost based on `domains.email_enabled`. Browser-perspective JMAP
  travels `/jmap` same-origin; Bulwark proxies Stalwart admin via
  `STALWART_API_URL=http://127.0.0.1:8446` internally.
- Per-mailbox SSO: "Webmail" action on every mailbox row mints a
  single-use SHA-256-hashed token (5-minute TTL), landing endpoint
  on `mail.<domain>/sso/webmail` decrypts `password_enc` via
  `sso.key`, POSTs Bulwark `/api/auth/session`, forwards the session
  cookie onto its own 303 to `/`.
- UI: Email tab on DomainEdit with live DNS-record status tags (ok /
  missing / conflict), Mailboxes tab under DomainEdit (admin) +
  dedicated `/jabali-panel/mailboxes` page (user shell).

**Out of scope (deferred):**
- Per-user Sieve rules UI (M6.1)
- CalDAV/CardDAV (separate milestone)
- Catch-all + forwarder management (no `mail_forwarders` table yet)
- Cluster mode / secondary MX failover
- Automatic DKIM rotation (M6.1)
- ACME `mail.<domain>` SAN expansion (M6.1; v1 reuses main domain cert)
- IMAP migration importer (deferred to M15 per ADR-0044)

**Status:** Shipped — ADRs 0041-0045 on `m6/email-stalwart` branch.

**Depends on:** M4 (DNS zones for autoconfig), M7 (shadow-password
pattern + `sso.key` reused for mailbox SSO), M20 (Kratos for panel
auth flow).

### M6.3: pdns-recursor local self-resolution (SHIPPED — branch)

**Status:** All 7 steps committed on branch `m6.3/pdns-recursor-integration`.
Awaits VM smoke + merge to `main`. Plan at `plans/m6.3-pdns-recursor.md`
(7-step blueprint, architect-reviewed — CRITICAL+HIGH+MEDIUM folded).
ADR-0047 accepted. Runbook at `plans/m6.3-pdns-recursor-runbook.md`.

**Goal:** The panel host resolves its own authoritative zones
(`<domain>`, `mail.<domain>`, `autoconfig.<domain>`) via a local
`pdns-recursor`, eliminating `/etc/hosts` workarounds and unblocking
M6 SSO ergonomics (Bulwark → Stalwart `/.well-known/jmap`) and ACME
HTTP-01 pre-flight on every install.

**Driving evidence:** Fresh M6-enabled installs needed
`NODE_TLS_REJECT_UNAUTHORIZED=0` (M6.2, commit `3219428`) to survive
Bulwark verifying the JMAP endpoint. That fixed the cert-trust leg
but left the DNS leg: `mail.<domain>` resolves via upstream public
recursion, which has no data about a freshly-created panel zone
until NS delegation propagates. `/etc/hosts` has been the stopgap.

**Strategy (ADR-0047):**
- `pdns-server` keeps `${JABALI_SRV_IPV4}:53` for external authoritative.
  Loopback moves to `:5300`.
- `pdns-recursor` owns loopback `:53` (both v4+v6), `allow-from=127.0.0.0/8,
  ::1/128` (narrower than Debian default — LXC bridges live in
  RFC1918 and the default would expose an open resolver to
  co-tenants).
- `/etc/powerdns/recursor.forwards` is reconciler-owned; agent commands
  (`pdns.recursor_add_zone` / `remove_zone` / `list`) wrap atomic write
  + strict validator + `rec_control reload-zones` + NS post-probe +
  rollback.
- systemd-resolved points at `127.0.0.1` via alphabetical-last drop-in
  `zz-jabali-recursor.conf` with explicit `DNS=` reset. Install gates
  on `resolvectl status` showing loopback-only.
- Backfill CLI (`jabali pdns backfill [--dry-run|--yes]`) converges
  existing hosts after upgrade.

**Depends on:** M6 (the problem M6.3 unblocks).
**Related:** ADR-0047 (pdns-recursor design + alternatives + failure modes),
`plans/m6.3-pdns-recursor.md` (7-step blueprint),
`plans/m6.3-pdns-recursor-runbook.md` (operator runbook).

### M6.4: Panel hostname as primary mail domain (IN PROGRESS — branch)

**Status:** Blueprint drafted at `plans/m6.4-panel-hostname-mail-domain.md`
(6-step plan, architect-reviewed — 4 CRITICAL + 3 HIGH + 4 MEDIUM + 3 LOW
folded). Branch `m6.4/panel-hostname-mail-domain`. Step 1 dispatchable.
ADR-0048 target.

**Goal:** Make `<panel-hostname>` itself a first-class email domain so
`admin@<panel-hostname>` works on every fresh install, the panel's
self-signed cert covers `mail.<panel-hostname>`, and
`{panel-hostname}/webmail` redirects to the Bulwark webmail at
`https://mail.<panel-hostname>/`.

**Driving evidence:** On 192.168.100.150 fresh reinstall, browsing to
`mail.jabali-panel.local` returned `SEC_ERROR_INADEQUATE_CERT_TYPE` —
the panel's self-signed cert covers only the apex hostname, no `mail.`
SAN. Separately, there's no nginx vhost for the panel hostname's
`mail.` subdomain because the panel hostname has never been a
configured email-enabled domain: no row in `domains` with
`email_enabled=1`, no DKIM keypair, no MX/SPF/DMARC records in the
M6.3 self-zone. The operator has no ergonomic path to their own
mailbox from a fresh install.

**Strategy (ADR-0048):**
- New column `domains.is_panel_primary` (TINYINT, at-most-one enforced
  in the Go repo layer) marks the single auto-registered hostname
  domain. Marker is distinct from `name` equality so hostname changes
  are handled in-place.
- `install.sh` adds `install_panel_primary_domain()` that INSERTs the
  domain row with `email_enabled=1, is_panel_primary=1` after
  `bootstrap_admin_user` + `bootstrap_pdns_self_zone`. Reconciler then
  takes over via the existing M6 email-enable path (DKIM keypair,
  Stalwart domain, nginx mail vhost, MX/SPF/DMARC TXT in self-zone).
- `provision_tls_cert` gains hostname-drift detection (CN vs current
  `$JABALI_SRV_HOSTNAME`) + `mail.<hostname>` SAN. Regen triggers full
  panel-api restart (Go HTTP server caches cert in memory; SIGHUP is
  a no-op).
- nginx default vhost gains `location = /webmail { return 301
  https://mail.<hostname>/; }` — heredoc-interpolated at install time,
  re-rendered on every install.sh run, so hostname drift propagates.
- Panel-primary row is delete-protected at both repo + API layer
  (403 with `panel_primary_protected` code). Panel UI Settings → Email
  card shows read-only state; Domains list hides Delete button with a
  "System" tag.
- `/webmail` fallback: no graceful-503; reconciler converges in ~30s
  and the TLS-handshake-fail window is deemed acceptable. M6.4.4
  follow-up if operators complain.

**Depends on:** M6 (email plumbing), M6.1 (cert SAN helper — reused),
M6.2 (webmail SSO — unchanged), M6.3 (self-zone local-resolvable).
**Related:** ADR-0048 (panel-primary domain marker + hostname-change +
/webmail fallback), `plans/m6.4-panel-hostname-mail-domain.md`.


### M6.5: Email features expansion (SHIPPED — branch)

**Goal:** Add six native-Stalwart-backed features to the user Mail page
— Forwarders, Autoresponders, Catch-All, Disclaimer, Shared Folders,
Logs — as tabs alongside the existing Mailboxes tab.

**Deliverables:**
- Migrations: `000060_create_email_forwarders.sql`,
  `000061_create_email_autoresponders.sql`,
  `000062_domains_add_catchall_and_disclaimer.sql`,
  `000063_create_mailbox_shares.sql`.
- Reconciler phase registry at `panel-api/internal/reconciler/phases/`
  (one file per feature; `init()` registers).
- Route registry `panel-api/internal/api/routes_m65.go` with per-feature
  `register*Routes` stubs — parallel steps edit only their stub body.
- panel-agent commands: `domain.catchall_set/clear`, `autoresponder.set`,
  `mailbox.share_set`, `forwarder.apply`, `domain.disclaimer_apply`,
  `mail.logs_query`.
- panel-api handlers: `/domains/:id/catchall`, `/domains/:id/disclaimer`,
  `/mailboxes/:mbid/autoresponder`, `/mailboxes/:mbid/shares` +
  `/mail/shares`, `/mailboxes/:mbid/forwarders` + `/mail/forwarders`,
  `/mail/logs`.
- panel-ui tabs under `/jabali-panel/mail/`: MailboxesTab (extracted
  from existing UserMailboxesPage), CatchAllTab, AutorespondersTab,
  SharedFoldersTab, ForwardersTab, DisclaimerTab, LogsTab. Legacy URL
  `/jabali-panel/mailboxes` redirects to `/jabali-panel/mail/mailboxes`.

**Architecture:**
- DB-as-truth per ADR-0002 + ADR-0051; reconciler converges jabali
  state to Stalwart every tick. Operator edits via Stalwart admin
  console are drift — overwritten next tick.
- Stalwart surfaces per feature: `x:Domain.catchAllAddress`,
  `VacationResponse` (RFC 8621 §8), `Mailbox.shareWith`,
  `x:UserAccount.aliases` + `x:SieveUserScript`, `x:SieveSystemScript`,
  `x:Trace/query` + `x:Trace/get`.
- Cross-domain tables: every tab lists across all email-enabled domains.

**Known limitations:**
- Disclaimer covers `text/plain` body parts only in first ship. HTML
  body coverage deferred pending live Spike A (sieve on HTML) and
  Spike B (MtaHook unix:// support) on 192.168.100.150 — see ADR-0052.
- Logs tab does not yet support trace event drilldown. Deferred to M6.6.

**Depends on:** M6, M6.1, M6.2, M6.3, M6.4, M25.
**Related:** ADR-0051 (DB-as-truth for M6.5, M6 pattern continued),
ADR-0052 (disclaimer sieve vs MtaHook decision matrix),
`plans/m6.5-email-features.md`, `plans/m6.5-email-features-runbook.md`,
`plans/m6.5-email-features-research.md`.

### M7: Databases (MariaDB) (SHIPPED)

**Goal:** Users can create and manage databases + database users with grant tables.

**Deliverables:**
- Migrations: `000020_create_databases.sql`, `000021_create_database_users.sql`, `000022_create_database_user_grants.sql`, `000024_add_privileges_to_grants.sql`, `000025_widen_grant_level_enum.sql`, `000027_create_phpmyadmin_sso_tokens.sql`
- Database CRUD (name, engine: mariadb; Postgres deferred per ADR-0018)
- Database user CRUD (username, bcrypt + reveal-once password per ADR-0021)
- Grant management: per-database rw/ro levels (ADR-0019); custom privileges enum via migration 000024/000025
- phpMyAdmin SSO: signon proxy + per-user shadow account over UDS (ADR-0020, ADR-0022)
- Agent commands: `db.create`, `db.drop`, `db_user.create`, `db_user.drop`, `db_user.grant`, `db_user.revoke`, `db_user.rotate_password`, `db.backup`, `db.restore`, `db.size`
- API: `/api/v1/databases`, `/api/v1/database-users`, `/api/v1/sso/phpmyadmin`
- UI: user database list/create, user list/create, grant table UI; admin view with prefix-filtered non-admin list
- Cascade delete: `DELETE /databases/:id` cascades grants (revoke on agent, idempotent) and blocks on active `wordpress_installs` with 409

**Status:** Shipped (ADRs 0018-0022; recent fixes in `b723fe1`, `9ebaad8`, `762c7fe`)

**Depends on:** M1 (users exist)

### M8: Cron (SHIPPED)

**Goal:** Users can create allow-listed cron jobs without security risk, executed via per-user systemd-user timers.

**Deliverables:**
- Per-user systemd-user timers (ADR-0029): service + timer unit files under `/etc/jabali-panel/cron-units/<user>/`
- Cron CRUD (per-user allow-list of safe commands): migration `000026_create_cron_jobs.sql` (name, command, schedule, enabled, last_run_at, last_exit_code)
- Shared cron validator: `/internal/cronvalidate/` (allowlist: `wp`, `wp-cron.php` + PHP scripts in owned docroots; reject shell metacharacters, path traversal, disallowed binaries)
- Agent commands: `cron.apply` (renders unit files), `cron.remove`, `cron.run_now` (immediate execution), `cron.tail_log` (journalctl tail), `cron.list`
- API: `/api/v1/cron[/:id]`, `/api/v1/cron/:id/run-now`, `/api/v1/cron/:id/log`
- Reconciler: `ReconcileCronJobs` (30-second cadence polling systemctl for last_run_at, last_exit_code via ExecMainStatus)
- UI: AntD user cron list page (create, edit, run-now, view log, delete; form validation + server-side rejection)

**Status:** Shipped (ADR-0029; see plans/m8-cron-runbook.md for ops; key commits: `26649a9` migration+model+repo, `79164ea` validator, `1f3df4f` agent cmds, `c40ea01` reconciler+API, `6eefd5a` UI)

**Depends on:** M1 (users exist), M2 (docroots exist)

### M9: PHP/FPM pool manager (SHIPPED)

**Goal:** Admins can create per-user PHP-FPM pools with configurable runtime settings. Users bind domains to pools. Reconciler ensures one default pool per user.

**Deliverables:**
- Migrations: `000028_create_php_pools.sql`, `000029_create_php_pool_ini_overrides.sql`, `000030_add_php_pool_id_to_domains.sql`
- PHP pool CRUD (per user; admin-only deletion per ADR-0023)
- Domain ↔ pool binding (admin explicit pool selection; user version-based lookup)
- php.ini overrides (memory_limit, max_execution_time, upload_max_filesize, etc.; allowlisted directives)
- Agent commands: `php.pool.apply`, `php.pool.remove`, `php.version.list`, `php.version.status`, `php.version.install`, `php.version.reload`
- API: `/api/v1/php-pools[/:id]`, `/api/v1/php-pools/:id/ini-overrides*`, `/api/v1/domains/:id/php-pool`, `/api/v1/php/versions`, `/api/v1/admin/php/versions/{status,/:version/install,/:version/reload}` (admin-only)
- UI: admin PHP versions management page (install/reload per version); admin PHP pool list/edit; domain binding via PHP section in settings
- Reconciler: ensures every user has ≥1 pool, applies pending changes, nginx renders pool's PHP block

**Status:** Shipped (commits `1aaa507` through final commit; admin versions UI shipped separately)

**Depends on:** M1 (users exist), Sury PHP multi-version install (install.sh)

### M10: WordPress (SHIPPED — generalised by M19 Applications Framework)

**Goal:** Admins and users can install, delete, and clone WordPress instances.

**Note:** Superseded as a single-app surface by **M19** (2026-04-19). The WordPress install flow now lives behind the generic `/applications` API and the per-app registry under `panel-api/internal/apps`. The M10 routes (`/wordpress-installs`) and agent commands (`wordpress.install`/`wordpress.delete`/`wordpress.clone`) remain registered through the M19 release window for one-release backwards compatibility; M19.1 deletes them. See ADR-0033.

**Deliverables:**
- New migration: `000033_create_wordpress_installs.sql` (domain_id, db_id, status, version, admin_username, admin_email, locale, last_error)
- WordPress CRUD (install, delete, clone from existing)
- Automated install script (WordPress core + wp-cli, database setup, admin user)
- Clone operation (copy docroot via rsync, backup/restore database, new domain binding, preserve admin credentials)
- Health check endpoint (`POST /wordpress/:id/health`) for status monitoring
- Agent commands: `wordpress.install`, `wordpress.delete`, `wordpress.clone` (with credential-handling invariant and placeholder rewrite for DB password)
- API: `/api/v1/wordpress` (list, create), `/api/v1/wordpress/:id` (get, delete), `/api/v1/wordpress/:id/clone` (POST)
- UI: user WordPress install/delete/clone buttons with live status polling; admin WordPress cross-user list; clone modal with destination domain selection
- Reconciler: WordPress install sweeper (drift detection for rows stuck in `installing`/`cloning`/`deleting` beyond 10min threshold)
- Playwright E2E test (install → clone → verify independent DB → login test → delete)
- Runbook for troubleshooting (stuck installs, failed clones, orphaned DBs, drift recovery)

**Status:** Shipped (commits `85ed8b4` through `main HEAD`)

**Depends on:** M2 (domains), M7 (databases), M9 (PHP-FPM pools for clone version compatibility)

### M11: File Manager (SHIPPED — superseded-and-rebuilt 2026-04-19)

**Goal:** Users can browse and manage files via a web UI scoped to their homedir.

**History:** First attempt integrated the third-party `filebrowser` project behind an SSO proxy. It burned ~a week on architectural mismatches (stateless proxy-auth, in-process user cache, CLI↔DB drift, POSIX ACL choreography). Decommissioned in Wave E. See ADR-0027 for the failed attempt and ADR-0030 for the replacement decision.

**Deliverables (current, AntD-native):**
- Shared `internal/filesafe/` path-safety validator consumed by both panel-api and panel-agent (same cross-boundary pattern as M8 cron's `/internal/cronvalidate/`)
- 7 agent commands: `files.list`, `files.read`, `files.write`, `files.delete`, `files.mkdir`, `files.rename`, `files.stat` — all scoped via filesafe
- 8 REST endpoints under `/api/v1/files`: `GET /`, `GET /tree`, `GET /home`, `GET /download`, `GET /preview`, `POST /upload`, `POST /mkdir`, `POST /rename`, `DELETE /`
- Wire-shape drift guard: `panel-api/internal/api/files_wire_test.go` asserts JSON tags match the agent
- UI: `FileManagerPage.tsx` at `/jabali-panel/files` — Breadcrumb + toolbar top, Tree left, Table right; Upload/NewFolder/Rename/Delete/Preview/Download
- Ownership model: operations run as root in the agent, results `chown`ed to `<user>:www-data` with mode 0640 (files) / 0750 (dirs) — matches deployed per-user FPM model verified on 192.168.100.150

**Phase-2 backlog (out of scope for v1):** drag-and-drop upload, multi-select, image preview, chmod UI, editor (Monaco), zip/unzip, binary-safe read/write (current content is UTF-8 string over JSON), domain-docroot scope (`/var/www/*` alongside `$HOME`), chunked upload above 100 MB.

**Status:** Shipped — Wave A–E committed between 2026-04-18 and 2026-04-19.

**Depends on:** M1 (users), M9.5 (per-user slices for FPM ownership model).

### M12: SFTP access via openssh (SHIPPED)

**Goal:** Users upload SSH public keys in the panel and SFTP into their homedir. Key-only auth, no shell, no chroot.

**Deliverables:**
- SSH key management UI: "SSH Keys" page in user shell (add/delete keys with fingerprint display)
- `ssh_keys` table: id, user_id, name, public_key, fingerprint, created_at
- API CRUD handlers: `POST /api/v1/ssh-keys`, `GET /api/v1/ssh-keys`, `DELETE /api/v1/ssh-keys/{id}`
- Agent commands: `ssh.authorized_keys.write`, `ssh.authorized_keys.delete`, `ssh.user.join_sftp_group`
- Reconciler: ensures all jabali users in `jabali-sftp` group; syncs authorized_keys from DB
- Systemd group: `jabali-sftp` (Match Group in sshd_config)
- Sshd drop-in: `/etc/ssh/sshd_config.d/jabali-sftp.conf` (ForceCommand internal-sftp, no TCP forwarding, no passwords)
- Playright E2E test: `tests/e2e/sftp.spec.ts` (add key → list with fingerprint → duplicate error → invalid error → delete)
- Runbook: `plans/m12-sftp-runbook.md` (manual SFTP verification, troubleshooting auth, group membership, key sync)

**Status:** Shipped (commits aaa0c82…0256773)

**Depends on:** M1 (users), M9 (per-user slices)

### M13: SSH shell sandbox (SHIPPED 2026-04-26)

**Goal:** Every hosting user gets a restricted login shell (`jabali-ssh-shell`) that runs their
  commands inside a sandbox — either bubblewrap (lightweight, default) or systemd-nspawn
  (ephemeral container off a versioned image).  Fail-closed to `/usr/sbin/nologin`.

**Deliverables:**
- `jabali-ssh-shell` binary (Go): reads `ssh_sandbox_mode` + `default_nspawn_image_version` from DB
- Bubblewrap mode: `bwrap` with stripped capabilities, `--unshare-all`, read-only system bind-mounts
- nspawn mode: `systemd-nspawn` off `/var/lib/jabali/nspawn-images/<version>`, ephemeral overlay
- Agent endpoint `GET /admin/system/nspawn-images` — lists available versioned images
- Admin Server Settings → General → Shell Sandbox section: mode Select + conditional image Select
- `ssh_sandbox_mode` + `default_nspawn_image_version` columns on `server_settings` (migration)
- ADR-0067 (ACCEPTED): sandbox design decisions
- E2E spec: `panel-ui/tests/e2e/ssh-sandbox.spec.ts` (4 mock-based tests)

**Status:** Shipped (commits on main 2026-04-26; ADR-0067 ACCEPTED)

**Depends on:** M1 (users), M12 (SFTP/SSH key management)

### M14: Notifications (SHIPPED 2026-04-24)

**Goal:** Admins can configure and receive alerts via multiple out-of-band channels AND see in-app notifications via a bell dropdown that also delivers Web Push when the panel tab is closed.

**Channels:**
- Email (via Stalwart — shipped in M6)
- Slack (incoming webhook)
- Discord (incoming webhook)
- ntfy.sh (self-hostable or public; POST to topic URL, optional bearer token + priority + tags)
- Generic webhook (arbitrary URL, JSON body, HMAC signature)
- Web Push (W3C Push API + VAPID, service-worker-backed, fires even when panel tab is closed)

**In-app surface:**
- Notification bell dropdown in the admin topbar (AntD `Dropdown` + `Badge` for unread count)
- Lists the last N notifications with: timestamp, severity, title, link to the related resource (domain, cert, service)
- Mark-as-read + Mark-all-as-read; persisted in `notification_history`
- Same dropdown registers + manages Web Push subscription for the signed-in admin (enable/disable toggle + permission prompt). VAPID keys auto-generated at first install and stored in `server_settings`.

**Deliverables:**
- New migrations: `create_notification_channels.sql`, `create_webhook_endpoints.sql`, `create_notification_history.sql`, `create_webpush_subscriptions.sql`
- Notification channel CRUD (email, Slack, Discord, ntfy, generic webhook, web push)
- Admin broadcast notification (system events: domain expiry, certificate renewal, disk full, service down, backup fail, CrowdSec ban-rate spike)
- Per-channel template customization + test-send button
- Notification history log (audit trail of every delivery: channel, outcome, retry count)
- Agent command: `notifications.send` (for system-level alerts originating from the agent root scope)
- Service worker `panel-ui/public/sw-push.js` handling `push` + `notificationclick` events
- API: `/api/v1/notification-channels`, `/api/v1/notifications/broadcast`, `/api/v1/notifications/inbox`, `/api/v1/notifications/webpush/subscribe`, `/api/v1/notifications/webpush/vapid-public-key`
- UI: admin Notification Channels config page + topbar bell dropdown + Web Push enablement flow

**Status:** Shipped — 9 steps across `feat/m14-*` branches merged to `main` 2026-04-24. ADRs 0056–0059. Runbook: `plans/m14-notifications-runbook.md`.

**Architecture delivered:**
- Producers (admin API, agent `notifications.send`, in-process event sources) → Redis Stream `jabali:notifications:queue` → Dispatcher goroutine → bounded-parallel fanout (cap 4) → ChannelSender (slack | discord | ntfy | webhook | webpush | email)
- DLQ `jabali:notifications:dlq` for parse errors + retries-exceeded
- Circuit breaker: 3 consecutive failures auto-disable the channel and fire a critical alarm row
- In-process event sources: cert_renew (7d/1d expiry + renewal fail), disk_full (85/95% on /, /var/www, /var/lib/mysql), service_down (jabali-* systemctl watchdog), crowdsec_spike (cscli decisions threshold)
- Rate limits: IP-tier strict + per-admin 5/min token bucket on broadcast/test

**Depends on:** M6 (email channel requires Stalwart)

### M15: Migration importers (PLANNED)

**Goal:** Admins can import domains, users, and DNS from cPanel, DirectAdmin, HestiaCP, WHM.

**Deliverables:**
- New migrations: `create_migration_jobs.sql` (source, status, progress, error_log)
- Per-source backup parser (cPanel tar.gz, DirectAdmin backup.json, HestiaCP, WHM SQL dump)
- Analyze stage (extract users, domains, databases, DNS zones)
- Fix-permissions stage (regenerate nginx vhosts, reset database privileges)
- Validate stage (syntax check DNS, test HTTP on domains)
- Restore stage (insert into Jabali schema, call agent commands to create vhosts)
- Rollback on error (cascade-delete imported data, preserve rollback export)
- Agent command: `migration.import`, `migration.rollback`
- API: `/api/v1/migrations/import`, `/api/v1/migrations/{id}/status`, `/api/v1/migrations/{id}/rollback`
- UI: admin migration import wizard (upload backup, preview, confirm, monitor progress)

**Status:** Planned

**Depends on:** M2 (domains), M7 (databases), M10 (WordPress)

### M16: Identity Federation + Automation API (ROLLED BACK 2026-04-21)

**Status:** Rolled back due to PKCE incompatibility with the auto-installed WordPress OpenID Connect Generic plugin (v3.11.3), which lacks PKCE support despite the OAuth 2 RFC 6749 requirement. Instead of relaxing Hydra's security posture or forking the plugin, the panel now uses **M22 magic-link** for WordPress SSO — a simpler token-exchange pattern that doesn't require standards federation. Automation API (Wave F) is deferred to a fresh decision when the need is clear. Full rationale and rollback steps: `docs/adr/0038-m16-rollback.md`.


### M17: Diagnostic reports (PLANNED)

**Goal:** Admins can export server state as an RSA-encrypted tarball for support.

**Deliverables:**
- New migration: `create_diagnostic_reports.sql` (created_at, expires_at, file_hash)
- Report generator (collect: database schema, nginx vhosts, DNS zones, service logs, agent version, Go version)
- RSA encryption (public key in panel, support team decrypts with private key)
- Time-limited downloads (report auto-expires after 7 days)
- Agent command: `diagnostics.collect` (logs, config, status)
- API: `/api/v1/diagnostics/generate`, `/api/v1/diagnostics/{id}/download`
- UI: admin diagnostics page with one-click export button

**Status:** Planned

**Depends on:** M1 (admin access)

### M18: Per-user resource limits (SHIPPED 2026-04-21)

**Goal:** Hosting packages become *enforceable* bundles. Admins set disk, CPU, memory, I/O, task, and per-domain request/connection limits on a package; the reconciler converges the system so every hosting user is capped by kernel + filesystem + nginx. Per-user override rows allow one-off tuning without forking packages.

**Deliverables (all landed):**
- Migrations 000042-000044: new columns on `hosting_packages` (cpu/memory/io/tasks), new columns on `domains` (rate_limit_rps, connection_limit), new `user_limit_overrides` table
- POSIX user quota enforcement (ext4/xfs; btrfs/zfs fail loud in install.sh)
- cgroups v2 drop-ins at `/etc/systemd/system/jabali-user-<u>.slice.d/limits.conf` (CPUQuota, MemoryMax, MemoryHigh=90%, TasksMax, IOReadBandwidthMax, IOWriteBandwidthMax)
- nginx `00-jabali-ratelimits.conf` fragment generator + per-vhost `limit_req` / `limit_conn` directives
- Agent commands: `user.limits.apply|report|clear` + `nginx.ratelimits.apply`
- CLI: `jabali limits check|apply|status|package apply [--dry-run]`
- Admin UI: package editor extended with 5 new fields under "Resource limits" divider
- User shell: MyProfileUsageCard with 10s polling, disk/memory Progress bars, effective limits Descriptions
- install.sh: `usrquota` configuration + tmpfs `/tmp` + cgroups v2 probe (idempotent, fail-loud on unsupported FS)
- ADR-0032 + runbook at `plans/m18-resource-limits-runbook.md`

**Status:** Code complete; 7 waves shipped (A-G) 2026-04-19. **Host validation** — the OS-level smoke tests (cgroups drop-in matches kernel state, OOM-kill, CPU throttling, nginx 503 on burst) executed against the test VM 2026-04-21 and all pass. Disk-quota enforcement is disabled on /home-on-root hosts (install.sh deliberately skips usrquota there — quota-on-root is unsafe). Four bugs surfaced during validation and were fixed in the same pass: (1) `jabali limits check` nginx-module probe false-negated on stock Debian (false "not compiled in" report; default-on modules aren't listed by `nginx -V`); (2) `serve.go` + `jabali limits {status,apply}` CLI passed `QuotaMount=/` on /home-on-root hosts, aborting the whole apply pipeline before cgroups drop-ins got written; (3) runbook §3 used `su - testuser` for memory/CPU tests — wrong, `su` spawns children in the root session scope not the user slice, so the enforcement never applies. Replaced with `systemd-run --slice=...`. (4) reconciler wrote vhosts referencing `limit_req zone=rl_<id>` before the `00-jabali-ratelimits.conf` fragment declared the zone → `nginx -t` failed with "unknown zone" and domain provisioning aborted for any domain with `rate_limit_rps > 0`. Fixed in `45a0ea6`: `ReconcileNginxRateLimits` now runs BEFORE the domain loop in every entry point (with the existing post-loop call preserved for the reverse N→0 transition); regression test asserts ordering.

**Depends on:** M9.5 (per-user slices from ADR-0025)

### M19: Applications Framework (SHIPPED — partial)

**Goal:** Generalise the M10 WordPress-specific surface into a Softaculous-style applications framework so DokuWiki, MediaWiki, Joomla, Drupal, phpBB, PrestaShop, Moodle, Nextcloud, Matomo (etc.) can be installed alongside WordPress on a domain or subdirectory using the same plumbing — one model, one repository, one reconciler, one set of API + agent commands keyed by an `app_type` discriminator.

**Deliverables (steps 1–5 + 8 shipped on the `m19/*` branch stack 2026-04-19):**
- Migration `000046_rename_wordpress_installs_to_application_installs.up.sql` — RENAME TABLE + add `app_type VARCHAR(32) NOT NULL DEFAULT 'wordpress'` + flip composite UNIQUE from `(domain_id, subdirectory)` to `(domain_id, subdirectory, app_type)`. Down migration uses `SIGNAL SQLSTATE '45000'` to refuse rollback once any non-WordPress row exists.
- `panel-api/internal/apps/` package: `App` descriptor + `Registry` + `ParamSpec` (typed `string`/`email`/`password`/`enum`/`bool`); WordPress descriptor opting into the catalog; registry validated at registration time + race-clean.
- `panel-api/internal/api/applications.go` — generic `POST/GET /applications`, `GET/DELETE /applications/:id`, `POST /applications/:id/clone`, plus `GET /applications/registry` for the install picker. Validates per-app `params` against the descriptor's `InstallParamSchema`. `RequiresDB=false` short-circuit skips the entire panel-row + agent `db.create / db_user.create / db_user.grant` chain. The legacy `/wordpress-installs` routes stay mounted in parallel (one-release back-compat).
- `panel-agent/internal/commands/app_dispatch.go` — `app.install` / `app.delete` / `app.clone` dispatcher reads `app_type` off the body and forwards to the per-app handler registered via `RegisterAppInstaller(name, handler)`. Each app's handler file owns the registration in its `init()`. Legacy `wordpress.*` agent commands stay registered alongside through M19.1.
- Six cross-boundary contract fixtures + round-trip tests under `panel-api/internal/agent/testdata/app_*.json` per the `feedback_cross_boundary_contracts` lesson.
- UI rename: `panel-ui/src/shells/{user,admin}/wordpress/` → `applications/`, Refine resource `wordpress-installs` → `applications`, sidebar label "WordPress" → "Applications" (`AppstoreAddOutlined`), routes `/jabali-panel/wordpress` → `/jabali-panel/applications` (and same for `/jabali-admin`). Install modal pulls `GET /applications/registry` and renders an "App" Select defaulted to WordPress; lists add an "App" column at column 1.
- ADR-0033 + runbook at `docs/runbooks/applications.md` documenting the registry pattern, common failure modes, and "how to add a new app" walkthrough.

**Step 5a (UI dynamic field renderer, shipped 2026-04-19):** The install modal now switches its per-app field block based on the picked descriptor's `InstallParamSchema` — `string`/`email`/`password`/`enum`/`bool` types render as the matching AntD control, with `locale`/`language` getting the curated dropdown. Adding an app type with new params (e.g. DokuWiki's `license` enum) requires zero UI code.

**Step 6 (DokuWiki, shipped 2026-04-19):** Second app in the catalog — flat-file PHP wiki, no database. Validates the framework's `RequiresDB=false` short-circuit end-to-end + the `enum` `ParamSpec` (license dropdown). Descriptor at `panel-api/internal/apps/dokuwiki.go`; agent installer + deleter at `panel-agent/internal/commands/dokuwiki_{install,delete}.go`. The installer downloads the upstream stable tarball, verifies SHA-256 against `dokuwikiTarballSHA256 = 1d10e8dc…d090f3` (verified against upstream 2026-04-21), extracts under `systemd-run --uid=<user>`, writes `conf/local.php` + `conf/users.auth.php` (bcrypt admin via `php password_hash`) + `conf/acl.auth.php`, drops `install.lock`, and normalises perms. The `m19/06-app-dokuwiki` branch shipped the panel + agent code.

**Step 7 (MediaWiki, shipped 2026-04-19):** Third app in the catalog — the wiki engine behind Wikipedia. Validates the per-app CLI installer pattern via MediaWiki's `php maintenance/install.php` so the user never sees the web installer wizard. Descriptor at `panel-api/internal/apps/mediawiki.go`; agent installer + deleter at `panel-agent/internal/commands/mediawiki_{install,delete}.go`. The installer pins `MEDIAWIKI_VERSION=1.41.2` (LTS series), downloads from `releases.wikimedia.org/mediawiki/1.41/mediawiki-1.41.2.tar.gz`, verifies SHA-256 against `mediawikiTarballSHA256 = 52bb42c3…b1ce5` (verified against upstream 2026-04-21), extracts under `systemd-run --uid=<user>`, then drives the CLI installer with `--dbtype=mysql --server=<origin> --scriptpath=<subdir> --lang=<lang> --pass=<wikipw>`. The CLI generates `LocalSettings.php` containing the DB credentials in the install root. Bump `mediawikiTarballSHA256` whenever the `MEDIAWIKI_VERSION` constant moves.

**Status:** API, agent, panel, UI, and three apps (WordPress + DokuWiki + MediaWiki) shipped end-to-end on the `m19/*` branch stack. The framework is now exercised by `RequiresDB=true` (WordPress, MediaWiki) AND `RequiresDB=false` (DokuWiki) AND a per-app CLI installer (MediaWiki) AND a config-file generator (DokuWiki) — each axis the plan called out is covered. The catalog grows from here per ad-hoc operator demand without further framework work.

**Depends on:** M10 (WordPress install plumbing — generalised here), M9 (PHP-FPM pools — required so non-WordPress PHP apps land on the right pool), M7 (databases — required for `RequiresDB=true` apps).

### M20: Kratos identity migration (SHIPPED — legacy fully removed)

**Goal:** Replace the hand-rolled JWT + HttpOnly-refresh auth stack with self-hosted Ory Kratos as the identity provider. panel-api is a session-cookie validator; zero custom JWT code in the tree.

**Deliverables (all 9 steps + legacy removal shipped 2026-04-20):**

- `install_kratos()` in `install.sh`: downloads Kratos v26.2.0 (bumped from v1.3.1 in `4c44ef4`; SHA-256 pinned in `install/kratos.sha256`), provisions `jabali_kratos` MariaDB schema, renders `/etc/jabali-panel/kratos.yml` from `install/kratos.yml.tmpl`, runs `kratos migrate sql`, writes `/etc/systemd/system/jabali-kratos.service` under `jabali.slice`, enables + starts. Idempotent.
- nginx `/.ory/` proxy snippet in the panel vhost: same-origin fronting so browsers attach `ory_kratos_session` to every panel request.
- `panel-api/internal/kratosclient/`: `Client` with Whoami (10s LRU cache keyed by SHA-256(cookie)), admin-identity CRUD, bcrypt-passthrough canary, keyset-pagination identity scan.
- Auth middleware: `RequireKratosSession(kratosClient, users)` is the only `/api/v1/*` gate. Resolves Kratos identity UUID → panel ULID via `users.kratos_identity_id` so every downstream ownership check (`db.UserID == claims.UserID`) keeps working. DB is authoritative for `is_admin` (Kratos trait is advisory only).
- API user-create inline hook: `POST /api/v1/admin/users` creates the Kratos identity atomically with the panel row (compensating transaction — ADR-0003 preserved).
- `BootstrapAdmin` Kratos extension: same atomic semantics at boot — first-boot admin gets a Kratos identity in the same call. `auth.KratosIdentityWriter` interface lets tests exercise the rollback paths.
- SPA rewrite: `authProvider.ts` cookie-only, no access token in JS, no 401 refresh interceptor. `pages/Login.tsx` fetches Kratos flow on mount, renders `ui.nodes` as AntD form fields, submits to `flow.ui.action` with CSRF token round-tripped. `kratos.ts` thin typed wrapper over the self-service API. `MyProfile` Security card links out to `/.ory/self-service/settings/browser` for password + 2FA.
- Migration 000046: adds `users.kratos_identity_id VARCHAR(64) UNIQUE` (nullable, MariaDB treats multiple NULLs as distinct).
- Migration 000049: drops `refresh_tokens`, `totp_backup_codes`, and the `totp_*` columns on `users` — no legacy storage remains post-removal.
- Playwright E2E spec: `panel-ui/tests/e2e/kratos-login.spec.ts` covers login + role-based landing + CSRF token round-trip + cold-load whoami-401. `fixtures.ts` gained shared `/.ory/*` mocks so the existing tests keep working.
- ADR-0034 + runbook at `plans/m20-kratos-runbook.md`: design rationale, cutover procedures, day-2 operations (identity list/disable/delete, session revoke, MFA reset, recovery-code generation, Kratos DB loss recovery, split-host TLS).

**Dropped (intentionally):**

- **M5c panel-side 2FA (`/auth/2fa/*`, TOTP secret storage in panel DB, backup codes)** — 2FA now lives entirely in Kratos; users manage TOTP via `/.ory/self-service/settings/browser`.
- **Legacy JWT auth (`/api/v1/auth/{login,refresh,logout}`, `auth.JWTIssuer`, `middleware.RequireAuth`, refresh-token table)** — removed wholesale. No `auth.provider` flag, no `jwt_secret` config, no dual-mode middleware.
- **`jabali kratos-migrate`** — one-shot migration tool dropped after use; fresh installs have no legacy rows to backfill.

**Status:** Cutover 2026-04-20. Custom JWT surface fully removed the same day. No rollback window — ADR-0034 records the decision to compress it once the VM validated green.

**Depends on:** M1 (users table), M7 (MariaDB, reused for Kratos's own schema).
**Blocks:** M16 (Automation API tokens via Hydra — Hydra integrates with Kratos via the login-consent flow).

### M20.1: Two-factor (TOTP + recovery codes) via Kratos built-ins (SHIPPED 2026-05-07)

**Goal:** Surface TOTP + lookup_secret recovery codes on the panel
without adding any panel-side TOTP storage, custom JWT, or
`/auth/2fa/*` endpoints. Kratos already ships both methods + AAL
policy + admin reset; the panel just renders the existing flows.

**Deliverables:**

- `install/kratos.yml.tmpl` — `selfservice.methods.lookup_secret.enabled: true` + `session.whoami.required_aal: highest_available`. Users without 2FA stay aal1 (no behaviour change). Users with 2FA enrolled MUST present a second factor before whoami succeeds.
- `panel-ui/src/kratos.ts` — `totpEnrolmentDisplay()` reads QR `img` + secret `text` nodes from the `totp` group; `lookupSecretReveal()` reads the codes array out of the `lookup_secret` group's text node. Narrow helpers so cosmetic Kratos changes don't ripple.
- `panel-ui/src/shells/user/MyProfile.tsx` — renders the QR + base32 secret above the TOTP form; renders the recovery codes (copy-once) above the lookup_secret form. Reuses the existing settings-flow renderer; preferred group order already had `totp` + `lookup_secret`.
- Login AAL2 path uses the existing flow handler — Kratos returns the same flow id with `requested_aal: "aal2"` and TOTP nodes; `submitLoginFlow`'s `continue` branch already re-renders the form.
- `internal/kratosclient/admin.go` — `RemoveSecondFactor()` issues two JSON-Patch removes (`/credentials/totp` + `/credentials/lookup_secret`), 422 on missing path treated as success.
- `panel-api/internal/api/users_2fa_reset.go` — `POST /api/v1/admin/users/:id/2fa/reset`, admin-only, no-op on pre-Kratos accounts (NULL `kratos_identity_id`).
- `panel-ui/src/shells/admin/users/UserReset2FAAction.tsx` — confirm-modal row action on the admin Users table.
- ADR-0090 + `plans/2fa-totp-runbook.md`.

**Rejected:** the previously-drafted bespoke 2FA plan (custom `totp_backup_codes` table, AES-GCM secret encryption, `2fa_pending` JWT, `/auth/2fa/*` endpoints). Predated M20; would have duplicated everything Kratos already does. ADR-0090 records the decision.

**Depends on:** M20 ✅ (Kratos identity).

### M21: Drop Refine (SHIPPED 2026-04-21)

**Goal:** Remove the Refine framework from the panel SPA. After M20 made identity Kratos-native, Refine's `authProvider`/`dataProvider`/`resources`/`<ThemedLayoutV2>` shrank to indirection around calls panel-api already served natively. Wrappers (`<List>`/`<Create>`/`<Edit>`) were injecting chrome (auto headings, breadcrumbs, sticky save bars, card wrappings) the operator had spent a day trying to strip at the call-site — the framework itself was the source.

**Deliverables (all five waves shipped on `m21/drop-refine` 2026-04-21):**

- **Wave A (foundation):** `src/query.ts` (singleton `QueryClient`), `src/hooks/useQueries.ts` (`useListQuery` / `useOneQuery` / `useCreate|Update|DeleteMutation`), `src/hooks/useTableURL.ts` (URL-backed page/sort/q/order state via `useSearchParams`), `src/hooks/useSelectQuery.ts`, `src/auth/{AuthContext,RequireAuth,RequireAdmin,RequireUser}.tsx`. Additive — existing pages untouched.
- **Wave B (shell):** `src/App.tsx` rewritten to `QueryClientProvider > AuthProvider > BrowserRouter > ConfigProvider > Routes`. Drop `<Refine>`, `routerProvider`, `resources[]`, `<ThemedLayoutV2>`, `<ThemedSiderV2>`. `AdminLayout`/`UserLayout` become plain AntD `<Layout>` + `<Sider>` + filtered `<Menu>` + `<Header>` + `<Content>` + `<Footer>`. Single source of menu items at `src/nav.ts`. `JabaliHeader` uses `useAuth().logout` (hard-nav to `/login` after) instead of Refine's `useLogout`.
- **Wave C (admin pages):** mechanical rewrite of every page under `src/shells/admin/*` — users (list/create/edit), packages, domains, DNS, SSL, server settings, PHP pools, databases, database-users. `useTable` → `useTableURL`, `useForm` → `Form.useForm + useCreate|UpdateMutation + useOneQuery`, `<List>/<Create>/<Edit>` wrappers deleted. Also folded a Wave A wire-contract fix: panel-api returns `{data, total, page, page_size}` (not `{items, total}` as the blueprint had assumed), so `useListQuery` now projects `data → items` and serializes camelCase `pageSize` as wire-side `page_size`. This cleared the four pre-existing `users.spec.ts` failures that had been red on `main` since before Wave A.
- **Wave D (user pages + shared chrome):** same mechanical rewrite across `src/shells/user/*` — domains, databases (with Quick Setup + phpMyAdmin SSO), DNS, applications (with transitional-state polling via a plain `setInterval` effect instead of Refine's `refetchInterval`). Shared `Domain*` buttons and `dns/DNSRecordsPage` moved from `useInvalidate` + `useNotification` to `useQueryClient` + AntD `notification`. Dead `shellSider.tsx` and `RoleGate.tsx` deleted.
- **Wave E (cleanup):** `@refinedev/{core,antd,react-router,simple-rest}` removed from `package.json`; `@ant-design/icons` promoted to a direct dep (Refine used to pull it in transitively). `authProvider.ts`, `dataProvider.ts`, `searchableTableUtils.ts` deleted. `SearchableTable.tsx` trimmed to its string-`q` variant. Login/Consent test stubs dropped their `<Refine>` wrapper. `main.tsx`'s `@refinedev/antd/dist/reset.css` → `antd/dist/reset.css`. `admin/applications/AdminApplicationList.tsx` rewritten (it was scope-fenced for wt-a but its `useTable` usage would have blocked `@refinedev/*` removal).

**Measured results:**

- `grep -r "@refinedev" panel-ui/src panel-ui/tests panel-ui/package.json`: 0 hits
- Production JS bundle: 2,192 kB → **1,586 kB** (−27.6%), gzip 700.7 kB → **507.4 kB** (−27.6%) — beats the ≥50 kB gzipped target by ~4×.
- `node_modules` installed packages: 358 → 239 (−119).
- `npx tsc -b` clean; `npm test` 28/28 vitest; `npm run build` clean; `npm run test:e2e` 22/22.

**Depends on:** M20 (Kratos identity — supersedes Refine's `authProvider` role so removing it doesn't lose functionality).
**Related:** ADR-0037 (design rationale, rollback note, outcome), `plans/m21-drop-refine.md` (five-wave blueprint).

### M25: Localhost backend hardening via Unix sockets (SHIPPED — branch)

**Status:** All 8 steps committed to branch `m25/unix-sockets` 2026-04-23 in 4 waves (A foundation → B Kratos → C panel-api+Bulwark+MariaDB → D Stalwart audit + ship). ADR-0050 accepted; runbook at `plans/m25-unix-sockets-runbook.md`. M25.1 follow-up tracked separately (skip-networking + Kratos DSN flip — both gated on live-VM verification).

**Goal:** Every localhost-only HTTP backend becomes a Unix-domain socket with filesystem permissions; every `:::`-bound TCP service that doesn't support sockets gets an explicit `127.0.0.1` bind. The Kratos admin API — currently reachable on all interfaces with zero auth on prod VMs without an external firewall — stops being a takeover surface.

**Strategy:**
- New `jabali-sockets` system group (members: `jabali`, `www-data`, `jabali-webmail` — NOT `jabali-mail`); socket files mode `0660`. Connecting process needs both group membership AND `connect(2)` write — mode `0640` would silently break nginx (review F-H-2).
- nginx Form D upstream: `upstream u { server unix:/path; } ... proxy_pass http://u/;`. Roll back to TCP is a one-line edit (`server unix:...;` → `server 127.0.0.1:<port>;`) without touching any `proxy_pass` directive.
- panel-api strips self-TLS; nginx terminates real Let's Encrypt cert at the edge (port 8443 via new `install/nginx/jabali-panel-vhost.conf.tmpl`). Internal HTTP no longer pays per-request TLS handshake cost.
- Bulwark wrapped via `install/jabali-webmail/server-unix.js` (~80 lines) — Next.js custom-server pattern; install.sh re-deploys on every run so a Bulwark-tarball update doesn't strand us.
- Stalwart admin-http (`:::8080`) + ephemeral `:::35181` pinned to `127.0.0.1:8080` + `127.0.0.1:18181` via two new `NetworkListener` entries in `install/stalwart/apply-plan.json.tmpl`.
- LLMNR disabled via `/etc/systemd/resolved.conf.d/10-jabali-disable-llmnr.conf` drop-in.
- install.sh post-start verification: `verify_socket_perms` + `verify_no_all_interface_binds` after every service restart. Wrong perms or a leaked TCP bind → installer aborts loud rather than letting the operator discover a 502 in production.

**Scope reduction (M25.1):**
- MariaDB `skip-networking` — needs phpMyAdmin SSO to dial via socket first (sso.php + panel-api response shape).
- Kratos DSN flip from TCP to socket — needs a live-VM `kratos migrate sql` verification gate.

Both deferred together because skip-networking breaks Kratos otherwise. Step 6 ships the panel-api + PDNS DSN moves to socket (no functional change without skip-networking, but the dial-path plumbing is now in place).

**Depends on:** None (pure infrastructure hardening; composes existing systemd + nginx + Go + Node).
**Related:** ADR-0050 (architecture + threat model), `plans/m25-unix-sockets.md` (8-step blueprint), `plans/m25-unix-sockets-runbook.md` (day-2 ops + per-service rollback drop-ins).

---

### M24: IP address manager (SHIPPED)

**Status:** All 10 steps merged to `main` 2026-04-22 in 5 waves (A schema → B agent+API → C wire-contract domain/nginx/DNS → D admin+picker UI → E docs/E2E). ADR-0049 accepted; runbook at `plans/m24-ip-manager-runbook.md`. VM host-validation on 192.168.100.150 is an operator follow-up (unreachable at merge time).

**Goal:** First-class managed IP pool so the operator can host customer-isolated domains on dedicated IPs without forking the codebase. Each domain gets nullable `listen_ipv4_id` / `listen_ipv6_id` FKs; NULL ⇒ "use family default". Reconciler converges nginx (`listen <ip>:80`) and DNS apex `@` A/AAAA in the same pass — change reflected ≤ 60s after admin saves.

**Strategy:**
- `managed_ips` table seeded with the existing `server_settings.public_ipv4` / `public_ipv6` as defaults — pre-M24 installs map seamlessly.
- jabali binds addresses ephemerally via agent `ip.bind` (`ip addr add`); persistence is operator-owned via netplan/provider config (out of scope, runbook documents).
- Connectivity probe after every bind catches firewall misconfig before the operator deploys a domain.
- `is_user_selectable` gates the user-shell picker (curated subset).
- Out-of-scope services (Stalwart mail, Bulwark webmail, SFTP, PowerDNS, panel-api itself) keep their server-wide bindings — runbook table makes this explicit.

**Depends on:** None (composes existing reconciler + agent RPC + AntD UI).
**Related:** ADR-0049 (architecture + threat model), `plans/m24-ip-manager.md` (10-step blueprint), `plans/m24-ip-manager-runbook.md` (day-2 ops).

---

### M23: Responsive panel UI (SHIPPED — branch)

**Status:** All 9 steps committed to branch `m23/responsive` 2026-04-22. Awaits merge to main. ADR-0046 accepted. Runbook at `docs/runbooks/m23-responsive.md`.

**Goal:** Every page renders and works correctly on phone, tablet, and desktop viewports without horizontal viewport scroll, clipped content, or broken interactions.

**Driving evidence:** At 442×915 (Samsung Android portrait) the admin Dashboard "Disk" table's `Usage` column was clipped off-screen because no `<Table>` in `panel-ui/src` sets `scroll={{ x: … }}`. The `<Sider>` at 64px collapsed has no reachable re-open trigger on touch (chevron sits below the fold).

**Strategy:**
- `<Drawer>` sidebar below lg (<992px), persistent `<Sider>` at ≥lg (tablets 768–992px keep the sider).
- `scroll={{ x: "max-content" }}` as a default merged into `SearchableTableStringQ`; standalone `<Table>` usages add it inline.
- No custom CSS media queries in component files — use AntD `Col xs/sm/md/lg` and `Grid.useBreakpoint()` only.
- New Playwright mobile project (iPhone 13 / Pixel 5 / iPad Mini) runs alongside existing desktop E2E.

**Depends on:** None (purely UI).
**Related:** ADR-0046 (responsive strategy), `plans/m23-responsive.md`.

---

### M22: Magic-link → self-deleting SSO file (REWORK SHIPPED 2026-04-21)

**Status:** Rework shipped (all 8 steps merged to main 2026-04-21 in 4 waves). ADR-0040 accepted; ADR-0039 superseded. Operator runbook at `plans/m22-sso-file-runbook.md`; existing test VM (192.168.100.150) cleaned up via `plans/m22-rework-vm-teardown.md`.

**Goal:** One-click admin login from the panel to any managed WordPress install. Operator clicks "Log in to admin" on an Applications row → new tab opens → lands signed in to `/wp-admin` as the install's admin user.

**Why the rework:** The original magic-link design (ADR-0039) shipped end-to-end and was verified on test VM 192.168.100.150 the same day. Verification surfaced 5 connectivity / lifecycle gaps all caused by the same root pattern: persistent panel-side WordPress mu-plugin + HTTPS callback from WP back to the panel. (1) Plugin's "did sed run?" guard self-mutates and silently no-ops; (2) `update.go` doesn't sync the canonical mu-plugin source; (3) existing pre-M22 installs never get the per-install plugin copy; (4) nginx default vhost has no `/applications/.../validate` proxy → 444 silent drop; (5) panel self-signed cert isn't in OS CA bundle → `wp_remote_post sslverify=true` fails. All five disappear if there's no persistent WP-side code and no callback.

**New design (ADR-0040):** Self-deleting `jabali-sso-<43chars>.php` file written per login to the WP webroot — the Installatron / Softaculous pattern that has run at scale for ~15 years. Filename embeds a 256-bit `crypto/rand` nonce (the filename **is** the capability — no HMAC, no signing key, no DB row). Single-use via `flock(LOCK_EX|LOCK_NB)` + `unlink(__FILE__)`. TTL via systemd reaper sweeping every 30s. Wire contract `{url, expires_in}` preserved — only the URL shape changes from `?jabali_admin_login=<token>` to `/jabali-sso-<nonce>.php`. Eliminates the entire 5-item M22 follow-up list (placeholder guard fix, update.go sync, reconciler ensure, nginx proxy, OS CA trust) — none apply once there's no panel-side WP code or callback.

**Depends on:** M16R (OIDC rollback already landed).
**Related:** ADR-0038 (M16 rollback), ADR-0039 (M22 magic-link — superseded), ADR-0040 (M22 sso-file design + threat model), `plans/m22-rework-sso-file.md` (8-step rework blueprint, opus-reviewed).

---

## 7. Configuration

### Environment variables (`.env`)

```bash
DATABASE_URL=mysql://root:password@127.0.0.1:3306/jabali_panel
AGENT_SOCKET=/run/jabali/agent.sock
AGENT_TIMEOUT=5s
PANEL_ADDR=0.0.0.0:8443
KRATOS_PUBLIC_URL=http://127.0.0.1:4433
KRATOS_ADMIN_URL=http://127.0.0.1:4434
RECONCILER_INTERVAL=30s
```

### Config file (`config.toml` or via env)

```toml
[panel]
port = 8443
addr = "0.0.0.0:8443"

[auth.kratos]
public_url = "http://127.0.0.1:4433"   # panel proxies /.ory/* here
admin_url  = "http://127.0.0.1:4434"   # loopback-only, never exposed

[agent]
socket = "/run/jabali/agent.sock"
timeout = "5s"

[reconciler]
interval = "30s"
enabled = true

[database]
url = "mysql://..."
```

See `config.example.toml` for the full annotated shape (including `[cors]`).

### Server settings (DB-backed)

All read/write via `/api/v1/system/settings`:
- `hostname` — server's FQDN
- `ns1` — primary nameserver IP (for DNS SOA)
- `ns2` — secondary nameserver IP
- `public_ipv4` — auto-detected public IPv4
- `public_ipv6` — auto-detected public IPv6

---

## 8. Security posture

**Key principles** (see `~/.claude/rules/security.md` for global standards):

1. **No hardcoded secrets** — all via environment variables or config files (not in code)
2. **Input validation** — every user input validated at API boundaries
3. **SQL injection prevention** — GORM parameterized queries throughout
4. **Authentication** — Ory Kratos session cookies are the only credential. Kratos stores passwords with bcrypt cost-12 (argon2id rehash disabled). Panel-api validates every request via `/sessions/whoami` and never signs tokens.
5. **Authorization** — RBAC middleware (RequireAdmin, RequireOwner) on protected routes. DB is authoritative for `is_admin`; Kratos identity traits are advisory only.
6. **Agent argument sanitization** — all agent command arguments escaped before passing to shell
7. **Rate limiting** — per-IP on auth endpoints, per-token on automation API (planned M15)
8. **Error messages** — never leak sensitive data (database names, file paths, internal IPs)

See ADRs for architectural security decisions:
- ADR #0001 (agent socket never over network)
- ADR #0002 (DB is truth, not config files)
- ADR #0003 (one write path = API only)

---

## 9. Testing

**Go backend:**
- Unit tests: `go test ./...` (GORM models, auth, reconciler)
- Integration tests: sqlite in-memory DB for agent mocks
- Coverage: `go test -cover ./...` (target 80%+)
- Linter: `golangci-lint run`

**React frontend:**
- Unit tests: Vitest (component logic, hooks)
- E2E tests: Playwright (user flows: login, domain create, DNS records)
- Build: `npm run build`

**CI/CD:**
- **Gitea Actions** pipeline at `.gitea/workflows/ci.yml` runs on every push +
  PR to `main`. Three parallel jobs on a self-hosted `act_runner` (host-mode,
  no Docker): `Go tests + vet` (runs `make vet` + `make test -race`),
  `panel-ui unit tests (vitest)`, and `Playwright E2E` (builds SPA, installs
  Chromium, runs `@playwright/test --project=chromium`).
- Concurrency group `ci-${{ github.ref }}` with `cancel-in-progress: true`
  auto-cancels stale runs on rapid pushes so the runner queue stays shallow.
- **Branch protection on `main`** (loose): PR merges blocked until all 3 CI
  checks green, direct-push whitelisted for `shukivaknin` only, force-push
  disabled, admin merge override enabled as escape hatch for genuinely-broken
  CI. Configured via `POST /repos/:owner/:repo/branch_protections`.
- Pre-commit hook `/.claude/hooks/block-agent-commit-main.sh` blocks direct
  `git commit` on `main` to prevent parallel-session stomping — workflow is
  branch + merge, enforced locally before the server-side protection kicks in.

---

## 10. Where to add new features

Use this table to navigate the codebase when adding a new capability:

| Capability | Database | Agent Command | API Handler | UI Page | CLI Subcommand |
|------------|----------|---|---|---|---|
| New entity (e.g., SSL certs) | Migration `000NNN_*.sql` | `ssl.issue`, `ssl.revoke` | `panel-api/internal/api/ssl.go` | `panel-ui/src/shells/SslPage.tsx` | `jabali ssl` (future) |
| New system config | `server_settings` table | `system.set_*` | `/api/v1/system/settings` | `ServerSettingsPage` | `jabali config` (future) |
| New service integration | New table + relations | `service.start`, `service.stop` | `/api/v1/services/{name}` | `ServicesPage` (planned) | `jabali service` (future) |
| New reconciler concern | Domain/zone/record FK | `domain.configure`, etc. | Auto-triggered via `/api/v1/reconcile/*` | — | — |

**Pattern for new entity:**

1. Create migration: `panel-api/internal/db/migrations/000NNN_*.sql`
2. Create GORM model: `panel-api/internal/models/*.go`
3. Create repository: `panel-api/internal/repository/*.go`
4. Create API handler: `panel-api/internal/api/myentity.go` (CRUD routes)
5. Register routes in Gin: `panel-api/internal/server/routes.go`
6. Create agent command (if needed): `panel-agent/internal/commands/myentity_*.go`
7. Register command in agent: `panel-agent/internal/commands/registry.go`
8. Create UI page/component: `panel-ui/src/shells/MyEntityPage.tsx`
9. Add Refine resource: `panel-ui/src/resources/` (if CRUD via REST)
10. Add WebSocket broadcast (if real-time updates needed): `panel-api/internal/websocket/hub.go`

---

## 11. Changelog

| Milestone | Shipped | Anchor commit |
|-----------|---------|---|
| M1: Foundations | 2026-01-XX | Earlier commits (not enumerated) |
| M2: Domain lifecycle + Nginx + Redirects | 2026-02-XX | `fb3abea`, `0e92a04` |
| M3: Server Settings + Hostname + IPs | 2026-03-XX | `6b5dea4`, `ab47b7d` |
| M4: DNS zones + records + Secondary NS | 2026-04-16 | `f7464a2`, `4b87395` |
| M5: SSL / Let's Encrypt (core + resilient ACME) | 2026-04-17 | `ba54273`, `66ae6d2`, `34379db`, `786079a`, `fc8ff7d` |
| M5c: Two-factor authentication (TOTP + backup codes) | 2026-04-19 | `0b68048` through `7b1ac16` on `feat-2fa-totp` |
| M6: Email (Stalwart + Bulwark webmail + mailbox SSO) | 2026-04-22 | `caa6e16` through `fc73da4` on `m6/email-stalwart` (ADRs 0041-0045) |
| M7: Databases (MariaDB) | 2026-04-17 | ADRs 0018-0022 |
| M8: Cron (SHIPPED) | 2026-04-18 | ADR-0029 |
| M9: PHP/FPM pools | 2026-04-17 | 1aaa507 (ADR), 5dbf471 (shipped) |
| M9.6: PHP extensions tab | 2026-04-19 | ADR-0031; steps in `5e6b2ab`, `8c06612`, `f345cce`, `2c8b3a3` |
| M10: WordPress | 2026-04-18 | `85ed8b4` through `main HEAD` |
| M11: File Manager (AntD-native, superseded filebrowser) | 2026-04-19 | ADR-0030, Waves A–E in `main` |
| M12: SFTP via openssh | 2026-04-18 | `aaa0c82` through `0256773` |
| M13: SSH shell sandbox | Shipped 2026-04-26 | ADR-0067; bubblewrap+nspawn modes; jabali-ssh-shell binary |
| M14: Notifications | 2026-04-24 | ADRs 0056-0059; runbook `plans/m14-notifications-runbook.md` |
| M15: Migration importers (renumbered → M35) | Renumbered 2026-04-29 | Original M15 number got reused for DNSSEC (shipped 2026-04-25). Migration importers re-tracked as M35 — see entry below. |
| M16: Automation API | Planned | — |
| M17: Diagnostic reports | Planned | — |
| M18: Per-user resource limits | 2026-04-19 | Waves A-G on `main` (`caebe7b` → `aaa6bd0`); ADR-0032; runbook at `plans/m18-resource-limits-runbook.md`; host validation complete 2026-04-21 (4 bugs fixed incl. rate-limit zone ordering in `45a0ea6`) |
| M19: Applications Framework (all 8 steps) | 2026-04-19 | `m19/*` branch stack `733d6b8` → MediaWiki commit; ADR-0033; runbook at `docs/runbooks/applications.md`; DokuWiki + MediaWiki SHA-256 tarball pins captured 2026-04-21 |
| M20: Kratos identity migration (all 9 steps + legacy removal) | 2026-04-20 | ADR-0034; runbook at `plans/m20-kratos-runbook.md`; Waves A–E on `main`; legacy JWT stack + M5c panel-side 2FA + `kratos-migrate` tool all deleted in the same batch — no dual-mode, no rollback flag |
| Infra: Gitea CI + branch protection | 2026-04-20 | `.gitea/workflows/ci.yml` (3 parallel jobs), self-hosted `act_runner` in host-mode (no Docker/Podman), loose branch protection on `main`. Also fixed pre-existing data race in `TestApplications_CreateWordPress_HappyPath` (`applications_service.go`) that CI's `-race` flag caught. Commits `b181c74` (workflow), `5d1f9a7` (race fix). |
| M21: Drop Refine (all 5 waves) | 2026-04-21 | ADR-0037; blueprint at `plans/m21-drop-refine.md`; Waves A–E on `m21/drop-refine`; 4 `@refinedev/*` packages removed; production JS −606 kB (−27.6%) / gzip −193 kB; `@ant-design/icons` promoted to direct dep; `useTableURL` + `useQueries` + `AuthContext` now power every list/form/whoami call |
| M24: IP address manager (all 10 steps) | 2026-04-22 | ADR-0049; blueprint at `plans/m24-ip-manager.md`; runbook at `plans/m24-ip-manager-runbook.md`; Waves A–E on `main` (`0948a9a` → `7a8a1ff`); migrations 000057 + 000058 (MariaDB 11.4+ reserved-word fix in `7a8a1ff`); agent commands `ip.list`/`ip.bind`/`ip.unbind`; reconciler converges per-domain nginx listen + DNS apex A/AAAA every tick; admin UI `/jabali-admin/ips` + per-domain picker on DomainEdit; host-validation on 192.168.100.150 is operator follow-up |
| M25: Localhost backend hardening via Unix sockets (all 8 steps) | 2026-04-23 | ADR-0050; blueprint at `plans/m25-unix-sockets.md`; runbook at `plans/m25-unix-sockets-runbook.md`; Waves A–D on `m25/unix-sockets` branch; new `jabali-sockets` group + `verify_socket_perms`/`verify_no_all_interface_binds` helpers; Kratos admin+public on `/run/jabali-kratos/*.sock`; panel-api on `/run/jabali-panel/api.sock` + nginx TLS terminator on :8443; Bulwark on `/run/jabali-bulwark/bulwark.sock` via custom `server-unix.js`; Stalwart admin-http pinned `127.0.0.1:8080` + ephemeral `:35181` → pinned `127.0.0.1:18181`; LLMNR drop-in disable. **M25.1 deferred:** MariaDB `skip-networking` + Kratos DSN flip (both gated on live-VM verification) |
| M26: Admin Security Tab (all 9 steps) | 2026-04-24 | ADRs 0053 (CrowdSec) / 0054 (UFW) / 0055 (ModSecurity — SUPERSEDED 2026-04-26); blueprint at `plans/m26-security-tab.md`; runbook at `plans/m26-security-tab-runbook.md`; install foundation pulls CrowdSec 1.7.x from packagecloud (Debian-stock 1.4.6 lacks unix-socket support); CrowdSec LAPI on `/run/crowdsec/api.sock` mode 0660 group `jabali` per ADR-0050; UFW idempotent enable with allow-list-before-enable + typed-YES gate on enable/disable; admin tabs at `/jabali-admin/security?tab={crowdsec,ufw}` (modsec tab dropped in the supersession). M27 extensions stacked on top: allowlists (ADR-0061), alerts, Console enroll/disenroll (ADR-0062), captcha remediation, per-scenario remediation override (ADR-0063), AppSec geoblock (ADR-0060). |
| M27: CrowdSec extensions + WAF default-on | 2026-04-26 | Five M27 cards inside the CrowdSec sub-tab — allowlists, alerts, Console, captcha, per-scenario; plus AppSec geoblock card (ADR-0060). AppSec engine listening 127.0.0.1:7422; nginx-bouncer dials in-band. Default rule set: `base-config + vpatch-* + generic-*`. Default install collections: linux, sshd, nginx, base-http-scenarios, http-cve, wordpress, whitelist-good-actors, appsec-virtual-patching, appsec-generic-rules. Console card branches on enrolled state (file truth via `/etc/crowdsec/online_api_credentials.yaml` + `cscli capi status`); Disenroll button wipes the credentials file + reloads crowdsec. Hub sub-tab shows curated Recommended-free-blocklists picker (10 items) with one-click Install/Remove. ModSecurity stack fully removed in the same window — apt purge in `cleanup_modsecurity` install.sh function, schema drop in migration `000074_drop_modsec_columns`, ADR-0055 marked SUPERSEDED. |
| M30: Backup & Restore (all 12 steps) | 2026-04-28 | ADR-0075; blueprint at `plans/m30-backup-restore.md`; runbook at `plans/m30-backup-restore-runbook.md` + wire-format reference at `docs/runbooks/backup-format.md`; branch `m30/backup-restore`. Foundation: restic apt install + shared repo `/var/lib/jabali-backups/repo/` (root:jabali 0750) + password file `/etc/jabali-panel/restic-repo.password` (root:jabali 0640) + `restic init` (idempotent) + `jabali-backup-retention.timer` (daily 04:30) + `jabali backup retention apply` CLI subcommand + migrations 000084 (backup_jobs) / 000085 (server_settings retention knobs). Wave-gate `internal/backup/`: typed restic CLI wrapper + manifest schema (schema_version=1) + canonical tagging convention (`jabali` blanket + `kind=` + `job-id=` + `stage=` + `user-id=` / `system=`). Account stages: `backup.home` (50 GiB ceiling), `backup.databases` (mariadb-dump → restic stdin per-db), `backup.mailboxes` (stalwart-cli export staged in `/run/jabali-backup/<job>/`, skips with `mailbox_export_skipped:stalwart_down` when Stalwart inactive). Orchestrator: `backup.create` runs all stages then writes a stdin-piped manifest snapshot tagged `stage=manifest`; `backup.status` reports per-job snapshot inventory. Restore: `backup.restore` with global flock at `/var/lib/jabali-backups/.restore.lock`. REST: `/admin/users/:id/backups` + `/admin/backups/:job_id/{status,download,cancel}` + `/admin/backups/restore` + `/me/backups`. UI: `/jabali-admin/backups` page with Account / System tabs + `MyProfileBackupCard` on user shell. System: `system.backup` + `system.restore` agent handlers + `jabali system restore --force` CLI for bare-metal recovery (no UI per ADR-0075). |
| M30.1: Backup destinations + scheduled backups | 2026-04-28 | ADR-0078; blueprint at `plans/m30.1-backup-schedules-destinations.md`; runbook at `plans/m30.1-backup-destinations-runbook.md`; branch `m30.1/backup-destinations-and-schedules`. Restic-native backends only — `local | sftp | s3 | b2 | azure | gcs | rest` (no rclone). Migrations 000086 (`backup_destinations`), 000087 (`backup_schedules`), 000088 (`backup_schedule_destinations` M:N), 000089 (`backup_jobs.schedule_id`), 000090 (`backup_copy_jobs`). Credentials in 0600 root:root env files at `/etc/jabali-panel/restic-remotes/<dest-id>.env` (DB stores only the file pointer). Three in-process tickers: scheduler 60s (`internal/backupscheduler`), finalizer 30s (`internal/backupfinalizer` — bridges agent's manifest snapshot to `backup_jobs.status=succeeded` and fans out copy_jobs), copy worker 60s (`internal/backupcopyworker` — spawns `systemd-run --unit=jabali-backup-copy-<id>.service` per row, hardened identical to retention timer). CLI `jabali backup copy run --copy-job-id=<id>` is the systemd-run entrypoint. Admin REST: `/admin/backup-destinations` (CRUD + `/test`) + `/admin/backup-schedules` (CRUD + `/run-now`) + `/admin/backups/:job_id/copy-jobs` for per-destination status drilldown. UI: two new tabs on `/jabali-admin/backups` (Destinations + Schedules) with cron-preset radio (Daily/Weekly/Monthly/Custom) and per-row "Test" + "Run now" buttons. Async `restic copy` after local backup seals; single shared repo password v1 (per-destination password = M30.2). |
| M33: Malware detection stack (all 10 steps) | 2026-04-27 | ADR-0072; blueprint at `plans/m33-malware-detection.md`; runbook at `plans/m33-malware-detection-runbook.md`; branch `m33/malware-detection` (`63b5dc3` → tip). Schema: migration 000081 (malware_quarantine + malware_events + yara_custom_rules + tetragon_policy_state + malware_settings singleton). install.sh `install_malware_stack` + `install_tetragon`: ClamAV + clamav-freshclam + LMD 1.6.6 (sha256-pinned `3ef7fd06...`) + YARA + sysctl `fs.inotify.max_user_watches=524288` + Tetragon v1.6.1 GitHub release tarball (BTF-gated, skip+sentinel on no-BTF). 4 systemd units: jabali-maldet-monitor.service (`maldet --monitor USERS`, hardened), jabali-maldet-update-signatures.timer (daily 02:30), jabali-maldet-scan-daily.timer (daily 03:00), jabali-malware-quarantine-purge.timer (daily 04:00 → `jabali malware-purge`). 4 default Tetragon TracingPolicies (jabali-exec-from-tmp / chmod-x-docroot / curl-bash / suspicious-syscalls). 16 agent commands `security.malware.{status,scan_path,scan_user,scan_status,quarantine.{list,restore,delete},update_signatures,monitor.reload,event,yara.{list,upload,toggle,delete},tetragon.{policies,toggle}}` + sessionwatcher (5s poll on `/usr/local/maldetect/sess/`). 17 panel-api routes incl. RequireLocalhost-gated `/event` ingest. eventsources/malware.go (30s ticker, severity-tier gating, Tetragon always critical). New `Malware` tab between CrowdSec and UFW with 7 sub-tab cards (Overview/Quarantine/Events/ManualScan/YARA/Tetragon/Settings). M14 dispatch via 2 EventKinds (malware.quarantine.added / malware.realtime.critical) over all 6 channels. **Deferred to follow-up:** explicit reconciler hook on user-create/delete (LMD inotify_minutes=45 covers it periodically); tetragon-relay log shim for ingest of Tetragon JSON events into malware_events.source=tetragon (rows still ingest manually). |
| M34: Per-user PHP-FPM egress firewall (8 steps shipped on branch) | 2026-04-29 | ADR-0084 (Proposed — pending live VM smoke); blueprint at `plans/m34-per-user-egress-firewall.md`; runbook at `plans/m34-per-user-egress-runbook.md`; branch `m34/per-user-egress` (tip `dcf8793d`). Closes the PHP-runtime-exfil gap left after Snuffleupagus rejection (operator FP intolerance). Schema: migrations 000100 (`user_egress_policies`), 000101 (`user_egress_requests`), 000102 (`server_settings` egress defaults + `egress_burst_threshold`). One nftables `inet` table `jabali_per_user` rendered to `/etc/nftables.d/jabali-per-user-egress.nft` by reconciler; `socket cgroupv2 level 3` + `vmap @cgroup_to_chain` keys per-user chains by full slice path `jabali.slice/jabali-user.slice/jabali-user-<USERNAME>.slice` (M18 nested topology, depth 3). Three states `off | learning | enforced`; LEARNING auto-flip via `jabali-per-user-egress-flip.timer` (daily 03:30 UTC + 15m jitter, 7-day soak) honoring `/etc/jabali/per-user-egress.mode = learning` operator pin. Default allowlist (loopback4 + RFC1918, loopback6 + ULA + link-local, TCP {25,53,80,443,465,587}, UDP 53) covers MariaDB / Redis / DNS / WP+Composer auto-update / SMTP-submission with zero FP on stock LAMP. Per-user counter `user_<U>_drops` exposed via `nft -j list counters` and tick-deltaed into `user_egress_policies.drop_count_24h`; M14 event source `egress.drop.burst` fires at `server_settings.egress_burst_threshold` (default 50) with 15-min per-user cooldown. Two agent commands `user.egress.{apply,read_counters}`. 9 REST routes (admin: GET/PUT `/admin/users/:id/egress` + GET `/admin/egress-requests` + POST `/admin/egress-requests/:id/{approve,deny}` + GET `/admin/egress-summary`; user: GET `/me/egress` + GET `/me/egress/requests` + POST `/me/egress/request` + DELETE `/me/egress/requests/:id`). New `Egress` tab on `/jabali-admin/security` with stats card + per-user policy table + Drawer-based extras CRUD + pending request approve/deny. CLI `jabali per-user-egress flip-mature [--soak-days N] [--dry-run]` runs the nightly auto-flip. install.sh `install_per_user_egress()` + `jabali-per-user-egress-load.service` (boot-time `nft -f`) + first-install marker chooses LEARNING (existing host upgrade) vs ENFORCED (fresh) at install time. **Deferred:** live VM end-to-end smoke (plant fsockopen webshell → SYN drop + counter increment + M14 fire); `/me/egress` SPA page; Server Settings global-allowlist editor (operator edits via SQL today); handler unit tests for `internal/api/user_egress.go`; Step 9 Tetragon companion policy for L3 audit (phase 2). |
| M39: Remove Tetragon, replace with narrow auditd | 2026-04-30 | ADR-0072 amendment + new ADR-0085; blueprint at `plans/m39-remove-tetragon-narrow-auditd.md`; runbook at `plans/m39-remove-tetragon-narrow-auditd-runbook.md`; branch `m39/remove-tetragon-narrow-auditd`. Removes the M33 Tetragon stack (k8s-shaped, BTF-fragmented, 4 default TracingPolicies were noise cannons on shared hosting, relay-shim deferred indefinitely → installed-but-unused). install.sh `install_tetragon()` deleted (~270 lines incl. BPF mount, 4 TracingPolicies, GitHub-tarball install, BTF probe); `cleanup_tetragon_legacy()` added — idempotent sweep on `jabali update` for upgrade hosts (mask + stop + disable units; rm `/opt/tetragon`, `/etc/tetragon`, `/var/log/tetragon`, sentinel, BPF map dir, binaries). bpftool apt block dropped. Replacement: `install_audit_exec()` writes `/etc/audit/rules.d/jabali-exec.rules` with 11 narrow rules (closed-set: `/bin/{bash,sh,dash}`, `/usr/bin/{wget,curl,nc,ncat,socat,python3,perl,php}`), all gated by `auid>=1000` and `auid!=4294967295`, single tag `jabali_susp_exec` (PHP CLI separately tagged). NOT blanket-syscall — would drown the operator. panel-agent `internal/commands/security_audit.go` adds `security.audit.{recent,by_user}` shelling out to `ausearch -k jabali_susp_exec --raw`; usernames resolved via `getent passwd` cache. panel-api `/admin/security/malware/audit/recent` forwards to recent. New "Exec audit" sub-tab on the Malware tab (read-only AntD Table, 60s auto-poll). Migration 000106 drops `tetragon_policy_state` table + `malware_settings.tetragon_enabled` column (both `IF EXISTS`). 15 files net -1014 LOC scrub: jabali-tetragon-relay binary deleted, 3 agent handlers + 5 panel-api types + 2 UI hooks + TetragonCard + scanner enum entry all gone. **Step 7 SHIPPED 2026-05-09 (`e5dbeb5e`):** `exec.audit.burst` event source at `panel-api/internal/eventsources/exec_audit_burst.go`. Polls `security.audit.recent` every 5 min, buckets by username + filters to current 5-min window so a slow tick doesn't double-fire on stale events, fires per-user when count crosses 20-events-per-window threshold (between WP install legitimate ~10/window and compromised account 50+/window). 30-min per-user cool-off via existing shouldFire history-table check. Disabled when AgentCaller nil. **Per-auid audit exclusion list** still operator-hand-edited. **Skipped:** AppArmor for daemons (M40, separate blueprint), AIDE FIM (M42, separate blueprint). |

---

| M40: AppArmor for jabali daemons + critical system services | 2026-04-30 | ADR-0086; blueprint at `plans/m40-apparmor-jabali-daemons.md`; runbook at `plans/m40-apparmor-jabali-daemons-runbook.md`; branch `m40/apparmor-jabali-daemons`. Closes the panel-RCE blast radius — a panel-api or panel-agent compromise no longer reads `/etc/shadow`, `/home/*/wp-config.php`, or `/etc/letsencrypt/live/*/privkey.pem`. install.sh `install_apparmor()` apt-installs `apparmor` + `apparmor-utils` if missing, probes the LSM (writes `/etc/jabali/.apparmor-grub-pending` if a GRUB edit + reboot is needed to activate), then `apply_apparmor_profiles()` copies + parses + sets mode for every `install/apparmor/usr.local.bin.jabali-*` source profile. **5 profiles shipped:** `jabali-panel` (HTTP API — tight: r `/etc/jabali/`, rw `/var/lib/jabali-panel/`, sockets, hard-deny `/etc/shadow`/`/home/**`/`/root/**`); `jabali-agent` (privileged orchestrator — wider cap set incl. `net_admin`/`sys_admin`, named-exec list of ~50 binaries with `ix` so children inherit); `jabali-bulwark` (Node.js public-facing — tight, **hard-deny `/etc/jabali/`** so a bulwark RCE can't leak panel secrets); `stalwart-mail` (mail daemon listening on 25/465/587); `jabali-kratos` (identity service — two unix sockets per M25). NOT shipped: php-fpm (operator FP intolerance — same cliff that killed Snuffleupagus + Tetragon defaults); mariadb / redis / pdns (vendor packages ship their own AppArmor profiles, install.sh leaves them alone). **Default mode on first install: complain** (audit-only, 7-day burn-in soak) so operator never gets a "panel broke after upgrade" surprise. Upgrade path preserves the operator's current per-profile mode. New CLI `jabali apparmor flip-mature [--profile X] [--dry-run]` + `jabali apparmor status`. panel-agent `internal/commands/security_apparmor.go` adds `security.apparmor.{status,set_mode}` (set-mode allowlist hard-coded to the 5 jabali-shipped profile names — arbitrary input rejected). panel-api `/api/v1/admin/security/apparmor/{status,profiles/:name/mode}`. New `AppArmor` sub-tab on the admin `/jabali-admin/security` page — read-only profile list + per-profile flip behind a confirm modal that names the risk before enforce. **Live VM smoke pending** on 192.168.100.150: `aa-status | grep jabali-` lists 4+ profiles in complain; `aa-exec -p jabali-panel -- cat /etc/shadow` denies; 24h workload produces 0 `apparmor="DENIED"` lines for jabali-* profiles; per-profile flip via CLI works without breaking the daemon. **Deferred:** soak-report systemd timer (operator can grep AVC logs themselves); php-fpm profile (operator demand only); mariadb/redis/pdns enforce (operator-driven). |

| M42: AIDE file integrity monitor for system binaries + configs | 2026-04-30 | ADR-0087; blueprint at `plans/m42-aide-fim-system-integrity.md`; runbook at `plans/m42-aide-fim-runbook.md`; branch `m42/aide-fim-system-integrity`. Closes the system-file gap LMD (user docroots) doesn't cover. install.sh `install_aide()` apt-installs `aide` + `aide-common`, renders `/etc/aide/aide.conf` with watch list (`/bin /sbin /usr/bin /usr/sbin /usr/local/bin /usr/local/sbin /lib /lib64 /usr/lib /etc /boot /root`) + canonical exclude list (`/etc/jabali/`, `/etc/letsencrypt/{live,archive,csr,keys,renewal,accounts}/`, every reconciler-managed config tree, AIDE rotation artefacts, /var, /run, /proc, /sys, /tmp, /home, /dev). Initial DB build is async-background `aide --init` so the install pipeline doesn't block (~2-5 min). Daily `jabali-aide-check.timer` at 04:30 UTC + 15-min jitter; service is hardened (ProtectSystem=strict, ReadWritePaths=/var/lib/aide /var/log/aide). panel-agent `internal/commands/security_aide.go` adds `security.aide.{status,check}` (status reads /var/log/aide/aide.report.log + DB mtime; check synchronously runs `aide --check` with 10-min timeout). panel-api `/api/v1/admin/security/aide/{status,check}`. New `AIDE` sub-tab on `/jabali-admin/security` (6th top-level after AppArmor) with Statistic row (DB age, last check, summary counts), success/warn Alert, sample table, Refresh + Run-check-now buttons. **M14 event source `aide.tamper.detected`** — 5-min tick, 24h cooldown keyed on report timestamp; severity escalates to `critical` for diffs in /etc/passwd, /etc/shadow, /etc/sudoers, /usr/local/bin/jabali-*, /root/.ssh/authorized_keys. New CLI `jabali aide {status,rebuild --full}`. **Live VM smoke pending** on 192.168.100.150: post-install dpkg -l aide returns installed; aide.db exists root:root 0600; manual `aide --check` returns "no differences"; synthetic tamper fires aide.tamper.detected. **Deferred to phase 2:** off-host DB shipping (sign + S3); partial re-baseline (`jabali aide rebuild --paths /usr/local/bin/jabali-*` to absorb panel-binary updates without nuking the whole DB).

---

| M44: Automation API tokens (HMAC-signed scoped bearers) | 2026-05-09 | ADR-0093; blueprint at `plans/automation-api-tokens.md`; runbook at `plans/automation-api-tokens-runbook.md`. Closes the gap left after M16 Hydra rollback — external automations (CI scripts, monitoring, partner integrations) need a small read-only HTTP surface without holding a Kratos session. Migration 000116 (automation_tokens) + AutomationToken model with AutomationScopes typed wrapper + `.Has()` wildcard matcher. middleware/automation_hmac.go parses `Authorization: Jabali-HMAC kid=…, ts=…, sig=…`, looks up token by kid, decrypts secret via ssokey, recomputes `HMAC_SHA256(secret, METHOD || PATH || ts || hex(sha256(BODY)))`, ConstantTimeCompares. 5-minute clock-skew window. Body cap 1 MiB. Verified token stashed in gin context for `RequireScope("read:domains")`. BumpLastUsed in goroutine — never blocks. Admin endpoints (RequireAdmin): `POST /admin/automation/tokens` (mint, returns plaintext secret ONCE), `GET` (list), `DELETE /:id` (soft revoke). Public routes (HMAC-only, OUTSIDE Kratos): `GET /api/v1/automation/{domains,users,applications,status}`. Scopes: `read:*` (wildcard) + `read:domains` / `read:users` / `read:applications` / `read:status`; `write:*` reserved + rejected at mint. Admin UI `/jabali-admin/automation` — list table + Mint Drawer + one-time-secret reveal Modal (copy-to-clipboard + "saved it" warning). ~~**Threat model caveat:** no nonce/JTI store~~ **CLOSED 2026-05-09 (`b1e4fa75`):** Redis SETNX on `automation:replay:<kid>:<sig>` with TTL = clock-skew window + 1 min grace gates each verified signature. Per-token scoping prevents cross-tenant collision. Production fail-closed: Redis-down returns 503 'replay store unavailable', not 200. `RequireAutomationHMAC(repo, key, *redis.Client)`; nil disables (test only). Original caveat preserved with strikethrough. |

| M13.1: Per-domain bandwidth tracking via goaccess | 2026-05-09 | Closes the "bandwidth daily sync from nginx access logs" cherry-pick from the old PHP-era blueprint. Reuses M13's existing goaccess install — no new deps, no chart-library bloat. Migration 000115 (bw_daily(domain_id, day, bytes_total, requests_total) — composite PK, ON DELETE CASCADE). panel-agent `bandwidth.scan_day` runs goaccess against `/var/log/nginx/<domain>-access.log.1` (yesterday's rotated log; logrotate's delaycompress keeps it uncompressed for the first day) → emits per-domain `{bytes_total, requests_total, day}`. panel-api `StartBandwidthTicker` fires every 24h (+60s warm-up) → upserts. BWDailyRepository: SumForDomain, SumByDomainForUser, SumByDomainIDs (batch), SumPerDayForDomain. New endpoint `GET /domains/:id/bandwidth` returns 30d totals + daily series. UI: `BW (30d)` Table.Column on admin DomainList + user UserDomainList (denormalized via `bytes_30d` field on list response, single batch SumByDomainIDs per page). New `<Sparkline>` (inline-SVG, no third-party dep) + `<DomainBandwidthCard>` (mounted on admin DomainEdit). New `humanBytes` shared util. Quota events: `bandwidth.quota.warn` at ≥80%, `bandwidth.quota.crit` at ≥100% of `BandwidthQuotaMB`; per-user dedupe with 6h cooldown. **Deferred:** auto-suspend on quota crit (needs UX for suspended user + grace period). |

| M40: AppArmor profiles ALL PARKED (Amendment 2026-05-09) | 2026-05-09 | ADR-0086 amended. Live audit on mx via `aa-exec -p <profile> -- /test_connect <socket>` exposed two structural problems: (1) AA 4.x on Debian 13 returns EACCES on every Unix-socket `connect()` made by an in-profile process regardless of `unix (...)` rules / `network unix stream` / abstractions/mysql / explicit path rules — disabling the profile lifts the block, (2) three of four non-agent profiles (panel-api, kratos, stalwart-mail) had `profile <name> flags=(complain) {` declarations WITHOUT a binary path, so they never auto-attached. Profiles were either anti-features (jabali-agent — broke `dns.zone.upsert`) or cosmetic. All 5 profile files renamed to `*.disabled`; new `cleanup_apparmor_legacy()` aa-disables + removes the live `/etc/apparmor.d/usr.local.bin.jabali-*` files on every install + `jabali update` tick + restarts each daemon (jabali-agent / jabali-panel / jabali-webmail / jabali-kratos / jabali-stalwart). System-daemon AA profiles (mariadb/redis/pdns_*) from `apparmor-profiles-extra` continue to load + complain-mode; flip-mature CLI works for those. M40.1 blueprinted at `plans/m40-1-apparmor-rewrite.md` for the eventual re-author. |

---

| M13.1.1: Bandwidth quota auto-suspend (opt-in) | 2026-05-09 | Builds on M13.1 ledger. New `server_settings.bandwidth_quota_enforce_enabled` toggle (default OFF) gates a per-tick reconciler that walks users with package quota > 0, sums month-to-date bytes via SumByDomainForUser, and on ≥ 100% sets every owned domain `is_enabled=false + is_quota_suspended=true`; on ≤ 80% reverses for is_quota_suspended-marked rows. Manual operator disables (is_quota_suspended=false) are NEVER auto-restored — that's why the column exists. Migration 000117 adds both columns. Admin Server Settings → Storage tab grows the toggle with a warning Alert ("notifications fire regardless; this only controls auto-disable"). Suspended domains tagged `Suspended (quota)` orange chip in admin Domains list. |

| M40 cleanup follow-ups (QA round 3) | 2026-05-09 | Two install regressions surfaced + fixed: (1) `/var/lib/stalwart` ownership drift on reinstall — `install -d -o ... -g ...` only sets the dir itself, not its existing sub-tree; appended a recursive chown after install -d for /var/lib/stalwart + /etc/stalwart so RocksDB reopens cleanly across re-installs. (2) AppArmor jabali-* profiles stayed in kernel memory after `jabali update` even though cleanup_apparmor_legacy rm'd the on-disk file; appended `aa-remove-unknown` to the cleanup so kernel-loaded orphan profiles get unloaded too. Also amended parked `jabali-kratos.disabled` profile to capture the QA-team's manual fix (kratos-identity-schema.json + /run/jabali-kratos/* paths) for future M40.1 work. |

| M37: PostgreSQL parity (phase 1) SHIPPED | 2026-05-09 (formal close) | ADR-0091; blueprint at `plans/m37-postgresql-parity.md`; runbook at `plans/m37-postgresql-parity-runbook.md`. Wave A: engine column on Database Users + Quick Database Setup engine picker + Adminer SSO (mariadb + postgres) + EngineTag with brand colors. Wave B: backup integration via `pg_dump -Fc` per-database stage in `backup_databases.go` + restore via `pg_restore --clean --if-exists --no-owner` in `backup_restore.go`. Wave C: per-user PG roles + grants engine dispatch (`db.postgres.create_role` + `db.postgres.grant`); event source `postgres.go` polls service_down / disk_high / connections_exhausted at 2-min tick; `service_list.go` includes `postgresql` in BaseAllowedServices for Server Status visibility; install.sh `install_postgres()` brings up the cluster gated by `server_settings.postgres_enabled`. Wave D: Adminer pg via TCP loopback (not unix-socket peer; M25 socket-mode wouldn't work for php-fpm Adminer pool); php-pgsql + adminer included in install. **Default-off** until operator flips `server_settings.postgres_enabled=true`. **Acknowledged limitations** (per ADR-0091 + runbook §7): no PG replication; no per-table grants; pgAdmin4 not shipped (phpPgAdmin via Adminer SSO is the UI); no PostGIS auto-install; no MariaDB→PG migration tooling (operator-driven via mysqldump --compatible=postgres). |
| M35: Migration importers (Wave A complete + Wave B/C scaffolds) | 2026-05-09 | ADR-0094 ACCEPTED; blueprint at `plans/m35-migration-importers.md`; runbook deferred to Step 9. Schema migrations 000120-122 (`migration_jobs` UNIQUE on (source_host, source_user, source_kind), `migration_stages` FK CASCADE for resume, `server_settings.migrations_{enabled,concurrent_per_source,imap_concurrent_folders}` default off). install.sh provisions `/var/lib/jabali-migrations` 0750 root:jabali. Wire surface at `panel-api/internal/migrate/`: `discover.go` Discoverer interface (Connect/ListAccounts/DescribeAccount/Close, SecretRef path→env-file with SSH_PASSWORD or SSH_PRIVATE_KEY zero'd post-auth); `manifest.go` AccountManifest SchemaVersion=1 covering Domains/Mailboxes/Databases/DNSZones/Cron/SSH/Apps/Warnings; `stage.go` analyze/fix_perms/validate/restore + jobTransitions enforcing legal next-states; `validate.go` 3 conflict kinds (target_user_exists, domain_taken, username_invalid) + projections; `registry.go` per-source factory map (panic on duplicate registration). cPanel importer at `panel-api/internal/migrate/cpanel/`: SSH+UAPI Discoverer w/ Principal probe (Admin via whmapi1 / User via UAPI), 5 area builders (DomainInfo::list_domains / Email::list_pops / Mysql::list_databases [+ Postgresql probe → postgres_unsupported warnings per ADR-0094 §5] / Cron::list_cron / SSH::listkeys), Pkgacct (90-min timeout) + PullFile (SSH cat-stream, no sftp dep), ParseTarball (path-escape + 100GiB-per-entry size cap + symlink/device skip), ImportSSHKeys (fingerprint dedup, sshkeys.ParseAndFingerprint reuse), ImportCron (cronvalidate-gated, Enabled=false safety so reconciler never schedules until operator reviews), ImportDatabases (idempotent collision-skip, db.create + db.restore + drop-on-fail cleanup). **Wave A SHIPPED end-to-end** for cPanel + WHM-pkgacct: 6/6 area writers (ImportSSHKeys + ImportCron + ImportDatabases + ImportDNS BIND parser + ImportHome rsync agent cmd + ImportMailboxes JMAP push via Email/import in `a5d2dc25`); Restore-stage Runner with IsValidJobTransition gate; `jabali migrate import` CLI with auto-create-user via internal/userops; admin REST list/get/stages/cancel/create + admin SPA list page + per-job detail page (Steps timeline + manifest panel) + new-migration drawer; `/etc/jabali-panel/migration-secrets/` provisioned + analyze stage callback wired. **Wave B Step 5 (Hestia)** + **Wave A Step 4 (DA)** Discoverer scaffolds shipped — Connect + ListAccounts + DescribeAccount-with-warning; per-area builders pending live source fixtures. **Wave C Step 7 (IMAP-only)** stub Discoverer registers source-kind + errors with operator-facing imapsync direction; agent.command `migration.imapsync` deferred to follow-up. Default-off until `server_settings.migrations_enabled` flips. |
| M41: Operator CLI extensions (`jabali db/cron/ssh-key`) | 2026-05-09 (refactor closed `0a8fd3c3`) | Closes the bootstrap-script gap — `~/jabali-test-bootstrap.sh` no longer ends with a "TODO via UI (no CLI yet)" list. `jabali ssh-key` (list/add/delete) shipped earlier; `jabali cron` (list/add/update/delete/run-now) shipped earlier; `jabali db` (list/create/delete) shipped today (`02142581`). Validation logic mirrored from REST handlers verbatim — name regex `^[a-z][a-z0-9_]{0,30}$`, package quota check via MaxDatabases, ExistsByUserAndName collision on prefixed name, engine gate (mariadb default; postgres requires `server_settings.postgres_enabled=true`), agent dispatch (`db.create`/`db.drop` for mariadb, `db.postgres.create_db`/`db.postgres.drop_db` for postgres). ~~**Drift risk:** REST + CLI logic duplicated~~ **CLOSED 2026-05-09 (`0a8fd3c3`):** `panel-api/internal/dbops/` extracted (Create + Delete + Deps wiring + typed sentinel errors). REST handler at `internal/api/databases.go` + cobra cmd at `cmd/server/db_cmd.go` are now thin wrappers — both call `dbops.Create` / `dbops.Delete`. Pipeline preserved exactly: validate → load user → prefix → quota → collision → engine gate → agent dispatch → row insert. Errors are typed `errors.Is` sentinels (`ErrUserNotFound`, `ErrPostgresOff`, `ErrNameTaken`, etc.); REST translates to HTTP status, CLI translates to user-readable strings. ADR-0083 still target — write once `internal/cronops/` + `internal/sshkeyops/` follow the same shape. |
| M36: Per-domain IP allow/deny ACLs | 2026-05-09 | Migration 000119 (`domain_ip_acls`: ULID PK, `domain_id` FK CASCADE, `cidr` VARCHAR(64), `action` ENUM-via-VARCHAR `allow|deny`, `priority` INT default 0, `comment` VARCHAR(200), idx on `(domain_id, priority)`). Repository orders by `priority ASC, created_at ASC`. REST `/api/v1/domains/:id/acls` (list / create / delete) — admin reads+writes any, users only own; cross-tenant returns 404 (parity with domain 404). Server-side validation via `net.ParseCIDR`; bare IP auto-suffixed `/32` or `/128`; action whitelist `{allow, deny}`. Reconciler `WithDomainIPACLs` setter populates an optional `ip_acls: [{cidr, action}]` array on `domain.create` agent dispatch (3s timeout, omitted when zero rules). panel-agent `domain_create.go` extends `domainCreateParams` + `vhostData` with `IPACLDirectives string`; `buildIPACLDirectives` writes a "M36 per-domain ACLs (priority order)" comment block followed by `    {action} {cidr};` lines into the nginx server block between `{{.IndexDirective}}` and `location /`. Defensive `validACLCIDR` whitelist gates digit/dot/colon/hex/slash chars only — invalid rules are silently dropped at template time (already validated at REST). UI: `IP Allow / Deny` section on admin Domain Edit between Listen IPs and Email — Table (priority, action tag green/red, monospace CIDR, comment, delete popconfirm) + inline add Form (CIDR, action select, priority InputNumber, comment) + alert explaining nginx top-down evaluation + final-`0.0.0.0/0` `deny` allowlist switch pattern. Hooks at `panel-ui/src/hooks/useDomainIPACL.ts` (useDomainIPACLs / useCreateDomainIPACL / useDeleteDomainIPACL) with per-domain queryKey invalidation. User-shell ACL UI deferred until user-side Domain Edit page exists. |
| M44 + M13.1 + M40 cleanup follow-ups | 2026-05-09 | Smaller deltas not big enough for their own row: `agent.fs.stat` (privileged path-only stat — wires WP probe + WP health endpoint, replaces the always-false stub); `humanBytes` dedupe (3 local copies → src/utils/bytes); WordPress probe sort+filter moved to repo (`ListReadyByUpdatedAtAsc`); WS log-stream same-origin enforcement (closes the `CheckOrigin: return true` TODO). Plus the QA round 3 mock-Kratos-settings fixture so MyProfile spec doesn't race the auto-redirect. |
| M46: Database server admin ops (Server Settings ▸ Databases tab) | 2026-05-16 | ADRs 0097-0100; blueprint `plans/m46-database-server-admin-ops.md`; runbook `plans/m46-database-server-admin-ops-runbook.md`; branch `m46/database-server-admin-ops`. Six cPanel/WHM-parity ops, both engines, in the existing Databases tab. (1) Root/superuser password ALONGSIDE socket/peer auth (ADR-0097, Option A) — MariaDB `IDENTIFIED VIA unix_socket OR mysql_native_password` (Context7-verified, socket-first) + SHOW-CREATE-USER/socket-probe guards + revert-on-fail so install.sh:1660 invariant holds; pg `ALTER ROLE postgres` peer untouched; reveal-once; secret files root:jabali 0640 tmp+rename. (2) Per-DB-user pw already shipped — informational note only. (3) Curated reconciler-converged config tuner (ADR-0098) — `internal/dbtuning` allowlist (8 maria + 7 pg keys, kind/range validated, NO raw editor), maria managed drop-in validate→backup→swap→probe→rollback→B7 unrecoverable marker+critical M14, pg ALTER SYSTEM; DB source of truth, pending-gated converger (no pg restart storm). (4) Admin all-DBs SSO (ADR-0099) — agent `db.pma_admin.ensure` (jabali_pma_admin ALL PRIVILEGES; honest root-equivalent), `POST /admin/databases/sso/{phpmyadmin,adminer}` RequireAdmin+same-origin+single-use token+scope=admin audit; validate seam is an early `__M46_ADMIN_ALL__` sentinel branch BEFORE the per-user shadow load so per-user SSO is byte-unchanged. (5) Maintenance (ADR-0100) — InnoDB-honest (NOT "repair"): mysqlcheck --optimize --analyze / vacuumdb+reindexdb; db_admin_jobs durable + 409 one-per-engine + detached run. (6) Processes — information_schema.PROCESSLIST/pg_stat_activity + KILL/pg_terminate_backend, list envelope, audited (no M14, noisy). Migration 000135 (db_tuning_settings/db_admin_jobs/db_admin_audit, schema-only). internal/dbtuning + agent-cmd arg-validation tests green under -race. **Live-VM smoke pending** on 192.168.100.150: root-pw socket-survival, config rollback, admin SSO both engines + per-user-SSO regression check, maintenance, kill. |
| M49: Unified audit log (admin + per-user activity) | 2026-05-18 | ADR-0106; blueprint `plans/m49-unified-audit-log.md`; runbook `docs/runbooks/m49-audit-log.md`. **Backend SHIPPED + merged to gitea main** (PRs #20/#22/#23/#24/#25, each loop-verified then Gitea-CI-gated). Append-only hash-chained `audit_events` (migration 000138; ULID id, ts, nullable actor/subject, action, target, result, source_ip, request_id, prev/row sha256, meta JSON; no `updated_at` — append-only). Dedicated `jabali:audit:queue` Redis stream + single-writer chain `Consumer` (NOT the M14 `Envelope` — design corrected 2026-05-17: notification-shaped, documented breaking to extend); Redis-down → buffered DB fallback (fail-open-but-recorded; consumer back-fills hashes on recovery); M14 reserved for the alert subset only. `Recorder` fire-and-forget (M44 discipline). `middleware.AuditRecord` records every mutating `/api/v1` request (method+route-template, actor, result, ip, req-id — **never the body**), which already covers security toggles generically. Typed emitters: automation token mint/revoke (M44) + M46 db-admin fold-in (dual-write; `db_admin_audit` kept a one-release alias-view per ADR-0106, M50 drops it; migration 000138 backfilled historical rows). Read API: `GET /api/v1/admin/audit` (RequireAdmin) + `GET /api/v1/me/activity` (subject scope enforced server-side in the repo — IDOR-safe). `jabali audit query|verify|prune`: `verify` replays the chain (non-zero exit on tamper, for a timer/monitor), `prune` is a bounded retention DELETE that records its own `audit.retention.prune` event (never a silent selective delete). **ADR-0105/migration-000137 collision with the team's parallel M32.x reconciled** via renumber (audit → 0106/000138, runbook §"Push to Gitea"). Step 3b-ii (impersonation/break-glass typed emitters) moot — no such implementation exists in this codebase line; the constructors are reserved. **Pending: Step 6** — admin "Audit Log" + user "Account Activity" SPA pages (panel-ui); flagged Gitea-CI-verified-only (can't be loop-pre-verified). |
| M50: Directory Privacy (per-subdirectory HTTP Basic Auth) | 2026-05-30 | ADR-0125; migration 000148. cPanel-style per-directory password protection. Two tables (`domain_directory_privacy` rules keyed `(domain_id, path)` + `domain_directory_privacy_credentials`, both FK-cascade off `domains`); bcrypt-hashed at the API boundary (reveal-once, never read back); agent renders one htpasswd per rule at `/etc/jabali-panel/dir-privacy/<rule_ulid>.htpasswd` (0640 root:www-data, outside docroot) and the vhost template emits `location ^~ <path>/ { auth_basic ...; }` per rule (reconciler-injected via `WithDomainDirectoryPrivacy`, orphan-pruned). **Fail-closed:** a zero-credential rule writes a deny-all placeholder so the location 401s rather than opening. Routes `/api/v1/domains/:id/directory-privacy/...` (domain-owner + admin); `path == /` protects the whole docroot (PR #114). UI: domain edit **Security** tab. Shipped 2026-05-30 (PRs #107/#114). |
| M51: Mailbox User Groups + Shared Resources | 2026-06-18 | ADR-0132; blueprint `plans/m51-mailbox-groups.md`. Distribution-list / shared-mailbox groups on mail domains: `mail_groups` + members; group *receive* via Stalwart `queryRecipient` member-expansion (Stalwart ro-user granted SELECT on `mail_groups` as a directory recipient source); admin **Groups** tab + group-select in the mailbox-create wizard. Groups e2e spec + ADR-0132 accepted. |
| M52: Mail Shared Resources & Groups redesign (Wave 3) | 2026-06-20 | ADR-0133; blueprint `plans/m52-mail-shared-resources.md`. `shared_resources` schema + `calendar/addressbook/file.share_set` agent commands under a **one-principal-per-resource** model (Stalwart directory, Context7-verified); `sharedresource.apply`/`destroy` + reconciler + REST + CLI + Shared Resources UI (dropped the dead group toggles, #236/#242); gap-closes: durable host GC, rename propagation, file node id. |
| M53: Updates Center (Updates page redesign) + Repair Center | 2026-06-09 (Repair Center follow-ups through 2026-07-03) | blueprint `plans/m53-updates-center.md`. Updates page redesign (drop the changelog, show the last 3 panel commits, #317); **Repair Center** GUI for `jabali repair` on the Updates page; prominent safety warning + recovery tips (GH #447); Repair + Domain-repair cards (GH #672). |
| M54: Username-only login (hard cutover) | 2026-06-10 | ADR-0119; blueprint `plans/m54-username-login.md`; runbook `plans/m54-username-login-runbook.md`; PR #322 (`m54/cutover`). Atomic cutover to **username** login: Kratos canonical v2 schema + `UpdateUsernameTrait` + `relabel-identifiers` CLI; admin password-reset endpoint **replaces** self-service recovery; bootstrap username + rebuild temp-password; `jabali user create --username` (email now optional); username-login UI. |
| M55: Python framework marketplace (JAB-164) | 2026-07-19 | ADR-0131 addendum; migration 000229 (`python_apps.framework` + `scaffolded_at`). One-click framework installs on the ADR-0131 runtime: `install/py-frameworks/<slug>/framework.yaml` (+ `template/` or `patch.py`) catalog (Flask, FastAPI, Django, Starlette, Litestar, Quart, Dash, Bottle, Django Channels, Wagtail); loaded by `pyframeworks.LoadDir`. CLI `python-app create --framework` + `python-app frameworks`; `POST /python-apps {framework}` + `GET /python-apps/frameworks`. Reconciler runs a one-shot `app.python.scaffold` before first apply: venv + pinned deps + starter (`django-admin`/`wagtail start` or template files) + settings hardening (generic `config/settings.py` patch, or per-entry `patch.py` for Wagtail's settings package + Channels' `ProtocolTypeRouter`) + `migrate`/`collectstatic`, all AS THE TENANT; `SECRET_KEY` minted with stdlib `secrets` to the app env, never source; scaffold writes via `sudo -u <tenant> tee` (symlink-TOCTOU-safe). Django static served by an admin-only nginx `static_alias` rule (alias constrained under `/home/`). Marketplace **Catalog** tab on the Python Apps page + vendored brand icons. Box-verified create->serve->delete for every framework on testserver + .86 (WSGI at sub-paths; ASGI clean at root, `--root-path` caveat at sub-paths). PRs #509/#510/#511/#512/#513 + `feat/jab164-wagtail-channels`. |

---

**Last updated:** 2026-07-19 (M51–M54 added — JAB-162)
