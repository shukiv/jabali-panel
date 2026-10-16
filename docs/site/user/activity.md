# Activity

`/jabali-panel/activity`. The audit trail scoped to your account.

## What is shown

Every privileged action that affected your account. Each row contains:

- Timestamp — in the server's timezone, with seconds. Hover it to see UTC.
- Actor — typically yourself, or "system" for reconciler-initiated changes, or "admin (`<username>`)" when an administrator acted on your behalf.
- Action — namespaced identifier (e.g. `mailbox.passwd`, `domain.create`, `ssl.issue`).
- Target — the affected resource (mailbox address, domain, database).
- Result — `ok` / `fail`.
- Diff — for field-level changes, the old and new values, with secret values redacted.

## Filters

- Time range.
- Action pattern.
- Result.
- Free-text search.

## Use cases

- Verify that a configuration change you expected was actually applied.
- Investigate a notification you received ("backup failed" — find the row, read the structured error code).
- Confirm whether an administrator action affected your account during a support session.
- Provide an evidence trail when investigating an incident.

## What you cannot see

- Other tenants' activity.
- Server-level actions that did not target your account.
- Administrative actions on the panel itself (server settings, package edits, IP pool changes) — those live in the admin-only [Audit Log](../admin/audit-log.md).

## Retention

Default: indefinite. The administrator may set a retention window under Server Settings → Audit; older rows are pruned by a daily timer.

## Export

The page header has **Export CSV** and **Export JSON**. Useful for compliance reports or for handing the activity record to a third-party investigator.
