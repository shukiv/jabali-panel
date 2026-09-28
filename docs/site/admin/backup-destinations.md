# Backup Destinations

Backups → Destinations. The list of repositories the panel can write restic snapshots to.

## Supported destination types

| Type | Configuration |
|---|---|
| **Local** | Filesystem path on the panel host. Cheapest, no off-host disaster recovery. |
| **SFTP** | Host, user, SSH key path. Off-host on a server you control. |
| **S3** | Endpoint, region, bucket, access key, secret key. Works against AWS S3, MinIO, Wasabi, Cloudflare R2, any S3-compatible service. |
| **Backblaze B2** | Account ID, application key, bucket. |
| **Azure Blob** | Account name, account key, container. |
| **Google Cloud Storage** | Bucket, path to a service-account JSON key. |
| **Restic REST server** | URL, optional bearer token. For self-hosted restic-rest-server (HTTPS recommended; `JABALI_RESTIC_INSECURE_TLS` to allow self-signed). |

## Adding a destination

1. **Add destination** → pick a type → fill in credentials.
2. The form refuses to save until the credentials parse syntactically.
3. Click **Test** before going live. The test action:
   - Connects to the destination.
   - Verifies write permission.
   - If no restic repository exists at the chosen path, runs `restic init` (auto-init).
4. On success, the destination is available for selection in [Schedules](./backup-schedules.md).

## Repository password

Each destination's restic repository password is stored AES-256-GCM-sealed on its row in the panel database. A destination whose password was never rotated uses the shared legacy password file. The UI never shows the password, and restic cannot read snapshots without it. To get a copy for emergency recovery, rotate it: `jabali backup destination rotate-password <id-or-name>` re-keys the repository, keeps every snapshot readable, and prints the new password once.

## Multi-destination by schedule

A schedule may target multiple destinations. Restic writes to each in turn. Bandwidth and storage cost are paid once per destination; deduplication happens per repository, not across repositories.

## Health

The panel tests a destination only when you click **Test** or run `jabali backup destination test`. It does not re-test destinations on a schedule. A backup that fails fires `backup.fail` ([Notifications Events](./notifications-events.md)).

## Removing a destination

Forbidden if any schedule targets it. Reassign or delete the schedules first.

## CLI

```bash
jabali backup destination list
jabali backup destination get <id-or-name>
jabali backup destination create --kind sftp --name daily-offsite --url sftp:backups@backup.example.com:/srv/restic
jabali backup destination update daily-offsite --sftp-auth key --sftp-key-path /root/.ssh/backup
jabali backup destination test daily-offsite
jabali backup destination delete daily-offsite
```
