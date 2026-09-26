# ADR-0105: Panel cert split — independent hostname + mail certs

**Status:** Accepted (amends ADR-0066)
**Date:** 2026-05-17

## Context

ADR-0066 modelled the panel TLS cert as a single singleton
`panel_certificate` row for the panel hostname. M32-followups disabled
the `mail.<panel-hostname>` SAN entirely (`extra_hostnames:[]`) because
bundling it into one LE order made an unpointed mail record fail the
*entire* panel cert on fresh installs.

The 2026-05-17 mx incident exposed the operator-facing cost: Panel SSL
showed one blended "Issuing…" badge. `mx.jabali-panel.com` was fully
issuable, but the operator could not see that — nor any mail status —
because there is only one row, one status, and mail isn't tracked at
all. Excluding mail removed the failure but also removed the
visibility and the mail cert.

## Decision

Split the panel cert into **two independent certificates**, tracked as
two rows in `panel_certificate` discriminated by `kind ∈
{hostname,mail}`:

- Each row owns the full ADR-0066 status state machine, its own
  cert/key path, retry/backoff, and Refresh.
- The **mail** row's issuance is gated by a routability preflight on
  `mail.<hostname>`. Not routable → the mail row parks in
  `pending_acme` with `last_error="mail DNS not pointed at server"`
  and retries independently. The **hostname** row reconciles and
  issues regardless — mail can never block it.
- Cert paths: hostname `/etc/jabali/tls/panel.crt` (unchanged), mail
  `/etc/jabali/tls/panel-mail.{crt,key}` (new). Per-kind deploy-hook
  reload chains.
- The UI Panel SSL block shows both rows' statuses separately.

Seeding the two rows is application-side (`PanelCerts.EnsureDefault`);
the migration is schema-only (kind column + PK repoint + backfill of
the existing row to `kind=hostname`).

## Consequences

- Operators see per-name truth (hostname Issued / mail Pending+reason).
- An unpointed mail record (Cloudflare-fronted, common) no longer
  hides or blocks the hostname cert.
- Replaces the M32-followups `extra_hostnames:[]` exclusion: mail is
  tracked + issued again, just independently and preflight-gated.
- One-time migration repoints the PK on a populated table; backfill of
  the existing row to `kind=hostname` must precede the PK switch.

Supersedes the panel-cert portion of ADR-0066; ADR-0066 otherwise
stands.

## Amendment — mail row follows the applied mail hostname (JAB-390, 2026-09-26)

The mail row is no longer tied to `mail.<hostname>`. Its name is the
**applied** shared mail hostname,
`models.EffectiveMailHostname(server_settings.mail_hostname, hostname)`.
With nothing applied, that is still `mail.<hostname>` (see the ADR-0048
amendment).

- **Seeding.** `EnsureDefault` seeds the mail row with the derived name.
  This is correct because no custom name can be applied before the row
  exists. JAB-389 keeps the row pinned afterwards: a panel-hostname
  change does not rename it.
- **Switchover.** Moving the row to a new mail hostname belongs to the
  JAB-390 switchover pass, not to a settings write. The pass runs this
  ADR's routability preflight and issuance against the **desired** name
  while the current mail cert keeps serving. It repoints the row, and
  writes the applied name, only after the new cert is issued and every
  dependent configuration has applied. A DNS or ACME failure leaves the
  row and the applied name unchanged and records the error for retry.
- **Display.** Panel SSL labels the mail row with its stored hostname.
  When that is empty, it falls back to the applied mail hostname rather
  than a fresh `mail.<primary>` derivation.
