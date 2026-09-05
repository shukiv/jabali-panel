# Email

`/jabali-panel/mail/mailboxes` (plus tabs). The mail surface for your domains.

## Tabs

- [Mailboxes](./mailboxes.md) — create, change password, set quota, delete mail accounts.
- [Forwarders](./forwarders.md) — forward an address to one or more external destinations.
- [Autoresponders](./autoresponders.md) — vacation responder per mailbox with start and end window.
- [Catch-all](./catch-all.md) — what happens to mail addressed to a recipient that does not exist.
- [Disclaimer](./disclaimer.md) — append a server-side disclaimer to outbound mail.
- [Shared Folders](./shared-folders.md) — IMAP shared folders for team mailboxes.
- [Email Logs](./email-logs.md) — live tail of inbound and outbound mail for your domains.

## Webmail

`https://mail.<domain>/` provides **Bulwark** (Next.js JMAP) webmail. The Mailboxes tab has a one-click **Open Webmail** button per mailbox that authenticates you via a single-use self-deleting SSO file (M22 pattern; ADR-0040).

## IMAP and SMTP submission

- **IMAP**: `imap.<panel-hostname>:993` with TLS, username is the full email address, password is the mailbox password.
- **SMTP submission**: `smtp.<panel-hostname>:587` (STARTTLS) or `:465` (TLS), same credentials.

## Autoconfig

Major mail clients can configure your account automatically:

- **Thunderbird, K-9, mainstream clients**: enter your email address and password; the client finds settings via the `autoconfig.<domain>` endpoint.
- **Outlook**: the same pattern using `autodiscover.<domain>`.
- **Apple Mail, iOS**: install the `.mobileconfig` profile served by the panel at `https://<panel-hostname>/.well-known/mobileconfig?email=<address>`.

## What is and is not included

The mail stack is **Stalwart** ([why](../mail.md)). It handles SMTP, IMAP, JMAP, mailbox storage, and per-user spam scoring. The panel ships:

- DKIM auto-key generation and rotation per domain.
- SPF and DMARC record templates.
- Per-domain MTA-STS policy.

Outside scope:

- Mailing lists (Mailman) — not currently shipped.
- POP3 — disabled by default; the operator may enable it server-wide.
- Calendar (CalDAV) and contacts (CardDAV) — provided by Stalwart but not exposed in the panel UI yet.
