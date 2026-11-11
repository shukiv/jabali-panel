package nginxrules

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A php_setting rule renders as `fastcgi_param PHP_VALUE "<name>=<value>"`.
// Rules already stored before the validator refused them must not reach
// PHP-FPM: a sensitive directive (open_basedir, disable_functions, …) or a
// value carrying a newline, which PHP_VALUE reads as a second directive.
// They are skipped; the safe rules around them still render.
func TestCompile_SkipsUnsafeStoredPHPSettings(t *testing.T) {
	rules := []models.NginxRule{{Type: "php_setting", Name: "memory_limit", Value: "256M"}}
	for _, name := range models.PHPSensitiveDirectives {
		rules = append(rules,
			models.NginxRule{Type: "php_setting", Name: name, Value: "x"},
			models.NginxRule{Type: "php_setting", Name: strings.ToUpper(name), Value: "x"})
	}
	rules = append(rules,
		models.NginxRule{Type: "php_setting", Name: "display_errors", Value: "1\nopen_basedir=/"},
		models.NginxRule{Type: "php_setting", Name: "display_errors\nopen_basedir", Value: "/"},
		models.NginxRule{Type: "php_setting", Name: "upload_max_filesize", Value: "64M"},
	)
	got := Compile(&models.Domain{Name: "main.example", NginxRules: rules})

	for _, want := range []string{`PHP_VALUE "memory_limit=256M"`, `PHP_VALUE "upload_max_filesize=64M"`} {
		if !strings.Contains(got, want) {
			t.Errorf("safe rule lost: want %s in:\n%s", want, got)
		}
	}
	low := strings.ToLower(got)
	for _, name := range models.PHPSensitiveDirectives {
		if strings.Contains(low, strings.ToLower(name)) {
			t.Errorf("sensitive directive %s rendered:\n%s", name, got)
		}
	}
	if strings.Contains(got, "display_errors") {
		t.Errorf("a rule with a newline rendered:\n%s", got)
	}
	if n := strings.Count(got, "PHP_VALUE"); n != 2 {
		t.Errorf("%d PHP_VALUE lines, want 2:\n%s", n, got)
	}
}

// nginx expands $variables inside a fastcgi_param value, so a value holding
// `$` would carry request-controlled text (newlines included) into
// PHP_VALUE. Stored rules like that are skipped.
func TestCompile_SkipsStoredPHPSettingWithNginxVariable(t *testing.T) {
	for _, v := range []string{"$request_body", "1$uri", "${arg_x}", "$http_x_set"} {
		got := Compile(&models.Domain{Name: "main.example", NginxRules: []models.NginxRule{
			{Type: "php_setting", Name: "display_errors", Value: v},
			{Type: "php_setting", Name: "memory_limit", Value: "256M"},
		}})
		if strings.Contains(got, "display_errors") || strings.Contains(got, "$") {
			t.Errorf("%q rendered:\n%s", v, got)
		}
		if !strings.Contains(got, `PHP_VALUE "memory_limit=256M"`) {
			t.Errorf("%q: safe rule lost:\n%s", v, got)
		}
	}
}
