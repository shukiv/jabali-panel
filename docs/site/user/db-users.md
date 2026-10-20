# Database Users

`/jabali-panel/database-users`. The DB-user accounts your applications use to connect to databases.

## Database users vs panel users

- A **panel user** is your login to the panel.
- A **database user** is an account inside MariaDB or PostgreSQL with a username, password, and per-database privileges. Applications connect using a DB user, not your panel login.

You may have many DB users (typical pattern: one DB user per app, one DB per app, with the DB user granted privileges only on its app's DB).

## Naming

DB user names are prefixed with your panel username, just like database names: `dbuser_wp` becomes `<your-username>_dbuser_wp`.

A database user name is unique on the server. A name that another database user already has is refused, and so is one ending in `_mysqladmin` or `_pgadmin`: those are the accounts phpMyAdmin and Adminer sign in with.

## Creating a DB user

Click **Create DB user**, supply:

- Username suffix.
- Password (or generate one — shown once on the success page).
- Granted databases (zero or more from your databases).

The DB user is created in the engine via the agent.

## Changing a DB user's password

Per-row **Change password**. Generates a new password (shown once) or accepts a supplied one. Existing connections using the old password remain authenticated until they disconnect; new connection attempts must use the new password.

After changing a DB user's password, update every application config that references it. Forgetting one application means the app starts failing on its next DB connect.

## Granting and revoking

Per-row **Grant / Revoke** opens a modal to add or remove databases from the user's grant set. Granting issues `GRANT ALL ON <db>.* TO <user>`; revoking issues `REVOKE`. Effects are immediate.

## Deleting a DB user

Per-row **Delete**. The agent issues `DROP USER` and tears down the engine-side account. Databases owned by the user are not deleted (database ownership is per-database, not per-user); to remove unused databases, do it from [Databases](./databases.md).

## Best practice

- One DB user per application.
- Avoid using the same DB user across multiple apps; password rotation becomes much harder.
- Avoid storing DB credentials in version control. Most application frameworks support environment variable or secret-file injection; use those.
