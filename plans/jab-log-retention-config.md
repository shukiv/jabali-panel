# Blueprint: operator-configurable log retention (Server Settings → Logs)

Closes JAB-102 (migration rows), JAB-105 (audit_events), JAB-124 (bw_daily), and
makes the *existing* retention-sweep windows configurable instead of hardcoded.
Default **90 days** everywhere. Configured in **Server Settings → Logs**.

## Storage

`server_settings` is a single-row typed-column table (already has JSON fields).
Add one column:

- `log_retention JSON` — map of **category → days**. Empty/absent category → use
  the 90-day default. `0` = keep forever (disable pruning). Migration `000227`.

A single JSON column (not one column per table) keeps it extensible — new log
tables just add a category key, no schema churn.

## Categories (group the ~13 tables into operator-meaningful units)

| Category key | Tables | Default |
|---|---|---|
| `notifications` | notification_history | 90 |
| `updates` | update_history | 90 |
| `mail_reports` | dmarc_aggregate, tlsrpt_aggregate, arf_report | 90 |
| `security_events` | malware_events, snuffleupagus_incidents | 90 |
| `db_admin_audit` | db_admin_audit | 90 |
| `sessions` | log_access_streams, terminal_sessions | 90 |
| `migrations` | migration_jobs, migration_stages (**JAB-102**) | 90 |
| `bandwidth` | bw_daily (**JAB-124**) | 90 |
| `audit` | audit_events (**JAB-105**) | **0 (keep forever)** |

**Audit exception:** `audit_events` is hash-chained/tamper-evident. Its default
is **keep-forever (0)**, not 90d — a security log shouldn't silently self-delete.
An operator *can* set a window; when they do, pruning re-anchors the chain (see
below) rather than a blind DELETE. This is the one deviation from "90d default";
flagged for sign-off.

## Backend

1. **models/server_settings.go** — add `LogRetention json.RawMessage`; a typed
   accessor `RetentionDays(category) int` returning the configured value or 90
   (or 0 for `audit`).
2. **retention_sweep_cmd.go** — each `retentionTarget` gains a `category`; the
   window is read from server_settings via the accessor, not a constant. `0` →
   skip. Existing batched-DELETE loop unchanged.
3. **JAB-102** — new target: terminal `migration_jobs` (+ cascade
   `migration_stages`) older than the `migrations` window. Fold into the sweep
   (delete stages first for the FK). reap-secrets still owns secrets/staging.
4. **JAB-124** — new target: `bw_daily` WHERE `day < now - window`.
5. **JAB-105** — `audit_events` prune (only when window > 0): before deleting the
   aged tail, compute + persist an **anchor** (the row_hash of the newest deleted
   row) so the surviving chain stays forward-verifiable; then delete. Store the
   anchor in `server_settings.audit_chain_anchor` (or an `audit_anchors` row).
   `jabali audit verify` already exists — teach it to accept the anchor as the
   chain root.
6. **API** — `server_settings` GET/PUT already exist; extend the payload with
   `log_retention` (validated: ints 0..3650).

## Frontend

7. **settings/LogRetentionCard.tsx** — one numeric field (days) per category,
   default 90, `0` = "keep forever", with a tooltip per category and an explicit
   warning on `audit` (tamper-evidence). Wired into `ServerSettingsPage`.

## Tests

- retention_sweep: window resolved from settings (default + override + 0-skip).
- migration-rows + bw_daily prune windows.
- audit prune re-anchors + `audit verify` accepts the anchor.
- server_settings PUT validates the retention map.

## Phasing (PRs)

- **P1** storage + settings API + retention-sweep reads config (existing tables
  become configurable; default 90d) + UI card. No behaviour change at default
  except db_admin_audit 1y→90d (call out).
- **P2** JAB-102 migration rows + JAB-124 bw_daily prunes.
- **P3** JAB-105 audit_events anchor-prune + verify.

Ship P1 first (the "configurable in Server Settings" ask), then 102/124/105.

**STATUS 2026-07-19: all phases implemented in PR #503** (P1 config+UI, P2 102/124, P3 105 audit anchor). Flip JAB-102/105/124 to Done when #503 merges.
