# Changelog

All notable changes to the **Jabali Cache** WordPress plugin are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/), and the
project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

Nothing yet.

## [1.0.5] — 2026-07-07

### Fixed
- Redis object-cache socket failures (`connect /run/redis/redis.sock failed: Permission denied (13)`) now include an actionable hint: the site's PHP-FPM user was missing from the `jabali-redis-clients` group. The panel now self-heals that membership every reconcile tick (`user.slice.ensure`) and restarts the pool, so migrated / reprovisioned accounts recover automatically instead of silently running without object cache.

## [1.0.4] — 2026-07-02

### Added
- Batched multi-key object-cache ops (GH #607): `set_multiple`/`add_multiple` pipeline their writes (pure-PHP RESP pipeline / phpredis `MULTI(PIPELINE)`), and `delete_multiple` issues one native multi-key `UNLINK` — instead of one Redis round-trip per key.
- phpredis persistent connections (`pconnect`) with a per-(ACL-user, DB) `persistent_id`, so steady-state requests skip the connect handshake when the extension is present (GH #606).
- Auto-purge the Jabali nginx FastCGI page cache on content changes (GH #611).
  Post edits do a **targeted** purge of the post's own URL + the home page
  (GH #619); site-wide changes (theme/options/comments) purge the whole domain.
  Tenant PHP can't touch the root-owned nginx cache, so the plugin drops a purge
  request into `/run/jabali-wp-purge`; the agent's watcher validates the tenant
  owns the host and runs the purge. No-op off Jabali.

## [1.0.3] — 2026-07-02

### Fixed
- Object-cache `incr()`/`decr()` preserve an existing key TTL via `SET ... KEEPTTL` (Redis 6.2+) instead of dropping it — counters with an expiry are no longer immortalised (GH #604). (Atomicity unchanged: the read-modify-write matches WP core's own non-atomic incr/decr.)
- Page-cache content-change purge hooks (`jabali_cache_register_purge_hooks`) now gate on `Jabali_Cache_Config` (the same source `Page_Cache::run()` serves from), not the options table — pages no longer stay stale when page cache is enabled via constants (GH #603).

### Changed
- Cache flushes/purges (`delete_by_pattern`) now use non-blocking **UNLINK**
  instead of `DEL`, so a large flush reclaims memory on a background thread and
  doesn't stall the shared Redis event loop / spike latency for other tenants
  (GH #608). Both the phpredis and pure-PHP RESP paths.

## [1.0.2] — 2026-07-02

### Changed
- **Redesigned the configuration section** (Settings → Jabali Cache) into a
  modern, card-based layout matching the reference (GH #614): a top
  **Drop-ins & Settings** card (Install/repair + Remove), then numbered cards —
  **(1) General Cache Settings**, **(2) Redis Connection**, **(3) Full-page
  Cache**, **(4) Advanced / Notes** — in a responsive two-column layout, with
  clear labels + helper text and a show/hide toggle on the Redis password.

### Preserved
- Save / install-repair / remove behavior and every setting (enable, full-page
  toggle, page TTL, object max TTL, Redis connection method + values, password).

### Notes
- Settings-UI change only — caching engine + drop-ins unchanged; drop-in
  versions not bumped.

## [1.0.1] — 2026-07-02

### Changed
- **Redesigned the admin dashboard** (Settings → Jabali Cache) to a modern,
  card-based layout (GH #609):
  - Large `Jabali Cache` page title and a drop-in status banner.
  - Four top metric cards: **Caching enabled**, **Redis connection**,
    **Server page cache**, and **Cache hits (this request)**.
  - Two-column lower layout: a **Cache Status** card (left) and, on the right, a
    **Technical Details** card plus an **Actions** card
    (Flush cache now / Refresh status / View documentation).
  - Inline SVG icons, soft borders, rounded corners, subtle shadows, and clear
    green success / neutral states; responsive down to a single column.
  - Connection settings and drop-in install/repair/remove moved below the
    dashboard under **Drop-ins & Settings**.

### Preserved
- All operational data still shown: caching enabled, Redis connection, driver,
  serializer, target, key prefix, key count, request hit/miss ratio, object TTL,
  object-cache and advanced-cache drop-in status, server page-cache status +
  last probe, Edge `Cache-Control`, and revalidation details.
- All existing actions still work: flush cache, install/repair/remove drop-ins,
  save connection settings, and the admin-bar **Flush Cache** shortcut.

### Notes
- Dashboard/UI change only — the caching engine, drop-ins, and wire behavior are
  unchanged. The `object-cache.php` / `advanced-cache.php` drop-in versions are
  intentionally **not** bumped, so no drop-in reinstall is triggered on upgrade.

## [1.0.0]

### Added
- Initial release.
- Persistent Redis object cache with strict per-site key isolation.
- Pure-PHP Redis client fallback when the phpredis extension is absent.
- Optional full-page cache (off by default; complements the nginx edge cache).
- WP-CLI commands: status, flush, enable, disable, update-dropins,
  remove-dropins, diagnose.
- Admin diagnostics screen with live connection status and fix-it guidance.
- Multisite-aware namespacing.
- Fail-safe: WordPress keeps running normally whenever Redis is unavailable.

[Unreleased]: https://github.com/shukiv/jabali-panel/blob/main/wp-plugins/jabali-cache
[1.0.1]: https://github.com/shukiv/jabali-panel/issues/609
[1.0.0]: https://github.com/shukiv/jabali-panel/blob/main/wp-plugins/jabali-cache
