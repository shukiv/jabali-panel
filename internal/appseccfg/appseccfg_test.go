package appseccfg

import (
	"strings"
	"testing"
)

// Single source of the crowdsecurity/jabali-appsec config (ADR-0083
// shape; ADR-0102 follow-up). Today the template is hand-written
// twice — install.sh (bash) + panel-agent security_crowdsec.go (Go,
// full-regenerate on geoblock Apply) — guaranteed to drift
// (feedback_cross_boundary_contracts). Render() is the one producer;
// both callers pass their host-dependent inband set.

func mustContain(t *testing.T, s, sub, why string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%s: missing %q\n--- got ---\n%s", why, sub, s)
	}
}

func mustNotContain(t *testing.T, s, sub, why string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Fatalf("%s: unexpected %q\n--- got ---\n%s", why, sub, s)
	}
}

func TestRender_HeaderAndInband(t *testing.T) {
	out := Render(Opts{
		Mode:           "off",
		Inband:         []string{"crowdsecurity/base-config", "crowdsecurity/vpatch-*", "crowdsecurity/generic-*"},
		AdminAllowlist: true,
	})
	mustContain(t, out, "# Managed by jabali", "header")
	mustContain(t, out, "# jabali-mode: off", "mode header line")
	mustContain(t, out, "name: crowdsecurity/jabali-appsec", "config name")
	mustContain(t, out, "default_remediation: ban", "default remediation")
	mustContain(t, out, "inband_rules:\n - crowdsecurity/base-config\n - crowdsecurity/vpatch-*\n - crowdsecurity/generic-*\n", "inband list in order")
	// ADR-0102: panel-API allowlist present.
	mustContain(t, out, `on_match:
 - filter: req.URL.Path startsWith "/api/v1/" || req.URL.Path startsWith "/phpmyadmin/" || req.URL.Path startsWith "/jabali-adminer/" || req.URL.Path startsWith "/dav/"
   apply:
    - CancelEvent()
    - CancelAlert()
    - SetRemediation("allow")
`, "ADR-0102 panel-API allowlist + GH#285 DB-tool allowlist + GH#1146 WebDAV")
	mustContain(t, out, `startsWith "/phpmyadmin/"`, "phpMyAdmin allowlisted from AppSec (GH#285)")
	mustContain(t, out, `startsWith "/jabali-adminer/"`, "Adminer allowlisted from AppSec (GH#285)")
	mustContain(t, out, `startsWith "/dav/"`, "WebDAV allowlisted from AppSec (GH#1146)")
	// No WebmailHosts → no webmail filter line.
	mustNotContain(t, out, `req.Host ==`, "no webmail allowlist when WebmailHosts empty")
	mustNotContain(t, out, `startsWith "mail."`, "must not emit unsafe req.Host startsWith")
	if strings.Contains(out, "pre_eval:") {
		t.Fatalf("mode=off must NOT emit pre_eval:\n%s", out)
	}
	// Deterministic order: inband_rules < on_match.
	if strings.Index(out, "inband_rules:") > strings.Index(out, "on_match:") {
		t.Fatal("inband_rules must precede on_match")
	}
}

func TestRender_WebmailHostsExplicitEquality(t *testing.T) {
	// Mixed-case, dupes, and noise: should be lowercased, deduped,
	// sorted, and stripped of anything outside [a-z0-9.-].
	out := Render(Opts{
		Mode:           "off",
		Inband:         []string{"crowdsecurity/vpatch-*"},
		AdminAllowlist: true,
		WebmailHosts: []string{
			"Mail.Foo.Com",
			"autoconfig.foo.com",
			"mail.foo.com", // dup
			"mail.bar.com",
			"",                     // empty (skip)
			"mail.evil\"injection", // quote (skip)
			"mail..oops.com",       // double dot (skip)
		},
	})
	// Stable equality OR-chain, sorted.
	mustContain(t, out, `req.Host == "autoconfig.foo.com" || req.Host == "mail.bar.com" || req.Host == "mail.foo.com"`, "deterministic equality OR-chain")
	// Must NOT contain unsafe startsWith form anywhere.
	mustNotContain(t, out, `startsWith "mail."`, "explicit equality, never prefix")
	mustNotContain(t, out, `startsWith "autoconfig."`, "explicit equality, never prefix")
	mustNotContain(t, out, `injection`, "noise stripped")
	mustNotContain(t, out, `..oops`, "double-dot label stripped")
}

func TestRender_WebmailHostsOnlyEmitsOnMatchEvenWithoutAdmin(t *testing.T) {
	out := Render(Opts{
		Mode:           "off",
		Inband:         []string{"crowdsecurity/vpatch-*"},
		AdminAllowlist: false,
		WebmailHosts:   []string{"mail.foo.com"},
	})
	mustContain(t, out, "on_match:", "on_match emitted when only WebmailHosts present")
	mustContain(t, out, `req.Host == "mail.foo.com"`, "single-host equality")
	mustNotContain(t, out, `req.URL.Path startsWith "/api/v1/"`, "no admin allowlist filter when AdminAllowlist=false")
}

func TestRender_GeoblockModes(t *testing.T) {
	inb := []string{"crowdsecurity/vpatch-*", "crowdsecurity/generic-*"}

	allow := Render(Opts{Mode: "allow", Countries: []string{"IL", "US"}, Inband: inb, AdminAllowlist: true})
	mustContain(t, allow, "# jabali-mode: allow", "allow mode header")
	mustContain(t, allow, "# jabali-countries: IL,US", "countries header")
	mustContain(t, allow, `pre_eval:
 - filter: IsInBand == true && GeoIPEnrich(req.RemoteAddr)?.Country.IsoCode not in ["IL", "US", ""]
   apply:
    - DropRequest("Forbidden Country (jabali allow-list)")
`, "allow pre_eval (note trailing empty for allow)")

	deny := Render(Opts{Mode: "deny", Countries: []string{"RU"}, Inband: inb, AdminAllowlist: true})
	mustContain(t, deny, `pre_eval:
 - filter: IsInBand == true && GeoIPEnrich(req.RemoteAddr)?.Country.IsoCode in ["RU"]
   apply:
    - DropRequest("Forbidden Country (jabali deny-list)")
`, "deny pre_eval")
	// on_match must coexist with pre_eval, and precede it.
	mustContain(t, deny, "on_match:", "on_match present in deny mode")
	if strings.Index(deny, "on_match:") > strings.Index(deny, "pre_eval:") {
		t.Fatal("on_match must precede pre_eval")
	}
}

func TestRender_AllowlistOptOut(t *testing.T) {
	out := Render(Opts{Mode: "off", Inband: []string{"crowdsecurity/vpatch-*"}, AdminAllowlist: false})
	if strings.Contains(out, "on_match:") {
		t.Fatal("AdminAllowlist=false + no WebmailHosts must NOT emit on_match")
	}
}

func TestRender_GeoExprAgentParity(t *testing.T) {
	// Matches panel-agent renderAppSecGeoblockRule exactly.
	a0 := Render(Opts{Mode: "allow", Countries: nil, Inband: []string{"x"}})
	mustContain(t, a0, `not in [""]`, "allow + no countries → not in [\"\"]")
	d0 := Render(Opts{Mode: "deny", Countries: nil, Inband: []string{"x"}})
	mustContain(t, d0, `in []`, "deny + no countries → in []")
	a2 := Render(Opts{Mode: "allow", Countries: []string{"IL", "US"}, Inband: []string{"x"}})
	mustContain(t, a2, `not in ["IL", "US", ""]`, "allow + N → trailing empty")
	d2 := Render(Opts{Mode: "deny", Countries: []string{"RU"}, Inband: []string{"x"}})
	mustContain(t, d2, `in ["RU"]`, "deny + N")
}

func TestSanitizeWebmailHosts(t *testing.T) {
	in := []string{
		"  Mail.Example.Com  ",
		"mail.example.com", // dup post-lowercase
		"mail.bar.com",
		"",                // empty
		".leading.dot",    // leading dot
		"trailing.dot.",   // trailing dot
		"double..dot.com", // empty label
		"weird\nhost",     // newline
		"quote\"host",     // quote
		"a.com",
		strings.Repeat("a", 254), // too long
	}
	got := sanitizeWebmailHosts(in)
	want := []string{"a.com", "mail.bar.com", "mail.example.com"}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d want=%d (got=%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

// GH #404 (revised, ADR-0147): the WordPress page-builder exemption is a
// SCOPED CRS rule-ID drop in the before-plugin, NOT an on_match blanket
// allow. Render() must therefore emit NO WP path-allow / SetRemediation for
// any wp path — that surface moved entirely into CRSPluginBefore().
func TestRender_NoWordPressBlanketAllow(t *testing.T) {
	out := Render(Opts{Mode: "off", Inband: []string{"crowdsecurity/vpatch-*"}, AdminAllowlist: true})
	mustNotContain(t, out, "wp-json/elementor", "no WP path-allow in the rendered yaml")
	mustNotContain(t, out, "wordpress_logged_in_", "no cookie-gated WP exemption in the yaml")
	mustNotContain(t, out, "/wp-admin/admin-ajax.php", "no WP admin-ajax allow in the yaml")
	// /api/v1/ admin allowlist is unaffected.
	mustContain(t, out, `startsWith "/api/v1/"`, "admin /api/v1/ allowlist intact")
}

// GH #404: the builder false-positive rules are dropped surgically in the
// CRS before-plugin — only 911100/942550/932370, only on the builder paths,
// and never as a SetRemediation("allow") blanket exemption.
func TestCRSPluginBefore_WordPressBuilderExclusions(t *testing.T) {
	out := CRSPluginBefore()
	for _, id := range []string{"911100", "942550", "932370"} {
		mustContain(t, out, "ctl:ruleRemoveById="+id, "drops builder FP rule "+id)
	}
	mustContain(t, out, `"@rx ^/wp-json/elementor/"`, "scoped to elementor REST")
	mustContain(t, out, `^/wp-admin/admin-ajax`, "scoped to admin-ajax")
	mustContain(t, out, `^/wp-admin/post`, "scoped to post.php")
	// wp/v2 is deliberately NOT excluded — public read surface stays fully inspected.
	mustNotContain(t, out, `^/wp-json/wp/v2/`, "public wp/v2 REST must keep full CRS coverage")
	// The original narrow 933120 exclusion is still present.
	mustContain(t, out, "ctl:ruleRemoveById=933120", "933120 wp-admin exclusion intact (now cookie-gated; the target-scoped form was a no-op)")
	// GH #594: the upstream WordPress CRS-exclusion plugin is activated, which
	// covers admin-ajax/builder false positives (incl. 942151) comprehensively.
	mustContain(t, out, "tx.wordpress-rule-exclusions-plugin_enabled=1", "WP exclusion plugin activated")
	// GH #594: SQLi-on-ARGS is dropped for Elementor saves so 942151 (+942150/
	// 180/200/…) can't ban owners — broad on elementor/post.php, but on
	// admin-ajax ONLY for action=^elementor (chained), so unauthenticated
	// wp_ajax_nopriv_* handlers keep full SQLi-args inspection.
	mustNotContain(t, out, `id:9599210`, "JAB-227: no-op-only rule removed, must not be resurrected")
	mustNotContain(t, out, "ctl:ruleRemoveTargetByTag=attack-sqli", "JAB-227: dead construct must not return")
	// admin-ajax phase-1 rule must NOT carry the broad tag drop (only narrow ById).
	mustContain(t, out, `^/wp-admin/admin-ajax\.php" "id:9599201,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"`, "admin-ajax phase-1 keeps only narrow ById drops")
	// Surgical, not blanket: no path-allow, no remediation override.
	mustNotContain(t, out, `SetRemediation`, "before-plugin must not blanket-allow")
}

// JAB-193 / JAB-227: the Code Snippets exclusion (9599220) is GONE.
//
// It expressed "stand down rce + php-injection on this authenticated REST save"
// entirely through ctl:ruleRemoveTargetByTag, which is a silent no-op in the
// Coraza engine CrowdSec ships. It never worked. Converting it would mean
// dropping ~27 rules (attack-rce + attack-injection-php at PL1) on that path — a
// far bigger hole than the false positive it was fixing, and exactly what the
// WAF-hole guard in crs_plugin_test.go exists to forbid.
//
// So that FP is unaddressed again, deliberately and visibly, rather than papered
// over by a directive that reads like protection and provides none. Reopen with
// per-rule evidence: which single rules actually fire on that endpoint.
func TestCRSPluginBefore_CodeSnippetsExclusionRemoved(t *testing.T) {
	out := CRSPluginBefore()
	mustNotContain(t, out, "id:9599220", "no-op-only rule must not be resurrected")
	mustNotContain(t, out, "ctl:ruleRemoveTargetByTag=attack-rce", "dead construct must not return")
	mustNotContain(t, out, "ctl:ruleRemoveTargetByTag=attack-injection-php", "dead construct must not return")
}

func TestCRSPluginBefore_AdminAjaxLibinjectionIsCookieGated(t *testing.T) {
	out := CRSPluginBefore()

	mustContain(t, out, "ctl:ruleRemoveById=942100", "942100 dropped (ruleRemoveById — the target-scoped form is a silent no-op)")

	// The drop must be chained on the logged-in cookie. Locate the rule and
	// assert the chain, rather than trusting that the cookie appears anywhere
	// in the file (it also appears in the code-snippets rule).
	idx := strings.Index(out, "id:9599230")
	if idx < 0 {
		t.Fatal("admin-ajax 942100 rule (id:9599230) missing")
	}
	// Start at the beginning of that SecRule's line — the URI match sits
	// before the id: on the same line.
	if nl := strings.LastIndex(out[:idx], "\n"); nl >= 0 {
		idx = nl + 1
	}
	rule := out[idx:]
	if end := strings.Index(rule, "\n# "); end > 0 {
		rule = rule[:end]
	}
	if !strings.Contains(rule, "chain") {
		t.Error("942100 drop is not chained — it would apply to unauthenticated requests")
	}
	if !strings.Contains(rule, "wordpress_logged_in_") {
		t.Error("942100 drop is not gated on the wordpress_logged_in_ cookie")
	}
	if !strings.Contains(rule, `^/wp-admin/admin-ajax`) {
		t.Error("942100 drop is not scoped to admin-ajax.php")
	}

	// Narrowness: only 942100 is dropped by ID here. Removing the whole
	// attack-sqli tag on this path would surrender libinjection everywhere on
	// the endpoint, which is the thing this scoping exists to avoid.
	if strings.Contains(rule, "ruleRemoveTargetByTag=attack-sqli") {
		t.Error("admin-ajax rule removes the whole SQLi tag — too broad")
	}
}
