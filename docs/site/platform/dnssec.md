# Platform — DNSSEC

M15. Per-domain, opt-in. ADR-0057.

## Model

Each hosted zone can be signed independently. Toggle per-domain from the domain's **⋯** menu on the DNS page (`/jabali-admin/dns` for the admin, `/jabali-panel/dns` for the owner): **Enable DNSSEC**, **Disable DNSSEC** (confirms first), **View DS & keys**. No global on/off.

When enabled, the agent runs:

```
pdnsutil secure-zone <domain>
pdnsutil rectify-zone <domain>
```

…which generates a KSK (Key-Signing Key) and a ZSK (Zone-Signing Key) and signs every RRSet.

## Key persistence

PDNS stores keys in its MariaDB backend (`domainmetadata`, `cryptokeys`). The Jabali panel **also** caches key metadata + the DS record in its own `dnssec_keys` table (admin- and user-visible) so the UI can show the DS record without shelling to `pdnsutil` per page load.

## Operator workflow

1. Toggle DNSSEC on.
2. Wait for "secured" badge in the UI (≤2 s typical).
3. Copy the **DS record** displayed in the DNSSEC card.
4. **Publish the DS record at the parent registrar** (where your domain is registered — Namecheap, Gandi, Cloudflare Registrar, etc.).
5. Wait for the parent registrar to push the DS into the parent zone (usually minutes; up to TTL of the parent zone).
6. Verify with `dig +dnssec @8.8.8.8 example.com SOA` → look for `ad` (Authenticated Data) flag.

Until step 5 is done, your zone is signed but not part of the chain of trust. Resolvers that don't validate will keep working; resolvers that validate will see the zone as "signed but unsigned by parent" — generally treated as if unsigned. Safe.

## Algorithm choice

Default: KSK = ECDSAP256SHA256 (algorithm 13), ZSK = ECDSAP256SHA256. Modern, small, fast. PowerDNS picks this when invoked without `--algorithm`.

(If you have a registrar that doesn't support algorithm 13, override via `pdnsutil add-zone-key <domain> ksk rsasha256 2048` and adjust the displayed DS accordingly. The UI doesn't currently expose this; CLI-only.)

## Rotation

Not automatic. Manual rotation:

```bash
pdnsutil activate-zone-key <domain> <new-key-id>
pdnsutil deactivate-zone-key <domain> <old-key-id>
# wait for old DS TTL to expire
pdnsutil remove-zone-key <domain> <old-key-id>
```

KSK rotation requires re-publishing the DS at the parent registrar.

## What is *not* covered

- **NSEC vs NSEC3**: Jabali uses NSEC by default. To switch a zone to NSEC3 (zone-walk resistance):

  ```bash
  pdnsutil set-nsec3 <domain> '1 0 1 ab'
  pdnsutil rectify-zone <domain>
  ```

- **CDS / CDNSKEY automation** (RFC 7344) — parent registrar must support it. Not currently published by Jabali; on the roadmap.
- **Algorithm rollover** automation.

## Live verification

End-to-end DNSSEC signing was live-verified on 192.168.100.150 (2026-04-25) — zone signed, DS published at the test registrar, `dig +dnssec` returned `ad`.
