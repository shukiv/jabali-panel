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
