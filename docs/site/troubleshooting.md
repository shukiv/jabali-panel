# Troubleshooting

## First steps for any issue

1. `jabali repair --diagnose` — covers most known states.
2. `/jabali-admin/server-status` — visual at-a-glance.
3. `journalctl -u jabali-panel -u jabali-agent -f` in a separate terminal while you reproduce the problem.

## Install-time issues

### CrowdSec fetch fails (DNS)

Symptom: installer hangs at "Installing CrowdSec" or `Could not resolve host: packagecloud.io`.

Cause: outbound `:53/udp` is blocked, so the recursor can't recurse.

Fix:

```bash
JABALI_DNS_FORWARDER=<reachable-resolver> bash install.sh
```

The installer then masks systemd-resolved, writes `/etc/resolv.conf` with `options use-vc timeout:5 attempts:2`, strips the `resolve [!UNAVAIL=return]` NSS shim, and forwards `.` via TCP.

### pdns-recursor crash-loops with `source value 18446744073709551615 is larger than 65535`

Cause (now fixed): the install template had `forward-zones-recurse=.=${DNS_FORWARDER:-1.1.1.1;9.9.9.9}` inside a quoted heredoc; the variable wasn't interpolated, recursor parsed the literal string, and the YAML loader cast a length-of-string overflow into the uint16 port field.

Fix: `jabali update` to pick up the post-fix install template.

### Installer fails at "render-config" for AppSec

Cause: `install_crowdsec_appsec` ran pre-build, so render-config had no binary. PR#92 gates this; PR#93 re-runs render-config post-build.

Fix: `jabali update`.

### Vite OOM on small VM

Symptom: `npm run build` exits 137 during `panel-ui` build on a 1 GB RAM VPS.

Cause: V8 heap default outsizes the VM.

Fix (already in install.sh ≥ PR #69): `NODE_OPTIONS=--max-old-space-size=<50%-of-RAM>` + 1 GB swap auto-created on exit 137 fallback.

## Runtime issues

### `Dirty database version N`

Cause: a migration ran partially, then failed.

Fix:

```bash
jabali migrate up        # idempotent retry
```

If it still errors, look at the journal:

```bash
journalctl -u jabali-panel -n 200 | grep -A 20 'migration'
```

Common root cause: a previous merge dropped a migration file but later migrations ALTER tables expecting it. The "audit migration numbering after merge" rule prevents this in CI; if you hit it, file an issue with the migration sequence (`ls panel-api/internal/db/migrations`).

### Webmail won't log in

Causes, in rough order of likelihood:

1. The mailbox's password changed in the panel but the user is using the old one — reset under Mail → Mailboxes.
2. Stalwart is down — `systemctl status stalwart-mail`.
3. The webmail SSO file expired before it was hit (TTL is 60 s) — sync NTP if the host clock is skewed.
4. AppArmor in `enforce` blocked something — `journalctl -k | grep DENIED`.

### SSL cert doesn't issue

Check the row at `/jabali-admin/ssl`:

| Last error | Likely cause |
|---|---|
| `unauthorized` | Domain doesn't resolve to this server's IP — fix DNS at registrar. |
| `rateLimited` | LE rate limit hit. Wait. |
| `connection refused on :80` | Firewall blocks `:80` from outside, or nginx isn't listening on `:80` for that vhost (it must, even when SSL is on, for HTTP-01). |
| Panel cert: `panel hostname not set` | Server Settings → Panel Hostname not yet configured. |

### Per-user PHP-FPM pool socket missing

Symptom: `502 Bad Gateway` on a user's vhost.

Fix:

```bash
jabali repair --diagnose       # confirms which user is affected
systemctl restart 'php*-fpm.service'
```

Underlying cause is usually a stale drop-in after a package change. The reconciler regenerates on next tick.

### Backup destination test fails

`/jabali-admin/backups` → Destinations → Test. Common causes:

- SFTP: wrong host key, wrong username, wrong key path on the panel host.
- S3: clock skew on the panel host > 15 minutes (AWS rejects SigV4).
- Restic REST: server has self-signed cert and `JABALI_RESTIC_INSECURE_TLS` isn't set.

Auto-init: the test action runs `restic init` if the repository doesn't exist. Won't auto-init on a destination that has files but isn't a restic repo (refuses to overwrite).

## Full teardown

There is no `jabali --uninstall`. Intended path: redeploy the VM.

If you must keep the host:

```bash
systemctl disable --now jabali-panel jabali-agent stalwart-mail kratos bulwark pdns pdns-recursor crowdsec
apt purge -y stalwart-mail crowdsec
trash /etc/jabali /var/lib/jabali /var/lib/jabali-* /etc/nginx/sites-*/jabali-* /etc/php/*/fpm/pool.d/jabali-* /etc/systemd/system/jabali-* /etc/letsencrypt
userdel -r jabali
# DB still has panel data; drop it manually.
```

Then reboot. The host won't be Jabali-clean (CrowdSec data dirs, restic caches, etc. linger) but the panel won't run.
