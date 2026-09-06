# Mailboxes

`/jabali-panel/mail/mailboxes`. The list and lifecycle of mail accounts in your domains.

## Per-row data

- Email address
- Domain
- Disk quota (used / max in MiB)
- Status (active / suspended)
- Last login (IMAP or webmail)
- Created

## Actions

- **Create** — opens the create wizard. Pick the local part, the domain, the quota (within your package's per-mailbox cap), and either supply a password or let the panel generate one (shown once on the success page).
- **Change password** — generates a new password (shown once) or accepts a supplied one. Stalwart hashes it with Argon2id.
- **Set quota** — change the per-mailbox disk quota. Reduces are accepted but do not delete existing mail; the mailbox simply rejects new mail until reduced under the limit.
- **Open webmail** — single-click sign-in to Bulwark webmail via the self-deleting SSO file (60-second TTL, 256-bit nonce filename).
- **Delete** — destructive. The agent removes the Stalwart account and the mailbox storage.

## Create wizard caveats

- Local part validation: lowercase, alphanumeric plus `.`, `_`, `-`, `+`; cannot start with `.`.
- The total number of mailboxes counts against your package's `max_mailboxes`.
- The default quota is your package's default; you may raise it up to the package's per-mailbox cap.

## Where the mail lives

Mailbox storage lives inside Stalwart's data directory (`/var/lib/stalwart/`). The panel does not expose direct filesystem access. To migrate a mailbox elsewhere, use the **IMAP sync** option in a third-party tool (`imapsync`, Thunderbird's "Move Folder", Apple Mail's "Move Mailbox") between the new and old IMAP endpoints.

## What happens to mail when a mailbox is deleted

All mail is gone. The Stalwart account is dropped, the storage is purged. Account-level backups (see [Backups](./backups.md)) include the mailbox; if you delete a mailbox in error, restore from the most recent backup.
