# ADR-0110 — M47 Waves 4/6/8/9: Stalwart-report ingest + deliverability score

**Status:** Accepted
**Date:** 2026-05-20
**Supersedes:** the Wave 4/6/8 architecture sketched in `plans/m47-email-deliverability.md` (JMAP mailbox poll)

## Context

The original M47 plan had Waves 4/6/8 each building a separate JMAP
mailbox-poll loop to read RUA / TLS-RPT / ARF emails out of the
operator's report mailbox, parse them, and persist normalised rows.

A live spike on `.150` (mx.jabali-panel.local, Stalwart 1.0.0,
2026-05-20 — saved as `project_stalwart_native_report_storage`)
showed Stalwart already does this work and exposes the parsed
payloads as first-class schema objects:

- `DmarcExternalReport` — `report:DmarcReport` (RFC 7489)
- `TlsExternalReport`   — `report:TlsReport`   (RFC 8460)
- `ArfExternalReport`   — `report:ArfFeedbackReport` (RFC 5965)

All three share an envelope (`from`, `to`, `subject`, `receivedAt`,
`expiresAt`) and Stalwart exposes filters keyed on `receivedAt` and
`domain`. The Wave 6 standalone parser (PR #63, `internal/dmarcrua`)
still has value as a fallback for direct operator file-uploads, but
the **primary ingest source** collapses to ONE pattern:

> Poll Stalwart admin REST every 5 min for each report type with
> `receivedAt:>cursor`; persist into the matching aggregate table;
> dispatch an M14 envelope.

## Decision

1. **`internal/stalwartadmin` is a stalwart-cli subprocess wrapper**,
   not a hand-rolled HTTP client. Stalwart's admin REST uses HTTP/2 +
   hash-redirect URLs that change with every schema version; the
   upstream CLI tracks the schema and gives us a stable interface.
   - `Client.Query(ctx, typeName, filters...)` shells out to
     `/usr/local/bin/stalwart-cli` with `--json`, returns the raw
     JSON array stdout.
   - `Client.Get(ctx, typeName, id)` for singletons.
   - Both validate `typeName` / `id` / `filter` strictly (CamelCase /
     alphanumeric-only / no `-` prefix) — argv-injection guard, since
     filters become CLI flags.
   - install.sh `_install_stalwart_cli` already provisions the binary
     on every host, so the panel-api can rely on it being present.
2. **Three thin ingest sources** (one per report type) share the
   same shape: poll on `mailDmarcIngestTick = 5 * time.Minute`, cursor
   from each repo's `MostRecent*` method, slack the cursor backwards
   by 1h to catch out-of-order `receivedAt`, idempotency gate via
   `ExistsForReport` / `ExistsForStalwartID`.
3. **Schema additions**
   - `mig 000143` — `arf_report` table (DMARC + TLS-RPT tables
     already exist from mig 000139). UNIQUE index on `stalwart_id`
     drives idempotency on re-runs.
   - `models.ARFReport` + `models.TLSRPTAggregate` (the latter only
     adds Go shape; table existed from mig 000139, just unused).
   - Repos: `ARFReportRepository`, `TLSRPTAggregateRepository`,
     extended `DMARCAggregateRepository` with `MostRecentWindowEnd` +
     `CountFailuresSince`.
4. **M14 dispatch** — three new EventKinds (string literals;
   ADR-0056 doesn't enum these):
   - `mail.dmarc.report_received` — severity warning when >10% of
     records DKIM-fail.
   - `mail.tls.report_received` — severity warning when any
     `totalFailureSessionCount > 0`.
   - `mail.feedback.received` — always warning (an inbound ARF
     report is an operator-worthy event).
5. **Wave 9 score card** — single admin REST `GET /admin/mail/deliverability`
   returns a 0-100 score plus four `components` (RBL / DMARC failures /
   TLS-RPT failures / abuse reports), each capped at a 25-point
   deduction so the operator sees exactly which signal cost what.
   Mounted at `/jabali-admin/mail/deliverability`.

## Trade-offs

- **Subprocess vs HTTP client**: subprocess adds ~30 ms per call but
  costs ZERO maintenance when Stalwart's REST URL hashing rotates.
  At 5-min cadence × 3 ingest types = 36 execs/hour — invisible
  load.
- **Cursor in DB vs separate cursor table**: persisting in the data
  table avoids a separate `stalwart_report_cursor` table and its
  schema-drift risk. Cost: a `SELECT MAX(window_end)` per poll, but
  the existing index covers it cleanly.
- **Cursor slack (`1h`)**: trades a few duplicate-existence checks
  per pass against the risk of permanently missing late-arriving
  reports (Stalwart can buffer reports for hours when its outbound
  queue is delayed). The `ExistsForReport` gate makes duplicates
  cheap; missed reports are silent and hard to detect.
- **Wave 9 score is server-wide** (not per-domain). Per-domain
  breakdown is a follow-up — the data is already in the aggregate
  tables, just needs a UI surface.

## Verification

- `internal/stalwartadmin` unit tests pin: happy-path argv, empty
  output → `[]`, type-name rejection, filter-flag rejection, stderr
  surfaced in errors, get rejects bad ids.
- Live-VM verification of the ingest loops is deferred to operator
  smoke (see `plans/m47-rest-waves-runbook.md`) — needs real RUA
  reports to land in the report mailbox, which takes 24-48h after
  first DMARC publication.

## Companion findings

- `project_stalwart_native_report_storage` — the spike that drove
  this architecture.
- `project_stalwart_mtaouthound_throttle_pin` — Wave 3 throttle
  shape pinned at the same time (deferred to Wave 7d when MtaSts
  singleton sync also lands via this same client).

## Amendment 2026-09-29 — ingest over JMAP, listing instead of a cursor

The ingest as built in 2026-05 never stored a report. On the .60 test
box (Stalwart 0.16, three reports delivered by SMTP to
`postmaster@mx.jabali-panel.com`) every part of Decision 1 and 2 failed:

- The panel's AppArmor profile denies it exec of `stalwart-cli`, and
  the panel user cannot read the admin secret in `stalwart.env`.
- `receivedAt` is neither filterable nor sortable on the report types
  (`unsupportedFilter` / `unsupportedSort`), so a `receivedAt:>cursor`
  query cannot be made.
- Lists inside a report come back as objects keyed `"0"`, `"1"`, …, not
  arrays; field names (`policyDomain`, `dateRangeBegin`,
  `organizationName`, `failedSessionCount`, …) and camelCase enums
  (`certificateExpired`, `authFailure`) differ from what the code read.
- ARF addresses keep their angle brackets, so the abuse count's
  `original_mail_from LIKE '%@<domain>'` could not match.

What replaces Decisions 1 and 2:

1. **`internal/stalwartadmin` is a JMAP client** (GH #1936): Basic auth
   `admin:<token>` from `/etc/jabali-panel/stalwart-admin.token`, which the
   panel user can read, to `http://127.0.0.1:8446/jmap` with the
   `urn:stalwart:jmap` capability. `QueryIDs` pages `x:<Type>/query`
   by position (1000 per page, `calculateTotal`); `GetMany` batches
   `x:<Type>/get`. The token travels only in the Authorization header.
2. **Listing, not a cursor.** Each pass (every 5 min, 60 s budget) lists
   every report id Stalwart holds (Stalwart expires them after about 30
   days) and fetches only the ids this process has not handled yet, at
   most 500 per pass in batches of 32. An id is marked handled only
   when its store succeeds, so a failed store is retried next pass; ids
   Stalwart no longer lists are forgotten. After a restart every report
   is fetched once more and the repo checks keep it from being stored
   twice. A cursor would also have let one forged report with a future
   date hide every later report.
3. **Duplicate keys.** A DMARC report and a TLS policy block are keyed
   by reporter + policy domain + date range (`ExistsForReport` gained
   the domain): a big receiver sends one report per domain for the same
   day, and the old reporter + range key dropped every domain after the
   first. TLS checks each policy block, not only the first. ARF stays
   keyed by Stalwart id.
4. **Untrusted input.** Anyone can mail a report to a report address.
   Strings are cut to their column and stripped of control characters,
   IPs parsed (else empty), enums mapped to a fixed set
   (`dkim`/`spf` → `pass|fail`, disposition → `none|quarantine|reject`,
   TLS result types → RFC 8460 names, unknown ones kebab-cased to
   `[a-z0-9-]` and 48 characters, ARF feedback types → RFC 5965 names or
   `other`), counts clamped to `INT UNSIGNED`. TLS failures a policy
   counts but does not detail are stored as result type `unspecified`.
5. **Notifications are per pass**, one per report type (the DMARC and
   TLS ones name up to five domains), so a flood of forged reports is
   one notification every 5 minutes. Severity rules are unchanged. All three link to
   `/jabali-admin/mail/deliverability`; the `/jabali-admin/mail/dmarc`,
   `/tlsrpt` and `/feedback` pages they linked to never existed.

The canonical `_dmarc` record is extended in the amendment below.

Verification: `internal/eventsources/mail_report_ingest_test.go` runs
the three reports captured from .60
(`testdata/stalwart_reports.ndjson`) through the ingest; each fix above
was neutralised in turn and its test failed.

## Amendment 2026-09-29 (2) — reports can reach the box: postmaster routing, rua, retention

The first amendment made the ingest work, but reports still could not
arrive for most domains:

- **Stalwart answered 550 to `postmaster@<domain>`** unless the domain
  had a postmaster mailbox or alias. RFC 5321 requires every mail
  domain to accept postmaster@, and the panel's TLS-RPT record already
  sent receivers there. A report addressed to a domain without one was
  refused.
- **The canonical `_dmarc` record had no `rua=`**, so no receiver sent
  DMARC aggregate reports at all.
- **Nothing pruned the report tables.** ADR-0103 set a 90-day retention,
  and the repos had `PruneOlderThan`, but nothing called it.

Decisions (the user picked "server admin" routing and "let the reports
land"):

1. **Postmaster routing in Stalwart's directory.** For an email-enabled
   domain with no postmaster mailbox, alias or group of its own,
   `queryRecipient` resolves `postmaster@<domain>` to the `postmaster`
   mailbox on the `is_panel_primary` domain, and `queryEmailAliases`
   lists those addresses on it. Both are needed: with only the first,
   Stalwart accepts the RCPT and then bounces "Mailbox not found" at
   local delivery, because the account does not own the address. The
   alias listing also lets that mailbox send as those addresses, which
   is within the admin's existing authority. A tenant's own postmaster
   (even a disabled one) wins. The queries read only tables
   `jabali-stalwart-ro` is already granted, and they stay
   byte-identical between install.sh's converger and
   apply-plan.json.tmpl (parity tests for both). A change to the
   directory queries takes effect after Stalwart reloads its settings;
   install.sh restarts Stalwart after the converger.
2. **The admin postmaster mailbox** is provisioned by panel-api at boot
   (`postmaster@<panel hostname>`, 1 GiB) when the panel domain has
   email and no postmaster yet. It is an ordinary listed mailbox (not
   `system`, which would hide it from the admin), with its password
   sealed for webmail SSO.
3. **Reports are also delivered to the postmaster mailbox.**
   `ReportSettings.inboundReportForwarding` stays on (Stalwart's
   default). Turning it off looked like the way to keep reports out of
   mailboxes, but on Stalwart 0.16 it silently drops ALL mail to
   postmaster@, human mail included (verified on .60). A test keeps
   either install path from setting it false. Report mail Stalwart files
   as spam is expunged from Junk after 30 days (`DataRetention`
   `expungeTrashAfter`).
4. **`rua=mailto:postmaster@<zone>`** is added to the canonical `_dmarc`
   record. The address is in the zone itself, so no RFC 7489 §7.1
   authorisation record is needed. Records rendered before are still
   canonical, so the reconciler upgrades them on its next pass; an
   operator-edited `_dmarc` is left alone. A zone name that is not a
   plain DNS name renders the record without `rua`.
5. **Retention.** Each ingest source prunes its table at most once a
   day, deleting rows older than 90 days. A report whose window ended
   before the cutoff (or an ARF report received before it) is not
   imported: the prune would delete it and a restart would import and
   announce it again.

Verification on .60: after the change, `postmaster@` of a second domain
was accepted and delivered to the admin postmaster mailbox; a DMARC
report to it was analysed and delivered; a tenant mailbox still signed
in and received mail; the new `_dmarc` was served by PowerDNS within one
reconcile pass.
