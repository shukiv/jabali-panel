# Platform — Monitoring

Three signal sources, complementary.

## 1. Audit log

`/jabali-admin/audit`. Append-only structured rows. Every privileged mutation lands here:

- Subject (user being acted on).
- Actor (operator / user / system that initiated).
- Source (UI / CLI / reconciler).
- Action (`domain.create`, `db.root.rotate`, `mailbox.passwd`, etc.).
- Target (resource id).
- Result (ok / fail + structured error code).

Use for: forensic "who did what when" investigation; compliance.

CLI:

```bash
jabali audit query                     # the last 50 events
jabali audit query --limit 500         # up to 1000
jabali audit query --q db.             # search action, target, actor kind and result
jabali audit verify                    # check the hash chain for tampering
```

## 2. Notifications

`/jabali-admin/notifications`. Event sources fan out to the configured channels: email, Slack, Discord, Telegram, ntfy, webhook, SMS and Web Push (see [notifications.md](../notifications.md)).

Use for: human-attention-needed alerts in real time.

## 3. Health and server status

The panel has no `/metrics` endpoint and no Prometheus exporter. An external
monitor polls the endpoints in [Health Monitor](health-monitor.md):
`GET /health` and `GET /health/agent` (no sign-in), and the automation
endpoints `GET /api/v1/automation/status` (scope `read:status`) and
`GET /api/v1/automation/server-status` (scope `read:metrics`).

Use for: uptime checks and fleet dashboards.

## Tail-the-logs alternative

For ad-hoc:

```bash
journalctl -u jabali-panel -u jabali-agent -u nginx -u jabali-stalwart -f
```

Structured JSON in stdout from panel-api and agent; `jq` away.

## What's *not* included

- **APM / tracing** — not yet. OpenTelemetry support is on the roadmap; the panel + agent currently emit log lines but not trace spans.
- **Continuous profiling** — not shipped.
- **eBPF observability** — not shipped. The Tetragon tripwires were removed.
