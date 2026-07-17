# Nginx Layer — Architecture & Operations

> **Status:** Living document. Updated when the nginx layer changes.
> **Audience:** Operators, developers, and QA working on Jabali Panel.
> **Scope:** How nginx is configured, how vhosts are generated, how the
> cache / rate limits / tunables work, and how changes flow from the
> database to disk.

---

## 1. Topology

```
Internet
   │
   ▼ :8443 (TLS)
┌──────────────────────────────────────────────────────┐
│  nginx (www-data)                                     │
│                                                       │
│  /etc/nginx/nginx.conf              — stock Debian +  │
│                                         jabali patches│
│  /etc/nginx/conf.d/                  — http{} scope   │
│    00-jabali-ratelimits.conf         — zone declarations│
│    05-jabali-tunables.conf           — server-wide knobs│
│    jabali-fastcgi-cache.conf         — cache keyzone   │
│    jabali-websocket-map.conf         — upgrade map     │
│    crowdsec_nginx.conf               — AppSec WAF      │
│                                                       │
│  /etc/nginx/sites-enabled/           — per-domain     │
│    <domain>.conf                     — tenant vhost    │
│    <domain>-mail.conf                — webmail vhost   │
│    jabali-panel.conf                 — panel vhost     │
│                                                       │
│  /etc/nginx/jabali/<domain>/         — per-install    │
│    <app>-<subdir>.conf               — rewrite snippet │
│    wordpress-xmlrpc.conf             — xmlrpc deny     │
└──────────────────────────────────────────────────────┘
   │                           │
   ▼ unix:/run/jabali-panel/   ▼ unix:/run/php/jabali-<user>/
   api.sock                     fpm.sock
┌──────────────────┐     ┌──────────────────┐
│  panel-api (Go)  │     │  PHP-FPM (per    │
│  jabali user     │     │  user, cgroup v2)│
└──────────────────┘     └──────────────────┘
```

### Two listener scopes

| Scope | Port | Vhost file | Purpose |
|-------|------|------------|---------|
| Panel | 8443 | `jabali-panel.conf` | API + SPA + phpMyAdmin + Adminer |
| Tenant domains | 80 → 443 | `<domain>.conf` | User websites |
| Webmail | 80 → 443 | `<domain>-mail.conf` | Bulwark + Stalwart JMAP |

The panel vhost is the **only** listener on :8443. Tenant domains
listen on :80 (HTTP→HTTPS redirect) and :443. Webmail vhosts listen
on :80 and :443 with `server_name mail.<domain> autoconfig.<domain> …`.

---

## 2. Vhost generation pipeline

```
Database (domains row)
       │
       ▼  reconciler tick (60s) or API mutation
panel-api internal/reconciler
       │
       ▼  agent.Call("domain.create", {vhostData…})
panel-agent internal/commands/domain_create.go
       │
       ▼  text/template.Execute(vhostTemplate, vhostData)
       │  → write to /etc/nginx/sites-available/<domain>.conf
       │  → symlink sites-enabled/<domain>.conf → sites-available/
       │  → nginx -t
       │  → systemctl reload nginx
       ▼
Live nginx
```

### Content-hash gate

The reconciler calls `writeVhost` on **every** 60s tick for every
domain. Without the content-hash gate this would rewrite + reload
nginx ~180 times/hour. The gate reads the existing file, compares
bytes, and skips the write + reload when identical.

### What goes into a vhost

The `vhostData` struct (`domain_create.go:397-460`) carries every
field the template needs:

| Field | Source | Validation |
|-------|--------|------------|
| `Domain` | DB `domains.name` | `domainRegex` (RFC 1035) |
| `DocRoot` | DB `domains.doc_root` | `filesafe.Scope` (openat2) |
| `FPMSocket` | `/run/php/jabali-<user>/fpm.sock` or per-domain | agent-computed |
| `CustomDirectives` | DB `domains.nginx_custom_directives` | admin-only; allowlist |
| `RuleDirectives` | `nginxrules.Compile(domain.NginxRules)` | typed rules; validated |
| `RedirectDirectives` | `redirects.Compile(domain)` | structured redirects |
| `IPACLDirectives` | `domain_ip_acls` table | `net.ParseCIDR` |
| `DirectoryPrivacyDirectives` | `domain_directory_privacy` table | bcrypt-hashed credentials |
| `RateLimitDirectives` | `BuildRateLimitDirectives(domainID, rps, conn)` | ULID-gated zone names |
| `CacheEnabled` / `CacheGate` / `CacheTTL` | DB `domains.cache_*` | panel-controlled |
| `SSLCertPath` / `SSLKeyPath` | `/etc/letsencrypt/live/<domain>/` or self-signed | agent-computed |
| `ListenIPv4` / `ListenIPv6` | M24 IP manager | `net.ParseIP` |

### HTTP/2 version awareness

The agent probes `nginx -v` on every render and picks the correct
HTTP/2 syntax (`listen … http2` on <1.25.1, `http2 on;` on ≥1.25.1).
See `nginx_http2.go`.

---

## 3. Per-domain vhost template

The template lives at `panel-agent/internal/commands/domain_create.go:132-395`.

### Structure

```
server {                          # :80 — HTTP
    listen 80;
    server_name <domain> www.<domain>;

    # ACME challenge location (^~ — wins over the redirect)
    location ^~ /.well-known/acme-challenge/ { … }

    # HTTP → HTTPS redirect (scoped to location /)
    location / { return 301 https://$host$request_uri; }
}

server {                          # :443 — HTTPS (when SSL is configured)
    listen 443 ssl;
    server_name <domain> www.<domain>;
    ssl_certificate …;
    ssl_certificate_key …;

    root <docroot>;
    index …;

    # IP ACLs (M36)
    # Directory Privacy (M50)

    location / {
        try_files $uri $uri/ /index.php?$query_string;   # PHP
        # or
        try_files $uri $uri/ =404;                        # static
    }

    # Error pages (M28)
    # Static asset caching (if cache_enabled)
    # PHP handler: location = /index.php { … }
    # PHP handler: location ~ \.php$ { … }
    # FastCGI micro-cache gate (if cache_enabled)

    # Per-install rewrites (include /etc/nginx/jabali/<domain>/*.conf)
    # Redirect directives
    # Rule directives (proxy_pass, rewrite, custom_header, ip_access, …)
    # Custom directives (admin-only allowlist)
    # Rate limit directives
}
```

### Disabled domain

When `IsEnabled = false`, the template serves a branded "site
disabled" page from `/var/www/jabali-disabled/` instead of the
tenant's docroot.

---

## 4. FastCGI micro-cache (ADR-0108)

### What it caches

PHP responses from `index.php` and `*.php` for **anonymous** GET
requests. The cache is opt-in per domain (`domains.cache_enabled`).

### Fail-closed gate

The cache gate (`$jabali_skip`) defaults to **skip** (1). It only
allows caching (sets to 0) when:

1. **No cookie** OR the cookie is exclusively known-benign
   analytics/consent tokens (exhaustive allowlist:
   `_ga*`, `_gid`, `_gat*`, `_gcl*`, `__utm*`, `_fbp`, `_fbc`,
   `_pk_*`, `_hj*`, `cookie_consent*`, `wordpress_test_cookie`, …)
2. **Request method is GET** (POST always skips)
3. **Query string is empty or tracking-only** (`utm_*`, `gclid`,
   `fbclid`, … — strict map in `jabali-fastcgi-cache.conf`)
4. **Request URI is not in the bypass list** (`/wp-admin/`,
   `/wp-login`, `/xmlrpc.php`, `/cart`, `/checkout`, …)
5. **Cache gate path matches** (per-install prefix when multiple
   installs share a domain)

Any cookie not in the allowlist → bypass. This is the Gitea #416/#419
fix: the old denylist was fail-open (unknown cookies were cached);
the new allowlist is fail-closed.

### Cache key

```
$scheme$request_method$host$jabali_cache_path
```

Where `$jabali_cache_path` is the **original** request path (before
`try_files` rewrites `$uri` to `/index.php`). This prevents every
WordPress permalink from collapsing onto one cache entry (Codeberg #5).

### Upstream opt-out

`fastcgi_no_cache` also checks `$upstream_http_cache_control` and
`$upstream_http_vary` — if the PHP backend sends
`Cache-Control: no-cache|no-store|private` or a non-Accept-Encoding
`Vary`, the response is not stored (GH #637, #642).

### Purge

| Mode | How | Cost |
|------|-----|------|
| Targeted (paths) | MD5 of cache key → direct file unlink | O(1) per path |
| Full domain | `filepath.WalkDir` over shared cache, match `KEY:` line host field | O(total cache files) |

> **Known issue:** full purge walks ALL tenants' cache files. See
> Plane issue `2ab86c0a`.

### Warmup

After cache enable or purge, the agent fetches the homepage + up to
20 sitemap URLs through the local nginx (`curl --resolve` pins the
host to `127.0.0.1`) to pre-populate the cache.

### Capacity

`nginx.cache.capacity_apply` rewrites `keys_zone`, `max_size`, and
`inactive` in `jabali-fastcgi-cache.conf` with regex replacement +
`nginx -t` gate + rollback.

---

## 5. Rate limits (M18 / M43)

### Zone declarations

A single fragment at `/etc/nginx/conf.d/00-jabali-ratelimits.conf`
declares every `limit_req_zone` and `limit_conn_zone` referenced by
any domain vhost. The `00-` prefix ensures alphabetical load order
(zone must exist before vhost references it).

### Per-vhost directives

`BuildRateLimitDirectives(domainID, rps, conn)` emits:

```nginx
    limit_req zone=rl_<ULID> burst=<rps*2> nodelay;
    limit_conn cn_<ULID> <conn>;
```

Zone names use the domain's ULID (26-char Crockford alphabet) —
validated by `ulidRegex`. No user-controllable data reaches the zone
name.

### Atomic apply

1. Write candidate to `.new`
2. Back up live → `.bak`
3. Rename `.new` → live
4. `nginx -t`
5. On fail: restore `.bak` → live
6. On pass: remove `.bak`

### Security framing (M43 / ADR-0089)

Rate limits are an **anti-noise pre-filter** (scraping resistance,
burst smoothing), **NOT** a security layer. CrowdSec scenarios +
AppSec own behavioural rate / attack detection.

---

## 6. Tunables (M55)

### Two destinations

| Directive type | Where | How |
|----------------|-------|-----|
| `server_tokens`, `gzip`, `keepalive_timeout`, `worker_processes`, `worker_connections` | `/etc/nginx/nginx.conf` | Replace-in-place (first match only) |
| `client_max_body_size`, timeouts, `proxy_*_timeout`, custom HTTP block | `/etc/nginx/conf.d/05-jabali-tunables.conf` | Additive fragment |

### Validation

| Input | Regex |
|-------|-------|
| Size values (`client_max_body_size`) | `^[0-9]+[kKmMgG]?$` |
| Time values (timeouts) | `^[0-9]+(ms\|s\|m\|h)?$` |
| `worker_processes` | `^(auto\|[1-9][0-9]?)$` |
| `worker_connections` | ≤ 1,048,576 |
| `custom_http` | ≤ 4000 chars, no NUL bytes |

The custom HTTP block is **not** directive-validated — it's gated
only by `nginx -t` (the "advanced, can destabilize" contract).

### Atomicity

Both files are staged with backups, swapped in, then a single
`nginx -t` validates. On failure, **both** roll back in reverse
order.

---

## 7. Custom directives & rule builder

### Custom directives (admin-only)

Admins can add raw nginx directives via
`domains.nginx_custom_directives`. The validation at
`validateNginxDirectives` (`domains.go:1244-1308`):

- Rejects NUL bytes
- Splits by newline, strips comments (respecting quoted strings)
- Counts braces (max nesting depth 3, must be balanced)
- Checks the first token of each line against `allowedNginxDirectives`
  (a ~80-entry allowlist)

Non-admins (tenants) **cannot** set custom directives — the PATCH
handler checks `claims.IsAdmin` before accepting the field.

### Rule builder (structured, tenant-safe subset)

The `NginxRules` JSON column holds typed rules:

| Type | Fields | Tenant-safe? |
|------|--------|--------------|
| `custom_header` | name, value, always | Yes |
| `rewrite` | pattern, replacement, flag | Yes (replacement must be local path) |
| `proxy_pass` | path, target, read_timeout, websocket | No (admin-only) |
| `ip_access` | path, mode, IPs | No (admin-only) |
| `php_setting` | name, value | No (admin-only) |
| `max_upload_size` | size | No (admin-only) |

Tenant-safe rules are validated by `validateTenantNginxRules` which:
1. Rejects any type not in `tenantSafeNginxRuleTypes` (rewrite + custom_header only)
2. For rewrites: rejects replacements containing `://` or starting with `//`
3. Falls through to the full `validateNginxRules` for structural checks

### NginxSafeOptions (GH #307)

A curated, structured set of options a tenant may set when the admin
opts in via `server_settings.tenant_domain_options_enabled`:

- `max_body_mb` → `client_max_body_size`
- `hsts` → `Strict-Transport-Security`
- `security_headers` → `X-Frame-Options`, `X-Content-Type-Options`, `Referrer-Policy`
- `gzip` → `gzip on` + `gzip_types`

Every option renders to a **fixed, vetted directive** with no
caller-supplied target, path, or routing.

---

## 8. Panel vhost (`jabali-panel.conf`)

Installed by `install.sh` from `install/nginx/jabali-panel-vhost.conf.tmpl`.

### Key features

- **Upstream:** `unix:/run/jabali-panel/api.sock` (jabali-sockets group)
- **TLS:** `ssl_protocols TLSv1.2 TLSv1.3`, `ssl_ciphers HIGH:!aNULL:!MD5`
- **Security headers:** HSTS, X-Frame-Options DENY, X-Content-Type-Options, CSP, Referrer-Policy
- **CSP:** `default-src 'self'; script-src 'self' 'unsafe-inline'; …` — `unsafe-inline` needed for AntD emotion CSS-in-JS
- **Body size:** 10 GiB ceiling (panel API enforces the precise per-user limit)
- **WebSocket:** `/api/v1/logs/stream/*` — upgrade map + relaxed CSP for GoAccess iframe
- **phpMyAdmin + Adminer:** `include` snippets with `location ^~`
- **Kratos:** `/.ory/*` is proxied by panel-api itself (in-process)

### GoAccess CSP exception

The `/api/v1/logs/stream/*` location overrides CSP to allow
`'unsafe-eval'` (GoAccess uses `new Function(...)` for its template
compiler). Parent block headers are re-declared because nginx
`add_header` semantics don't inherit when a location defines any
`add_header`.

---

## 9. Webmail vhost

Installed per-domain by `webmail_vhost.go` from
`install/nginx/jabali-mail-vhost.conf.tmpl`.

- **Upstream:** `jabali_bulwark` (Unix socket) for the SPA
- **Stalwart JMAP:** proxied to `127.0.0.1:8446` at `/jmap` and `/.well-known/jmap`
- **SSO landing:** `/sso/webmail` proxied to `jabali_panel_api`
- **sub_filter:** rewrites `${PANEL_HOSTNAME}` → `$host` so the SPA stays same-origin
- **No X-Forwarded-Proto:** intentionally omitted (Bulwark's Next.js middleware breaks on it)

---

## 10. nginx -t and reload

Every nginx-mutating agent handler follows the same pattern:

1. Write the new config (vhost file, fragment, snippet)
2. Run `nginx -t`
3. On failure: remove/rollback the file, return error
4. On success: `systemctl reload nginx`
5. On reload failure: return error (config is valid but reload failed)

### Known issue: no serialization

There is no shared mutex across the 6+ handlers that do
write-test-reload. Concurrent operations can interleave. See Plane
issue `0df956af`.

---

## 11. File layout reference

| Path | Owner | Mode | Purpose |
|------|-------|------|---------|
| `/etc/nginx/nginx.conf` | root:root | 0644 | Stock Debian + jabali patches |
| `/etc/nginx/conf.d/00-jabali-ratelimits.conf` | root:root | 0644 | Zone declarations |
| `/etc/nginx/conf.d/05-jabali-tunables.conf` | root:root | 0644 | Server-wide tunables |
| `/etc/nginx/conf.d/jabali-fastcgi-cache.conf` | root:root | 0644 | Cache keyzone + maps |
| `/etc/nginx/conf.d/jabali-websocket-map.conf` | root:root | 0644 | WebSocket upgrade map |
| `/etc/nginx/sites-available/<domain>.conf` | root:root | 0644 | Per-domain vhost |
| `/etc/nginx/sites-enabled/<domain>.conf` | root:root | 0777 (symlink) | → sites-available |
| `/etc/nginx/jabali/<domain>/*.conf` | root:root | 0644 | Per-install rewrite snippets |
| `/var/cache/nginx/jabali/` | www-data:www-data | 0700 | FastCGI cache store |
| `/var/log/nginx/<domain>-access.log` | www-data:adm | 0640 | Per-domain access log |
| `/var/log/nginx/jcache-<domain>.log` | www-data:adm | 0640 | Cache status log |

---

## 12. Related ADRs

| ADR | Topic |
|-----|-------|
| 0009 | Nginx file-per-vhost |
| 0014 | Panel port 8443, user sites on 443 |
| 0046 | Responsive tables (`scroll={{ x: "max-content" }}`) |
| 0050 | Localhost backend via Unix sockets |
| 0077 | HTTP/2 directive version awareness |
| 0089 | Rate limits as anti-noise, not security |
| 0108 | Per-domain FastCGI micro-cache |

---

## 13. Troubleshooting

### `nginx -t` fails after a domain change

```bash
nginx -t 2>&1
# Read the error — it names the file and the directive
```

Common causes:
- Duplicate `location /` — custom directives declare it when the
  template also emits one (check `RootOverridden` logic)
- Missing zone — rate limit zone not declared in
  `00-jabali-ratelimits.conf` before the vhost references it
- Bad SSL path — cert not yet issued; check `ssl_certificates` table

### Domain serves the disabled page despite being enabled

The vhost file was not regenerated after the `is_enabled` flip.
Force a reconcile:

```bash
jabali-panel admin reconciler run --token JWT
```

Or wait 60s for the next tick.

### Cache not working (always MISS)

Check the `X-Jabali-Cache` response header:

| Value | Meaning |
|-------|---------|
| MISS | First request — expected |
| HIT | Cache working |
| BYPASS | Gate skipped — check cookies, query string, auth |
| EXPIRED | TTL elapsed — expected, background update refreshes |

Verify the cache gate log:

```bash
tail /var/log/nginx/jcache-<domain>.log
```

### Reload fails but `nginx -t` passes

Rare — means nginx's master process is stuck. Check:

```bash
systemctl status nginx
journalctl -u nginx -n 20
# If master is unresponsive:
systemctl restart nginx  # hard restart (drops connections)
```
