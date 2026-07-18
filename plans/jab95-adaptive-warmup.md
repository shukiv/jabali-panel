# JAB-95 — Adaptive, resumable, status-visible cache warmup

**Issue:** JAB-95 (perf/cache, medium). Cache warmup is a best-effort sitemap
crawl with a hard cap (50) and almost no feedback. On larger WordPress sites it
warms the wrong 50 URLs and leaves hot pages cold; it can't resume, can't be
watched, and the panel's requested limit is silently clamped.

**Guiding constraint:** every phase ships independently and is box-verifiable on
testserver (WP at jabali.site). Warmup must never overload PHP-FPM. Keep the
"through local nginx via curl --resolve" pinning that already prevents off-box
fetches.

## Ground truth (don't re-discover)

- **Agent:** `panel-agent/internal/commands/nginx_cache_warmup.go`
  - `nginxCacheWarmupHandler(ctx, params)` — entry (registered `nginx.cache_warmup`).
  - `cacheWarmupHardMax = 50`; `max := req.MaxURLs; if max > cacheWarmupHardMax { max = 50 }` — the silent clamp.
  - `sitemapPaths(ctx, host, max)` — WP core `/wp-sitemap.xml` (index, one level expanded), then `/sitemap.xml`, `/sitemap_index.xml`; `locRe` pulls `<loc>`.
  - Returns only `{warmed}` today.
- **Panel API:** `panel-api/internal/api/applications_cache.go` calls warmup with
  `max_urls: 100`; `applications_cache_settings.go` + `domain_cache.go` are the
  cache-settings surfaces; the API record infers URL count from the response.
- **Models:** `panel-api/internal/models/application_install.go` holds
  cache-related install state.
- **UI:** application cache settings card (panel-ui, admin/user shells — the
  "Cache" section that shows warmup).

## Phase 1 — Rich warmup stats + honest clamp reporting  (agent + api)

**One-PR scope.** Foundation for everything else; no persistence yet.

- Agent: change the handler return from `{warmed}` to a struct:
  `{requested, attempted, warmed, skipped, failed, first_error, duration_ms, clamped_to}`.
  - `requested` = `req.MaxURLs`; `clamped_to` = the effective max after
    `cacheWarmupHardMax`. When `requested > cacheWarmupHardMax`, set
    `clamped_to = 50` and surface it (no more silent clamp).
  - Count `attempted`/`warmed`/`skipped`(already-fresh)/`failed`; capture the
    first non-2xx/err into `first_error`; stamp `duration_ms`.
- API: thread the new fields into the warmup record instead of inferring the
  count; expose them on the cache-status response.
- **Verify:** unit-test the stats accounting (table-driven over fake fetch
  results); box-test on testserver: `jabali` warmup a domain, confirm the record
  shows requested/warmed/failed + `clamped_to=50` when asked for 100.
- **Exit:** the panel shows real warmed/attempted/failed + the clamp is reported.

## Phase 2 — URL prioritization  (agent)

**One-PR scope.** Depends on Phase 1's `attempted` accounting.

- Build the warm list in priority order, then take the first `clamped_to`:
  1. homepage `/`;
  2. operator-configured critical URLs (new optional `critical_urls` param, cap N);
  3. recent/high-traffic paths from the host's nginx access log when readable
     (`/var/log/nginx/<host>.access.log` → top-N GET 200 HTML paths, last window);
  4. sitemap URLs ordered by `<priority>`/`<lastmod>` when present (extend
     `locRe`/`sitemapPaths` to parse priority+lastmod, sort desc);
  5. WooCommerce category/product pages before low-value archives (URL-pattern
     heuristic: `/product/`, `/product-category/` rank above `/tag/`, `/author/`).
- De-dup across sources; keep the host-pin safety (drop off-host URLs).
- **Verify:** unit tests for the ordering (sitemap priority sort, access-log
  parse, WooCommerce ranking, dedup); box-test: warm jabali.site, confirm
  homepage + high-priority pages are attempted first.
- **Exit:** warmup attempts likely-hot pages first, not just the first sitemap entries.

## Phase 3 — Persistent, resumable progress  (models + migration + api + agent)

**One-PR scope.** The biggest design surface — own PR.

- New table `cache_warmup_runs` (migration): `id, host, requested, clamped_to,
  attempted, warmed, skipped, failed, first_error, state('queued'|'running'|
  'done'|'failed'), cursor(int, next index into the priority list), started_at,
  updated_at, finished_at`. One active run per host (unique partial index on
  state in (queued,running)).
- Agent: accept a `resume_run` / persist cursor via a callback to the panel
  (or the panel drives batches): warmup processes a bounded batch per call,
  advances `cursor`, returns progress; the panel re-invokes until `done`. This
  keeps the agent stateless and makes an interrupted warmup resumable from
  `cursor`.
- API: `POST /applications/:id/cache/warmup` creates/reuses a run; a
  reconciler/ticker or the same request loop advances batches; `GET
  .../cache/warmup` returns the run row.
- **Verify:** unit tests for the run state machine (queued→running→done/failed,
  cursor advance, resume from mid-cursor); box-test: start a warmup, kill it
  mid-run, restart, confirm it resumes from `cursor` (not from 0).
- **Exit:** an interrupted warmup resumes without starting over; state is durable.

## Phase 4 — Per-host load controls  (agent)

**One-PR scope.**

- Bound warmup concurrency per host (small worker pool, e.g. 2) + a min inter-
  request delay so warmup can't spike PHP-FPM. Config knobs with safe defaults;
  respect a global "warmup paused" switch.
- Back off on repeated 5xx/timeout (circuit-break the run to `failed` with
  `first_error`) instead of hammering a struggling site.
- **Verify:** unit test the pacing/backoff; box-test: warm a domain while
  watching FPM — no request-rate spike beyond the configured ceiling.
- **Exit:** warmup has per-host rate/concurrency controls and doesn't spike FPM.

## Phase 5 — UI progress surface  (panel-ui)

**One-PR scope.** Depends on Phase 3's `GET .../cache/warmup`.

- In the application Cache card: show run state (queued/running/done/failed),
  warmed/attempted count + a progress bar (`warmed`/`clamped_to`), last run
  time, and `first_error` on failure. Poll while `running`.
- Report the clamp ("requested 100, warming 50") from Phase 1 so the limit is
  clear, not silently dropped.
- **Verify:** vitest for the card states; 390px + desktop visual check against a
  running panel (auth-gated — operator eyeball).
- **Exit:** admin can see whether warmup is running, what it warmed, and why it stopped.

## Sequencing

1 → 2 → 3 → 4 → 5, strictly. 1 unblocks everything (stats contract); 3 is the
durable-state keystone that 5 renders. 2 and 4 are agent-only and could swap
order, but 4 reads cleaner after 2's list-building lands.

## Acceptance (issue) → phase map

- see running/warmed/why-stopped → Phase 1 (stats) + Phase 5 (UI).
- prioritizes hot pages → Phase 2.
- resumable → Phase 3.
- per-host load controls, no FPM spike → Phase 4.
- tests: sitemap priority (2), max-clamp reporting (1), progress persistence (3),
  failure status (1/3).
