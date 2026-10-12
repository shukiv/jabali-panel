# Backup Download (User)

Profile → Backups → **Download my account**. Produce a fresh `account_full` snapshot on demand and download it.

## Availability

Subject to the administrator allowing tenant-initiated backups for your package. If disabled, the button is hidden; ask the administrator if you need an on-demand backup.

## Flow

1. Click **Download my account**. The page asks you to confirm — generating a snapshot consumes resources proportional to your account size.
2. The agent runs `account_full` and writes the snapshot to a temporary location.
3. When complete, a download link appears on the page (and you receive an in-app notification).
4. The link is short-lived (15 minutes by default) and accessible only to your panel session.

## File name

The download is named for your account and the time the backup was made (UTC), for example `backup_alice_2026-10-11_0430.tar.zst`. A Files, Database or Folders backup adds its content: `backup_alice_files_2026-10-11_0430.tar.zst`.

## What the tarball contains

- `files/` — your home directory tree.
- `databases/` — per-database SQL dumps.
- `mail/` — per-mailbox JMAP export.
- `dns/` — zone files for your domains.
- `manifest.json` — kind, your username, timestamp, snapshot id.

## What the tarball does *not* contain

- Clear-text mailbox passwords (only the Argon2id hashes).
- SSH private keys (only the authorized_keys file).
- Database root password (you do not have it).

## Frequency limits

The administrator may set a per-day limit on tenant-initiated backups (default: 1 per 24 hours). Hitting the limit returns a "try later" message with the next available time.

## Use cases

- Take a snapshot before a risky change (theme switch, plugin upgrade) so you can restore quickly if something breaks.
- Hand a copy of your account to a colleague or successor during access transitions.
- Comply with a "personal data export" request you received about your own account.

## Storage cost

The tarball lives on the panel host until you download it or until the daily cleanup timer purges it (24 hours). It does not count against your disk quota.

## What if my account is too large

Very large accounts (>100 GiB) may exceed the time the page is willing to wait. For these cases, ask the administrator to run a scheduled `account_full` to an off-host destination and pull the snapshot from there.
