# Security (Admin)

`/jabali-admin/security`. Parent page for the security tabs (M26).

## Tabs

| Tab | What it covers |
|---|---|
| **CrowdSec** | Decisions, scenarios, allowlists, console enrolment, test-IP card. See [CrowdSec](./crowdsec-decisions.md). |
| **AppSec** | The CrowdSec AppSec WAF replacing the removed ModSecurity stack (M27). See [AppSec](./appsec.md). |
| **AppArmor** | Per-profile status (enforce / complain / disabled) for shipped profiles. See [AppArmor](./apparmor.md). |
| **Snuffleupagus** | PHP runtime hardening rule packs and per-app exception files. See [Snuffleupagus](./snuffleupagus.md). |
| **AIDE** | Host-integrity daily scan results, manual scan trigger. See [AIDE](./aide.md). |
| **Malware** | LMD native scanner, YARA-X rules, LMD opt-in real-time monitor (M33 + M33.2). See [Malware](./malware.md). |
| **UFW** | Port baseline only (IP decisions live in CrowdSec since M43). See [UFW](./ufw-baseline.md). |
| **Egress** | Per-user nftables + cgroup v2 vmap egress firewall (M34). See [Egress](./egress.md). |

## Quick status at the top

A header strip summarises:

- CrowdSec: decisions in the last hour, alerts in the last 24 h.
- AppSec: blocked requests in the last hour.
- AppArmor: number of profiles in `enforce` vs `complain`.
- AIDE: time since last clean scan; current diff count.
- Malware: open file-hit count.

Each link drills into the relevant tab.

## Operator workflow

1. **Daily** — open this page, glance at the header strip, drill into any anomaly.
2. **Weekly** — review CrowdSec decisions for false positives; add legitimate sources to allowlist.
3. **After any incident** — review the audit log for the time window and correlate with the security tabs.

## What is not on this page

- **Audit log** — separate page; see [Audit Log](./audit-log.md).
- **Mail-specific security (DKIM, SPF, DMARC)** — see [Mail Deliverability](./mail-deliverability.md).
- **TLS certificates** — see [SSL Manager](./ssl-manager.md).
