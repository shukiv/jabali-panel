# AppArmor

Security → AppArmor. Per-profile status surface for the AppArmor profiles the panel ships.

## Shipped profiles

| Profile | File in `/etc/apparmor.d/` | Confines |
|---|---|---|
| `jabali-panel` | `usr.local.bin.jabali-panel-api` | The panel API process. |
| `jabali-bulwark` | `usr.local.bin.jabali-bulwark` | Bulwark webmail. |
| `stalwart-mail` | `usr.local.bin.stalwart-mail` | Stalwart SMTP / IMAP / JMAP. |
| `jabali-fpm-app` | `usr.local.libexec.jabali.fpm-exec` | Each site's PHP-FPM workers. |
| `jabali-sendmail` | `usr.local.libexec.jabali.fpm-exec` | The PHP `mail()` shim. Its rules follow `jabali-fpm-app` in the same file, and the local file's rules apply only to `jabali-fpm-app`. |

The agent and Kratos run without a profile; their systemd units sandbox them. A new profile is loaded in `complain` mode first; a daily timer moves it to `enforce` once it is soak-clean (see [Security](../security.md)). `jabali update` keeps the mode each profile is in.

## States

For each profile, the page shows:

- **Mode** — `enforce`, `complain`, or `disabled`.
- **Denial count (24 h)** — incremented every time the kernel logs an AppArmor `DENIED` line for the profile.
- **Last denial** — the most recent denial detail (rule, path, syscall).

## Per-profile actions

- **Reload** — re-parse the profile from `/etc/apparmor.d/` and apply.
- **Set complain** — switches the profile to log denials without enforcing. Useful during incident response or after a host package upgrade introduces a transient denial pattern.
- **Set enforce** — restores enforcement.
- **Disable** — unload the profile entirely. Only do this temporarily.

## Diagnosing a denial

```bash
journalctl -k | grep DENIED
```

…shows kernel-side AppArmor denials with the profile, operation, and target. The most common cause of a new denial after `jabali update` is a path the agent now needs to touch (a new pool directory, a new state file) that is not in the shipped profile yet. File an issue; the fix lands in the next release. Until then, allow the path in the profile's local file ([Custom rules](#custom-rules)).

## Custom rules

`jabali update` overwrites every file in the table above and reloads it, so don't edit them. Put site-specific rules in `/etc/apparmor.d/local/<file>`, named after the profile's file: each profile ends with `include if exists <local/<file>>`, and Jabali never writes there. Then reload the profile, for example:

```bash
apparmor_parser -r /etc/apparmor.d/usr.local.bin.stalwart-mail
```

A local rule can add access but can't lift a `deny` rule in the profile: a deny always wins. The profiles deny, for example, `/home` and `/root` to Stalwart. Each service's systemd sandbox applies as well: Stalwart can write only to the paths its unit lists in `ReadWritePaths=`, whatever its profile allows.

To keep Stalwart's mail on another disk, mount the disk at `/var/lib/stalwart` instead of giving Stalwart a new path; no local rule is needed. See [Mail → Mail storage on another disk](../mail.md#mail-storage-on-another-disk).

## Why per-profile

A single global profile would either be too permissive (defeating the point) or too restrictive (denying legitimate operations the agent or panel needs). Per-process profiles let each component carry the narrowest possible policy.
