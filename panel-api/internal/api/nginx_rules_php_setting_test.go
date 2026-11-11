package api

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A php_setting nginx rule is `fastcgi_param PHP_VALUE "<name>=<value>"`, so
// it must never carry a sensitive directive (open_basedir, disable_functions,
// …; admin-only, through the domain's PHP Settings) or a value with a newline
// (PHP_VALUE reads it as a second directive). Refused for every writer: the
// admin PATCH, the tenant PATCH, .htaccess import and backup restore all go
// through these validators.
func TestValidateNginxRules_RefusesUnsafePHPSettings(t *testing.T) {
	bad := []models.NginxRule{
		{Type: "php_setting", Name: "display_errors", Value: "1\nopen_basedir=/"},
		{Type: "php_setting", Name: "display_errors", Value: "1\r\n"},
		{Type: "php_setting", Name: "display_errors\nopen_basedir", Value: "/"},
		{Type: "php_setting", Name: " open_basedir", Value: "/"},
		{Type: "php_setting", Name: "memory_limit=1G", Value: "x"},
		{Type: "php_setting", Name: "display_errors", Value: "$request_body"},
		{Type: "php_setting", Name: "display_errors", Value: "1$uri"},
		{Type: "php_setting", Name: "display_errors", Value: "${arg_x}"},
	}
	for _, name := range models.PHPSensitiveDirectives {
		bad = append(bad,
			models.NginxRule{Type: "php_setting", Name: name, Value: "x"},
			models.NginxRule{Type: "php_setting", Name: strings.ToUpper(name), Value: "x"})
	}
	// Tenants cannot write php_setting at all today; they stay in the loop so
	// that opening the type to tenants later cannot reopen these.
	validators := map[string]func(models.NginxRules) error{
		"admin":  validateNginxRules,
		"tenant": validateTenantNginxRules,
	}
	for who, validate := range validators {
		for _, r := range bad {
			if err := validate(models.NginxRules{r}); err == nil {
				t.Errorf("%s: accepted php_setting %q=%q", who, r.Name, r.Value)
			}
		}
	}
	ok := models.NginxRules{{Type: "php_setting", Name: "memory_limit", Value: "256M"}}
	if err := validateNginxRules(ok); err != nil {
		t.Errorf("admin: refused memory_limit=256M: %v", err)
	}
}
