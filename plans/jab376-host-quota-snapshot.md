# JAB-376 — Host Quota Snapshot (module core; alerts slice already shipped #1266)

**State:** Migration step 4 (alerts read the persisted snapshot, no per-user agent call) shipped in **#1266** (`f9c4bece8`). Remaining = steps 1–3 + 5: the **batch quota inventory** that replaces the sweeper's per-user `quota -u <user>` loop. Ticket is a module-parent — this PR builds the core; parent stays OPEN for benchmarks/ADR addendum.

## Problem (verified in code)
- `diskusagesweeper/sweeper.go`: `SweepOnce` loops every linux user, calls `user.limits.report` per user (`reportOne`, agent `quota -p -w -u <user>`), waits `InterUserDelay = 250ms` between each, and persists one SQL per user (`Users.UpdateDiskUsage`). At 5,000 users the 250 ms pacing alone is ~20 min — past the 15-min cadence before command/DB time.
- `disk_quota.go` alert source already reads the persisted rows (#1266) — so the ONLY remaining per-user agent fan-out is the sweeper itself.
- `repquota` is used nowhere yet.

## Build
**1. Agent — `system.quota_inventory`** (new command). Runs `repquota -O csv <mount>` ONCE for the **explicitly resolved** quota mount (ADR-0032 — never all-filesystems), returns `[]{username, used_kb, limit_kb}`.
   - `repquota -O csv` reads the kernel quota state via quotactl, so it is **filesystem-agnostic** (one path covers ext4 AND XFS) and emits fixed columns — no separate XFS adapter/seam is needed, and the human-format grace-column shift is avoided. Validated against real `repquota -O csv /` output from the .60 box (ext4, quota-tools 4.06).
   - block counts (BlockUsed idx 3, BlockHardLimit idx 5) are 1-KiB.
   - Deterministic handling (AC): unknown/`#uid` users skipped, users without quota = absent (not 0-as-real), duplicate usernames last-wins-logged, partial/garbled output → return what parsed + a `partial` flag (never a fabricated 0).
   - `runCmdFn` exec-seam for fixture tests. No `du`, no cgroup reads (this is quota-only).

**2. Panel — Host Quota Snapshot consumer.** `Observe(ctx, mount) (map[string]Row, observedAt, error)` calls the agent once. The sweeper maps usernames → owned accounts, and batch-persists:
   - New repo `Users.BatchUpdateDiskUsage(ctx, []{id, usedKB, limitKB}, checkedAt)` — chunked (e.g. 500/stmt) `INSERT … ON DUPLICATE KEY UPDATE` or `CASE` update, ONE txn, **never touches `users.updated_at`** (rename/audit-noise + the row-ceiling class).
   - A username in the inventory with no owned account → skipped. An owned account absent from the inventory → left at last-good (not zeroed).

**3. Sweeper switch + loop removal.** `SweepOnce` = one `Observe` + one batch persist. Delete the per-user loop + `InterUserDelay`. Keep the manual on-demand `measure_disk`/`du` path and the detailed per-user `user.limits.report` UNTOUCHED (different cost/freshness — AC).

## Tests (unit, no box)
- ext4 + XFS **fixture parity**: canned `repquota` outputs → identical normalized rows.
- Edge cases: `#uid`/unknown user skipped, no-quota absent, duplicate username, partial output → `partial` + parsed subset, empty mount.
- Batch persist **statement budget**: 1 inventory call + ⌈N/chunk⌉ statements (assert bounded, scale-invariant 100 vs 5,000); assert `updated_at` untouched (sqlmock/real-DB column check).
- Staleness/last-good: an `Observe` error retains prior rows + timestamps; sweeper logs, doesn't zero.
- Sweeper: N-user sweep issues exactly ONE agent call (fake agent counts).

## Box validation (.60, ext4)
`repquota -p -O csv <mount>` real output → confirm parser matches; verify a known account's used/limit matches the old per-user path; confirm quota enabled on the mount. XFS parity is fixture-only unless an XFS box exists.

## Risks / watch-items
- **repquota needs quota ON for the mount** — if `quotaon` isn't active, repquota errors; must fall back to last-good + alert, NOT zero every account (a false "0 used" would clear real quota alerts). Characterize before switching.
- **util-linux version drift** in repquota output columns → pin the `-O csv` machine format if available; fixture the exact version on the fleet.
- **Fleet-wide blast radius** — the sweeper runs on every box; wrong parsing = wrong quota alerts everywhere. Box-validate before merge; keep the per-user path revertable one release.
- **`updated_at` invariant** — batching must not bump it (breaks rename/audit + risks the string-col row ceiling if done naively).

## Staging (one PR)
(1) characterize current parse + persist (golden). (2) agent `system.quota_inventory` + parser + fixtures. (3) `BatchUpdateDiskUsage` repo + budget test. (4) sweeper switch + loop removal. (5) box-validate .60. Parent OPEN for benchmarks table + ADR-0032 addendum.
