# Email

`/jabali-panel/mail/mailboxes` (plus tabs). The mail surface for your domains.

## Tabs

- [Mailboxes](./mailboxes.md) — create, change password, set quota, delete mail accounts.
- [Forwarders](./forwarders.md) — forward an address to one or more external destinations.
- [Autoresponders](./autoresponders.md) — vacation responder per mailbox with start and end window.
- [Catch-all](./catch-all.md) — what happens to mail addressed to a recipient that does not exist.
- [Disclaimer](./disclaimer.md) — append a server-side disclaimer to outbound mail.
- **Settings** — per-domain mail options. Currently the webmail toggle (see below).
- [Shared Folders](./shared-folders.md) — IMAP shared folders for team mailboxes.
- **Groups** — distribution lists (each member gets a copy) and shared workspaces (one shared inbox, calendar, contacts and files). See [Mail groups](../mail.md#mail-groups-gh-1818).
- [Email Logs](./email-logs.md) — live tail of inbound and outbound mail for your domains.

## Webmail

`https://mail.<domain>/` provides **Bulwark** (Next.js JMAP) webmail. The Mailboxes tab has a one-click **Open Webmail** button per mailbox that authenticates you via a single-use self-deleting SSO file (M22 pattern; ADR-0040).

The **Settings** tab has a per-domain webmail switch. Turning it off drops just the `mail.<domain>` webmail vhost for that domain — IMAP, SMTP, and mail delivery are unaffected. Webmail also has to be enabled in your hosting plan, so turning it on here has no effect while your plan has webmail off. The change applies on the next reconcile.

## The domain directory

Each domain whose mail Jabali hosts gets a read-only address book, the
domain directory. It lists the domain's mailboxes by name and address, and
every mailbox it lists can read it.

- **Webmail** uses it for recipient suggestions: typing a colleague's name or
  address in the To field finds them. Under **Contacts** it appears as a
  shared address book, **Shared: jabali-directory@<domain>**.
- Webmail loads the directory when you sign in. A mailbox added while you are
  signed in shows up in suggestions after you sign in again.
- **Mail apps that sync contacts over CardDAV** do not find the directory on
  their own. Add it as an address book at
  `https://mail.<domain>/dav/card/jabali-directory%40<domain>/default/` and
  sign in with your mailbox address and password.

- The directory lists only your domain's own mailboxes. It never shows other
  domains on the server.
- It lists mailboxes people use. System relays, the panel's notification
  sender, send-only accounts and disabled mailboxes are left out. A disabled mailbox also loses access to
  the directory until you enable it again.
- The name shown is the mailbox's display name.
- Nobody can edit the directory from webmail or a mail app. Jabali rebuilds
  it from the mailbox list, normally within a minute of a change.
- The address `jabali-directory@<domain>` belongs to the directory. You
  cannot use it for a mailbox, group or shared resource, and it does not
  receive mail.

## The postmaster address

Mail to `postmaster@<domain>` goes to the server administrator, as for every
domain on the server. It carries the reports other mail servers send about
mail from your domain, and messages from people who have a problem with it.

You cannot make a mailbox, alias, group or shared resource at
`postmaster@<domain>`. A postmaster address made before this rule keeps
working and still gets the domain's postmaster mail.

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
