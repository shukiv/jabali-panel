# Cron Jobs

`/jabali-panel/cron`. Scheduled commands that run under your account (M8).

## Adding a cron job

Click **Add cron job**, supply:

- **Name** — operator label.
- **Schedule** — 5-field cron (`min hour day month dow`). Examples: `0 3 * * *` (daily at 03:00), `*/15 * * * *` (every 15 minutes), `0 0 1 * *` (first day of each month at midnight).
- **Command** — must be on the allowlist (see below).

On save, the agent creates two systemd-user units in your home:

- `~/.config/systemd/user/jabali-cron-<id>.service` — the command.
- `~/.config/systemd/user/jabali-cron-<id>.timer` — the schedule.

Both are owned by you. The first time you add a cron job, systemd lingering is enabled for your account (`loginctl enable-linger <your-username>`) so timers fire even when you are not actively logged in.

## Command allowlist

The panel does not allow arbitrary shell commands, and cron jobs run **outside**
the SSH shell sandbox as your user — so the command must be one of a fixed set
of interpreters running a file you own, with no shell features (no `cd`, `$HOME`,
`|`, `;`, `&&`, backticks, redirects, or globbing). Use **absolute paths**
instead of `cd`/`$HOME`.

The allowlist:

- **`wp`** (WP-CLI) — requires `--path=<absolute-docroot>` instead of `cd`.
  Example: `wp --path=/home/USER/domains/example.com/public_html cron event run --due-now`
- **`php`** (or a pinned version, e.g. `php8.3`) — runs an **absolute `.php` file
  inside one of your docroots**. Inline code (`-r`, `-R`, `-B`, `-E`) is not allowed.
- **`python`** / **`python3`** / a pinned version (e.g. `python3.11`), or the
  absolute path to a **virtualenv** interpreter — runs an **absolute `.py` file
  inside your account** (home or a docroot). Inline/module execution (`-c`, `-m`,
  reading from stdin) is not allowed. Django example:
  `python3 /home/USER/site/manage.py runjobs` or, with a venv,
  `/home/USER/venv/bin/python /home/USER/site/manage.py migrate` — note there is
  no `cd`; give `manage.py`'s absolute path.
- **`node`** / **`nodejs`** (or an absolute path to one, e.g. an nvm build) — runs
  an **absolute `.js` / `.mjs` / `.cjs` file inside your account**. Inline code
  (`-e`, `--eval`, `-p`, `--print`) is not allowed.
- **`curl`** / **`wget`** — a plain GET to one of **your own domains** (a
  self-domain HTTP trigger; the panel pins the target and hardens the request).

A job may hold **several commands, one per line** — they run in order and stop
if one fails. Blank lines and `#` comments (including a `#!/bin/bash` shebang
line) are ignored, so you can paste a script's body, but every executable line
must still match the allowlist above.

## Why systemd-user, not crontab

- Per-job journal logs (`journalctl --user -u jabali-cron-<id>`).
- `OnFailure=` triggers a notification through the M14 dispatcher.
- `RandomizedDelaySec=` provides natural jitter without adding `sleep $RANDOM` to your command.
- You do not need an interactive shell to manage your cron jobs (and you do not have one).

## Editing and deleting

Per-row **Edit** changes the schedule or command. **Delete** removes both unit files; the reconciler converges within 60 seconds.

## Running on demand

Per-row **Run now** triggers the service unit immediately, bypassing the timer. Useful for testing a new cron job without waiting for the next scheduled fire.

## Common cron patterns

- WordPress: `wp cron event run --due-now --url=https://example.com` every 15 minutes (replaces WP's built-in pseudo-cron, which fires only on page hits).
- Maintenance: a custom PHP script to clean up old uploads daily.
- Backups: a `mysqldump` to your own off-site target (this is on top of the panel's `account_full` backups).

## Failure handling

If a cron job exits non-zero, the `OnFailure=` hook fires the `cron_failed` notification event. You can route this to your in-app bell or email under Profile → Notifications.
