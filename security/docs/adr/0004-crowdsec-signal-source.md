# ADR-0004: CrowdSec as Signal Enrichment, Not Standalone Firewall

**Date**: 2026-04-02
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

CrowdSec provides community-sourced threat intelligence via its LAPI (Local API). It detects attack patterns (SSH brute-force, HTTP probing, SQL injection) and shares decisions across the CrowdSec network. Jabali Security needs to integrate this intelligence without duplicating firewall management or creating conflicting blocking rules.

The CrowdSec firewall bouncer (`crowdsec-firewall-bouncer-nftables`) independently manages nftables rules. Jabali Security also manages nftables rules for brute-force blocking. Running both creates conflicts and makes it hard to reason about which system blocked an IP and why.

## Decision

Use CrowdSec as a signal enrichment layer only. The jabali-security daemon polls the LAPI decision stream and feeds signals into its own unified scoring and blocking system. CrowdSec scenarios map to threat scores (ssh-bf=60, sqli=70, backdoors=80). Community signals (CAPI origin) get a +20 bonus. Known CrowdSec attackers get halved brute-force thresholds.

The firewall bouncer is installed as a separate defense layer but jabali-security does not depend on it for its own blocking decisions.

## Alternatives Considered

### Alternative 1: Let CrowdSec bouncer handle all IP blocking
- **Pros**: Simple, CrowdSec manages everything, no custom code
- **Cons**: No unified view of threats, no correlation with file scanning/WAF events, can't apply custom scoring logic
- **Why not**: Jabali Security needs a unified threat score across all signals (file scanning, brute-force, WAF, CrowdSec, threat intel feeds)

### Alternative 2: Replace CrowdSec entirely with custom detection
- **Pros**: Full control, no external dependency
- **Cons**: Loses community intelligence (millions of IPs), must reimplement SSH/HTTP attack detection
- **Why not**: CrowdSec's community network is too valuable to ignore; reinventing it is not practical

## Consequences

### Positive
- Unified threat scoring: CrowdSec signals are weighted alongside YARA findings, WAF events, brute-force detection, and threat intel feeds
- Community intelligence: known attackers from the CrowdSec network are flagged before they attack
- Brute-force enrichment: IPs flagged by CrowdSec get tighter thresholds (halved), catching attacks faster
### Negative
- Dependency on CrowdSec LAPI being available (graceful degradation: `auto` mode disables if LAPI is unreachable)
- Two systems managing nftables rules (CrowdSec bouncer + jabali-security firewall manager)
- CrowdSec LAPI uses port 8080 — other services (e.g., Stalwart HTTP/JMAP) must avoid this port

### Risks
- Port 8080 conflict with Stalwart mail server — mitigated by moving Stalwart's HTTP listener to a different port (panel-side fix)
