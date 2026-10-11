# SSL (User)

`/jabali-panel/ssl`. The TLS certificate state for the domains in your account.

## Per-domain view

For each domain: status (issued / pending / failed / off), issued at, expires at, last attempt result.

## Toggling SSL

1. Open the domain in [Domains](./domains.md) → Edit.
2. Toggle **SSL** to on (or off).
3. The reconciler issues or revokes the certificate within 60 seconds.

The certificate is provided by Let's Encrypt at no cost, using the HTTP-01 challenge over your existing port-80 vhost. The panel handles the renewal automatically — certificates are renewed approximately 30 days before expiry.

## Common failure causes

- **Domain does not resolve to the panel's IP** — Let's Encrypt cannot complete the HTTP-01 challenge. Update DNS at your registrar so the domain points to the IP shown on the SSL page. Wait for DNS propagation (typically minutes), then retry.
- **Firewall blocks port 80 from the public internet** — HTTP-01 requires inbound `:80`. This is rare on the panel side (the operator generally opens it) but possible if you are behind a corporate proxy or specific cloud-firewall rule.
- **Rate limit** — Let's Encrypt enforces 5 duplicate certificates per week and 50 names per registered domain per week. If you exceed it, the only remediation is to wait the window.

## Domains behind Cloudflare

If a domain's DNS is at Cloudflare and proxied (orange cloud), Let's Encrypt reaches Cloudflare, not this server, so the HTTP-01 check cannot pass. The panel can check the domain through DNS instead, using a Cloudflare API token.

1. In Cloudflare, create an API token (**My Profile → API Tokens**) with the permissions **Zone · Zone · Read** and **Zone · DNS · Edit**, for the zones of your domains only.
2. On the SSL page, paste it into **Cloudflare DNS check** and click **Verify & save**.

Before saving, the panel checks that Cloudflare accepts the token. In each zone holding your domains (up to 10), it adds and deletes a test record, `_jabali-dns01-check`. If any check fails, nothing is saved and the reason is shown. The token is stored encrypted and never shown again.

Your token is used only for your own domains. Without one, the server's token is used if the administrator has set one. **Remove token** deletes it.

## Retry

Click **Retry** on a failed row to force an immediate fresh attempt without waiting for the next scheduled retry (the reconciler retries failed issuances every three hours).

## Force renewal

Available only when a certificate is currently issued and not in the imminent-expiry window. Forces certbot to renew immediately. Use sparingly to avoid the rate limit.

## What about certificates for hostnames you do not control

You cannot issue a certificate for a hostname that does not point to the panel, except a proxied Cloudflare domain (see [Domains behind Cloudflare](#domains-behind-cloudflare)). There is no wildcard certificate for arbitrary subdomains: you cannot request one from the panel.

## What about EV or wildcard certificates

Both are out of scope. Let's Encrypt does not issue Extended Validation certificates, and you cannot request a wildcard certificate here. If you need EV or a wildcard, purchase it externally and ask the operator to install it manually.
