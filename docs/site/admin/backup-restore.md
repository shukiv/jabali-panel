# Backup Restore

Backups → Restore. Browse snapshots in any destination and restore an account or the whole system.

## Browse

Pick a destination; the page lists every snapshot (newest first) with: timestamp, kind (`account_full` / `system_backup`), subject, size, restic snapshot id.

Filter by kind, by subject (for `account_full`), by date range.

## Restore — `account_full`

Pick a snapshot, then choose:

- **Target user** — restore to the same user (overwrite), to a new user (preserve original), or to an existing different user (uncommon; usually for forensic investigation).
  An account deleted and created again with the same username counts as the
  same user: the restore rebuilds its domains, databases, mailboxes and other
  panel records on it (GH #1993).
- **Components** — restore everything (a **Select all** toggle) or pick subsets (files, databases, mailboxes, DNS, FTP subaccounts). FTP/SFTP subaccounts are rebuilt with their original login password (GH #1361); MariaDB **and** PostgreSQL databases are covered.
- **Dry run** — see what would be touched without writing.

A single domain's document root can be restored on its own (GH #1359) without
touching the rest of the account.

The agent restores in the same per-stage order the backup ran: files first, databases second, mailboxes third, DNS records last. Each stage is idempotent at the file or row level.

### SSL certificates

An account backup carries each domain's certificate and private key (GH #1993)
when the certificate was issued by a certificate authority or uploaded on the
custom certificate page, and covers only the account's own domains. A
self-signed certificate isn't carried, and neither is one that also covers a
name outside the account, such as a wildcard of the panel's own domain. A
backup made before this release carries none. Keep downloaded backups private:
they hold the domains' private keys.

When an account restore brings back a domain whose certificate isn't on this
server, because the backup came from another server or the files are gone:

- **A backup from this server's destinations**, or `jabali backup account-restore`,
  installs the backup's certificate when it pairs with its key, covers the
  domain, and is valid for at least another day.
- **An uploaded file** installs it only when **Keep the backup's SSL
  certificates** is checked in the restore drawer. It is off by default,
  because the file's private keys come from outside this server. The
  certificate must also be signed by a certificate authority this server
  trusts. The account restores of a full-server restore don't install them.
- A domain whose ownership isn't verified gets no restored certificate, except
  one with a custom certificate.

A restored certificate from Let's Encrypt or another certificate authority
serves until 21 days before it expires. From then on, Let's Encrypt issues the
domain's own certificate as soon as it can validate the name here: the
domain's DNS points at this server, or this server can write its DNS-01
record. A domain whose DNS still points at the old server, or one behind a
proxy without DNS-01, keeps the restored certificate until it expires. A
restored custom certificate stays the owner's to replace before it expires.

Any other domain gets a new certificate as a new domain does: Let's Encrypt,
once its DNS points at this server. A domain that used a shared certificate
gets one of its own, because the shared certificate belongs to the server. A
domain with a custom certificate that isn't installed switches to Let's
Encrypt. The restore report lists each domain whose certificate wasn't
installed, and why.

### Database users and their passwords

A restore recreates each restored database user's MariaDB account or
PostgreSQL role and its grants, not just its row in the panel, so a site's
database login keeps working on a new server (GH #1993). A backup records each
database user's password hash for this: the MariaDB hash, or the PostgreSQL
SCRAM-SHA-256 verifier.

- A user from a backup made before this release has no recorded password. It
  comes back with a new one: set a password under **Databases** and in the
  site's configuration. The restore report lists each such user. So does a
  PostgreSQL role whose password the source server stored as md5: an md5 hash
  works only under the role's original name.
- A database user the account already has keeps its password; the restore adds
  only the grants it restores.
- A database user is not restored when a MariaDB account or PostgreSQL role
  with its name already exists on this server and is not the account's, or
  when another account has a database or database user with its name (an older
  backup can hold one the account has since handed over). The restore report
  says which.
- The restore never touches this server's own databases, MariaDB accounts or
  PostgreSQL roles (`mysql`, `root`, `postgres`, `jabali_*`, …), nor the
  account's phpMyAdmin account or Adminer role.
- A grant joins a database user and a database of the same engine only.
- A PostgreSQL role gets the access the **Databases** page gives: all of each
  database it is granted, and that database's `public` schema with its tables
  and sequences.

The server agent must be as new as the panel to create the accounts and
roles. With an older agent the restored database users are left out and the
report says to run `jabali update`.

### PostgreSQL databases

A restore loads each PostgreSQL database's dump as a role with no server-wide
rights, never as the server's `postgres` role (GH #1993). The dump goes into a
new database, which takes the old one's place only once the load succeeds. A
dump that fails to load leaves the database as it was, and the restore report
gives the loader's error. A dump that needs an extension only a superuser can
create doesn't load.

- **Who owns the restored tables.** A restored database's tables, views,
  sequences and functions belong to its first database user: the first one
  granted on it. That user can alter and drop them, so the site's migrations
  run after a restore.
- **A database with no database user.** Its restored tables belong to a role
  that can't sign in, until a database user is granted on it: the first user
  you grant on it under **Databases** takes them over. The restore report
  lists each such database.
- **Access.** The roles that could connect to the database before the restore
  can connect to the restored one.
- **Overwrite.** With **Overwrite existing items with the backup** on, a
  database that already has data is replaced by the backup's: what isn't in
  the backup is gone from it.

### DNS records

A restore brings back each domain's custom DNS records: the ones added under
DNS (GH #1993). The panel rebuilds its own records for the domain (its address,
mail, SPF and DKIM) itself. The custom records are added once the domain's DNS
zone exists; for a domain the restore brought back, the panel creates the zone
first, which can take up to a minute. Each record goes through the same checks
as a record added under DNS:

- A record already in the zone is left as it is. Nothing in the zone is
  removed.
- A record that isn't valid, or that clashes with one in the zone (a CNAME at
  a name that has other records), isn't added.
- An address record (A or AAAA) at a name where this server publishes its own
  address, such as the domain itself, isn't added, so the site keeps pointing
  at this server.

No records are restored for a domain whose DNS is hosted elsewhere, whose
ownership isn't verified yet, or whose zone is disabled. Each record left out
is listed in the restore report.

## Restore — `system_backup`

System restores are typically performed on a freshly-bootstrapped panel host. Sequence:

1. Provision a clean Debian 13 host and run the standard `bash install.sh`.
2. Configure access to the backup destination (or copy credentials over).
3. On the new panel host:

   ```bash
   jabali system restore --remote-url s3:s3.amazonaws.com/<bucket>/<path> --credentials-ref /root/recovery.env --password-file /root/restic.password --snapshot latest --force
   ```

   `/root/recovery.env` holds the storage credentials (root:root, 0600). `/root/restic.password` holds the repository password: the one `jabali backup destination rotate-password` printed.

4. The restore covers all 7 stages: panel-DB × 3, OS users, Stalwart state, Kratos state, hosted sites, config snapshot, plus the wrapping restic snapshot integrity check.
5. After completion, run `jabali repair --diagnose` to surface any drift between restored state and the fresh host (typically only IP-related mismatches if the new host has a different IP).

Round-trip restore was live-verified on 192.168.100.150.

## Restore from an uploaded archive

Besides restic destinations, both account and full-server restores accept a
backup **archive you upload** directly (GH #1408) — the portable path for moving
to a host that shares no restic destination with the source:

- **Account** — upload an account backup and restore selected legs. Tenants can
  do this for their own account (self-service restore-from-upload).
- **Full server** — `jabali system restore --from-tar <file>` rebuilds the system
  leg and every account from a Full Server container, **creating missing OS
  users** and even restoring an account into a user that doesn't yet exist
  (create-from-manifest).

### Uploaded backups stay on the server

An account backup an admin uploads is kept on this server and listed in the
**Backups** tab under **Uploaded backups** (GH #1993). A failed restore can be
retried from there without uploading the file again. Before the upload, choose
what happens to it after the restore:

- **Keep it on this server until I delete it** (the default).
- **Keep it for 7 days** — removed automatically after that.
- **Delete it once a restore succeeds** — a failed restore keeps it for a
  retry. The entry stays listed for a day so its restore report can be read.

Each entry shows the account, the file and its size, when it was uploaded, how
long it is kept, and its last restore. **Restore** restores it again; when its
account is not on this server, the account can be created from the backup.
**Delete** removes the file (accounts already restored from it are not
changed). One backup can't be
restored twice at once, or deleted while a restore of it runs.

The files live in `/var/lib/jabali-uploads/kept/` and count against the
server's disk. The 12-hour cleanup of `/var/lib/jabali-uploads` skips that
directory. A tenant's own restore from upload (in their account) keeps
nothing: the file is deleted once the restore ends.

### Checks before a restore

Before **Restore**, the restore drawer checks the uploaded backup against this
server and lists what it found (GH #1993). The panel runs the same check again
when the restore starts, so a backup the check blocks is never restored.

- **PHP versions** — every PHP version the account's sites use must be
  installed here. A missing one blocks the restore: install it under **PHP
  Versions**, then restore.
- **PHP extensions** — the extensions the source server had enabled for those
  versions, other than the ones built into PHP. One this server lacks is a
  warning: the restore runs, and a site that needs it doesn't work until you
  enable it under **PHP Versions**. A backup made before Jabali recorded the
  extensions can't be checked for them, and the drawer says so.
- **PostgreSQL**, **mail**, **DNS** and **Docker apps for users** — when one
  is turned off on this server and the backup has some of it, the drawer warns
  that it won't be restored, and the restore leaves it out:
  - PostgreSQL off: the backup's PostgreSQL databases and their users;
  - mail off: its mailboxes (with their auto-replies and shares) and
    forwarders. The domains keep their mail settings;
  - DNS off: its custom DNS records;
  - Docker apps for users off, or the Docker engine not installed: its Docker
    apps.

  The restore report lists each part left out. To restore one, turn it on
  under **Server Settings** first, then restore again.

The check reads the archive, so it can take a minute or two for a large
backup. The server agent must be as new as the panel to run it; with an older
agent the restore is refused until the agent is updated. A tenant's restore of
their own account and a full-server restore (`jabali system restore
--from-tar`) run no check and leave nothing out.

### Progress

A restore from an uploaded backup shows its progress while it runs, in the
restore drawer and in the **Uploaded backups** list (GH #1993):

1. **Restoring files, databases and apps** — first unpacking the backup (with
   a percentage), then each part in turn ("Restoring database shop_wp (2 of 4)").
2. **Rebuilding the account's domains, mailboxes and settings.**
3. **Restoring mail** — only when the backup has mail and it was selected.
4. **Restoring DNS records** — the backup's custom ones; skipped when DNS is turned off here.

A tenant restoring their own account sees the same detail for its one step.
The server agent must be as new as the panel to report what a step is doing;
with an older agent only the step is shown.

### Keep or overwrite what is already there

A restore from an uploaded archive adds only what the account is missing
(GH #1993). **Overwrite existing items with the backup** in the restore drawer
is off by default:

- **Off** — files already in the home stay as they are; only missing files are
  added. A MariaDB database that already has tables, a PostgreSQL database
  that already holds anything (a table, view, sequence, function or large
  object), or a Docker app folder that already has files, is skipped and
  listed in the restore report. An existing
  mailbox keeps its auto-reply. A PHP pool the account has keeps its process
  settings, and a PHP setting it has keeps its value; the backup's other PHP
  settings are added. Mail already there is not copied twice.
- **On** — the backup replaces the account's data, as restores did before:
  the home is mirrored from the backup (files added since are deleted),
  databases are reloaded from their dumps, and Docker app folders are
  replaced. On an admin restore, the mailboxes, database users, PHP pools and
  domains the account already has also take the backup's settings:
  - a mailbox takes the backup's quota and whether it is disabled. It keeps
    its password, because the mail already in it didn't come from the archive;
  - a database user takes the backup's password only when every database it
    can open holds nothing but the archive's data (see below). Otherwise it
    keeps its password;
  - a PHP pool takes the backup's process settings (mode, max children, idle
    timeout), held to the same rules as a restored pool (see below). Settings
    the PHP pool page would refuse leave the pool's own in place. A PHP
    setting both have takes the backup's value; one only the pool has stays;
  - a domain takes the backup's on/off state, redirect-all, index priority,
    PHP limits, rate and connection limits, PHP pool, catch-all and outbound
    disclaimer, through the checks its own pages run. A PHP limit the
    account's hosting package lets only an administrator set stays as it is.
    The catch-all is taken only when it points at a mailbox the account had
    on this server before the restore. A backup without a catch-all or a
    disclaimer clears the domain's. The domain's name, document root, SSL,
    DKIM, DNSSEC, custom nginx directives and mail provider stay as they are.

  The restore report lists each password kept, and why.

Restoring into a home that may hold the tenant's symlinks, the copy that keeps
existing files never follows a symlink: whatever the tenant has at a name,
link or not, counts as already there. The tenant's own restore offers the same
choice. A full-server restore (`jabali system restore --from-tar`) replaces,
as before, and leaves the settings of the rows each account already has.

Keeping needs an agent as new as the panel; an older one would replace, so
the panel refuses that restore (`agent_update_required`). With **Overwrite**
on, an older agent still restores.

### What an uploaded archive can't restore

An uploaded archive is a file anyone could have written, so the panel restores
it into the target account only (GH #1993):

- **Databases and database users** — the account's own, or new ones named
  `<account>_<name>`. Never this server's own databases, MariaDB accounts or
  PostgreSQL roles, another account's, or a database that already exists here
  without belonging
  to the account. One an admin created without the account prefix is refused;
  restore it by hand.
  A database comes back in the panel only when its data was restored too.
  A grant from the archive is made only on a database that holds nothing but
  the archive's data: one that was new or empty before the restore (no
  tables, views, stored routines or events, and in PostgreSQL no large
  objects) and whose data loaded without an error. A database that already held something is still reloaded from the
  dump with **Overwrite** on, but the archive's database users get no access
  to it, and the restore report says so. The archive's author never gets a
  login to data they didn't supply.
- **Mail** — only for the account's own domains, including the ones the
  archive brings: mail is restored last, after the account's domains.
- **PHP pools** — a pool's process settings that the PHP pool page would
  refuse are replaced by the defaults, and its max children is held to the
  account's package cap. A PHP setting the pool page would refuse (a control
  character in a value, a flag other than on or off) is left out, whether
  **Overwrite** is on or off.
- **Docker apps** — the account's own, or an app name not in use here.
  Server-level apps are not restored, and neither is an app whose name another
  account's app uses. An app comes back in the panel only when its data was
  restored too.
- **Symbolic links** — restored inside the account's home and an app's data
  folder, as the backup has them. An archive with a symbolic link anywhere
  else is refused before anything is restored.
- **Custom nginx directives** on a domain are left out. Re-add them in the
  domain's settings after reviewing them.
- **Index priority** on a domain that the domain page doesn't offer is left
  out; the domain gets the default.
- **PHP limits** on a domain (memory, upload and post size, input variables,
  execution and input time) that the domain's PHP settings page would refuse
  are left out, and so is one the account's hosting package lets only an
  administrator set. When the package can't be read, every limit is left out.
  The domain uses the server's defaults for them.
- **FTP passwords** — an FTP or SFTP subaccount's password comes back only
  into an account the restore created, whose home holds nothing but the
  archive's data (GH #1993). It is taken only for the account's own
  subaccounts (`<account>_<name>`) that aren't on this server yet. An FTP
  subaccount restored into an account that was already here gets a new
  password, and so does one from a backup made before Jabali kept FTP
  passwords: the restore report lists each one, and the account sets a new
  password under **FTP Accounts**. An account created by an earlier attempt
  at the same restore counts as already here.
- **SSH keys** — restored only into an account the restore created, for the
  same reason as FTP passwords (GH #1993). Into an account that was already
  here, each key the account doesn't have is listed in the restore report:
  check it, then add it under **SSH Keys**.
- **Sign-in** — the account's sign-in from the backup (its password) is not
  imported. The account keeps the sign-in it has on this server; an account
  the restore creates gets a new password and needs a recovery link.
- **DNS records** — only the record types the account may add under DNS
  (never NS). Address records that don't point at this server are listed in
  the report so you can check them.

Each item left out is listed in the restore report. The server agent must be
as new as the panel: an older agent can't confine the restore, so the panel
refuses it (`agent_update_required`).

## Operator-only safety rails

- A restore into an existing user requires typing the user's username as confirmation.
- A `system_backup` restore into a non-empty panel database (i.e. not a fresh host) requires `--force-overwrite`. The default refuses to clobber.
- Every restore writes one `backup.restore` audit row plus per-stage rows.

## What restore does *not* do

- Re-issue Let's Encrypt certificates after a full-server restore — they are restored from the snapshot. Run `jabali ssl renew <domain>` for any cert whose expiry is near. (An account restore installs the backup's certificates or issues new ones when the files aren't on this server; see [SSL certificates](#ssl-certificates).)
- Reconcile listen IPs — if the new host has different IPs than the snapshot's host, the operator must update [IP Addresses](./ip-addresses.md) before the reconciler succeeds.
- Restart third-party services not under the panel's control.

## CLI

```bash
jabali backup account-restore --user <username> --snapshot <id> --destination recovery --force
jabali system restore --remote-url <restic-repo-url> --credentials-ref /root/recovery.env --password-file /root/restic.password --snapshot <id> --force
```
