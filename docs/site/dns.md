# DNS

Jabali ships **two** PowerDNS processes:

- **pdns-server** (authoritative) — answers for hosted zones, MariaDB backend, listens on the server's public IPs `:53`.
- **pdns-recursor** — handles local `.` recursion for the server itself, listens on loopback `127.0.0.1:53`.

Split-port setup (ADR-0047): the recursor binds the loopback so local processes (the panel, certbot, Stalwart, etc.) can resolve external names without going through an upstream resolver, while the authoritative binds public IPs so the world can query hosted zones.

## Zones

Each hosted domain gets an authoritative zone in PowerDNS. Default records:

- `@` A → server primary IP (or domain's listen IP if set)
- `@` AAAA → server primary IPv6 (if configured)
- `www` CNAME → `@`
- `@` MX 10 `mail.<panel-hostname>.` (if domain has mail enabled)
- `mail` A → server primary IP (if mail enabled)
- `_dmarc` TXT (if mail enabled)
- DKIM TXT (if mail enabled)
- SPF TXT (if mail enabled)
- MTA-STS TXT (if mail enabled, ADR-0109)

Add / edit / delete custom records under DNS → `<domain>` → Records.

## DNSSEC

Per-domain toggle. When enabled:

1. Agent calls `pdnsutil secure-zone <domain>` → KSK + ZSK generated and rectified.
2. Panel persists key metadata in the DB so it survives PDNS rebuilds.
3. The DNSSEC page displays the **DS record** to publish at the parent registrar.

Until the DS is published at the registrar, the zone is signed but not part of the chain of trust — that's fine for testing; not fine for production.

CLI:

```bash
jabali pdns dnssec enable <domain>
jabali pdns dnssec status <domain>
jabali pdns dnssec ds <domain>      # prints DS for parent registrar
jabali pdns dnssec disable <domain>
```

See [platform/dnssec.md](./platform/dnssec.md) for the architecture details.

## Mail provider (per domain)

Each domain has a **mail provider** (Domains → Edit → Email → *Mail provider*,
or chosen at domain-add). It is the single source of truth for the domain's
mail DNS and certificate posture — `email_enabled` and the cert's mail SANs
are derived from it:

| Provider | MX | SPF (apex) | Other | Jabali mail cert SANs |
|----------|----|-----------|-------|------------------------|
| **Jabali mail** (default) | `mail.<domain>` | `v=spf1 mx …` | DKIM, autoconfig/autodiscover, IMAP/submission SRVs (the M6 set) | yes (mail/autoconfig/autodiscover/mta-sts) |
| **No mail** | — | — | none | no |
| **Microsoft 365** | `<domain-dashed>.mail.protection.outlook.com` | `include:spf.protection.outlook.com -all` | `autodiscover → autodiscover.outlook.com`; optional `selector1/2._domainkey` CNAMEs (set the tenant's `onmicrosoft` name) | no |
| **Google Workspace** | `smtp.google.com` | `include:_spf.google.com ~all` | optional `google._domainkey` TXT (paste from Google Admin) | no |
| **Custom template** | defined by the template | defined by the template | any records the template holds (A/AAAA/CNAME/MX/TXT/NS/SRV/CAA), seeded once at create | no |

Switching providers is reconciled like any other DNS change: the panel
publishes the new set, prunes the previous one (scoped by `managed_by` —
`mail-apex` for the apex MX/SPF/DMARC, `mail-provider-<p>` for the rest, `m6`
for the Jabali set), and re-issues the certificate without the mail SANs for
external/none. There is always exactly one `v=spf1` TXT at the apex.
Operator-authored apex mail records are replaced when a provider is selected
(pick a preset, or pick **No mail** and hand-manage the records yourself).
DKIM for Microsoft 365 / Google is published only when you supply the token;
MX / SPF / autodiscover are automatic.

## Custom DNS templates

A **custom DNS template** is a named, admin-defined set of DNS records that is
seeded into a new domain's zone at create time — the "come up with my standard
records already in place" shortcut for another mail platform, a verification
record set, or any third-party service records. It is the general form of the
built-in mail presets above: instead of Microsoft 365 or Google Workspace, an
admin defines the exact records once and tenants pick that template when adding
a domain.

**Defining templates (admin).** Server Settings → DNS → *DNS templates*. Each
template has a name, an optional description, and an ordered set of records; each
record is `Type` (A, AAAA, CNAME, MX, TXT, NS, SRV, CAA), `Name`, `Value`, `TTL`,
and `Priority` (used by MX and SRV). A template may hold up to 100 records. Every
record is validated on save with the **same** `ValidateDNSRecord` rules a tenant's
own DNS records use, so a template can never carry a record the record API would
reject; when the server rejects one, the exact reason (`record 3: …`) is shown
inline.

Use the `{domain}` token in a record's name or value and it is replaced with the
zone's own name when the template is seeded (e.g. an MX of `mail.{domain}`, or a
CNAME whose value is `{domain}`).

**Selecting a template (tenant or admin).** When adding a **Web Domain** (with
"Add Mail Domain" unchecked) or a **DNS Zone**, the admin-defined templates appear
under a *Custom templates* group in the same Template / DNS Template select as the
mail presets. Choosing one sets the domain's mail posture to **external** (like
Microsoft 365 / Google Workspace — the template, not Jabali, owns the apex mail
records) and records `mail_template_id` on the domain. A template requires the
panel to host the zone, so the template options are disabled while "Add DNS Zone"
is unchecked. Over the API this is the `dns_template_id` field on `POST /domains`;
on the CLI it is `jabali domain create --dns-template <id>` (mutually exclusive
with `--mail`, requires `--manage-dns`).

**Seeding is once, at bootstrap.** The reconciler copies the template's records
into the fresh zone the first time it is built, substituting `{domain}`. The
seeded records are **tenant-owned** (`managed_by` unset) — the tenant edits or
deletes them afterwards like any hand-added record, and the panel never
re-asserts them. Deleting or editing the template later does not touch domains
already created from it. Templates are **global**: every tenant sees every
admin-defined template (a template is a convenience — a tenant can already type
any of those records into their own zone by hand — so there is no per-package
entitlement on it).

## Cache invalidation

PowerDNS auth caches answers for `cache-ttl` (default 60 s). After any panel-side mutation (record add/update/delete, zone create/delete) the agent runs `pdns_control purge <zone>$` so callers don't see stale data for up to 60 s. This is the "PDNS cache after backend write" rule — without it, fresh records appear stuck.

## Recursor forwarders

The recursor uses `/etc/powerdns/recursor.forwards`, populated by `jabali pdns backfill` from the panel DB. Use this if you want to forward specific zones to an internal resolver (e.g. for split-horizon DNS).

## DNS-related agent commands

- `dns.zone.upsert` — create or update a zone.
- `dns.zone.delete` — drop a zone.
- `dns.dnssec.enable` / `dns.dnssec.disable` — per-domain DNSSEC.

All zone mutations go through the agent so the cache purge is automatic and atomic.
