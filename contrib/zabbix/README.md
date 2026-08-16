# Zabbix template for Jabali (GH #1110)

`jabali_panel_template.yaml` — importable into **Zabbix 7.x** (validated by
importing into a real Zabbix 7.0 server via `configuration.import`).

## What it monitors

| Check | Mechanism | Agent needed |
|---|---|---|
| Panel health (`/health`) | HTTP, expects `status: ok` | none |
| Root agent health (`/health/agent`) | HTTP, expects `status: ok` | none |
| Jabali service set (panel, agent, nginx, mariadb, stalwart, pdns, redis, crowdsec, vsftpd, postgres) | systemd discovery — optional modules appear only when installed | Zabbix agent 2 |
| SMTP reachability (port 25) | simple check | none |
| Panel TLS certificate (+ days-left, warns < 14) | `web.certificate.get` | Zabbix agent 2 |

Triggers: panel/agent unhealthy (HIGH), any discovered Jabali unit not
`active` (HIGH), SMTP silent 5 min (AVERAGE — ignore with mail module off),
cert expiring (WARNING).

## Import

Zabbix UI → *Data collection → Templates → Import* → pick the YAML, or via
API `configuration.import` with `format: yaml`.

## Host setup

1. Link the **Jabali Panel** template to the host.
2. Install **Zabbix agent 2** on the Jabali server for the systemd +
   certificate items (the health checks work without it).
3. Macros (override per host as needed):
   - `{$JABALI.PANEL.PORT}` — default `8443`
   - `{$JABALI.CERT.HOST}` — set to the panel FQDN for a proper
     certificate-chain check (defaults to the host connection address)
   - `{$JABALI.UNITS.MATCHES}` — regex of units treated as Jabali stack

## Self-signed panel certificates

The health items verify TLS (secure default). On a panel still serving a
self-signed certificate (fresh install before Let's Encrypt), the two HTTP
items will fail — either get the panel a real certificate (the normal
path), or per host open each health item and untick *SSL verify peer* /
*SSL verify host*. Do not disable verification fleet-wide in the template.

## Not covered yet

Mail queue depth, per-site status, and backup freshness need a metrics
endpoint on the panel side — tracked in GH #1110. This template is the
no-code-changes first slice.
