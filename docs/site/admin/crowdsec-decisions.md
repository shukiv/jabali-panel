# CrowdSec — Decisions

Security → CrowdSec → **Decisions**. Live and historic list of every BAN, CAPTCHA, and ALLOWLIST decision currently in force on the panel host.

## Columns

- IP (IPv4 or IPv6)
- Decision type (`ban`, `captcha`, `allowlist`)
- Scenario that triggered the decision (e.g. `crowdsecurity/ssh-bf`, `crowdsecurity/http-probing`)
- Origin (`crowdsec`, `cscli`, `CAPI`, `cscli-import`; see [Sources](#sources))
- Duration remaining
- First seen, last seen

## Filters

- Decision type
- Scenario
- IP range (CIDR)
- Origin
- Free-text search

## Per-row actions

- **Extend duration** — add time to a current decision.
- **Remove** — `cscli decisions delete --id <id>` via the agent.
- **Add to allowlist** — promote the IP to a persistent allowlist entry (see [Allowlists](./crowdsec-allowlists.md)). The decision is removed in the same operation.

## How decisions arise

Decisions are produced by CrowdSec **scenarios**, which match patterns in log streams from the bouncers:

- `nginx` access log → HTTP probing, scanner detection, abusive paths.
- `sshd` journal → SSH bruteforce.
- `jabali-stalwart` journal → IMAP / SMTP authentication flood.
- `jabali-panel` journal → panel login flood.
- `crowdsec-appsec` log → WAF rule trips (M27).

Each scenario has a configurable severity, leakspeed (decay rate), and capacity (threshold). Modify them under [Per-Scenario Override](./crowdsec-allowlists.md) — same page, scenario-override tab.

## Bouncers

Decisions are enforced by per-service bouncers:

- `crowdsec-nginx-bouncer` — refuses HTTP requests from banned IPs at the nginx layer and presents the configured CAPTCHA page for `captcha` decisions.
- `crowdsec-firewall-bouncer` — drops packets from banned IPs in nftables, for every port, so mail (SMTP, IMAP, POP3) and SSH are covered too.

## Sources

Each decision has an origin: `crowdsec` (detected on this server), `cscli` (added by hand), `CAPI` (the CrowdSec community blocklist) or `cscli-import` (imported with `cscli decisions import`). The **Blocklists** tab counts active decisions per source. The extra blocklists the admin turns on (see [Firewall → Extra blocklists](../firewall.md#extra-blocklists)) are `cscli-import` decisions with the scenario `jabali-list:<list>`.

The panel is not connected to the CrowdSec console; console enrolment was removed (ADR-0113), so no decisions are synced with it.

## CLI

The standard `cscli` tool is also available on the host:

```bash
cscli decisions list
cscli decisions delete --id <id>
cscli decisions add --ip <ip> --duration 4h --reason "manual block"
```
