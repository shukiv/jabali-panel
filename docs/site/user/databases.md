# Databases (User)

`/jabali-panel/databases`. Your MariaDB and PostgreSQL databases.

## Per-row data

- Database name
- Engine (MariaDB / PostgreSQL)
- Default DB user
- Size on disk
- Created

## Database naming

Database names are prefixed with your username for isolation: a database you create with name suffix `wp_site` becomes `<your-username>_wp_site`. The prefix is enforced server-side.

## Adding a database

Click **Create database**, supply:

- Engine (MariaDB or PostgreSQL).
- Name suffix.
- Default DB user — pick an existing DB user or create one in the same wizard. The DB user is granted `ALL PRIVILEGES` on the new database.

The database count is checked against your package's `max_databases`.

## phpMyAdmin / Adminer SSO

Each row has an **Open phpMyAdmin** (MariaDB) or **Open Adminer** (PostgreSQL) button. Clicking it:

1. Issues a single-use, short-TTL **SSO token** (panel-internal).
2. Redirects to the web admin URL with the token.
3. The web admin authenticates as the corresponding **shadow account** (CONTEXT.md: SSO Token Resolution).
4. The token is consumed and cannot be reused.

You arrive already logged in to phpMyAdmin (MariaDB) / Adminer (PostgreSQL) with the DB user's privileges.

## Download + Restore from file

Each row also has **Download backup** and **Restore from file** (both engines):

- **Download** streams a dump of that one database.
- **Restore from file** uploads a dump and replaces the whole database. The
  upload is chunked and async (it beats Cloudflare's ~100 MB origin limit), with
  a progress modal. PostgreSQL accepts plain-SQL **and** pgAdmin's custom / tar
  archive formats and shows the real error on failure. Uploaded dumps run through
  a non-superuser scoped loader into a staging database and only swap onto the
  live name on success, so a bad upload never wipes your database.

## Connecting from an application

Your application connects via a Unix socket (the panel runs MariaDB with `skip-networking`; tenant connections happen over the socket):

```
host=/run/mysqld/mysqld.sock
user=<db-user>
password=<password>
dbname=<your-username>_<suffix>
```

For PostgreSQL:

```
host=/var/run/postgresql
user=<db-user>
password=<password>
dbname=<your-username>_<suffix>
```

PHP applications use the same socket path implicitly when host is set to `localhost`.

## Redis

The **Redis access** card on this page gives you a Redis credential for your
applications. Redis is reachable only over the unix socket
`/run/redis/redis.sock`, as the user `t_<your-username>`. Your keys must start
with the prefix `jt:<your-username>:`; set it as your client's key prefix (for
example phpredis `Redis::OPT_PREFIX` or Laravel `REDIS_PREFIX`). The credential
can read and write only keys under that prefix.

Commands that work across every user's keys are not available: `KEYS`, `SCAN`,
`FLUSHDB`, `FLUSHALL`, `CONFIG` and the other admin commands. A `FLUSHALL` from
your application fails with `NOPERM`.

### Flushing your keys

To delete all your keys, use **Flush my Redis keys** on the card. It deletes
every key under your prefix, in every Redis database, and nothing else.

An application can do the same through the API:

```
curl -X POST -H "Authorization: Bearer <token>" \
  https://<panel-host>/api/v1/me/redis-access/flush
```

Create the token under **API Tokens** with **Custom** permissions and only
**Redis: Flush**. That token can flush your Redis keys and do nothing else.

The response is `{"deleted": <n>, "complete": true}`. One call runs for at most
20 seconds; if you have more keys than that, `complete` is `false` and you call
it again. Flushes are rate-limited per user.

## Backups

Database content is included in `account_full` backups. For a single database, use the per-row **Download backup** / **Restore from file** actions above, or phpMyAdmin's / Adminer's own **Export** feature.

## Deleting a database

Per-row **Delete**. Destructive. The DB user remains (it may own other databases); delete the user separately under [Database Users](./db-users.md) when no databases reference it.

## CLI

If you have shell access (operators only):

```bash
jabali db list --user <your-id>
jabali db create --user <your-id> --name <suffix>
jabali db delete <id>
```
