# ADR-0174: Website mail through the operator's smarthost

**Status:** Accepted (2026-10-10)
**Driven by:** GH #2056 (a reporter who runs their own mail and DNS asked for "Website sends email", delivering through their smarthost; design approved by the maintainer on 2026-10-10).
**Related:** JAB-230 (the jabali-sendmail shim and its relay credentials), GH #371 (send-only relay mailboxes), GH #282 (no-package policy), ADR-0050 (agent ingress).

## Context

Every PHP-FPM pool pins `php_admin_value[sendmail_path]` to
`/usr/local/libexec/jabali/jabali-sendmail`. The shim runs as the site's user
and submits to the local Stalwart on 127.0.0.1:587 as the domain's
`noreply@` send-only mailbox. Without the mail module there is no Stalwart, so
PHP `mail()` (WordPress's default `wp_mail`, most contact forms) fails.
Installing postfix doesn't help: the pools never call `/usr/sbin/sendmail`.
The mail module itself needs the DNS module, so an operator who already runs
mail and DNS elsewhere had no way to let the sites send.

## Decision

1. **A server setting picks where website mail goes**: the local mail server
   (the default, today's behavior) or a smarthost. The smarthost is a host, a
   port (25, 465, 587 or 2525 only), an encryption mode and an optional login.
   - `starttls` requires STARTTLS; it never falls back to plaintext.
   - `tls` is implicit TLS.
   - `none` is plaintext and allows no login, so a password never crosses the
     wire unencrypted.
   - Certificates are always verified against the host.
2. **The password is stored sealed** with the panel's sso key
   (`server_settings.smarthost_password_enc`, AES-256-GCM), like the Cloudflare
   token (JAB-235). The API never returns it; an empty password on save keeps
   the stored one, but only for the host and username it was saved with. A
   test or save aimed at another host needs the password typed again, so an
   admin session can't be used to send the stored password to a host it
   chooses and read it there.
3. **Switching to the smarthost tests it first**: connect, TLS, login, quit.
   Nothing is saved when the test fails, so a typo can't silently stop every
   site's mail. A separate Test button checks the form without saving. The
   shared client is `internal/smarthost`, used by both the panel and the relay.
4. **A separate relay, `jabali-mailrelay`, holds the password.** The shim runs
   as the site's user, so anything it can read, the site's PHP can read too:
   the shim must not hold the smarthost login. In smarthost mode the shim hands
   the message to the relay over a unix socket. The relay runs as
   `jabali-agent mailrelay` under its own `jabali-mailrelay` user (never root,
   never the agent), learns the caller's UID from the socket (SO_PEERCRED), and
   only senders on its list may send. One account may hold at most two
   connections at a time, and a sending slot is taken only once its whole
   message has arrived, so one site can't hold the relay for the others.
5. **The envelope sender is unchanged**: `noreply@` a domain the calling user
   owns (the From domain when the user owns it, else the user's primary domain),
   the same rule the shim follows with Stalwart. SPF and DKIM for those domains
   are the smarthost's job.
   - **The From header is restricted too.** With Stalwart, its sender check
     stops a site from writing someone else's address in From. The smarthost
     can't tell which site sent a message, so the relay does it: a message
     keeps its From only when that is one header naming one address in a
     domain the user owns. Otherwise From becomes the envelope address (the
     display name is kept) and the original address moves to Reply-To when the
     message has none, so a contact form that puts the visitor in From still
     gets replies to the visitor. A site's own Sender header is dropped.
   - The relay writes the From header fresh from the parsed address, so a
     comment or group another parser might read differently never reaches
     the smarthost, and a display name that carries an address is dropped.
     A bare CR becomes a line break before anything is parsed, so the relay,
     the SMTP client and the smarthost all see the same lines.
6. **No local queue in v1.** If the smarthost is down or answers 4xx, `mail()`
   returns false and the failure is logged. A spool can follow if needed.
7. **A "Website sends email" package flag** applies in both modes.
   - A migration sets it on for every existing package, so nothing changes for
     existing sites.
   - New packages start with it off; the package editor shows it.
   - Users with no package (GH #282) may send through the local mail server, as
     today, but not through the smarthost: the relay sends with the operator's
     login and reputation, which makes it a privileged feature.

## Not decided here

- Direct delivery to recipients' MX without the mail module: no queue or
  retries, no DKIM, and the server IP's reputation; mail would land in spam.
  Direct delivery stays the mail module's job.
- Stalwart sending through the smarthost when the mail module is on. Stalwart
  supports Relay routes (`MtaRoute` of type `Relay`), so this can be a later
  slice.
- A per-site send rate limit in the relay. v1 relies on the smarthost's own
  limits.
- Trusting a private CA for a smarthost with an internal certificate.

## Consequences

- Operators without the mail module can let their sites send, through their
  own mail system.
- A new service and a socket that every site's user can reach. Its only
  input is a message and a recipient list. The sender comes from the caller's
  UID: the message's From header can only name one of that UID's own domains.
- The smarthost password lives in the panel database (sealed) and in the
  relay's root-owned config (plaintext, 0640 root:jabali-mailrelay), like the
  Stalwart and restic credentials the box already holds.
