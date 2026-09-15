# Hosting Packages

`/jabali-admin/packages`. A **Package** is a bundle of quotas and limits assigned to a user.

## Fields

| Field | Unit | Purpose |
|---|---|---|
| Name, slug, description | — | Identifier and display label |
| `disk_quota_mib` | MiB | POSIX disk quota applied to `/home/<user>` |
| `bandwidth_quota_gib` | GiB / month | Outbound bandwidth cap; tracked separately, suspends user if exceeded |
| `memory_limit_mib` | MiB | `MemoryMax=` on the user's systemd slice |
| `cpu_pct` | % | `CPUQuota=` on the user's systemd slice |
| `tasks_max` | int | `TasksMax=` on the user's systemd slice |
| `req_per_sec`, `req_burst` | per-IP | nginx `limit_req` rate and burst for the user's vhosts |
| `max_domains`, `max_mailboxes`, `max_databases`, `max_cron_jobs` | int | Hard caps enforced at create time |
| `php_versions_allowed` | list | Subset of installed PHP versions the user may pick |
| `php_ini_overrides` | object | Caps for `memory_limit`, `upload_max_filesize`, `max_execution_time`, `post_max_size`, `max_input_vars` |
| `apps_allowed` | list | Subset of [Applications](./applications.md) the user may install |
| `egress_policy` | enum | `default-restricted` (allow 443 + mail) or `unrestricted` |
| `webmail_enabled` | bool | Whether tenants on this plan get webmail (the Bulwark UI). Defaults **ON**, including the auto-assigned `default` package. GH #1628 slice 1 only stores and edits this flag — the webmail reconciler does not read it yet (that rewire, which also replaces the per-user webmail toggle, lands in a follow-up). Set it per package via the editor or `--webmail=false` on the CLI. |

## List page

Each row shows the package name, the number of users assigned, total quota allocated, and total disk in use across those users.

Actions: Edit, Delete, Duplicate.

## Lifecycle

- **Create**: see [Create Package](./hosting-packages-create.md).
- **Edit**: see [Edit Package](./hosting-packages-edit.md). Changes converge to every assigned user on the next reconciler tick (within 60 seconds).
- **Delete**: forbidden if any user is assigned. Reassign users first.

## Default package

The installer creates a `default` package suitable for small VPS scale (10 GiB disk, 1 GiB RAM cap, 50% CPU, 5 domains, 25 mailboxes, 5 databases). Edit it freely; the row identity is the slug `default`, so do not rename it if you rely on the auto-assignment to "default" when creating users without an explicit package.

## CLI

```bash
jabali package list
jabali package create --name standard --disk-quota-mib 5120 --memory-limit-mib 512 --cpu-pct 25 …
jabali package update <id> --memory-limit-mib 1024
jabali package delete <id>
```
