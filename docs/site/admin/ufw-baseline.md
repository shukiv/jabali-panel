# UFW — Port Baseline

Security → UFW. The simple port-open / port-close baseline. UFW has only this role; IP-trust decisions live in [CrowdSec Decisions](./crowdsec-decisions.md).

## Default rules

The installer applies:

| Port | Protocol | Purpose |
|---|---|---|
| 22 | tcp | SSH and SFTP |
| 25 | tcp | SMTP MTA |
| 53 | tcp+udp | PowerDNS authoritative |
| 80 | tcp | HTTP (and ACME HTTP-01) |
| 443 | tcp | HTTPS |
| 465 | tcp | SMTP submission TLS |
| 587 | tcp | SMTP submission STARTTLS |
| 993 | tcp | IMAPS |
| 995 | tcp | POP3S (only if POP3 enabled in Stalwart) |

Default policy: `deny incoming`, `allow outgoing` (outbound per-user is constrained by [Egress](./egress.md)).

## Page surface

- The current ruleset rendered as a sortable table.
- Per-row **Disable** to take a port closed.
- **Add port** form for non-standard ports (additional SSH, alternate web port).
- A warning panel listing any rule of the form `from <ip>` — the move of IP decisions to CrowdSec replaced these with CrowdSec decisions; the warning surfaces any that escaped migration.

## Migrating from old `ufw deny`

```bash
jabali ufw migrate-ip-bans
```

Walks `ufw status numbered`, lifts every `from <ip>` deny rule into a CrowdSec decision with `reason=migrated-from-ufw`, then deletes the UFW rule. Idempotent.

## Why this split

UFW is excellent at static port baselines, and the operator-readable syntax stays useful for "is `:25` open?" debugging. UFW is a poor fit for the high-cardinality, short-lived per-IP block decisions CrowdSec produces (the ruleset becomes unwieldy at >1000 entries). Splitting the responsibilities keeps each tool playing to its strengths.

## IPv6

The same port rules apply over IPv6. CrowdSec scenarios match both address families uniformly.

## CLI

Standard UFW commands work:

```bash
ufw status numbered
ufw allow 8443/tcp
ufw delete <rule-number>
```

…but for IP-blocking always use `cscli` instead of `ufw deny from`.
