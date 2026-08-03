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
	// Native missing-file 404s: the PHP locations' guards must fall back to
	// the named location (their 50x-only error_page detached them from the
	// branded 404), and the named location must re-brand with correct status.
	if got := strings.Count(out, "try_files $uri @jabali_missing;"); got != 2 {
		t.Errorf("@jabali_missing guard count = %d, want 2", got)
	}
	if !strings.Contains(out, "location @jabali_missing {") {
		t.Error("named location @jabali_missing missing")
	}
	if !strings.Contains(out, "error_page 404 /jabali-err-404.html;\n        return 404;") {
		t.Error("@jabali_missing must re-brand the 404 with correct status")
	}
	// server-level 404 + the one inside @jabali_missing — never inside the
	// PHP locations (that would intercept the app's own FPM 404s).
	if got := strings.Count(out, "error_page 404"); got != 2 {
		t.Errorf("error_page 404 count = %d, want 2 (server-level + @jabali_missing)", got)
	}
	// Only the ACME challenge location keeps a bare =404 guard.
	if got := strings.Count(out, "try_files $uri =404;"); got != 1 {
		t.Errorf("bare =404 guard count = %d, want 1 (ACME only)", got)
	}
}

func TestInterceptErrors_Off_ByteIdentical(t *testing.T) {
	out := renderVhostForInterceptTest(t, false)
	if strings.Contains(out, "fastcgi_intercept_errors on;") {
		t.Error("intercept off must not render the fastcgi_intercept_errors directive")
	}
	// Server-level branded pages unchanged; guards back to plain =404.
	if !strings.Contains(out, "error_page 500 502 503 504 /jabali-err-500.html;") {
		t.Error("server-level 50x error_page missing")
	}
	if strings.Contains(out, "@jabali_missing") {
		t.Error("named location must not render with intercept off")
	}
	// ACME + both PHP-location guards.
	if got := strings.Count(out, "try_files $uri =404;"); got != 3 {
		t.Errorf("=404 guard count = %d, want 3 (ACME + 2 PHP locations)", got)
	}
}
