# M34 Per-user Egress Firewall — Operator Runbook

Companion to ADR-0084. Use this for day-2 operations: troubleshooting
blocked legitimate egress, adding admin-debug holes, reading kernel-log
LEARNING-mode hints, validating drop counters, and rolling back.

## What it is, in one sentence

Every Linux user's PHP-FPM pool sits inside a cgroup v2 slice
(`jabali.slice/jabali-user.slice/jabali-user-<USERNAME>.slice`); an
nftables `socket cgroupv2 level 3` match keyed by that slice path
drops outbound SYNs that don't hit the default allowlist or a per-user
override.

## Files / units / commands

| Where | What |
|---|---|
| `/etc/nftables.d/jabali-per-user-egress.nft` | Generated rule file. Reconciler is the only writer. |
| `/etc/jabali/per-user-egress.mode` | State a user's missing policy row is seeded in (`learning` on hosts that predated M34, `enforced` on fresh installs). Not a pin. |
| `/etc/jabali/per-user-egress.pin` | Operator pin. `learning` halts the auto-flip timer. |
| `/etc/jabali/.per-user-egress-installed` | One-time marker; chooses LEARNING (existing host) vs ENFORCED (fresh install) at install time. |
| `jabali-per-user-egress-load.service` | Boot-time `nft -f` re-apply of the rule file. |
| `jabali-per-user-egress-flip.timer` | Daily 03:30 UTC LEARNING→ENFORCED auto-flip. |
| `jabali per-user-egress flip-mature [--soak-days N] [--dry-run]` | Manual flip. Honors operator pin. |
| `nft list table inet jabali_per_user` | Inspect live rules + counters. |
| `nft -j list counters table inet jabali_per_user` | Counter snapshot for scripting. |

## Daily checks

```bash
systemctl is-active jabali-per-user-egress-load.service
systemctl status jabali-per-user-egress-flip.timer --no-pager
nft list table inet jabali_per_user | head -40
mysql -u jabali_panel_app jabali_panel \
  -e "SELECT state, COUNT(*) FROM user_egress_policies GROUP BY state;"
```

Expected on a healthy host: load.service `active (exited)`,
flip.timer active, table populated, state distribution shows zero
`off` users (off is break-glass, never the default).

## Troubleshooting blocked legitimate egress

**Symptom:** A user's WordPress install fails to download a plugin
update / Composer can't reach packagist / cron job times out hitting
an external API.

1. Confirm it's egress filter (not DNS, not nginx limit_req, not
   CrowdSec). Run as the user:
   ```bash
   sudo -u <user> -- bash -c 'curl -v https://api.github.com 2>&1 | head -20'
   ```
   Connection refused / timeout pointing to a non-allowlisted port =
   egress filter is doing its job.

2. Check the user's drop counter:
   ```bash
   nft list counter inet jabali_per_user user_<USERNAME>_drops
   ```
   Non-zero packets = drop pattern matches. Zero = look elsewhere
   (DNS, slice not active, etc).

3. Three resolution paths, in order of preference:
   - **User submits a request** via /me/egress/request and admin
     approves at /jabali-admin/security?tab=egress. Best for repeat
     destinations (admin keeps audit history).
   - **Admin adds a per-user extra** at the same admin tab. Use this
     when the request channel is too slow.
   - **Operator harden/relax the global default allowlist** at
     server_settings.egress_default_ports_tcp / loopback_cidrs / etc.
     Use this only when the destination is universal (DNS over HTTPS
     to a non-:443 port, etc.).

## Adding a temporary admin-debug hole

Not yet wired (Step 8 plan called for `/admin/users/:id/egress/test`
but it was deferred — defaults editor is available via DB). For now:

```sql
UPDATE user_egress_policies SET allowed_extra = JSON_ARRAY_APPEND(
  COALESCE(allowed_extra, JSON_ARRAY()), '$',
  JSON_OBJECT('cidr', '192.0.2.5/32', 'port', 4444, 'protocol', 'tcp', 'comment', 'debug 2026-04-29')
) WHERE user_id = '<USER_ULID>';
```

Reconciler picks up the change within one tick (~60 s). Remember to
clean up the row afterwards.

## Reading LEARNING-mode kernel logs

LEARNING chains log + accept (rate-limited 5/min/user) instead of
dropping. Tail with:

```bash
journalctl --since "10 minutes ago" -k | grep 'jabali-egress-learn-<USER>'
```

Each line shows the source/destination so the operator can decide
whether to allowlist before flipping the user to ENFORCED.

## Auto-flip behaviour

Hosts upgrading from a build without M34 start every user in
LEARNING for 7 days. The daily timer flips matured rows to ENFORCED
unless `/etc/jabali/per-user-egress.pin = learning` is set.

To extend the soak indefinitely:
```bash
echo learning > /etc/jabali/per-user-egress.pin
```
To run the auto-flip immediately (dry-run first):
```bash
jabali per-user-egress flip-mature --dry-run
jabali per-user-egress flip-mature
```

To keep one user in LEARNING while flipping others, set their state
explicitly via the admin UI before the timer runs.

## Rollback

Three levels of rollback, increasing in scope:

1. **Single user → off.** Admin UI `/jabali-admin/security?tab=egress`,
   click the user, set state to OFF. Reconciler emits no chain for
   that user; the slice falls through to default accept within one
   tick.

2. **Whole feature paused (drop the table).** No supported toggle in
   this release, but the runbook fallback is:
   ```bash
   nft delete table inet jabali_per_user
   systemctl mask jabali-per-user-egress-load.service
   systemctl mask jabali-per-user-egress-flip.timer
   ```
   Reconciler will recreate the table on its next tick — also disable
   the reconciler hook by stopping panel-api or reverting the deploy.

3. **Migration rollback.** `golang-migrate -path … down 3` undoes
   migrations 100–102. The agent commands and renderer remain in
   place but no table on disk; rule file becomes a no-op header. Re-
   apply `up 3` to re-enable.

## What's NOT covered

- IPv6 outbound is permitted via the loopback6 default + per-user
  IPv6 extras only. Full IPv6 egress policy parity is a phase 2
  blueprint.
- No payload inspection. CrowdSec AppSec covers the HTTP layer (M27).
- Tetragon companion policy (Step 9 of the M34 plan) is deferred.

## Cross-references

- ADR-0084 — design rationale + alternatives considered.
- M18 (slices) / M14 (notifications) / M27 (CrowdSec AppSec).
- `feedback_install_sh_is_truth` — install.sh owns the
  install_per_user_egress() postconditions; this runbook is a
  recovery aid, not a substitute for re-running the installer.
