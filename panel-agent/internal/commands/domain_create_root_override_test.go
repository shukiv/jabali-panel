package commands

import (
	"bytes"
	"strings"
	"testing"

	templ "text/template"
)

func TestCustomDirectivesOverrideRoot_NoDirectives(t *testing.T) {
	if customDirectivesOverrideRoot("") {
		t.Error("empty string must not override root")
	}
	if customDirectivesOverrideRoot("   \n\t") {
		t.Error("whitespace-only must not override root")
	}
}

func TestCustomDirectivesOverrideRoot_PrefixForm(t *testing.T) {
	body := `location / {
    proxy_pass http://127.0.0.1:3200;
}`
	if !customDirectivesOverrideRoot(body) {
		t.Errorf("plain prefix `location /` should override; got false for:\n%s", body)
	}
}

func TestCustomDirectivesOverrideRoot_ExactForm(t *testing.T) {
	body := `location = / { proxy_pass http://127.0.0.1:3200; }`
	if !customDirectivesOverrideRoot(body) {
		t.Error("exact `location = /` should override")
	}
}

func TestCustomDirectivesOverrideRoot_PreferentialPrefix(t *testing.T) {
	body := `location ^~ / { proxy_pass http://127.0.0.1:3200; }`
	if !customDirectivesOverrideRoot(body) {
		t.Error("`location ^~ /` should override")
	}
}

func TestCustomDirectivesOverrideRoot_Subpath(t *testing.T) {
	// `location /api` is a SUBPATH override, not the root. Default
	// `location /` must still render so non-/api requests fall through
	// to the PHP docroot.
	body := `location /api {
    proxy_pass http://127.0.0.1:3200;
}`
	if customDirectivesOverrideRoot(body) {
		t.Errorf("subpath `location /api` must NOT count as root override")
	}
}

func TestCustomDirectivesOverrideRoot_CommentLine(t *testing.T) {
	// A comment line that contains the literal text must not trip
	// the detector — only an actual block opening counts.
	body := `# location / { proxy_pass ... } -- this is commented out
location /healthz { return 200 "ok"; }`
	if customDirectivesOverrideRoot(body) {
		t.Errorf("commented-out `# location /` must not trip detector")
	}
}

func TestCustomDirectivesOverrideRoot_RegexExactRoot(t *testing.T) {
	// `location ~ /` is regex form — nginx treats / as "match any URI
	// containing a slash" (i.e. all). Counts as a full override.
	body := `location ~ / { proxy_pass http://127.0.0.1:3200; }`
	if !customDirectivesOverrideRoot(body) {
		t.Error("`location ~ /` should override (regex matches all)")
	}
}

func TestVhost_RootOverriddenOmitsDefaultLocations(t *testing.T) {
	// Walks the rendered template body to confirm:
	//   - default `try_files $uri $uri/ /index.php?$query_string;` is GONE
	//   - PHP fastcgi block is GONE
	//   - the user's `location / { proxy_pass ... }` is the only `location /`
	tmpl := mustRenderVhost(t, vhostData{
		Domain:           "demo.test",
		DocRoot:          "/home/u/demo",
		HasPHP:           true,
		Username:         "u",
		FPMSocket:        "/run/php/jabali-u/fpm.sock",
		IndexDirective:   "index index.html;",
		IsEnabled:        true,
		SSLCertPath:      "/etc/ssl/x.crt",
		SSLKeyPath:       "/etc/ssl/x.key",
		ListenIPv4:       "1.2.3.4",
		CustomDirectives: "location / { proxy_pass http://127.0.0.1:3200; }",
		RootOverridden:   true,
	})
	if strings.Contains(tmpl, "try_files $uri $uri/ /index.php?$query_string") {
		t.Errorf("default location / still rendered when RootOverridden=true:\n%s", tmpl)
	}
	if strings.Contains(tmpl, "fastcgi_pass") {
		t.Errorf("PHP fastcgi block still rendered when RootOverridden=true:\n%s", tmpl)
	}
	// Custom directive should appear exactly once.
	if c := strings.Count(tmpl, "proxy_pass http://127.0.0.1:3200"); c != 1 {
		t.Errorf("expected exactly 1 proxy_pass, got %d:\n%s", c, tmpl)
	}
}

func TestVhost_DefaultUnchangedWhenNoOverride(t *testing.T) {
	tmpl := mustRenderVhost(t, vhostData{
		Domain:         "demo.test",
		DocRoot:        "/home/u/demo",
		HasPHP:         true,
		Username:       "u",
		FPMSocket:      "/run/php/jabali-u/fpm.sock",
		IndexDirective: "index index.php;",
		IsEnabled:      true,
		SSLCertPath:    "/etc/ssl/x.crt",
		SSLKeyPath:     "/etc/ssl/x.key",
		ListenIPv4:     "1.2.3.4",
		RootOverridden: false, // no override
	})
	if !strings.Contains(tmpl, "try_files $uri $uri/ /index.php?$query_string") {
		t.Errorf("default location / missing when RootOverridden=false:\n%s", tmpl)
	}
	if !strings.Contains(tmpl, "fastcgi_pass unix:/run/php/jabali-u/fpm.sock") {
		t.Errorf("PHP fastcgi block missing when RootOverridden=false:\n%s", tmpl)
	}
}

func mustRenderVhost(t *testing.T, vd vhostData) string {
	t.Helper()
	tmpl, err := tmplParse(vhostTemplate)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vd); err != nil {
		t.Fatalf("execute template: %v", err)
	}
	return buf.String()
}

func tmplParse(s string) (*templ.Template, error) {
	return templ.New("vhost").Parse(s)
}

func TestDirectivesOverrideRoot_RuleBuilderProxyPassRoot(t *testing.T) {
	// Regression for 2026-06-04 vpsjournal.com bug: Rule Builder
	// proxy_pass with path=`/` populates `ruleDirectives` (compiled
	// nginx_rules), NOT `customDirectives`. Pre-fix, RootOverridden
	// only checked customDirectives, so the rule's `location /`
	// collided with the template's default `location /` -> nginx -t
	// emerg "duplicate location /" -> vhost rolled back ->
	// tenant fell through to a sibling vhost.
	rule := `    location / {
        proxy_pass http://127.0.0.1:4569;
        proxy_set_header Host $host;
    }`
	if !directivesOverrideRoot("", rule) {
		t.Errorf("ruleDirectives with `location /` must trigger RootOverridden")
	}
	// Custom only, no rule.
	if !directivesOverrideRoot("location / { return 200; }", "") {
		t.Errorf("customDirectives with `location /` must trigger RootOverridden")
	}
	// Both empty.
	if directivesOverrideRoot("", "") {
		t.Errorf("two empty inputs must not trigger RootOverridden")
	}
	// Neither has root.
	if directivesOverrideRoot("location /api { return 200; }", "    location /healthz { return 200; }") {
		t.Errorf("subpath-only directives must not trigger RootOverridden")
	}
}

func TestVhost_RuleBuilderRootProxyDoesNotDuplicateLocation(t *testing.T) {
	// End-to-end: render the vhost with a proxy_pass root rule via
	// the ruleDirectives slot. The rendered config must contain
	// exactly one `location /` block. Previously the template's
	// default block AND the rule's block both rendered.
	ruleBlock := `    location / {
        proxy_pass http://127.0.0.1:4569;
        proxy_set_header Host $host;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }`
	tmpl := mustRenderVhost(t, vhostData{
		Domain:         "yacht.test",
		DocRoot:        "/home/u/yacht",
		HasPHP:         false,
		Username:       "u",
		FPMSocket:      "/run/php/jabali-u/fpm.sock",
		IndexDirective: "index index.html;",
		IsEnabled:      true,
		SSLCertPath:    "/etc/ssl/x.crt",
		SSLKeyPath:     "/etc/ssl/x.key",
		RedirectHTTPS:  true, // GH #896 redirect; JAB-237 splits the :443 gate
		ServeHTTPS:     true,
		ListenIPv4:     "1.2.3.4",
		RuleDirectives: ruleBlock,
		RootOverridden: directivesOverrideRoot("", ruleBlock),
	})
	// Count `location /` block openings inside server { ssl } context
	// only (ignore the redirect-from-80 server block which legitimately
	// has its own `location /` for 301-to-https). Easier check: total
	// `location /` openings should equal 2 — one in the HTTP redirect
	// block + one from the rule. Pre-fix this was 3 (extra template
	// default emitted the duplicate).
	const want = 2
	got := strings.Count(tmpl, "location / {")
	if got != want {
		t.Errorf("expected %d `location /` blocks, got %d:\n%s", want, got, tmpl)
	}
	if !strings.Contains(tmpl, "proxy_pass http://127.0.0.1:4569") {
		t.Errorf("rule's proxy_pass missing:\n%s", tmpl)
	}
}

// TestVhost_VersionedFPMSocket verifies GH #329: a domain bound to a versioned
// pool renders fastcgi_pass at that pool's socket, not the default per-user one.
func TestVhost_VersionedFPMSocket(t *testing.T) {
	tmpl := mustRenderVhost(t, vhostData{
		Domain:    "example.com",
		DocRoot:   "/home/alice/public_html/example.com",
		HasPHP:    true,
		Username:  "alice",
		FPMSocket: "/run/php/jabali-alice-php8.2/fpm.sock",
		IsEnabled: true,
	})
	if !strings.Contains(tmpl, "fastcgi_pass unix:/run/php/jabali-alice-php8.2/fpm.sock;") {
		t.Errorf("expected versioned fastcgi_pass socket, got:\n%s", tmpl)
	}
	if strings.Contains(tmpl, "unix:/run/php/jabali-alice/fpm.sock") {
		t.Errorf("must not fall back to the default per-user socket")
	}
}
