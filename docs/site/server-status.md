# Server Status

M31. `/jabali-admin/server-status`.

Single page, 5-second polling, errgroup-aggregated.

## What's shown

- **Services card**: jabali-panel, jabali-agent, jabali-kratos, nginx, mariadb, redis-server and ssh, plus jabali-stalwart, jabali-webmail, pdns, postgresql and docker when their module is on. Each row shows the state and the Start / Restart / Reload / Stop / Enable / Disable buttons. See [Services](./admin/services.md).
- **Host vitals**: CPU%, load avg, RAM (used / free / cache), disk used per mount, network in/out per interface.
- **Queues card** (placeholder — defers to M31.1): mail queue depth, backup queue depth, reconciler tick lag.
- **Recent panel-api requests**: top 10 by latency.

## Health badge (JAB-373)

The admin header carries a lightweight **Health** badge backed by a cheap
`?view=health` projection of Server Status (single-flight + short TTL so the
badge poll can't stampede the box). It gives an at-a-glance green / degraded
signal without opening the full Server Status page.

## Per-service controls

Each card has start / stop / restart buttons. Disabled by default — flip the **off-toggle** in Server Settings → General to enable for admins who want to react from the UI instead of SSH.

When enabled, clicking restart fires `systemctl restart <unit>` via the agent. Audited.

## Why polling, not WebSocket

Polling at 5 s + errgroup aggregation kept the implementation simple and the page reliable behind every reverse-proxy / corporate-firewall combination we hit. WebSocket was considered, rejected for now (the page doesn't need ms-scale updates).

## Live-verified

Smoke test passed on 192.168.100.150. The card surface is the system's vitals view of record.

## Related

- [security.md](./security.md) for CrowdSec console.
- [notifications.md](./notifications.md) — the `service.down` event sends a notification when a service goes down without your having the dashboard open.
- [updates.md](./updates.md) for `jabali update` (it ties into Server Status because mid-update is the most common reason a service briefly disappears).
