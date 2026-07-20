# ADR-0158: Per-domain cacheable query-param allowlist

## Status
Accepted (2026-07-21). Extends [ADR-0108](0108-per-domain-fastcgi-microcache.md) (per-domain FastCGI micro-cache).

## Context
The nginx full-page micro-cache bypasses **every** URL that carries a real
(non-tracking, non-empty) query string: `$jabali_qs_kind = other → skip=1`, and
the cache key is path-only (`$scheme$request_method$host$jabali_cache_path`, query
stripped — the Codeberg #5 collision fix). That is correct for tracking params
and unknown query state, but it means **paginated / archive pages never cache**:
`?paged=2`, category archives, and similar GET pages regenerate PHP+DB on every
hit. For listing-heavy sites (e.g. JetEngine review/directory sites) that is the
single largest uncached surface.

We want those GET pages to cache as **distinct** entries, without re-opening the
cross-content-bleed class or exploding the cache with faceted params.

## Decision
An **opt-in, per-domain allowlist** of query-param names that become part of the
cache key. Default empty → the vhost renders byte-identical to pre-feature (the
#1 review gate, enforced by a golden test).

- **Storage:** `domains.cache_query_allowlist` (migration 000230), comma-joined,
  lower-cased, sorted, de-duped, `^[a-z0-9_]{1,32}$`, max 8 names. Validated at
  the API and **re-validated at the agent** (config-injection trust boundary,
  like `sanitizeBypassPaths`) so a name can never carry regex metacharacters.
- **Gate (per-domain, replaces the shared `qs_kind=other` line):** a rendered
  `$jabali_qs_dirty` test — the query is "clean" only when it consists entirely
  of allowlisted params with **non-empty** values:
  `^(?:(?:paged|…)=[^&]+)(?:&(?:…)=[^&]+)*$`. The block **only ever sets
  `skip=1`**; an allowlisted page relies on the earlier no-cookie rule for
  `skip=0`, so logged-in / cart / wp-admin / POST stay bypassed (those checks
  run before and after and always re-assert `skip=1`).
- **Canonical key:** allowlisted args are folded into the key **by name, in a
  fixed panel-sorted order** via `$arg_<name>`, so `?a=1&b=2` and `?b=2&a=1`
  collapse and non-allowlisted args never enter the key. A conditional
  `$jabali_qsep` adds the `?` **only when an allowlisted arg is present**, so a
  query-less page keys as plain `$jabali_cache_path` — byte-identical to the
  non-allowlist key and safe for targeted post-edit purge.
- **Pure nginx, no lua** (jabali nginx has no lua module). `set`-inside-`if` is
  the known-safe subset used.

## Consequences
- **Wins:** pagination/archive GET pages cache (HIT) as distinct entries. Opt-in,
  tight, admin-curated — no faceted explosion by default.
- **Non-goal — JetEngine AJAX filters:** `?jet_*` filters are `admin-ajax.php`
  **POST** requests → always bypass (POST + wp-admin gates). This feature helps
  GET query-param pages only; the UI help text says so.
- **No device split:** the cache key has **no `$http_user_agent`**, so there is
  one entry per URL regardless of device, and warmup need not double per UA.
- **Purge:** whole-domain purge (host-match) evicts query variants (they share
  the host in the KEY). **Targeted** post-edit purge computes
  `md5(scheme+method+host+path)` and so covers the **base page** only; query
  variants (`path?paged=2&`) fall to whole-domain purge or TTL. Documented v1
  limitation, acceptable because content edits trigger a broader purge and TTLs
  are short.
- **Mixed allowlist+tracking** (`?paged=2&utm_source=x`) is treated as dirty →
  bypass in v1 (the gate matches allowlisted-only). The common `?paged=N` case is
  covered; combining with tracking params is the rarer path.

## Alternatives rejected
- **Shared http `map` for the allowlist** — `map` is http-context, cannot be
  per-domain; rendered per-server instead.
- **Caching all query params** — re-opens content-bleed + unbounded cache growth.
- **Lua-based key normalization** — no lua module in the jabali nginx build.

## Verification
Golden test asserts the empty-allowlist render is byte-identical. Render tests
assert the dirty gate, canonical sorted key, and conditional separator.
Box-verified on testserver with the `X-Jabali-Cache` header: `?paged=2` → HIT
(distinct body), `?paged=3` → distinct HIT, `?notallowed=1` → BYPASS, a second
domain with an empty allowlist → `?paged=2` still BYPASS, `nginx -t` clean.
