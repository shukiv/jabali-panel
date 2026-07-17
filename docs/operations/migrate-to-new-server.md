# Migrating Jabali Panel to a New Server

This guide moves a whole Jabali install — the panel database, domains, TLS
certificates, system users, and (optionally) per-account home/database/mail
data — to a fresh host using the built-in **system backup + restore** (M30,
restic-backed).

> TL;DR: take a **system backup to a remote destination** on the old server,
> then run the **one-shot install + restore** on the new server, repoint DNS,
> and let Let's Encrypt re-issue. Some pieces (Stalwart mail state, Docker app
> data) need a manual step — see [What migrates](#what-migrates).

## Where do backups live?

- **Local restic repository:** `/var/lib/jabali-backups/repo`, encrypted with
  the key in `/etc/jabali-panel/restic-repo.password`.
- **Remote destinations** you add under **Admin → Backups → Destinations**
  (SFTP, S3, B2, Azure, GCS, REST). Credentials are stored at
  `/etc/jabali-panel/restic-remotes/<dest-id>.env` (root:root 0600).

A backup is a set of restic snapshots tagged with the job. The "Download"
button materializes a snapshot to a tarball, but for a server move you restore
**directly from the restic repo** — don't download/re-upload.

> **Keep three things offline, not only inside the backup:** the restic
> password file, the destination-credentials env file, and the repository URL.
> Without the password the repo is unreadable.

## Step 1 — Back up the old server to a remote repo

A purely-local repo doesn't help if the old host dies. So:

1. Add a remote destination: **Admin → Backups → Destinations → Create**
   (SFTP/S3/B2/Azure/GCS/REST). Click **Test** before saving.
2. Take a system backup: **Admin → Backups → System backup → Create**, pick
   that destination, and enable **Include accounts**. (System backups are
   created from the panel or on a schedule — there's no `jabali system backup`
   CLI; restore is the CLI half.)

**Include accounts** adds each tenant's home + databases + mailboxes. Without
it you migrate only the panel/server config. Note the **restic password**
(`/etc/jabali-panel/restic-repo.password`) and the destination's credentials
env file — you need both on the new host to read the repo.

## Step 2 — Restore on the new server (one-shot)

On a clean host, the installer can lay down the base system **and** restore in
one command:

```bash
# On the NEW server (root)
bash <(curl -fsSL https://github.com/shukiv/jabali-panel/raw/main/install.sh) \
    --restore-from=sftp:user@backup-host:/path/to/repo \
    --restore-credentials=/root/dest.env \
    --restore-password=/root/restic-repo.password \
    --restore-snapshot=latest
```

The installer builds the panel, skips the bootstrap-admin seed (the restored
DB carries the real admin), then runs `jabali system restore … --include-accounts
--apply --force`, which auto-applies:

| Stage          | Action                                              |
|----------------|-----------------------------------------------------|
| `panel_db`     | Pipes each DB dump back into MariaDB (unix socket)  |
| `panel_config` | Rsync onto `/etc/jabali-panel`                      |
| `tls`          | Rsync onto `/etc/letsencrypt`                       |

To inspect before applying, run the same restore with `--apply=false` — it stages
files under `/var/lib/jabali-backups/restore-staging/<job-id>/<stage>/` for
review, then re-run with `--apply`.

## Step 3 — Repoint DNS and re-issue certificates

1. Update each domain's **A/AAAA** records (and the panel/nameserver glue) to
   the new server's IP. If Jabali runs your authoritative DNS, the restored
   zones already point at the new NS IPs once you set them in Server Settings.
2. **Certificates:** the `tls` stage restores your existing Let's Encrypt certs
   as a stopgap, and Jabali **re-issues automatically** (HTTP-01 over the
   existing `:80` ACME path) on the next reconcile once DNS resolves to the new
   host. No manual cert copying needed for the long term.

## What migrates

| Area | Status |
|------|--------|
| Panel database (users, domains, packages, settings, DNS zones) | **Auto** (`panel_db`) |
| `/etc/jabali-panel` config | **Auto** (`panel_config`) |
| Let's Encrypt certs | **Auto** (`tls`) + re-issue on DNS cutover |
| Tenant home dirs, databases, mailbox exports | **Auto** with `--include-accounts` |
| System users (`/etc/passwd` + `/etc/shadow`) | **Staged, manual merge** (`os_users`) — review before applying to avoid clobbering the new host's accounts |
| Stalwart mail state (RocksDB) | **Staged, manual** (`mail_state`) — stop `stalwart-mail`, rsync `/var/lib/stalwart` from the staging dir, start it |
| nginx / PHP service config | **Not restored** — `install.sh` writes the canonical configs; per-domain vhosts are regenerated from the panel DB by the reconciler |
| Security rules (UFW / CrowdSec) | **Staged** (`security`) — review the diff before applying |
| **Docker apps** | **Reinstall + copy volumes.** App rows restore with the DB, but container data under `/var/lib/jabali/docker-apps/<slug>/` is not part of the system backup — copy those trees to the new host (rsync) before the app reconciles, or reinstall the app and restore its data volume manually. |

## Step 4 — Verify

```bash
systemctl is-active jabali-panel jabali-agent nginx mariadb
curl -k https://localhost:8443/api/v1/health
journalctl -u jabali-panel -n 100 --no-pager
```

Then log into the panel, confirm domains resolve to the new IP, certs are
valid (or re-issuing), and spot-check a tenant site + webmail.

## Notes & limits

- Restore is **CLI-only** by design (ADR-0075) — there's no "restore" button.
- The restic password is per-repo; the source and destination of a `restic
  copy` share it.
- Do a **dry run first**: `jabali system restore --remote-url=/var/lib/jabali-backups/repo
  --snapshot=latest --apply=false --force` on a throwaway VM to confirm the repo
  and snapshot are readable before the real cutover.

See also: `plans/m30-backup-restore-runbook.md`,
`plans/m30.1-backup-destinations-runbook.md`.
