# ADR-0120: Mail-provider DNS templates

**Status:** Proposed
**Date:** 2026-06-13
**Deciders:** Shuki (operator/architect)
**Related:** GH#181, ADR-0070 (auto-SAN skip), M6 email (000054), GH#132
(mail-cert autodiscover SAN 404), GH#134 (full mail record set)

## Context

A domain added to Jabali currently gets, when email is enabled, the full
Jabali mail record set (MX→`mail.<d>`, autoconfig/autodiscover, IMAP/
submission SRVs, DKIM, TLS-RPT, CAA) plus a mail SSL certificate whose
SANs include `mail/autoconfig/autodiscover/mta-sts.<d>`. Many domains do
not use Jabali as their mailserver — they use Microsoft 365, Google
Workspace, or no mail at all (common for subdomains). For those domains
the Jabali records are wrong, and the mail cert's aux SANs cannot validate
(the GH#132 `autodiscover` HTTP-01 404 class), leaving the operator to
hand-fix DNS and suppress cert SANs per domain.

The primitives to express "not Jabali mail" already exist —
`domain.email_enabled` (whole mail path) and `domain.skip_auto_san`
(ADR-0070, drop mail SANs from the cert) — but they are independent
booleans with no first-class "who hosts this domain's mail" intent, and
there is no way to publish a third-party provider's records.

## Decision

Introduce a single per-domain enum, **`mail_provider`** ∈ {`jabali`
(default), `none`, `m365`, `google`}, chosen at domain-add and editable
later. It is the single source of truth for the domain's mail posture;
`email_enabled` and `skip_auto_san` are **derived** from it, not set
independently:

- `jabali` → email_enabled=true, skip_auto_san=false (current behavior).
- `none` → email_enabled=false, skip_auto_san=true, no mail records.
- `m365` / `google` → email_enabled=false, skip_auto_san=true, publish the
  provider's MX + SPF + autodiscover (+ optional operator-supplied DKIM).

The **apex MX / SPF / DMARC** are the load-bearing part. Today they come
from `BootstrapRecords` with `managed_by = NULL` (so the `m6` email-disable
prune can't touch them) and are written once at domain-create;
`BuildEmailRecords` does not emit them. A provider switch must **replace**
the single apex MX and the single apex SPF (two `v=spf1` TXT at the apex is
an RFC 7208 permerror), so a new `mail-apex` managed scope becomes the sole
owner of one apex MX + one apex SPF + one `_dmarc`, written per provider
(`jabali`: MX `mail.<d>` / `v=spf1 mx …`; `m365`:
`<dashed>.mail.protection.outlook.com` / `include:spf.protection.outlook.com`;
`google`: `smtp.google.com` / `include:_spf.google.com`; `none`: omitted) by
delete-by-scope + insert so a switch can never duplicate SPF. The migration
re-stamps existing apex mail rows into `mail-apex`; the web apex-A and
`mail`-A records are untouched.

A new pure compiler `BuildExternalMailRecords(provider, zone, tokens)`
produces provider records from **constants** (MX/SPF/autodiscover strings
are fixed per provider); only optional DKIM tokens carry operator data and
are charset-validated. Each provider's rows carry a distinct `managed_by`
sentinel so a provider switch prunes the previous set by scope without
touching operator-authored records. CAA moves out of the mail set into the
always-published web/bootstrap set (it gates the LE *web* cert, which
issues regardless of mail provider). PowerDNS query cache is purged on
switch.

## Alternatives considered

- **Keep two independent booleans (email_enabled + skip_auto_san), add a
  separate "provider records" feature.** Rejected — that is exactly the
  drift surface that has bitten before (a domain marked email-off but
  still carrying mail SANs, or vice versa). One enum that derives both is
  the invariant.
- **Free-form DNS template editor (operator writes arbitrary record
  sets).** Rejected for v1 — powerful but it re-implements the DNS editor
  the panel already has, and the value johnnyq asked for is the *curated
  presets*. Power users can still hand-edit records after picking `none`.
- **Auto-fetch DKIM from the provider API (Graph / Google Admin).**
  Rejected for v1 — adds OAuth + per-provider API surface; operator pastes
  the DKIM token or adds it manually. Forward-compatible.
- **Legacy Google 5×ASPMX MX set.** Rejected — Google recommends the
  single `smtp.google.com` MX for new domains; v1 ships that.
- **A `mail_provider=external-custom` with operator-supplied MX/SPF.**
  Deferred — `none` + manual DNS editing covers it until there's demand.

## Consequences

**Positive**
- One click at domain-add selects the mail destination; the right records
  (or none) are published, and the cert never carries un-validatable mail
  SANs (closes the GH#132 class for external/none domains).
- `email_enabled` / `skip_auto_san` can no longer drift from intent.
- Forward-compatible: new providers are new enum cases + a compiler branch.

**Negative / risk**
- Switching providers is destructive to the previous provider's *managed*
  DNS rows (scoped by `managed_by`; operator records untouched) and forces
  a cert reissue — must be communicated in the UI.
- DKIM for M365/Google still needs an operator-supplied token for full
  deliverability; MX/SPF/autodiscover auto-publish, DKIM does not unless
  the token is provided.
- Backfill: `email_enabled=0 → none`; `email_enabled=1 & skip_auto_san=0
  → jabali`. The ADR-0070 combo `email_enabled=1 & skip_auto_san=1` maps to
  `jabali` but the migration PRESERVES its `skip_auto_san` (the
  `jabali ⇒ skip_auto_san=false` derivation fires only on an explicit
  provider re-selection) — otherwise the backfill would silently re-add mail
  SANs and force a cert reissue. This ADR thus carves out a legacy
  `jabali + skip_auto_san=1` state, superseding that ADR-0070 combination
  only when the operator actively re-picks the provider. A domain with
  hand-configured external mail reads as `none` until re-selected; its
  hand-authored (unmanaged) records survive.

## Amendment — owner-authored DMARC and SPF (GH #2523, 2026-11-16)

The prune that runs for `none` / `m365` / `google` removed every unscoped
`_dmarc` TXT and every apex SPF outside `mail-apex`, owner rows included. So an
owner's DMARC, and on `none` their SPF, disappeared on the next pass, against
"operator records untouched" above and ADR-0107.

- **Who owns a row.** The prune removes only rows the panel wrote: `Managed`
  with no `managed_by` (the bootstrap set) or the `m6` marker. A row the owner
  created or edited is `Managed=false` (ADR-0107) and stays. That covers the
  `_dmarc`, the bootstrap-shaped MX, the `mail` A/AAAA and, on `none`, the SPF.
- **DMARC.** The `mail-apex` `_dmarc` steps aside while the zone has an
  owner `_dmarc`, so there is never more than one DMARC record. It is
  published again once the owner's is deleted.
- **SPF on Microsoft 365 / Google is unchanged.** The provider's SPF is the
  only `v=spf1` at the apex, and an owner's is still replaced. A switch to a
  provider must replace an SPF written for the mail the domain had before.
