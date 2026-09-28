# Platform — Health Monitor

The endpoints an external monitor can poll, and what each one proves.

## Endpoints

| Endpoint | Auth | Returns |
|---|---|---|
| `GET /health` | none | `200 {"status":"ok","version":"<build>"}` while the panel process is running. It does not check the database, the agent or any other service. |
| `GET /health/agent` | none | `200 {"status":"ok","agent":{"version":"…","go_version":"…","uptime_seconds":N,"started_at":"…"}}` when the agent answers within 2 seconds. Otherwise an error status, such as `503` when the agent is unreachable or `504` when it does not answer in time. |
| `GET /api/v1/automation/status` | Automation token, scope `read:status` | Full server metrics for a fleet monitor (see below). |
| `GET /api/v1/automation/server-status` | Automation token, scope `read:metrics` | A smaller, normalized CPU and host view. |

The panel serves all four on `https://<panel-hostname>:8443`. Port 443 does not serve `/health`. The two automation endpoints are also on port 443 when the operator turns on public access for the Automation API; see [Automation API](../admin/automation-api.md).

The panel has no `/metrics` endpoint and no Prometheus exporter.

## Fleet metrics: `/api/v1/automation/status`

For a multi-server manager that polls each Jabali server without a panel session
(GH #308 / JAB-75). It is authenticated with an **automation token** (HMAC, with a replay check)
that carries the `read:status` scope. Mint one with:

```
jabali automation-token mint <name> --scope read:status
```

The endpoint returns the same collectors as the admin Server Status page. The panel asks the agent
for them in parallel and caches the result for 5 seconds, so frequent polling does not
load the agent:

```json
{
  "healthy": true,
  "time": "2026-07-08T20:10:00Z",
  "version": "<panel build>",
  "commit": "<full commit SHA>",
  "build_time": "<RFC 3339 link time>",
  "system":   { "hostname": "...", "uptime_seconds": N, "load_avg": [..],
                "cpu_count": N, "mem_total_kb": N, "mem_used_kb": N,
                "swap_total_kb": N, "partitions": [{ "mount_point": "/",
                "total_bytes": N, "used_bytes": N, "free_bytes": N }] },
  "services": { "services": [ { "unit": "...", "active": "...", "sub": "...",
                "load_state": "...", "unit_file_state": "...", "uptime_seconds": N } ] },
  "service_health": [ { "name": "web", "unit": "nginx.service", "status": "healthy",
                "reason": "running", "uptime_seconds": N, "last_checked": "..." } ],
  "cpu":      { ... live CPU usage ... },
  "net":      { ... WAN throughput and packet loss ... },
  "errors":   { "net": "<error>" }   // only present when a collector failed
}
```

- `healthy` is `true` when every collector answered. A failed collector appears in
  `errors`, and the endpoint still returns HTTP 200 with the data it collected.
- `service_health` gives each service a stable name (`web`, `database`, `cache`, `mail`,
  `webmail`, `identity`, `dns`, `docker`, `panel`, `agent`, `ssh`) and one of `healthy`,
  `degraded` (starting or stopping), `failed` or `stopped`. A service that is not installed
  on the server is left out.
- The payload has metrics only: no credentials, no tokens and no per-tenant data.

## Use with an external monitor

For UptimeRobot, Pingdom or a self-hosted Uptime Kuma:

- Poll `https://<panel-hostname>:8443/health` with no credentials. Expect HTTP 200 and a body that contains `"status":"ok"`. This proves only that the panel process is running.
- Poll `https://<panel-hostname>:8443/health/agent` as well to know that the agent answers.
- To watch the services themselves, poll `/api/v1/automation/status` with an automation token and alert on `healthy` or on `service_health`.

## Notifications integration

For alerts inside the panel, the `service.down` event checks the core units once a minute; see [Alerts when a unit goes down](../admin/services.md#alerts-when-a-unit-goes-down). An external monitor is still needed for the case where the panel itself is down and cannot send the alert.
