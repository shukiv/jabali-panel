# Database Tuning

Server Settings → Database → **Tuning**. The curated MariaDB / PostgreSQL configuration tuner. M46, ADR-0098.

## What can be tuned

Only whitelisted keys may be edited from the panel. The whitelist exists because uncurated `[mysqld]` settings can break replication, crash recovery, or memory accounting in ways that are very hard to revert from the UI.

### MariaDB keys (representative)

| Key | Default | Notes |
|---|---|---|
| `innodb_buffer_pool_size` | 25% of RAM | The single biggest dial. Larger is better up to ≈70% of RAM. |
| `innodb_log_file_size` | 96 MiB | Larger reduces checkpoint pressure on write-heavy workloads. |
| `max_connections` | 200 | Raise only when you observe `Too many connections` errors. |
| `query_cache_size` | 0 | MariaDB 10.3+ deprecates the query cache; the panel keeps it off. |
| `thread_cache_size` | 16 | Raise on bursty connection patterns. |
| `tmp_table_size` / `max_heap_table_size` | 32 MiB each | Increase if `Created_tmp_disk_tables` is high. |

### PostgreSQL keys (representative)

| Key | Default | Notes |
|---|---|---|
| `shared_buffers` | 25% of RAM | Equivalent to InnoDB buffer pool. |
| `effective_cache_size` | 50% of RAM | Planner hint. |
| `work_mem` | 16 MiB | Per-sort, per-hash. Beware multiplication by concurrent connections. |
| `maintenance_work_mem` | 256 MiB | Used during `VACUUM`, `CREATE INDEX`. |
| `max_connections` | 100 | Raise sparingly; pair with a pooler at high counts. |

## Apply flow

1. The admin edits the values.
2. Click **Apply**. Each change writes to the `db_tuning` table.
3. The reconciler (`db_tuning_reconcile.go`) renders the override file (`/etc/mysql/mariadb.conf.d/jabali-tuning.cnf` for MariaDB, `postgresql.auto.conf` for PostgreSQL).
4. The agent issues `db.config.apply`, which sets dynamic variables in-place when possible (no restart) and restarts the daemon when the key requires it.

The action is audited and announced via [Notifications](./notifications-events.md).

## Why "curated"

A free-text `[mysqld]` block exposed to operators historically produced two failure modes:

1. Typos that silently dropped settings (`innodb_buffer_pool_siz = ...`).
2. Settings that interacted in non-obvious ways with `innodb_log_file_size` or page-size assumptions and broke crash recovery.

The whitelist enforces correctness at submit time and rejects out-of-range values before they reach the daemon.

## Custom tuning beyond the whitelist

Edits to `/etc/mysql/mariadb.conf.d/zz-custom.cnf` (operator-owned) are preserved across `jabali update`. The reconciler does not touch this file. Use this path for `[client]` section changes, alternate storage engine configs, etc., that the curated tuner does not cover.

## CLI

```bash
jabali db config get                           # the tunable parameters and their current values
jabali db config set <param=value> [...]       # set, apply, and restart the engine if needed
```

`set` accepts only parameters on the tuner's allowlist. Add `--engine postgres` to tune PostgreSQL.
