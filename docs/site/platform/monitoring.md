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
jabali audit list --since 24h
jabali audit list --since 7d --action 'db.*'
jabali audit list --user <id>
```

## 2. Notifications

`/jabali-admin/notifications`. Event sources fan out to 6 channels (see [notifications.md](../notifications.md)).

Use for: human-attention-needed alerts in real time.

## 3. Metrics

`GET /metrics` (Prometheus). Buckets:

- HTTP: `panel_http_requests_total{path,method,status}`, `panel_http_request_duration_seconds{path}`.
- Reconciler: `panel_reconciler_ticks_total{result}`, `panel_reconciler_tick_duration_seconds`, per-converger tick counts.
- Agent: `agent_calls_total{action,result}`, `agent_call_duration_seconds{action}`.
- Notifications: `notifications_dispatched_total{channel,result}`, queue depth.
- Backups: `backup_run_duration_seconds{destination,kind}`, `backup_bytes_total{destination}`.
- SSL: `ssl_certs_expiring_soon`, `ssl_renewals_total{result}`.

Use for: time-series dashboards (Grafana) + alertmanager rules.

## Tail-the-logs alternative

For ad-hoc:

```bash
journalctl -u jabali-panel -u jabali-agent -u nginx -u jabali-stalwart -f
```

Structured JSON in stdout from panel-api and agent; `jq` away.

## What's *not* included

- **APM / tracing** — not yet. OpenTelemetry support is on the roadmap; the panel + agent currently emit log lines but not trace spans.
- **Continuous profiling** — not shipped.
- **eBPF observability** — not shipped. The Tetragon tripwires were removed in M39.
