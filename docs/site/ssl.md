# SSL / TLS

Let's Encrypt-only. HTTP-01 over the domain's existing port-80 nginx vhost, or DNS-01 for a domain behind a CDN (see [Domains behind Cloudflare](#domains-behind-cloudflare-dns-01)).

## Per-domain certificates

Toggle SSL on a domain (admin: Domains → Edit → SSL; user: Domains → Edit → SSL). The reconciler:

1. Verifies the domain resolves to one of the server's IPs (or to the configured listen IP for that domain).
2. Calls certbot via the agent with `--webroot` against the live nginx vhost.
3. Stores the cert under `/etc/letsencrypt/live/<domain>/`.
4. Reloads nginx.

Issuance latency: ≤60 s typical. Failures retry every 3 hours (reconciler interval).

## Domains behind Cloudflare (DNS-01)

When a domain's address is a CDN rather than this server (Cloudflare's orange cloud), the CDN answers the HTTP-01 check itself and the certificate cannot be issued that way. The panel then uses DNS-01: it writes the `_acme-challenge` TXT record where the zone is really hosted.

- **This server's DNS**, when it is authoritative for the zone.
- **Cloudflare**, through an API token. For each name, the panel tries, in order:
  1. the domain owner's own token (user: **SSL → Cloudflare DNS check**);
  2. the server's token (admin: **Server Settings → General → Cloudflare DNS-01**).

A user's token is used only for that user's domains, on that user's certificates. If one user's domain sits under another user's (subdomain delegation), neither user's token is used for the other's names.

With no usable token, the certificate stays self-signed with the reason shown, and the panel re-checks daily. No Let's Encrypt attempt is spent while it waits.

**Verify & Save** (user token). Nothing is stored unless:

1. Cloudflare says the token is valid and active;
2. it can see an active zone that holds one of the user's domains;
3. in each such zone (up to 10) it can add and delete a TXT record `_jabali-dns01-check.<zone>`.

The token needs `Zone:Read` and `Zone:DNS:Edit` on those zones. Verify & Save proves the token, not the delegation: a zone whose nameservers are not at Cloudflare yet still passes, and issuance then reports it as delegated elsewhere. Both tokens are stored encrypted with the panel's sso key and never shown again.

**Renewal.** certbot stores the challenge hooks with each certificate, and renewals (the timer, **Renew**) reuse them. A certificate issued before tenant tokens existed therefore keeps renewing with the server's token only, even after its owner saves a token. Keep the server token set while such certificates exist.

## Renewal

certbot's own systemd timer renews. The agent's deploy-hook reloads nginx automatically. No human action.

## Panel-hostname certificate

The panel itself runs on a Let's Encrypt cert for the **Panel Hostname** (Server Settings → General). Cert lives in the singleton `panel_certificate` table; the agent's `ssl.panel.issue` action handles issuance via the same HTTP-01 path (with a `/.well-known/acme-challenge/` location on the panel-hostname nginx vhost). Deploy hook reloads **nginx → panel-api → Bulwark** in that order.

If issuance fails, the panel falls back to a self-signed cert so the UI stays reachable; SSL Manager → **Panel SSL** shows the failure reason. Hit **Issue / retry** or wait for the next reconciler tick.

## Custom certificates

Besides Let's Encrypt, a domain can use an **uploaded custom certificate**
(cert + key + chain). The **Domain Certificate Lifecycle** op (JAB-345) manages
install / replace / removal with a forward-recovery finalize, and de-tracks the
Let's Encrypt lineage first so a custom cert doesn't clobber it (or vice-versa on
removal).

## Cross-user view (admin) — Certificate console

`/jabali-admin/ssl` is the **Certificate console** (redesigned, GH #1355): every
cert known to the panel with issuer, expiry (Issued + Last-check folded into the
**Expires** column, GH #1271) and last issuance result. Per row:

- **View Certificate** — inspect the certificate's subject, SANs, issuer, and
  validity dates (GH #1355).
- **Retry** — force a fresh issuance attempt.

The panel's own certificates are on the **Panel SSL** tab; see [Panel Certificate](./admin/panel-certificate.md#the-panel-ssl-tab).

Renew and Retry are admin actions. A tenant's SSL Manager and a domain's SSL
tab show the certificate and its state but not those buttons: renewal and
issuance retries run on their own, and the tab says so.

## Common failure modes

| Symptom | Cause | Fix |
|---|---|---|
| `Failed authorization procedure` | Domain does not resolve to this server | Update DNS at the registrar; wait propagation; retry. |
| `urn:ietf:params:acme:error:rateLimited` | Hit LE rate limit | Wait the window (typically 1 hour). |
| Cert issued but browser shows old cert | nginx not reloaded | `systemctl reload nginx` or trigger reconciler. |
| Panel cert never issues | Bulwark serving on `:80` | Reconciler should fix; check `journalctl -u jabali-agent -f`. |
| `invalid webroot ""` on the panel hostname, capped at 20 attempts | A per-domain certificate was requested for the panel hostname (GH #2126) | Nothing: the panel removes that row on its own. The hostname's certificate is under SSL Manager → Panel SSL (`jabali panel-cert status`). |

## CLI

```bash
jabali ssl list [--user <id>]
jabali ssl enable <domain>          # toggle on; reconciler does the rest
jabali ssl disable <domain>
jabali ssl renew <domain>           # synchronous renew via agent
```
