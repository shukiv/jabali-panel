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
		`ctl:ruleRemoveTargetById=933120;ARGS:_wp_http_referer`,
		// WooCommerce checkout: order-attribution field name contains
		// "user_agent" (a php-config-directives.data entry) — 933120 FP on
		// every checkout. Target-scoped drop on wc-ajax endpoints only.
		`SecRule REQUEST_URI "@contains wc-ajax="`,
		`id:9599300`,
		`ctl:ruleRemoveTargetById=933120;ARGS:post_data`,
		`ctl:ruleRemoveTargetById=933120;ARGS:wc_order_attribution_user_agent`,
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
	allowedDrops := map[string]bool{"911100": true, "942550": true, "932370": true}
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "ruleRemoveById=") {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "SecRule REQUEST_URI") {
			t.Errorf("ruleRemoveById on a non-REQUEST_URI-scoped rule: %q", line)
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
