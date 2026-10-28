# Server Settings

`/jabali-admin/settings`. Global settings that apply to the whole installation, organized into sections.

## General

- **Panel hostname** — the FQDN the panel and SPA serve on. Changing it triggers reissuance of the [Panel Certificate](./panel-certificate.md).
- **Primary mail domain** — the FQDN Stalwart presents in `HELO`, the `Message-ID` host, and the default `From:` for system mail.
- **Default IP pool** — the IP returned by zone creation when no per-domain override is set.
- **Default package** — the package assigned to new users if the form is left blank.
- **Locale** — default UI locale for new users; users may override per-account.
- **Tenant domain options** — off by default. When on, non-admin domain owners get three extra tabs on their own domains: *Domain options* (a curated, safe set of nginx options — max upload size, HSTS, security headers, gzip — each rendered as a fixed, vetted directive), *Rewrite rules* (structured `rewrite` and `custom_header` rules, each validated; a `rewrite` target is forced to a local path so it can't become an open redirect or proxy; plus the file-extension *Deny paths* and *Static cache* rules, and a *Front controller* that only changes which `.php` script in the site's own folder handles requests that match no file), and *Advanced directives* (a raw box confined to `add_header` / `expires` / `etag` only — one statement per line, no blocks or backslashes, panel-managed response headers rejected). `proxy_pass`, `root`/`alias`, `location` blocks, IP access, and PHP settings stay admin-only regardless. Surfaced to tenants on the [Domains](../user/domains.md#nginx-options-and-rewrite-rules) page. A tenant's advanced directives are visible to you (with a **Clear** button) on the domain's **Nginx** tab — disabling this toggle does not deactivate a snippet a tenant already saved, so clear it there if you need to remove one.
- **Tenant document-root editing** — on by default. Lets non-admin owners repoint a domain's document root to a folder inside that domain's own tree (e.g. a framework's `public/` subdir); confined server-side so it can't escape the domain.
- **Support chat (Ask Jabali)** — an AI support chat for the admin panel and the client panels, off by default. See [Support chat](#support-chat-ask-jabali) before you turn it on.

## Server Time

General → **Server Time** sets the clock people see.

- **Timezone** — the server's IANA timezone, for example `Europe/Berlin`. Saving it also sets the operating system's zone (`timedatectl`), so tenant cron jobs and system timers follow it. Empty means the operating system's zone. A zone the server does not have is refused. The list uses the current zone names, for example `Asia/Kolkata` and `Europe/Kyiv`, not the older `Asia/Calcutta` and `Europe/Kiev` that some browsers use, and an older name sent through the API is saved under its current name. The same list is used for `date.timezone` on a domain's PHP Settings page.
- **Date format** — how the panel shows dates and times. The default is `16/10/26 06:36` (`DD/MM/YY HH:mm`). The other choices are `16/10/2026 06:36`, `16.10.2026 06:36`, `2026-10-16 06:36`, `10/16/2026 06:36 AM` and `16 Oct 2026, 06:36`. Logs and the audit log add seconds.

Every time in the panel shows in this zone and format, for administrators and clients alike. Hover a time to see it in UTC. The API, the database and the log files keep UTC (ISO 8601).

Backup schedules run on this clock too: a backup cron of `0 3 * * *` fires at 03:00 in this zone, and the tenant backup window is in this zone. When you change the timezone, the scheduler moves the next run of every cron backup schedule to the new zone within a minute.

Before this release, backup crons and the tenant backup window ran in UTC. After the update they run in the server's timezone, so on a server that is not on UTC each schedule moves by the server's UTC offset.

## Database

- **Root password** — rotate the MariaDB root password (see [Admin DB ops](../databases.md#admin-db-ops)). Stored encrypted in `db_admin_secrets`.
- **Curated tunables** — apply changes to the whitelisted set of MariaDB / PostgreSQL config keys. Reconciler-converged through `db_tuning_reconcile.go`.
- **Maintenance** — schedule `OPTIMIZE` / `ANALYZE` / `CHECK` / `REPAIR` against selected databases.
- **Processlist + Kill** — live processlist with one-click `KILL` against a query.

See [Database Tuning](./database-tuning.md) for the per-key reference.

## Nginx

Server-wide nginx tunables. Applied at `http{}` scope so they default for every site; individual vhosts may still override per-directive. Every change is validated with `nginx -t` and rolled back automatically if the test fails.

- **Max upload size** (`client_max_body_size`) — largest request body nginx accepts before returning 413. Default `512m`, the same as the PHP pool's `post_max_size`. Raise it for big-upload apps (ownCloud, Nextcloud); this is also what lets reverse-proxied apps accept uploads.
- **Show nginx version** (`server_tokens`) — off by default (hides the version from responses and error pages).
- **Gzip compression** — on by default.
- **Keepalive timeout**, **client body / header timeouts**, **send timeout** — slow-client tuning.
- **Reverse-proxy timeouts** (`proxy_connect_timeout` / `proxy_read_timeout` / `proxy_send_timeout`) — defaults applied to every proxied app. Default `300s`.
- **Worker processes / connections** — `worker_processes` and `worker_connections`, patched into `nginx.conf`.
- **Page cache capacity** — the shared FastCGI page cache used by every cache-enabled site: `max_size` (GB, default 4), `keys_zone` (MB, default 64), and `inactive` (minutes, default 1440 = 24 hours, maximum 1440). `inactive` is how long a cached page is kept without being requested. An expired page is still served while it refreshes in the background, and WordPress purges a page when it is edited, so a long window keeps low-traffic pages fast without serving outdated content. After the Jabali Cache plugin purges a page, the agent fetches it again at once, so the next visitor gets a cached copy.
- **Allow proxy_pass to local / private addresses** — off by default. A domain's **Proxy Pass** rule (admin-only) normally refuses a target on this server or the private network: `localhost` and `*.localhost`, `127.0.0.0/8`, `::1`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7`. That stops a hijacked admin session from publishing an internal service (MariaDB, Redis, the panel's own APIs) through a public site. Turn it on to proxy a site to an app on this server or the LAN. Unix sockets, link-local addresses (including `169.254.169.254`, cloud metadata) and `0.0.0.0` stay refused either way. So does a numeric host not written as four decimal numbers, such as `127.1`, `2130706433` or `0x7f000001`, which the resolver would read as an address. Saving the switch doesn't reload nginx, and turning it off again doesn't remove rules saved while it was on. For a tenant's own app on this server, a **Reverse proxy** domain (see [Domains](../domains.md)) is the safer route: it gives the tenant a loopback port of their own and refuses the ports system services use. CLI: `jabali settings set nginx_proxy_allow_internal=true`.
- **Advanced** — a free-form box for raw `http{}`-scope directives. Gated by `nginx -t` only; a syntactically valid but wrong directive can still destabilize the server, so it carries a red warning. Leave empty unless you know exactly what you are adding.

Tunables persist to `server_settings` and are applied by the agent's `nginx.tunables.apply` verb: additive directives render into `/etc/nginx/conf.d/05-jabali-tunables.conf`, while the worker knobs are patched in place in `nginx.conf`. `server_tokens`, `gzip` and `keepalive_timeout` are patched in place where `nginx.conf` declares them; where nothing in the `http{}` block declares one (stock Debian and Ubuntu ship `server_tokens` commented out), it goes in the fragment instead. The panel applies the saved values again each time it starts, so these files follow this page even on a server that has never saved it; nothing is rewritten when they already match.

## Mail

- **Mail hostname** — the name mail clients and webmail use, `mail.<panel-hostname>` by default. See [Mail Hostname](./mail-hostname.md).
- **Recovery sender** — the `From:` for Kratos password-recovery email.
- **Spam filter** — the scores at which incoming mail goes to Junk, is rejected or is discarded (defaults 5 / 15 / 20; reject and discard can be off). See [Spam filter](../mail.md#spam-filter).
- **Outbound throttles** — defaults (see [Mail Throttles](./mail-throttles.md)).
- **MTA-STS policy mode** — `enforce` or `testing`.
- **Stalwart expression filters** — admin-defined routing / drop / quarantine expressions.

## Security

- **AppSec policy** — log only / block (default).
- **Default per-user egress policy** — `default-restricted` / `unrestricted`.
- **AIDE schedule** — daily / weekly / off.
- **Snuffleupagus rule pack** — enabled / disabled.

## Notifications

- **VAPID keys** — Web Push public/private. Auto-generated on first install; rotation here invalidates all current Web Push subscriptions.
- **Default channel routing** — server-wide defaults; per-event overrides live on [Notifications Routing](./notifications-routing.md).

## Updates

Panel self-update is not set here. Turn it on under [Server Updates](./server-updates.md) → **Jabali panel self-update**. It is off by default, and runs daily at the time you pick (default 04:30).

## Support

The diagnostic bundle has no settings. See [Support](./support.md).
- **Recipient email** — overrides `webmaster@jabali-panel.com`.

### Support chat (Ask Jabali)

**General** tab. An AI support chat that answers questions about using Jabali Panel. It has two switches, both off by default:

- **Admin panel** — the chat button shows on every admin page.
- **Client panels** — the chat button shows on every client (user) page.

The chat is a [Chatwoot](https://www.chatwoot.com/) widget served from `support.jabali-panel.com`. Before you turn it on, know what that means:

- A script from `support.jabali-panel.com` runs in the panel page. While either switch is on, the panel's `Content-Security-Policy` allows that one origin in `script-src`, `connect-src` and `frame-src`. With both off, the CSP is the same as on a panel without the chat.
- The panel adds one script tag, `https://support.jabali-panel.com/ask_jabali.js`. The widget's code (its settings, the reply sound, the Jabali button, the light/dark theme) lives in that script on `support.jabali-panel.com`. Its operators can change the widget there without a panel update.
- What people type in the chat goes to `support.jabali-panel.com`. The panel doesn't pass account details to the widget. Like any website chat, the widget sees the page address and the browser.

The script loads after the page has finished loading and the browser is idle, so it doesn't slow the panel down. A CSP applies to the page it came with: after you turn the chat on, it shows from the next page load. Turning it off hides it at once.

The CSP lives in `/etc/nginx/snippets/jabali-panel-csp.conf`, which the panel vhost includes. The agent's `nginx.panel_csp.apply` verb rewrites it, checks it with `nginx -t` and reloads nginx; a failed check restores the old file, and a failed turn-on also turns the switches back off. The panel applies the saved switches again each time it starts. Don't edit the snippet by hand: the next save or panel start overwrites it. If the snippet goes missing, `nginx -t` fails; `jabali repair` (check `panel-csp-snippet`) writes the chat-off policy back, never an empty file.

From the shell, as root, set the same two switches with `jabali settings set support_chat_admin=on support_chat_clients=off` (`on`/`off` or `true`/`false`). It updates the CSP the same way and waits for the result; a failed turn-on switches the chat back off and exits with an error.

The admin dashboard shows a banner about the chat until an admin closes it or turns the chat on. Closing it hides it for every admin.

## Convergence

Server settings persist to the `server_settings` table. Most changes take effect immediately; the few that require host-side work (panel hostname, certificate, MariaDB tuning) are scheduled with the reconciler and converge within 60 seconds.

## Audit

Every change writes a structured-diff audit row.
