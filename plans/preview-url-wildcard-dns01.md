# Preview URLs + DNS-01 wildcard certificates

User decisions (2026-08-01):
- Temp URL shape: `<slug>.preview.<hostname>` (slug = domain name, dots→dashes)
- Per-domain **opt-in** toggle (default OFF)
- TLS: **one wildcard cert** `*.preview.<hostname>` via DNS-01
- DNS-01/wildcard plumbing must be **generic** — regular tenant domains can
  also request `*.<domain>` certs (their zone must be served by this panel's pdns)

## Existing machinery this builds on

- `shared_certificates` (JAB-170): wildcard/multi-SAN cert stored once, attached
  to many domains via `domains.ssl_mode='shared'` + `shared_certificate_id`.
  Today upload-only. Vhost render for attached domains already ships.
- pdns self-zone for `<hostname>` seeded at install (`bootstrap_pdns_self_zone`),
  panel-primary domain row + reconciler already write records into it.
- Agent certbot runner (`panel-agent/internal/certbot/runner.go`) — HTTP-01
  webroot only today; SAN-expand + lineage pinning already handled.
- `checkSharedCertExpiry` reconciler pass — warns on expiry today.
- Scars to honour: pdns cache purge after backend writes; GORM bool default:1
  trap (opt-in default-false is zero-value-safe); delete-cascade parity via the
  shared agent `domain.delete`; wire-contract verification for new UI fields;
  per-tick idempotent reconciler steps.

## PR 1 — DNS-01 plumbing + ACME shared certs (generic wildcard)

1. **ACME DNS hook** (panel binary subcommand, invoked by certbot as root):
   `jabali-panel dns acme-hook auth|cleanup` reading `CERTBOT_DOMAIN` /
   `CERTBOT_VALIDATION` env. Writes/removes TXT `_acme-challenge.<domain>` in
   the pdns records table, purges pdns cache, short settle wait. Refuses
   domains whose zone is not served locally.
2. **Agent RPC** `ssl.issue_dns01` (or `challenge:"dns-01"` param on the
   existing issue command): runs
   `certbot certonly --manual --preferred-challenges dns
    --manual-auth-hook <hook> --manual-cleanup-hook <hook>` with the SAN list
   (supports `*.name`). Lineage pinned like HTTP-01. Renewal conf keeps the
   hooks, so `certbot renew` keeps working.
3. **SharedCertificate gains `source`** (`uploaded` | `acme`) + issue flow:
   admin/tenant "Request wildcard cert (*.domain)" → panel validates the zone
   is local → agent DNS-01 issue → row stores paths/SANs/expiry.
   `checkSharedCertExpiry`: for `source=acme`, trigger renew via agent instead
   of only warning.
4. Attach/detach + vhost render: unchanged (JAB-170 path).

## PR 2 — Preview URLs

1. **Server-side bootstrap (lazy, on first enable)**: wildcard `A`/`AAAA`
   `*.preview.<hostname>` in the self-zone + one server-wide shared cert row
   (`UserID NULL`, `source=acme`, SAN `*.preview.<hostname>`) issued via PR 1
   machinery.
2. **Per-domain**: `domains.temp_url_enabled` (tinyint, default 0, opt-in).
   Reconciler/domain.create payload gains `preview_host` + preview cert paths.
3. **Agent**: renders an extra server block per enabled domain —
   `server_name <slug>.preview.<hostname>`, same docroot + PHP pool socket,
   TLS from the shared preview cert, HTTP→HTTPS redirect. Content-hash gated.
   Teardown in shared `domain.delete` + on toggle-off.
4. **UI**: toggle in domain create drawer + settings drawer; copy-chip with the
   preview URL in the domain lists.
5. **Caveats (docs)**: WordPress canonical-redirects away from the preview
   host; preview only resolves publicly when the hostname zone is delegated to
   this server; cookie-scope note (tenant preview sites share the hostname's
   registrable domain — panel session cookie must stay host-only).

## Verification

- Unit: hook TXT write/remove + zone-locality refusal; certbot arg construction
  (SAN with wildcard, hooks present, no webroot); shared-cert source lifecycle;
  agent preview-block render golden.
- Box (testserver): issue `*.preview.<hostname>` end-to-end (LE staging first),
  enable preview on a real domain, curl the preview URL over HTTPS pre-cutover.
- `dig` + pdns check-zone after record writes (SRV-priority scar).
