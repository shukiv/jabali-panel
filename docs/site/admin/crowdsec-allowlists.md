# CrowdSec — Allowlists and Scenario Overrides

Security → CrowdSec → **Allowlists** and **Scenario Overrides** tabs (M27 extensions, ADRs 0061–0063).

## Allowlists

A persistent list of IPs or CIDR ranges that bypass every scenario. Use for:

- The operator's office or VPN exit (so a scripted operation never gets banned).
- Monitoring vendors that probe legitimately (UptimeRobot, Pingdom).
- Internal infrastructure that emits high request volumes.

Each allowlist entry contains: IP / CIDR, label, optional expiry, optional scenario restriction (allowlist applies only to a specific scenario; everything else still scans normally).

Allowlist entries survive CrowdSec restarts and `jabali update` runs — they are stored in the panel database, not in CrowdSec's own state file. The reconciler converges CrowdSec's allowlist config from the panel rows on each tick.

## Scenario overrides

Each scenario ships with defaults: severity, leakspeed (decay rate), capacity (threshold).

- **Severity** — informational, low, medium, high; influences default routing in [Notifications](./notifications-events.md).
- **Leakspeed** — the rate at which the bucket drains (typical: a few seconds to a minute).
- **Capacity** — events required to fill the bucket and trigger a decision.

The Scenario Overrides tab lets the operator tune these without editing `/etc/crowdsec/scenarios/*.yaml` by hand. Overrides are namespaced per scenario and reconciler-converged.

## Alert routing

`crowdsec.ban.spike` fires on an unusually large burst of new bans in a short window. It goes to the bell and to every enabled channel; see [Routing](./notifications-routing.md).

## Console-pushed allowlists

If the panel is enrolled with the central console, allowlists may be pushed from the console down to the host. Console allowlists are read-only on this page; remove them from the console UI.

## Operator workflow

1. After a false positive, click **Add to allowlist** on the affected decision row in [Decisions](./crowdsec-decisions.md).
2. After repeated overzealous bans on a specific scenario, tune the scenario's capacity here.
3. Quarterly: review allowlists, drop any that are stale.
