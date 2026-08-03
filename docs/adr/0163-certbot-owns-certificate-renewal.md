# ADR-0163 — certbot owns certificate renewal; the panel observes

- Status: Accepted
- Date: 2026-08-03
- Tracks: JAB-203

## Context

The panel had two renewal stories and ran neither honestly.

`ListDueForRenewal` selects issued certificates approaching expiry. Its only
caller was `StartSSLTicker`, and a repo-wide grep returned **only the
definition** — zero call sites, tests included. The ticker that would have
driven panel-side renewal had never run.

The live ticker, `RetrySSLDueForACME`, matches `pending` and
`pending_acme_retry` only. It never matches `status = 'issued'`, so an issued
certificate was never picked up by the panel for renewal at all.

What actually renews certificates is Debian's own `certbot.timer`, against
`/etc/letsencrypt/renewal/<domain>.conf`. That is a fine mechanism. The problem
is that it runs entirely outside the panel, and the panel assumed otherwise.

Two consequences, both observed on a live box:

**`expires_at` drifted.** Measured on testserver 2026-08-03: the panel row said
`2026-08-15` while the certificate on disk ran to `2026-10-30` — 2.5 months
stale, with `last_renewed_at = NULL` and `last_attempt_at` still in May.
`eventsources/cert_renew.go` computes `domain.expiry.7d` and `domain.expiry.1d`
from that column, so those alerts fired off a value nothing kept current, and
could be wrong in either direction.

**`cert.renew.fail` could not fire for the failure that actually happens.** It
triggers on `status = failed && last_error != nil`, which only a panel-driven
ACME attempt ever sets. A certbot-timer failure leaves the row untouched:
testserver sat at `status=issued`, `last_error=NULL`, `retry_count=0` through
daily renewal failures from 2026-07-29. The kind is `DefaultOn: true` and
advertises *"ACME renewal failed — action usually required"*, and for the most
likely renewal failure it never fired. The operator's only signal was a failed
systemd unit nobody was watching.

## Decision

**certbot's timer owns renewal. The panel observes and reports.**

1. **Delete the panel-side renewal path.** `StartSSLTicker` and
   `ListDueForRenewal` are removed rather than wired. Wiring code that has been
   unexercised for months, to run a second renewer alongside certbot, trades a
   known-idle path for an unknown-active one — and two renewers racing into
   Let's Encrypt's rate limits is a worse failure than the one being fixed.

2. **Add an observation pass** (`ReconcileSSLObservation`, hourly). It reads the
   certificate file at each issued row's `cert_path`, parses the leaf's
   `notAfter`, and corrects `expires_at`. `last_renewed_at` is stamped only when
   the certificate moved *forward*, so it keeps meaning "when this certificate
   last got newer" rather than "when we last looked".

3. **Alert from observation, not from attempts.** A certificate whose on-disk
   `notAfter` is inside the renewal window and still not renewed is a renewal
   that is silently failing — true regardless of who was supposed to renew it.
   That fires `cert.renew.fail` with the certbot commands an operator needs.

## Consequences

The panel cannot renew a certificate on demand. It never could — the code that
claimed to had no call sites — so this removes an illusion rather than a
capability. On-demand issuance (`ssl.issue`, `ssl.renew` via the agent) is
unaffected; only the scheduled sweep is certbot's.

The panel's view converges on disk within an hour of any renewal, whoever
performed it. That includes renewals an operator runs by hand, which previously
left the row stale forever.

`cert.renew.fail` now fires for certbot-timer failures. It may also fire for a
certificate an operator is deliberately letting lapse; the 21-day window makes
that unlikely to be a surprise.

Reading the certificate needs no agent round-trip: `/etc/letsencrypt/live` and
`/archive` are `0755` on Debian, and panel-api's `jabali` user can read
`fullchain.pem` — verified on a live box before choosing this design.

An unreadable certificate file is recorded as unreadable and skipped. It is
never treated as expired: a permissions change must not page an operator about a
healthy certificate, and must not pass silently either.

## Alternatives considered

**Wire `StartSSLTicker` and disable `certbot.timer` fleet-wide.** Single source
of truth, native failure reporting. Rejected: it re-implements what certbot
already does well, and any box where the timer survived — an older install, a
hand-edited unit, a restored image — would renew twice. It also puts the panel
on the hook for ACME retry semantics, lockfiles and `renewal.conf` lineage,
which we already know is delicate (#738/#745: a custom certificate over an LE
lineage left an orphan `renewal.conf` that aborted `certbot renew` box-wide).

**Leave renewal alone and only fix `expires_at`.** Would have fixed the drift
but not the missing failure signal, which is the half that costs an outage.

**Have the panel drop and re-issue on expiry.** Destructive, rate-limit hungry,
and solves a problem certbot does not have.
