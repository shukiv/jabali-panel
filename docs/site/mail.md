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

## Mail groups (GH #1818)

A mail group is an address on a domain whose members are mailboxes on the same domain. **Mail → Groups** creates one; there are two types, fixed at creation:

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
- **Cross-domain directory isolation (GH #1581).** Stalwart keeps all accounts in one flat directory — its per-tenant isolation is an Enterprise-only feature, and Jabali runs the OSS edition. Left at Stalwart's defaults, any authenticated mailbox can enumerate every account on the server across all domains via JMAP `Principal/query` or the WebDAV principal-search family (surfaced by the webmail's calendar/file share picker and recipient autocomplete). `install_stalwart_apply` converges the built-in **User** role to disable those enumeration permissions (`jmapPrincipalQuery`, `jmapPrincipalQueryChanges`, `jmapPrincipalChanges`, `davPrincipalList/Match/Search/SearchPropSet`) via `disabledPermissions`, which takes precedence and fails closed. `jmapPrincipalGet` / `GetAvailability` stay enabled — they need a known principal id, so they can't enumerate — preserving self-account reads and free/busy. **Trade-off:** in-webmail sharing-by-search no longer works (the picker resolves targets through the same query); a user shares by knowing the exact address. True per-domain scoping would require Stalwart Enterprise + per-domain tenants.

## CLI

```bash
jabali mailbox list --domain example.com
jabali mailbox create user@example.com --quota-mib 1024
jabali mailbox set-quota user@example.com 2048
jabali mailbox passwd user@example.com
jabali mailbox delete user@example.com
```
