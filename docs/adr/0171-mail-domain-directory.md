# ADR-0171: A read-only directory address book per mail domain

**Status:** Accepted (2026-09-28)
**Driven by:** GH #1637 (option A).
**Related:** GH #1581 and its fix #1605 (cross-domain principal enumeration), ADR-0042 (SQL directory `mailboxes` table), M52 shared resources (`plans/m52-mail-shared-resources.md`: one Group principal per shared resource, `shareWith` grants).

## Context

Stalwart OSS keeps every account in one flat directory. #1605 took the
principal-listing permissions (`jmapPrincipalQuery`, the WebDAV principal
search family) away from the built-in User role, because they let any mailbox
list every account on the server, across all domains. The price was same-domain
recipient suggestions in webmail: Bulwark had filled them from that listing.

#1637 first proposed restoring the listing through Bulwark's server-side JMAP
proxy, scoped to the caller's domain (option B2). Reading Bulwark and testing
on Stalwart 0.16.15 showed that this does not work without upstream changes:

- Bulwark's browser code sends JMAP straight to Stalwart with its own
  `Authorization` header. The `/api/account/stalwart/jmap` proxy only carries
  the `x:` management calls, so there is no server-side hop to filter.
- A master-user login (`mailbox%master`) acts as that mailbox, so
  `Principal/query` returns `forbidden` for it too. Only the admin can list
  principals, and the admin sees all of them. Bulwark would need the Stalwart
  admin credentials to do B2.
- Bulwark (1.8.0, which Jabali pins, and its main branch) builds recipient
  suggestions from the contacts of **every** contact-capable account the
  session can see, shared accounts included (`getAllContacts`,
  `isShared: !isPrimary`).

The third point allows a fix without touching Bulwark: an address book shared
with the mailbox appears in its suggestions.

## Decision

The panel keeps one address book per mail domain. It lists the domain's
mailboxes (display name and address) and is shared **read-only** with them.

- **Host.** The book belongs to a Stalwart **Group** principal at
  `jabali-directory@<domain>`, the same host model the M52 shared resources
  use. It is created without a secret. The local part `jabali-directory` is
  reserved (`mailaddr.DirectoryLocalPart`). Every door that creates an address
  refuses it: mailbox create (API, CLI, cPanel migration), backup restore,
  shared-resource create, and mail-group create (API, CLI).
- **Agent verb `mail.directory.apply`.** It takes the whole desired state
  (`host_email`, `display_name`, `entries[{email,name}]`, `readers[]`) and
  converges to it:
  1. It ensures the Group.
  2. It refuses a host whose `x:Account/get` `@type` is not `Group`, so an
     older principal at that address is never taken over.
  3. It ensures the book: the Group's default book, or a new one.
  4. It converges the cards. Each card's uid is
     `urn:jabali:directory:<email>`. The verb destroys duplicates and cards it
     did not write.
  5. It replaces the book's `shareWith` with `{readerAccountId: {mayRead: true}}`.

  The verb resolves every reader before step 5. A lookup error fails the call,
  because a `shareWith` pushed without that reader would revoke its grant. The
  verb validates all input: canonical addresses in the host's domain only,
  names without control characters and at most 255 bytes, at most 10000
  entries.
- **Reconciler pass (`PhaseMailDirectory`).**
  - Scope: domains with `email_enabled` and the jabali mail provider.
  - Entries and readers: the domain's mailboxes, except system relays,
    send-only accounts and disabled mailboxes.
  - Needed: a domain with no mailbox a person uses gets no directory.
  - Fingerprint: the spec plus the mailbox row ids. A mailbox deleted and
    created again at the same address is a new Stalwart account and must get
    its grant.
  - Ledger: a domain that leaves the scope is forgotten. Turning mail off
    purges the Group with the other accounts, so the directory is rebuilt as
    soon as mail returns.
  - Pacing: 20 domains per tick, a 15-minute backoff after a failure, and an
    audit every hour.
  - Retry: a reader the agent could not resolve counts as a failure, so the
    domain is retried rather than stamped as done.
  - Collision: a domain whose directory address is already held by a mailbox,
    mail group or shared resource row is skipped. A lookup error counts as
    held.
- **Teardown.** `mail.domain.purge_accounts` already destroys every account in
  the domain, the Group included. Domain delete, user delete and the mail-only
  purge need no new step.

### Verified on Stalwart 0.16.15 (test box)

- A reader sees the Group as a shared account, over JMAP and over CardDAV
  (`/dav/card/jabali-directory%40<domain>/`).
- Every write a reader tries returns `forbidden`: card create, update and
  destroy, and book rename and destroy.
- A mailbox of another domain gets `forbidden`.
- `x:Account/get` reports `@type` `Group` for the host and `User` for a mailbox.
- `RCPT TO:<jabali-directory@<domain>>` returns 550: delivery resolves through
  the SQL directory, which has no row for the host.
- Stalwart shows a reader its own label for a shared book: the default book
  reads "Stalwart Address Book (jabali-directory@<domain>)" and any other
  book reads "Address Book". The name the verb sets is seen only by the owner
  and the admin. Bulwark lists the directory under the account name, "Shared:
  jabali-directory@<domain>".
- A PROPFIND on `/dav/card/` lists the directory's home to its readers only.
  A reader's `addressbook-home-set` holds only its own home, so a CardDAV
  client that follows the standard discovery does not find the directory.
  The book is at `/dav/card/jabali-directory%40<domain>/default/`.

### Verified in Bulwark (real browser, test box)

- alice typing "bo" is offered bob@<domain>. A new mailbox is found by its
  display name.
- carol, in another domain, is offered nothing for "al" and "bo", and only
  her own address for a prefix both domains' names share.
- Bulwark reads contacts at sign-in. A session that was open before a new
  mailbox's card existed did not offer it; a new sign-in did.
- Disabling bob removed his card, and his grant: signing in right after he
  was enabled again showed no directory until the next pass.

## Consequences

**Positive**

- Same-domain recipient suggestions work again in webmail. A CardDAV client
  can read the directory too, once the user adds it by URL.
- The #1605 isolation stays as it is. No principal listing is re-enabled, and
  no mailbox can read another domain's directory.
- No Bulwark change, and no new inbound authentication surface on the panel.
- The panel database is the source of truth, and the reconciler heals drift
  and failed applies.

**Negative**

- The share picker (calendars, files) still cannot search. It needs principal
  ids from `Principal/query`, which stays disabled. Sharing still means typing
  the exact address.
- Every mailbox in a domain can read the display names and addresses of the
  other mailboxes in that domain. The domain belongs to one panel user, and
  before #1605 the whole server could see them.
- Each mail domain with mailboxes gets one more Stalwart principal. Its
  address is reserved.
- A change normally reaches Stalwart within one reconciler tick (60 s by
  default), and a failed apply waits 15 minutes before its retry. A webmail
  session that is already open sees it after its next sign-in.
- CardDAV clients do not discover the directory; the user adds its URL.
- The cost is two JMAP lookups per reader per apply, plus the card sync. The
  ledger makes an unchanged domain free except for the hourly audit.
- The agent refuses a domain with more than 10000 mailboxes rather than
  listing only some of them.

## Alternatives considered

- **B2: domain-scoped listing in Bulwark's proxy.** Rejected. The browser
  bypasses the proxy for JMAP, and scoping would need the Stalwart admin
  credentials inside Bulwark. It would also be an upstream change.
- **Re-enable `Principal/query` for the User role.** Rejected. That is the
  cross-domain leak #1605 fixed.
- **A panel endpoint that serves the directory.** Rejected. Bulwark has no
  authenticated path to the panel, and one would be a new inbound surface.
  Bulwark also does not read contacts from anywhere but JMAP.
- **Copy the cards into each mailbox's own address book.** Rejected. That is
  N×N cards per domain. The user could edit or delete them, and the copies
  would conflict with the user's own contacts.
