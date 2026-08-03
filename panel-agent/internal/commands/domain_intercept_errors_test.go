package commands

// GH #879: branded 500 for application errors. The toggle must render
// fastcgi_intercept_errors + a 50x-ONLY location-scoped error_page in BOTH
// PHP locations, and off must stay byte-identical (no intercept anywhere).

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func renderVhostForInterceptTest(t *testing.T, intercept bool) string {
	t.Helper()
	tmpl, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		t.Fatalf("template parse: %v", err)
	}
	vd := vhostData{
		Domain:          "example.com",
		DocRoot:         "/home/u/public_html/example.com",
		HasPHP:          true,
		PHPVersion:      "8.3",
		Username:        "u",
		FPMSocket:       "/run/php/jabali-u/fpm.sock",
		IsEnabled:       true,
		InterceptErrors: intercept,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vd); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	return buf.String()
}

func TestInterceptErrors_On_RendersInBothPHPLocations(t *testing.T) {
	out := renderVhostForInterceptTest(t, true)
	if got := strings.Count(out, "fastcgi_intercept_errors on;"); got != 2 {
		t.Errorf("fastcgi_intercept_errors count = %d, want 2 (index.php + .php locations)\n%s", got, out)
	}
	// The location-scoped error_page must list ONLY 5xx codes — a 404 in
	// this set would steal the app's own 404 page once interception is on.
	if got := strings.Count(out, "error_page 500 502 503 504 /jabali-err-500.html;"); got != 3 {
		// 2 location-scoped + 1 server-level (pre-existing)
		t.Errorf("50x error_page count = %d, want 3\n%s", got, out)
	}
	if strings.Contains(out, "error_page 404 /jabali-err-404.html;\n        fastcgi_intercept_errors") ||
		strings.Count(out, "error_page 404") != 1 {
		t.Errorf("404 error_page must stay server-level only (count=%d)", strings.Count(out, "error_page 404"))
	}
}

func TestInterceptErrors_Off_ByteIdentical(t *testing.T) {
	out := renderVhostForInterceptTest(t, false)
	if strings.Contains(out, "fastcgi_intercept_errors on;") {
		t.Error("intercept off must not render the fastcgi_intercept_errors directive")
	}
	// Server-level branded pages unchanged.
	if !strings.Contains(out, "error_page 500 502 503 504 /jabali-err-500.html;") {
		t.Error("server-level 50x error_page missing")
	}
}
