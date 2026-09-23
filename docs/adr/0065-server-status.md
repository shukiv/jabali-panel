# ADR-0065: Server Status aggregator

**Status:** ACCEPTED (2026-04-25). M31 Steps 1–6 shipped.
**Related:** plans/m31-server-status.md.

## Context

Operators want one page to see the live state of a managed Jabali host:
hostname, kernel, uptime, load, CPU/mem/disk meters, per-service health,
network rates, top-N processes, pending updates, NTP, and a top-of-page
alert banner. The existing admin Dashboard surfaces a fraction of this
and hits multiple endpoints sequentially. M31 introduces a dedicated
page; this ADR documents the backend half.

## Decisions

### 1. Single REST aggregator endpoint

`GET /admin/server-status` returns the entire envelope in one shot. The
panel-api handler fans out to the agent in parallel using
`golang.org/x/sync/errgroup` and synthesizes the alerts before
responding.

Rejected alternatives:
- One REST per slice (host, cpu, network…). 5–8 round-trips per refresh
  multiplied by N admin tabs hammers the agent for no reason.
- WebSocket / SSE push. Server status is not a real-time feed; 5s
  polling is adequate and avoids long-lived connection state on the
  panel-api side.

### 2. Per-sub-call 5s timeout, hard cap of 8 in-flight

`g.SetLimit(maxInFlight=8)` + `context.WithTimeout(subCallTimeout=5s)`
on every sub-call. A slow `system.processes` (sorting top-N over 500
procs on a busy host) doesn't block the rest of the envelope: the slow
slice becomes `null` in the response and an `errors.processes` entry
explains why.

The aggregator never returns 5xx for sub-call failure — the envelope is
"best effort" by design. UI handles `null` slices with a "—" cell.

### 3. Reuse existing agent commands; add three more

| Command | Purpose | Status |
|---|---|---|
| `system.info` | hostname, OS, kernel, CPU model + count, mem, swap, partitions, uptime, load avg, NTP | extended (added OS/kernel/cpu_model/swap/NTPSynced) |
| `service.list` | systemd units allowlist + active/load_state/enabled | reused as-is |
| `service.reload` | `systemctl reload <unit>` for nginx + pdns(-recursor) | new (M31 follow-up) |
| `system.network` | per-iface state + bps + pps + errors | new |
| `system.processes` | total/running/sleeping/zombie + top-N by RSS | new |
| `system.cpu_usage` | aggregate + per-core busy% + iowait%, delta-cached | new |
| `system.service_details` | per-unit memory/tasks/uptime + UnitFileState via one `systemctl show` | new (UnitFileState added in alert-refinement follow-up) |
| `system.user_slices` | per-user cgroup v2 metrics from `/sys/fs/cgroup/jabali.slice/jabali-user.slice/jabali-user-*.slice` | new (M31 follow-up) |

`system.network` and `system.cpu_usage` keep an in-memory previous
sample per agent process so they can compute rates from the next call's
counter delta. First call after agent boot returns `warming_up=true`
with zero rates so the UI doesn't render a misleading 0 B/s.

### 4. Unit allowlist enforced agent-side

`system.service_details` validates every requested unit against
`AllowedServices()` from `service_list.go`. A malformed panel-api
request can't introspect arbitrary systemd units even if the panel-api
side were compromised. Same boundary protects the existing service
lifecycle commands.

### 5. Alert synthesis lives panel-api-side, not agent-side

Threshold rules (disk > 80% warning, > 95% critical; load > cores × 2
warning; service `failed` → critical; service `inactive` → critical
**only when** `UnitFileState ∈ {enabled, enabled-runtime, static, alias}`,
suppressed otherwise) live in `synthesizeAlerts`. The agent ships raw
numbers; the panel decides what they mean. Lazy-started units (e.g.
`jabali-webmail` boots on the first domain.email_enable) stay disabled
until needed, so flagging their inactivity as critical would paint a
permanent red banner on hosts with no mail domains. This keeps
threshold-tuning a one-place edit + lets the same agent serve multiple
panel versions with different rule sets.

Step 1 ships a minimal rule set; Step 4 will extend with service-
specific link-out alerts (e.g. "system updates pending" → deep link to
`/jabali-admin/updates`).

### 6. Per-call response carries `as_of` timestamp

`system.info` doesn't ship one (it's the canonical "now-state" call;
caller stamps the envelope with its own `as_of`). `system.network` and
`system.cpu_usage` DO carry their own `as_of` so the UI can detect a
sub-call that timed out vs. one that returned cached-but-stale data.
The envelope-level `as_of` is the panel-api's wall-clock at handler
start.

### 7. No DB writes; no migration

Pure read-through. State is in-memory in the agent (delta caches,
small) and ephemeral on the panel-api side. Survives an agent restart
gracefully — first call after restart sets `warming_up=true` and
recovers on the next.

## Consequences

- **Pro:** one round-trip per dashboard refresh.
- **Pro:** sub-call failures are visible (errors map + warning alerts)
  instead of hidden under a generic 500.
- **Pro:** allowlist + agent-side validation keep the audit surface
  small even though the page surfaces a lot of host detail.
- **Con:** 5s timeout is conservative. A genuinely overloaded host
  (load > 50, scanning /proc takes 6s) will keep flagging
  `processes: timeout` until pressure drops. Acceptable — the UI
  surfaces it as a warning rather than a hard failure.
- **Con:** delta-cache state lives in the agent process. Restart =
  one warming_up cycle (~5s on the next call). UI handles it.

## Verification

- `go test ./panel-agent/internal/commands/...` covers the parsers
  (`/proc/stat`, `/proc/net/dev`, `/proc/<pid>/stat,statm,status`,
  `systemctl show` output).
- `go test ./panel-api/internal/api/...` covers RBAC + happy-path +
  timeout-handling on the aggregator.
- Live-VM smoke deferred to Step 6 once the UI shell exists.

## Open

- **Queue stats** (mariadb / nginx / stalwart). UI ships placeholder
  card; aggregator extension lands in M31.1.
- **Pending-updates + crowdsec alerts** (currently surfaced via the
  Updates card; not synthesized into the AlertsBanner yet).
- **Historical charts** — out of scope (see plan §Out of scope).
- **Per-process CPU%** — deferred (needs two `/proc/<pid>/stat` samples
  + per-pid state in the agent).

## Layout addendum (Masonry refactor)

The original cut placed a single full-width banner card at the top
(`HostHeaderCard`) with everything else in `<Row>/<Col>` below.
Operators wanted a categorised "system information" table next to a
compact services control card, plus the option to add per-user slice
metrics without forcing matched row heights.

Switched to AntD `<Masonry columns={{xs:1,sm:1,md:2,lg:3}}>` over an
`items` array. Caveat: AntD Masonry only renders nodes from
`items[].children` — children passed between `<Masonry>` tags render
as nothing, which shipped a black page in the first cut. The fix is
documented in the file header so the next refactor doesn't repeat it.

Cards added in this layout pass: `SystemInfoCard` (categorised
hostname/OS/kernel/CPU/memory table with Tag chips for category),
`ServicesSummaryCard` (Service / status icon / inline Restart-or-
Reload action behind Popconfirm), `UserSlicesCard` (per-user CPU% +
memory + tasks). `HostHeaderCard` and the heavier `ServicesGrid` were
deleted — both were superseded.

## JAB-373 addendum (in-process cache, Health/Full projections, stale-serve)

The single-aggregator decision above held, but every `GET
/admin/server-status` still fanned out all eight agent commands on every
request. The UI polls every 5 s while a Server Status or Dashboard tab is
foreground, and the admin header health badge mounts on *every* admin page at a
30 s cadence, so separate tabs and operators multiplied identical host work
(≈960 agent calls/hour per foreground header; ≈5,760 per open Server Status
tab). JAB-373 keeps the one-envelope contract and adds a cache Seam behind it
rather than an endpoint-per-slice or a push transport (SSE/WebSockets were
explicitly rejected — the polling model and the single REST envelope stay).

### In-process per-slice cache + singleflight (#1272)

An in-process `statusCache` sits between the aggregator and the agent. Each
slice has its own TTL; a request inside a slice's TTL serves the last snapshot
with zero agent calls, and concurrent misses for the same slice collapse to one
refresh (process-wide singleflight). The refresh runs on a detached background
context bounded by its own per-call deadline, so a cancelled poller can't abort
a refresh other pollers are blocked on. Volatile slices keep a short TTL;
host/unit state can be longer; the software inventory keeps its five-minute
cadence.

### Fixed Health projection (#1474)

`GET /admin/server-status?view=health` returns a fixed light projection that
omits the three expensive slices the header never renders — `system.processes`
(walks every PID with a 200 ms sample), `system.software`, and
`system.user_slices` — while keeping the cheap host/services/network/apparmor/
nginx slices the synthesized `alerts` derive from. The header badge uses this
projection on a **distinct** query key (`["admin","server-health"]`), not the
shared full key, because the Dashboard and Server Status page need the full
envelope. When both are mounted the per-slice cache already has the cheap slices
warm from the 5 s full poll, so the projection adds one light round-trip rather
than a second fan-out. The module, not each adapter, owns this coherent
observation policy — a deeper Seam than a caller-supplied include list.

### Stale-serve with a display-only invariant (#1755)

On a refresh failure (a superset of the AC's "timeout" — any error, including
connection-refused during an agent restart), a slice within `ttl + maxStale`
(`maxStale = 2m`) serves its retained last-good body flagged `stale`; beyond that
window it is abandoned and the error surfaces as before. The envelope gains an
additive per-slice `meta` map (`SliceMeta{observed_at, stale, error}`), populated
for every served slice.

**Invariant — stale data is display-only.** The aggregator feeds
`synthesizeAlerts` a *fresh-only* map (freshly fetched slices), never the results
map that now also carries stale bodies, so a stale snapshot can neither raise a
phantom outage nor mask a real one. `errors[slice]` now means "last refresh
failed" with the slice still present (previously an error implied the slice was
absent); `meta[slice].stale` means "this body is last-good". The panel-ui
`StaleSlicesNotice` on the Server Status page consumes `meta.stale` to name the
slices showing last-known values (with each one's `observed_at` on hover); it is
likewise display-only and never derives an alert.

### Metrics (#1755)

`statusCache` records per-slice counters — hit, miss, refresh, stale-serve, and
refresh latency (count / total / last / max ms) — exposed read-only at `GET
/admin/server-status/cache-metrics` (admin-only). Refresh latency is measured on
an injected clock so tests are deterministic; do not "fix" it to `time.Now`, the
seam is intentional. Demo redaction drops `meta` (fail-closed allowlist rebuild)
by design.

### Remaining

- **TTL tuning from live metrics** (migration step 5) — the per-slice TTLs ship
  at conservative defaults; tuning them wants real hit/miss/latency data from a
  production host, so it is an operator-data step, not a code change.
- **Prometheus `/metrics` export** of the cache counters is a separate
  maintainer call; the JSON `cache-metrics` endpoint is the current surface.
