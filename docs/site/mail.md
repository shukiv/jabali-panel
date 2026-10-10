# Mail

Jabali's mail stack is [**Stalwart**](https://stalw.art) (SMTP submission + MTA + JMAP + IMAP, single process) + **Bulwark**, a Next.js JMAP webmail served per-tenant on `mail.<domain>`.

Mail is an optional module (Server Settings → Modules). It needs the DNS module installed and running first, so the Mail switch stays off until DNS shows **active** ([DNS](./dns.md)).

## Per-mailbox

- Authentication: per-mailbox password, bcrypt-hashed in the panel's database. Stalwart reads it through a read-only SQL directory, so the password is changed in the panel: **Rotate password** on the mailbox (Mail → a domain → Mailboxes), by the account owner or an admin. Webmail's password change is refused (GH #2072).
- Quota: per-mailbox MiB, enforced by Stalwart.
- Webmail: **Bulwark** at `https://mail.<domain>/`. One-click SSO from `/jabali-panel/mail/mailboxes` uses the M22 self-deleting `jabali-sso-*.php` file (not the failed M22 magic-link/mu-plugin path).
- IMAP / SMTP submission: `imap.<panel-hostname>:993` (TLS), `smtp.<panel-hostname>:465` (TLS) or `:587` (STARTTLS).
- Autoconfig / autodiscover: Apple `mobileconfig`, Thunderbird `autoconfig.xml`, Outlook `autodiscover.xml` (see [platform/mail-autoconfig.md](./platform/mail-autoconfig.md)).
- Calendars + contacts: the mail vhost serves CalDAV / CardDAV and `/.well-known/caldav|carddav`, and the `_caldavs._tcp` / `_carddavs._tcp` SRV records point clients at it, so mail apps find a mailbox's calendars and contacts on their own. The panel no longer shows per-mailbox DAV URLs (GH #1917); an external-DNS domain gets the two SRV records in its mail DNS record list.

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

## Mail groups (GH #1818)

A mail group is an address on a domain whose members are mailboxes on the same domain. **Mail → Groups** creates one; there are two types, fixed at creation. A group cannot take a mailbox's address, and a mailbox cannot take a group's (see [One owner per address](./user/mailboxes.md#one-owner-per-address)).

- **Distribution list** (the default) — every member receives their own copy of each message in their own inbox. Jabali projects it as a Stalwart mailing list whose recipients are the members that can receive mail (disabled and send-only mailboxes are left out). A list with no such members has nothing at its address, so senders get a `550` instead of mail that is accepted and dropped.
- **Shared workspace** — one shared inbox that members open in webmail (no copies are delivered), plus a shared calendar, contacts and files, and send-as the group address.

**Internal delivery only** accepts mail from senders in the group's own domain and rejects everyone else. On a distribution list it is delivered through a Sieve script that redirects to each member, and Stalwart allows 20 redirects per message, so an internal-only distribution list is limited to **20 members**: adding a 21st member, or turning internal-only on for a larger list, is refused with `422 too_many_members`.

Distribution lists are re-applied to Stalwart by the reconciler whenever the list or a member changes (and every 15 minutes), so a failed save heals on its own. On boxes created before GH #1818, a distribution list was stored as a shared inbox that members could not see. The first reconcile after the update converts each one. Any mail already in that inbox is copied into every member's inbox first, and the old inbox is removed only after every copy succeeded.

## Per-domain deliverability (admin)

`/jabali-admin/mail/deliverability` — for every domain, shows:

- DKIM key presence + DNS publication state
- SPF record presence + soft/hard fail
- DMARC record + policy
- MTA-STS policy + MX host alignment (ADR-0109, per-domain MTA-STS)

Buttons:
- **Rotate DKIM** — generate a new DKIM key, publish DNS record, retire the old key on the configured grace period.

## Website mail: local mail server or a smarthost (GH #2056)

Sites send email with PHP `mail()`, which contact forms and WordPress use. PHP
runs the `jabali-sendmail` shim as the site's user. **Server Settings → Email →
Website mail** picks where the shim's mail goes:

- **The local mail server** (the default). The shim logs in to the mail
  module's Stalwart as `noreply@<domain>`, a send-only identity the panel
  creates for each domain. This needs the mail module; the card warns when it
  is off.
- **A smarthost**: your own mail relay (an SMTP server, or a service such as
  Amazon SES, Mailgun or your mail provider). Use it when the server doesn't
  run the mail module, or when outgoing mail must leave through your own mail
  system. Ports 25, 465, 587 and 2525. Encryption is STARTTLS (required, never
  opportunistic), TLS from the start, or none; a login is never sent without
  encryption, and the certificate is always checked. Saving tests the
  smarthost first and saves nothing if the test fails; **Test** checks the
  form without saving. The password is stored encrypted and never shown again;
  it is only used with the host and username it was saved for.

How the smarthost mode works (ADR 0174):

- The shim can't hold the smarthost login: anything it can read, the site's
  PHP can read too. It hands the message to the **website mail relay**
  (`jabali-mailrelay.service`, the agent binary run as `jabali-agent
  mailrelay`) over `/run/jabali-mailrelay/relay.sock`. The relay runs as its
  own `jabali-mailrelay` system user, never root, and only it can read the
  login (`/etc/jabali-panel/mailrelay/relay.json`, 0640
  root:jabali-mailrelay). The agent starts the relay when the smarthost is
  selected and stops it, deleting the login, when it isn't.
- The relay learns which account sent a message from the socket (the caller's
  UID), never from the message. The envelope sender is `noreply@` one of that
  account's domains: the From domain when the account owns it, otherwise its
  oldest domain.
- The From header is restricted too, because the smarthost can't tell which
  site sent a message. A message keeps its From only when that is one address
  in a domain the account owns. Otherwise From becomes the `noreply@` address
  (the display name is kept) and the original address moves to Reply-To, so a
  contact form that puts the visitor in From still gets replies to the
  visitor. Like sendmail, the relay adds a `Date` and a `Message-ID` when the
  message has none (PHP's `mail()` writes neither, and some providers refuse
  mail without a Message-ID).
- Who can send through the smarthost: accounts with a Linux user and a hosting
  package whose **Websites can send email** switch is on, that aren't admins
  or suspended, with their enabled, ownership-verified domains. Accounts
  without a package can't (#282). The card shows how many accounts can send,
  and after a save lists the ones the server left out with the reason.
- An account can have two messages in flight at once, and a message must
  arrive within 30 seconds, so one site can't hold the relay for the others.
  There is no local queue: if the smarthost is down or answers 4xx, `mail()`
  returns false and the failure is logged. One line per message goes to the
  journal (`journalctl -u jabali-mailrelay`), without the message content.
- SPF and DKIM for the sites' domains are the smarthost's job: add the
  smarthost to each domain's SPF record as your provider documents.

The **Websites can send email** package switch applies to the local mail
server too: a site whose package has it off gets no `noreply@` relay identity.

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
- **Cross-domain directory isolation (GH #1581).** Stalwart keeps all accounts in one flat directory — its per-tenant isolation is an Enterprise-only feature, and Jabali runs the OSS edition. Left at Stalwart's defaults, any authenticated mailbox can enumerate every account on the server across all domains via JMAP `Principal/query` or the WebDAV principal-search family (surfaced by the webmail's calendar/file share picker and recipient autocomplete). `install_stalwart_apply` converges the built-in **User** role to disable those enumeration permissions (`jmapPrincipalQuery`, `jmapPrincipalQueryChanges`, `jmapPrincipalChanges`, `davPrincipalList/Match/Search/SearchPropSet`) via `disabledPermissions`, which takes precedence and fails closed. `jmapPrincipalGet` / `GetAvailability` stay enabled — they need a known principal id, so they can't enumerate — preserving self-account reads and free/busy. **Trade-off:** in-webmail sharing-by-search no longer works (the picker resolves targets through the same query); a user shares by knowing the exact address. True per-domain scoping would require Stalwart Enterprise + per-domain tenants. Same-domain recipient suggestions come back through the domain directory (next bullet).
- **Domain directory (GH #1637, ADR-0171).** Each mail domain with mailboxes gets a read-only address book, `<domain> directory`. It lists the domain's enabled mailboxes (display name and address) and is shared read-only with them. Webmail suggests recipients from it; Bulwark reads contacts at sign-in, so a new mailbox appears in an open session after the next sign-in. CardDAV clients do not discover it (a mailbox's `addressbook-home-set` lists only its own home) but can add `/dav/card/jabali-directory%40<domain>/default/` by URL. System relays, the panel's notification sender (`jabali-notify@<panel hostname>`), send-only and disabled mailboxes are left out. The book belongs to a Stalwart Group principal at `jabali-directory@<domain>`. That local part is reserved at every create door and at backup restore, and the address receives no mail. The reconciler converges the directory through the agent verb `mail.directory.apply` (20 domains per tick, hourly audit). The directory is skipped for a domain where a mailbox, group or shared resource already holds its address. The domain purge removes the Group with the other accounts.

## CLI

```bash
jabali mailbox list --domain example.com
jabali mailbox create --domain example.com --local user --quota-mb 1024
jabali mailbox set-quota user@example.com 2048
jabali mailbox passwd user@example.com
jabali mailbox delete user@example.com
```
