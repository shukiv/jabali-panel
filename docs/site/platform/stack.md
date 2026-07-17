# Platform — Stack

The full inventory of moving parts.

## Process model

| Process | Language | Owner UID | Listens on | Purpose |
|---|---|---|---|---|
| `jabali-panel.service` | Go (Gin) | `jabali` | unix `/run/jabali-panel.sock` | The HTTP panel itself. Behind nginx. |
| `jabali-agent.service` | Go | `root` | unix `/run/jabali-agent.sock` (group `jabali-sockets`) | The only thing that performs privileged host ops. |
| `bulwark.service` | Node (Next.js standalone) | `jabali` | unix `/run/bulwark.sock` | SPA + SSO / magic-link / autoconfig bridge. |
| `kratos.service` | Go (Ory) | `kratos` | unix `/run/kratos-public.sock`, `/run/kratos-admin.sock` | Identity (login, 2FA, recovery). Sockets only. |
| `nginx.service` | C | `www-data` | `:80`, `:443` (+ admin pool IPs) | Reverse-proxy panel + serves every vhost. |
| `php<ver>-fpm.service` | C | `root` master, per-user pool workers | unix `/run/php/jabali-<user>/fpm.sock` | One systemd unit per PHP version; one pool socket per panel user. |
| `mariadb.service` | C | `mysql` | unix `/run/mysqld/mysqld.sock` (skip-networking on) | Both panel DB and tenant DBs. |
| `postgresql.service` | C | `postgres` | local socket | Tenant DBs only. |
| `pdns.service` | C++ | `pdns` | public IPs `:53` | Authoritative DNS for hosted zones, MariaDB backend. |
| `pdns-recursor.service` | C++ | `pdns-recursor` | `127.0.0.1:53` | Local recursive resolver. |
| `stalwart-mail.service` | Rust | `stalwart` | `:25`, `:465`, `:587`, `:993`, `:995`, admin HTTP `127.0.0.1:8080` | SMTP MTA + submission + IMAP + JMAP + mailbox store. |
| `redis.service` | C | `redis` | unix `/run/redis/redis.sock` | Notifications dispatcher stream, panel cache. |
| `crowdsec.service` + bouncers | Go | `crowdsec` | unix sockets | IP-trust source + AppSec WAF. |
| `tetragon.service` | Go + eBPF | `root` | unix socket | Kernel-level tripwires for malware detection. |
| `aide.timer` | shell | `root` | — | Daily host-integrity scan. |

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
domain.create       { user_id, domain, has_php, php_pool_id, cache_enabled, listen_ip, … }
ssl.issue           { domain }
ssl.panel.issue     {}
mail.mailbox.create { domain, local_part, password_hash, quota_mib }
db.config.apply     { engine, keyspace }
nginx.reload        {}
nginx.cache.purge   { domain }
pdns.dnssec.enable  { domain }
backup.schedule.run { schedule_id }
```

Wire-contract drift is caught by golden tests (mirror `security_crowdsec_geoblock_golden_test.go`) — JSON tags on `domainCreateParams` must match the panel side. The "verify wire contract against handler" rule is mandatory whenever a new field is added.

## Why this shape

- **DB-as-truth** prevents host-edit drift; restart-safe.
- **Reconciler-converged** means anything you change in the DB shows up on host within ≤60 s; restart of the agent doesn't lose state.
- **Single agent over UDS** keeps privilege boundary one process; no setuid binaries, no sudoers.d expansion.
- **Sockets only for everything that can be a socket** (Kratos public/admin, MariaDB, Stalwart admin) removes TCP attack surface.

ADRs covering the major decisions live under [docs/adr/](https://github.com/shukiv/jabali-panel/tree/main/docs/adr).
