# Audit Log

`/jabali-admin/audit`. Append-only structured record of every privileged mutation on the panel (ADR-0106).

## What is recorded

Every action that changes state writes one row. Each row contains:

- **Timestamp** — UTC.
- **Actor** — the user (administrator or tenant) who initiated the action; may be `system` for reconciler-initiated changes.
- **Subject** — the user being acted on (frequently the same as the actor, but distinct for admin-on-user actions).
- **Source** — `ui`, `cli`, `reconciler`, or `agent`.
- **Action** — namespaced dotted identifier (e.g. `domain.create`, `db.root.rotate`, `mailbox.passwd`, `ssl.issue`).
- **Target** — the resource identifier (domain name, mailbox address, user id).
- **Result** — `ok` or `fail` with a structured error code.
- **Diff** — for field-level changes, the old and new values, with secret values redacted.
- **Request ID** — correlates with panel-api / agent journal lines.

## Filters

- Time range (last hour, 24 h, 7 d, custom).
- Action pattern (`domain.*`, `mailbox.passwd`, `db.root.rotate`).
- Actor / subject by username, email, or id.
- Source.
- Result (`ok`, `fail`).
- Free text (matches across action, target, and request ID).

## Per-row drill-in

Click a row to see the full diff, the request ID, the linked services' journal lines if available, and any cascading actions the row triggered (a domain create writes one audit row but typically schedules a reconciler convergence whose result is its own row).

## Retention and export

Audit rows are retained indefinitely by default. Configurable under Server Settings → Audit → Retention. Export to CSV or JSON is available on the page header.

## Append-only enforcement

The `audit_log` table is write-once: no UPDATE or DELETE statements are issued in normal operation. Schema-level triggers reject mutations from any role except a single migration role used only when an explicit pruning policy is applied.

## Tenant view

Each tenant has their own view at `/jabali-panel/activity` filtered to rows where they are the subject. Tenants cannot see rows for other tenants.

## CLI

```bash
jabali audit query                     # the last 50 events
jabali audit query --limit 500         # up to 1000
jabali audit query --q db.             # search action, target, actor kind and result
jabali audit verify                    # check the hash chain for tampering
```
