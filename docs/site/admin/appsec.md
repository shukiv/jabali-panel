# AppSec WAF

Security → AppSec. The Web Application Firewall layer that inspects HTTP requests for malicious payloads. M27, ADR-0060.

## Architecture

The CrowdSec AppSec component runs an inline filter in nginx via the `appsec-block` bouncer (`/etc/nginx/conf.d/jabali-appsec.conf`). Each request is sent to the AppSec inspector over a fast local socket; if the inspector returns "block", nginx returns `403`; otherwise the request continues.

This replaces the previously-shipped ModSecurity stack (libmodsecurity + nginx connector + OWASP CRS), which was removed entirely in M27. The installer's `cleanup_modsecurity` step purges leftover ModSecurity packages on every install to prevent broken nginx reloads.

## Rule packs

Rule packs come from `hub.crowdsec.net/author/crowdsecurity`. The default install enables:

- `crowdsecurity/vpatch` — virtual patches for known CVEs.
- `crowdsecurity/base-config` — generic web exploit signatures.
- `crowdsecurity/appsec-virtual-patching` — common LFI / RCE / XSS patterns.

The install path is **flat** (`/etc/crowdsec/appsec-rules/`). An earlier bug used a `crowdsecurity/` subdirectory and re-purged-and-reinstalled 170 vpatch rules on every `jabali update`; the fix shipped in PR #69.

## Operator controls

- **Policy mode** — `block` (default) or `log-only`. Log-only is useful when adopting a new rule pack to gauge false-positive rate before enforcing.
- **Per-rule exception** — exclude a specific rule for a specific URL path or for a specific user (drop the rule from the inspector configuration).
- **Per-domain exception** — exclude all AppSec checks for a domain (rarely useful; documented for completeness).

## Bot detection (CrowdSec 1.8)

A separate **bot-detection challenge** is available, **off by default**
(per-server). Enable it under Security → AppSec. It is layered and scoped so it
never silently challenges traffic on a domain the operator didn't choose:

- **Per-server** master switch (default OFF).
- **Per-domain opt-in** (`scope=selected`) — challenge only chosen domains.
- **Per-domain opt-out** — exclude a specific domain.
- **Tenant self-service** — a tenant can toggle it on their **own** domains.

Oversized request bodies are inspected up to the AppSec limit and then passed
(the `partial` body-size action) rather than dropped, so large uploads on
`/api/v1/*` are not blocked before the panel's own allowlist runs.

## Inspecting blocks

The AppSec inspector logs every blocking decision with the matched rule and the request body excerpt. Surface them in [Email Logs](./email-logs.md) under the AppSec filter, or tail directly:

```bash
journalctl -u crowdsec-appsec -f
```

## Update cadence

Rule packs auto-update via `cscli hub update && cscli hub upgrade`, run by the daily CrowdSec hub timer. The update never replaces operator exceptions; those live in panel-managed override files outside the hub-controlled tree.

## Related

- [CrowdSec Decisions](./crowdsec-decisions.md) — IP-layer; AppSec is request-content layer.
- [Removed Features](../removed-features.md) — context for the ModSecurity removal.
