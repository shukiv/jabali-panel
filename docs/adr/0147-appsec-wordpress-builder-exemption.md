# ADR-0147: AppSec exemption for WordPress page-builder endpoints (scoped CRS rule drop)

**Date:** 2026-06-22
**Status:** Accepted — live-verified on 10.0.3.14 (revised after reporter feedback)
**Owner:** shuki
**Issue:** GH #404

## Context

CrowdSec AppSec (OWASP CRS) false-positive-bans legitimate WordPress admins. A
page builder (Elementor) saving a page POSTs arbitrary HTML/CSS/URLs inside
JSON, which is indistinguishable from injection at the WAF: CRS 942550 (JSON
SQLi) + 932370 (RCE) + 911100 (method) push the inbound anomaly score past 949110
→ AppSec issues a ~4h **ban on the editor's own IP** → every subsequent request
(save, `wp-json`, `admin-ajax`) 403s → admin locked out. Hit on `example-p.co.il`
during migration ops.

The endpoints: `/wp-json/elementor/*`, `/wp-admin/admin-ajax.php`,
`/wp-admin/post.php`. The issue proposed a blanket path-allow on these plus
`/wp-json/wp/v2/`.

## Decision

**Localize by rule + path, not by remediation.** Drop **only** the three false-
positive CRS rules (`911100`, `942550`, `932370`), and **only** on the three
builder / admin endpoints (`/wp-json/elementor/`, `/wp-admin/admin-ajax.php`,
`/wp-admin/post.php` — the paths the incident actually hit), via the existing jabali-owned CRS "before" plugin
(`CRSPluginBefore`, written to
`/var/lib/crowdsec/data/crs-plugins/jabali/jabali-before.conf`). Every other CRS
rule keeps inspecting those same paths.

```
SecRule REQUEST_URI "@rx ^/wp-json/elementor/"       "id:9599200,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"
SecRule REQUEST_URI "@rx ^/wp-admin/admin-ajax\.php" "id:9599201,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"
SecRule REQUEST_URI "@rx ^/wp-admin/post\.php"       "id:9599202,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"
```

This **supersedes** the first cut of this ADR, which added an `on_match` filter
applying `SetRemediation("allow")` (cookie-gated for `admin-ajax`/`post.php`).
That approach was rejected by the reporter (and contradicts jabali's own
`appseccfg.go` philosophy, "prefer narrow over path-allow"): a `SetRemediation
("allow")` exempts the **entire request** on those paths — it cancels the WHOLE
event, so XSS, LFI, RCE, and every other SQLi rule stopped inspecting the builder
endpoints too. The cookie gate narrowed *who* was exempt, not *what* was exempt.

The scoped drop is strictly better: it removes exactly the three rules that fire
on legitimate builder markup and nothing else, so the WAF still bans real attacks
on those same endpoints. Because the exclusion lives in the CRS before-plugin
(not the `on_match` block of `jabali-appsec.yaml`), it is also independent of the
panel-agent's geoblock re-render — the agent rewrites the yaml but never touches
the plugin file, so the exemption can no longer be dropped by a reconcile tick.
The flag `Opts.WordPressAllowlist` and its `on_match` block were removed; the
exclusion is unconditional, like the pre-existing 933120/`_wp_http_referer`
exclusion (id 9599100) it sits beside.

### Why `ruleRemoveById`-by-path, not `ruleRemoveTargetById`-by-arg

jabali's narrowest tool is `ctl:ruleRemoveTargetById=<rule>;ARGS:<arg>` (drop one
rule's inspection of one named argument) — that is how 9599100 handles the
`_wp_http_referer` FP. It does **not** fit here: a page builder POSTs arbitrary,
unpredictable argument names inside its JSON, so there is no stable arg to target.
Path-scoped `ruleRemoveById` for three specific rule IDs is the tightest scope
that actually stops the FP. A future, finer version would capture a real
Elementor save and target the specific data parameter(s); that is a follow-up,
not done here.

### Expr / engine notes (validated live, not assumed)

- CrowdSec AppSec runs Coraza, which honours `ctl:ruleRemoveById` from a
  before-plugin against later phase-2 detection rules — the same mechanism the
  9599100 `ruleRemoveTargetById` exclusion already relies on.
- `crowdsec -t` validates the config; a malformed plugin makes crowdsec refuse to
  start (WAF down). Always `crowdsec -t` + keep a backup before reload, and
  **restart** crowdsec (not just yaml re-read) so the plugin reloads.

## Consequences

- Live-verified on 10.0.3.14 against the inband endpoint (`:7422`) after
  `render-config` + `crowdsec -t` + restart:
  - **Positive / path-scoping (isolated 911100 via a disallowed method):**
    `200` allow on `/wp-admin/admin-ajax.php`, `/wp-admin/post.php`,
    `/wp-json/elementor/…`; **`403` deny** for the *same* request on `/` and
    `/wp-login.php`. Proves the three rules are dropped only on builder paths.
  - **Negative / ship gate (other attacks on the builder path):** XSS, LFI, and
    a UNION-based SQLi on `/wp-admin/admin-ajax.php` **all still `403`** (and
    issued a real 4h ban). Proves the exclusion is surgical, not the old blanket
    allow relocated.
  - A multi-rule RCE payload (`Invoke-Expression …`) on a builder path **still
    `403`** — dropping only 932370 doesn't clear the score when other 932-series
    rules also fire. Correct: "other RCE stays blocked."
- The rendered `jabali-appsec.yaml` no longer contains any WordPress path-allow
  or `wordpress_logged_in_` cookie filter; that surface moved entirely into the
  before-plugin.
- **Residual:** the three dropped rules no longer inspect the builder paths even
  for unauthenticated requests, so an attacker who can reach `admin-ajax.php`
  unauthenticated has those three (and only those three) checks lifted there. The
  far larger remainder of CRS still applies, which is the inverse of the rejected
  blanket allow.

- `/wp-json/wp/v2/` (public WP REST) is **deliberately NOT excluded.** The QA
  correction's regex included it, and it was briefly shipped, but automated
  security review flagged it (MEDIUM): `^/wp-json/wp/v2/` lifts the three rules
  across the entire public read+write REST surface for all methods. There is no
  captured trace showing Elementor trips an FP on wp/v2 (the incident was on
  elementor/* + admin-ajax + post.php), so per "narrow as the evidence" it stays
  fully inspected. If a real wp/v2 FP is captured later, re-add it scoped by both
  endpoint (`(pages|posts|media)`) and write method (POST/PUT), not the bare
  prefix.

## Acceptance (revised)

Elementor / builder saves on the three endpoints no longer trip a self-ban, while
XSS, LFI, other SQLi, and other RCE on those same endpoints stay blocked.
