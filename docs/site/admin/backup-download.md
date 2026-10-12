# Backup Download (Admin)

Backups → Download. Generate a single on-demand `account_full` snapshot and offer it as a download.

## When to use

- A tenant requests a one-off copy of their data outside the schedule cadence.
- Migration off the panel: produce a final snapshot and hand the snapshot file (or a temporary destination URL) to the tenant.
- Compliance: produce a point-in-time export for audit.

## Flow

1. Pick the user (or "this admin's own account" for an operator-account snapshot).
2. Pick a destination (typically a temporary one such as `local:/var/lib/jabali/exports/` so the file is reachable for the download step).
3. The agent runs `account_full` against that destination.
4. The page displays the snapshot id and a link to download the restic snapshot as a tarball.
5. The download link is short-lived (15 minutes by default) and accessible only to the requesting admin.

## File name

A download is named for the account and the time the backup was made (UTC), for example `backup_alice_2026-10-11_0430.tar.zst`. A system backup uses `system` in place of the account, and a Files, Database or Folders backup adds its content (`backup_alice_files_2026-10-11_0430.tar.zst`). If the account no longer exists, the job ID takes its place. On a server without `zstd`, the archive is an uncompressed `.tar`.

## What the tarball contains

- `files/` — the user's home directory tree.
- `databases/` — per-database SQL dumps (mariadb `mysqldump`, postgresql `pg_dump`).
- `mail/` — per-mailbox JMAP export.
- `dns/` — zone files for the user's domains.
- `manifest.json` — kind, subject, timestamp, snapshot id, restic repository fingerprint.

## What the tarball does *not* contain*

- Cleartext mailbox passwords (only the Argon2id hashes).
- Linux account passwords (passwords are not stored — SSH key auth only).
- Database root password (admin-scope; per-DB user passwords are included if requested).

## Recipient verification

The download endpoint requires the same admin session that initiated the snapshot. There is no public URL; sharing the link with a tenant is not supported by this flow (use SFTP or the tenant's own [Backup Download](../user/backup-download.md) page for that).

## Cleanup

Temporary local exports are purged daily by a systemd timer (`jabali-backup-exports-prune.timer`) after 24 hours. Manually clean with:

```bash
trash /var/lib/jabali/exports/*
```
