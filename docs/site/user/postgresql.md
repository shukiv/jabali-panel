# PostgreSQL

PostgreSQL databases are managed under the same [Databases](./databases.md) and [Database Users](./db-users.md) pages as MariaDB; pick **PostgreSQL** as the engine when creating.

## Differences from MariaDB

- **Connection** — Unix socket path is `/var/run/postgresql` (not `/run/mysqld/`).
- **Web UI** — Adminer instead of phpMyAdmin. Per-row SSO works identically.
- **Naming** — same `<username>_<suffix>` prefix policy.
- **Defaults** — `client_encoding = utf8`, `timezone = UTC`. Override per-database via `ALTER DATABASE … SET …` from Adminer or `psql`.
- **Roles** — PostgreSQL's role model conflates user and group. The DB-user concept in the panel maps to a PostgreSQL role with `LOGIN` privilege.

## Connection string

```
postgres:///<your-username>_<suffix>?host=/var/run/postgresql&user=<db-user>&password=<password>
```

For libraries that take separate fields:

```
host=/var/run/postgresql
port=5432
user=<db-user>
password=<password>
dbname=<your-username>_<suffix>
```

## Extensions

PostgreSQL extensions (`postgis`, `pgvector`, `uuid-ossp`, `pg_trgm`, etc.) are enabled at the engine level by the operator. Once enabled, you may install them in your database from Adminer or `psql`:

```sql
CREATE EXTENSION pgvector;
```

If an extension you need is not enabled, contact your administrator.

## Backups

PostgreSQL databases are included in `account_full` backups via per-database `pg_dump`. There is also a per-row **Download backup** / **Restore from file** action: plain-SQL dumps replay via `psql`, and pgAdmin custom / tar archives via `pg_restore` — both through a non-superuser scoped loader into a staging database (GH #1045).

## Tuning

Per-database tuning (work_mem, shared_buffers per-tenant) is not exposed at the tenant level. Server-wide tuning is operator-controlled under [Database Tuning](../admin/database-tuning.md).

## Why both engines

PostgreSQL is preferred for new applications that benefit from its richer SQL features (window functions, JSON-with-indexes, CTEs, advisory locks). MariaDB remains the default for the vast majority of PHP applications (WordPress, Moodle, etc.) that assume MySQL semantics. The panel runs both so tenants can choose.
