# Platform — Stack

The full inventory of moving parts.

## Process model

| Process | Language | Runs as | Listens on | Purpose |
|---|---|---|---|---|
| `jabali-panel.service` | Go (Gin) | `jabali` | unix `/run/jabali-panel/api.sock`, `/run/jabali-panel/sso.sock` | The panel API plus the embedded SPA. nginx serves it on `:8443`. |
| `jabali-agent.service` | Go | `root` | unix `/run/jabali/agent.sock` (group `jabali`, 0660), `/run/jabali/agent-pty.sock` | The only thing that performs privileged host ops. |
| `jabali-webmail.service` | Node (Next.js standalone) | `jabali-webmail` | unix `/run/jabali-bulwark/bulwark.sock` | Bulwark webmail, plus the SSO and autoconfig bridge. |
| `jabali-kratos.service` | Go (Ory) | `jabali` | unix `/run/jabali-kratos/public.sock`, `/run/jabali-kratos/admin.sock` | Identity (login, 2FA, recovery). Sockets only. |
| `nginx.service` | C | `root` master, `www-data` workers | `:80`, `:443`, `:8443` (panel) (+ admin pool IPs) | Reverse-proxies the panel and serves every vhost. |
| `jabali-fpm@<user>.service` | C | the panel user | unix `/run/php/jabali-<user>/fpm.sock` | One PHP-FPM master per panel user, on that user's PHP version. The distro `php<ver>-fpm` units are masked. |
| `mariadb.service` | C | `mysql` | unix `/run/mysqld/mysqld.sock`, `127.0.0.1:3306` | Panel DB and tenant DBs. Loopback TCP only because Stalwart's SQL directory cannot use a socket. |
| `postgresql.service` | C | `postgres` | unix `/run/postgresql`, `127.0.0.1:5432` | Tenant DBs only; optional. |
| `pdns.service` | C++ | `pdns` | public IPs `:53`, `127.0.0.1:5300` | Authoritative DNS for hosted zones, MariaDB backend. |
| `pdns-recursor.service` | C++ | `pdns` | `127.0.0.1:53` | Local recursive resolver. |
| `jabali-stalwart.service` | Rust | `jabali-mail` | `:25`, `:465`, `:587`, `:993`, `:995`, `:4190`; JMAP `127.0.0.1:8446`, internal `127.0.0.1:18181` | SMTP, submission, IMAP, POP3, ManageSieve, JMAP and the mailbox store. |
| `jabali-mailhook.service` | Go | `jabali` | `127.0.0.1:8462` | Mail disclaimer hook Stalwart calls (ADR-0143). |
| `jabali-webdav-auth.service` | Go | `root` | unix `/run/jabali-webdav/auth.sock` | nginx `auth_request` check for WebDAV. |
| `redis-server.service` | C | `redis` | unix `/run/redis/redis.sock` | Notifications dispatcher stream, panel cache. |
| `crowdsec.service` + bouncers | Go | `root` | `127.0.0.1:8081` (LAPI), `127.0.0.1:7422` (AppSec), unix `/run/crowdsec/api.sock` | IP-trust source + AppSec WAF. |
| `jabali-aide-check.timer` | shell | `root` | — | Daily host-integrity scan (04:30 UTC). |

## Data model

- **Panel DB** (MariaDB): single DB, ~150 tables. Every domain, user, mailbox, DNS record, audit row, backup job, etc. is here.
- **Stalwart store**: mailbox blobs + metadata; opaque to the panel.
- **Kratos store**: identity DB; opaque to the panel except for the user FK.
- **PowerDNS DB**: zones + records. The agent syncs from the panel DB (DB-as-truth).

## Reconciler

The single in-process loop inside `jabali-panel`. Wakes on:

- A 60 s timer.
- Any panel-side write that schedules itself (`Reconciler.Schedule(<domain-id>)`).

What it does (per tick):

1. Diffs DB intent vs. host state for: domains (vhosts, SSL, DNSSEC, listen IPs), per-user resource limits, mail accounts, DNS zone files, cron timers, backup destinations + schedules, PHP pool files, SSH key files, CrowdSec allowlists, IP pool, panel-cert state.
2. For each diff, calls the agent over UDS to converge.
3. Records the result in the audit log if it was a real change.

Idempotency rule: every converger compares before/after and skips side-effects on no-change. Tracked by the "per-tick idempotent loops" audit checklist.

## Agent contract

`jabali-agent` accepts a small set of typed JSON RPCs over UDS. Each handler is a single Go file under `panel-agent/internal/commands/`. Examples:

```
domain.create       { domain_id, username, domain, doc_root, has_php, php_version, fpm_socket, cache_enabled, … }
ssl.issue           { domain, webroot, email, hostnames, staging }
ssl.panel.issue     { hostname, extra_hostnames, email, staging }
mailbox.create      { id, email, display_name, … }
db.config.apply     { settings, restart_required }
nginx.reload        {}
nginx.cache.purge   { domain, paths }
dns.dnssec_enable   { domain_name }
```

Wire-contract drift is caught by golden tests (mirror `security_crowdsec_geoblock_golden_test.go`) — JSON tags on `domainCreateParams` must match the panel side. The "verify wire contract against handler" rule is mandatory whenever a new field is added.

## Why this shape

- **DB-as-truth** prevents host-edit drift; restart-safe.
- **Reconciler-converged** means anything you change in the DB shows up on host within ≤60 s; restart of the agent doesn't lose state.
- **Single agent over UDS** keeps privilege boundary one process; no setuid binaries, no sudoers.d expansion.
- **Sockets or loopback only for internal services.** Kratos, the panel and the agent listen on unix sockets; MariaDB, Stalwart's JMAP and internal listeners, and the mail hook bind to loopback. Only nginx, Stalwart's mail ports and PowerDNS face the network.

ADRs covering the major decisions live under [docs/adr/](https://github.com/shukiv/jabali-panel/tree/main/docs/adr).
