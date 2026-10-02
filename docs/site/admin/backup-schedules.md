# Backup Schedules

Backups → Schedules. The cron expressions that drive periodic backup runs.

## Per-schedule fields

- **Name** — operator label.
- **Kind** — `account_full` or `system_backup`.
- **Subject** — for `account_full`, one user, a set of users, or "all users". For `system_backup`, the panel host (single subject).
- **Destination(s)** — one or more from [Destinations](./backup-destinations.md). Restic writes to each.
- **Cron expression** — standard 5-field cron (`min hour day month dow`).
- **Retention** — `--keep-daily N`, `--keep-weekly N`, `--keep-monthly N`, with restic's meaning. A schedule with none set keeps every backup.
- **Enabled** — schedule may be paused without deletion.

## Implementation

Each schedule becomes a `systemd` system timer managed by the agent. The timer triggers a one-shot service unit that calls the agent's `backup.run` action with the schedule id; the agent constructs the restic command line, runs the per-stage pipeline, and reports success or the first failure line.

## Lock and concurrency

A schedule is serialised by id: a new tick will not start if the previous run has not finished. Two schedules for the same subject may overlap; the underlying restic repository handles concurrent writes natively.

## Retention application

Retention runs once a day, from `jabali-backup-retention.timer` at 04:30 (`jabali backup retention apply`), for every enabled schedule with a keep count, on each of its destinations. It is not part of a backup run.

Retention keeps or forgets **whole backups**. One backup is several restic snapshots: one per stage (home folder, databases, mail, … for an account; the panel database, TLS, OS users, … for the system backup) and a manifest that ties them together. The sweep:

1. Groups the schedule's snapshots by backup (their `job-id` tag), and the backups by account, or by host for the system backup. One account's backups never use up another's keep counts.
2. Applies restic's keep rules to each account's backups, by the backup's time. They count complete backups (those with a manifest) and account backups without a manifest that still hold home folder, database, mail or docker app data. As in restic, one backup of each day, week or month is kept, up to the counts: a complete backup if that day, week or month has one, otherwise the one holding the most kinds of data, and of equal ones the most recent. When a count is not used up, the oldest backup is kept too.
3. Forgets every snapshot of each backup it does not keep, by snapshot ID, and deletes that backup's row in the panel.

So a restore point is never left partial. An older version applied the keep counts to each stage separately. When a backup's stages fell on different days (a run across midnight), or a failed run wrote only some stages, that could keep a manifest whose home folder or database snapshot was forgotten.

That older version also grouped snapshots by path. The manifest's path is the same for every account, so a server kept only about one policy's worth of manifests in total, while each account's home folder and database snapshots kept that account's full history. So many backups made before whole-backup retention hold data but no manifest. The panel cannot restore them, but their data can still be read with `restic restore` or `restic dump`, and they are counted by the keep rules rather than forgotten as leftovers. A backup that holds only mail is forgotten when the rules do not keep it.

A backup with no manifest and none of that data (a run that failed before it saved any) is forgotten once the account has a newer complete backup. A system backup without a manifest is treated the same way, against the server's newer complete system backup.

Never forgotten:
- a backup that is still queued or running;
- snapshots without a `job-id` tag.

Before that prune, the sweep also deletes the panel row of every finished backup on that destination that has nothing left in its repository: no snapshot tagged with its `job-id` and none matching its recorded snapshot ID. Sweeps before whole-backup retention forgot snapshots but kept those rows, so the panel listed backups that could no longer be restored, and they counted toward plan backup limits. A backup must have finished at least an hour before the sweep's snapshot listing to be judged, and a repository that lists no snapshots at all is left alone (a wrong path or credentials would look the same). `--dry-run` lists the rows it would delete.

A single `restic prune` per destination then frees the space; it can take longer than the backups themselves on large repositories. A DR standby never runs retention: its destinations are the primary's.

Preview a sweep with `jabali backup retention apply --dry-run`: it lists the backups it would forget and forgets nothing.

Each destination is opened with its own repository password once that password has been rotated (see [Repository password](./backup-destinations.md#repository-password)), and with the shared password file otherwise. A destination the sweep cannot open fails on its own; the others are still swept. The **Local** destination that the default local backup schedule creates has no URL; the sweep prunes the same default repository (`/var/lib/jabali-backups/repo`) the backups write to.

### Checking existing backups

`jabali backup retention verify` reads the manifest of every backup in each enabled destination (`--destination <id-or-name>` for one) and checks that every stage snapshot it lists is still in the repository. It reports:

- **broken**: a stage snapshot the manifest lists is missing, or the manifest lists no stage that wrote data. That backup cannot be restored. Sweeps from before whole-backup retention could leave backups like this.
- **unreadable**: the manifest itself could not be read.

It changes nothing and reads the repository without a lock, so it can run while backups do. It reads one manifest per backup, which takes a while on a large remote repository. It exits non-zero when it finds anything, and `--json` prints the full report. Delete a broken backup from **Admin → Backups**; that forgets every snapshot of the backup.

## Quotas and limits

`system_backup` is heavy by definition (the entire panel host). On constrained disks the operator should target an off-host destination (`sftp`, `s3`, `b2`) rather than `local`. Disk-quota checks at run time skip a run with a warning if the destination is short on space.

## Notifications

A failed job fires `backup.fail`. A finished job fires `backup.success`, which is off by default (see [Notifications Events](./notifications-events.md)).

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
