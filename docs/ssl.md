# SSL/TLS Certificate Management

Jabali Panel uses Certbot and Let's Encrypt to manage SSL/TLS certificates for domains and the panel itself, with automatic renewal via systemd timers.

## Certificate Issuance

### Issue Domain Certificate
```bash
jabali ssl:issue <domain> [--force]
```

Issues a new certificate from Let's Encrypt using webroot validation. The certificate includes:
- Primary domain (e.g., `example.com`)
- `www` subdomain (e.g., `www.example.com`)
- Mail subdomain (e.g., `mail.example.com`) for Stalwart

Use `--force` to overwrite an existing certificate. Certificates are stored in `/etc/letsencrypt/live/{domain}/` with symlinks to actual certificate files.

### Panel Certificate
```bash
jabali ssl:panel [--json]
```

Displays the control panel's SSL certificate status, expiration date, and issuer.

### Issue Panel Certificate
```bash
jabali ssl:panel-issue [--json]
```

Issues a new certificate for the control panel itself (FrankenPHP running on port 8443). The panel certificate is separate from domain certificates and is critical for panel access.

## Certificate Renewal

### Renew Certificate
```bash
jabali ssl:renew <domain>
```

Manually renews an existing certificate before expiration. Certbot checks certificate age and skips renewal if not due.

### Check Certificate Status
```bash
jabali ssl:check [domain] [--issue-only] [--renew-only]
```

Validates all domain certificates or checks a specific domain:
- `--issue-only` — only check for missing certificates (exit with error if any domain lacks a cert)
- `--renew-only` — only check for expiring certificates (90+ days old)

With no options, performs all checks (missing and expiring certificates).

### List Certificates
```bash
jabali ssl:list [--json]
```

Shows all installed certificates with domain names, issue dates, and expiration dates.

### Certificate Details
```bash
jabali ssl:status <domain>
```

Displays full certificate information including:
- Common name (CN)
- Subject alternative names (SAN)
- Issue date
- Expiration date
- Certificate chain
- Issuer details

## Automatic Issuance & Renewal

### Scheduled SSL Check (`jabali:ssl-check`)
Runs every 3 hours via Laravel scheduler. Handles all SSL automation:

1. **Panel certificate** — checks if self-signed or expiring within 30 days, calls `ssl.panel.issue` via the agent to get a real LE cert
2. **Domain certificates** — issues certs for domains that don't have one (up to 3 retries for failed domains, 6h cooldown between attempts)
3. **Mail certificates** — issues certs for `mail.$domain` on active email domains (checks DNS points to server first)
4. **Renewals** — renews LE certs expiring within 30 days (up to 5 retry attempts)
5. **Expiry alerts** — notifies admin for certs expiring within 7 days

### Certbot Timer
Certbot also runs daily via systemd timer (`certbot.timer`) as a secondary renewal mechanism. A deploy hook copies renewed panel certs to FrankenPHP and reloads.

Renewal process:
1. Certbot validates domain ownership via webroot (places token in domain's public directory)
2. Let's Encrypt verifies the token via HTTP
3. If validation succeeds, new certificate is issued
4. Certificates are stored in `/etc/letsencrypt/live/{domain}/`
5. Nginx vhost config is updated with new cert paths
6. Nginx is reloaded (no downtime)

## Webroot Validation

Certificate validation uses webroot mode: Certbot places a challenge token in the domain's public web directory (usually `/home/{user}/{domain}/public/.well-known/acme-challenge/`). Let's Encrypt fetches the token via HTTP to verify domain ownership.

For webroot validation to succeed:
- Domain DNS must resolve to the server
- HTTP (port 80) must be accessible
- The `.well-known/acme-challenge/` directory must be writable by the web server
- Nginx must serve files from the webroot without redirecting to HTTPS

## Integration with Panel

The panel:
- Detects new domains and automatically requests certificates
- Monitors certificate expiration and schedules renewal
- Stores certificate metadata in the database (expiration dates, issuer)
- Displays certificate status on the Domains page
- Alerts administrators to expiring or missing certificates
- Auto-deploys certificates to Nginx vhost configurations

## Database Storage

Certificates are NOT stored in the database; only metadata is:
- `domains.ssl_issued_at` — timestamp when certificate was issued
- `domains.ssl_expires_at` — timestamp when certificate expires
- `domains.ssl_status` — status (active, pending, failed, expired)

Actual certificate files are in `/etc/letsencrypt/live/{domain}/`:
- `privkey.pem` — private key
- `cert.pem` — certificate
- `chain.pem` — certificate chain
- `fullchain.pem` — certificate + chain combined

## Nginx Configuration

Vhost files at `/etc/nginx/sites-available/{domain}.conf` reference certificates:

```nginx
listen 443 ssl http2;
ssl_certificate /etc/letsencrypt/live/example.com/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/example.com/privkey.pem;
```

When certificates are renewed, Nginx is reloaded to use the new certificate paths.

## Troubleshooting

### Certificate Issuance Failed

Check Let's Encrypt validation:
1. Verify domain DNS resolves: `dig example.com`
2. Verify HTTP is accessible: `curl http://example.com/.well-known/acme-challenge/test`
3. Check Certbot logs: `certbot logs` or `/var/log/letsencrypt/`
4. Verify domain is added to system: `jabali domain:list`

### Certificate Expired

Certificates that expire are typically renewed automatically. If renewal fails:
1. Check renewal logs: `certbot renew --dry-run`
2. Manually renew: `jabali ssl:renew <domain>`
3. Check Let's Encrypt rate limits (max 50 certs per domain per week)

### Webroot Not Accessible

If Let's Encrypt cannot access the challenge token:
1. Verify nginx is serving the domain: `curl -I http://example.com`
2. Check `.well-known/acme-challenge/` is writable: `ls -la /home/{user}/{domain}/public/.well-known/acme-challenge/`
3. Verify nginx config doesn't redirect HTTP to HTTPS before challenge validation
4. Check web server user can write to challenge directory

### Panel Certificate Issues

Panel certificate is critical for control panel access. If it expires:
1. Check status: `jabali ssl:panel`
2. Issue new cert: `jabali ssl:panel-issue`
3. Restart FrankenPHP: `systemctl restart frankenphp`

## Security

- Private keys are stored with restrictive permissions (readable only by root)
- Certificates are signed by Let's Encrypt, a trusted Certificate Authority
- Validation ensures only domain owners can request certificates
- Automatic renewal prevents certificate expiration
- Certificate transparency logs record all issued certificates (public record)

## Cost

Let's Encrypt certificates are **free**. No fees or subscriptions apply.
