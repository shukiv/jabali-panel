# Disaster Recovery Runbook

> **When to use this.** The server is unrecoverable (disk failure, host
> compromise, failed OS upgrade, etc.) and you need to bring the panel back up
> on a fresh box from a `jabali-backup server-backup` snapshot.
>
> For per-user restores (one account misbehaving on an otherwise-healthy
> server), use [`RESTORE.md`](RESTORE.md) instead.

## Preconditions

Before starting, confirm you have:

- [ ] A **fresh Ubuntu 24.04 LTS** (or compatible) target box — **not** a
      partially-failed old host. Wipe and reinstall the OS; otherwise
      restore will collide with leftover state.
- [ ] Root access (sudo).
- [ ] **Domain DNS still pointing to an IP you can control.** If DNS moved
      during the outage, update the A/AAAA records to the new target first.
- [ ] The **restic repository password** (`/etc/jabali/restic-password` from
      the old box, saved out-of-band — e.g., in a password manager). Without
      it, no snapshots are recoverable.
- [ ] **Network reachability** to the restic repository (S3/SFTP/local mount).
- [ ] (Optional but recommended) Saved copy of `destinations.json` from the
      old box, or know the destination URL + credentials by heart.

## Phase 0 — Sanity check

```bash
# From your laptop, verify you can still reach the backup repo
restic -r <REPO_URL> snapshots --tag type:server
# Expect: at least one "server" snapshot listed.
```

If no server snapshots exist, the server backup was never run. You can still
restore per-user data via the standard per-user restore path, but
`server-restore` will have nothing to replay.

## Phase 1 — Install Jabali on the fresh box

```bash
# On the target box:
curl -fsSL https://panel.jabali.com/install.sh | sudo bash
```

This installs the panel, nginx, FrankenPHP, MariaDB, PowerDNS, Stalwart,
Redis, PHP-FPM — everything the backup expects.

## Phase 2 — Install jabali-backup and point it at the repo

```bash
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-backup/main/install.sh | sudo bash

# Restore the restic password (obtained out-of-band)
sudo mkdir -p /etc/jabali
echo '<PASSWORD>' | sudo tee /etc/jabali/restic-password
sudo chmod 600 /etc/jabali/restic-password

# Point at the backup repo
sudo jabali-backup destination add
# Follow the prompts (rclone/S3/SFTP/local).

sudo jabali-backup doctor --fix
```

## Phase 3 — Verify the DR snapshot is visible

```bash
sudo jabali-backup list server-snapshots
```

Expected output:
```
Disaster Recovery Snapshots (tag type:server)
─────────────────────────────────────────────────────────────
ID         Date                 Hostname             Size
─────────────────────────────────────────────────────────────
abc123de   2026-04-13 02:00:00  jabali-prod-1        2026-04-13
...
```

Pick the snapshot ID you want to restore from (usually the most recent).

Optional browse before committing:
```bash
sudo jabali-backup ls server --snapshot=abc123de
sudo jabali-backup ls server config/jabali --snapshot=abc123de
sudo jabali-backup ls server data/stalwart --snapshot=abc123de  # DKIM check
```

## Phase 4 — Restore

```bash
sudo jabali-backup server-restore --snapshot=abc123de
```

The restorer runs in 5 phases (~minutes to tens of minutes depending on data
size):

1. **Base install** — `.env`, `/etc/jabali/`, panel source tree (git clone or
   tarball extract), storage uploads.
2. **Databases** — `jabali` + `powerdns` MySQL databases, migrations applied.
3. **Service configs** — nginx, PHP-FPM, Stalwart (config + RocksDB + DKIM),
   Redis, MariaDB, panel SSL, systemd units, Let's Encrypt.
4. **User accounts** — per-user restores for every account captured.
5. **Finalize** — agent addons, Bulwark reinstall hint, service restarts,
   post-restore health checks.

Use `--skip-users` for a server-only replay (config sanity check) or
`--force` to overwrite existing files (default is non-destructive).

## Phase 5 — Post-restore verification

```bash
# Service health
systemctl status jabali-panel jabali-agent jabali-queue nginx \
    stalwart-mail redis-server mariadb pdns

# Panel reachable
curl -sk https://localhost:8443/jabali-admin/login | head

# Nginx config valid
sudo nginx -t

# Database contents
mysql -e "SELECT COUNT(*) FROM jabali.users"

# DNS answering
dig @localhost example.com

# Every domain responds
for conf in /etc/nginx/sites-enabled/*.conf; do
    domain=$(basename "$conf" .conf)
    curl -sk --resolve "$domain:443:127.0.0.1" "https://$domain" -o /dev/null -w "%{http_code} $domain\n"
done
```

### DKIM verification

If Stalwart data was included in the snapshot (default, unless
`--skip-stalwart-data` was used), DKIM keys were restored from RocksDB and
should match the DNS records without change:

```bash
sudo stalwart-cli --url http://localhost:8080 dkim-keys list
dig TXT _domainkey.example.com +short
```

If Stalwart data was **not** in the snapshot, DKIM keys are regenerated on
first boot and **must be re-published to DNS** for mail to authenticate:

```bash
# For each domain:
sudo stalwart-cli --url http://localhost:8080 dkim-keys rotate example.com
# Then update the _domainkey.example.com TXT record at your DNS provider.
```

### Bulwark webmail

If `metadata/bulwark.json` was in the snapshot, Bulwark files are **not**
restored — the install.sh reinstall path is the supported recovery:

```bash
sudo /opt/jabali-backup/install.sh --reinstall-bulwark
```

## Phase 6 — Cut DNS (if necessary)

If the target box has a different IP than the original, update the A/AAAA
records for the panel hostname and every user domain. Let's Encrypt renewal
state was restored, so existing certs continue to work until expiry.

## Troubleshooting

### "No server snapshots found"
The restic repo may be pointed at the wrong path. Run `jabali-backup
destination list` and confirm the active destination. If multiple
destinations exist, pass `--destination=<name>` explicitly.

### Panel returns 502 after restore
PHP-FPM pools are restored in phase 3, nginx in the same phase — pool sockets
should exist before nginx tries to connect. If the 502 persists:
```bash
sudo systemctl restart php8.4-fpm nginx
sudo ls /run/php/
```

### `composer install` / `npm ci` fail during tarball-mode restore
The no-git fallback assumes internet access on the target box. If offline,
install Composer/Node deps on a sibling box and `rsync` the `vendor/` and
`node_modules/` directories across.

### Mail authentication failing despite DKIM key restore
The RocksDB restore may have landed with the wrong ownership. Fix with:
```bash
sudo systemctl stop stalwart-mail
sudo chown -R stalwart-mail:stalwart-mail /var/lib/stalwart-mail
sudo systemctl start stalwart-mail
```

## Related

- [README.md](README.md) — command reference
- [ARCHITECTURE.md](ARCHITECTURE.md) — 20-row collector/restorer map
- `jabali/docs/backup-server-spec.md` — authoritative spec (what must be backed up)
- `plans/server-backup-completion.md` — audit + gap-closing plan
- [`adr/`](adr/) — architectural decisions (ADR-0005: server-download temp-file approach, ADR-0006: PHP-FPM before nginx, ADR-0007: delegate infra recreation to agent)
