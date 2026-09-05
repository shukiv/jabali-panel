# Shared Folders

`/jabali-panel/mail/shared-folders`. IMAP shared folders for collaborative mailboxes.

## Use cases

- A team `support@` mailbox where multiple agents read and reply.
- A read-only archive folder shared across an organisation.
- A staging folder where the operations team triages mail before assignment.

## Creating a shared folder

Click **Create shared folder**, supply:

- **Folder name** — appears under the IMAP `Shared/` namespace in clients that support it.
- **Source mailbox** — the mailbox whose folder is being shared. Most often a dedicated team mailbox.
- **Source folder** — `INBOX`, or any subfolder.

The agent updates the Stalwart ACL on the source folder.

## Granting access

Per-shared-folder, add an ACL entry per recipient mailbox:

- **Lookup** — folder is visible.
- **Read** — read messages.
- **Reply** — reply (sending from the source mailbox's identity if the client supports it).
- **Write** — mark read/unread, flag, move messages within the folder.
- **Delete** — delete messages.
- **Administer** — change ACLs (rarely granted; typically only the source mailbox owner).

Default for a freshly-added recipient: Lookup + Read.

## Client support

IMAP shared folders work in clients that implement RFC 4314 ACLs and the `Shared/` namespace:

- Thunderbird: Tools → Account Settings → Server Settings → Advanced → "Show only subscribed folders" off → "Subscribe" the shared folder.
- Apple Mail: Mailbox → Subscribe and pick the shared folder.
- Outlook (desktop): supports via the IMAP namespace; older Outlook versions are inconsistent.
- Bulwark webmail: shared folders appear over JMAP once you're subscribed to them.

## What is and is not shared

A shared folder shares the folder's messages, not the mailbox login. Recipients use their own credentials and see the shared folder under `Shared/<owner>/<folder>`. Owner's drafts, sent items, and other folders are not shared unless explicitly added.

## Revocation

Remove an ACL entry to revoke access. The recipient's client may need to unsubscribe and resubscribe to clear the cached folder listing.
