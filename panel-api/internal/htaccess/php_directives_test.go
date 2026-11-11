package htaccess

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// cpanelMultiPHPBlock is the shape cPanel's MultiPHP INI editor writes into a
// site's .htaccess: the same directives once per PHP handler module.
const cpanelMultiPHPBlock = `# BEGIN cPanel-generated php ini directives, do not edit
<IfModule php7_module>
   php_flag display_errors Off
   php_value max_execution_time 30
   php_value memory_limit 256M
   php_value session.save_path "/var/cpanel/php/sessions/ea-php74"
</IfModule>
<IfModule lsapi_module>
   php_flag display_errors Off
   php_value max_execution_time 30
   php_value memory_limit 256M
   php_value session.save_path "/var/cpanel/php/sessions/ea-php74"
</IfModule>
# END cPanel-generated php ini directives, do not edit`

func phpSettings(r Result) map[string][]string {
	out := map[string][]string{}
	for _, rule := range r.Rules {
		if rule.Type == "php_setting" {
			out[strings.ToLower(rule.Name)] = append(out[strings.ToLower(rule.Name)], rule.Value)
		}
	}
	return out
}

func warnedAbout(r Result, needle string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w.Source, needle) {
			return true
		}
	}
	return false
}

// One rule per directive, and the session path is not carried over.
func TestCpanelMultiPHPBlock_OneRulePerDirective(t *testing.T) {
	r := Convert(cpanelMultiPHPBlock, "/")
	got := phpSettings(r)
	for name, want := range map[string]string{
		"display_errors":     "Off",
		"max_execution_time": "30",
		"memory_limit":       "256M",
	} {
		if v := got[name]; len(v) != 1 || v[0] != want {
			t.Errorf("%s: got %q, want exactly one rule %q", name, v, want)
		}
	}
	if v, ok := got["session.save_path"]; ok {
		t.Errorf("session.save_path converted: %q", v)
	}
	if !warnedAbout(r, "session.save_path") {
		t.Errorf("refused session.save_path must be warned about: %+v", r.Warnings)
	}
}

// None of the directives the panel keeps admin-only may become a site rule,
// whatever the value and however it is spelled.
func TestSensitivePHPDirectivesRefused(t *testing.T) {
	for _, name := range models.PHPSensitiveDirectives {
		for _, directive := range []string{"php_value", "php_admin_value", "php_flag"} {
			line := directive + " " + strings.ToUpper(name) + " 1"
			r := Convert(line, "/")
			if len(phpSettings(r)) != 0 {
				t.Errorf("%q converted to %+v", line, r.Rules)
			}
			if len(r.Warnings) == 0 {
				t.Errorf("%q dropped without a warning", line)
			}
		}
	}
}

// A value pointing into the source panel's own tree is dead on Jabali.
func TestCpanelPathValueRefused(t *testing.T) {
	for _, v := range []string{
		"/var/cpanel/php/sessions/ea-php81",
		"/opt/cpanel/ea-php81/root/usr/share/pear",
		"/usr/local/cpanel/logs/error_log",
		"\".:/opt/cpanel/ea-php74/root/usr/share/pear\"",
	} {
		line := "php_value error_log " + v
		r := Convert(line, "/")
		if len(phpSettings(r)) != 0 {
			t.Errorf("%q converted to %+v", line, r.Rules)
		}
		if len(r.Warnings) == 0 {
			t.Errorf("%q dropped without a warning", line)
		}
	}
}

// Apache applies the last php_value for a name; the converted rules must too,
// as a single rule, whatever the case of the name.
func TestDuplicatePHPSettingLastWins(t *testing.T) {
	r := Convert("php_value memory_limit 128M\nphp_value upload_max_filesize 8M\nphp_value MEMORY_LIMIT 512M", "/")
	got := phpSettings(r)
	if v := got["memory_limit"]; len(v) != 1 || v[0] != "512M" {
		t.Fatalf("memory_limit: got %q, want one rule 512M", v)
	}
	if v := got["upload_max_filesize"]; len(v) != 1 || v[0] != "8M" {
		t.Fatalf("upload_max_filesize: got %q", v)
	}
}

// Ordinary settings still convert.
func TestOrdinaryPHPSettingStillConverts(t *testing.T) {
	r := Convert("php_value memory_limit 256M\nphp_flag display_errors off", "/")
	got := phpSettings(r)
	if v := got["memory_limit"]; len(v) != 1 || v[0] != "256M" {
		t.Fatalf("memory_limit: got %q", v)
	}
	if v := got["display_errors"]; len(v) != 1 || v[0] != "off" {
		t.Fatalf("display_errors: got %q", v)
	}
}
