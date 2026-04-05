# Architecture

Jabali Panel is a Laravel 12 + Filament v5 web hosting control panel with privileged operations delegated to a PHP agent daemon.

## System Overview

```
User Browser
    ↓
nginx (port 80, 443)
    ↓
FrankenPHP Panel (port 8443, PANEL_PORT env configurable)
    ├── Systemd service: TimeoutStopSec=10, KillMode=mixed (graceful shutdown)
    ├── Filament Admin UI (/jabali-admin/)
    ├── Filament User UI (/jabali-panel/)
    └── REST API endpoints
    ↓
AgentClient → Unix Socket (/var/run/jabali-agent.sock)
    ↓
jabali-agent (PHP binary, bin/jabali-agent)
    ├── File system operations (owned by root)
    ├── Service management
    ├── SSL certificate handling
    ├── User/domain operations
    └── System commands (escapeshellarg, proc_open for safety)
```

## Core Components

### 1. Panel (FrankenPHP)

**Process**: Laravel app running on FrankenPHP, listening on port 8443 (configurable via `PANEL_PORT` environment variable). This is independent of nginx, which proxies requests from ports 80/443.

**Guards**: 
- `admin` guard at `/jabali-admin/` for administrative access
- `web` guard at `/jabali-panel/` for user access
- Both guards share the same `users` provider

**Entry Points**:
- `bootstrap/app.php` — middleware, exception handling, routing configuration (Laravel 12 streamlined structure)
- `routes/web.php` — Filament route registration
- `routes/api.php` — API endpoints for agent communication

**Key Directories**:
- `app/Filament/Admin/Pages/` — admin panel pages (Services, SSL, Users, System, etc.)
- `app/Filament/Jabali/Pages/` — user panel pages (Dashboard, Domains, Backups, Emails, etc.)
- `app/Services/Agent/AgentClient` — all privileged operations route through this service

### 2. Agent (Privileged Daemon)

**Binary**: `bin/jabali-agent` (PHP standalone executable, ~21k lines)

**Communication**: Unix socket at `/var/run/jabali-agent.sock`. Panel communicates via `AgentClient::call()`, passing JSON-encoded command and arguments. Returns JSON response.

**Addons**: Agent loads addon route handlers from `/etc/jabali/agent.d/*.php`. Addons return action→handler arrays for custom operations (e.g., jabali-backup plugin).

**Responsibility**: All operations requiring root privileges:
- File ownership/permissions
- Service start/stop/restart
- SSL certificate issuance and installation
- User home directory creation/deletion
- Cron job management
- Shell command execution with proper escaping (`escapeshellarg()`)
- Password management via `proc_open()` to `chpasswd`

**Safety**:
- All shell arguments escaped with `escapeshellarg()`
- Passwords piped via `proc_open()` (not via command line)
- File paths validated with `PathSanitizer::clean()` to prevent traversal
- Cron commands validated against allowlist via `CronCommandValidator::validate()`
- Service names checked against `$allowedServices` global array

**Response Format**: JSON with `success` boolean, `result` (data), and optional `error` string.

### 3. Services

#### Stalwart Mail Server

**Role**: Only mail backend. Supports SMTP, IMAP, JMAP, ManageSieve.

**Configuration**: MySQL-backed storage for accounts, domains, settings.

**Management**:
- `jabali mail:*` commands (create, delete, password, quota, list, log, queue, queue-retry, queue-delete)
- Mailbox quotas stored in database
- Log viewing via `mail:log` (filters by domain, type like `smtp` or `delivery`)

**Integration**: Panel syncs mailbox operations to Stalwart, reads statistics and logs from database and log files.

#### PowerDNS

**Role**: Authoritative DNS server with REST API and MySQL backend. Supports DNSSEC.

**Configuration**: MySQL stores zones and records. REST API at `http://localhost:8081` (Poweradmin credentials).

**Management**:
- `jabali dns:*` commands (list, records, add, delete-record, sync)
- Zone files synced from PowerDNS API
- Records created/updated/deleted via API
- `dns:sync` refreshes zone from API (useful after bulk imports)

**Integration**: Panel manages DNS records for domains and subdomains, including mail.$domain (MX records).

#### Database Systems

**MariaDB**: Always enabled, supports CRUD operations, user management, privilege assignment, backup (mysqldump), and restore (file upload).

**PostgreSQL**: Optional. Admins enable/disable from Server Settings > Databases tab. Supports service control, database CRUD, role/privilege management (CREATE, CONNECT, TEMPORARY, ALL), backup (pg_dump), restore (file upload), and database info queries. Appears in ServicesTableWidget when enabled.

#### PHP-FPM Configuration

**Pool Management**: Server Settings > PHP-FPM tab provides configurable defaults:
- **Process Manager type**: Dynamic (adjusts based on load), Static (fixed workers), or On Demand (spawns only when needed)
- **Conditional fields**: Dynamic mode shows start/min/max spare server controls; static and on-demand modes hide these
- **Per-pool settings**: Max processes, max requests, memory limit, open files limit, process priority, request timeout
- **Application scope**: Settings apply to new user pools by default, or can be applied to all existing pools via "Apply to All" action

#### Backup System

**Status**: Fully removed in this session. Will be rebuilt as standalone tool (jabali-backup) with addon architecture.

#### Certbot / Let's Encrypt

**Role**: SSL/TLS certificate issuance and renewal using webroot mode.

**Configuration**: Webroot at domain document root, certificates stored in `/etc/letsencrypt/live/{domain}/`.

**Management**:
- `jabali ssl:*` commands (issue, renew, check, list, status, panel, panel-issue)
- Automatic renewal via systemd timer
- Panel certificate separate (for FrankenPHP itself)
- Includes `mail.$domain` as additional SAN

**Integration**: Panel issues certs, checks expiration, renews on demand, auto-deploys to web serving.

#### GoAccess Statistics

**Role**: Real-time web traffic analytics. Daemon mode with WebSocket updates.

**Configuration**: Reads nginx access logs, generates HTML report. WebSocket server for live updates.

**Integration**: Panel displays bandwidth and visitor statistics on user domains page.

### 3b. File Browser

**Role**: Live file browsing and management via Filament page.

**Flexibility**: 
- Subclasses can set `$readOnly = true` to disable write operations
- Per-instance `$disabledFeatures` array allows disabling specific features without affecting global plugin config
- All operations validated and scoped to user ownership

**Integration**: Accessible from user or admin panel based on permissions.

### 4. Webmail (Bulwark)

**Binary**: Next.js JMAP client at `/opt/bulwark`, served via nginx proxy at `/webmail/`.

**Port**: 3000 (internal), proxied by nginx on user-facing port 443.

**Authentication**: SSO via token file at `/var/lib/jabali/sso-tokens/{token}`. Custom endpoint `PUT /webmail/api/auth/session` accepts token + username, returns session cookie.

**Patches**: Applied during install.sh via `patch_bulwark()`:
- basePath configuration (running under `/webmail/`)
- SSO integration (token file reading)
- auth-store modifications (session handling)
- proxy.ts configuration (upstream headers)

**Update**: `upgrade_bulwark()` rebuilds from source, applies patches, validates version marker.

### 5. Security Integration

**Daemon**: `jabali-security` (separate repo, PHP service).

**Features**:
- Brute-force protection (IP-based rate limiting on login)
- WAF (Web Application Firewall)
- Malware scanning (on-demand via `wp:scan` for WordPress)
- CrowdSec integration (optional, for threat intelligence)

**Integration**: Filament plugin provides admin dashboard, WordPress plugin hooks into scan workflow.

### 6. Shell Isolation (jabali-isolator)

**Technology**: systemd-nspawn containers (lightweight namespaced environments).

**Purpose**: SSH shell access sandboxed per user, preventing lateral movement.

**Architecture**:
- One container per user with user-owned filesystem
- SSH login via `jabali-shell` set as user's login shell (via `chsh`), not ForceCommand in sshd_config
- `jabali-shell` uses login shell args and TTY detection for VS Code Remote SSH compatibility
- `jabali-shell` sets C.UTF-8 locale fallback to prevent setlocale warnings and supports container nsenter
- Container idle timeout via systemd timer (`jabali-container-idle-check.sh`)
- Web serving (PHP-FPM) runs on host, not in container

**Integration**: `ssh_shell_enabled` toggle on hosting packages, auto-enabled on user creation.

### 7. Job Queue

**Technology**: Laravel queue system with database driver (default), or Redis for high-volume.

**Jobs**:
- Backup creation (`CreateBackupJob`)
- SSL certificate issuance (`IssueCertificateJob`)
- User deletion (cleanup of domains, emails, databases)
- WordPress operations (update, scan, import)

**Processing**: Supervisor or systemd service runs `queue:work` daemon.

### 8. Directory Layout

```
/home/shuki/projects/jabali/
├── app/
│   ├── Console/Commands/Cli/                  # 86 CLI command classes
│   ├── Filament/Admin/Pages/                  # Admin panel pages
│   ├── Filament/Admin/Resources/              # Admin resources (Users, Domains, etc.)
│   ├── Filament/Jabali/Pages/                 # User panel pages
│   ├── Filament/Jabali/Resources/             # User resources
│   ├── Http/Controllers/                      # API controllers
│   ├── Http/Requests/                         # Form request validation
│   ├── Jobs/                                  # Queue jobs
│   ├── Models/                                # Eloquent models (User, Domain, Backup, etc.)
│   ├── Services/Agent/AgentClient.php         # Agent communication
│   ├── Services/Agent/AgentRequest.php        # Request builder
│   ├── Services/Auth/                         # Authentication services
│   └── Providers/                             # Service providers
├── bin/
│   ├── jabali                                 # CLI entry point
│   └── jabali-agent                           # Privileged agent binary
├── bootstrap/
│   └── app.php                                # Application configuration (Laravel 12)
├── config/
│   ├── app.php, cache.php, database.php, etc. # Configuration files
│   └── services.php                           # Third-party service configs
├── database/
│   ├── migrations/                            # Database schema
│   └── factories/                             # Model factories
├── docs/
│   ├── cli-reference.md
│   ├── architecture.md                        # This file
│   ├── mail.md
│   ├── ssl.md
│   ├── security.md
│   ├── dns.md
│   ├── one-time-login.md
│   └── diagnostic-logs.md
├── resources/
│   ├── views/                                 # Blade templates
│   ├── wordpress/jabali-cache/                # WordPress cache plugin
│   └── css/, js/                              # Frontend assets
├── routes/
│   ├── web.php                                # Filament routes
│   └── api.php                                # API routes
├── storage/
│   ├── logs/                                  # Log files
│   └── uploads/                               # User uploads (symlinked to public/storage)
├── stubs/
│   ├── jabali-shell.sh                        # SSH login shell (login shell via chsh, login args, TTY detection, nsenter, locale)
│   ├── jabali-container-idle-check.sh         # Container idle timeout (systemd timer)
│   └── [config templates]                     # nginx, PHP-FPM, service templates (sshd_config before Match blocks)
├── tests/
│   ├── Feature/                               # Feature tests (Livewire, API)
│   ├── Unit/                                  # Unit tests
│   └── Pest.php (or phpunit.xml)              # Test configuration
├── .env.example                               # Environment template
├── composer.json                              # PHP dependencies
├── package.json                               # NPM dependencies
├── artisan                                    # Laravel CLI
├── install.sh                                 # Server installation script
└── README.md
```

## Data Flow Examples

### User Creation

1. Admin clicks "Create User" in `/jabali-admin/`
2. Filament form posts to `CreateUserAction`
3. Action validates input, creates User model record
4. Agent command dispatched: `createUser(username, password, email, is_admin)`
5. Agent creates Unix user, home directory, SSH key, database user
6. Panel stores user record with agent response data
7. If `ssh_shell_enabled`, container created automatically

### Domain Creation

1. User clicks "Add Domain" in `/jabali-panel/`
2. Filament form posts to domain creation action
3. Panel validates domain ownership (DNS TXT record or email verification)
4. Agent creates vhost directory, symlinks to user home
5. Agent adds DNS zone to PowerDNS
6. Panel creates Domain model, associates with User and SSL certificate request
7. Certbot automatically issues certificate via webroot

### SSL Certificate Issuance

1. Panel detects new domain or expiring certificate
2. Dispatches `IssueCertificateJob`
3. Job calls agent `sslInstallCertificate(domain)`
4. Agent calls Certbot with webroot, stores cert in `/etc/letsencrypt/live/{domain}/`
5. Agent updates nginx vhost config with cert paths
6. Panel stores certificate metadata (expiration, issuer)
7. Auto-renewal via systemd timer

