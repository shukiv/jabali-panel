package commands

// GH #962: PATH_INFO support for front-controller PHP apps (osTicket, …).
// The toggle must render an extra PATH_INFO location that fires only for a
// real .php followed by a further /segment, with an existence guard (the
// JAB-187 second lock). Off must stay byte-identical — no PATH_INFO location.

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func renderVhostForPathInfoTest(t *testing.T, pathInfo bool, phpValue string) string {
	t.Helper()
	tmpl, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		t.Fatalf("template parse: %v", err)
	}
	vd := vhostData{
		Domain:         "example.com",
		DocRoot:        "/home/u/public_html/example.com",
		HasPHP:         true,
		PHPVersion:     "8.3",
		Username:       "u",
		FPMSocket:      "/run/php/jabali-u/fpm.sock",
		IsEnabled:      true,
		EnablePathInfo: pathInfo,
		PHPValueParam:  phpValue,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vd); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	return buf.String()
}

func TestPathInfo_On_RendersGuardedLocation(t *testing.T) {
	out := renderVhostForPathInfoTest(t, true, "")
	// The PATH_INFO location, with the non-greedy named-capture split.
	if !strings.Contains(out, `location ~ ^(?<jabali_pi_script>.+?\.php)(?<jabali_pi_suffix>/.+)$ {`) {
		t.Errorf("PATH_INFO location missing or malformed\n%s", out)
	}
	// JAB-187 second lock: existence guard before FPM.
	if !strings.Contains(out, "if (!-f $realpath_root$jabali_pi_script) { return 404; }") {
		t.Error("PATH_INFO location missing the -f existence guard")
	}
	// SCRIPT_FILENAME must be the split script part (ends in .php), and PATH_INFO
	// the suffix — this is the whole point.
	if !strings.Contains(out, "fastcgi_param SCRIPT_FILENAME $realpath_root$jabali_pi_script;") {
		t.Error("PATH_INFO location must set SCRIPT_FILENAME to the split .php part")
	}
	if !strings.Contains(out, "fastcgi_param PATH_INFO $jabali_pi_suffix;") {
		t.Error("PATH_INFO location must set PATH_INFO to the split suffix")
	}
	// The generated end-anchored handler must still exist and come FIRST so plain
	// .php requests never reach the PATH_INFO location.
	phpIdx := strings.Index(out, `location ~ \.php$ {`)
	piIdx := strings.Index(out, "jabali_pi_script")
	if phpIdx < 0 || piIdx < 0 || phpIdx > piIdx {
		t.Errorf("the .php$ handler must precede the PATH_INFO location (phpIdx=%d piIdx=%d)", phpIdx, piIdx)
	}
}

func TestPathInfo_On_CarriesPHPValue(t *testing.T) {
	// osTicket's PATH_INFO endpoints include ajax.php attachment uploads, so the
	// domain's PHP_VALUE overrides (upload/post size) must reach this location too.
	out := renderVhostForPathInfoTest(t, true, "upload_max_filesize=64M")
	if !strings.Contains(out, `fastcgi_param PHP_VALUE "upload_max_filesize=64M";`) {
		t.Errorf("PATH_INFO location must carry PHP_VALUE when set\n%s", out)
	}
	// Three PHP_VALUE params total: location = /index.php, location ~ \.php$,
	// and the PATH_INFO location.
	if got := strings.Count(out, "fastcgi_param PHP_VALUE"); got != 3 {
		t.Errorf("PHP_VALUE count = %d, want 3 (index.php + .php$ + PATH_INFO)", got)
	}
}

func TestPathInfo_Off_ByteIdentical(t *testing.T) {
	out := renderVhostForPathInfoTest(t, false, "")
	if strings.Contains(out, "jabali_pi_script") || strings.Contains(out, "jabali_pi_suffix") {
		t.Error("PATH_INFO location must not render when the toggle is off")
	}
	if strings.Contains(out, "fastcgi_param PATH_INFO") {
		t.Error("PATH_INFO param must not appear anywhere with the toggle off")
	}
}
