# Shared Folders

`/jabali-panel/mail-domains/<domain>/shared` — the **Shared Folders** tab of a mail domain. Give another mailbox of your account access to a mailbox's Inbox.

## What is shared

- Only the owner mailbox's **Inbox**. Sent, Drafts and other folders stay private.
- The target mailbox signs in with its own password. The owner's password is never shared.
- Both mailboxes must belong to your account. The panel refuses a target mailbox of another account.

A typical use: a team address such as `support@` whose Inbox several staff mailboxes can read.

## Sharing a mailbox

Click **Share folder** and fill in:

- **Source mailbox (owner)** — the mailbox whose Inbox is shared.
- **Target mailbox (grantee)** — the mailbox that gets access. It cannot be the owner itself.
- **Rights** — at least one (see below).

Click **Share**. The panel saves the share and applies it on the mail server. The target sees the folder the next time its mail client refreshes the folder list.

Each owner can share with a target once. To change the rights, remove the share and add it again.

If the mail server does not accept the share at that moment, the share is still saved and the panel applies it automatically on a later pass.

## Rights

| Right | Mail server right | What the target can do |
|---|---|---|
| Read | `mayReadItems` | See the folder and read its messages. |
| Add | `mayAddItems` | Put messages into the folder. |
| Remove | `mayRemoveItems` | Remove messages from the folder. |
| Create folder | `mayCreateChild` | Create subfolders under it. |
| Rename | `mayRename` | Rename the folder. |
| Delete | `mayDelete` | Delete the folder. |
| Admin | `mayShare` | Change who the folder is shared with. |
| Submit | `maySubmit` | The JMAP `maySubmit` right on the folder. |

**Read** alone is read-only: the target can open and read messages, but cannot add messages, and cannot mark messages read, unread or flagged.

The panel's share list is what the mail server keeps. Sharing changes made from a mail client (with **Admin**) are replaced the next time the panel applies the owner's list.

## In a mail client

Over IMAP, a share appears in the target's folder list under the `Shared Folders` namespace, named after the owner:

```
Shared Folders/support@example.com/INBOX
```

If a client shows only subscribed folders, subscribe to that folder in the client's folder settings.

## Removing a share

Click **Remove** on the row and confirm. The panel revokes the access on the mail server first, and removes the row only after the mail server accepted the change. The folder then leaves the target's folder list; a client can show it until it refreshes its folder list.

If the mail server does not answer, nothing is removed: the row stays, the access stays, and the panel shows `share_apply_failed`. Try again later.

## Command line

Administrators can manage shares with the `jabali` CLI on the server:

```
jabali mailbox shares list   --owner support@example.com
jabali mailbox shares add    --owner support@example.com --shared-with anna@example.com --rights ro
jabali mailbox shares remove --id <share id from list>
```

`--rights` takes a preset: `ro` (Read), `rw` (Read, Add, Remove, the default) or `admin` (every right). `add` and `remove` apply the change on the mail server and print whether it was applied.
