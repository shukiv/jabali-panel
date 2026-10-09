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

## Consequences

**Positive**
- A flush is O(1) and correct even when the reclaim does nothing.
- A flush no longer costs every other site on the host a walk over DB 1.

**Negative**
- Keys and pages written by 1.1.0 are not read after the upgrade; each site's
  cache refills once.
- Keys from old generations stay in Redis until the reclaim, their TTL or LRU
  eviction removes them.
- `wp_cache_flush_group()` is a full object flush.

## Alternatives considered

- **Keep `SCAN` + `DEL`.** Rejected for both costs above.
- **One counter per group** (per-group flush without a full flush). Rejected
  for now: it adds a counter read per group per request for a feature only
  WordPress core's AI client cache uses, and core already handles
  `supports( 'flush_group' ) === false`.
- **Seed at 0, or at seconds.** Rejected: an evicted counter could come back on
  a used generation.
