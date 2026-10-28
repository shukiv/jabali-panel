# Backups (User)

`/jabali-panel/backups`. Your account-level backup view.

## What you can see

If the administrator has enabled tenant-visible backups for your package, this page shows:

- **Snapshots** — the list of `account_full` snapshots taken for your account, with timestamp, size, and source destination.
- **Backup schedules** — on a plan that allows more than one, your own schedules: what each backs up (the full account, files only or databases only), how often (hourly, every 6 hours, every 12 hours, daily or weekly), the destination, the days to keep, and its next run. On a plan with one schedule, the schedule the administrator configured for your account.
- **Restore** — request restore of a specific snapshot back to your account.

If tenant-visible backups are disabled for your package, the page shows only the [Backup Download](./backup-download.md) card (if even that is enabled).

## Requesting a restore

Open a backup's **Restore** action. The drawer lists what that backup holds; tick what to bring back:

- **Home directory** — every file of your home, over the live files. Files you added since the backup are kept.
- **Databases** — each one is emptied and loaded from the backup.
- **Mailboxes** — messages from the backup come back; messages you have now are kept, and none are doubled.
- **DNS records** — every domain of the backup is listed. Restoring one replaces its custom DNS records with the backed-up set; the records the panel manages rebuild themselves. A domain with no custom records in the backup is shown greyed out, with "no custom DNS records in this backup", since there is nothing to restore.
- **Domain files** — one domain's folder (`~/domains/<domain>`), without restoring the whole home. Every domain of the backup is listed. The backup's files are written over the live ones; files added since the backup are kept, unless you tick **Delete files added since the backup**, which leaves each selected domain's folder exactly as it was in the backup. Other domains' folders are never touched.

Confirm, then **Restore selected**. The restore runs in the background and the drawer shows what was restored when it ends; it also appears in the backups list. Only domains your account has now are listed: a domain the account no longer has can't be restored into.

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

When your account holds as many backups as your plan allows, a schedule with **Days to keep** set removes its own oldest backup to make room for the next run. It never removes its last backup, or a backup you made yourself or another schedule made.

## Off-host destination

By default, backups live on the panel host's local disk. If the administrator has configured an off-host destination (SFTP, S3, B2, Azure, GCS, restic REST), snapshots are replicated there automatically — so a panel host failure does not lose the backup data.

## Download

For an ad-hoc download of your account state, use [Backup Download](./backup-download.md). The download produces a fresh snapshot on demand and offers it for direct download.
