# Server Settings

`/jabali-admin/settings`. Global settings that apply to the whole installation, organized into sections.

## General

- **Panel hostname** — the FQDN the panel and SPA serve on. Changing it triggers reissuance of the [Panel Certificate](./panel-certificate.md).
- **Primary mail domain** — the FQDN Stalwart presents in `HELO`, the `Message-ID` host, and the default `From:` for system mail.
- **Default IP pool** — the IP returned by zone creation when no per-domain override is set.
- **Default package** — the package assigned to new users if the form is left blank.
- **Locale** — default UI locale for new users; users may override per-account.
- **Tenant domain options** — off by default. When on, non-admin domain owners get two extra tabs on their own domains: *Domain options* (a curated, safe set of nginx options — max upload size, HSTS, security headers, gzip — each rendered as a fixed, vetted directive) and *Rewrite rules* (structured `rewrite` and `custom_header` rules, each validated; a `rewrite` target is forced to a local path so it can't become an open redirect or proxy). Raw nginx directives, `proxy_pass`, IP access, and PHP settings stay admin-only regardless. Surfaced to tenants on the [Domains](../user/domains.md#nginx-options-and-rewrite-rules) page.
- **Tenant document-root editing** — on by default. Lets non-admin owners repoint a domain's document root to a folder inside that domain's own tree (e.g. a framework's `public/` subdir); confined server-side so it can't escape the domain.

## Database

- **Root password** — rotate the MariaDB root password ([M46](../databases.md#admin-db-ops-m46)). Stored encrypted in `db_admin_secrets`.
- **Curated tunables** — apply changes to the whitelisted set of MariaDB / PostgreSQL config keys. Reconciler-converged through `db_tuning_reconcile.go`.
- **Maintenance** — schedule `OPTIMIZE` / `ANALYZE` / `CHECK` / `REPAIR` against selected databases.
- **Processlist + Kill** — live processlist with one-click `KILL` against a query.

See [Database Tuning](./database-tuning.md) for the per-key reference.

## Nginx

Server-wide nginx tunables (M55). Applied at `http{}` scope so they default for every site; individual vhosts may still override per-directive. Every change is validated with `nginx -t` and rolled back automatically if the test fails.

- **Max upload size** (`client_max_body_size`) — largest request body nginx accepts before returning 413. Default `50m`. Raise it for big-upload apps (ownCloud, Nextcloud); this is also what lets reverse-proxied apps accept uploads.
- **Show nginx version** (`server_tokens`) — off by default (hides the version from responses and error pages).
- **Gzip compression** — on by default.
- **Keepalive timeout**, **client body / header timeouts**, **send timeout** — slow-client tuning.
- **Reverse-proxy timeouts** (`proxy_connect_timeout` / `proxy_read_timeout` / `proxy_send_timeout`) — defaults applied to every proxied app. Default `300s`.
- **Worker processes / connections** — `worker_processes` and `worker_connections`, patched into `nginx.conf`.
- **Advanced** — a free-form box for raw `http{}`-scope directives. Gated by `nginx -t` only; a syntactically valid but wrong directive can still destabilize the server, so it carries a red warning. Leave empty unless you know exactly what you are adding.

Tunables persist to `server_settings` and are applied by the agent's `nginx.tunables.apply` verb: additive directives render into `/etc/nginx/conf.d/05-jabali-tunables.conf`, while the worker knobs and the directives nginx already ships in `nginx.conf` (`server_tokens`, `gzip`, `keepalive_timeout`) are patched in place.

## Mail

- **Recovery sender** — the `From:` for Kratos password-recovery email.
- **Outbound throttles** — defaults (see [Mail Throttles](./mail-throttles.md)).
- **MTA-STS policy mode** — `enforce` or `testing`.
- **Stalwart expression filters** — admin-defined routing / drop / quarantine expressions (M47 Wave 3v2).

## Security

- **CrowdSec console enrolment** — enrol once to sync decisions, alerts, and allowlists with the central console.
- **AppSec policy** — log only / block (default).
- **Default per-user egress policy** — `default-restricted` / `unrestricted`.
- **AIDE schedule** — daily / weekly / off.
- **Snuffleupagus rule pack** — enabled / disabled.

## Notifications

- **VAPID keys** — Web Push public/private. Auto-generated on first install; rotation here invalidates all current Web Push subscriptions.
- **Default channel routing** — server-wide defaults; per-event overrides live on [Notifications Routing](./notifications-routing.md).

## Updates

- **Update source** — `origin/main` (default) or a pinned branch.
- **Update window** — optional cron expression; outside the window, `jabali update --auto` refuses to run.

## Support

- **Recipient public key** — overrides the maintainers' public key for the encrypted diag bundle.
- **Recipient email** — overrides `webmaster@jabali-panel.com`.

## Convergence

Server settings persist to the `server_settings` table. Most changes take effect immediately; the few that require host-side work (panel hostname, certificate, MariaDB tuning) are scheduled with the reconciler and converge within 60 seconds.

## Audit

Every change writes a structured-diff audit row.
