# Users

`/jabali-admin/users`. The list and lifecycle controls for every panel user (administrators and hosting customers).

## What is a User

A panel user combines several pieces of state:

- A Kratos identity (email, password credential, optional TOTP).
- A row in the panel `users` table (display name, role, package assignment, suspension flags).
- A Linux account under `/home/<username>` with disabled shell login (used by SFTP, PHP-FPM, per-user cron, and per-user egress firewall slice).
- A per-user PHP-FPM pool socket at `/run/php/jabali-<username>/fpm.sock`.

The reconciler converges Linux account state, PHP pool drop-ins, quota assignments, and slice limits from the `users` table on each tick.

## List

Columns: username, email, role (admin / user), package, domains, disk used / quota, suspended flag, last login.

Filters: by role, by package, by suspension state, by free-text search across email + username + primary domain.

## Actions per row

- **Edit** — opens [Edit User](./users-edit.md).
- **Reset password** — generates a new password (shown once); writes a Kratos credential update.
- **Reset 2FA** — strips TOTP and recovery codes; the user must re-enrol on next login.
- **Suspend** — sets `users.is_suspended=1`; the reconciler returns a "suspended" page on every vhost owned by the user.
- **Delete** — destructive; removes domains, databases, mailboxes, OS account, `/home/<user>`, and all related rows.

## Create

Top-right **Create User** button opens [Create User](./users-create.md).

## CLI parity

Every operation here has a CLI equivalent:

```bash
jabali user list
jabali user create --username … --email … --package … --primary-domain …
jabali user password <email>
jabali user 2fa-reset <email>
jabali user delete <email>
```

Direct-DB path (M20-safe); bypasses the HTTP layer, runs from the panel host.

Deleting a user now removes its Kratos identity as well as the DB row and OS account, so the username and email free up immediately and the same user can be recreated. To clear an identity orphaned by an older build (a delete that left the identity behind, so recreate failed with "username taken"):

```bash
jabali admin purge-orphan-identities                       # dry-run, lists orphans
jabali admin purge-orphan-identities --username <name> --apply
```

## Audit

Every action above writes an audit row: actor user, subject user, action, result. See [Audit Log](./audit-log.md).
