# Panel SSL — runbook

The panel hostname's TLS cert sits at `/etc/jabali/tls/panel.{crt,key}`. By default it is **self-signed**. M32 (ADR-0066) adds an opt-in flow to replace it with a Let's Encrypt cert covering `<hostname>` + `mail.<hostname>` SAN.

## What each status means

| Status | Meaning |
|---|---|
| `self_signed` | The starting state and the silent fallback. The cert is the openssl-generated SAN cert `provision_tls_cert` produced. The "Use Let's Encrypt" toggle is OFF, OR the routability gate failed. |
| `pending_acme` | The reconciler (or the admin via "Retry now") just dispatched `ssl.panel.issue`. Will flip to `issued` or `pending_acme_retry` within a minute. |
| `issued` | Certbot returned a fresh lineage; the deploy-hook copied `fullchain.pem` + `privkey.pem` to `/etc/jabali/tls/panel.{crt,key}` and reloaded nginx + jabali-panel + jabali-bulwark (and vsftpd when FTP is active). `expires_at` is the LE notAfter. |
| `pending_acme_retry` | Last attempt failed; `last_error` carries the reason. Reconciler will retry every 3 hours until either issued or the admin disables `use_le`. |
| `failed` | Terminal — non-retryable error (e.g. LE rate-limit exhausted). M32.1 will surface a Reset button; today: edit the row directly via `mariadb` to clear the state. |

## Routability gate

The "Use Let's Encrypt" toggle is **disabled** in the UI when the gate refuses. Reasons surfaced as `routable_reason`:

- `missing hostname` — set Server Settings → General → Hostname first.
- `missing public_ipv4` — set Server Settings → General → Public IPv4 first.
- `non-routable hostname suffix` — `localhost`, `*.local`, `*.localdomain`. LE refuses these and so do we.
- `dns lookup failed: …` — `<hostname>` doesn't resolve from the panel server. Check the registrar A-record.
- `dns lookup returned no IPv4 records` — only AAAA records returned. M32.1 may add IPv6-only support.
- `dns points elsewhere (got X.X.X.X, want Y.Y.Y.Y)` — the A-record points at a different IP than the panel's `public_ipv4`. Either fix the registrar record or update Server Settings.

## First-time issuance

Sequence on a fresh public VPS:

1. Set the registrar A-record `<hostname>` → VPS public IP. Wait for propagation (`dig +short <hostname> @8.8.8.8`).
2. Server Settings → General → confirm Hostname / Public IPv4 / Admin email are correct (admin email is required for LE registration).
3. Optional: flip the **staging** toggle ON before flipping use_le ON for a rehearsal that doesn't burn the LE rate limit.
4. Flip "Use Let's Encrypt" ON. Status flips to `pending_acme` within ~30s (one reconciler tick), then `issued` within another minute.
5. If you used staging: flip staging OFF, then click **Retry now** to pull a real (browser-trusted) cert.
6. Hard-refresh `https://<hostname>:8443/` and `https://mail.<hostname>/` — both should now show a valid padlock.

## Manual recovery — reconciler stuck on `pending_acme_retry`

If the reconciler keeps failing and you want to debug certbot directly:

```sh
# Read what certbot saw last:
mariadb -uroot jabali_panel \
  -e "SELECT status, attempt_count, last_error, next_retry_at FROM panel_certificate"

# Hand-run certbot (same args the agent uses):
sudo certbot certonly --webroot \
  -w /var/www/jabali-panel-acme \
  -d "$(hostname -f)" -d "mail.$(hostname -f)" \
  -m "<admin-email>" \
  --agree-tos --non-interactive --keep-until-expiring

# If it succeeds, run the deploy-hook so the panel picks up the cert:
sudo /etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh

# Reset the row's retry state so the reconciler stops backing off:
mariadb -uroot jabali_panel \
  -e "UPDATE panel_certificate SET status='self_signed', attempt_count=0, next_retry_at=NULL WHERE id=1"
```

## Roll back to self-signed

Flip "Use Let's Encrypt" OFF in the UI. The existing LE cert stays on disk until it expires; the reconciler stops scheduling renewals via this code path (certbot's own timer keeps renewing the lineage on disk, but nothing copies it back to `/etc/jabali/tls/` once `use_le=0`).

To force-regenerate self-signed today:

```sh
sudo rm /etc/jabali/tls/panel.{crt,key}
sudo /usr/local/bin/jabali update -f   # provision_tls_cert re-runs
```

## Renewal mechanics

- `certbot.timer` (Debian default) runs daily.
- On every successful renewal certbot calls every script in `/etc/letsencrypt/renewal-hooks/deploy/`.
- `/etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh` (M32 deploy-hook):
    1. Copies `fullchain.pem` → `/etc/jabali/tls/panel.crt` (root:jabali 0640) atomically via `install -m`.
    2. Copies `privkey.pem` → `/etc/jabali/tls/panel.key` (same).
    3. `systemctl reload nginx` (graceful, keeps existing :443 sessions up).
    4. `systemctl restart jabali-panel` (Go server doesn't SIGHUP-reread; ~100ms TLS gap).
    5. `systemctl restart jabali-bulwark` (Next custom server reads cert at boot; ~3-5s webmail gap).
    6. `systemctl try-reload-or-restart vsftpd` **only if vsftpd is already active** (JAB-268). FTPS reads the same `panel.{crt,key}` but has no hook of its own, so without this it presents the stale in-memory cert until the daemon next restarts. The `is-active` guard means a masked/opt-out FTP module (the default) is never started by a renewal. A failure is logged naming FTPS as the stale consumer.

The panel cert is intentionally separate from the per-domain SSL certs the reconciler manages for hosted domains. M32 only governs the panel hostname; everything else still flows through `ssl_certificates` + the M14 SSL renewal pipeline.

## Files of interest

| Path | Owner | Why |
|---|---|---|
| `/etc/jabali/tls/panel.crt` | root:jabali 0640 | The actual cert nginx, panel-api, and Bulwark all read |
| `/etc/jabali/tls/panel.key` | root:jabali 0640 | Same lifecycle |
| `/etc/letsencrypt/live/<hostname>/` | root:root | LE lineage; deploy-hook reads from here |
| `/etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh` | root:root 0755 | Deploy-hook itself |
| `/var/www/jabali-panel-acme/` | root:www-data 0750 | LE HTTP-01 challenge webroot |
| nginx default `:80` server block | n/a | Has `location ^~ /.well-known/acme-challenge/ { root /var/www/jabali-panel-acme; }` |
