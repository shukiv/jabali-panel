package nginxrules

import (
	"fmt"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// phpIniNameRe is the shape of a PHP ini directive name.
var phpIniNameRe = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

// PHPSettingRefusal says why a php_setting rule must not be stored or
// rendered, or "" when it may. The rule becomes
// `fastcgi_param PHP_VALUE "<name>=<value>"`, so:
//   - the name must be a plain ini directive name;
//   - it must not be one of models.PHPSensitiveDirectives (any case): those
//     are admin-only and set through the domain's PHP Settings, never as a
//     PHP_VALUE a request carries into a shared worker;
//   - the value must not hold a control character: PHP_VALUE reads a newline
//     as the start of a second directive.
//
// The validator refuses such a rule for every writer, and Compile skips one
// that was stored before.
func PHPSettingRefusal(name, value string) string {
	if !phpIniNameRe.MatchString(name) {
		return fmt.Sprintf("%q is not a PHP ini directive name (letters, digits, '_' and '.')", name)
	}
	for _, s := range models.PHPSensitiveDirectives {
		if strings.EqualFold(name, s) {
			return fmt.Sprintf("%s is a sensitive PHP directive and can't be set by an nginx rule; set it in the domain's PHP Settings", s)
		}
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "the value must not contain control characters (a newline would set a second PHP directive)"
	}
	return ""
}
