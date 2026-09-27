# ADR-0084 — Per-user PHP-FPM egress firewall via nftables + cgroupv2 socket match (M34)

**Status:** ACCEPTED — live end-to-end smoke 2026-05-07 on
192.168.100.150 (Debian 13 / kernel 6.12.74 / nftables 1.1.3).
Inserted `user_egress_policies(state=enforced)` for an existing
PHP-FPM-bound user, reconciler tick (≤60s) populated the
`cgroup_to_chain` vmap and rendered the per-user enforced chain.
Outbound `curl` from the user's slice via `systemd-run --uid=… --slice=…`:
  - port 4444 (not in default-allowed) → connection timed out;
    `user_<u>_drops` counter incremented from 0 → 2 packets / 120
    bytes (SYN+retry both dropped).
  - port 443 (in default_ports_tcp) → HTTP 301 from 1.1.1.1 (allowed).
Match validates the cgroupv2-socket-match path end to end. Original
PROPOSED status pending live smoke is now satisfied.
**Date:** 2026-04-29
**Supersedes/extends:** ADR-0054 (UFW over iptables), ADR-0068 (per-user cgroup v2 slice metrics)

## Context

After M33 amendment 3 (ClamAV removed; maldet 2.0 native HEX/MD5/SHA-256 +
yara-x as the on-disk scanner), one residual gap stands: **PHP runtime
exfil**. A webshell that lands inside a user's docroot — past every
filesystem-based scanner — can still phone home or pull a second-stage
payload at execution time via PHP network APIs (fsockopen, curl, or any
of the dozens of stream wrappers PHP exposes).

Two PHP-runtime hardening paths were evaluated to close this gap:

- **A. Snuffleupagus (PHP extension).** Inline runtime guard, popular
  with hosting providers, blocks dangerous functions and patterns at
  PHP-VM level. **Operator FP intolerance:** the panel operator runs
  diverse user code (WordPress + Composer + custom apps) and rejected
  this in M9 because the false-positive rate forces repeated
  per-tenant allowlisting. Bypassable via PHP-extension exec or
  LD_PRELOAD before the extension boots.
- **B. Suhosin.** Dead upstream; last release predates PHP 8.0.
  Rejected on supportability.
- **C. eBPF cgroup_skb program.** Stronger than Snuffleupagus
  (kernel-layer, no PHP-extension bypass), but adds a long-lived BPF
  program to the install surface and a tooling dependency
  (`bpftool` / `libbpf` / Go bindings). More moving parts than the
  alternative below.
- **D. nftables `socket cgroupv2 level 2 "user-<u>.slice"` match.**
  Native nftables matcher (kernel ≥ 5.7, Debian 13 ships ≥ 6.1 — already
  required). Per-user cgroup v2 slices already exist (M18 + ADR-0068).
  Zero new daemons, zero new compiled artifacts; one extra reconciler
  pass that renders `/etc/nftables.d/jabali-per-user-egress.nft`.

**D wins.** ROI is highest, blast radius is lowest, install surface
unchanged.

## Decision

1. **One nftables `inet` table `jabali_per_user`** owned by the panel.
   `nft -f` reload only — never `systemctl restart nftables` (would
   flush other tables we do not own, e.g. CrowdSec blocklists).
   Coexists with the existing UFW + CrowdSec tables (ADR-0054).

2. **vmap dispatch by cgroup path** for O(1) per-user routing. One
   `map cgroup_to_chain { type cgroupsv2 : verdict }` keyed on the
   level-2 slice path (`user-<u>.slice`). At 1000 users this is
   constant-time, vs O(N) linear matches in the naive form.

3. **Three policy states** per Linux user, stored as an ENUM in
   `user_egress_policies.state`:
   - `off` — no chain emitted; the slice falls through to the default
     accept policy. Used for break-glass.
   - `learning` — would-drops are logged (rate-limited 5/min) and
     counter-bumped, but accepted. The Step 8 timer auto-flips
     `learning` rows older than 7 days to `enforced`.
   - `enforced` (default) — would-drops are counter-bumped + dropped
     at the kernel packet layer.

4. **Default allowlist** (must include or stuff breaks):
   - **Loopback4:** 127.0.0.0/8 + 10.0.0.0/8 + 172.16.0.0/12 +
     192.168.0.0/16 — covers MariaDB / Redis / php-fpm sockets that
     drift between unix and 127.0.0.1 DSN configs.
   - **Loopback6:** ::1/128 + fc00::/7 + fe80::/10 — IPv6 parity for
     the same drift class.
   - **TCP 53/80/443/587/465/25** — DNS + WordPress auto-update +
     Composer + apt + SMTP submission via mail(). Removing :443 would
     silently break wp-cron updates → operator support load.
   - **UDP 53** — DNS resolver fallback.
   Operators who want to harden defaults (drop :25/:465/:587 on a
   no-mail-from-PHP host) edit them in Server Settings → Security
   (M34 Step 6); changes take effect within one reconciler tick (~60 s).

5. **Per-user extras** in `user_egress_policies.allowed_extra` (JSON
   array). Admin sets directly; user submits requests via
   `/me/egress/request` and admin approves via
   `/admin/egress-requests/:id/approve`.

6. **DB-as-truth** (ADR-0002). Two new tables:
   - `user_egress_policies` (one row per user; state, allowed_extra,
     denormalised drop_count_24h, learning_started_at).
   - `user_egress_requests` (queue, status pending/approved/denied).
   The reconciler is the only writer to `/etc/nftables.d/`.

7. **Per-user drop counter** (`counter user_<u>_drops`) exposed via
   `nft -j list counters` and read into
   `user_egress_policies.drop_count_24h` every reconciler tick. M14
   event source `egress_drop_burst` fires when a single user crosses
   `server_settings.egress_burst_threshold` (default 50) drops/minute.

8. **LEARNING auto-flip** for upgrading hosts. Existing hosts
   (`/etc/jabali/installed` already present at install time) start in
   LEARNING for 7 days, anchored on
   `user_egress_policies.learning_started_at`. A daily systemd timer
   (`jabali-per-user-egress-flip.timer`) runs the
   `jabali per-user-egress flip-mature` CLI. Operator pin via
   `/etc/jabali/per-user-egress.mode = learning` halts the auto-flip.
   (Superseded 2026-09-27: the pin is `/etc/jabali/per-user-egress.pin`;
   see the amendment at the end.)
   New installs default to ENFORCED with the default allowlist.

## Consequences

**Wins**
- Webshell-phone-home becomes a kernel-level drop, not a userspace
  filter. PHP-extension bypass paths (LD_PRELOAD, in-process payload
  obfuscation) cannot evade a packet-layer drop.
- Zero install surface increase: nftables, cgroup v2 slices, and the
  reconciler pattern are all already on the host.
- Operator FP rate is approximately zero on a stock LAMP workload —
  the default allowlist covers MariaDB / Redis / DNS / HTTP/S / SMTP
  submission.
- Per-user signal (`egress_drop_burst`) gives operators a
  high-confidence "this user is infected" alert without per-user
  payload inspection.

**Costs**
- One additional reconciler pass and one nft reload per tick (~60 s
  cadence).
- Operators must learn the LEARNING / ENFORCED model; the runbook
  (Step 8) covers the common workflow.
- IPv6 outbound is allowed today only via the loopback6 default + any
  per-user extras; full IPv6 egress policy parity is a phase 2
  blueprint.

**Trade-offs explicitly accepted**
- Per-user, not per-pool. Some users run multiple PHP-FPM pools at
  different trust levels; pool-level scoping is deferred until concrete
  operator demand. Slice-level granularity matches the existing M18
  topology and is what the cgroup match natively keys on.
- No payload inspection. That is CrowdSec AppSec's job at the HTTP
  layer (M27). nftables drops the SYN; it does not inspect bytes.

## Reservations / phase 2

- **open_basedir + disable_functions PHP-FPM hardening** — separate
  blueprint, simpler scope, can run in parallel with M34.
- **Tetragon companion policy** for L3 audit — Step 9 (deferred). Belt
  and braces for the rare kernel-bug or mis-slice-attribution case.
- **IPv6 full coverage** — beyond loopback6.
- **Per-pool granularity** — slice-level scoping per ADR-0068 amendment.
- **Egress rate-limiting (vs binary drop)** — nftables `limit rate`
  could throttle suspicious egress; phase 3.

## Live evidence

VM 192.168.100.150 (Debian 13, kernel 6.12.74, nftables 1.1.3) — 2026-04-29:

1. **Slice topology probe.** `systemctl list-units 'user-*.slice'` confirms
   numeric `user-<UID>.slice` at level 2 of /sys/fs/cgroup. Real PHP-FPM
   pool processes resolve via `/proc/$(pgrep -f 'php-fpm: pool' | head -1)/cgroup`
   to `0::/jabali.slice/jabali-user.slice/jabali-user-<USERNAME>.slice/jabali-fpm@<USERNAME>.service`,
   confirming the M18 nested hierarchy at depth 3 — original blueprint
   sketch (`level 2 + user-<UID>.slice`) would have failed parse on
   first apply. Renderer corrected to `level 3` + full path.

2. **nftables vmap parse.** `nft -c -f -` accepts the rendered file for
   synthetic users {shukivaknin/enforced, test1/learning, test2/off}
   with vmap key `"jabali.slice/jabali-user.slice/jabali-user-shukivaknin.slice"`
   and `socket cgroupv2 level 3 vmap @cgroup_to_chain`. Bare
   `"user-1001.slice"` fails parse (`No such file or directory` —
   nftables validates cgroupv2 paths against /sys/fs/cgroup at parse
   time).

3. **Verification commands** (M34 runbook reproducible smoke):
   ```
   ssh root@<host>
   nft list table inet jabali_per_user
   tcpdump -i any 'tcp[tcpflags] & tcp-syn != 0 and host 192.0.2.1'
   # plant fsockopen('192.0.2.1', 4444) test in user docroot
   curl https://<user-domain>/shell.php
   # tcpdump shows ZERO outbound SYN under ENFORCED
   nft -j list counters table inet jabali_per_user | jq .
   # user_<U>_drops counter increments per attempt
   ```

## Amendment 2026-09-27 — dispatch by socket owner as well as cgroup

**Gap.** The cgroup dispatch only sees processes inside
`jabali-user-<user>.slice`. An SSH login is placed by logind in
`user.slice/user-<uid>.slice/session-N.scope`, so a tenant shell (and anything
it starts) fell through to the output chain's
`policy accept`: any port, and cloud metadata, while the same tenant's PHP was
filtered. Observed on the test box: a drill tenant's shell ran in
`session-19890.scope` and reached `1.1.1.1:853` with the tenant enforced.
Moving the session into the slice from a PAM hook (as JAB-263 does for vsftpd)
was rejected: logind owns `session-N.scope`, and moving the process out breaks
session tracking. Matching `user.slice/user-<uid>.slice` was rejected too: the
path exists only while the user is logged in, and nft rejects the whole file
for a missing path.

**Decision.** The output chain also dispatches by socket owner:

```
set tenant_uids { type uid; elements = { … } }            # every user, state=off included
map uid_to_chain { type uid : verdict; elements = { <uid> : jump user_<u>_<state>, … } }

chain output {
  <slice-scoped SSRF floor>
  meta skuid @tenant_uids ip daddr 169.254.0.0/16 … drop  # uid-scoped SSRF floor
  meta skuid @tenant_uids ip6 daddr fe80::/10 … drop
  socket cgroupv2 level 3 vmap @cgroup_to_chain
  meta skuid vmap @uid_to_chain
}
```

- The per-user chains are unchanged; only the dispatch is added. A process in
  the slice is matched by its cgroup first, and every user chain ends in a
  verdict, so it never reaches the uid map.
- A map, not a rule per tenant (same reason as `cgroup_to_chain`): a packet from
  root, nginx or MariaDB takes one lookup.
- Accepted sockets keep the listener's uid, so sshd's own connection (root
  listener) is not matched; outbound sockets the tenant's sshd child opens for
  a permitted forward are.
- Only uids `>= minTenantUID` (1000) are matched, so a panel username that
  resolved to a system account never filters that daemon. A uid two users
  share is rendered once (a duplicate map key would make nft reject the whole
  file); the apply response's fail-open report uses the renderer's predicate.
- The GH #708 missing-slice fallback is now the same map.

**Consequence.** The per-package `egress_ssh_out` / `egress_icmp` allowances
(GH #1798) now govern shells too: without them, `ssh` / `git@github.com` and
`ping` from a shell are dropped for an enforced tenant. Learning-state tenants
with heavy shell use will log more would-drops.

## Amendment 2026-09-27 (2) — every hosting user gets a policy row; the pin is its own file

**Gap 1: most tenants had no policy.** The renderer only sees users with a
`user_egress_policies` row. Nothing created one when a user was created: the
repository comment said a user-create hook called `EnsureDefault`, but no such
hook existed. The only callers were the admin and tenant Egress page handlers.
A tenant whose Egress page was never opened had no allowlist and no uid
dispatch. Their PHP was covered only by the slice-scoped SSRF floor, and their
shell by nothing.

**Gap 2: the upgraded-host soak never ended.** On a host that predated M34,
install.sh wrote `learning` into `/etc/jabali/per-user-egress.mode`, and
`ReadEgressPin` read that same file as the operator pin. So the installer's
default was an indefinite hold, and on those hosts the nightly flip never
promoted a row. Nobody noticed because gap 1 meant there were almost no rows.

**Decision.**

- Each reconciler tick first seeds a row for every hosting user that has none
  (`SeedMissing`: one `INSERT … SELECT … LEFT JOIN … WHERE p.user_id IS NULL`,
  users with a username; `ON DUPLICATE KEY` no-op). This happens before the
  tick lists policies. It covers every create path (API, CLI, billing,
  migration, restore) without a hook in each, and repairs existing boxes.
- The seed state comes from the mode file: `learning` on hosts that predated
  M34, `enforced` on fresh installs. A missing, unreadable or unknown mode file
  seeds `enforced` (fail closed). A learning seed stamps
  `learning_started_at` at seed time, so the 7-day soak runs from the release
  that seeds it, not from the host's install.
- The operator pin moves to `/etc/jabali/per-user-egress.pin` (contents
  `learning`). The mode file is only the seed default.

**Consequences.** At the release that ships this, every tenant gets a policy.
On hosts that predated M34, tenants are logged-only for 7 days and then
enforced. Apps using ports outside the allowlist (remote databases, custom
APIs) break on day 8 unless the operator adds extras or pins. The warning is
the would-drop log (`jabali-egress-learn-<user>`) and `egress_drop_burst`. On
hosts installed after M34, tenants are enforced at once. An operator who had
pinned by writing `learning` into the mode file must now write the pin file.
A user created on a host in learning mode also starts with a 7-day soak.
