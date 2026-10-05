# Resource Limits

M18. ADR-0032.

Three layers:

## 1. POSIX disk quota

Enforced on `/home`. Per-user quota set from the user's package (default + override). Hitting the soft limit warns; hitting the hard limit blocks writes (PHP-FPM logs `disk full`, Stalwart bounces with `552`, file uploads fail in the UI).

Implementation: `setquota -u <user> <soft> <hard> 0 0`. Reconciler re-applies on every tick; if a user's package quota changes the new limit lands within 60 s.

Per-tick idempotent: the reconciler compares current quota → desired quota and only calls `setquota` on diff (the "per-tick idempotent loops" audit rule — gates side-effects behind a no-change compare).

## 2. cgroup v2 slice drop-in

Each hosting account runs in its own systemd slice, `jabali-user-<username>.slice`, under `jabali.slice/jabali-user.slice`. The account's effective limits (its package's values, or the per-user overrides set under Admin → Users → user → **Resource limits**) are written to the drop-in `/etc/systemd/system/jabali-user-<username>.slice.d/limits.conf`:

```ini
[Slice]
CPUQuota=<cpu_quota_percent>%
MemoryMax=<memory_limit_mb>M
MemoryHigh=<90% of memory_limit_mb>M
IOReadBandwidthMax=/ <io_read_mbps>M
IOWriteBandwidthMax=/ <io_write_mbps>M
TasksMax=<max_tasks>
```

Only non-zero limits are written; a limit of 0 means unlimited and leaves its line out. With every limit at 0 the drop-in is removed. The reconciler re-applies the drop-in on every tick.

The slice holds **everything** the account runs: PHP-FPM masters and workers, Python apps, cron jobs, the account's systemd user manager, and SSH sessions.

### Max tasks

`TasksMax` caps the processes **and threads** in the slice at once. Size it for what the account runs:

- each PHP-FPM pool: 1 master plus up to `pm.max_children` workers (on-demand pools start a worker per request);
- each Python app: at least 4 (a gunicorn or uvicorn master plus 3 workers), more if the app starts threads;
- the account's systemd user manager, when it is running: 2;
- cron jobs and SSH sessions while they run.

Lowering the limit does not stop processes that are already running; it only stops new ones from starting. When the slice is full, systemd logs `Failed to spawn executor: Resource temporarily unavailable`, PHP-FPM pools and Python apps fail to start, and running on-demand pools time out because they cannot start a worker. The pool's or app's error then says the account has reached its Max tasks limit and shows the count. To check by hand:

```
systemctl show jabali-user-<username>.slice -p TasksCurrent -p TasksMax
systemd-cgls --no-pager -u jabali-user-<username>.slice
```

## 3. nginx `limit_req`

Per-user request rate cap on the user's vhosts. Default zone:

```nginx
limit_req_zone $binary_remote_addr zone=jabali_<user>:10m rate=<package_req_per_sec>r/s;
```

Inside each vhost: `limit_req zone=jabali_<user> burst=<package_req_burst> nodelay;`. Override per-domain via Domains → Edit (planned).

## Package fields

A **Package** carries:

- `disk_quota_mib`
- `bandwidth_quota_gib` (monthly; tracked separately)
- `memory_limit_mib`
- `cpu_pct`
- `tasks_max`
- `req_per_sec`, `req_burst`
- `max_domains`, `max_mailboxes`, `max_databases`, `max_database_users` (JAB-329)
- `max_ftp_accounts` (FTP/SFTP subaccount cap, when the module is enabled)
- PHP-INI overrides (`memory_limit`, `upload_max_filesize`, etc.)

Edit packages: `/jabali-admin/packages`. Users on a package get the new limits applied on the next reconciler tick.

## Suspension

If a user exceeds bandwidth: the reconciler sets `is_quota_suspended=1` on the user; vhosts return a "Bandwidth limit reached" page. Suspension auto-clears at the start of the next billing month (or admin clears manually).

(The "domain.Update allowlist silent drop" scar bit us here once — `is_quota_suspended` needed its own dedicated update method per column instead of a generic field-allowlist update. Fixed in PR#74.)
