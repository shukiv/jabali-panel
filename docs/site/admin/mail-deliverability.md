# Mail Deliverability

`/jabali-admin/mail/deliverability`. Per-domain view of the DNS records and policies that govern outbound mail reputation.

## Columns

- **Domain** — every domain on the panel that has mail enabled.
- **DKIM** — present / absent at the panel; published / missing at the public resolver. Selector visible on hover.
- **SPF** — record exists; soft-fail (`~all`) or hard-fail (`-all`) verdict; warnings if the record references too many includes (RFC 7208 §4.6.4 limit of 10 DNS lookups).
- **DMARC** — record exists; policy (`none`, `quarantine`, `reject`); reporting addresses (`rua` / `ruf`) parsed.
- **MTA-STS** — TXT record, policy file, MX-host alignment. See [ADR-0109](../platform/stack.md#adr-0109-per-domain-mta-sts).
- **Reverse DNS** — PTR for the server's primary mail IP resolves to the panel hostname (gold standard for SMTP acceptance at major providers).

## Per-row actions

- **Rotate DKIM** — generate a new DKIM key, publish the new DNS record, retain the old key on a configured grace period (default 7 days) so already-signed in-flight mail still validates.
- **Re-publish records** — re-write the panel's recommended SPF, DMARC, and MTA-STS records into the zone if the operator manually edited them.
- **View inbound reports** — Stalwart ingests TLS-RPT, MTA-STS-RPT, and DMARC aggregate reports (M47 Wave 2). The row drills into a per-domain reports panel with sender reputations, failure reasons, and trend lines.

## Color coding

- Green: all four (DKIM, SPF, DMARC, MTA-STS) present and aligned.
- Amber: DKIM and SPF present, DMARC missing or `p=none`.
- Red: DKIM missing or SPF missing — mail from this domain is likely to be rejected or quarantined by major receivers.

## Why this page exists

A new operator typically has SPF and DKIM set automatically when the domain is added, but DMARC and MTA-STS are opt-in. This page surfaces what is missing in one glance instead of forcing a per-domain DNS audit.

## CLI

```bash
jabali domain email-dkim-rotate <domain>
```
