# CrowdSec — Decisions

Security → CrowdSec → **Decisions**. Live and historic list of every BAN, CAPTCHA, and ALLOWLIST decision currently in force on the panel host.

## Columns

- IP (IPv4 or IPv6)
- Decision type (`ban`, `captcha`, `allowlist`)
- Scenario that triggered the decision (e.g. `crowdsecurity/ssh-bf`, `crowdsecurity/http-probing`)
- Origin (`local`, `console`, `manual`)
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

- `cs-nginx-bouncer` — drops requests from banned IPs at the nginx layer; presents the configured CAPTCHA HTML for `captcha` decisions.
- `cs-firewall-bouncer` — drops packets in nftables for non-HTTP traffic.
- `cs-stalwart-bouncer` — rejects IMAP / SMTP connections.

## Console sync

If the admin has enrolled with the central CrowdSec console (Server Settings → Security), decisions sync bidirectionally: locally-produced decisions appear in the console, and console-pushed decisions (community blocklists) appear here marked `origin=console`.

## CLI

The standard `cscli` tool is also available on the host:

```bash
cscli decisions list
cscli decisions delete --id <id>
cscli decisions add --ip <ip> --duration 4h --reason "manual block"
```
