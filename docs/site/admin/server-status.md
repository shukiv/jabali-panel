# Server Status

`/jabali-admin/server-status`. M31. Live status of every watched service plus host vitals.

## What is rendered

Errgroup-aggregated polling every 5 seconds. Each card returns its own status; one slow card does not block the others.

### Services card

One row per watched unit: jabali-panel, jabali-agent, jabali-kratos, nginx, mariadb, redis-server and ssh always, plus jabali-stalwart, jabali-webmail, pdns, postgresql and docker when their module is on. See [Services](./services.md) for the full list and the rules for each unit.

Each row shows:

- The unit's state: active, inactive, failed, activating or deactivating. An inactive unit that is not enabled at boot shows a grey **idle** tag.
- **Start**, **Restart**, **Reload**, **Stop**, **Enable at boot** and **Disable at boot** buttons, depending on the unit's state.

### Host vitals

- CPU usage and 1-minute load average
- Memory: used / free / cache, swap usage
- Disk: per-mount used / free, including `/var/lib/mysql`, `/var/lib/stalwart`, `/home`
- Network: in / out per interface

### Queues card

Placeholder pending M31.1. Will surface mail queue depth, backup queue depth, reconciler tick lag, and notification dispatch lag.

### Recent panel requests

Top 10 panel-api requests in the last 60 seconds by latency. Drill-in shows the full route, status code, and request-id (correlate with `journalctl -u jabali-panel`).

## Service controls

The buttons are always on for admins. The agent runs each action and the panel logs it with the admin who ran it.

The panel refuses Stop and Disable on the units that serve the panel itself (jabali-panel, jabali-agent, jabali-kratos, mariadb, nginx, redis-server) with `403 self_destruct_blocked`. The card does not show those buttons for those units. See [Units you cannot stop](./services.md#units-you-cannot-stop).

## Polling, not WebSocket

The page polls at 5 seconds via a single endpoint that returns the aggregated state JSON. WebSocket was considered and rejected: polling works behind every reverse-proxy and corporate-firewall combination tested, and the page does not require millisecond-scale updates.

## Live-verified

The page surface was live-verified on 192.168.100.150 as the system's primary vitals view.

## Related

- [Services](./services.md) — the watched units and what each button does.
- [Notifications](./notifications-events.md) — the `service.down` event sends a notification when a watched unit goes down while no one is watching the page. It checks its own list of units; see [Alerts when a unit goes down](./services.md#alerts-when-a-unit-goes-down).
- [Updates](./server-updates.md) — running `jabali update` is the most common reason a service briefly disappears from this page.
