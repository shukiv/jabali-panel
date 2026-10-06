# Backup Restore

Backups → Restore. Browse snapshots in any destination and restore an account or the whole system.

## Browse

Pick a destination; the page lists every snapshot (newest first) with: timestamp, kind (`account_full` / `system_backup`), subject, size, restic snapshot id.

Filter by kind, by subject (for `account_full`), by date range.

## Restore — `account_full`

Pick a snapshot, then choose:

- **Target user** — restore to the same user (overwrite), to a new user (preserve original), or to an existing different user (uncommon; usually for forensic investigation).
- **Components** — restore everything (a **Select all** toggle) or pick subsets (files, databases, mailboxes, DNS, FTP subaccounts). FTP/SFTP subaccounts are rebuilt with their original login password (GH #1361); MariaDB **and** PostgreSQL databases are covered.
- **Dry run** — see what would be touched without writing.

A single domain's document root can be restored on its own (GH #1359) without
touching the rest of the account.

The agent restores in the same per-stage order the backup ran: files first, databases second, mailboxes third, DNS records last. Each stage is idempotent at the file or row level.

### SSL certificates

An account backup doesn't include the domains' certificate files (a
full-server backup does). When an account restore brings back a domain whose
certificate isn't on this server, because the backup came from another server
or the files are gone, the panel issues a new one as it does for a new
domain: Let's Encrypt, once the domain's DNS points at this server
(GH #1993). The restore report lists each domain this applies to.

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

### What an uploaded archive can't restore

An uploaded archive is a file anyone could have written, so the panel restores
it into the target account only (GH #1993):

- **Databases and database users** — the account's own, or new ones named
  `<account>_<name>`. Never this server's own databases or MariaDB accounts,
  another account's, or a database that already exists here without belonging
  to the account. One an admin created without the account prefix is refused;
  restore it by hand.
  A database comes back in the panel only when its data was restored too.
- **Mail** — only for the account's own domains, including the ones the
  archive brings: mail is restored last, after the account's domains.
- **Docker apps** — the account's own, or an app name not in use here.
  Server-level apps are not restored, and neither is an app whose name another
  account's app uses. An app comes back in the panel only when its data was
  restored too.
- **Custom nginx directives** on a domain are left out. Re-add them in the
  domain's settings after reviewing them.
- **Sign-in** — the account's sign-in from the backup (its password) is not
  imported. The account keeps the sign-in it has on this server; an account
  the restore creates gets a new password and needs a recovery link.

Each item left out is listed in the restore report. The server agent must be
as new as the panel: an older agent can't confine the restore, so the panel
refuses it (`agent_update_required`).

## Operator-only safety rails

- A restore into an existing user requires typing the user's username as confirmation.
- A `system_backup` restore into a non-empty panel database (i.e. not a fresh host) requires `--force-overwrite`. The default refuses to clobber.
- Every restore writes one `backup.restore` audit row plus per-stage rows.

## What restore does *not* do

- Re-issue Let's Encrypt certificates after a full-server restore — they are restored from the snapshot. Run `jabali ssl renew <domain>` for any cert whose expiry is near. (An account restore issues new ones when the files aren't on this server; see [SSL certificates](#ssl-certificates).)
- Reconcile listen IPs — if the new host has different IPs than the snapshot's host, the operator must update [IP Addresses](./ip-addresses.md) before the reconciler succeeds.
- Restart third-party services not under the panel's control.

## CLI

```bash
jabali backup account-restore --user <username> --snapshot <id> --destination recovery --force
jabali system restore --remote-url <restic-repo-url> --credentials-ref /root/recovery.env --password-file /root/restic.password --snapshot <id> --force
```
