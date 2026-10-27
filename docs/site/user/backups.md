# Backups (User)

`/jabali-panel/backups`. Your account-level backup view.

## What you can see

If the administrator has enabled tenant-visible backups for your package, this page shows:

- **Snapshots** — the list of `account_full` snapshots taken for your account, with timestamp, size, and source destination.
- **Backup schedules** — on a plan that allows more than one, your own schedules: what each backs up (the full account, files only or databases only), how often (hourly, every 6 hours, every 12 hours, daily or weekly), the destination, the days to keep, and its next run. On a plan with one schedule, the schedule the administrator configured for your account.
- **Restore** — request restore of a specific snapshot back to your account.

If tenant-visible backups are disabled for your package, the page shows only the [Backup Download](./backup-download.md) card (if even that is enabled).

## Requesting a restore

1. Pick the snapshot you want to restore from.
2. Pick the components — everything, or a subset (files only, databases only, mailboxes only).
3. Pick a target — restore in place (overwrites current state) or to a "staging" location (writes to `/home/<your-username>-restore-<timestamp>/`).
4. Submit. The request is queued for operator approval before running.

Operator approval is required by default to prevent accidental large-scale data overwrites. The operator may grant your package automatic restore approval if they trust you to manage your own data — ask if useful.

## What a restore includes

- Home directory files.
- Databases (each restored into its original DB; the DB is dropped and recreated before the dump replays).
- Mailbox content (each mailbox is restored as-is, including IMAP folder structure and flags).
- DNS zone records.
- Cron job definitions.

## What a restore does *not* include

- Linux account password (you use Kratos for panel login; SFTP uses SSH keys).
- TLS certificates (auto-reissued via Let's Encrypt within minutes of restore).
- Application file ownership for files created by the application after the snapshot.

## Frequency

Snapshot frequency is set by the administrator per package. Typical cadence: nightly with 7 daily / 4 weekly / 12 monthly retention.

## When scheduled backups run

The panel runs your schedules itself: it checks for due backups every minute, whether or not you are signed in.

- A schedule's first run is one interval after you save it. Later runs keep the times of day of that first run, on the server's clock (shown next to the schedules): every 6 hours from 19:29 runs at 01:29, 07:29, 13:29 and 19:29 each day, and weekly runs on the same day and time each week.
- A run that starts late, or a run missed while the panel was down, runs once when it can, and the next one is at its usual time.
- Saving a schedule without changing **How often** keeps its times. Changing **How often** starts again from when you save. Turning a schedule back on waits for its next time; it doesn't start a backup at once.
- Through a daylight saving change the runs keep their times of day. A time the change to summer time skips (02:00–03:00 in Central Europe) is skipped that night, as cron does, and a time that happens twice when the clocks go back runs once. Hourly schedules run every hour across the change.
- If your host restricts backups to a time window, the schedules run inside it.

**Days to keep** is how many days of backups the schedule keeps. Once a day, early in the morning, the panel keeps the newest backup of each day and removes the others. So a schedule that runs every 6 hours lists every backup made since that cleanup, and one for each earlier day.

## Off-host destination

By default, backups live on the panel host's local disk. If the administrator has configured an off-host destination (SFTP, S3, B2, Azure, GCS, restic REST), snapshots are replicated there automatically — so a panel host failure does not lose the backup data.

## Download

For an ad-hoc download of your account state, use [Backup Download](./backup-download.md). The download produces a fresh snapshot on demand and offers it for direct download.
