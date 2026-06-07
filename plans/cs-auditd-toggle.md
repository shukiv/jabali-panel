# CrowdSec auditd collection — admin opt-in toggle

**Status:** Blueprint queued (parked, not dispatchable)
**Owner:** dispatcher
**Related:** M39 (narrow auditd already installed), M27 (CrowdSec extensions), ADR-0061

## Why

M39 landed `auditd` + 11 narrow `jabali_susp_exec` rules feeding `/var/log/audit/audit.log`. The CrowdSec hub collection `crowdsecurity/auditd` parses the same log and ships scenarios for post-exploitation behavior CrowdSec catches end-to-end:

- Suspicious `rm` / `pkill` patterns
- Execution from network paths (`/tmp`, `/dev/shm`, `/var/tmp`)
- `base64 | sh`-style obfuscated exec
- SUID crash patterns
- Unexpected `chmod +s`, `setcap`

We have the parser-side data (M39 audit rules). We don't yet wire CrowdSec to it. This blueprint closes that gap behind an admin opt-in toggle because the collection is **known noisy** on busy hosts; default off, single click to enable on hardened/audited servers.

## Scope

- Single bool toggle in `/jabali-admin/security` → "Advanced server protection".
- `server_settings.crowdsec_auditd_enabled` (default `0`).
- Agent installs `crowdsecurity/auditd` collection + wires `/etc/crowdsec/acquis.d/audit.yaml` reading `/var/log/audit/audit.log` (label `audit`) on enable.
- Agent removes both on disable (purge with `--force`).
- Status read shows `enabled | collection_present | acquis_present | last_alert_ts | recent_decisions_count`.

NOT in scope:
- Per-scenario tuning UI (defer; admin can edit `/etc/crowdsec/scenarios.d/` by hand).
- M14 notification rewiring (auditd alerts already flow through existing `crowdsec_spike` channel via shared LAPI; no new event source).

## Files

### Schema (migration NNN_crowdsec_auditd_toggle)

```sql
ALTER TABLE server_settings
  ADD COLUMN crowdsec_auditd_enabled TINYINT(1) NOT NULL DEFAULT 0;
```

(Or add to `crowdsec_settings` row if M27 created that table; verify before writing migration.)

### panel-api (Go / Gin)

- `panel-api/internal/api/admin_security_crowdsec.go` — extend existing CrowdSec settings route group with:
  - `GET  /api/admin/security/crowdsec/auditd` → `{enabled, collection_present, acquis_present, last_alert_ts, recent_decisions_24h}`
  - `PUT  /api/admin/security/crowdsec/auditd` body `{enabled: bool}` → toggles DB + dispatches `security.crowdsec.auditd.apply` to agent
- Admin-only middleware (reuse `requireAdmin`).
- On PUT: `h.cfg.Reconcile.Apply("security.crowdsec.auditd")` so reconciler converges.

### panel-agent (Go / NDJSON UDS)

- `panel-agent/internal/commands/security_crowdsec_auditd.go` (NEW):
  - `apply(enabled bool) error`:
    - enable: `cscli collections install crowdsecurity/auditd --force` then write `/etc/crowdsec/acquis.d/audit.yaml`:
      ```yaml
      filenames:
        - /var/log/audit/audit.log
      labels:
        type: audit
      ```
      (atomic write tmp+rename, `root:crowdsec 0640`), then `systemctl reload crowdsec`.
    - disable: `cscli collections remove crowdsecurity/auditd --purge --force`, `rm /etc/crowdsec/acquis.d/audit.yaml`, `systemctl reload crowdsec`.
  - `status()` → introspect via `cscli collections list -o json` + `os.Stat` on acquis path + LAPI query for `last_alert_ts` (reuse existing CrowdSec status helper).

### Reconciler

- `panel-api/internal/reconciler/security_crowdsec_auditd_reconciler.go` (NEW):
  - Per-tick: compare DB `crowdsec_auditd_enabled` vs `status.collection_present`; on mismatch fire `security.crowdsec.auditd.apply`.
  - Idempotent (matches M34/M43 reconciler pattern).
  - Add `reconcileCrowdSecAuditd()` to `ReconcileAll()`.

### panel-ui (React / AntD)

- `panel-ui/src/shells/admin/security/AdminSecurityCrowdsecAuditd.tsx` (NEW) — Card with Switch + status badges (collection / acquis / alerts last 24h). Mirror `AdminSecurityAide.tsx` shape.
- `panel-ui/src/hooks/useSecurityCrowdsecAuditd.ts` (NEW) — query + mutation.
- Mount in `AdminSecurityCrowdsec.tsx` as a sub-section "Advanced — auditd integration" below the existing CrowdSec card.

### install.sh

- No change. M39 already installs auditd. CrowdSec is already installed. Toggle is runtime, not install-time.

## Validation

- Unit: repository `EnsureRow` round-trip; api handler ownership 403; agent apply enable→disable→enable idempotence.
- Build: `npm run build` (panel-ui), `go build ./...` (panel-api + panel-agent).
- Live smoke on 192.168.100.150:
  1. Toggle on → confirm `cscli collections list | grep auditd` shows it, `/etc/crowdsec/acquis.d/audit.yaml` exists `0640 root:crowdsec`, `systemctl reload crowdsec` clean.
  2. `sudo bash -c 'echo cmd | base64 | bash'` from non-root user (M39 rule fires) → confirm CrowdSec alert lands in `cscli alerts list`.
  3. Toggle off → confirm purge + acquis removed + reload clean.
  4. Reconciler self-heal: manually `cscli collections remove crowdsecurity/auditd --force` with DB enabled=1 → reconciler reinstalls within 1 tick.

## Risk / known issues

- **Noise:** auditd scenarios are documented as noisy on busy hosts. Ship with admin-only opt-in + an inline UI warning "May generate false positives — review alerts before extending decisions to bans".
- **Audit log rotation:** acquis must follow rotation. CrowdSec's filenames acquis uses `tail -F` semantics so logrotate `copytruncate` works; verify on first smoke.
- **Concurrent M14:** Existing `crowdsec_spike` event source counts ALL alerts. After enabling auditd, baseline spike threshold may need re-tuning. Document in runbook.

## Decision log

- 2026-06-07 — Recommended to user during CrowdSec collection audit; parked as future feature behind admin toggle. Default-off because of documented noise.
