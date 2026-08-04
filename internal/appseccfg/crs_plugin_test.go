package appseccfg

import "strings"

import "testing"

// CRSPluginBefore must stay SURGICAL: drop only rule 933120's inspection
// of only the _wp_http_referer arg, only under /wp-admin/. A broadening
// to ruleRemoveById or a path-allow would silently open a WAF hole.
func TestCRSPluginBefore_Surgical(t *testing.T) {
	body := CRSPluginBefore()

	mustContain := []string{
		`SecRule REQUEST_URI "@beginsWith /wp-admin/"`,
		`id:9599100`,
		`ctl:ruleRemoveById=933120`, // was target-scoped (silent no-op); now cookie-gated
		// WooCommerce checkout: order-attribution field name contains
		// "user_agent" (a php-config-directives.data entry) — 933120 FP on
		// every checkout. Target-scoped drop on wc-ajax endpoints only.
		`SecRule REQUEST_URI "@contains wc-ajax="`,
		`id:9599300`,
		// Was two target-scoped drops; both were silent no-ops in Coraza, so the
		// checkout FP was never actually fixed. ruleRemoveById is the only form
		// that works, and "wc-ajax=" is narrow enough to contain it.
		`ctl:ruleRemoveById=933120`,
		// text/plain must be an allowed request content type (sendBeacon
		// default; jQuery contentType habit) — pre-seeded WITH the full CRS
		// default list, not as a lone value, or 901162 skips its defaults and
		// every normal form post gets blocked instead.
		`id:9599001`,
		`|application/x-www-form-urlencoded| |multipart/form-data| |multipart/related| |text/xml| |application/xml| |application/soap+xml| |application/json| |application/cloudevents+json| |application/cloudevents-batch+json| |text/plain|`,
	}
	for _, sub := range mustContain {
		if !strings.Contains(body, sub) {
			t.Errorf("CRSPluginBefore missing %q", sub)
		}
	}

	// Must NOT broaden: no path-allow / blanket allow.
	mustNotContain := []string{
		"SetRemediation", // path-allow / blanket allow
		"CancelEvent",
	}
	for _, sub := range mustNotContain {
		if strings.Contains(body, sub) {
			t.Errorf("CRSPluginBefore must not contain %q (too broad)", sub)
		}
	}

	// GH #404 (revised): ruleRemoveById IS allowed, but ONLY for the three
	// builder false-positive rules and ONLY on a SecRule that is path-scoped
	// by REQUEST_URI. A bare/global ruleRemoveById, or one targeting any
	// other rule ID, is a WAF hole.
	// GH #404 allowed three builder rules. JAB-227 adds two more, because the
	// target-scoped construct that used to express these narrowly turned out to
	// be a silent no-op — see CRSPluginBefore's header. Anything outside the
	// original trio must be CONTAINED: either a tight URI match or a logged-in
	// cookie chain, asserted separately below.
	allowedDrops := map[string]bool{
		"911100": true, "942550": true, "932370": true,
		"933120": true, // wp-admin referer FP + WooCommerce checkout FP
		"942100": true, // libinjection vs wp-admin/admin-ajax.php
	}
	// Rules whose ctl sits on a chain's second line are URI-scoped by the
	// chain's FIRST line; track that so the scoping check below stays honest.
	chained := map[int]bool{}
	{
		lines := strings.Split(body, "\n")
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "SecRule REQUEST_URI") && strings.Contains(l, ",chain") {
				if i+1 < len(lines) {
					chained[i+1] = true
				}
			}
		}
	}
	for i, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "ruleRemoveById=") {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "SecRule REQUEST_URI") && !chained[i] {
			t.Errorf("ruleRemoveById on a rule scoped by neither REQUEST_URI nor a URI-scoped chain: %q", line)
		}
		// Every ruleRemoveById=<id> on the line must be an allowed builder rule.
		for _, tok := range strings.Split(line, "ctl:ruleRemoveById=") {
			id := ""
			for _, c := range tok {
				if c >= '0' && c <= '9' {
					id += string(c)
				} else {
					break
				}
			}
			if id != "" && !allowedDrops[id] {
				t.Errorf("ruleRemoveById drops non-builder rule %q (WAF hole): %q", id, line)
			}
		}
	}

	// id stays in the reserved jabali CRS-plugin range 9,599,000–9,599,999.
	if !strings.Contains(body, "id:95990") && !strings.Contains(body, "id:95991") &&
		!strings.Contains(body, "id:95992") && !strings.Contains(body, "id:9599") {
		t.Error("CRSPluginBefore id outside reserved 9,599,000–9,599,999 range")
	}
}

// Only ctl:ruleRemoveById has any effect in CrowdSec's Coraza engine. The
// target-scoped and tag-scoped forms parse, pass `crowdsec -t`, load without
// error, and then silently do nothing — proven 2026-08-04 by A/B on a
// single-rule payload (/probe?c=' OR 1=1 -> 942100) from a non-allowlisted
// source, changing only the construct: ruleRemoveTargetById left it blocked
// (403), ruleRemoveById let it through (404).
//
// Four exclusions shipped for months as placebos because the tests asserted the
// directive was PRESENT rather than that it WORKED. This guard cannot prove an
// exclusion works — only a live box can — but it stops the dead forms being
// reintroduced in the rules themselves.
func TestCRSPluginBefore_NoDeadRemovalConstructsInRules(t *testing.T) {
	for _, line := range strings.Split(CRSPluginBefore(), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "SecRule") && !strings.HasPrefix(trimmed, "SecAction") {
			continue // comments may (and do) discuss the dead forms on purpose
		}
		for _, dead := range []string{
			"ctl:ruleRemoveTargetById",
			"ctl:ruleRemoveTargetByTag",
			"SecRuleUpdateTargetById",
			"SecRuleUpdateTargetByTag",
		} {
			if strings.Contains(line, dead) {
				t.Errorf("%s is a SILENT NO-OP in Coraza — the rule will read correctly and "+
					"do nothing. Use ctl:ruleRemoveById and narrow the MATCH instead: %q", dead, line)
			}
		}
	}
}

// Widening from per-argument to whole-rule removal is only acceptable because
// the match compensates. Pin the containment: exclusions on authenticated
// surfaces stay chained on the logged-in cookie, so an UNAUTHENTICATED request
// to the same endpoint keeps full CRS scoring.
func TestCRSPluginBefore_WholeRuleDropsStayContained(t *testing.T) {
	body := CRSPluginBefore()
	for _, tc := range []struct{ id, why string }{
		{"9599100", "/wp-admin/ is authenticated; unauth traffic must keep 933120"},
		{"9599230", "admin-ajax.php is reachable unauthenticated via nopriv_ actions"},
	} {
		idx := strings.Index(body, "id:"+tc.id)
		if idx < 0 {
			t.Errorf("rule %s missing", tc.id)
			continue
		}
		seg := body[idx:]
		if end := strings.Index(seg, "\n#"); end > 0 {
			seg = seg[:end]
		}
		if !strings.Contains(seg, "chain") || !strings.Contains(seg, "wordpress_logged_in_") {
			t.Errorf("rule %s drops a whole rule without a logged-in cookie gate — %s", tc.id, tc.why)
		}
	}
}
