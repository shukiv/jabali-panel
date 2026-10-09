# ADR-0148: Multi-tenant Redis ACL model for the WordPress cache

**Status:** Proposed (2026-06-23)
**Supersedes/extends:** ADR-0059 (Redis as shared local cache/queue)
**Driven by:** Issue #406 / `plans/m-wp-cache-switch.md` Phase 0 — blocking gate for the
per-app WordPress cache switch.
**Review:** pending operator + security-reviewer sign-off before any code lands.

---

## Context

The `jabali-wp-cache` plugin (embedded at `wp-plugins/jabali-wp-cache/`, commit
`9dd6aad7`) is a WordPress Redis object/page cache. To use it, a tenant's PHP-FPM
process (running as the tenant uid) must connect to the panel Redis. ADR-0059
provisioned exactly one Redis: a unix-socket instance at `/run/redis/redis.sock`,
**no AUTH**, `db 0` for the M14 notification dispatcher and `db 1` reserved for WP.

### Live finding (2026-06-23, test host)

```
$ stat /run/redis/redis.sock
srw-rw---- redis:jabali-sockets   # 0660
$ redis-cli -s /run/redis/redis.sock ACL GETUSER default
flags: on nopass ~* &* +@all      # no password, full access, all keys, all channels
```

Two facts make the naive path unsafe:

1. **`jabali-sockets` is the wrong group to put tenants in.** That group is the
   access boundary for *every* privileged local socket, not just Redis:

   ```
   /run/jabali/agent.sock          ← root command-execution channel (!!)
   /run/jabali-kratos/admin.sock   ← identity admin API
   /run/mysqld/mysqld.sock         ← MariaDB
   /run/jabali-bulwark/bulwark.sock, /run/stalwart-*.sock, /run/jabali-panel/*.sock …
   ```

   Adding a tenant to `jabali-sockets` to reach Redis would also hand them the
   **root agent control socket**. Non-negotiable: tenants must never be in
   `jabali-sockets`.

2. **The `default` user is `nopass +@all`.** Any process that can open the socket
   gets full read/write/`FLUSHALL` over *all* tenants' cache **and** the panel's
   notification streams (`jabali:notifications:*`). So "just grant socket access"
   = full cross-tenant and panel-data compromise. **Prefix-only isolation is
   rejected** (the plugin's own prefix scoping is a namespacing convenience, not a
   security boundary, once the socket is reachable with the default user).

We need real, enforced, per-tenant isolation on the shared instance — without a
second Redis process, and without breaking the M14 dispatcher.

---

## Decision

Adopt **Redis 7 ACLs** as the enforcement boundary, with a dedicated client group
for socket reachability and the `default` user locked. Three layers:

### 1. Socket reachability — a new `jabali-redis-clients` group

- Create system group `jabali-redis-clients`.
- The Redis socket becomes `redis:jabali-redis-clients` `0660` (was
  `redis:jabali-sockets`). Update `unixsocketperm`/`ExecStartPost` chgrp in the
  install.sh drop-in accordingly.
- Members: `jabali` (panel-api, for the dispatcher connection) and **each tenant
  OS user, added only when that tenant first enables WP caching** — never by
  default, never `jabali-sockets`.
- Socket-group membership only grants the *right to attempt a connection*. With
  `default` locked (below), connecting grants nothing without a per-user token.

> Rationale for a group rather than `0666`: keeps the socket out of reach of
> unrelated daemons/users entirely; group + ACL is defense-in-depth, matching the
> M25 socket-hardening pattern (ADR-0050).

### 2. Lock the `default` user

In the persisted ACL file: `user default off nopass ~* resetchannels -@all`
(equivalently `ACL SETUSER default off`). After this, opening the socket with no
AUTH yields `NOAUTH`/`NOPERM` for every command. Socket access alone is inert.

### 3. Named ACL users, persisted via `aclfile`

Switch Redis to an external ACL file: `aclfile /etc/redis/users.acl`
(`0640 redis:redis`). install.sh seeds it; runtime changes go through
`ACL SETUSER … ; ACL SAVE`. Three principal classes:

**a. Dispatcher — `jabali_dispatcher`** (panel-api / M14):
```
user jabali_dispatcher on >$DISPATCHER_TOKEN \
     ~jabali:notifications:* resetchannels +@all -@dangerous +@connection
```
Scoped to the notification keyspace only; stream commands (`XADD`, `XREADGROUP`,
`XGROUP`, `XACK`, `XAUTOCLAIM`) are in `+@all -@dangerous`. It can touch nothing
under `jc:*`. Token from `/etc/jabali/panel.env` (see "M14 credentialing").

**b. Per-tenant — `wp_<osuser>`** (the plugin, tenant uid):
```
user wp_<osuser> on >$TENANT_TOKEN \
     ~jc:<prefix>:* resetchannels \
     +@read +@write +@keyspace +@connection -@dangerous
```
- `~jc:<prefix>:*` is an **allowlist** — the user can address *only* its own
  prefix. It therefore **cannot** read/write another tenant's `jc:<otherprefix>:*`
  nor the dispatcher's `jabali:notifications:*`. The cross-tenant/notification
  denial is automatic, not a blocklist we must maintain.
- `-@dangerous` removes `FLUSHDB`/`FLUSHALL`/`KEYS`/`CONFIG`/`ACL`/`INFO`/`DEBUG`.
  The plugin already never issues `FLUSHDB` (prefix-scoped `SCAN`+`DEL`; since
  plugin 1.2.0 a flush bumps a generation counter instead, ADR-0173); `SCAN`,
  `GET/SET/SETEX/DEL/MGET/INCRBY/DECRBY` remain permitted. `SELECT`/`PING`/`AUTH`
  via `+@connection`.
- The plugin receives `$TENANT_TOKEN` through its existing `JABALI_CACHE_PASSWORD`
  config knob (already implemented in `includes/lib.php` / `class-settings.php`).

**c. ACL admin — `jabali_acl_admin`** (panel-api, lifecycle only):
```
user jabali_acl_admin on >$ACL_ADMIN_TOKEN ~* resetchannels +@admin +acl +@connection
```
Used solely by panel-api to `ACL SETUSER`/`ACL DELUSER`/`ACL SAVE` when a tenant
enables/disables caching or is deleted. Token in `panel.env`, never handed out.

### Lifecycle (panel-api owns it)

- **On cache-enable** (`PUT /applications/:id/cache {enabled:true}`):
  1. generate a 256-bit `$TENANT_TOKEN`;
  2. (agent) add the tenant OS user to `jabali-redis-clients`;
  3. (panel-api, as `jabali_acl_admin`) `ACL SETUSER wp_<user> …` + `ACL SAVE`;
  4. dispatch `wordpress.cache_set` with `redis_password=$TENANT_TOKEN`, prefix, db.
- **On cache-disable:** deactivate plugin (agent); `ACL DELUSER wp_<user>` +
  `ACL SAVE`; optionally remove from the group (keep if other installs for that
  user still cache — see Open Questions).
- **On OS-user delete cascade:** `ACL DELUSER wp_<user>`, drop group membership.

### M14 dispatcher credentialing (the high-risk part)

The dispatcher today connects via `redis.ParseURL(cfg.Redis.URL)` with
`unix:///run/redis/redis.sock?db=0` and no credentials (`serve.go:122`). Once
`default` is locked it **must** authenticate as `jabali_dispatcher` or all
notifications break (XGROUP/XADD start returning `NOAUTH`).

Sequencing is load-bearing — credential the dispatcher **before** locking
`default`, in this order inside `install_redis()` / panel first-boot:

1. write `users.acl` with `default` still `on nopass` **plus** the three named
   users; `ACL LOAD`;
2. write `JABALI_REDIS_DISPATCHER_TOKEN` (+ acl-admin token) into `panel.env`;
   wire `cfg.Redis.URL` → `unix://jabali_dispatcher:$TOKEN@/run/redis/redis.sock?db=0`
   (or set `Options.Username`/`Password` after `ParseURL` — to be confirmed for
   the unix scheme; go-redis supports `Username`/`Password` fields regardless);
3. restart panel-api, **verify a test notification round-trips** (XADD→XREADGROUP);
4. only then flip `default off` + `ACL SAVE`.

Idempotent installs/`jabali update`: re-running must not rotate the dispatcher
token (would orphan the running panel until restart). Tokens are generated once
and persisted; install.sh reads-or-creates.

---

## Consequences

**Positive**
- Real per-tenant enforcement: a compromised/curious tenant can reach only its own
  `jc:<prefix>:*` keyspace; the panel notification streams and every other tenant
  are unreachable at the server, not just by convention.
- No second Redis process; one instance, one socket, bounded memory unchanged.
- Reuses the plugin's existing `JABALI_CACHE_PASSWORD` path — no plugin change.

**Negative / cost**
- New ACL lifecycle surface in panel-api + agent (create/delete users, group
  membership) — must be covered by the user-delete cascade or tokens leak.
- `aclfile` adoption means user definitions move out of `redis.conf`; the two are
  mutually exclusive. install.sh must own the file and never let a hand-edit and a
  runtime `ACL SAVE` fight (managed-file + `ACL SAVE` is authoritative).
- Highest-risk regression is M14 notifications; gated by the sequencing + a
  mandatory post-lock round-trip check (and a documented rollback: `default on`).
- `INFO` is denied to tenants (`-@dangerous`), so the plugin's admin "server info"
  degrades to empty for the tenant view; key counts use `SCAN` and still work.

**Neutral**
- `maxmemory-policy allkeys-lru` is unchanged; tenant keys can still be evicted —
  the plugin already treats every read as best-effort.

---

## Alternatives considered

- **Prefix-only isolation (ADR-0059 status quo + open socket).** Rejected per the
  live finding: the `default nopass +@all` user makes the prefix a namespace, not a
  boundary.
- **Per-tenant Redis instance/socket.** Real isolation but N processes, N sockets,
  N systemd units, per-tenant memory floors — operationally heavier than ACLs for
  no extra security over a correctly-scoped ACL user. Revisit only if ACL
  key-pattern scoping proves insufficient.
- **Per-tenant logical DB (`SELECT n`).** Rejected (also in the blueprint): 16-DB
  cap, and `SELECT` across DBs is not an isolation boundary.
- **TCP Redis with per-tenant auth.** Rejected: violates ADR-0050 (unix sockets
  only; `skip-networking`).

## Open questions for review

1. **Group-removal on disable:** if a tenant has multiple WP installs and disables
   one, keep them in `jabali-redis-clients` (other installs still cache) — drop
   only when their last cache-enabled install goes off, or on user delete. Confirm
   the bookkeeping lives in panel-api (count of cache-enabled installs per user).
2. **go-redis unix + AUTH:** confirm `ParseURL("unix://user:pass@/path?db=0")`
   populates `Username/Password`; if not, set them explicitly post-parse. (Verify
   against the pinned go-redis v9 before Phase 0 code.)
3. **Token storage for tenants:** the tenant token is written into the WP config
   file under the tenant's home (readable by the tenant — acceptable, it only
   unlocks that tenant's own prefix). Confirm we are comfortable with at-rest token
   in `wp-content/jabali-wp-cache-config.php` (mode 0640, tenant-owned).
4. **ACL admin from panel-api vs agent:** panel-api holds `jabali_acl_admin` and
   runs `ACL SETUSER` over the socket (it is already a `jabali-redis-clients`
   member). Alternative: the agent (root) does it. Proposed: panel-api, to keep
   the credential out of the agent and ACL logic next to the lifecycle state.

---

## Implementation note (2026-06-23) — Phase 0 SHIPPED + corrected

**Status: Accepted.** Phase 0 (this ACL model) is implemented + live-verified on
the test host (Ubuntu noble, Redis 7.0.15) and codified in `install_redis_acl()`.

**Correction to §3a — the panel user is NOT notifications-only.** panel-api uses a
**single** go-redis client across all its keyspaces: `jabali:notifications:*`
(dispatcher + DLQ + inbox + server-status XLEN), plus `jabali:audit:*`,
`jabali:login-wl-seen:*`, `jabali:session-seen:*`, `jabali:secret`, **and**
`automation:replay:*` (M44 HMAC replay-defense — note: NOT `jabali:`-prefixed).
Scoping it to `~jabali:notifications:*` as first drafted would NOPERM automation
replay-defense + audit. The shipped `jabali_panel` user is therefore:
```
user jabali_panel on >TOKEN ~jabali:* ~automation:* resetchannels +@all -@dangerous +acl +@connection +info
```
`+acl` lets the same connection run the per-tenant ACL lifecycle (`ACL SETUSER`/
`DELUSER`/`SAVE`), so panel-api needs no second admin connection — the separate
`jabali_acl_admin`/`jabali_dispatcher` split in §3 is collapsed into one trusted
control-plane user. Tenants remain tightly scoped (`~jc:<prefix>:*`).

`+info` was added later. `INFO` is in `@dangerous`, so the first line refused it,
and panel-api dropped the NOPERM: the WordPress cache diagnostic's
`evicted_keys` warning and the admin hit ratio / `used_memory` never appeared.
`INFO` reports server statistics only (no keys, values or credentials), and
`+acl` already lets this user change its own rules, so granting it moves no real
boundary. `CONFIG` and the rest of `@dangerous` stay denied. Fresh installs get
it from the ACL line; `converge_redis_panel_info_acl` in install.sh grants it on
existing hosts (`ACL SETUSER jabali_panel +info` + `ACL SAVE`, acting only on a
NOPERM reply), and panel-api now logs a failed INFO.

**Verified live (gated, default-lock survives restart):** default off → no-auth
PING = NOAUTH; panel reconnects as jabali_panel + dispatcher starts; a
`~jc:demopfx:*` tenant user gets OK on its own prefix and NOPERM on another
prefix, on `jabali:notifications:*`, and on FLUSHALL. Survives redis restart
(aclfile-persisted).
