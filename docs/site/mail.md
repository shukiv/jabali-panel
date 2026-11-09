# Mail

Jabali's mail stack is [**Stalwart**](https://stalw.art) (SMTP submission + MTA + JMAP + IMAP, single process) + **Bulwark**, a Next.js JMAP webmail served per-tenant on `mail.<domain>`.

Mail is an optional module (Server Settings → Modules). It needs the DNS module installed and running first, so the Mail switch stays off until DNS shows **active** ([DNS](./dns.md)).

## Per-mailbox

- Authentication: per-mailbox password, bcrypt-hashed in the panel's database. Stalwart reads it through a read-only SQL directory, so the password is changed in the panel: **Rotate password** on the mailbox (Mail → a domain → Mailboxes), by the account owner or an admin. Webmail's password change is refused.
- Quota: per-mailbox MiB, enforced by Stalwart.
- Webmail: **Bulwark** at `https://mail.<domain>/`. One-click SSO from a mailbox (Mail Domains → a domain → Accounts) uses the self-deleting `jabali-sso-*.php` file (not the earlier magic-link/mu-plugin approach, which failed).
- IMAP / SMTP submission: `imap.<panel-hostname>:993` (TLS), `smtp.<panel-hostname>:465` (TLS) or `:587` (STARTTLS).
- Autoconfig / autodiscover: Apple `mobileconfig`, Thunderbird `autoconfig.xml`, Outlook `autodiscover.xml` (see [platform/mail-autoconfig.md](./platform/mail-autoconfig.md)).
- Calendars + contacts: the mail vhost serves CalDAV / CardDAV and `/.well-known/caldav|carddav`, and the `_caldavs._tcp` / `_carddavs._tcp` SRV records point clients at it, so mail apps find a mailbox's calendars and contacts on their own. The panel no longer shows per-mailbox DAV URLs; an external-DNS domain gets the two SRV records in its mail DNS record list.

## App passwords

Webmail's **Settings → Security** lets a mailbox create app passwords for its mail apps. Stalwart checks an app password itself, apart from the mailbox password the panel keeps, so the panel removes app passwords when they must stop working:

- **Password changed in the panel**: the app passwords made before the change, or in the minute after it, are removed. Until the panel's flush of Stalwart's login cache reaches it, webmail and JMAP still take the old password for a few seconds, so an app password made in that minute may have been made with the old one. Webmail opened from the panel in that minute is signed out about a minute later; open it again.
- **Mailbox disabled, or its owner suspended**: all of the mailbox's app passwords are removed. Enabling the mailbox or unsuspending the owner does not bring them back; the user creates new ones.
- The reconciler's `mail.credentials` pass checks every mailbox when the panel starts, when a mailbox's password or sign-in state changes, a minute after a password change, and every 10 minutes, and removes what the rules above would. It also covers changes made outside these actions, such as a domain that is no longer verified. The first start after the update that added it removes every app password made before the update.

Opening webmail from the panel signs in with a short-lived app password of the same kind, so the same rules apply to it.

Stalwart API keys are not available to mailboxes: `install_stalwart_apply` disables them on the built-in **User** role (next to the lockdown below), and the reconciler removes any that exist.

## Mail Domains

Mail is organised **per domain**, not as one flat page. `/jabali-panel/mail-domains`
lists your **Mail Domains**: the domains with mail on, sortable, with an SSL
column, a live queue count, Disable and Delete in each row's menu, and a
**Create Mail Domain** button. Click a domain to drill into its accounts and
settings. Old `/jabali-panel/mail` links land here.

Per-domain tabs:

- **Mailboxes** — create, change password, set quota, delete.
- **Forwarders** — forward `alice@example.com` to one or more external addresses.
- **Autoresponders** — vacation responder per mailbox, with start/end window and subject template.
- **Catch-all** — send unmatched recipients to a chosen mailbox or `:drop` / `:reject`.
- **Disclaimer** — append HTML / plaintext disclaimer server-side to outbound mail per domain.
- **Shared Folders** — create IMAP shared folders for the team; manage ACLs.
- **CalDAV / CardDAV override** — repoint a domain's calendar + contacts SRV
  records at an external server instead of the built-in Stalwart DAV.
- **Logs** and **Statistics** — scoped to the selected domain (sortable Mail Logs
  columns); the old flat Mail page is retired.

### Mail-only delete

A domain can have **just its mail torn down while the web domain stays** —
deletes mailboxes, forwarders, DKIM, and the Stalwart domain entry
without removing the vhost or DNS zone.

### Mail off, or mail hosted elsewhere

When a domain's email is off, or its mail provider is anything but Jabali
(Google Workspace, Microsoft 365, No mail, or a custom DNS template), this
server stops acting as the domain's mail server:

- Mail sent from this server to the domain (a contact form, a WordPress
  notification, another account on the server) goes to the domain's MX, like
  mail to any other domain. It is no longer delivered here, or refused with
  "Mailbox does not exist".
- Mail for the domain that arrives without signing in is refused with
  `550 5.7.1`: the domain's mail is received by its own MX, not here.
- Mailboxes left on the domain are kept. They still sign in, keep the mail
  they have and can send, but new mail for them goes to the domain's MX, so
  move their mail before switching provider. While mailboxes are left, the
  panel asks first, and says how many, before it turns email off (the
  domain's Email switch, or Disable on Mail Domains) or saves another mail
  provider.
- The website's own mail (PHP `mail()`) keeps working: it signs in as the
  domain's send-only `noreply@`.

The panel applies a change within a minute. The mail server's routing for
these domains is set up by the installer, so an existing server gets it at
its next `jabali update`. If the mail server's outbound route, queue schedule
or recipient-stage script was changed by hand, the update leaves them as they
are and prints a warning, and such domains stay local.

## Mail groups

A mail group is an address on a domain whose members are mailboxes on the same domain. There are two types, fixed at creation, each on its own tab of the mail domain: **Distribution lists** (next to Forwarders) and **Groups** (shared workspaces). A group cannot take a mailbox's address, and a mailbox cannot take a group's (see [One owner per address](./user/mailboxes.md#one-owner-per-address)).

- **Distribution list** — every member receives their own copy of each message in their own inbox. A list of member mailboxes only is projected as a Stalwart mailing list whose recipients are the members that can receive mail (disabled and send-only mailboxes are left out), with no limit on how many. A list with outside addresses, or with **Internal delivery only**, is delivered by a Sieve script that redirects to each recipient instead (see below). A list with no recipients has nothing at its address, so senders get a `550` instead of mail that is accepted and dropped.
- **Shared workspace** — one shared inbox that members open in webmail (no copies are delivered), plus a shared calendar, contacts and files, and send-as the group address. It has mailbox members only.

### Outside addresses on a distribution list

A distribution list can also deliver to addresses on other mail servers, a Gmail address for example. Add them under **Manage members → Outside addresses** (or `PUT /api/v1/mailgroups/{id}/external-members` with `{"addresses": [...]}`). An address on the list's own domain is refused: add that mailbox as a member instead. A `+tag` is kept, so `someone+lists@gmail.com` receives the list as written.

The administrator sets how many outside addresses one list may hold in **Server Settings → Email → Distribution lists** (default 20, at most 20, 0 allows none). Lowering the limit keeps the addresses lists already hold; a list over it can remove addresses but not add new ones.

A list with outside addresses is delivered by a Sieve script on the list's address that redirects to each recipient, member mailboxes included. Every copy leaves with the list's address as its envelope sender, so the receiving provider checks your server (SPF), not the original sender's. The message itself is not changed and still shows the original sender in From, so a provider can still file mail from someone outside your domain as spam when that sender's domain asks for strict checks (DMARC). A delivery failure for an outside address comes back to the list's address and is dropped, not copied to the members.

Stalwart allows 20 redirects per message, so a list with outside addresses holds at most **20 recipients, member mailboxes and outside addresses together**: a change that would take such a list past 20, including the first outside address on a list of 20 or more mailboxes, is refused with `422 too_many_members`. Removing the last outside address makes the list a mailing list again, with no limit.

**Internal delivery only** accepts mail from senders in the group's own domain and rejects everyone else; a list's outside addresses still receive what the list accepts. On a distribution list it uses the same Sieve redirect, with the same limit of **20 recipients, member mailboxes and outside addresses together**: going past that, or turning internal-only on for a larger list, is refused with `422 too_many_members`.

Distribution lists are re-applied to Stalwart by the reconciler whenever the list or a member changes (and every 15 minutes), so a failed save heals on its own. On servers set up by an earlier release, a distribution list was stored as a shared inbox that members could not see. The first reconcile after the update converts each one. Any mail already in that inbox is copied into every member's inbox first, and the old inbox is removed only after every copy succeeded.

## Per-domain deliverability (admin)

`/jabali-admin/mail/deliverability` — for every domain, shows:

- DKIM key presence + DNS publication state
- SPF record presence + soft/hard fail
- DMARC record + policy
- MTA-STS policy + MX host alignment (per-domain MTA-STS)

Buttons:
- **Rotate DKIM** — generate a new DKIM key, publish DNS record, retire the old key on the configured grace period.

## Website mail: local mail server or a smarthost

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

How the smarthost mode works:

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
  without a package can't. The card shows how many accounts can send,
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

## Spam filter

Stalwart gives every incoming message a spam score. **Server Settings → Email
→ Spam filter** sets what happens at each score:

| Threshold | Default | At or above it, the message |
|---|---|---|
| Move to Junk | 5 | goes to the mailbox's Junk folder |
| Reject | 15 | is refused at SMTP time; the sending server gets an error, so the sender knows |
| Discard | 20 | is dropped without telling anyone |

- Each threshold is at most 100. Reject and discard can be turned off (stored
  as 0); when on, each must be above the Junk threshold.
- Stalwart checks reject before discard. With rejecting on, a discard
  threshold at or above the reject threshold never applies (the defaults are
  like this: mail scoring 20 is rejected, not discarded).
- A higher Junk threshold files fewer real messages as Junk and lets more spam
  reach the Inbox.
- The thresholds are server-wide. The panel stores them and the reconciler
  applies them to Stalwart within a minute, followed by a settings reload
  (Stalwart keeps the old thresholds until it reloads). An install or
  `jabali update` no longer sets them, so an admin's values stay. A threshold
  changed directly on Stalwart is put back within ten minutes.
- A message's `X-Spam-Status` and `X-Spam-Result` headers show its score and
  the rules behind it.

Mail from a sender in the mailbox's contacts (any of its address books in
webmail) is not treated as spam (`X-Spam-Status: No, reason=card-exists`),
as long as the sender's domain passes SPF or DMARC; a forged sender with the
same address still goes through the filter. A mailbox's own filter rules
(Sieve) can't move a message out of Junk.

### Trusted senders (per mailbox)

Each mailbox has a list of senders it trusts: **Edit mailbox → Trusted
senders**, or `GET` / `POST` / `DELETE /api/v1/mailboxes/{id}/trusted-senders`.
The rows live in `mailbox_trusted_senders` (migration 000320). The agent verb
`mailbox.trusted_senders.apply` writes them as contact cards in a **Trusted
senders** address book of the mailbox's Stalwart account, and the spam filter
trusts them like any contact (above).

- **Matching.** Stalwart matches a sender to a card exactly, ignoring case
  (tested on Stalwart 0.16.24). So addresses are stored lowercased, with the
  domain in punycode, and keep a +tag: `news+weekly@x.com` and `news@x.com`
  are different senders.
- **Limit.** A mailbox can trust up to 500 senders.
- **Which cards the panel manages.** Only the cards it wrote, marked by uid
  `urn:jabali:trusted:<address>`. A card of the panel's that the user moved
  to another address book is moved back. The user's own contacts are left
  alone, even at a trusted address.
- **Adding.** The panel sends the mailbox's list to Stalwart at once. If
  that fails, the row is kept and the answer carries a `warning`.
- **Removing.** The panel sends the list to Stalwart first, and deletes the
  row only after Stalwart has taken it. If Stalwart can't, the answer is
  `502 mail_server_unavailable` and the sender stays on the list. So the
  list never shows a sender as removed while Stalwart still trusts it.
- **Reconciler.** The `mailbox.trusted_senders` pass retries failed pushes
  and re-applies each mailbox's list every hour. It runs only for domains
  whose mail Jabali hosts.
- **New mailboxes.** A mailbox that has never signed in gets its Stalwart
  account created, so the first message from a trusted sender is already
  trusted.
- **Backups.** A backup carries each mailbox's list (`trusted_senders`). A
  restore adds it back, checking each address the way the API does.

## Outbound throttles

`/jabali-admin/mail/throttles` — per-sender + per-domain rate limit (msgs / minute, msgs / hour, recipients / message). Bulwark enforces; CrowdSec sees throttle hits and can escalate.

## Stalwart reports

Stalwart ingests inbound TLS-RPT, MTA-STS-RPT, and DMARC aggregate reports into the panel DB. Visible per-domain under Deliverability.

## Expression filters

Admin-defined Stalwart expressions for routing / drop / quarantine. UI under Server Settings → Mail.

## Mail storage on another disk

Stalwart keeps all mail, message bodies included, in `/var/lib/stalwart`. To put it on another disk, mount the disk there, or bind-mount a directory of it there. Don't point Stalwart at a new path: its systemd sandbox lets it write only to `/var/lib/stalwart`, `/etc/stalwart` and `/etc/jabali-panel`, and its AppArmor profile allows only the same. With the disk mounted at `/var/lib/stalwart`, neither needs a change, and the system backup, which copies `/var/lib/stalwart`, still includes the mail.

1. Stop Stalwart: `systemctl stop jabali-stalwart`.
2. Copy the directory onto the new disk, keeping owners and permissions:
   ```bash
   rsync -aHAX /var/lib/stalwart/ /mnt/maildisk/stalwart/
   ```
3. Move the old directory aside, create an empty one, and bind-mount the copy over it:
   ```bash
   mv /var/lib/stalwart /var/lib/stalwart.old
   mkdir /var/lib/stalwart
   echo '/mnt/maildisk/stalwart /var/lib/stalwart none bind 0 0' >> /etc/fstab
   systemctl daemon-reload
   mount /var/lib/stalwart
   ```
4. Make Stalwart wait for the mount, so it never starts on the empty directory, and start it:
   ```bash
   mkdir -p /etc/systemd/system/jabali-stalwart.service.d
   printf '[Unit]\nRequiresMountsFor=/var/lib/stalwart\n' > /etc/systemd/system/jabali-stalwart.service.d/30-mail-disk.conf
   systemctl daemon-reload
   systemctl start jabali-stalwart
   ```
5. Once mail works, delete `/var/lib/stalwart.old`.

`jabali update` keeps the fstab line and the drop-in. Account backups export each mailbox through Stalwart, so they include its mail wherever it is stored.

If Stalwart's blob store already points at a path outside `/var/lib/stalwart`, that path needs three things: a `ReadWritePaths=` and a `RequiresMountsFor=` line in a drop-in like the one above, and a rule in `/etc/apparmor.d/local/usr.local.bin.stalwart-mail` (see [AppArmor → Custom rules](./admin/apparmor.md#custom-rules)). The system backup doesn't include that path.

A server set up either way isn't moved to Stalwart 1.0 by `jabali update`: see [Moving to Stalwart 1.0](#moving-to-stalwart-10).

## Moving to Stalwart 1.0

Stalwart 1.0 can't open a 0.16 mail store. So `jabali update` moves a server from 0.16 to 1.0 by copying the data with Stalwart's migration tool:

1. A server on a 0.16 release older than 0.16.27 first gets 0.16.27, the release the tool copies from.
2. The tool copies the mail into a new store beside `/var/lib/stalwart` while 0.16 keeps serving. This needs free space for about one and a half times the store, plus 1 GB.
3. Stalwart stops for the final copy, about half a minute on a small server, and starts again on 1.0. If 1.0 doesn't come up, the update switches back to 0.16.
4. The 0.16 store stays at `/var/lib/stalwart-0.16`, and its binary at `/opt/stalwart-0.16`, so the server can be switched back. Remove them once mail works on 1.0.

After the move:
- Mail apps that signed in with OAuth sign in again once. Apps that use a password aren't affected.
- Calendar and contact apps fetch their data again once.
- Search in mail is incomplete until Stalwart has rebuilt its index.
- A mailbox that turned on encryption at rest itself has to turn it on again.

The update moves only Jabali's own layout. The server stays on 0.16, keeps working, and the update says why in these cases:
- `/var/lib/stalwart` is a mount point or a symlink (see [Mail storage on another disk](#mail-storage-on-another-disk));
- the blob store points outside `/var/lib/stalwart`.

Moving such a server needs the migration tool run by hand.

## Architecture choices

- **DB-as-truth, reconciler-converged.** Mailboxes / forwarders / autoresponders are panel-DB rows; Stalwart's JMAP API is called by the agent on every change.
- **Self-deleting SSO file** for webmail one-click. Not a magic-link plugin, not a session cookie hand-off — the panel writes `/jabali-sso-<43-char-nonce>.php` into the mail vhost, redirects the user to it; the file `flock`s + `unlink`s itself on first hit or after 60 s.
- **Unix sockets only.** Stalwart admin HTTP pinned to `127.0.0.1:8080`; MariaDB skip-networking; nothing exposes mailbox auth over TCP outside the SMTP/IMAP ports themselves.
- **Cross-domain directory isolation.** Stalwart keeps all accounts in one flat directory — its per-tenant isolation is an Enterprise-only feature, and Jabali runs the OSS edition. Left at Stalwart's defaults, any authenticated mailbox can enumerate every account on the server across all domains via JMAP `Principal/query` or the WebDAV principal-search family (surfaced by the webmail's calendar/file share picker and recipient autocomplete). `install_stalwart_apply` converges the built-in **User** role to disable those enumeration permissions (`jmapPrincipalQuery`, `jmapPrincipalQueryChanges`, `jmapPrincipalChanges`, `davPrincipalList/Match/Search/SearchPropSet`) via `disabledPermissions`, which takes precedence and fails closed. The same patch disables API keys (`sysApiKey*`, see [App passwords](#app-passwords)). `jmapPrincipalGet` / `GetAvailability` stay enabled — they need a known principal id, so they can't enumerate — preserving self-account reads and free/busy. **Trade-off:** in-webmail sharing-by-search no longer works (the picker resolves targets through the same query); a user shares by knowing the exact address. True per-domain scoping would require Stalwart Enterprise + per-domain tenants. Same-domain recipient suggestions come back through the domain directory (next bullet).
- **Domain directory.** Each mail domain with mailboxes gets a read-only address book, `<domain> directory`. It lists the domain's enabled mailboxes (display name and address) and is shared read-only with them. Webmail suggests recipients from it; Bulwark reads contacts at sign-in, so a new mailbox appears in an open session after the next sign-in. CardDAV clients do not discover it (a mailbox's `addressbook-home-set` lists only its own home) but can add `/dav/card/jabali-directory%40<domain>/default/` by URL. System relays, the panel's notification sender (`jabali-notify@<panel hostname>`), send-only and disabled mailboxes are left out. The book belongs to a Stalwart Group principal at `jabali-directory@<domain>`. That local part is reserved at every create door and at backup restore, and the address receives no mail. The reconciler converges the directory through the agent verb `mail.directory.apply` (20 domains per tick, hourly audit). The directory is skipped for a domain where a mailbox, group or shared resource already holds its address. The domain purge removes the Group with the other accounts.

## CLI

```bash
jabali mailbox list --domain example.com
jabali mailbox create --domain example.com --local user --quota-mb 1024
jabali mailbox set-quota user@example.com 2048
jabali mailbox passwd user@example.com
jabali mailbox delete user@example.com
```
