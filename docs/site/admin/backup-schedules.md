# Backup Schedules

Backups → Schedules. The cron expressions that drive periodic backup runs.

## Per-schedule fields

- **Name** — operator label.
- **Kind** — `account_full` or `system_backup`.
- **Subject** — for `account_full`, one user, a set of users, or "all users". For `system_backup`, the panel host (single subject).
- **Destination(s)** — one or more from [Destinations](./backup-destinations.md). Restic writes to each.
- **Cron expression** — standard 5-field cron (`min hour day month dow`).
- **Retention** — restic-style `--keep-daily N`, `--keep-weekly N`, `--keep-monthly N`, `--keep-yearly N`.
- **Enabled** — schedule may be paused without deletion.

## Implementation

Each schedule becomes a `systemd` system timer managed by the agent. The timer triggers a one-shot service unit that calls the agent's `backup.run` action with the schedule id; the agent constructs the restic command line, runs the per-stage pipeline, and reports success or the first failure line.

## Lock and concurrency

A schedule is serialised by id: a new tick will not start if the previous run has not finished. Two schedules for the same subject may overlap; the underlying restic repository handles concurrent writes natively.

## Retention application

After every successful run, restic's `forget --prune` runs with the schedule's retention flags. The prune phase may take longer than the backup itself on large repositories; the run is not marked complete until prune finishes.

## Quotas and limits

`system_backup` is heavy by definition (the entire panel host). On constrained disks the operator should target an off-host destination (`sftp`, `s3`, `b2`) rather than `local`. Disk-quota checks at run time skip a run with a warning if the destination is short on space.

## Notifications

Each run emits `backup_succeeded` or `backup_failed` (see [Notifications Events](./notifications-events.md)).

## Common patterns

- **Account nightly + system weekly** — `account_full` per user nightly, `system_backup` weekly.
- **Account hourly for paying tier, daily for free** — segment by package, two schedules.
- **3-2-1 (operator-side)** — three copies, two media types, one off-site. Achieved with a local destination plus an off-site S3 destination on the same schedule.

## CLI

```bash
jabali backup schedule list
jabali backup schedule create --kind account_full --user <id> --destination daily-offsite --cron "0 3 * * *" --keep-daily 7 --keep-weekly 4 --keep-monthly 12
jabali backup schedule delete <id>
jabali backup scheduler tick                          # fire all due schedules now
jabali backup schedule run-now <id>                   # fire one
```
