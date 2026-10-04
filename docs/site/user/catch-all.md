# Catch-all

What happens to mail sent to an address at your domain that has no mailbox,
forwarder or group.

## Where to set it

**Mail → Mail Domains → your domain → Settings → Catch-All**
(`/jabali-panel/mail-domains/<domain id>/settings`). It used to be a separate
Catch-All tab; old links to that tab open Settings.

The Settings tab shows the catch-all only when the domain has email enabled.

- **Not set** (the default): the mail server rejects mail to an unknown
  address, and the sending server returns it to the sender as a bounce. This
  is the recommended setting: legitimate senders learn the address is wrong,
  and nothing piles up.
- **Set**: pick a **Target mailbox** from the domain's mailboxes and click
  **Set catch-all**. Mail to any unknown address at the domain is delivered
  to that mailbox.

**Clear catch-all** removes it, and unknown addresses are rejected again.

Catch-all is per domain: each domain has its own setting, on its own Settings
tab.

The same setting is available from the command line:

```
jabali domain catchall show  <domain-name-or-id>
jabali domain catchall set   <domain-name-or-id> --target <email>
jabali domain catchall clear <domain-name-or-id>
```

The command line also accepts an address outside the domain as the target.
The Settings tab shows such a target as "(current)" and keeps it when you save.

## Spam

A catch-all mailbox receives every typo of every real address, and every
address a spammer guesses. It typically gets far more spam than a normal
mailbox. The spam filter still applies, but set a catch-all only when you have
a reason to receive mail at addresses you never created.
