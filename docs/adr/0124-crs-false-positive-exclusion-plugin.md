# ADR-0124: jabali CRS "before" plugin for targeted AppSec false-positive exclusions

**Date**: 2026-06-14
**Status**: accepted
**Deciders**: shukiv
**Relates to**: ADR-0102 (panel-API AppSec allowlist), ADR-0060/0063 (CrowdSec)

## Context

CrowdSec AppSec runs the OWASP CRS. Legitimate app traffic occasionally
trips a CRS detection rule and accumulates enough anomaly score for
949110 to block → a 4h ban on the operator's own users. First instance:
a logged-in WordPress admin search on a custom post type issues
`/wp-admin/edit.php?...&_wp_http_referer=<double-URL-encoded URL>`; the
nested URL-inside-a-URL trips CRS **933120** (PHP injection) → score 10
≥ threshold 5 → 949110 bans. (Rule 901340, named in first triage, is the
non-blocking body-inspection enabler, not the banner — verify the real
rule before excluding.)

We need a way to neutralize a specific FP that is:
- **surgical** — never disables a rule globally or allow-lists a path
  (ADR-0102 already allow-lists the panel API; web vhosts keep full WAF);
- **durable** — survives both `jabali update` and crowdsec hub updates;
- **single-sourced** — not a hand-placed box file.

## Decision

Ship a jabali-owned CRS "before" plugin file at
`/var/lib/crowdsec/data/crs-plugins/jabali/jabali-before.conf`.
`crowdsecurity/crs` globs `crs-plugins/*/*-before.conf` (via its
`seclang_files_rules`) and runs them **before** the CRS detection +
anomaly-scoring rules — the only place a `ctl:ruleRemoveTargetById` takes
effect against 933/949. The `jabali/` subdir is not hub-managed, so it is
not clobbered on hub update; `jabali appsec render-config` (run by
install.sh and every `jabali update`, which then reloads crowdsec) is the
single writer. The body is single-sourced in `internal/appseccfg`
(`CRSPluginBefore`).

Exclusions MUST be as narrow as the evidence:
`ctl:ruleRemoveTargetById=<rule>;ARGS:<arg>` scoped by `REQUEST_URI
@beginsWith <path>` — drop one rule's inspection of one argument under
one path. Never `ctl:ruleRemoveById` (kills the rule everywhere) and
never a path SetRemediation("allow") (kills the whole WAF for the path).

Reserved jabali CRS-plugin SecRule id range: **9,599,000–9,599,999**.
First rule (9599100) excludes 933120 for `ARGS:_wp_http_referer` under
`/wp-admin/`.

## Alternatives Considered

- **`ctl:ruleRemoveById=933120` on `/wp-admin/`** — removes PHP-injection
  detection for the whole admin area. Too broad; real attacks via other
  admin args would pass. Rejected.
- **Path allow-list (on_match SetRemediation "allow")** like ADR-0102 —
  disables ALL WAF rules for `/wp-admin/`. Far too broad. Rejected.
- **Activate the upstream `crs-exclusion-plugin-wordpress`** — installed
  but a data-fetcher only, disabled by default, and even enabled it
  exempts 921180/200002, not 933120 on this arg. Doesn't fix it.
- **Disable 901340** (first brief) — 901340 is `pass` (body-inspection
  enabler); removing it disables body inspection (a WAF hole) and does
  not stop the ban. Wrong rule.

## Consequences

### Positive
- FP gone for legit WP admins; WAF otherwise fully intact (verified on
  10.0.3.14: FP→allow, same payload other-arg/other-path→ban, real
  attack in the arg→still ban).
- Reusable, bounded mechanism for future CRS FPs (add sibling
  `ctl:ruleRemoveTargetById` lines in the reserved id range).

### Negative / Risks
- A genuinely malicious value placed in `_wp_http_referer` under
  `/wp-admin/` is no longer inspected by 933120 specifically — mitigated
  because every other CRS rule (incl. RCE/SQLi) still inspects it
  (confirmed: a `system()` payload in that arg still bans).
- Couples to the CRS plugin glob path; a crowdsec packaging change could
  move it. Mitigated by the render-config rewrite + reload on every
  update surfacing a load error.

## Amendment — operator exclusions split to a second file (GH #1655, 2026-09-11)

JAB-227 added operator-managed exclusions (`jabali appsec exclusion add`,
DB-backed) and originally appended them to the SAME `jabali-before.conf`
this ADR describes. That combined file has two writers: `render-config`
(built-ins **+** operator exclusions, from the DB) and the agent's
boot-time `ApplyAppSecBeforePlugin` (built-ins **only** — the agent has no
DB access). Once an operator added an exclusion the two writers diverged,
and every agent restart (`jabali update`, crash, reboot, manual) rewrote
the file to built-ins-only, **dropping every operator exclusion** until the
next `render-config` — silently re-banning the operator's users. This was
the likely root of "the exclusions I added keep coming back" on GH #1641.

Fix: the operator section now renders to a **separate** file,
`crs-plugins/jabali/jabali-operator-before.conf`
(`CRSPluginOperatorBeforePath`), written only by `render-config` and never
touched by the agent. Both files match the same
`crs-plugins/*/*-before.conf` glob and load before the CRS detection
rules; order between them is irrelevant (operator rules are self-contained
`ctl:ruleRemoveById` in phase 1). `jabali-before.conf` is now written
byte-identical by both writers, so they can no longer clobber each other.
When the last exclusion is removed the operator file is deleted (a stale
file would keep a since-removed exclusion live); a transient DB outage
leaves it untouched rather than dropping live exclusions.

Reload (GH #1653): `render-config` gained an opt-in `--reload` flag
(best-effort `systemctl reload crowdsec` on a real diff). `exclusion
add`/`rm` now print `render-config --reconcile --reload` so a manual
change takes effect in one step. install.sh leaves the flag **off** and
keeps its single deferred reload at the end of crowdsec setup, so an
update never reloads crowdsec mid-run (the GH discussion #109 fresh-install
ordering scar).

The operator id range stays **9,597,000–9,597,999**, disjoint from the
built-in 9,599,xxx range, so the two files never collide on SecRule ids.
