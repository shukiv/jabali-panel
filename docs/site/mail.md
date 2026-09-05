# Mail

Jabali's mail stack is [**Stalwart**](https://stalw.art) (SMTP submission + MTA + JMAP + IMAP, single process) + **Bulwark**, a Next.js JMAP webmail served per-tenant on `mail.<domain>`.

## Per-mailbox

- Authentication: per-mailbox Argon2id-hashed password stored by Stalwart.
- Quota: per-mailbox MiB, enforced by Stalwart.
- Webmail: **Bulwark** at `https://mail.<domain>/`. One-click SSO from `/jabali-panel/mail/mailboxes` uses the M22 self-deleting `jabali-sso-*.php` file (not the failed M22 magic-link/mu-plugin path).
- IMAP / SMTP submission: `imap.<panel-hostname>:993` (TLS), `smtp.<panel-hostname>:465` (TLS) or `:587` (STARTTLS).
- Autoconfig / autodiscover: Apple `mobileconfig`, Thunderbird `autoconfig.xml`, Outlook `autodiscover.xml` (see [platform/mail-autoconfig.md](./platform/mail-autoconfig.md)).
- Calendars + contacts: per-mailbox **CalDAV / CardDAV** URLs are surfaced for manual client setup (GH #1039), and the mail vhost routes CalDAV/CardDAV so clients auto-mount.

## Mail Domains (GH #1387)

Mail is organised **per domain**, not as one flat page. `/jabali-panel/mail`
lists your **Mail Domains** — sortable, with SSL + Status columns, a live queue
count, per-domain Enable / Disable, breadcrumbs, and a **Create Mail Domain**
button (GH #1479). Click a domain to drill into its accounts and settings.

Per-domain tabs:

- **Mailboxes** — create, change password, set quota, delete.
- **Forwarders** — forward `alice@example.com` to one or more external addresses.
- **Autoresponders** — vacation responder per mailbox, with start/end window and subject template.
- **Catch-all** — send unmatched recipients to a chosen mailbox or `:drop` / `:reject`.
- **Disclaimer** — append HTML / plaintext disclaimer server-side to outbound mail per domain (ADR-0052).
- **Shared Folders** — create IMAP shared folders for the team; manage ACLs.
- **CalDAV / CardDAV override** — repoint a domain's calendar + contacts SRV
  records at an external server instead of the built-in Stalwart DAV (GH #1462).
- **Logs** and **Statistics** — scoped to the selected domain (sortable Mail Logs
  columns, GH #1365); the old flat Mail page is retired.

### Mail-only delete

A domain can have **just its mail torn down while the web domain stays** (GH
#1387) — deletes mailboxes, forwarders, DKIM, and the Stalwart domain entry
without removing the vhost or DNS zone.

## Per-domain deliverability (admin)

`/jabali-admin/mail/deliverability` — for every domain, shows:

- DKIM key presence + DNS publication state
- SPF record presence + soft/hard fail
- DMARC record + policy
- MTA-STS policy + MX host alignment (ADR-0109, per-domain MTA-STS)

Buttons:
- **Rotate DKIM** — generate a new DKIM key, publish DNS record, retire the old key on the configured grace period.

## Outbound throttles

`/jabali-admin/mail/throttles` (M47 Wave 3) — per-sender + per-domain rate limit (msgs / minute, msgs / hour, recipients / message). Bulwark enforces; CrowdSec sees throttle hits and can escalate.

## Stalwart reports

Stalwart ingests inbound TLS-RPT, MTA-STS-RPT, and DMARC aggregate reports (M47 Wave 2) into the panel DB. Visible per-domain under Deliverability.

## Expression filters (M47 Wave 3v2)

Admin-defined Stalwart expressions for routing / drop / quarantine. UI under Server Settings → Mail.

## Architecture choices

- **DB-as-truth, reconciler-converged.** Mailboxes / forwarders / autoresponders are panel-DB rows; Stalwart's JMAP API is called by the agent on every change.
- **Self-deleting SSO file** for webmail one-click (M22 rework, ADR-0040). Not a magic-link plugin, not a session cookie hand-off — the panel writes `/jabali-sso-<43-char-nonce>.php` into the mail vhost, redirects the user to it; the file `flock`s + `unlink`s itself on first hit or after 60 s.
- **Unix sockets only.** Stalwart admin HTTP pinned to `127.0.0.1:8080`; MariaDB skip-networking; nothing exposes mailbox auth over TCP outside the SMTP/IMAP ports themselves.

## CLI

```bash
jabali mailbox list --domain example.com
jabali mailbox create user@example.com --quota-mib 1024
jabali mailbox set-quota user@example.com 2048
jabali mailbox passwd user@example.com
jabali mailbox delete user@example.com
```
