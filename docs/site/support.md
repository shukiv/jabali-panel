# Support

`/jabali-admin/support`. M29.

Uploads a redacted, encrypted diagnostic bundle for the Jabali maintainers, without leaking secrets or end-user data.

## What the bundle contains

Collected verbatim by `panel-agent/internal/diagnostic/diagnostic.go` (hard-coded list — no operator-tunable surface, so a malicious request cannot widen the scope):

| Entry | Source |
|---|---|
| `00-uname.txt` | `uname -a` |
| `01-os-release.txt` | `/etc/os-release` |
| `02-uptime.txt` | `uptime` |
| `03-free.txt` | `free -h` |
| `04-df.txt` | `df -h` |
| `05-git-head.txt` | panel repo `git rev-parse HEAD` |
| `06-git-status.txt` | panel repo `git status --porcelain` |
| `07-ss-tnlp.txt` | `ss -tnlp` (listening sockets + owning processes) |
| `08-iptables-input.txt` | `iptables -L INPUT -n` |
| `09-dpkg-list.txt` | installed package versions |
| `10-letsencrypt.log` | `tail -n 500 /var/log/letsencrypt/letsencrypt.log` |
| `svc/<unit>.is-active.txt` | per-unit `systemctl is-active` |
| `svc/<unit>.status.txt` | per-unit `systemctl status --no-pager` |
| `svc/<unit>.journal.txt` | per-unit `journalctl -n 200 --no-pager` |

Services included (`servicesToCollect`):

- `jabali-panel.service`, `jabali-agent.service`
- `jabali-stalwart.service`, `jabali-webmail.service`, `jabali-kratos.service`
- `pdns.service`, `pdns-recursor.service`
- `mariadb.service`, `redis-server.service`, `nginx.service`
- `certbot.service`, `certbot.timer`

## Mandatory redaction

Every collected file passes through the redactor (`panel-agent/internal/diagnostic/redact.go`) before it lands in the tar. The redactor strips:

- IPv4 / IPv6 addresses outside RFC1918 down to a `/24` (or `/64`) prefix.
- Email addresses to `<redacted-email>`.
- Bearer tokens, API tokens, passwords, database connection strings.

Redactor cannot be disabled from the UI. The `RedactionCount` field on the bundle reports how many substitutions were made.

## Encryption

The agent builds the tar in memory and encrypts it before it leaves the host. It uses the [enclosed](https://github.com/CorentinTh/enclosed) note format, so the server stores only ciphertext. To decrypt, you need both the link (its `#` fragment carries the key) and a separate password.

## Delivery

**Send Diagnostic Report** uploads the encrypted bundle to `https://enclosed.jabali-panel.com`, a note server the Jabali project runs. The upload starts as soon as the dialog opens. The note expires after 7 days.

The dialog shows the link and the password. **Send via email** opens your mail client with both filled in, addressed to `webmaster@jabali-panel.com`. Nothing is emailed until you send that message.

If the agent has a support-claim service configured (`JABALI_CLAIM_URL`), the dialog also shows a short claim code (`JAB-XXXXXXXX`). The code is safe to post anywhere, even in a public issue. The claim service holds the link and the password for support.

The host must reach `enclosed.jabali-panel.com` over HTTPS. If it cannot, the upload fails and you get no bundle. If your policy forbids diagnostic data leaving the host, do not open the dialog.

## What the bundle does *not* contain

To keep this list current with the actual collector, the following are **not** collected — request a feature add if you need them:

- `nginx -T` (full rendered config)
- `jabali repair --diagnose` output
- AIDE last-diff report
- DB schema dump or row counts
- Mail queue depth
- CrowdSec decisions list
- AppSec block log

The intent is to keep the bundle small and to limit the surface to host-level state needed for incident triage. Operator-specific deep dives (a full `nginx -T`, a `mysqldump --no-data`) are run on demand against a live host once the maintainers and operator are in contact.

## CLI

```bash
jabali system diagnostic          # uploads the bundle; prints the link and the password
jabali system diagnostic --json   # the raw result, for scripts
```
