# Jabali Cache

Redis-backed WordPress **object cache** (and optional full-page cache) built for the
[Jabali hosting panel](https://jabali-panel.com/). It plugs into the Redis instance the
panel already provisions and follows the panel's shared-hosting security model.

> This is a self-contained WordPress plugin. It does **not** modify any Jabali panel
> code — it lives under `wp-plugins/jabali-cache/` and is deployed into a WordPress
> install's `wp-content/plugins/` directory.

## Why it fits Jabali

| Jabali fact | How the plugin uses it |
|---|---|
| Redis at `unix:///run/redis/redis.sock`, **db 1 reserved for WP** (ADR-0059) | Default connection target; no host/port config needed. |
| Redis runs `maxmemory-policy allkeys-lru` | Every read is best-effort; an evicted key is a normal miss, never an error. |
| db 1 is **shared across all tenants** | Isolation is by per-site key **prefix**. The plugin **never** issues `FLUSHDB`. A flush bumps the site's generation counter, which every key carries, so nothing cached before it is read again; the old keys are then reclaimed prefix-scoped (ADR-0173). |
| Jabali installs the `redis` (phpredis) + `igbinary` PHP extensions by default (GH #606) | Uses phpredis with persistent `pconnect()`; the dependency-free pure-PHP RESP client is a portable fallback if the extension is ever absent. |
| Tenant PHP-FPM runs as the site user, `open_basedir`-jailed | Connects gracefully; on failure it degrades to a per-request cache and prints the exact host fix. |
| nginx fastcgi microcache already exists (ADR-0108) | WP full-page cache is **off by default** to avoid double-caching; object cache is the primary win. |

## Architecture

```
jabali-cache/
├── jabali-cache.php              # plugin bootstrap, activation, purge hooks
├── uninstall.php                 # prefix-scoped Redis purge + cleanup
├── config-sample.php             # optional wp-config.php overrides
├── includes/
│   ├── lib.php                   # Config resolver + Redis client (phpredis OR pure-PHP RESP)
│   ├── class-object-cache.php    # WP_Object_Cache engine
│   ├── class-page-cache.php      # full-page cache engine
│   ├── class-settings.php        # options store (legacy generated config file is deleted on save)
│   ├── class-dropin-manager.php  # installs/removes wp-content drop-ins (signature-guarded)
│   ├── class-admin.php           # Settings → Jabali Cache screen + diagnostics
│   └── class-cli.php             # `wp jabali-cache ...`
├── dropins/
│   ├── object-cache.php          # → wp-content/object-cache.php (thin loader, no-op fallback)
│   └── advanced-cache.php        # → wp-content/advanced-cache.php (page cache, fail-safe)
└── tests/
    └── test-lib.php              # runnable unit test for the RESP client + key building
```

The drop-ins are **thin loaders**: they locate the plugin (path stamped at install,
with fallbacks) and delegate to `includes/`. If the plugin folder is ever missing,
`object-cache.php` falls back to a per-request non-persistent cache so the site never
fatals.

## Install

1. Copy `jabali-cache/` into `wp-content/plugins/`.
2. Activate it — the `object-cache.php` drop-in is installed automatically.
3. **Settings → Jabali Cache** → confirm *Redis connection: Connected*.

### Host prerequisites (panel-managed — do not do by hand)

**The panel manages Redis socket access automatically** when you enable the cache
for a site — nothing to run by hand.

How it works (reference only): the shared Redis socket lives at
`/run/redis/redis.sock`. Its owning group is `jabali-sockets`, which is
**privileged (panel-api)** — a tenant site user must **NEVER** be added to it.
Instead the panel grants each cache-enabled tenant read/write to the socket via a
POSIX ACL for the dedicated `jabali-redis-clients` group (`setfacl -m
g:jabali-redis-clients:rw /run/redis/redis.sock`) and adds the site user to
`jabali-redis-clients`, then reloads the per-user FPM master. The pool's
`open_basedir` already allows `/run/redis`.

> Do **not** run `usermod -aG jabali-sockets <site-user>` — that would put a tenant
> in the privileged panel group and is a security downgrade. If the admin screen
> shows *Not reachable*, re-enable the cache in the panel (it re-applies the ACL +
> group + FPM reload); the site keeps working as a non-persistent cache meanwhile.

## WP-CLI

```bash
wp jabali-cache status          # connectivity, driver, key count
wp jabali-cache diagnose        # extension + connection hints
wp jabali-cache flush [--pages] # flush object cache (+ page cache)
wp jabali-cache enable|disable
wp jabali-cache update-dropins  # (re)install drop-ins
wp jabali-cache remove-dropins
```

## Configuration

Everything is configurable from the settings screen. Power users can override via
constants in `wp-config.php` — see `config-sample.php`. Constants win over the screen.

## Tests

`tests/test-lib.php` is a plain-PHP harness (no PHPUnit needed):

```bash
php wp-plugins/jabali-cache/tests/test-lib.php
# add a live socket round-trip when Redis is available:
JABALI_TEST_REDIS=/run/redis/redis.sock php wp-plugins/jabali-cache/tests/test-lib.php
```

## License

GPL-2.0-or-later.
