---
title: Backups and Restore
---

This guide covers backup strategy, schedules, retention, and restores.

## Backup types

- **Admin backups**: system-wide backups managed by administrators.
- **User backups**: per-account backups for self-service restore.

## Recommended strategy

- Keep at least one offsite destination.
- Use multiple retention windows (daily/weekly/monthly).
- Test restores regularly on a staging account.

## Configure schedules (admin)

1) Open **Admin → Backups**.
2) Add destinations (local and/or remote).
3) Create schedules and set retention.
4) Validate the first run completes successfully.

## User backups

Users can trigger and manage their own backups under **User → Backups**.

## Restore workflow

1) Choose a backup snapshot.
2) Select a restore scope (full account, database, or files).
3) Confirm the target account and overwrite options.
4) Review the restore logs and verify services.

## CLI examples

Create a backup for a user:

```
jabali backup create <user>
```

Restore a backup to a user:

```
jabali backup restore <path> --user=<user>
```

## Troubleshooting

- If restores fail, check `jabali-agent` and `jabali-queue` status.
- Review logs in `/var/www/jabali/storage/logs` and systemd logs.
