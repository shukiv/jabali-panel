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
