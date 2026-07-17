# Edit User

Reached from the **Edit** action on a row in [Users](./users.md). Modifies every mutable attribute of a panel user.

## Sections

- **Identity** — display name, email, role. Email changes write through to Kratos.
- **Package** — reassign to a different package. Quotas and limits converge on the next reconciler tick.
- **Quotas (overrides)** — per-user override of disk quota, memory limit, CPU percentage, request rate, and PHP-INI values. Each override is independent; clearing an override returns the field to the package default.
- **Suspension** — set the suspension reason and effective date. The reconciler stamps `is_suspended=1` on the row; vhosts return a suspended page.
- **Egress firewall** — per-user destination overrides for the nftables ruleset. See [Per-User Egress](./egress.md).
- **SSH** — toggle password authentication (default off — SSH keys only).
- **Notifications** — opt-in / opt-out per event source for this user's own events (cron failure, backup result, mail quarantine).

## Operator-only fields

- **Force password reset on next login** — flips a Kratos flag that requires credential rotation at next session start.
- **Lock account** — temporarily prevents login without deleting state; reversible.
- **Notes** — free-text operator note attached to the user, visible only to administrators.

## What persists on save

A single transactional `UPDATE` against `users`, plus a Kratos call if identity fields changed. The reconciler is scheduled with `Reconciler.Schedule(<user-id>)` so quota, slice limits, and PHP-FPM pool drop-ins reflect any changed fields within 60 seconds.

## What does **not** happen on save

- Domains owned by the user are not modified.
- Mailboxes owned by the user are not modified.
- The Linux account is not renamed, even if the panel username is renamed (Linux username changes require a full delete-and-recreate cycle).

## Audit

Every field-level change writes an audit row with a structured diff (old value → new value). See [Audit Log](./audit-log.md).
