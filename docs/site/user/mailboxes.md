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
- **Change password** — generates a new password (shown once) or accepts a supplied one.

A password you supply needs at least 8 characters and at most 72 bytes. The API and the `jabali mailbox` commands refuse anything else (`422 weak_password`). The panel stores a bcrypt hash of it, plus an encrypted copy used only for one-click webmail sign-in.

Webmail cannot change a mailbox's password: the mail server reads mailbox passwords from the panel, so the password change in webmail's settings is refused. Change it here. An admin can also change any mailbox's password.
- **Set quota** — change the per-mailbox disk quota. Reduces are accepted but do not delete existing mail; the mailbox simply rejects new mail until reduced under the limit.
- **Open webmail** — single-click sign-in to Bulwark webmail via the self-deleting SSO file (60-second TTL, 256-bit nonce filename).
- **Delete** — destructive. The agent removes the Stalwart account and the mailbox storage.

## Create wizard caveats

- Local part validation: lowercase, alphanumeric plus `.`, `_`, `-`, `+`; cannot start with `.`.
- `jabali-directory` is reserved for the [domain directory](./email.md#the-domain-directory) and is refused.
- `postmaster` is reserved for the server administrator and is refused. See [The postmaster address](./email.md#the-postmaster-address).
- An address that is already an alias of another mailbox, a mail group or a shared resource on the domain cannot become a mailbox (`409 address_in_use`). Delete the alias, group or resource first. See [One owner per address](#one-owner-per-address).
- If the mail server cannot be reached, the mailbox is not created (`503 mail_server_unavailable`). Try again once mail is back up.
- The total number of mailboxes counts against your package's `max_mailboxes`.
- The default quota is your package's default; you may raise it up to the package's per-mailbox cap.

## One owner per address

Each address on a domain belongs to one thing: a mailbox, an alias of a mailbox, a mail group or a shared resource. The panel refuses a second one at the same address, in any combination with a mailbox.

The mail server keeps a record of every alias it has delivered to, on the mailbox that had it. So the panel also clears an address on the mail server:

- before it creates a mailbox there, so the new mailbox signs in to its own account and not to the one that once had the alias;
- when an alias moves to another mailbox, so its mail goes to the new mailbox;
- when an alias is deleted, so the old mailbox stops receiving its mail.

If the mail server is down when an alias moves, the panel finishes the move within 10 minutes of it coming back. An alias that was deleted while the mail server was down can keep delivering to its old mailbox until the address is used again.

## Where the mail lives

Mailbox storage lives inside Stalwart's data directory (`/var/lib/stalwart/`). The panel does not expose direct filesystem access. To migrate a mailbox elsewhere, use the **IMAP sync** option in a third-party tool (`imapsync`, Thunderbird's "Move Folder", Apple Mail's "Move Mailbox") between the new and old IMAP endpoints.

## What happens to mail when a mailbox is deleted

All mail is gone. The Stalwart account is dropped, the storage is purged. Account-level backups (see [Backups](./backups.md)) include the mailbox; if you delete a mailbox in error, restore from the most recent backup.
