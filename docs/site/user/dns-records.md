# DNS Records

`/jabali-panel/domains/<id>/dns`. The records inside a single DNS zone you own.

## Record types you can add

- **A** — IPv4 address.
- **AAAA** — IPv6 address.
- **CNAME** — alias to another DNS name.
- **MX** — mail exchanger; preference plus the target host.
- **TXT** — free-text record (SPF, DMARC, MTA-STS policy descriptor, domain verification tokens for third-party services).
- **SRV** — service location record (autodiscover, SIP, XMPP).
- **CAA** — Certification Authority Authorization; restricts which CAs may issue certs for the domain.
- **NS** — delegation to other nameservers (only for subdomains within the zone).

`SOA` is generated and managed by the panel; you do not edit it directly.

## Adding a record

Click **Add record** → pick a type → fill in fields. Validation enforces type-correct content (an A record requires a valid IPv4, a CAA record requires a recognised tag).

On save, the agent:

1. Inserts the record into the PowerDNS MariaDB backend.
2. Issues `pdns_control purge <zone>$` so subsequent lookups bypass any stale cache (PowerDNS caches answers for 60 seconds by default).
3. Bumps the zone serial automatically.

Convergence is sub-second.

## Editing and deleting

Same actions, same cache purge. Records that the panel manages automatically (the default A and AAAA for the apex, the DKIM TXT records when mail is enabled) are marked **System-managed** and cannot be edited from this page — they re-render from the panel's source of truth on every reconciler tick.

## DNSSEC

If DNSSEC is enabled for the zone (see [DNSSEC](./dnssec.md)), records are signed on the fly. Adding or removing a record does not require a key roll; the zone signer re-signs the affected RRset.

## Common record patterns

- **Email**: MX 10 mail.example.com (if mail is enabled, the panel adds this automatically), TXT `"v=spf1 include:_spf.<panel-hostname> -all"`, TXT `_dmarc "v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@example.com"` (the panel adds this one too).
- **Domain verification**: TXT records of the form `google-site-verification=...`, `apple-domain-verification=...`. Paste exactly as the third party requests.
- **Static IP**: A `@ -> <ip>`, AAAA `@ -> <ipv6>`, CNAME `www -> @`.

## Why no zone-file import

The current page is record-oriented. Zone-file import (BIND-style) is available via the migration pipelines ([cPanel](../admin/cpanel-migration.md), [DirectAdmin](../admin/directadmin-migration.md), [Hestia](../admin/hestiacp-migration.md)) but not on this page. Paste records one by one for ad-hoc additions.
