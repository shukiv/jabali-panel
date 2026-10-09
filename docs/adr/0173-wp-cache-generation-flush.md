# ADR-0173: WP cache flushes bump a generation counter

**Status:** Accepted (2026-10-09)
**Related:** ADR-0059 (Redis DB 1 for WordPress), ADR-0148 (per-site Redis ACL users for the WP cache).

## Context

The bundled jabali-cache plugin keeps every site's object cache (and the
optional page cache) in Redis DB 1, which all sites on a host share. Each site
is isolated by a key prefix (`jc:<osuser>:<install>:`). Up to 1.1.0, a flush
(`wp_cache_flush()`, `wp_cache_flush_group()`, a page-cache purge) found the
site's keys with `SCAN MATCH <prefix>*` and deleted them.

That has two costs:

- `SCAN MATCH` walks the whole database and filters afterwards, so a flush
  costs time in proportion to every site's keys on the host, not to the
  site's own.
- A flush is only correct if that walk finds and deletes every key. If it
  can't run, or stops early, the site keeps serving what it meant to flush.

## Decision

From plugin 1.2.0, a flush bumps a per-site generation counter, and the
generation is part of what is read:

- **Object cache:** every key is `{prefix}o{gen}:{group}:{blog}{key}`. The
  generation lives in `{prefix}gen:o`. A flush is one `INCR`: keys from an
  older generation are never read again.
- **Page cache:** every stored page records the generation it was built under
  (`pgen`). The generation lives in `{prefix}gen:p`. A page from an older
  generation is a miss. An object-cache flush bumps both counters, so it still
  clears cached pages, as before.
- **Seed:** a missing counter is created at the current time in microseconds,
  not 0. If Redis evicts a counter (DB 1 runs `allkeys-lru`), the new value
  can't land on a generation that was already used and bring flushed data
  back. Milliseconds are not enough: a few quick flushes in a row can outrun
  them.
- **Strings, not ints:** generations stay digit strings in PHP. On 32-bit PHP
  the value doesn't fit an int, and a cast would pin every generation to
  `PHP_INT_MAX`.
- **No generation, no Redis:** if the counter can't be read, the request uses
  the in-memory cache only, so a key built without a generation never reaches
  Redis. A long-running process (WP-CLI, a queue worker) re-reads the counter
  every 5 seconds. When it changes, the process drops its in-memory copies from
  the old generation.
- **Group flush:** `wp_cache_supports( 'flush_group' )` is false. WordPress
  core checks it before calling `wp_cache_flush_group()`. A direct call does a
  full object flush, which flushes more than asked but never leaves stale data.
- **Reclaim:** after the bump, the plugin still deletes the site's old keys
  with the prefix-scoped `SCAN` + `UNLINK` (never the counters). This is now
  only a memory reclaim; old keys also expire on their TTL or are evicted.
  The budget trim never deletes the counters either.

`jabali update` also re-syncs the bundled plugin
(`/usr/local/share/jabali/wp-plugins/jabali-cache`) from the checkout on every
update, before the `refresh-cache-plugin` sweep re-copies it to every
cache-enabled site. Before, only install.sh and the release-tarball step did,
so a `--from-source` update re-staged the old plugin.

### Panel cleanup

Old generations no longer have to be found by the plugin. The panel removes
them itself (`panel-api/internal/api/wpcache_gc.go`):

- **One pass every 15 minutes** (the first 2 minutes after start), at most
  5 minutes long. A pass that runs out of time stops; the next one starts over.
- **One walk for the host.** The pass runs a single `SCAN MATCH jc:*` over DB 1
  as `jabali_panel`, instead of every site walking the database on each flush.
- **Every read and delete as the site's own user.** For each cache-enabled,
  ready WordPress install, the pass connects as `wp_<osuser>_<install>` with
  that install's token. Redis fences that user to the install's own keys, so a
  mistake in the pass can't touch another site's keys. A site whose token
  doesn't authenticate is skipped, not cleaned as the panel.
- **What is removed:** object keys whose generation is older than `gen:o`, and
  1.1.0 keys (no generation) once the site has a `gen:o`. Counters (`gen:`),
  locks (`lock:`) and the plugin's own `__jabali` keys are always kept. Pages
  are counted and left to their TTL. A generation newer than the counter (a
  flush that landed mid-pass) is kept.
- **Key budget.** The same pass enforces the site's `redis_maxmemory_mb` budget
  (GH #612) at ~2 KiB per key, removing at most 5000 keys per site per pass.
- **Key count.** The pass records each site's key count and when it was taken.
  The cache drawer's "Keys (this site)" shows that count instead of the
  plugin's own.

The per-install ACL user now leaves with the site:

- **Disable and delete.** Turning a site's cache off, or deleting a WordPress
  app (from the panel or `jabali app delete`), removes the site's keys first,
  through its own user, then the user. A failed purge still removes the user;
  the keys are then left to TTL and LRU.
- **Orphan users.** The pass removes per-install users whose install row no
  longer exists. It gives each one a fresh one-off password, removes its keys
  through it, deletes the user and runs `ACL SAVE`. It only does this when it
  read the full install list. A site with its cache off but its row present is
  never treated as an orphan: an enable creates the user before it records the
  flag.

Plugin 1.2.1 reports a flush or page purge that Redis didn't record (the
generation couldn't be bumped): `flush()` and `purge_all()` return false,
`wp jabali-cache flush` exits with an error, and the admin page says the flush
failed instead of "Cache flushed."

### The site's Redis user no longer gets SCAN

A flush is now a counter bump, and the panel removes old keys, enforces the
key budget and counts each site's keys, so the plugin no longer needs `SCAN`.
Least privilege: the per-install rule (`applyInstallACL`) no longer grants it.

- **New and re-provisioned users.** A cache enable, `cache-doctor --repair`
  and `cache-doctor --migrate-acl` write the rule without `SCAN`. These paths
  also re-stage the bundled plugin, so the plugin and the rule change together.
- **Existing users** keep their rule until `jabali app refresh-cache-plugin`,
  which every `jabali update` runs, refreshes the site to a plugin that
  flushes without `SCAN` (1.2.0 or later). It then re-applies the rule
  (`ResyncInstallACL`). The rule resets the user's passwords, so the re-sync
  first checks that the site's token still authenticates. If it doesn't, the
  user is left alone.
- **Decided from the bundle.** In the default (bundled) mode, the agent's
  refresh runs nothing as the tenant: it skips a site without a
  `wp-content/plugins` directory, stages the bundle, and reports the
  bundle's version. A broken `wp` on a site, or the site's own WordPress or
  wp-cli config, doesn't stop the ACL update.
- **One way only.** A version the sweep can't parse, or an older one, leaves
  the ACL as it is. Nothing in the panel adds `SCAN` back.
- **The plugin's own SCAN uses.** Its reclaim and budget trim get `NOPERM`,
  find nothing and carry on. Its key count reads 0, so the cache-stats API
  drops it and sends `keys` only once the panel has counted. The cache drawer
  shows "—" until then.
- The opt-in `JABALI_WP_CACHE_SOURCE=wordpress-org` source assumes the
  published plugin is 1.2.0 or later.

## Consequences

**Positive**
- A flush is O(1) and correct even when the reclaim does nothing.
- A flush no longer costs every other site on the host a walk over DB 1.
- A site's Redis user can do only what the plugin needs; `SCAN` is gone.

**Negative**
- Keys and pages written by 1.1.0 are not read after the upgrade; each site's
  cache refills once.
- Keys from old generations stay in Redis until the panel's cleanup pass (up
  to 15 minutes), the plugin's reclaim, their TTL or LRU eviction removes them.
- `wp_cache_flush_group()` is a full object flush.
- The plugin's own admin page shows 0 keys for the site; the panel's cache
  drawer has the count.

## Alternatives considered

- **Keep `SCAN` + `DEL`.** Rejected for both costs above.
- **One counter per group** (per-group flush without a full flush). Rejected
  for now: it adds a counter read per group per request for a feature only
  WordPress core's AI client cache uses, and core already handles
  `supports( 'flush_group' ) === false`.
- **Seed at 0, or at seconds.** Rejected: an evicted counter could come back on
  a used generation.
