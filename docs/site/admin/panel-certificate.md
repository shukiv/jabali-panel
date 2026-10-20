# Panel Certificate

The Let's Encrypt certificate used by the panel itself for the configured panel hostname, and its mail certificate for `mail.<hostname>`. Managed on the **Panel SSL** tab of [SSL Manager](./ssl-manager.md) (`/jabali-admin/ssl?tab=panel`). Server Settings → General → **Panel SSL** shows each certificate's status and links there.

## The Panel SSL tab

SSL Manager → **Panel SSL** holds the **Use Let's Encrypt for the panel** and **Staging** switches, which govern both certificates, and one card per certificate: the panel hostname and the mail name. Each card shows:

- **Status** — with Let's Encrypt on, the certificate's state in the panel (self-signed, issuing, issued, pending retry, failed) and whether its name resolves to this server. With it off, the certificate the panel serves: **Issued (self-signed)**, or **Issued by** the authority of a certificate kept until it expires.
- What the certificate file on disk holds, read each time the tab loads: **Type** (self-signed, Let's Encrypt, Let's Encrypt staging, or another authority), **Issuer**, **Names**, **Valid from** and **Valid until** (a warning from 30 days before expiry), and the **SHA-256 fingerprint**. Compare the fingerprint with the one your browser shows before you accept a self-signed certificate.
- Its action: **Issue / retry**, or **Regenerate self-signed** with Let's Encrypt off.
- With Let's Encrypt on, the last error and the next retry, when an issue failed.

When the file is missing or holds no certificate, the card says so instead.

## Why it is special

The panel hostname certificate differs from a tenant domain's certificate in three ways:

1. **One per host** — the `panel_certificate` table is a singleton; the hostname change replaces the existing row.
2. **Self-signed fallback** — on the very first boot, or after an issuance failure, the panel serves a self-signed certificate so the operator can reach the UI to diagnose. With Let's Encrypt on, **Issue / retry** on the Panel SSL tab triggers a fresh attempt. With it off, the button is **Regenerate self-signed**; see [Self-signed mode](#self-signed-mode-lets-encrypt-off).
3. **Three-service deploy hook** — after a successful issuance or renewal, the agent reloads, in order: nginx → panel API → Bulwark. This sequencing avoids a window in which one component is using the new certificate and another is still using the old, which would surface as TLS handshake failures on the SPA.

## Issuance path

Identical to a tenant domain: HTTP-01 over the existing port-80 vhost for the panel hostname. The agent action `ssl.panel.issue` performs the call. On success, the cert lands in `/etc/letsencrypt/live/<panel-hostname>/` and `panel_certificate` is updated with the issued / expires timestamps.

## Renewal

certbot's own systemd timer (`certbot.timer`) handles renewal. The deploy hook runs the three-service reload automatically.

## Failure handling

If issuance fails, the panel:

1. Falls back to a self-signed certificate to keep the UI reachable.
2. Records the failure on the Panel SSL tab with the certbot error string.
3. Schedules a retry every three hours.

Common failure causes are the same as tenant-domain failures: the panel hostname does not resolve to one of the server's IPs, the firewall blocks inbound `:80`, or the Let's Encrypt rate limit has been hit. See [SSL Manager](./ssl-manager.md) for the diagnostic mapping.

## Self-signed mode (Let's Encrypt off)

With **Use Let's Encrypt for the panel** off, the panel serves its own self-signed certificate for the panel hostname. It keeps that certificate matched to the hostname: after a hostname change, or when the certificate expires, it writes a new one.

On the hostname row, the button is **Regenerate self-signed** instead of **Issue / retry**. It doesn't check public DNS and doesn't ask Let's Encrypt, so it works while the hostname is behind a proxy such as Cloudflare. The panel writes a new certificate only when the current one no longer fits the hostname or has expired; otherwise it says what it already serves, and until when. After writing one, the panel restarts to serve it, and your browser asks you to accept the new certificate.

A Let's Encrypt certificate that is still valid when you turn Let's Encrypt off is kept until it expires. The panel then replaces it with a self-signed one.

The Panel SSL tab then shows no Let's Encrypt state: no routability, no last error and no next retry. If the panel hostname doesn't resolve publicly to this server, the **Use Let's Encrypt for the panel** switch can't be turned on, and a line under it gives the reason. Before GH #2166 the tab showed "Let's Encrypt is unavailable for the panel" and "Not routable" in this mode, although the self-signed certificate was in use.

Behind a Cloudflare proxy, set Cloudflare's SSL/TLS mode to **Full**: Full (strict) doesn't accept a self-signed certificate.

### The mail certificate

The mail certificate follows the same switch. With Let's Encrypt off, and email enabled on the panel hostname, the panel serves a self-signed certificate for the mail name (`mail.<hostname>`, or the custom mail hostname, plus `mail.<hostname>`). It has its own key, separate from the panel hostname's. Webmail serves it, and so does the mail server on IMAPS and submission. Without it, a panel that never had a Let's Encrypt mail certificate would leave the mail server on its built-in certificate named `localhost`, which mail apps reject.

The mail card's button is **Regenerate self-signed** too, with the same rules: it writes a new certificate only when the current one no longer fits the mail names or has expired, and a Let's Encrypt certificate that is still valid is kept until it expires. After writing one, the mail server restarts to load it (open IMAP connections reconnect), and mail apps ask you to accept the new certificate. Compare its SHA-256 fingerprint on the card with the one your mail app shows.

With email off on the panel hostname, nothing serves the mail name, and its certificate is left alone.

A server without the mail module (Server Settings → Modules) runs no mail server, so it has no mail certificate. The Panel SSL tab shows only the panel hostname's card, and the panel neither requests nor generates a mail certificate. The API answers 409 `module_disabled` to `issue?kind=mail`. Before GH #2166, a self-signed mail certificate was pushed on such a server too, which started a mail server that isn't installed. Each failed start raised a "Stalwart is down" alert. To clear that state left over from before the fix, run `systemctl reset-failed jabali-stalwart`.

## Operator notes

- Changing the panel hostname triggers a fresh issuance against the new hostname. The old certificate is retained for one renewal cycle to ease rollback.
- The certificate path is consumed by nginx, the panel API (which serves the SPA over `:443`), and Bulwark. Each service reads the certificate on reload; in-flight connections complete on the prior certificate.
- The separate panel mail certificate follows the mail hostname. See [Mail Hostname](./mail-hostname.md).
