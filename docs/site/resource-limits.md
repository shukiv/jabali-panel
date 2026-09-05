# Resource Limits

M18. ADR-0032.

Three layers:

## 1. POSIX disk quota

Enforced on `/home`. Per-user quota set from the user's package (default + override). Hitting the soft limit warns; hitting the hard limit blocks writes (PHP-FPM logs `disk full`, Stalwart bounces with `552`, file uploads fail in the UI).

Implementation: `setquota -u <user> <soft> <hard> 0 0`. Reconciler re-applies on every tick; if a user's package quota changes the new limit lands within 60 s.

Per-tick idempotent: the reconciler compares current quota → desired quota and only calls `setquota` on diff (the "per-tick idempotent loops" audit rule — gates side-effects behind a no-change compare).

## 2. cgroup v2 slice drop-in

Per-user systemd slice (`user-<UID>.slice`). Drop-in `/etc/systemd/system/user-<UID>.slice.d/jabali-resource-limits.conf`:

```ini
[Slice]
MemoryMax=<package_memory_limit_mib>M
CPUQuota=<package_cpu_pct>%
TasksMax=<package_tasks_max>
IOWeight=100
```

This wraps **everything** the user owns: PHP-FPM worker, systemd-user timers, SSH session (if password auth is enabled), backup helper.

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
