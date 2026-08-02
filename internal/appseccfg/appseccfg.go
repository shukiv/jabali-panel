// Package appseccfg is the single source of the
// crowdsecurity/jabali-appsec AppSec config (ADR-0083 shape;
// ADR-0102 follow-up). It replaces two hand-written copies — the
// bash heredoc in install.sh and the Go string template in
// panel-agent security_crowdsec.go (which fully regenerates the file
// on every geoblock Apply). Both now call Render so the schema, the
// ADR-0102 admin-API allowlist, and the geoblock pre_eval cannot
// drift.
//
// The inband-rule set is host-dependent (install.sh presence-gates it
// by stat'ing /etc/crowdsec/appsec-rules/*; the agent uses its fixed
// set) so it is a parameter, not baked in — only the template is
// single-sourced.
package appseccfg

import (
	"sort"
	"strings"
)

// Opts is everything a caller must supply. Inband is the presence-gated
// rule list (caller decides which files exist). Mode is
// "off"|"allow"|"deny"; Countries are ISO-3166-1 alpha-2 codes used by
// the geoblock pre_eval for allow/deny modes.
type Opts struct {
	Mode           string
	Countries      []string
	Inband         []string
	AdminAllowlist bool
	// PanelHost (GH #706) scopes the admin path-allowlist to the panel host so a
	// tenant vhost serving /api/v1/, /phpmyadmin/, or /jabali-adminer/ paths cannot
	// bypass AppSec. Empty = path-only (fallback; never breaks the operator tools).
	PanelHost string
	// WebmailHosts is the explicit allowlist of FQDNs exempted from
	// CrowdSec AppSec. These are the dedicated webmail/autodiscover
	// vhosts served exclusively by Bulwark + Stalwart, both of which
	// auth-gate their own traffic — CRS rule 911100 ("Method not
	// allowed by policy") otherwise 403s Bulwark's PUT /api/auth/
	// session, which the SPA needs to hydrate the impersonation
	// session after the JWT exchange.
	//
	// Caller MUST populate from a managed source (the panel-api
	// reconciler writes /var/lib/jabali-panel/webmail-hosts.list from the
	// domain repo every pass; render-config + agent both read that
	// file). MUST NOT be derived from the request — `req.Host
	// startsWith "mail."` is bypassable via arbitrary Host headers
	// reaching the default_server (which serves phpMyAdmin).
	WebmailHosts []string
}

// allowExpr/denyExpr reproduce panel-agent renderAppSecGeoblockRule
// EXACTLY (byte parity is the point of single-sourcing):
//
//	no countries  → allow `""`         deny ``
//	N countries    → allow `"A", "B", ""`  deny `"A", "B"`
func geoExpr(codes []string, allow bool) string {
	if len(codes) == 0 {
		if allow {
			return `""`
		}
		return ""
	}
	q := make([]string, len(codes))
	for i, c := range codes {
		q[i] = `"` + c + `"`
	}
	j := strings.Join(q, ", ")
	if allow {
		return j + `, ""`
	}
	return j
}

// sanitizeWebmailHosts dedupes, lowercases, and rejects anything that
// would break the YAML literal or the seclang expr. Valid hostnames are
// strictly [a-z0-9.-], 1-253 chars; anything else is dropped. Returns
// a stable-sorted slice for deterministic output (write-on-diff stays
// stable even when the source list comes back in random order).
func sanitizeWebmailHosts(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || len(h) > 253 {
			continue
		}
		valid := true
		for _, r := range h {
			if !(r == '.' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		// Reject empty labels, leading/trailing dots and double dots.
		if strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") || strings.Contains(h, "..") {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// CRSPluginBeforePath is the jabali-owned CRS "before" plugin file.
// crowdsecurity/crs globs crs-plugins/*/*-before.conf (via its
// seclang_files_rules) and runs them BEFORE the CRS detection rules
// (REQUEST-933 PHP-injection, REQUEST-949 anomaly blocking), which is
// exactly where a targeted exclusion must sit. The jabali/ subdir is
// not hub-managed, so it survives crowdsec hub updates; render-config
// rewrites it on every install + `jabali update`.
const CRSPluginBeforePath = "/var/lib/crowdsec/data/crs-plugins/jabali/jabali-before.conf"

// CRSPluginBefore returns the body of the jabali CRS "before" plugin —
// targeted CrowdSec AppSec / CRS false-positive exclusions that must run
// before the detection + anomaly-scoring rules.
//
// Jabali CRS-plugin SecRule id range: 9,599,000–9,599,999.
//
// Keep every exclusion as NARROW as the evidence: prefer
// ctl:ruleRemoveTargetById=<rule>;ARGS:<arg> (drop one rule's
// inspection of one argument) over ruleRemoveById (kills the rule
// everywhere) or a path-allow (kills the whole WAF for that path).
func CRSPluginBefore() string {
	return `# Managed by jabali — CRS "before" exclusion plugin.
# DO NOT hand-edit. Written by ` + "`jabali appsec render-config`" + `.
# Loaded by crowdsecurity/crs via the crs-plugins/*/*-before.conf glob,
# BEFORE REQUEST-933 (PHP injection) + REQUEST-949 (anomaly blocking).
#
# Activate the upstream crowdsecurity/crs-exclusion-plugin-wordpress (GH #594).
# It is installed + listed in jabali-appsec.yaml inband_rules but inert by
# default: wordpress-rule-exclusions-before.conf strips ALL its own rules
# (9507100-9507999) when tx.wordpress-rule-exclusions-plugin_enabled == 0, and
# nothing set it. Setting it here turns on the upstream-maintained, SURGICAL
# (per-rule, per-endpoint — NOT a path-allow) WordPress exclusion set, which
# clears many WP false-positives across the 942/941/932 families on the
# standard WP endpoints. This file sorts before wordpress/ in the
# crs-plugins/*/*-before.conf glob, so the flag is set before that plugin's
# self-strip check runs. Respects ADR-0147's "surgical, no blanket bypass".
# NOTE (GH #594): the plugin does NOT remove 942151 on admin-ajax, and its
# attack-sqli tag drop is scoped to specific NAMED args (post_title, url, …).
# Elementor saves post their payload under actions/data JSON blobs, so 942151
# ("SQL function name", PL1/CRITICAL → +5 = anomaly threshold → bans alone)
# still fires. The explicit attack-sqli;ARGS drop on the builder rules below is
# what neutralizes the SQLi family on builder ARGS (broad on elementor/post.php;
# Elementor-action-gated on admin-ajax — see 9599210 below).
SecAction "id:9599000,phase:1,nolog,pass,setvar:tx.wordpress-rule-exclusions-plugin_enabled=1"
#
# Allow text/plain as a request Content-Type (arizot-e.com incident,
# 2026-08-02). CRS 920420 blocks any CT outside tx.allowed_request_content_type
# (+5 critical = instant anomaly block), and the CRS 4.0 default list omits
# text/plain. But text/plain is what navigator.sendBeacon() sends BY DEFAULT
# for string payloads, and a common jQuery $.ajax contentType habit in WP
# plugins (the stg-site street picker GETs its city JSON with it) — so stock
# WordPress/WooCommerce sites kept crossing the appsec-native threshold and
# banning real shoppers. cPanel/DirectAdmin stacks don't police CT, so plugin
# authors never see it. Trade-off: a text/plain body is not parsed into ARGS,
# so ARGS rules don't inspect such bodies — marginal, since attackers can
# already pick an allowed CT and almost no PHP endpoint parses text/plain.
# CRS 901162 only seeds the default when the var is unset, and this plugin
# file loads before the CRS rule files, so pre-seeding here wins. Full
# default list (REQUEST-901-INITIALIZATION.conf 901162) + text/plain:
SecAction "id:9599001,phase:1,nolog,pass,setvar:'tx.allowed_request_content_type=|application/x-www-form-urlencoded| |multipart/form-data| |multipart/related| |text/xml| |application/xml| |application/soap+xml| |application/json| |application/cloudevents+json| |application/cloudevents-batch+json| |text/plain|'"
#
# 933120 vs WordPress admin search: editing a custom post type issues
# /wp-admin/edit.php?...&_wp_http_referer=<double-URL-encoded URL>. The
# nested URL-inside-a-URL trips CRS 933120 (PHP injection) → php_injection
# score 10 >= anomaly threshold 5 → 949110 blocks → CrowdSec AppSec 4h
# ban. False positive on legit logged-in admin traffic.
#
# Drop ONLY 933120's inspection of ONLY the _wp_http_referer arg, ONLY
# under /wp-admin/. Body inspection, every other rule, and 933120 on
# every other arg/path all stay active.
SecRule REQUEST_URI "@beginsWith /wp-admin/"     "id:9599100,phase:1,pass,nolog,ctl:ruleRemoveTargetById=933120;ARGS:_wp_http_referer"
#
# 911100 / 942550 / 932370 vs WordPress page builders (GH #404). Saving an
# Elementor page POSTs arbitrary HTML/CSS/JSON markup that trips method-
# enforcement (911100), JSON-SQLi (942550) and the RCE rule (932370),
# pushing the inbound anomaly score past 949110 -> a ~4h AppSec ban on the
# editor's OWN IP. Drop ONLY those three rule IDs, ONLY on the builder
# endpoints. Every other rule still inspects these paths, so XSS (941),
# LFI (930), path traversal, other SQLi/RCE rules, etc. stay fully active
# (surgical, not a path-allow). Replaces the rejected on_match
# blanket-allow exemption (ADR-0147 revised).
#
# PLUS the whole attack-sqli family on ARGS (GH #594): builder JSON
# (actions/data blobs) is full of SQL-ish tokens (if(/char/concat/left(/…)
# that trip 942151 + 942150/180/200/… — dropping them by ID is endless
# whack-a-mole, so drop the attack-sqli TAG on ARGS instead. SCOPING matters:
#  - /wp-json/elementor/ + /wp-admin/post.php are all-Elementor / cap+nonce
#    editor paths — broad attack-sqli;ARGS drop is fine.
#  - admin-ajax.php is NOT (it also serves UNAUTHENTICATED wp_ajax_nopriv_*
#    handlers — jet_ajax_search, cx_search_posts, … — that route untrusted
#    args into queries). A broad drop there removes SQLi-args coverage from a
#    live unauthenticated surface. So on admin-ajax keep the narrow ById drops
#    broad, and drop attack-sqli;ARGS ONLY when the action is Elementor's
#    (chained phase:2 rule 9599210 — ARGS:action is parsed in phase 2;
#    jabali-before loads before CRS, so the ctl disables the 942 ARGS target
#    before those rules evaluate). elementor_ajax / elementor_save_builder both
#    match ^elementor. Every other admin-ajax handler (incl. nopriv) keeps full
#    SQLi-args inspection. Still surgical, ADR-0147-consistent: only attack-sqli,
#    only ARGS; BODY/headers/cookies + every non-SQLi family stay active.
SecRule REQUEST_URI "@rx ^/wp-json/elementor/"       "id:9599200,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370,ctl:ruleRemoveTargetByTag=attack-sqli;ARGS"
SecRule REQUEST_URI "@rx ^/wp-admin/post\.php"       "id:9599202,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370,ctl:ruleRemoveTargetByTag=attack-sqli;ARGS"
SecRule REQUEST_URI "@rx ^/wp-admin/admin-ajax\.php" "id:9599201,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"
SecRule REQUEST_FILENAME "@endsWith /wp-admin/admin-ajax.php" "id:9599210,phase:2,pass,nolog,chain"
    SecRule ARGS:action "@rx ^elementor" "t:none,t:lowercase,ctl:ruleRemoveTargetByTag=attack-sqli;ARGS"
#
# 933120 vs WooCommerce checkout (arizot-e.com incident, 2026-08-02).
# WooCommerce 8.5+ order attribution submits a field literally named
# wc_order_attribution_user_agent with every checkout AJAX call — and
# "user_agent" is an entry in php-config-directives.data, so CRS 933120
# (PHP config directive found, PL1/CRITICAL, +5 = anomaly threshold) fires
# on EVERY legitimate checkout: on the flat arg at ?wc-ajax=checkout and
# via the serialized post_data blob at ?wc-ajax=update_order_review. A few
# field edits in one session cross the appsec-native scenario threshold and
# hand a paying customer a 4h IP ban mid-purchase.
# Drop ONLY 933120's inspection of ONLY those two args, ONLY on wc-ajax
# endpoints. All other rules still inspect both args (a real attack value
# there is still caught), and 933120 stays active everywhere else.
SecRule REQUEST_URI "@contains wc-ajax=" "id:9599300,phase:1,pass,nolog,ctl:ruleRemoveTargetById=933120;ARGS:post_data,ctl:ruleRemoveTargetById=933120;ARGS:wc_order_attribution_user_agent"
`
}

// Render returns the full jabali-appsec.yaml body. Deterministic:
// header(+inband) → on_match (ADR-0102) → pre_eval (geoblock).
func Render(o Opts) string {
	mode := o.Mode
	if mode == "" {
		mode = "off"
	}
	csv := strings.Join(o.Countries, ",")

	var b strings.Builder
	b.WriteString("# Managed by jabali — M27 AppSec config.\n")
	b.WriteString("# DO NOT hand-edit. Set via the admin Security → CrowdSec tab OR\n")
	b.WriteString("# POST /api/v1/admin/security/crowdsec/appsec/geoblock.\n")
	b.WriteString("# jabali-mode: " + mode + "\n")
	b.WriteString("# jabali-countries: " + csv + "\n")
	b.WriteString("name: crowdsecurity/jabali-appsec\n")
	b.WriteString("default_remediation: ban\n")
	b.WriteString("inband_rules:\n")
	for _, r := range o.Inband {
		b.WriteString(" - " + r + "\n")
	}

	// ADR-0102 (amended 2026-05-19): the ENTIRE panel API (/api/v1/)
	// is Kratos-session-gated, same-origin SPA control plane — not
	// public web attack surface. The CRS generic ruleset false-
	// positives on legitimate REST: rule 911100 "Method not allowed
	// by policy" blocks every PATCH/PUT/DELETE (DNS records, etc.),
	// and body-inspection flags JSON/ULID payloads. Narrowing to
	// /api/v1/admin/ left the SPA's own mutations (e.g.
	// PATCH /api/v1/dns/records/:id) WAF-blocked with an opaque 403.
	// Exempt the whole prefix; public vhosts keep full AppSec.
	//
	// M6.6 follow-up (2026-06-01): the webmail vhosts (mail.<dom> /
	// autoconfig.<dom>) are served exclusively by Bulwark + Stalwart,
	// both of which auth-gate their own traffic. CRS rule 911100
	// blocks Bulwark's PUT /api/auth/session — the SPA's session
	// rehydration call AFTER /api/auth/impersonate sets cookies — so
	// every webmail SSO landed on a logged-out SPA (symptom: "SSO not
	// working" even though the JWT was accepted and impersonation
	// cookies were set). JMAP batch POSTs also trip body-inspector
	// false-positives. Allowlist these hostnames with EXPLICIT
	// equality, never a `startsWith` prefix — req.Host is a client-
	// controlled header and nginx falls back to default_server (which
	// serves phpMyAdmin) when no vhost matches.
	hosts := sanitizeWebmailHosts(o.WebmailHosts)
	if o.AdminAllowlist || len(hosts) > 0 {
		b.WriteString("on_match:\n")
	}
	if o.AdminAllowlist {
		// /api/v1/ — the Kratos-session-gated SPA control plane (above).
		// /phpmyadmin/ + /jabali-adminer/ — the SSO-gated DB admin tools
		// (GH #285). Their whole JOB is to run arbitrary SQL, so CRS SQLi /
		// RCE body rules (942xxx/932xxx) categorically false-positive on a
		// legit SQL import or query and CrowdSec bans the operator's own IP.
		// The SSO gate (signon) is the real boundary; behavioral log-based
		// scenarios + the firewall bouncer for already-banned IPs are
		// unaffected (this only skips AppSec body inspection). Path-prefix,
		// matching the /api/v1/ precedent — both are jabali-owned paths not
		// routed on tenant vhosts.
		adminFilter := `req.URL.Path startsWith "/api/v1/" || req.URL.Path startsWith "/phpmyadmin/" || req.URL.Path startsWith "/jabali-adminer/"`
		if o.PanelHost != "" {
			// GH #706: only skip AppSec for these paths ON THE PANEL HOST, so a
			// tenant vhost serving the same paths is still WAF-inspected. req.Host
			// equality (never a prefix — it is a client-controlled header).
			adminFilter = "(" + adminFilter + `) && req.Host == "` + o.PanelHost + `"`
		}
		b.WriteString(" - filter: " + adminFilter + "\n")
		b.WriteString("   apply:\n")
		b.WriteString("    - CancelEvent()\n")
		b.WriteString("    - CancelAlert()\n")
		b.WriteString("    - SetRemediation(\"allow\")\n")
	}
	if len(hosts) > 0 {
		parts := make([]string, len(hosts))
		for i, h := range hosts {
			parts[i] = `req.Host == "` + h + `"`
		}
		b.WriteString(" - filter: " + strings.Join(parts, " || ") + "\n")
		b.WriteString("   apply:\n")
		b.WriteString("    - CancelEvent()\n")
		b.WriteString("    - CancelAlert()\n")
		b.WriteString("    - SetRemediation(\"allow\")\n")
	}

	// Geoblock pre_eval (ADR-0060). off = inert (no block).
	switch mode {
	case "allow":
		// allow-list: drop everything NOT in the set; the trailing ""
		// keeps requests whose GeoIP lookup yields no country (local /
		// private ranges) reachable.
		b.WriteString(`pre_eval:
 - filter: IsInBand == true && GeoIPEnrich(req.RemoteAddr)?.Country.IsoCode not in [` + geoExpr(o.Countries, true) + `]
   apply:
    - DropRequest("Forbidden Country (jabali allow-list)")
`)
	case "deny":
		b.WriteString(`pre_eval:
 - filter: IsInBand == true && GeoIPEnrich(req.RemoteAddr)?.Country.IsoCode in [` + geoExpr(o.Countries, false) + `]
   apply:
    - DropRequest("Forbidden Country (jabali deny-list)")
`)
	}
	return b.String()
}
