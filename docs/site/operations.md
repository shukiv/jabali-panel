# Operations Runbook

Day-2 operator reference. For day-1 install see [installation.md](./installation.md).

## Standard daily / weekly cadence

| When | Action |
|---|---|
| Daily | Review `/jabali-admin/security` → CrowdSec → Decisions. Spot any allowlist additions you need to make. |
| Daily | Review `/jabali-admin/notifications` (or the bell) for `cert_renew failed`, `backup_failed`, `service_down`. |
| Weekly | `jabali update` — pulls latest fixes; auto-applies migrations. |
| Weekly | Inspect `/jabali-admin/server-status` → trends. Spot disks filling up before quota alarms fire. |
| Monthly | Verify a restore round-trip from at least one backup destination. |

## Common operator tasks

### Lock out a panel user

```bash
jabali user disable <email|username>
```

(Domain stays up; user can't log in to the panel UI.)

### Force-reset a user's password / 2FA

```bash
jabali user password <email>            # generates new password, prints once
jabali user 2fa-reset <email>           # strip TOTP + recovery codes (CLI escape hatch)
```

### Issue a one-time admin login

```bash
jabali admin one-time-login
```

Useful when 2FA on an admin account is locked out.

### Rotate the DB root password

`/jabali-admin/settings` → Database → Root Password → Rotate.

Or CLI:

```bash
jabali admin db root-password rotate
```

Audited (success + failure), announced via M14.

### Move a domain between users

Not a first-class operation in the UI. The supported path:

1. Backup the old user's `account_full`.
2. Restore into the new user under a fresh domain name (or temp domain).
3. Adjust DNS.

(Or: contact upstream — this is on the roadmap.)

### Add a new IPv4 to the pool

1. Bring the IP up on the host (`ip addr add <ip>/<prefix> dev <iface>`).
2. `/jabali-admin/ips` → Add.
3. Assign per-domain via Domains → Edit → Listen IP.

### Verify the public installer URL (get.jabali-panel.com)

Short install URL (GH #444). After a release or any DNS/redirect change, confirm
it still serves the installer:

```bash
curl -fsSL https://get.jabali-panel.com | head -5   # expect the bash shebang + installer header
```

If it fails, the canonical fallback always works:
`curl -fsSL https://github.com/shukiv/jabali-panel/raw/main/install.sh | sudo bash`.
Setup + redirect config live in `deploy/get-installer/`.

### Add a new PHP version

1. `apt install php8.x-fpm php8.x-cli php8.x-mbstring …`
2. The panel detects it on the next reconciler tick; appears in `/jabali-admin/php-pools`.
3. Domains can pick it under Edit → PHP Version.

## Common failures

| Symptom | First thing to check |
|---|---|
| Panel UI says "502 Bad Gateway" | `systemctl status jabali-panel`; `journalctl -u jabali-panel -f` |
| Cert never issues | `jabali ssl list` → see the row's last error; `journalctl -u jabali-agent -f` while you toggle SSL again |
| `Dirty database version N` on boot | A failed migration. `jabali migrate up` to retry; if it's deterministic, file an issue with the journal output. |
| `nginx -t` fails after a domain edit | Missing FastCGI cache keyzone (if you enabled cache before `jabali update` landed the keyzone conf). Run `jabali update` once. |
| Mail accounts work in IMAP but not SMTP submission | Bulwark may be down. `systemctl status bulwark`; check `:587` listener. |
| User app suddenly can't reach external API | Per-user egress firewall may have dropped the destination. Users → Edit → Egress. |

## Repair

```bash
jabali repair --diagnose       # default: report only
jabali repair --auto           # safe auto-fixes (perms, drop-in regen, no destructive)
jabali repair --all --yes      # destructive (wipes nginx tmp, regen pool sockets) — only when --diagnose tells you to
```

`jabali repair` covers 7 detectors (M33-era), grouped by destructiveness. ADR-0077.

## Migration cleanup

After a successful cPanel / DA / Hestia restore: `jabali domain orphan-prune --dry-run` then `--apply` to clean any source-side orphans the importer missed.

## Diag bundle

`/jabali-admin/support` → encrypted bundle, `mailto:` send. See [support.md](./support.md).
