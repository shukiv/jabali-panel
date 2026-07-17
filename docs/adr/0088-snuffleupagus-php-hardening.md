# ADR-0088 — Snuffleupagus for PHP RCE Hardening (M41)

**Status:** Proposed
**Date:** 2026-05-03
**Supersedes:** —
**Superseded-by:** —
**Related:** ADR-0086 (M40 AppArmor — parked), ADR-0031 (M9.6 PHP extensions),
ADR-0050 (M25 unix sockets), ADR-0056 (M14 notifications)

## Context

Customer PHP code is the dominant RCE vector in shared hosting. The defaults
in upstream PHP allow patterns that abuse-prone CMS plugins routinely
exploit: dynamic-eval of user input, unserialize of cookies, shell-out from
URL parameters, file-upload paths writing arbitrary `.php` under document
roots. Out-of-the-box PHP has no defense; every shared-host panel that
takes this seriously layers something on top — Imunify360 calls its layer
"Proactive Defense", cPanel ships "ImunifyAV+", DirectAdmin uses mod_security
with a custom rule set.

We previously planned to lean on AppArmor (M40, ADR-0086) as defense-in-
depth. Field experience showed two problems:

1. AppArmor confines our **daemons**, not the customer-PHP attack surface.
   The 95% threat on a hosting box is customer code calling dynamic eval
   on tainted input. AppArmor on the panel does nothing about that.
2. The M40 operator surface (Security → AppArmor tab, complain↔enforce
   flips, soak windows, denial triage) is exactly the "let the operator
   tune security" model Jabali rejects. Jabali targets hosting operators
   who want a turnkey panel; per-profile flip-mature decisions are not
   their job.

M40 is parked. M41 ships **Snuffleupagus** — a PHP-Zend extension that
hooks the same dangerous-function entrypoints attackers target — as the
turnkey replacement for the customer-PHP threat surface.

## Decision

Adopt Snuffleupagus as the canonical PHP-hardening module across every
PHP minor version Jabali ships. Build per minor. One server-wide policy
maintained by Jabali (not per-domain, not per-customer). Three modes
operator-selectable: `off`, `simulation`, `enforce`. Default on first
install: `simulation` for 7-day soak, then operator promotes to
`enforce`. Incidents flow into the existing M14 notification dispatcher.
The PHP-CLI bypass — customer with shell access running php with a
custom .ini file to evade our `sp.configuration_file` — is closed via
a `jabali-php` wrapper, plus pin in the per-user nspawn image, plus
audit-detect alerts on any unwrapped php-cli invocation.

## Consequences

### Positive

- Closes the dominant shared-hosting RCE vector with a curated, Jabali-
  maintained rule set. Operator does zero rule editing.
- Composes with M9.6 (per-PHP-version extension build) and M25 (per-user
  pool socket isolation): we already iterate every PHP minor; adding
  one more `.so` is incremental, not architectural.
- Composes with M14 (notifications): incidents become first-class events
  that route to the operator's chosen channel without a separate UI.
- The `simulation` mode lets operators see what would have been blocked
  before turning on enforcement — exactly the safe-rollout shape for a
  panel servicing multi-tenant production.
- The Jabali-maintained rule bundle becomes the operator-facing value
  prop: same as Imunify360's moat, but bundled.

### Negative

- Build pipeline grows by one extension per PHP minor. CI minute cost
  rises slightly.
- We own a curated rule set and inherit its maintenance burden. False-
  positives from legitimate CMS internals (WordPress cron, WooCommerce
  email shellouts, etc.) require ongoing triage.
- The PHP-CLI bypass mitigation requires a wrapper binary and an audit-
  detect rule; if a customer with shell access invokes the real
  `/usr/bin/php` with a custom .ini they CAN sidestep our config, but
  the audit alert fires within seconds.
- Snuffleupagus is upstream-maintained by a small team. If the project
  goes dormant we own a fork; the format is stable enough that this is
  recoverable, not catastrophic.

### Neutral

- M40 AppArmor stays parked — profiles remain in the tree but are not
  loaded, applied, or surfaced in the UI. Future revival is not blocked
  by M41.

## Decisions Recorded

1. **Build mode:** source build per PHP minor, NOT distro packages.
   Distro coverage is incomplete; building from upstream tag pinned by
   SHA256 gives us a single artefact pipeline regardless of OS or PHP
   version.
2. **Config scope:** server-wide. One pinned config file, applied to
   every per-user pool via `php_admin_value[sp.configuration_file]` in
   the M9.6 pool template. Rejected: per-domain config — too much
   operator surface for the customer benefit.
3. **Modes:** `off` / `simulation` / `enforce`. `simulation` is the
   first-install default for a 7-day soak. The state lives in the
   `snuffleupagus_state` singleton row; the reconciler renders the
   active rule file based on the row's mode.
4. **Rule ownership:** Jabali maintains the bundle in
   `install/snuffleupagus/rules/`. Customers cannot edit. Operators
   get a per-rule kill switch (`snuffleupagus_rule_overrides`) for the
   inevitable false-positive case where a specific tenant app collides
   with a specific rule. The bundle starts from upstream
   `config/example.rules` plus per-CMS overlays (WordPress, Drupal,
   Joomla, PrestaShop) and grows from incident triage.
5. **PHP-CLI bypass closure:** ship `/usr/local/bin/jabali-php` wrapper
   that always passes `-c /etc/jabali/snuffleupagus/cli.ini` (with
   `sp.configuration_file` pinned). Per-user nspawn images mount
   `/etc/jabali/snuffleupagus/` read-only. An audit-detect (auditd) rule
   flags any exec of `/usr/bin/php*` from a customer slice without our
   wrapper and fires `notifications.send` with severity high.
6. **Incident pipeline:** `sp.log_media=syslog`, `sp.log_facility=local6`.
   Agent tails `journalctl -t snuffleupagus -f`, INSERTs
   `snuffleupagus_incidents`, fires `notifications.send` on action≥block.
7. **Default mode on first install:** `simulation`. Operator promotes
   to `enforce` from the Security UI after the soak. We do NOT auto-
   promote — an unattended fresh install should not start blocking
   tenant code without operator intent.
8. **DB schema:** three tables — `snuffleupagus_state` (singleton row),
   `snuffleupagus_incidents` (append-only event log), and
   `snuffleupagus_rule_overrides` (admin per-rule kill list). Migration
   `000109`.
9. **AppArmor M40:** parked. Files remain at `install/apparmor/` but
   `install_apparmor` is gated behind a non-default flag. Future revival
   is a separate ADR.

## Wave Plan

- Wave A: ADR + plan + repo skeleton + per-PHP-version build pipeline.
  Mergeable additive — no behavior change.
- Wave B: rule bundle + DB schema + config render. Mergeable additive
  with `mode=off` default, no enforcement.
- Wave C: PHP-CLI wrapper + nspawn-image pin + audit-detect rule.
- Wave D: agent ingest + REST routes + UI Security card.
- Wave E: E2E + runbook + flip default mode to `simulation` on first
  install. This is the wave that changes user-visible behavior.

## References

- Snuffleupagus upstream: https://github.com/jvoisin/snuffleupagus
- Documentation: https://snuffleupagus.readthedocs.io/
- Imunify360 Proactive Defense (the comparison product):
  https://docs.imunify360.com/
- ADR-0031 (M9.6 PHP extension build pipeline)
- ADR-0086 (M40 AppArmor — parked, this ADR's predecessor in scope)
