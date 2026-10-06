# Backups (Admin)

`/jabali-admin/backups`. The parent surface for backup destinations, schedules, retention, and restore. M30 and M30.1.

## Tabs

- **Destinations** — see [Backup Destinations](./backup-destinations.md).
- **Schedules** — see [Backup Schedules](./backup-schedules.md).
- **Retention** — global default plus per-schedule overrides.
- **Restore** — see [Backup Restore](./backup-restore.md).
- **History** — every backup run with destination, kind, size, duration, result.

## Backup kinds

- **`account_full`** — one user's entire account: home directory, all databases owned by the user, all mailboxes in the user's domains, DNS records, cron jobs, app installs.
- **`system_backup`** — the whole panel host: 7 stages comprising three rolling panel-DB dumps, OS users, Stalwart state, Kratos state, hosted sites, config snapshot, then a single restic snapshot wrapping all of the above.

Both kinds are restic-backed: deduplicated, encrypted at rest, multi-destination.

## Default schedule

The installer creates no schedules by default; the operator picks the cadence per environment. Common starting point:

- `account_full` per user nightly at 03:00 local, 7 daily / 4 weekly / 12 monthly snapshots.
- `system_backup` weekly Sunday 04:00 local, 4 weekly / 6 monthly snapshots.

## What is *not* backed up

- The kernel and OS packages.
- `/proc`, `/sys`, `/tmp`, `/var/cache`.
- Restic itself (use a `b2` / `s3` destination with its own provider-side history, or a second restic repo, for disaster-recovery of the panel's own backup state).

## Run history

Each row: started at, kind, target (user or whole-system), destination, status (`ok` / `fail`), bytes written (deduplicated), duration.

Click for the detailed stage-by-stage log.

## Uploaded backups

Account backups uploaded from another server with **Restore from Upload** are
listed above the run history, with **Restore** and **Delete**. See
[Uploaded backups stay on the server](./backup-restore.md#uploaded-backups-stay-on-the-server).

## CLI

```bash
jabali backup destination list
jabali backup destination create --kind sftp --name daily-offsite --url sftp:backups@backup.example.com:/srv/restic
jabali backup destination test daily-offsite

jabali backup schedule list
jabali backup schedule create --kind account_full --user <id> --destination daily-offsite --cron "0 3 * * *" --keep-daily 7

jabali backup scheduler tick    # manual trigger of all due schedules
```
