# Panel Certificate

The Let's Encrypt certificate used by the panel itself for the configured panel hostname. Managed through Server Settings → General → **Panel SSL** and, for cross-row visibility, through [SSL Manager](./ssl-manager.md).

## Why it is special

The panel hostname certificate differs from a tenant domain's certificate in three ways:

1. **One per host** — the `panel_certificate` table is a singleton; the hostname change replaces the existing row.
2. **Self-signed fallback** — on the very first boot, or after an issuance failure, the panel serves a self-signed certificate so the operator can reach the UI to diagnose. With Let's Encrypt on, **Issue / retry** on the Panel SSL card triggers a fresh attempt. With it off, the button is **Regenerate self-signed**; see [Self-signed mode](#self-signed-mode-lets-encrypt-off).
3. **Three-service deploy hook** — after a successful issuance or renewal, the agent reloads, in order: nginx → panel API → Bulwark. This sequencing avoids a window in which one component is using the new certificate and another is still using the old, which would surface as TLS handshake failures on the SPA.

## Issuance path

Identical to a tenant domain: HTTP-01 over the existing port-80 vhost for the panel hostname. The agent action `ssl.panel.issue` performs the call. On success, the cert lands in `/etc/letsencrypt/live/<panel-hostname>/` and `panel_certificate` is updated with the issued / expires timestamps.

## Renewal

certbot's own systemd timer (`certbot.timer`) handles renewal. The deploy hook runs the three-service reload automatically.

## Failure handling

If issuance fails, the panel:

1. Falls back to a self-signed certificate to keep the UI reachable.
2. Records the failure on the Panel SSL card with the certbot error string.
3. Schedules a retry every three hours.

Common failure causes are the same as tenant-domain failures: the panel hostname does not resolve to one of the server's IPs, the firewall blocks inbound `:80`, or the Let's Encrypt rate limit has been hit. See [SSL Manager](./ssl-manager.md) for the diagnostic mapping.

## Self-signed mode (Let's Encrypt off)

With **Use Let's Encrypt for the panel** off, the panel serves its own self-signed certificate for the panel hostname. It keeps that certificate matched to the hostname: after a hostname change, or when the certificate expires, it writes a new one.

On the hostname row, the button is **Regenerate self-signed** instead of **Issue / retry**. It doesn't check public DNS and doesn't ask Let's Encrypt, so it works while the hostname is behind a proxy such as Cloudflare. The panel writes a new certificate only when the current one no longer fits the hostname or has expired; otherwise it says what it already serves, and until when. After writing one, the panel restarts to serve it, and your browser asks you to accept the new certificate.

A Let's Encrypt certificate that is still valid when you turn Let's Encrypt off is kept until it expires. The panel then replaces it with a self-signed one.

Behind a Cloudflare proxy, set Cloudflare's SSL/TLS mode to **Full**: Full (strict) doesn't accept a self-signed certificate.

The mail certificate's button stays **Issue / retry**, a Let's Encrypt request. `mail.<hostname>` can't be proxied, so it can have a Let's Encrypt certificate while the panel hostname is proxied.

## Operator notes

- Changing the panel hostname triggers a fresh issuance against the new hostname. The old certificate is retained for one renewal cycle to ease rollback.
- The certificate path is consumed by nginx, the panel API (which serves the SPA over `:443`), and Bulwark. Each service reads the certificate on reload; in-flight connections complete on the prior certificate.
- The separate panel mail certificate follows the mail hostname. See [Mail Hostname](./mail-hostname.md).
