# Backup Schedules

Backups → Schedules. The cron expressions that drive periodic backup runs.

## Per-schedule fields

- **Name** — operator label.
- **Kind** — `account_full` or `system_backup`.
- **Subject** — for `account_full`, one user, a set of users, or "all users". For `system_backup`, the panel host (single subject).
- **Destination(s)** — one or more from [Destinations](./backup-destinations.md). Restic writes to each.
- **Cron expression** — standard 5-field cron (`min hour day month dow`), read in the server's timezone ([Server Settings → Server Time](./server-settings.md#server-time)): `0 3 * * *` runs at 03:00 server time. The form sets it from a frequency:
  - **Hourly** — every 1, 2, 3, 4, 6, 8 or 12 hours, at a minute past the hour (`15 */6 * * *` runs at 00:15, 06:15, 12:15 and 18:15). Each run backs up every account the schedule covers.
  - **Daily**, **Weekly** (on chosen days) or **Monthly** (on a day of the month), at a time.

  A schedule whose cron the form can't show (one set with the CLI, say) opens with a notice that shows its cron; saving replaces it with the time in the form.
- **What to back up** — for account schedules: the full account, files only, or databases only. A Full Server schedule applies it to the account backups; its system backup is always complete.
- **Retention** — `--keep-daily N`, `--keep-weekly N`, `--keep-monthly N`, with restic's meaning. A schedule with none set keeps every backup. An hourly schedule uses the daily count: retention keeps one backup a day, so the daily sweep removes the day's other hourly backups.
- **Enabled** — schedule may be paused without deletion.

A schedule keeps its kind once it is saved: editing an Accounts or Full Server schedule cannot turn it into a System schedule, or the other way round. Create a new schedule instead.

## Accounts' own schedules

On a plan that allows more than one backup schedule, an account adds its own schedules on its **Backups** page. An admin can add one for it here: **New schedule → Type: Account's own**, then pick the account. They are listed here with the type **Account's own**, what they back up, and the account's name. Every account schedule in the list shows what it backs up: Full account, Files only or Databases only. Each one:

- backs up only that account;
- runs on an interval instead of a cron expression: hourly, every 6 hours, every 12 hours, daily or weekly. Its runs keep the times of day of its first run, like cron, on the server's clock: every 6 hours from 19:29 runs at 01:29, 07:29, 13:29 and 19:29. When **Settings → Restrict tenant backups to a maintenance window** is on, it runs inside that window;
- backs up the full account, files only, or databases only;
- writes to one destination.

**Edit** opens it with the same choices the account has: what to back up, how often, the destination, the copies to keep, and on or off. The account sees your changes on its Backups page. Changing how often re-times the next run; other changes leave it where it is. Its account cannot be changed, and it cannot include the system backup. An existing cron schedule cannot become one.

When the runs happen ([user guide](../user/backups.md#when-scheduled-backups-run)): the panel's scheduler checks for due schedules every minute, whether or not anyone is signed in. A run that starts late, or that runs once after the panel was down, doesn't move the ones after it. Through a daylight saving change the runs keep their times of day; a time the change to summer time skips is skipped, as cron does, a time that happens twice when the clocks go back runs once, and hourly schedules count elapsed hours. An interval schedule turned back on, here or on the account's page, waits for its next time instead of running at once. Before the November 2026 release each run was timed one interval after the previous one started, so a late start, or the account saving it, moved every later run, and a daylight saving change moved them by an hour.

The account's hosting plan applies to it the same way it does on the account's page, so the account can always open and save it:

- Adding one needs a plan that allows scheduled backups and more than one schedule, with room left: the account's own schedules count toward the plan's limit, however they were added. For a plan with one schedule, raise the plan's backup schedule limit first.
- Its destination must be enabled and of a kind the plan allows. An enabled schedule needs a destination.
- Copies to keep: 1 up to the plan's backup limit.

When editing, the destination and copies are checked only when they change, so a schedule can always be turned off.

On a plan with a single backup schedule, the account's one schedule is also listed as **Account's own**. It runs at **Settings → Tenant scheduled-backup time (cron)** and opens in the cron form with its account fixed. When the account saves it again, its time goes back to that setting.

The API refuses a `cron_expr` for an interval schedule (`interval_schedule`), a `cadence` for a cron schedule (`cron_schedule`), and, on any account's own schedule, a user list other than that account or `include_system_backup: true` (`account_schedule`). `POST /admin/backup-schedules` with a `cadence` creates an account's own schedule: exactly one entry in `user_ids`, at most one in `destination_ids`, no `cron_expr`. The plan refusals use the codes of the account's own page: `scheduled_backups_not_enabled`, `schedule_limit_reached`, `destination_not_allowed`, `destination_disabled`, `unknown_destination`, `destination_required`, plus `single_schedule_plan`, `one_destination` and `keep_out_of_range`. A plan that can't be read refuses with 503 `backup_gating_unavailable`.

## Implementation

Each schedule becomes a `systemd` system timer managed by the agent. The timer triggers a one-shot service unit that calls the agent's `backup.run` action with the schedule id; the agent constructs the restic command line, runs the per-stage pipeline, and reports success or the first failure line.

## Lock and concurrency

A schedule is serialised by id: a new tick will not start if the previous run has not finished. Two schedules for the same subject may overlap; the underlying restic repository handles concurrent writes natively.

When the panel or agent gives up on a restic command (a timeout, or the call is cancelled), restic is sent SIGINT, so it removes its repository lock before it exits. It is killed only if it is still running 20 seconds later.

## Retention application

Retention runs once a day, from `jabali-backup-retention.timer` at 05:00 (`jabali backup retention apply`), for every enabled schedule with a keep count, on each of its destinations. It is not part of a backup run. It runs after the panel self-update (default 04:30), so a sweep always uses the build that update installed; if a self-update is still running at 05:00, the sweep waits for it to finish.

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

Before that prune, the sweep also deletes the panel row of every finished backup on that destination that has nothing left in its repository: no snapshot tagged with its `job-id` and none matching its recorded snapshot ID. Sweeps before whole-backup retention forgot snapshots but kept those rows, so the panel listed backups that could no longer be restored, and they counted toward plan backup limits. It also deletes the row of a backup whose manifest is gone but whose data snapshots are still there; those earlier sweeps left many of these. Restoring from the panel starts from the manifest, so such a backup was listed but could not be restored. Its data snapshots stay in the repository, still counted by the keep rules, until they age out. A backup must have finished at least an hour before the sweep's snapshot listing to be judged, and a repository that lists no snapshots at all is left alone (a wrong path or credentials would look the same). Failed and cancelled backups are judged the same way once they are 30 days old: a run that failed before it saved anything has nothing for the keep rules to forget, so its row would otherwise stay in the panel for good, while a younger one stays visible with its error. A failed run whose snapshots are still in the repository keeps its row until the keep rules forget them. `--dry-run` lists the rows it would delete.

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
