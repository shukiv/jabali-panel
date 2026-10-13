# SSL Manager

`/jabali-admin/ssl`. The cross-user view of every Let's Encrypt certificate the panel is responsible for.

## List

Columns: domain, owner, status (`issued` / `pending` / `failed`), issued at, expires at, last attempt result, retry-after.

## Per-row actions

- **Retry** — schedules an immediate issuance or renewal via the agent. Useful after fixing a DNS misconfiguration.
- **Force renewal** — bypasses certbot's "not yet due" check and renews now. Use sparingly to avoid Let's Encrypt rate limits.
- **Revoke** — removes the certificate from the host and from `/etc/letsencrypt/live/<domain>`. The next request without SSL toggled on returns to the HTTP-only vhost.
- **Reissue** (per-domain mail certificates only) — clears the backoff and re-requests the mail certificate. Mail-cert rows (shown as `mail.<domain>`) support only Reissue; the website-cert Retry / Force renewal / Revoke actions do not apply to them.

## Convergence model

A certificate row reflects desired state. The reconciler converges:

- `desired=issued, actual=missing` → call the agent's `ssl.issue` action; certbot performs HTTP-01 over the existing port-80 vhost; on success, install the cert and reload nginx.
- `desired=issued, actual=expiring (<14d)` → call `ssl.renew`.
- `desired=disabled, actual=installed` → call `ssl.revoke`.

Successful issuance typically completes within 60 seconds. Failures retry every three hours (with exponential backoff up to one day).

## Panel-hostname certificate

The panel itself uses a Let's Encrypt certificate for the configured panel hostname. The row appears at the top of the list, marked **Panel hostname**. The deploy hook reloads nginx, then the panel API, then Bulwark, in that order. A failure here falls back to a self-signed certificate to keep the UI reachable; the failure reason is visible on the row and on the Server Settings → General → Panel SSL card.

The per-domain certificate actions refuse the panel hostname: `jabali ssl enable|renew|retry` exit with an error, and the API answers 409 `panel_hostname_certificate`. Its certificate is the one on this row, managed under Server Settings → Panel SSL or with `jabali panel-cert status|issue`. Before GH #2126 those actions created a per-domain certificate for it that could only fail with `invalid webroot ""` and end as "manual retry required"; the panel now removes such a row on its own.

See also: [Panel Certificate](./panel-certificate.md), [SSL](../ssl.md).

## Per-domain mail certificates

Domains with per-domain mail TLS enabled appear in the list as `mail.<domain>` rows, owned by the domain's owner, next to their website certificate. Each row shows the certificate status and last error and offers a **Reissue** action that re-requests the Let's Encrypt certificate Stalwart presents for `mail.<domain>` (SMTP/IMAP/submission). The panel's own mail hostname (`mail.<panel-hostname>`) is covered by the [Panel Certificate](./panel-certificate.md) and is not duplicated here.

For the certbot HTTP-01 challenge to succeed, `mail.<domain>` must resolve to the server and port 80 must be reachable. The shared mail ACME webroot is `/var/www/jabali-acme` (provisioned by install.sh and on every `jabali update`).

## Rate limit visibility

Let's Encrypt's per-domain rate limits (5 duplicate certificates per week, 50 names per registered domain per week) are not visible to the panel. A `rateLimited` failure in the **Last attempt** column means the only remediation is to wait. The retry-after column shows the next scheduled attempt.

## CLI

```bash
jabali ssl list [--user <id>]
jabali ssl enable  <domain>
jabali ssl disable <domain>
jabali ssl renew   <domain>
```
