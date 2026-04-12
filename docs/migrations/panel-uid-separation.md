# Migration: Panel uid separation (Phase 12 of ADR-0007)

**Status**: playbook + script only. **NOT YET APPLIED** to any server.

**Risk**: High. This changes the uid the panel runs as. File ownership on
`storage/`, `bootstrap/cache/`, vendor/, logs, and uploaded files all have
to move in lockstep. Getting it wrong takes the panel down.

## Why

Today the panel runs as `www-data`. So does every WordPress site hosted on
the same box. A WordPress RCE on any tenant → the attacker is in the same
uid as the panel → they can open the agent socket (which is `0660
root:www-data`) → root.

Moving the panel to its own uid (`jabali-panel`) closes that path without
sacrificing the agent's privilege model.

## Target state

| Component | Current | After |
|---|---|---|
| FrankenPHP `User=` | `www-data` | `jabali-panel` |
| Agent socket mode | `0660 root:www-data` | `0660 root:jabali-panel` |
| `/var/www/jabali/storage` | `www-data:www-data` | `jabali-panel:jabali-panel` |
| `/var/www/jabali/bootstrap/cache` | `www-data:www-data` | `jabali-panel:jabali-panel` |
| `/var/www/jabali/.env` | `www-data:www-data 0640` | `jabali-panel:jabali-panel 0640` |
| Uploaded files under `storage/app/public` | `www-data:www-data` | `jabali-panel:jabali-panel` |
| WordPress sites (PHP-FPM pools) | `www-data:www-data` | `www-data:www-data` (unchanged) |
| Nginx cache (`/home/*/cache/nginx`) | `www-data:www-data` | `www-data:www-data` (nginx writes these) |

## Preflight

Must be run on a test server (LXC) first and left running for **at least
a week** before touching production.

```bash
# 1. Confirm the panel is fully idle
systemctl stop jabali-queue.timer
systemctl stop jabali-panel

# 2. Snapshot the current state
tar czf /root/pre-uid-migration-backup.tgz \
    /etc/systemd/system/jabali-panel.service \
    /etc/systemd/system/jabali-agent.service \
    /var/www/jabali/bootstrap \
    /var/www/jabali/storage \
    /var/www/jabali/.env

# 3. Run the migration
bash /var/www/jabali/scripts/migrate-panel-uid.sh apply

# 4. Verify
systemctl status jabali-panel
ls -la /var/run/jabali/agent.sock
ls -la /var/www/jabali/storage
curl -k -I https://localhost:8443/jabali-admin/login
```

## Rollback

If anything goes wrong, the rollback is:

```bash
bash /var/www/jabali/scripts/migrate-panel-uid.sh rollback
# Or, from the snapshot:
systemctl stop jabali-panel jabali-agent
tar xzf /root/pre-uid-migration-backup.tgz -C /
systemctl daemon-reload
systemctl restart jabali-agent jabali-panel
```

## Per-phase checkpoints

| Step | Verify |
|---|---|
| Create `jabali-panel` system user | `id jabali-panel` |
| Stop FrankenPHP | `systemctl is-active jabali-panel` → inactive |
| chown storage, bootstrap/cache, .env | `stat -c %U /var/www/jabali/storage` → jabali-panel |
| Update FrankenPHP `User=` and `Group=` | `systemctl cat jabali-panel \| grep User=` → jabali-panel |
| Update agent socket group | `ls -la /var/run/jabali/agent.sock` shows `root:jabali-panel` |
| Restart agent (creates new socket) | `systemctl restart jabali-agent` |
| Restart panel | `systemctl restart jabali-panel` |
| Smoke test login flow | HTTP 200 on `/jabali-admin/login` |

## Known migration traps

- **Uploaded files** (branding logos, favicons) accumulate over time. The
  script uses `chown -R` on `storage/app/public` but must also fix any
  files uploaded between the snapshot and the actual switchover.
- **Composer runs during install/upgrade** — `composer install` is done as
  `www-data` in several install.sh functions. Those invocations need to
  switch to `jabali-panel` *after* this migration. Missing one will cause
  vendor/ ownership drift.
- **`.git/info/exclude` + `.git` directory ownership** — the deploy flow
  does `git pull` as www-data today. Switch to `jabali-panel` in the
  deploy helper.
- **Agent reads panel-owned files** (for diagnostics, SSL cert paths, etc).
  Add `jabali-panel` to groups as needed, or have the agent read via root
  ops. Most reads are already through the socket so the agent already has
  root — this is only a concern for any direct filesystem coupling.
- **Sessions are stored under `storage/framework/sessions`** — existing
  sessions will become readable by the new uid after chown, so logged-in
  users stay logged in. If chown skips session files, everyone is kicked.

## Status tracking

- [ ] Script runs end-to-end on LXC test server
- [ ] Panel serves requests after switchover for 24 hours with no errors
- [ ] Panel serves requests for 7 days with no errors
- [ ] Addon install/uninstall flows verified
- [ ] SSL issuance flow verified (Agent v2 path)
- [ ] Backup run verified (Agent v2 path)
- [ ] Rollback path tested end-to-end
- [ ] `install.sh` updated so new servers ship with this uid from day zero
- [ ] Production rollout

None of these checkboxes is complete as of this writing. This document
exists so the migration has a plan; the plan will be executed over multiple
sessions with real testing in between.
