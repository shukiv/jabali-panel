# Support

`/jabali-admin/support`. M29. Upload an encrypted diagnostic bundle for the Jabali maintainers, then email them the link.

## Bundle contents

Hard-coded in `panel-agent/internal/diagnostic/diagnostic.go` so a malicious request cannot widen the scope.

| Entry | Source |
|---|---|
| `00-uname.txt` | `uname -a` |
| `01-os-release.txt` | `/etc/os-release` |
| `02-uptime.txt` | `uptime` |
| `03-free.txt` | `free -h` |
| `04-df.txt` | `df -h` |
| `05-git-head.txt`, `06-git-status.txt` | panel repo HEAD + working-tree status |
| `07-ss-tnlp.txt` | listening sockets with owning processes |
| `08-iptables-input.txt` | INPUT chain dump |
| `09-dpkg-list.txt` | installed package versions |
| `10-letsencrypt.log` | last 500 lines of `/var/log/letsencrypt/letsencrypt.log` |

Plus per-service triple (`is-active`, `status`, `journalctl -n 200`) for:

- `jabali-panel`, `jabali-agent`
- `jabali-stalwart`, `jabali-webmail`, `jabali-kratos`
- `pdns`, `pdns-recursor`
- `mariadb`, `redis-server`, `nginx`
- `certbot.service`, `certbot.timer`

## Mandatory redaction

Every line passes through a redactor before tarring (`panel-agent/internal/diagnostic/redact.go`):

- Non-RFC1918 IPv4 / IPv6 truncated to `/24` / `/64`.
- Email addresses replaced with `<redacted-email>`.
- Bearer tokens, API tokens, passwords, DB connection strings stripped.

`Bundle.RedactionCount` reports how many substitutions occurred. The redactor cannot be disabled from the UI.

## Encryption

The agent builds the tar in memory and encrypts it before it leaves the host. It uses the [enclosed](https://github.com/CorentinTh/enclosed) note format, so the server stores only ciphertext. To decrypt, you need both the link (its `#` fragment carries the key) and a separate password.

## Delivery

**Send Diagnostic Report** uploads the encrypted bundle to `https://enclosed.jabali-panel.com`, a note server the Jabali project runs. The upload starts as soon as the dialog opens. The note expires after 7 days.

The dialog shows the link and the password. **Send via email** opens your mail client with both filled in, addressed to `webmaster@jabali-panel.com`. Nothing is emailed until you send that message.

If the agent has a support-claim service configured (`JABALI_CLAIM_URL`), the dialog also shows a short claim code (`JAB-XXXXXXXX`). The code is safe to post anywhere, even in a public issue. The claim service holds the link and the password for support.

The host must reach `enclosed.jabali-panel.com` over HTTPS. If it cannot, the upload fails and you get no bundle. If your policy forbids diagnostic data leaving the host, do not open the dialog.

## What is *not* in the bundle

The collector is intentionally narrow. The following are **not** included; request a feature add if your incident triage needs them:

- `nginx -T` (full rendered config)
- `jabali repair --diagnose` output
- AIDE last-diff report
- DB schema dump (`mysqldump --no-data`) or row counts
- Mail queue depth
- CrowdSec decisions list
- AppSec block log

The bundle is kept small; deep dives happen against a live host once the operator and maintainers are in contact.

## CLI

```bash
jabali system diagnostic          # uploads the bundle; prints the link and the password
jabali system diagnostic --json   # the raw result, for scripts
```
