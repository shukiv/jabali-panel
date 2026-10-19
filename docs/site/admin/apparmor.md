# AppArmor

Security → AppArmor. Per-profile status surface for the AppArmor profiles the panel ships.

## Shipped profiles

| Profile | Confines |
|---|---|
| `jabali-panel` | The panel API process. |
| `jabali-agent` | The privileged agent process. |
| `stalwart-mail` | Stalwart SMTP / IMAP / JMAP. |
| `pdns` | PowerDNS authoritative. |
| `pdns-recursor` | PowerDNS recursor. |
| `nginx` | The nginx master and workers. |
| `php-fpm` | Per-version FPM masters and pool workers. |
| `kratos` | Kratos identity. |
| `bulwark` | Bulwark Node SPA + bridge. |

Each profile is shipped under `/etc/apparmor.d/`. The installer enables them in `enforce` mode by default.

## States

For each profile, the page shows:

- **Mode** — `enforce`, `complain`, or `disabled`.
- **Denial count (24 h)** — incremented every time the kernel logs an AppArmor `DENIED` line for the profile.
- **Last denial** — the most recent denial detail (rule, path, syscall).

## Per-profile actions

- **Reload** — re-parse the profile from `/etc/apparmor.d/` and apply.
- **Set complain** — switches the profile to log denials without enforcing. Useful during incident response or after a host package upgrade introduces a transient denial pattern.
- **Set enforce** — restores enforcement.
- **Acknowledge** (complain mode only) — records that you keep the profile in complain mode on purpose, for example `jabali-fpm-app` so a site's PHP can run `df` or `ls`. The Server Status alert ("AppArmor profile not enforcing") and the header badge then leave the profile out. The page still shows its mode, with an **acknowledged** tag. The acknowledgement ends when the profile's mode changes: at once when you flip it on this page, and on the panel's next read of the profile modes (the header checks every 30 seconds while an admin is signed in) when `jabali apparmor flip-mature` or a manual `aa-enforce` changed it. A profile that later goes back to complain is then named again. **Show warning again** withdraws it. A profile that is missing or unconfined can't be acknowledged: that is a fault, not a choice (GH #2078).
- **Disable** — unload the profile entirely. Only do this temporarily.

## Automatic enforce

A daily timer, `jabali-apparmor-flip-mature.timer`, runs `jabali apparmor flip-mature`. It switches a profile on its list (`jabali-panel`, `jabali-agent`, `jabali-bulwark`, `jabali-kratos`, `stalwart-mail`) from complain to enforce once the profile has had no violations for 7 days.

A violation is a `DENIED` event, or the `ALLOWED` event a complain-mode profile logs for each access it would deny. The command reads them from the audit log (`/var/log/audit/audit.log` and its rotations) and from the journal. A profile that is still violating stays in complain mode, and `journalctl -u jabali-apparmor-flip-mature` shows its events. `jabali-fpm-app` and `jabali-sendmail` are never switched automatically.

## Diagnosing a denial

```bash
journalctl -k | grep DENIED
```

…shows kernel-side AppArmor denials with the profile, operation, and target. The most common cause of a new denial after `jabali update` is a path the agent now needs to touch (a new pool directory, a new state file) that is not in the shipped profile yet. File an issue; the fix lands in the next release.

## Custom rules

The shipped profiles include `tunables/global` and the local include `tunables/<profile>` for site-specific additions. Adding an `audit deny` rule in the local include is safe across `jabali update` — the agent does not overwrite the local includes.

## Why per-profile

A single global profile would either be too permissive (defeating the point) or too restrictive (denying legitimate operations the agent or panel needs). Per-process profiles let each component carry the narrowest possible policy.
