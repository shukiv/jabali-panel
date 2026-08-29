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
// Keep every exclusion as NARROW as the evidence. The construct that would
// express that best — ctl:ruleRemoveTargetById=<rule>;ARGS:<arg>, dropping one
// rule's inspection of ONE argument — is a SILENT NO-OP in CrowdSec's Coraza
// engine. So is ruleRemoveTargetByTag, and so is SecRuleUpdateTargetByTag. All
// three parse, pass `crowdsec -t`, load without error, and do nothing. Proven
// 2026-08-04 by A/B on a single-rule payload with only the construct changed:
// ruleRemoveTargetById left the request blocked (403), ruleRemoveById let it
// through (404).
//
// ctl:ruleRemoveById is the only removal that works, and it drops the rule for
// the WHOLE matched request. Narrowness therefore has to come from the MATCH:
// scope the URI tightly, and chain on a wordpress_logged_in_ cookie where the
// endpoint is authenticated, so unauthenticated traffic keeps full scoring.
// (ctl: does propagate from a chain — verified.) A path-allow remains banned.
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
# what WOULD neutralize the SQLi family on builder ARGS — except that the
# attack-sqli tag drop was ctl:ruleRemoveTargetByTag, a silent no-op, and has
# been removed (JAB-227). That FP class is currently covered only by the
# upstream plugin above plus the three working ruleRemoveById drops.
SecAction "id:9599000,phase:1,nolog,pass,setvar:tx.wordpress-rule-exclusions-plugin_enabled=1"
#
# !! REMOVED, NOT CONVERTED — JAB-227 !!
# 9599210 (Elementor SQLi on admin-ajax) and 9599220 (Code Snippets rce +
# php-injection) expressed their whole effect through
# ctl:ruleRemoveTargetByTag=<family>;ARGS, which is a SILENT NO-OP in Coraza.
# They never worked, so they are deleted rather than converted: the working
# construct drops a rule for the entire request, so saying "exclude the SQLi
# family from these ARGS" would mean dropping ~19 rules (attack-sqli PL1) or
# ~27 (attack-rce + attack-injection-php PL1) on those endpoints — a far bigger
# hole than the false positive, and exactly what crs_plugin_test.go forbids.
# The same tag drop was also stripped from 9599200/9599202.
#
# Consequence, stated plainly: those two false-positive classes are UNADDRESSED
# again. They were never actually addressed; the directives only looked like it.
# The ruleRemoveById drops that remain (911100/942550/932370) DO work and are
# what really fixed GH #404, and the upstream crs-exclusion-plugin-wordpress
# enabled just above covers much of the same ground.
#
# To fix properly: measure which INDIVIDUAL rules still fire on those endpoints
# and drop those by ID, or get ruleRemoveTargetByTag supported upstream.
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
SecRule REQUEST_URI "@beginsWith /wp-admin/" "id:9599100,phase:1,pass,nolog,chain"
    SecRule REQUEST_COOKIES_NAMES "@rx ^wordpress_logged_in_" "t:none,ctl:ruleRemoveById=933120"
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
#    (this was chained phase:2 rule 9599210, now removed — ARGS:action is
#    jabali-before loads before CRS, so the ctl disables the 942 ARGS target
#    before those rules evaluate). elementor_ajax / elementor_save_builder both
#    match ^elementor. Every other admin-ajax handler (incl. nopriv) keeps full
#    SQLi-args inspection. Still surgical, ADR-0147-consistent: only attack-sqli,
#    only ARGS; BODY/headers/cookies + every non-SQLi family stay active.
SecRule REQUEST_URI "@rx ^/wp-json/elementor/"       "id:9599200,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"
SecRule REQUEST_URI "@rx ^/wp-admin/post\.php"       "id:9599202,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"
SecRule REQUEST_URI "@rx ^/wp-admin/admin-ajax\.php" "id:9599201,phase:1,pass,nolog,ctl:ruleRemoveById=911100,ctl:ruleRemoveById=942550,ctl:ruleRemoveById=932370"
#
# Code Snippets plugin (JAB-193). POST /wp-json/code-snippets/v1/snippets/<id>
# saves a PHP snippet, so the JSON body legitimately IS PHP source — <?php,
# function calls, superglobals. CRS scores it exactly as it would score an RCE
# payload (observed on a production editor: rce: 5, php_injection: 5,
# anomaly: 10 -> inline 403) and the editor simply cannot save their work. The
# stopgap was allowlisting that one contractor's IP out of the ENTIRE WAF,
# permanently — strictly worse than a scoped rule drop.
#
# There is no narrower content signal available here: accepting code IS the
# endpoint's purpose, so no payload shape distinguishes a legitimate save from
# an injection attempt. Scope on identity and reach instead:
#   - Chained on a wordpress_logged_in_* cookie. An UNAUTHENTICATED request to
#     the same path keeps FULL rce + php-injection scoring. A forged cookie
#     buys an attacker nothing by itself — the route is capability-gated
#     (manage_options) and nonce-checked by the plugin, so WordPress answers
#     401/403 long before a snippet is written; the cookie only decides whether
#     CRS scores the body, never whether the write is allowed.
#   - Only the two families that fire on source text, and only on ARGS +
#     REQUEST_BODY. Headers, cookies and the URI stay inspected, and every
#     other family (SQLi, XSS, LFI, traversal, scanner detection) stays fully
#     active on this path.
#   - v[0-9]+ so a future API version can't silently re-break saves.
# 942100 vs wp-admin/admin-ajax.php (newzaps, observed 2026-08-02..03).
# admin-ajax.php is WordPress's general-purpose AJAX endpoint: every plugin
# and the block editor POST arbitrary user-authored content through it —
# post bodies, meta values, saved queries, form-builder field definitions.
# CRS 942100 is libinjection, which scores on SQL-shaped text rather than a
# fixed pattern, so ordinary editorial content ("... 1 or 2 items", quoted
# fragments, anything resembling a boolean clause) reads as SQLi. Measured on
# newzaps: 26 denies across 5 source addresses, including a consumer ISP
# address repeatedly locked out of the site's own wp-admin.
#
# 9599201 above already drops 911100/942550/932370 here unconditionally, but
# not 942100 — libinjection is a far more valuable detector and admin-ajax.php
# is reachable UNAUTHENTICATED for plugins that register nopriv_ actions, which
# is precisely where WordPress SQLi is exploited. So this is scoped on identity
# rather than widened on the path:
#   - Chained on a wordpress_logged_in_* cookie. An unauthenticated request to
#     the same endpoint keeps FULL libinjection scoring. A forged cookie buys
#     nothing on its own: admin-ajax dispatches to capability- and
#     nonce-checked handlers, so the cookie only decides whether CRS scores the
#     body, never whether the action runs.
#   - Only 942100, and only on ARGS + REQUEST_BODY. Every other SQLi rule, and
#     every other family, stays fully active on this path — a real injection in
#     these args is still caught by the rest of the 942 group.
SecRule REQUEST_URI "@rx ^/wp-admin/admin-ajax\.php" "id:9599230,phase:1,pass,nolog,chain"
    SecRule REQUEST_COOKIES_NAMES "@rx ^wordpress_logged_in_" "t:none,ctl:ruleRemoveById=942100"
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
SecRule REQUEST_URI "@contains wc-ajax=" "id:9599300,phase:1,pass,nolog,ctl:ruleRemoveById=933120"
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

	// GH #1044: the CrowdSec AppSec engine drops any request whose body
	// exceeds its max (default 10 MB) BEFORE rule evaluation — earlier than
	// the /api/v1/ on_match allowlist below can cancel it, so a large chunked
	// DB restore (or admin File-Manager upload) POST to the panel API returns
	// an opaque "403 CrowdSec Access Forbidden". The 10 MB default surfaced
	// once the DB-restore chunk size was bumped to 80 MB (#1394); it also bit
	// every other big-body panel route. Switch the oversize behaviour from
	// "drop" to "partial": the first 10 MB is still inspected (WAF attacks
	// live in the first bytes, and the inspection cost stays bounded — we do
	// NOT raise max_body_size), but an oversized body is then PASSED instead
	// of blocked. This is engine-wide (the AppSec listener is shared by every
	// vhost), which is the deliberate tradeoff: no vhost blocks purely on
	// body size, all vhosts keep full inspection of the first 10 MB. Set via
	// the on_load hook (the supported mechanism — max_body_size /
	// body_size_exceeded_action are not plain AppsecConfig fields).
	b.WriteString("on_load:\n")
	b.WriteString(" - apply:\n")
	b.WriteString("    - SetBodySizeExceededAction(\"partial\")\n")

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
		//
		// /dav/ — WebDAV for FTP/SFTP subaccounts (GH #1146). Its non-standard
		// methods (PROPFIND/MKCOL/MOVE/COPY/PUT) and XML request bodies
		// categorically trip CRS method/protocol/body rules, so the WAF 403-bans
		// every WebDAV request (verified live). The real boundary is the
		// per-request auth_request authenticator (unix_chkpwd + jabali-webdav
		// group), exactly as the signon gate is for the DB tools; IP-reputation
		// banning still applies (this only skips AppSec body inspection).
		adminFilter := `req.URL.Path startsWith "/api/v1/" || req.URL.Path startsWith "/phpmyadmin/" || req.URL.Path startsWith "/jabali-adminer/" || req.URL.Path startsWith "/dav/"`
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
