# Backups

Two backup kinds:

- **account_full** — one user's entire account (home dir + DBs + mail + DNS + cron + apps).
- **system_backup** — the whole panel host (panel DB × 3 + OS users + every site).

Both are restic-backed (deduplicated, encrypted at rest, multi-destination).

Beyond the scheduled restic snapshots, Jabali also supports **portable container
backups** (a single downloadable archive) and **restore-from-upload**, so an
account — or a whole server — can be rebuilt on a host that has no restic
destination in common with the source (GH #1408). See
[Full Server backup / restore](#full-server-backup--restore) and
[Restore from an uploaded archive](#restore-from-an-uploaded-archive).

## Destinations

`/jabali-admin/backups` → Destinations:

| Type | Config |
|---|---|
| Local | filesystem path on the panel host |
| SFTP | host, user, key path |
| S3 | endpoint, bucket, access key + secret |
| Backblaze B2 | account ID, application key |
| Azure Blob | account name, key, container |
| Google Cloud Storage | bucket, service-account JSON |
| Restic REST server | URL, optional auth |

Multiple destinations per backup are supported — restic writes to each.

## Schedules

`/jabali-admin/backups` → Schedules:

- Pick the backup kind, the user(s) (for `account_full`), the destination, the cron schedule, and the retention policy.
- Schedules become `systemd` system timers managed by the agent.

Retention policies use restic's `--keep-daily`, `--keep-weekly`, `--keep-monthly`, `--keep-yearly` flags.

The dispatcher runs jobs with a **per-destination slot cap and a circuit
breaker** (JAB-362): one dead or unreachable destination can no longer starve
the whole queue — its jobs are skipped-if-pending and retried on their own
per-destination slots while other destinations keep draining, and a failing
destination raises an alert instead of silently backing up.

### Automatic daily local backups

Admins can enable **opt-in automatic daily local backups for all users**
(default **off**, GH #1240) under `/jabali-admin/backups`. When on, every account
is backed up daily to the local destination on a staggered window, giving a
baseline safety net without configuring a per-user schedule.

## system_backup contents

7 stages:

1. **panel_db × 3** — three rolling `mariabackup` dumps of the panel DB itself.
2. **OS users** — `/etc/passwd`, `/etc/shadow`, `/etc/group`, `/etc/gshadow` snapshot.
3. **Stalwart state** — Stalwart's internal data dir (mailboxes are inside).
4. **Kratos state** — identity store.
5. **Hosted sites** — every `/home/<user>` tree.
6. **Config snapshot** — `/etc/nginx/`, `/etc/php/`, `/etc/powerdns/`, `/etc/letsencrypt/`, `/etc/jabali/`, install-marker drop-ins.
7. **Restic snapshot** — wraps all of the above into one restic snapshot per destination.

## Restore

### From a restic snapshot (admin)

`/jabali-admin/backups` → Restore tab:

- Pick a destination, browse snapshots, pick one.
- For `account_full`: choose target user (overwrite or new). The restore drawer
  has a **Select all** toggle (GH #1363) plus per-leg checkboxes, so you can
  restore only the home dir, only the databases, only mail, and so on.
- For `system_backup`: typically restored on a fresh host — the panel is
  bootstrapped, then `jabali system restore` is run from the CLI.

An `account_full` restore rebuilds the account's databases (MariaDB **and**
PostgreSQL), mail, DNS, cron, apps, and **FTP/SFTP subaccounts** — the last with
their original login password preserved through a root-only one-shot staging
file (GH #1361).

### Tenant self-service restore-from-upload

Tenants no longer need an admin for a restore. From the user Backups page a
tenant can **upload a backup archive and restore selected legs** — files,
databases, and mail — of their own account (GH #1408). A per-account download is
prepared in the background with a live **"Preparing…"** indicator so a large
account doesn't block the request.

### Restore a single domain's document root

From a backup you can restore just **one domain's document root** without
touching the rest of the account (GH #1359) — useful for reverting a single site
after a bad deploy.

### Database restore from a file

Both engines support **Restore from file** on the per-database row (see
[Databases](./databases.md)):

- The upload is **chunked and async** so it beats Cloudflare's ~100 MB / 524
  origin limits (GH #1323), with an upload-progress modal.
- **MariaDB** and **PostgreSQL** are both covered. PostgreSQL accepts plain-SQL
  **and** pgAdmin's default custom / tar archive formats, and surfaces the real
  loader error instead of a generic failure (GH #1045). The uploaded dump is
  never loaded as a superuser — it runs through a per-database, non-superuser
  scoped role, built in a throwaway staging database and swapped onto the real
  name only on success, so a bad upload never wipes the live database.

Round-trip is live-verified on the test fleet.

## Full Server backup / restore

A **Full Server backup** packages one backup run into a single downloadable
**container archive** (GH #1408, #502) — everything needed to reconstruct the
host, in one file you can move anywhere.

Restoring from an uploaded container (`jabali system restore --from-tar`, GH
#1408):

- Rebuilds the **system leg** and every hosted account.
- **Creates missing OS users** and rebuilds per-user metadata, so the target
  host does not have to already know the accounts.
- Can **create-from-manifest** — restore an account into a user that does not yet
  exist on the target.

This is the path for moving a whole server to new hardware when the source and
target share no restic destination.

## Restore from an uploaded archive

Both the account and full-server flows accept a backup **archive you upload**
directly, rather than one reached through a configured restic destination — the
portable counterpart to snapshot restore for cross-host moves and disaster
recovery.

## CLI

```bash
jabali destination list
jabali destination create --type sftp --name daily-offsite --host backup.example.com --user backups --key /root/.ssh/backup
jabali destination test daily-offsite

jabali backup schedule list
jabali backup schedule create --kind account_full --user <id> --destination daily-offsite --cron "0 3 * * *" --keep-daily 7 --keep-weekly 4
jabali backup schedule run-now <id>

# Restore one account from a snapshot (add --apply to write to the live system)
jabali backup account-restore --user <name> --destination daily-offsite --snapshot <id> --force --apply

# System restore
jabali system restore --snapshot <id> --destination daily-offsite
# …or from a Full Server container archive (no shared restic destination needed):
jabali system restore --from-tar /path/to/full-server-backup.tar
```

See [`platform/cli-reference.md`](./platform/cli-reference.md) for the complete,
generated flag reference.

## What you don't get

- **Bare-metal disk image** — not in scope. Use your hypervisor's snapshot tools for that.
- **Application-aware restore for non-WP apps** — restic restores the files + DB; you may need to update site URLs by hand if the FQDN changes.
- **Continuous (WAL-style) DB shipping** — only periodic snapshots. For sub-minute RPO you need a separate replication setup.
