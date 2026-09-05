# Domains

A **Domain** is a hosted vhost owned by exactly one user. Each domain row in the panel DB drives a real nginx vhost — the reconciler converges them on every tick.

## Per-domain settings

Edit a domain via `/jabali-admin/domains/edit/:id` (admin) or `/jabali-panel/domains/edit/:id` (owner).

| Setting | What it does |
|---|---|
| **Domain name** | The vhost server_name (set at create time; rename = delete + recreate). |
| **PHP version** | Which Sury PHP-FPM pool the vhost passes `.php` to. Selected from the versions enabled on the host. |
| **PHP settings** | Per-user (not per-domain) overrides; see PHP Settings. |
| **SSL** | Enable Let's Encrypt for this domain (HTTP-01). Reconciler issues within ≤60 s and reloads nginx. |
| **DNSSEC** | Enable DNSSEC signing for the zone. Generates KSK + ZSK; displays the DS record to publish at the parent registrar. |
| **Listen IP** | Pick from the admin's managed IP pool. Default: server's primary IP. Apex DNS records auto-update. |
| **Redirects** | HTTP → HTTPS (always emitted when SSL is on); per-path redirects (planned). |
| **Aliases** | Additional `server_name` values that resolve to the same vhost + same docroot. |
| **Cache** | Per-domain opt-in FastCGI micro-cache (ADR-0108), with a safe-bypass for cart / admin / authenticated cookies and a manual purge. Default off. |
| **Document root** | Set the docroot when adding the domain, confined to the owner's home tree (GH #1413). |
| **Environment variables** | Per-domain env vars passed to the FPM pool (GH #1332). |
| **Web / Mail / DNS** | Each service is an **independent per-domain flag** (GH #1449) — a domain can run any combination. Turning **Mail** on adds DKIM keys + MX hint + Stalwart Domain entry; **DNS** on manages the zone in PowerDNS; **Web** on builds the nginx vhost. |
| **Reverse proxy** | Instead of serving files, proxy the domain to a local app on a chosen loopback port. Tenants pick the port through an SSRF-safe validator with an agent-side bind check (GH #1175, #1401); ports come from a shared loopback-port allocator so two tenants can't collide. |

## Domain lifecycle

```
create  → DB row inserted → reconciler builds vhost, requests cert if SSL=on
suspend → DB flag set       → reconciler returns 503 page
delete  → DB row removed    → reconciler tears down vhost, revokes cert, drops Stalwart domain
```

The reconciler is the only thing that re-applies vhosts; never edit nginx site files by hand — they'll be overwritten on the next tick.

**Delete also removes files (opt-in).** Domain delete keeps the document root by
default; tick **also delete files** (default off, GH #1382) to remove the docroot
tree at the same time.

**Change owner.** An admin can reassign a domain to a different user (GH #1238);
the vhost, DNS, and mail follow the new owner on the next reconcile.

## What lives outside the vhost

- **DNS records** are managed under DNS (PowerDNS auth backend, MariaDB-backed).
- **Mailboxes** for this domain live under Mail.
- **Databases** belong to the user, not the domain.

## CLI

```bash
jabali domain list                 # all domains
jabali domain create <name> --user <id> --web-enabled --manage-dns --mail
jabali domain enable <name|id>
jabali domain disable <name|id>
jabali domain delete <name|id>
```

`jabali domain create` takes `--web-enabled`, `--manage-dns`, and `--mail` to set
the independent Web / Mail / DNS service flags at create time (GH #1449).

See [platform/cli-reference.md](./platform/cli-reference.md) for the full,
generated flag reference.
