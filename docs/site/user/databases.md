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
