# DNS Zones (Admin)

`/jabali-admin/dns`. The cross-user view of every PowerDNS zone served by the panel.

## List

One list of zones (GH #1918). Columns: domain name, owner, zone status, record count, TTL, DNSSEC (Signed / Unsigned), registration expiry.

- **Domain name** — opens the zone's records: `A`, `AAAA`, `CNAME`, `MX`, `TXT`, `SRV`, `CAA`, `PTR`, `NS`, `SOA`. Inline edit / delete, paginated, searchable by name.
- **⋯ menu** — per-zone actions:
  - **Enable DNSSEC** on an unsigned zone, or **Disable DNSSEC** on a signed one. Disable asks for confirmation: remove the DS record at the registrar first.
  - **View DS & keys** on a signed zone — the active keys and the DS record to publish at the parent registrar. See [Per-Domain DNSSEC](../platform/dnssec.md).
  - **Delete zone** (or **Delete domain** for a DNS-only domain). A signed zone must be unsigned first.
- A domain whose DNS is hosted elsewhere shows **Enable DNS** instead of a menu.

## Adding a zone

A zone is created automatically when a domain is added under [Domains](./domains.md). To host an additional zone that is not a panel domain (for example a partner zone you DNS for), use **Add zone** and supply the zone name plus the owning user.

## Deleting a zone

Forbidden if the zone is referenced by an active domain. Delete the domain first, or use **Detach zone** to remove the link without deleting the zone records.

## Cache invalidation

PowerDNS caches answers for the duration of `cache-ttl` (default 60 seconds). After any zone or record mutation, the agent issues `pdns_control purge <zone>$` so callers do not see stale answers. This rule is load-bearing: omitting the purge causes fresh records to appear stuck for up to one minute.

## Recursor forwarders

If the panel host should forward specific zones to an internal resolver (split-horizon DNS), edit `/etc/powerdns/recursor.forwards` and run:

```bash
jabali pdns backfill
```

The reconciler also re-applies forwarders on each tick to converge them with the panel database.

## CLI

```bash
jabali pdns dnssec enable  <domain>
jabali pdns dnssec status  <domain>
jabali pdns dnssec ds      <domain>
jabali pdns dnssec disable <domain>
jabali pdns backfill
```
