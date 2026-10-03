# Cron Jobs

M8. systemd-user timers + a command allowlist.

## Model

A **Cron Job** is:

- 5-field cron schedule (`min hour day month dow`).
- An owner user (must have a Linux account).
- A command from the **allowlist** (`php`, `wp`, `python`, `node`, plus a curated set per-app), or an **ordered sequence** of `wp` / `php` commands run one after another in a single job (GH #1435, #1437).
- An optional name (display label).

Internally each cron row becomes:

- `~/.config/systemd/user/jabali-cron-<id>.service` — the command.
- `~/.config/systemd/user/jabali-cron-<id>.timer` — the schedule.

Both are owned by the user. `loginctl enable-linger <user>` runs at user creation so timers fire without an active session.

## Why not crontab?

systemd-user timers give:

- Per-job journal logs (`journalctl --user -u jabali-cron-<id>`).
- `OnFailure=` hook → notification dispatcher.
- `RandomizedDelaySec=` — natural jitter without operators having to add `sleep $RANDOM`.
- No per-user `crontab -e` shell access (we don't grant interactive shell to panel users).

## Command allowlist

Only commands the admin has marked allowed can be scheduled. The default allowlist (`/internal/cronvalidate/`) is the shared validator used by both the REST API and the CLI (`Cron Job Intake` — the single ingest path, per CONTEXT.md). Alongside `php` and `wp`, the validator accepts **`python` and `node`** scripts located under the owner's home (GH #1435), and an **ordered sequence** of `wp`/`php` steps in one job (GH #1437). Custom shell scripts are not allowed by default — admins can extend the allowlist.

## Admin view

**Admin → Cron Jobs** lists every tenant's jobs. From there an admin can create a job under any tenant (or as `root`), and toggle, run, view the log of, edit, or delete any job. Editing changes only the name, command and schedule. The owner and the run-as target stay fixed; to move a job to another tenant, delete it and create it again. An admin's edit is checked exactly like the tenant's own: the command must stay inside the **job owner's** directories, not the admin's (GH #1686).

A `root` job is always owned by the admin who creates it. The API refuses `run_as_root` combined with another account's `user_id` (`422 run_as_root_owner_mismatch`), because a root job's command is checked against its owner's directories. A root job whose owner is not an admin, which only a raw API call could create before this check, can no longer be edited or toggled (`422 root_cron_owner_not_admin`). Delete it and create it again as an admin. Blocking edits does not stop such a job: its system timer keeps running until the job is deleted. To find such jobs, including ones whose owner no longer exists: `SELECT c.id, c.user_id FROM cron_jobs c LEFT JOIN users u ON u.id = c.user_id WHERE c.run_as_root = 1 AND (u.id IS NULL OR u.is_admin = 0);`

## System jobs

**Admin → Cron Jobs → System jobs** lists the scheduled jobs Jabali itself installs on the server, apart from tenant jobs (GH #1686). The tab can be linked directly: `/jabali-admin/cron?tab=system`.

Each row shows what the job does, its schedule, its status, when it last ran and whether that run succeeded, and when it runs next. The status is one of these:

- **Scheduled**: the job's timer is active and the job will run at the next run time.
- **Running**: the job is running now.
- **Disabled**: the job's timer is not active, so the job does not run. Some jobs are off on purpose. For example, panel auto-update is off until it is turned on on the Updates page, and the free-hostname check-in runs only on a server that uses a free hostname. The schedule column then reads **Not scheduled**, with the schedule the job would run on once enabled underneath.

A job that is not installed on the server is not shown. The success of a run is systemd's verdict on it (`Result`), not the raw exit code, because some jobs exit with a non-zero code by design. For example, AIDE exits non-zero when it reports file changes.

The last run counts every run, scheduled or started with Run now, including runs of a disabled job. systemd forgets a finished run of a job whose timer is disabled, and every job's last run after a reboot; the agent then reads the last run from systemd's own start and finish lines in the journal.

The actions are:

- **Run now** starts the job immediately, after a confirmation. The job runs in the background, and the list updates while it runs. A job that is already running is not started again.
- **View log** shows the job's recent journal lines.
- **Open Updates** and **Open Backups** go to the page that owns the job's settings. The list does not repeat those settings.

Run now is not offered for three jobs:

- **Operating system updates** and **Panel auto-update** run from the Updates page, which shows their progress.
- **Single sign-on cleanup** runs every 30 seconds on its own.

Nothing in this list can be turned off. The security jobs (malware signatures and scans, YARA rules, AIDE, CrowdSec, AppArmor and outbound-traffic enforcement, secret cleanup) always stay on.

The server-wide backup schedules appear as rows that link to the Backups page. That page is where they are changed and run, and where their results are shown. A tenant's own backup schedules are tenant jobs and are not listed. Jobs that run inside the panel itself, such as the reconciler and notification delivery, are not listed: there is nothing to run or change on them.

The list is a fixed catalog compiled into the panel and the agent (`internal/systemjobs`). A request names a job by its catalog id, never by a systemd unit. The agent refuses Run now for the three jobs above even if the panel asked. The API:

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/v1/admin/system-jobs` | The installed jobs and the server-wide backup schedules. |
| `POST` | `/api/v1/admin/system-jobs/{id}/run` | `202` started, `409 already_running`, `422 run_now_not_allowed`, `404 unknown_system_job` or `404 system_job_not_installed`. Rate-limited per admin, and audit-logged (`kind=system_job_run`). |
| `GET` | `/api/v1/admin/system-jobs/{id}/log?lines=N` | The job's last `N` journal lines, 1 to 500, 200 by default. |

All three endpoints are admin-only.

## CLI

```bash
jabali cron list --user <id>
jabali cron add --user <id> --schedule "0 3 * * *" --command "wp cron event run --due-now --url=https://example.com"
jabali cron update <job-id> --schedule "*/15 * * * *"
jabali cron delete <job-id>
jabali cron run-now <job-id>     # synchronous, ignores schedule
```

## Failures

If a job exits non-zero, the `OnFailure=jabali-cron-notify@%n.service` unit fires and the notifications dispatcher (M14) sends an alert via the user's configured channels (in-app bell, email, etc.).
