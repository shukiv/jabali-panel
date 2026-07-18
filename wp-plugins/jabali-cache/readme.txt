=== Jabali Cache ===
Contributors: apunker
Tags: redis, object cache, cache, performance, page cache
Requires at least: 5.6
Tested up to: 7.0
Requires PHP: 7.4
Stable tag: 1.1.0
License: GPLv2 or later
License URI: https://www.gnu.org/licenses/gpl-2.0.html

Persistent Redis object cache for WordPress. Zero-config on the Jabali panel, works without the phpredis extension, and always fails safe.

== Description ==

**Jabali Cache makes WordPress fast by keeping its object cache in Redis instead of rebuilding it on every request.** WordPress normally throws away its internal cache at the end of each request, so the same expensive database queries run again and again. This plugin makes that cache persistent — backed by the Redis instance the [Jabali hosting panel](https://jabali-panel.com) already runs — so pages assemble from memory instead of hammering MySQL.

It is purpose-built for shared hosting, where many sites share one Redis server. That shapes every design decision: strict per-site isolation, safe behaviour under memory pressure, and a hard rule that a caching layer must *never* take a site down.

= Why you might want it =

* **Fewer database queries, faster pages.** Options, post meta, terms, user data and transients come from Redis on repeat requests instead of MySQL.
* **Lighter database load.** Especially noticeable on WooCommerce, membership, and query-heavy sites under traffic.
* **Drop-in.** Activate it and WordPress's standard object-cache API (`wp_cache_*`, transients) starts using Redis. No theme or plugin changes.

= Built for the way Jabali hosts sites =

* **Zero-config on Jabali.** Connects to the panel Redis over the unix socket `/run/redis/redis.sock`, logical database 1. Nothing to type — no host, port, or password.
* **Strict per-site isolation.** Every install gets its own key prefix. Sites share one Redis database but can never read or flush each other's cache. A site's flush only clears that site's keys.
* **No phpredis required.** If the `redis` PHP extension is present it is used automatically; if not (the Jabali default), the plugin falls back to a dependency-free, pure-PHP RESP client over the same socket. Either way it just works.
* **Safe under memory pressure.** The panel Redis runs an `allkeys-lru` policy, so it may evict keys at any time. Every read is best-effort — an evicted or missing key simply falls through to the database. An eviction is never an error.
* **Fails safe, always.** If Redis is unreachable for any reason, WordPress keeps running with its normal per-request cache. Nothing breaks; caching just stops accelerating until the connection is back. The admin screen tells you exactly what is wrong and how to fix it.
* **Multisite-aware.** On a network, per-blog data is namespaced per site while genuinely global groups (network, site meta) are shared correctly.

= Object cache vs. page cache =

The persistent **object cache** is the main event and is on as soon as you activate the plugin.

A full **page cache** is also included, but it is **off by default**. Jabali already serves a faster page cache at the edge via nginx (FastCGI microcache), so turning on the WordPress page cache as well would just cache the same HTML twice. Enable the in-WordPress page cache only on hosts where the nginx one is not available.

= Privacy =

Jabali Cache stores WordPress's own cache data in your Redis server and nowhere else. It does not collect analytics, contact any external service, or send data off your server.

= Support =

Development happens on GitHub. Found a bug, or have a feature request? Please open an issue:

[https://github.com/shukiv/jabali-panel/issues](https://github.com/shukiv/jabali-panel/issues)

== Installation ==

= On the Jabali panel (recommended) =

1. Install and activate **Jabali Cache**. Activation drops in `object-cache.php` automatically.
2. In the panel, enable caching for the site (Applications → cache toggle). This provisions the site's Redis socket access.
3. Visit **Settings → Jabali Cache** and confirm **Redis connection: Connected**.

= On any other host =

1. Upload the `jabali-cache` folder to `wp-content/plugins/` (or install the zip from Plugins → Add New).
2. Activate **Jabali Cache**.
3. Point it at your Redis server with a few constants in `wp-config.php` (see the FAQ). Over a unix socket or TCP both work.
4. Check **Settings → Jabali Cache** for a green connection status.

If the status ever shows **Not reachable**, the screen prints the exact prerequisite to fix — and the site keeps working the whole time as a normal non-persistent cache.

== Frequently Asked Questions ==

= The status says Redis is "Not reachable". What do I do? =

On the Jabali panel, socket access is set up when you enable caching for the site (Applications → cache toggle). If the status still shows Not reachable, re-run that toggle.

On a standalone host, the site's PHP-FPM user needs two things for a unix socket:

1. **open_basedir** must allow the socket's directory — add `/run/redis` (or wherever your socket lives) to the pool's `open_basedir`.
2. **Read/write access to the socket** for the PHP-FPM user.

Until that is in place the plugin runs as a normal per-request cache and the site is completely unaffected — caching just isn't persistent yet.

= Does it need the phpredis (PECL redis) extension? =

No. It auto-detects phpredis and uses it when available; otherwise it uses a built-in pure-PHP client. You get persistent caching either way.

= How do I use it outside the Jabali panel? =

Define overrides in `wp-config.php`. With none of these set, it defaults to the Jabali socket and a per-site prefix.

`define( 'JABALI_CACHE_SOCKET', '/path/to/redis.sock' );` — unix socket, or:
`define( 'JABALI_CACHE_HOST', '127.0.0.1' );`
`define( 'JABALI_CACHE_PORT', 6379 );`

Optional:
`define( 'JABALI_CACHE_DB', 1 );` — logical database number
`define( 'JABALI_CACHE_PASSWORD', '...' );` — Redis AUTH password
`define( 'JABALI_CACHE_PREFIX', 'mysite' );` — key prefix (auto-derived per site if unset)
`define( 'JABALI_CACHE_DISABLED', true );` — force off without deactivating

= Will it isolate my cache from other sites on the same server? =

Yes. Each install uses a unique key prefix, so reads, writes, and flushes are scoped to your site only. One site cannot see or clear another's cache, even though they may share the same Redis database.

= Is it safe if Redis runs out of memory? =

Yes. The expected policy is `allkeys-lru` — Redis evicts least-recently-used keys when full. Every read in this plugin is best-effort, so an evicted key just rebuilds from the database. There is no error path that can break the site under memory pressure.

= Does it work on WordPress Multisite? =

Yes. Per-site cache data is namespaced per blog, while truly global cache groups are shared across the network as WordPress expects.

= Should I also turn on the page cache? =

Only if your host does not already have one. On Jabali, nginx serves a FastCGI microcache at the edge, which is faster than caching in PHP — so the WordPress page cache stays off by default to avoid double-caching. Off Jabali, you can enable it from the settings screen.

= How do I manage it from WP-CLI? =

    wp jabali-cache status          # connection, driver, prefix, key count, page-cache state
    wp jabali-cache flush           # flush this site's object cache
    wp jabali-cache flush --pages   # also purge page-cache entries
    wp jabali-cache enable          # enable caching
    wp jabali-cache disable         # disable caching
    wp jabali-cache update-dropins  # (re)install the wp-content drop-ins
    wp jabali-cache remove-dropins  # remove the drop-ins
    wp jabali-cache diagnose        # full connectivity + environment report

= What happens when I uninstall it? =

Deactivating removes the `object-cache.php` drop-in cleanly, so WordPress reverts to its built-in non-persistent cache. Uninstalling removes the plugin's options. Your Redis data is just a cache and is safe to discard.

== Changelog ==

= 1.1.0 =
* Bypass the full-page cache for authenticated requests and honor a response `Cache-Control: no-cache/private` so logged-in / no-store pages are never served from cache (JAB-60, JAB-61).
* Fail-fast circuit breaker: after repeated Redis errors the plugin short-circuits to the origin for a cool-off window instead of stalling every request on a dead socket (JAB-89).
* Stampede protection for the Redis full-page cache: concurrent misses no longer all regenerate the same page (JAB-90).
* Targeted page-cache purge on post edit (the post URL + home) instead of a whole-site flush (JAB-91).
* gzip large page-cache payloads and skip oversize bodies, cutting Redis memory for big pages (JAB-92).
* Bound + cache the object-cache key-count scans behind the budget trim so a large keyspace no longer does an unbounded SCAN each tick (JAB-94).
* Keep phpredis persistent connections across requests via a per-(ACL-user, DB) persistent id (JAB-96).
* Atomic INCRBY/DECRBY for raw integer counters instead of get-modify-set, removing a lost-update race under concurrency (JAB-97).

= 1.0.5 =
* Redis object-cache socket failures (`Permission denied`) now include an actionable hint that the site's PHP-FPM user was missing from the `jabali-redis-clients` group; the panel self-heals that membership every reconcile and restarts the pool (GH #410 follow-up).

= 1.0.4 =
* Batch multi-key object-cache writes: set_multiple/add_multiple now pipeline their Redis commands and delete_multiple uses one native multi-key UNLINK, instead of one round-trip per key (GH #607).
* Use a persistent Redis connection (pconnect) when the phpredis extension is present, so steady-state requests skip the connect handshake (GH #606).
* Auto-purge the Jabali nginx page cache when content changes (GH #611): post edits purge that post's URL + the home page (targeted, GH #619); theme/option/comment changes purge the whole domain. On Jabali only; a no-op elsewhere.

= 1.0.3 =
* Object-cache incr()/decr() now preserve an existing key TTL (SET ... KEEPTTL) instead of dropping it, so counters/throttles with an expiry are no longer immortalised (GH #604).
* Page-cache content-change purge hooks now gate on the same runtime config the cache serves from, so pages no longer go stale when page cache is enabled by constants (GH #603).
* Redis cache flushes/purges now use non-blocking UNLINK instead of DEL, so a large flush no longer stalls the shared Redis event loop or spikes latency for other tenants (GH #608).

= 1.0.2 =
* Redesigned the configuration section into a modern, card-based layout (GH #614):
  a top "Drop-ins & Settings" card, then numbered cards — (1) General Cache
  Settings, (2) Redis Connection, (3) Full-page Cache, (4) Advanced/Notes — in a
  responsive two-column layout, with clear labels + helper text and a show/hide
  toggle on the Redis password. Existing save/install/remove behavior preserved;
  settings-only change (drop-in versions unchanged).

= 1.0.1 =
* Redesigned the admin dashboard (Settings → Jabali Cache): large page title, a
  drop-in status banner, four at-a-glance metric cards (caching enabled, Redis
  connection, server page cache, request hit ratio), and a two-column layout
  with a Cache Status card, a Technical Details card, and an Actions card
  (Flush cache now / Refresh status / View documentation). Modern card styling
  with icons and clear success/neutral colors. All existing operational data and
  actions are preserved; the connection settings and drop-in management moved
  below the dashboard. No behavior change to caching itself. (GH #609)

= 1.0.0 =
* Initial release.
* Persistent Redis object cache with strict per-site key isolation.
* Pure-PHP Redis client fallback when the phpredis extension is absent.
* Optional full-page cache (off by default; complements the nginx edge cache).
* WP-CLI commands: status, flush, enable, disable, update-dropins, remove-dropins, diagnose.
* Admin diagnostics screen with live connection status and fix-it guidance.
* Multisite-aware namespacing.
* Fail-safe: WordPress keeps running normally whenever Redis is unavailable.

== Upgrade Notice ==

= 1.0.4 =
Auto-purges the server page cache on content changes. Safe drop-in update.

= 1.0.3 =
Non-blocking cache flushes (UNLINK). Safe drop-in update.

= 1.0.2 =
Redesigned configuration screen only — same caching engine. Safe drop-in update.

= 1.0.1 =
Refreshed admin dashboard only — same caching engine. Safe drop-in update.

= 1.0.0 =
Initial release.
