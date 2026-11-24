package htaccess

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func boolp(b bool) *bool { return &b }

// findRule returns the first rule of the given type, or false.
func firstRule(r Result, typ string) (models.NginxRule, bool) {
	for _, x := range r.Rules {
		if x.Type == typ {
			return x, true
		}
	}
	return models.NginxRule{}, false
}

func hasSecurityWarning(r Result) bool {
	for _, w := range r.Warnings {
		if w.Security {
			return true
		}
	}
	return false
}

func TestFrontControllerSkipped(t *testing.T) {
	in := `RewriteEngine On
RewriteBase /
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]`
	r := Convert(in, "/")
	if len(r.Rules) != 0 {
		t.Fatalf("front-controller should emit no rules, got %d: %+v", len(r.Rules), r.Rules)
	}
	if len(r.Notes) == 0 {
		t.Fatalf("front-controller should add an informational note")
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("front-controller should not warn, got %+v", r.Warnings)
	}
}

func TestWordPressCanonicalIsNoOp(t *testing.T) {
	// Stock WordPress .htaccess.
	in := `# BEGIN WordPress
<IfModule mod_rewrite.c>
RewriteEngine On
RewriteBase /
RewriteRule ^index\.php$ - [L]
RewriteCond %{REQUEST_FILENAME} !-f
RewriteCond %{REQUEST_FILENAME} !-d
RewriteRule . /index.php [L]
</IfModule>
# END WordPress`
	r := Convert(in, "/")
	// The only rewrites are the index.php passthrough and the front
	// controller; neither should produce a typed rule, and nothing security
	// relevant should be dropped.
	if hasSecurityWarning(r) {
		t.Fatalf("stock WordPress should not produce security warnings: %+v", r.Warnings)
	}
	for _, ru := range r.Rules {
		if ru.Type == "rewrite" && ru.Replacement == "/index.php" {
			t.Fatalf("front controller leaked into a rule: %+v", ru)
		}
	}
}

// Redirect lines go to the domain's Redirects list (exact nginx locations,
// their own cap), not the 500-rule Rule Builder: an affiliate site's hundreds
// of short links must fit, and cost nothing per request.
func TestRedirectBecomesPageRedirect(t *testing.T) {
	r := Convert(`Redirect 301 /old-page https://example.com/new-page`, "/")
	if len(r.Rules) != 0 {
		t.Fatalf("a Redirect must not become a rewrite rule: %+v", r.Rules)
	}
	want := models.PageRedirect{Source: "/old-page", Destination: "https://example.com/new-page", Type: "301", KeepQuery: true, TrailingSlash: true}
	if len(r.Redirects) != 1 || r.Redirects[0] != want {
		t.Fatalf("got %+v, want [%+v]", r.Redirects, want)
	}
}

func TestRedirectStatusesAndTargets(t *testing.T) {
	cases := []struct {
		line       string
		wantType   string // "" = not converted, a warning instead
		wantSource string
		wantDest   string
	}{
		// PrettyLinks short link: quoted, 307, destination with its own query.
		{`Redirect 307 "/booking" "http://www.booking.com/index.html?aid=1157792"`, "307", "/booking", "http://www.booking.com/index.html?aid=1157792"},
		{`Redirect 308 /a https://x.example/b`, "308", "/a", "https://x.example/b"},
		{`Redirect seeother /a https://x.example/b`, "303", "/a", "https://x.example/b"},
		{`Redirect 303 /a https://x.example/b`, "303", "/a", "https://x.example/b"},
		{`RedirectPermanent /a /b`, "301", "/a", "/b"},
		// Hebrew source, %-encoded in the file: nginx matches the decoded path.
		{`Redirect 301 /he/%D7%A9%D7%9C%D7%95%D7%9D /he/new/`, "301", "/he/שלום", "/he/new/"},
		{`Redirect 301 /he/שלום /he/new/`, "301", "/he/שלום", "/he/new/"},
		// A literal $ would be an nginx variable.
		{`Redirect 302 /a https://x.example/?p=$1`, "302", "/a", "https://x.example/?p=%241"},
		{`Redirect gone /a`, "", "", ""},
		{`Redirect 410 /a`, "", "", ""},
		{`Redirect 301 /a relative/path`, "", "", ""},
		{`Redirect 301 /a //evil.example/x`, "", "", ""},
	}
	for _, c := range cases {
		r := Convert(c.line, "/")
		if c.wantType == "" {
			if len(r.Redirects) != 0 || len(r.Warnings) == 0 {
				t.Errorf("%s: want a warning and no redirect, got %+v / %+v", c.line, r.Redirects, r.Warnings)
			}
			continue
		}
		if len(r.Redirects) != 1 {
			t.Errorf("%s: want one redirect, got %+v (warnings %+v)", c.line, r.Redirects, r.Warnings)
			continue
		}
		got := r.Redirects[0]
		if got.Type != c.wantType || got.Source != c.wantSource || got.Destination != c.wantDest || !got.KeepQuery || !got.TrailingSlash {
			t.Errorf("%s: got %+v", c.line, got)
		}
	}
}

// Apache uses the first Redirect for a path; a second would also be a
// duplicate nginx location and fail the whole vhost.
func TestRedirectOfRootPointsAtWholeDomain(t *testing.T) {
	res := Convert("Redirect 301 / https://new.example/", "/")
	if len(res.Redirects) != 0 {
		t.Fatalf("a Redirect of / must not become an exact entry: %+v", res.Redirects)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Reason, "whole-domain redirect") {
		t.Fatalf("warnings %+v", res.Warnings)
	}
}

func TestRedirectDuplicatePathKeepsFirst(t *testing.T) {
	r := Convert("Redirect 301 /a https://one.example/\nRedirect 307 /a/ https://two.example/", "/")
	if len(r.Redirects) != 1 || r.Redirects[0].Destination != "https://one.example/" {
		t.Fatalf("want only the first redirect, got %+v", r.Redirects)
	}
	if len(r.Warnings) != 1 {
		t.Fatalf("want a warning for the duplicate, got %+v", r.Warnings)
	}
}

// nginx rewrite flags only issue 301/302: a 307/308 must not come out as 302.
func TestRedirect307NeverDowngradedTo302(t *testing.T) {
	for _, line := range []string{
		`RewriteRule ^old$ /new [R=307,L]`,
		`RewriteRule ^old$ /new [R=308,L]`,
		`RedirectMatch 307 ^/old/(.*)$ https://x.example/$1`,
	} {
		r := Convert(line, "/")
		if len(r.Rules) != 0 || len(r.Redirects) != 0 || len(r.Warnings) != 1 {
			t.Errorf("%s: want no rule and one warning, got rules %+v redirects %+v warnings %+v", line, r.Rules, r.Redirects, r.Warnings)
		}
	}
}

func TestErrorDocumentPointsAtRawDirective(t *testing.T) {
	r := Convert(`ErrorDocument 404 /he/404/index.html`, "/")
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0].Reason, "error_page 404 /he/404/index.html;") {
		t.Fatalf("want the error_page line to add under Raw directives, got %+v", r.Warnings)
	}
}

func TestRedirectMatchRegex(t *testing.T) {
	r := Convert(`RedirectMatch 301 ^/blog/(.*)$ https://new.example.com/$1`, "/")
	ru, ok := firstRule(r, "rewrite")
	if !ok {
		t.Fatalf("expected rewrite, got %+v", r)
	}
	if ru.Pattern != `^/blog/(.*)$` || ru.Replacement != `https://new.example.com/$1` || ru.Flag != "permanent" {
		t.Errorf("unexpected RedirectMatch mapping: %+v", ru)
	}
}

func TestRewriteRuleExternalRedirect(t *testing.T) {
	r := Convert(`RewriteRule ^promo$ https://example.com/sale [R=301,L]`, "/")
	ru, ok := firstRule(r, "rewrite")
	if !ok {
		t.Fatalf("expected rewrite, got %+v", r)
	}
	if ru.Pattern != "^/promo$" {
		t.Errorf("pattern not rebased: %q", ru.Pattern)
	}
	if ru.Flag != "permanent" {
		t.Errorf("R=301 should be permanent, got %q", ru.Flag)
	}
}

func TestRewriteRuleInternalRebaseSubdir(t *testing.T) {
	// .htaccess living under /shop/ rewriting product URLs.
	r := Convert(`RewriteRule ^item/([0-9]+)$ product.php?id=$1 [L]`, "/shop/")
	ru, ok := firstRule(r, "rewrite")
	if !ok {
		t.Fatalf("expected rewrite, got %+v", r)
	}
	if ru.Pattern != "^/shop/item/([0-9]+)$" {
		t.Errorf("subdir pattern not rebased: %q", ru.Pattern)
	}
	if ru.Replacement != "/shop/product.php?id=$1" {
		t.Errorf("subdir replacement not rebased: %q", ru.Replacement)
	}
	if ru.Flag != "last" {
		t.Errorf("internal rewrite should be last, got %q", ru.Flag)
	}
}

func TestRewriteBaseOverridesArg(t *testing.T) {
	in := `RewriteBase /app/
RewriteRule ^go$ target.php [L]`
	r := Convert(in, "/")
	ru, _ := firstRule(r, "rewrite")
	if ru.Pattern != "^/app/go$" {
		t.Errorf("RewriteBase should override basePath: %q", ru.Pattern)
	}
}

func TestHeaderSet(t *testing.T) {
	r := Convert(`Header set X-Frame-Options "SAMEORIGIN"`, "/")
	ru, ok := firstRule(r, "custom_header")
	if !ok {
		t.Fatalf("expected custom_header, got %+v", r)
	}
	if ru.Name != "X-Frame-Options" || ru.Value != "SAMEORIGIN" {
		t.Errorf("unexpected header: %+v", ru)
	}
	if ru.Always == nil || *ru.Always {
		t.Errorf("non-always Header should set Always=false")
	}
}

func TestHeaderAlways(t *testing.T) {
	r := Convert(`Header always set Strict-Transport-Security "max-age=63072000"`, "/")
	ru, _ := firstRule(r, "custom_header")
	if ru.Always == nil || !*ru.Always {
		t.Errorf("`Header always set` should set Always=true: %+v", ru)
	}
	if ru.Value != "max-age=63072000" {
		t.Errorf("quoted value not preserved: %q", ru.Value)
	}
}

func TestHeaderUnsetWarns(t *testing.T) {
	r := Convert(`Header unset X-Powered-By`, "/")
	if len(r.Rules) != 0 {
		t.Errorf("Header unset has no typed equivalent, should emit no rule")
	}
	if len(r.Warnings) == 0 {
		t.Errorf("Header unset should warn")
	}
}

func TestPHPValueAndFlag(t *testing.T) {
	r := Convert("php_value upload_max_filesize 64M\nphp_flag display_errors off", "/")
	if len(r.Rules) != 2 {
		t.Fatalf("expected 2 php_setting rules, got %d", len(r.Rules))
	}
	if r.Rules[0].Name != "upload_max_filesize" || r.Rules[0].Value != "64M" {
		t.Errorf("php_value mapping wrong: %+v", r.Rules[0])
	}
	if r.Rules[1].Name != "display_errors" || r.Rules[1].Value != "off" {
		t.Errorf("php_flag mapping wrong: %+v", r.Rules[1])
	}
}

func TestOrderAllowDenyWhitelist(t *testing.T) {
	in := `Order Allow,Deny
Allow from 192.168.1.0/24
Allow from 10.0.0.5`
	r := Convert(in, "/admin/")
	ru, ok := firstRule(r, "ip_access")
	if !ok {
		t.Fatalf("expected ip_access, got %+v", r)
	}
	if ru.Mode != "allow_list" {
		t.Errorf("Order Allow,Deny should be allow_list, got %q", ru.Mode)
	}
	if ru.Path != "/admin/" {
		t.Errorf("ip_access should be scoped to base path, got %q", ru.Path)
	}
	if len(ru.IPs) != 2 {
		t.Errorf("expected 2 IPs, got %v", ru.IPs)
	}
}

func TestOrderDenyAllowBlacklist(t *testing.T) {
	// Subdir scope: a deny_list compiles to `location /uploads/ { … }`, which
	// does NOT clobber the default routing, so it's emitted.
	in := `Order Deny,Allow
Deny from 1.2.3.4`
	r := Convert(in, "/uploads/")
	ru, _ := firstRule(r, "ip_access")
	if ru.Mode != "deny_list" {
		t.Errorf("Order Deny,Allow should be deny_list, got %q", ru.Mode)
	}
	if len(ru.IPs) != 1 || ru.IPs[0] != "1.2.3.4" {
		t.Errorf("unexpected deny IPs: %v", ru.IPs)
	}
}

// Docroot-level (Path "/") allow_list / deny_list would compile to a
// `location /` that REPLACES the vhost's default routing (dropping try_files
// + the PHP location → PHP served as source). Must warn-and-skip, NOT emit.
func TestDocrootAllowListRefusedToProtectPHP(t *testing.T) {
	in := `Order Allow,Deny
Allow from 203.0.113.0/24`
	r := Convert(in, "/") // docroot
	if _, ok := firstRule(r, "ip_access"); ok {
		t.Fatalf("docroot allow_list must NOT be emitted (would serve PHP as source): %+v", r.Rules)
	}
	if !hasSecurityWarning(r) {
		t.Errorf("refusing a docroot whole-site restriction must raise a security warning")
	}
}

func TestDocrootDenyListRefused(t *testing.T) {
	in := `Order Deny,Allow
Deny from 1.2.3.4`
	r := Convert(in, "/")
	if _, ok := firstRule(r, "ip_access"); ok {
		t.Errorf("docroot deny_list must NOT be emitted (would clobber PHP routing)")
	}
	if !hasSecurityWarning(r) {
		t.Errorf("refusing a docroot deny_list must raise a security warning")
	}
}

// The one safe docroot case: `Deny from all` locks the whole site (everything
// 403s, no routing needed) → emitted as deny-all.
func TestDocrootDenyAllStillEmitted(t *testing.T) {
	in := `Order Allow,Deny
Deny from all`
	r := Convert(in, "/")
	ru, ok := firstRule(r, "ip_access")
	if !ok {
		t.Fatalf("docroot Deny-from-all is safe (fully locked) and should still emit, got %+v", r)
	}
	if ru.Mode != "allow_list" || len(ru.IPs) != 0 {
		t.Errorf("expected deny-all (allow_list, no IPs), got %+v", ru)
	}
}

func TestDenyFromAllProducesDenyAll(t *testing.T) {
	in := `Order Allow,Deny
Deny from all`
	r := Convert(in, "/private/")
	ru, ok := firstRule(r, "ip_access")
	if !ok {
		t.Fatalf("Deny from all should still emit a deny-all ip_access, got %+v", r)
	}
	if ru.Mode != "allow_list" || len(ru.IPs) != 0 {
		t.Errorf("deny-all should be allow_list with no IPs (compiles to `deny all`), got %+v", ru)
	}
}

// ----- FAIL-CLOSED negative tests: must DROP, never emit the permissive half -----

func TestRewriteCondNonFrontControllerDropped(t *testing.T) {
	// A hotlink/referer guard: dropping the cond but keeping the rule would
	// invert the protection. Must drop the whole thing.
	in := `RewriteCond %{HTTP_REFERER} !^https://example.com [NC]
RewriteRule \.(jpg|png)$ - [F]`
	r := Convert(in, "/")
	if len(r.Rules) != 0 {
		t.Fatalf("conditional rule must be dropped, got rules: %+v", r.Rules)
	}
	if !hasSecurityWarning(r) {
		t.Errorf("dropping a conditional protection must raise a security warning")
	}
}

func TestForbidRuleNotSilentlyDropped(t *testing.T) {
	r := Convert(`RewriteRule ^secret/ - [F]`, "/")
	if len(r.Rules) != 0 {
		t.Errorf("[F] has no typed equivalent, should emit no rule")
	}
	if !hasSecurityWarning(r) {
		t.Errorf("[F] (forbid) dropped → must be a SECURITY warning, not silent")
	}
}

func TestMixedAllowDenyDroppedFailClosed(t *testing.T) {
	in := `Order Allow,Deny
Allow from 10.0.0.0/8
Deny from 10.0.0.99`
	r := Convert(in, "/")
	if _, ok := firstRule(r, "ip_access"); ok {
		t.Errorf("mixed allow+deny can't be expressed as a flat list — must drop, not emit")
	}
	if !hasSecurityWarning(r) {
		t.Errorf("dropping a mixed access block must be a security warning")
	}
}

func TestAllowDenyWithoutOrderDropped(t *testing.T) {
	r := Convert(`Allow from 1.2.3.4`, "/")
	if _, ok := firstRule(r, "ip_access"); ok {
		t.Errorf("Allow with no Order → default action unknown → must drop")
	}
	if !hasSecurityWarning(r) {
		t.Errorf("ambiguous access must raise a security warning")
	}
}

func TestAllowHostnameDroppedFailClosed(t *testing.T) {
	// A hostname in a deny list: if we silently skipped it, the deny list
	// would be incomplete → access granted that should be denied.
	in := `Order Deny,Allow
Deny from evil.example.com`
	r := Convert(in, "/")
	if _, ok := firstRule(r, "ip_access"); ok {
		t.Errorf("unparseable host in deny list must drop the whole block")
	}
	if !hasSecurityWarning(r) {
		t.Errorf("dropping an incomplete deny list must be a security warning")
	}
}

func TestRequireValidUserWarns(t *testing.T) {
	in := `AuthType Basic
AuthName "Restricted"
AuthUserFile /home/u/.htpasswd
Require valid-user`
	r := Convert(in, "/")
	if len(r.Rules) != 0 {
		t.Errorf("auth can't be migrated (no creds in .htaccess) — emit no rule")
	}
	if !hasSecurityWarning(r) {
		t.Errorf("Require valid-user should warn to use Directory Privacy")
	}
}

func TestRequireIPAllowList(t *testing.T) {
	in := `Require ip 203.0.113.0/24 198.51.100.7`
	r := Convert(in, "/wp-admin/")
	ru, ok := firstRule(r, "ip_access")
	if !ok {
		t.Fatalf("Require ip should map to allow_list, got %+v", r)
	}
	if ru.Mode != "allow_list" || len(ru.IPs) != 2 {
		t.Errorf("unexpected Require ip mapping: %+v", ru)
	}
}

func TestRequireAllDenied(t *testing.T) {
	r := Convert(`Require all denied`, "/")
	ru, ok := firstRule(r, "ip_access")
	if !ok || ru.Mode != "allow_list" || len(ru.IPs) != 0 {
		t.Errorf("Require all denied → deny-all (allow_list, no IPs), got %+v", r.Rules)
	}
}

func TestCommentsAndContinuationsAndBlankLines(t *testing.T) {
	in := `# a comment
   # indented comment

Header set X-Test \
  "value spanning"`
	r := Convert(in, "/")
	ru, ok := firstRule(r, "custom_header")
	if !ok {
		t.Fatalf("continuation join failed: %+v", r)
	}
	if ru.Name != "X-Test" || ru.Value != "value spanning" {
		t.Errorf("unexpected continuation parse: %+v", ru)
	}
}

func TestUnknownDirectiveWarns(t *testing.T) {
	r := Convert(`SetEnvIf User-Agent "badbot" bad_bot`, "/")
	if len(r.Warnings) == 0 {
		t.Errorf("unknown/unsupported directive should warn")
	}
}

func TestRuleOrderPreserved(t *testing.T) {
	in := `Header set A 1
RedirectMatch 301 ^/x$ /y
php_value memory_limit 256M`
	r := Convert(in, "/")
	if len(r.Rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(r.Rules))
	}
	if r.Rules[0].Type != "custom_header" || r.Rules[1].Type != "rewrite" || r.Rules[2].Type != "php_setting" {
		t.Errorf("rule order not preserved: %v", []string{r.Rules[0].Type, r.Rules[1].Type, r.Rules[2].Type})
	}
}

// Apache runs Redirect and RedirectMatch as one first-match pass, so the
// earlier exact Redirect lines beat a later catch-all RedirectMatch. A typed
// rewrite rule would run before their exact locations and shadow them all.
func TestRedirectMatchAfterExactRedirectsNotTyped(t *testing.T) {
	in := `Redirect 301 /product/mug/ https://academy.example/product/mug/
Redirect 301 /he/product/cup/ https://academy.example/he/product/cup/
RedirectMatch 301 "^/product(-category)?/" "/shop/"
RedirectMatch 301 "^/he/product(-category)?/" "/he/shop/"
RedirectMatch 301 ^/blog/(.*)$ https://new.example/$1`
	r := Convert(in, "/")
	if len(r.Redirects) != 2 {
		t.Fatalf("want the 2 exact redirects, got %+v", r.Redirects)
	}
	// The later ^/blog/ one follows the demoted ones: a rule would jump ahead.
	if len(r.Rules) != 0 {
		t.Fatalf("want no rule, got %+v", r.Rules)
	}
	if len(r.Warnings) != 3 {
		t.Fatalf("want a warning per RedirectMatch, got %+v", r.Warnings)
	}
	for i, want := range []string{
		"(/product/mug/; 1 in all)",
		"(/he/product/cup/; 1 in all)",
		"an earlier RedirectMatch was left for Raw directives",
	} {
		if w := r.Warnings[i].Reason; !strings.Contains(w, want) {
			t.Errorf("warning %d: want %q, got %q", i, want, w)
		}
	}
	for i, want := range []string{
		"location ~ ^/product(-category)?/ { return 301 /shop/$is_args$args; }",
		"location ~ ^/he/product(-category)?/ { return 301 /he/shop/$is_args$args; }",
		"location ~ ^/blog/(.*)$ { return 301 https://new.example/$1$is_args$args; }",
	} {
		if w := r.Warnings[i].Reason; !strings.Contains(w, want) {
			t.Errorf("warning %d: want the raw recipe %q, got %q", i, want, w)
		}
	}
}

// A RedirectMatch before the exact lines wins in Apache too: still a rule.
func TestRedirectMatchBeforeExactRedirectsStaysRule(t *testing.T) {
	r := Convert("RedirectMatch 301 ^/product/ /shop/\nRedirect 301 /product/mug/ https://academy.example/product/mug/", "/")
	if len(r.Rules) != 1 || len(r.Redirects) != 1 || len(r.Warnings) != 0 {
		t.Fatalf("want 1 rule + 1 redirect, no warning, got rules %+v redirects %+v warnings %+v", r.Rules, r.Redirects, r.Warnings)
	}
}

// [R=4xx] answers that status: dropping it silently would open the URL.
func TestRewriteRuleStatus4xxWarns(t *testing.T) {
	for line, want := range map[string]string{
		`RewriteRule ^wp-admin/admin-ajax\.php$ - [R=410,L]`: "location = /wp-admin/admin-ajax.php { return 410; }",
		`RewriteRule ^old/ - [R=403,NC]`:                     "location ~* ^/old/ { return 403; }",
		`RewriteRule ^gone$ /new [R=404]`:                    "location = /gone { return 404; }",
	} {
		r := Convert(line, "/")
		if len(r.Rules) != 0 || len(r.Warnings) != 1 || !r.Warnings[0].Security || !strings.Contains(r.Warnings[0].Reason, want) {
			t.Errorf("%s: want no rule and one security warning with %q, got rules %+v warnings %+v", line, want, r.Rules, r.Warnings)
		}
	}
	r := Convert(`RewriteRule ^a$ - [R=301]`, "/")
	if len(r.Rules) != 0 || len(r.Warnings) != 1 {
		t.Errorf("- [R=301]: want one warning, got %+v", r)
	}
}

// The .htaccess is the tenant's; the recipe is for an administrator to paste
// into unrestricted raw directives, so it must never carry a token or block
// break from the file.
func TestRawRecipeNeverCarriesInjection(t *testing.T) {
	for _, in := range []string{
		"Redirect 301 /product/a/ https://x.example/\nRedirectMatch 301 ^/product/ \"/shop/; } location /x { return 200 pwn\"",
		"Redirect 301 /product/a/ https://x.example/\nRedirectMatch 301 \"^/product/(;})?\" /shop/",
		"Redirect 301 /product/a/ https://x.example/\nRedirectMatch 301 ^/product/ /shop/$host",
		`RewriteRule "^a b$" - [R=410]`,
		`RewriteRule ^a\ - [R=410]`,
		"RewriteRule ^a#{ - [R=410]",
	} {
		r := Convert(in, "/")
		if len(r.Rules) != 0 || len(r.Warnings) != 1 {
			t.Errorf("%q: want no rule and one warning, got rules %+v warnings %+v", in, r.Rules, r.Warnings)
			continue
		}
		if w := r.Warnings[0].Reason; strings.Contains(w, "location ~") || strings.Contains(w, "location =") || strings.Contains(w, "pwn") {
			t.Errorf("%q: recipe must be withheld, got %q", in, w)
		}
	}
}

// An escape the regex decodes into a literal (`\x3b` is ";") must not reach
// a `location =` recipe; the escaped regex itself is safe as `location ~`.
func TestRawRecipeDecodedLiteralStaysRegex(t *testing.T) {
	r := Convert(`RewriteRule ^a\x3b\x7d$ - [R=410]`, "/")
	if len(r.Warnings) != 1 {
		t.Fatalf("want one warning, got %+v", r.Warnings)
	}
	if w := r.Warnings[0].Reason; !strings.Contains(w, `location ~ ^/a\x3b\x7d$ { return 410; }`) {
		t.Errorf("want the regex recipe, got %q", w)
	}
}

// After a RedirectMatch is left for raw directives, Apache still tries it
// before every later line: a later RedirectMatch can't become a rule that
// runs first, and a later Redirect it covers is never reached.
func TestDemotedRedirectMatchKeepsApacheOrder(t *testing.T) {
	in := `Redirect 301 /product/mug/ https://x.example/mug/
RedirectMatch 301 ^/product/ /shop/
RedirectMatch 301 ^/product/special/(.*)$ /y/$1
Redirect 301 /product/late/ https://x.example/late/
Redirect 301 /about https://x.example/about`
	r := Convert(in, "/")
	if len(r.Rules) != 0 {
		t.Fatalf("want no rule, got %+v", r.Rules)
	}
	if len(r.Redirects) != 2 || r.Redirects[0].Source != "/product/mug/" || r.Redirects[1].Source != "/about" {
		t.Fatalf("want /product/mug/ and /about only, got %+v", r.Redirects)
	}
	if len(r.Warnings) != 3 || !strings.Contains(r.Warnings[2].Reason, "(^/product/) left for Raw directives already redirects this path") {
		t.Fatalf("want 3 warnings, the last for /product/late/, got %+v", r.Warnings)
	}
}

// A target with its own query keeps it; mod_alias appends none then.
func TestRedirectMatchRecipeTargetQuery(t *testing.T) {
	r := Convert("Redirect 301 /p/a https://x.example/\nRedirectMatch 301 ^/p/ /shop/?from=p", "/")
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0].Reason, "{ return 301 /shop/?from=p; }") {
		t.Fatalf("want the target unchanged, got %+v", r.Warnings)
	}
}

// Only an error status gets a return recipe.
func TestRewriteRuleOddStatusNoRecipe(t *testing.T) {
	r := Convert(`RewriteRule ^a$ - [R=200]`, "/")
	if len(r.Warnings) != 1 || strings.Contains(r.Warnings[0].Reason, "{ return") {
		t.Fatalf("want a warning without a recipe, got %+v", r.Warnings)
	}
}
