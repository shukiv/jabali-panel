# Mail Hostname

The mail hostname is the name mail clients and webmail use to reach the server's shared mail service (IMAP, SMTP submission, webmail). By default it is `mail.<panel-hostname>`. An administrator can change it under **Server Settings → Email → Change mail hostname**, for example to keep the panel at `panel.example.com` while mail uses `mail.example.com`.

The change is a request, not an immediate switch. The panel applies it only once the new name points at this server and a certificate for it is issued and served. Until then, mail keeps working on the current name.

## Before you start

A change runs only when all of these hold; otherwise the request waits and is retried:

- the panel hostname and the admin e-mail address are set;
- the panel certificates use Let's Encrypt (a self-signed panel cannot change its mail hostname);
- the panel hostname's own domain exists with email and webmail on, and webmail is on server-wide.

The new name cannot be:

- the panel hostname;
- a hosted domain or a web alias;
- a name under a hosted domain;
- a name with hosted domains or web aliases under it.

Names under the panel's own domain are allowed.

## 1. Create the DNS records

Create the records at the DNS provider that serves the new name publicly. The Email card lists them for the name you type:

| Type | Name | Value | |
|---|---|---|---|
| A | the new name | the server's public IPv4 | Required. The panel checks that public DNS returns this address before it requests the certificate. |
| AAAA | the new name | the server's public IPv6 | Optional, and only if the server has a public IPv6. |

If the card warns that the public IPv4 is not set, set it under **Server Settings → General → Public IPv4** first.

## 2. Request the change

Type the name and click **Request change**, or use the CLI:

```
jabali settings mail-hostname --set mail.example.com
jabali settings mail-hostname --status
```

Requests are rate limited per administrator and recorded in the audit log (`settings.mail_hostname.request`, `settings.mail_hostname.cancel`).

## What the panel does

The reconciler runs the change in this order:

1. It checks that the new name and `mail.<panel-hostname>` both resolve publicly to this server.
2. It issues a Let's Encrypt certificate for the new name, with `mail.<panel-hostname>` as a second name, and deploys it to the mail server.
3. It checks that the mail server actually serves the new certificate on IMAPS (993) and SMTPS (465).
4. It applies the new name. Within one reconcile tick, webmail, Bulwark's JMAP URL and the `/webmail` redirects follow it.

## States

| State | Meaning |
|---|---|
| pending | Waiting for the next attempt. |
| issuing | A certificate is being issued and checked. The request cannot be cancelled now. |
| failed | The last attempt failed. The card shows the reason and the time of the next attempt. |
| done | The new name is applied. |

A pending or failed request can be cancelled with **Cancel change** or `jabali settings mail-hostname --cancel`.

## When a change fails

The reason on the card names what went wrong. The most common ones:

- **The name does not point at this server.** Fix the DNS record. The request is retried every 10 minutes.
- **Let's Encrypt refused the certificate.** The request is retried after one hour.
- **The mail server does not serve the new certificate.** The certificate was issued, but the certificate hooks did not deploy it to the mail server, usually because they are out of date. Run `jabali update` to reinstall them. The request is retried after one hour.

A failed change never replaces the name in use.

## Switching back

To go back to the default, request `mail.<panel-hostname>`; while a custom name is applied, the card offers **Switch back to mail.<panel-hostname>**. It runs through the same steps.

## The old name keeps working

`mail.<panel-hostname>` stays served after every change. The certificate always covers it, so mail clients configured with it keep working.

It is also the target of the panel domain's MX record, its MTA-STS policy and its mail autoconfig, and these do not move with the mail hostname. **Keep its DNS record pointing at this server.** Removing it would break incoming mail, and the certificate could no longer renew.

By design, there is no action to retire it.

## Certificates

The mail certificate is stored under `/etc/letsencrypt/live/<mail-hostname>/`.

When a change replaces an earlier custom name, the panel deletes that name's certificate so certbot stops renewing it. This happens when you move from one custom name to another (for example `mail.a.com` to `mail.b.com`) or back to the default.

The certificates for the panel hostname and `mail.<panel-hostname>` are never deleted.

## Panel rename

Renaming the panel does not move mail. A Let's Encrypt mail certificate stays on the name it was issued for, and that name is recorded as the mail hostname. Moving mail to `mail.<new-hostname>` is an ordinary change request.

## What does not change

- Mailbox addresses: they stay on the panel-primary domain.
- Hosted domains' own `mail.<domain>` names, records and certificates.
- The panel domain's MX and MTA-STS records, and the mail client settings and autoconfig. These keep `mail.<panel-hostname>`.

See also: [Panel Certificate](./panel-certificate.md), [Server Settings](./server-settings.md).
