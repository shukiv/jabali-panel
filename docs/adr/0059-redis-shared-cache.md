# ADR-0059: Redis as shared local cache/queue (unix socket, jabali-sockets group)

**Status:** Accepted (2026-04-24)
**Driven by:** Plan `plans/m14-notifications.md` Step 1 — folded in after the initial dispatcher draft (buffered in-memory channel) failed the restart-safety requirement. Operator also wants Redis for a future WordPress object-cache, so install-time provisioning instead of a "add Redis later" milestone.

## Context

Two concrete needs as of M14:

1. **Notification dispatcher queue** (ADR-0056): needs persistent FIFO + consumer-group semantics + in-flight visibility. Redis Streams give us that out of the box.
2. **WordPress object cache (planned, post-M14):** `wp-redis` + `redis-object-cache` are the standard accelerators for WP on a shared host; both use a Redis unix socket against a shared instance. One Redis install that serves both avoids a second provisioning step.

Running two Redis instances (one for the dispatcher, one per WP install) is unnecessary complexity — Redis supports multiple logical databases (`SELECT db`) which isolate keyspaces without process overhead. `db 0` for the dispatcher, `db 1` for WP (when it lands) is enough separation; different databases can't see each other's keys and `FLUSHDB` on one doesn't touch the other.

The install-time security model has to fit ADR-0050 (M25 unix-socket hardening). Every local service that can use a unix socket must; Redis supports them natively; no TCP listener should be open on a fresh panel. Socket permission + group membership is the cross-process boundary — same pattern already in place for Kratos, Bulwark, MariaDB, panel-api.

## Decision

Install **`redis-server`** (Debian repo, 7.x on bookworm and trixie) with a unix-socket-only listener at `/run/redis/redis.sock`, mode `0660`, group `jabali-sockets`. No TCP.

### `install_redis()` in install.sh

Drops `/etc/redis/redis.conf.d/10-jabali-socket.conf` (Debian's `redis.conf` includes `include /etc/redis/redis.conf.d/*.conf` — if absent on a particular variant, install.sh patches the main conf):

```
port 0
unixsocket /run/redis/redis.sock
unixsocketperm 660

maxmemory 128mb
maxmemory-policy allkeys-lru
appendonly yes
```

systemd drop-in at `/etc/systemd/system/redis-server.service.d/10-jabali-socket.conf`:

```
[Service]
RuntimeDirectory=redis
RuntimeDirectoryMode=0750
Group=jabali-sockets
ExecStartPost=/bin/chmod 0660 /run/redis/redis.sock
ExecStartPost=/bin/chgrp jabali-sockets /run/redis/redis.sock
```

`RuntimeDirectory=redis` gives Redis its own `/run/redis/` on every boot (systemd creates + tears down). Socket file lives in there; belt-and-suspenders `chmod/chgrp` in `ExecStartPost=` mirrors the M25 pattern (F-C-3 in ADR-0050) — `unixsocketperm` in `redis.conf` ideally sets it, the post-hook catches any edge case where Redis's own conf parse drifted.

### Security + group membership

- `redis` user (created by the package) gets added to `jabali-sockets` (the group that owns the socket file) so Redis's own `listen(2)` creates the socket with group perms visible to clients.
- Clients — `jabali` (panel-api), `www-data` (nginx-level WP FPM pools when WP cache lands) — are already in `jabali-sockets` per ADR-0050. No new group membership.
- `Group=jabali-sockets` on the unit sets the process's primary group to that — which interacts with memory `feedback_systemd_group_supplementary.md` (systemd `Group=` drops the primary group from supplementary unless paired with `SupplementaryGroups=`). For Redis the practical effect is narrow (Redis doesn't touch any `root:X` 0640 file), but the drop-in explicitly pins `SupplementaryGroups=redis` to be safe.

### Persistence + eviction

- `appendonly yes` (AOF). Dispatcher queue survives `systemctl restart redis-server`. AOF default fsync policy (`everysec`) is the right trade-off — at most 1 s of queue loss on an ungraceful crash, which is fine for notification events (the upstream event source will re-emit on the next reconciler tick).
- `maxmemory 128mb` + `allkeys-lru`. Notification queue is tiny (few KB); WP cache will push the needle when it lands. LRU eviction is safe for both workloads — the dispatcher's stream entries get explicitly XACKed + trimmed (not LRU'd), and WP cache is designed to tolerate eviction (any miss falls through to the DB).
- Memory note for operators: 128 MB is a starting floor. When WP cache load meaningfully grows, bumping `maxmemory` is a one-line drop-in override (a higher-numbered `/etc/redis/redis.conf.d/*.conf` file).
- **Update (sized to RAM):** 128 MB proved too small once WP object caching was live. A fleet box sat at 99.7% of it, with tenants' object caches evicting each other. `install.sh:size_redis_maxmemory` now writes `/etc/redis/redis.conf.d/15-jabali-maxmemory.conf` on fresh installs and on every `jabali update`:
  - `maxmemory` = RAM/16, rounded down to a 64 MB step, floor 128 MB, ceiling 1024 MB (4 GB → 256 MB, 8 GB → 512 MB, 16 GB and up → 1024 MB);
  - it loads after `10-jabali-socket.conf`, so it replaces the fixed 128 MB. An operator override numbered above 15 (for example `50-local.conf`) still wins;
  - applying it restarts `redis-server` once, only when the drop-in changes (root has no credential for `CONFIG SET`); AOF keeps the keys.
- The eviction claim above no longer holds for the notification stream. Under `allkeys-lru` a stream key is as evictable as any cache key once memory is full; XACK and trimming do not protect it. Sizing to RAM makes that much rarer but does not rule it out. Protecting the stream needs its own Redis instance (with `noeviction`) or another store, and is a separate change.

### Client wiring

- `github.com/redis/go-redis/v9` in `go.mod`.
- `serve.go` constructs a single `*redis.Client` with `Options{Network: "unix", Addr: "/run/redis/redis.sock", DB: 0}`. Pinged on boot; non-ping is fatal. `Requires=redis-server.service` + `After=redis-server.service` drop-in on `jabali-panel.service` guarantees Redis is up before the panel starts.
- The client lives on the application context (`app.Redis`) so Step 2's dispatcher, any future cache layer, and future WP-integration code all pull from the same connection pool.

### What Redis is NOT used for (M14 scope)

- No session store. Kratos holds sessions in its own SQLite DB; not moving that.
- No rate-limit cache (future — CrowdSec-style counters would fit; not shipping now).
- No cross-panel coordination. Panel is single-node; if HA ever happens, Redis is already a natural fit but the decision is explicitly deferred.

## Alternatives considered

**Keep the in-memory buffered channel for M14; add Redis later when WP cache lands.** Rejected: the restart-safety problem is blocking for production use of notifications, and adding Redis as a second cutover is riskier than doing it once now.

**Use MariaDB as the queue (poll a `notification_history` WHERE `outcome='pending'` table).** Rejected: latency floor is the poll interval; no equivalent of consumer-group + XCLAIM without reimplementing it by hand; locking at high alert rates (disk-full cascades can emit 10s of events in a second) would hit MariaDB row-lock waits.

**TCP-only Redis on `127.0.0.1:6379`.** Rejected: violates ADR-0050. Unix sockets are free; TCP adds an attack surface that `skip-networking` specifically exists to close.

**Separate Redis instances for dispatcher vs WP cache.** Rejected: two processes, two configs, two systemd units, ~20 MB extra RSS minimum, no security benefit (same host, same operator, same data classification). One instance with two logical DBs is the textbook answer.

## Consequences

**Positive:**
- Dispatcher gets restart-safe FIFO for free.
- Future WP object-cache ships without another install milestone.
- Consistent with existing unix-socket security model (ADR-0050) — no new exception.
- Package is in the distro repo; no vendoring / custom build.

**Negative:**
- One more systemd dep for panel-api. If Redis itself goes bad (corrupted AOF, disk full), the panel refuses to start. Recovery runbook (in the M14 runbook, written during Step 8) documents AOF rebuild from RDB fallback.
- 128 MB RSS footprint on idle panel. Small on VPS specs M14 targets (1 GB+), but not zero.
- `redis-cli` access is admin-only via unix socket — operators used to `redis-cli -h 127.0.0.1` need the `-s /run/redis/redis.sock` flag. Documented in the runbook.
- Two logical databases (`db 0`, `db 1`) need discipline: dispatcher code must always pass `DB: 0` on client construction, WP cache must always pass `DB: 1`. Misuse (e.g. dispatcher accidentally connecting to `db 1`) would silently write to the wrong key-space. Mitigation: each caller constructs its own `*redis.Client` with the DB pinned; no shared "figure it out from the env" helper.

## Related

- Plan: `plans/m14-notifications.md` — Step 1 install tasks, Step 2 dispatcher consumer
- ADR-0050: M25 unix-socket hardening — same security pattern, same group
- ADR-0056: Dispatcher — the primary consumer of `db 0`
- ADR-0002: DB as truth — Redis is transport + cache; MariaDB is still the record of truth
- Memory: `feedback_systemd_group_supplementary.md`, `feedback_install_sh_is_truth.md`
