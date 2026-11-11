package htaccess

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/nginxrules"
)

// Every php_setting the converter emits must pass the nginx-rules validator
// (nginxrules.PHPSettingRefusal). Otherwise one line it let through — a value
// holding '$', which nginx would expand from the request — makes the
// validator refuse the whole .htaccess import. Such a line is dropped here,
// with one warning, like the other refused directives.
func TestConvertPHP_AgreesWithTheNginxRulesValidator(t *testing.T) {
	refused := []string{
		`php_value error_log "$document_root/error.log"`,
		`php_value display_errors $arg_x`,
		`php_flag bad-name on`,
	}
	for _, line := range refused {
		r := Convert(line, "/")
		if got := phpSettings(r); len(got) != 0 {
			t.Errorf("%q converted to %+v", line, got)
		}
		if len(r.Warnings) != 1 {
			t.Errorf("%q: %d warnings, want 1: %+v", line, len(r.Warnings), r.Warnings)
		}
	}

	// A sensitive directive is refused once, with the converter's own reason.
	r := Convert("php_value open_basedir /", "/")
	if len(r.Warnings) != 1 {
		t.Errorf("sensitive directive: %d warnings, want 1: %+v", len(r.Warnings), r.Warnings)
	}

	mixed := "php_value memory_limit 256M\nphp_flag display_errors off\n" +
		"php_value upload_max_filesize 64M\n" + refused[0] + "\nphp_value include_path /x\n"
	res := Convert(mixed, "/")
	for _, rule := range res.Rules {
		if rule.Type != "php_setting" {
			continue
		}
		if why := nginxrules.PHPSettingRefusal(rule.Name, rule.Value); why != "" {
			t.Errorf("converter emitted %s=%q, which the validator refuses: %s", rule.Name, rule.Value, why)
		}
	}
	if n := len(phpSettings(res)); n != 3 {
		t.Errorf("%d php_setting rules, want 3 (memory_limit, display_errors, upload_max_filesize)", n)
	}
}
