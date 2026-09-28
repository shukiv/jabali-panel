# CLI

The `jabali` binary is one Cobra root command with subcommands. Run it as root on the panel host. Most commands read the panel database and call the agent over `/run/jabali/agent.sock`.

This page groups the commands by area and shows the common tasks. The [CLI reference](./cli-reference.md) lists every command and every flag. It is generated from the command tree, and CI fails when it drifts. `jabali <command> --help` gives the same detail on a box.

## Global flags

- `--json`: print JSON instead of text, where the command supports it.
- `--config <path>`: the config file (default `/etc/jabali/config.toml`).

## Command groups

### Panel and updates

| Command | What it does |
|---|---|
| `jabali serve` | Start the Jabali Panel HTTP(S) server |
| `jabali update` | Pull latest code, rebuild, migrate, and restart services |
| `jabali repair` | Detect and fix known deployment-host issues |
| `jabali version` | Print Jabali version, commit, and runtime info |
| `jabali release` | Release-channel management (stable/development) |
| `jabali migrate` | Database migration commands |
| `jabali retention-sweep` | Prune expired rows from unbounded log/report tables |
| `jabali active-tasks` | Show running/queued tasks (updates, backups, malware scans) |
| `jabali secrets` | Rotate panel secrets after remediation (JAB-357; operator ceremony, run as root) |
| `jabali settings` | Inspect and patch server settings (headless equivalent of /admin/settings) |

### Users and access

| Command | What it does |
|---|---|
| `jabali user` | Manage panel users |
| `jabali user-token` | Manage a user's API tokens (mint / list / revoke) |
| `jabali session` | Audit and revoke active login sessions (Kratos) |
| `jabali impersonation` | Manage admin act-as impersonation grants (list / create / end) |
| `jabali automation-token` | Manage Automation API tokens (headless provisioning) |
| `jabali sso` | SSO (Single Sign-On) management commands |
| `jabali sso-reap` | Sweep stranded jabali-sso-<nonce>.php files (M22 reaper) |
| `jabali admin` | Operator-only administrative subcommands |

### Domains and web

| Command | What it does |
|---|---|
| `jabali domain` | Manage hosted domains |
| `jabali ssl` | Manage Let's Encrypt SSL certificates |
| `jabali panel-cert` | Manage the panel's own TLS certificates (hostname + mail) |
| `jabali page-template` | Manage error/index page templates |
| `jabali php` | PHP version + extension + per-user pool management |
| `jabali php-defense` | PHP-defense (Snuffleupagus) status / mode / rules / incidents |
| `jabali package` | Manage hosting packages |
| `jabali limits` | Per-user resource limits (cgroups v2 + POSIX quota + nginx) |
| `jabali files` | Scoped tenant file manager (list/read/mkdir/move/chmod/archive/…) — same policy as the GUI |
| `jabali disk-usage` | Inspect / refresh a tenant's disk-usage breakdown (files / email / databases) |
| `jabali log` | Inspect log types and mint/revoke log-stream access grants |

### DNS

| Command | What it does |
|---|---|
| `jabali dns` | DNS zones and records (dns_zones / dns_records): list / add / update / delete |
| `jabali pdns` | PowerDNS helpers (recursor forwarders, etc.) |

### Mail

| Command | What it does |
|---|---|
| `jabali mailbox` | Manage mailboxes (M6 Email via Stalwart) |
| `jabali mail` | Admin mail queue + outbound throttle management |
| `jabali mail-cert` | Manage a domain's mail TLS certificate (mail.<domain> SAN) |
| `jabali mail-group` | Manage mail groups (distribution lists + shared-resource groups) and members |
| `jabali shared-resource` | Manage shared mail resources — calendars, contacts, files (M52) |
| `jabali panel-primary` | Manage the panel's primary mail domain row (ADR-0048) |

### Databases, cron and SSH

| Command | What it does |
|---|---|
| `jabali db` | Manage user databases (mariadb / postgres) |
| `jabali cron` | Manage user cron jobs (systemd-user timers) |
| `jabali ssh-key` | Manage user SSH authorized keys |
| `jabali nspawn` | Manage SSH sandbox nspawn images (M13) |

### Apps

| Command | What it does |
|---|---|
| `jabali app` | Manage one-click app installs (direct DB — M20-safe) |
| `jabali docker` | Manage the docker engine + app-marketplace host (M48/M49) |
| `jabali docker-app` | Manage M48 docker-app catalog installs (admin-only) |
| `jabali python-app` | Manage Python apps (ADR-0131; admin-only) |

### Backups and recovery

| Command | What it does |
|---|---|
| `jabali backup` | Backup & restore subcommands (M30 — restic-backed; ADR-0075 / 0080) |
| `jabali dr` | Disaster-recovery standby: pair, status, promote (GH #331) |

### Security

| Command | What it does |
|---|---|
| `jabali crowdsec` | CrowdSec + AppSec operations (decisions, allowlists, hub, alerts, geoblock, captcha) |
| `jabali appsec` | CrowdSec AppSec config operator subcommands |
| `jabali apparmor` | AppArmor profile management (M40) operator commands |
| `jabali aide` | AIDE file integrity monitor (M42) operator commands |
| `jabali malware` | Malware scan / quarantine / YARA / settings (incident response) |
| `jabali malware-purge` | Hard-delete terminated malware quarantine rows past retention (M33) |
| `jabali ufw` | UFW utilities (M43 — port baseline only; IP decisions live in CrowdSec) |
| `jabali per-user-egress` | Per-user PHP-FPM egress firewall (M34) operator commands |
| `jabali audit` | Query, verify, and prune the unified audit log (M49 / ADR-0106) |

### Server

| Command | What it does |
|---|---|
| `jabali system` | System information and services |
| `jabali service` | Admin service control (start/stop/restart/reload/enable/disable) |
| `jabali ip` | Admin IP address pool (managed_ips): list / add / update / delete |
| `jabali notification` | Inspect notification channels and toggle event notifications |

## Common tasks

### Get back into the panel

The installer prints the admin username and password at the end. If you lost them, set a new password, or print a one-click recovery link:

```bash
jabali user password <email|username|user-id>          # sets and prints a new password
jabali user password <email|username|user-id> --link   # prints a recovery URL (valid 24 h)
jabali user 2fa-reset <email|username|user-id>         # removes TOTP and recovery codes
```

### Users and domains

```bash
jabali user list
jabali user create --username alice --email alice@example.com --password-stdin
jabali user suspend <id> --reason "unpaid invoice"
jabali user unsuspend <id>
jabali domain list
jabali domain create --user alice --name example.com
jabali ssl enable example.com
jabali ssl renew example.com --force
```

### Mail and databases

```bash
jabali mailbox create --domain example.com --local info    # prints the password if it generated one
jabali mailbox passwd info@example.com
jabali domain email-dkim-rotate example.com
jabali db create --user alice --name shop
jabali db user create --user alice --name shop
jabali db root-password                                     # rotates the MariaDB root password
```

### Backups

```bash
jabali backup destination list
jabali backup destination test <id-or-name>                 # creates the restic repo if it is missing
jabali backup schedule list
jabali backup schedule run-now <id>
jabali backup account-restore --user alice --snapshot <id> --destination <name> --force
```

### Updates, repair and support

```bash
jabali update                   # download the release, migrate and restart
jabali repair --diagnose        # list known host problems, change nothing
jabali repair --auto            # fix every non-destructive problem
jabali service status           # the units the panel watches
jabali system diagnostic        # upload an encrypted support bundle; prints a link and a password
jabali audit query --limit 100
jabali audit verify             # check the audit log's hash chain
```

## Exit status

A command exits `0` on success. On any error it prints the error on stderr and exits `1`.
