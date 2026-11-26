package appseccfg

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The after-plugin may only take named cookies out of one rule's targets. A
// rule removal, an action or a path match here would apply to every site on
// every server, after all the CRS rules, so anything but that one form fails.
func TestCRSPluginAfter_OnlyNarrowsCookieTargets(t *testing.T) {
	allowed := regexp.MustCompile(`^SecRuleUpdateTargetById 9[0-9]{5} "!REQUEST_COOKIES:/\^[A-Za-z0-9_]+/"$`)
	body := CRSPluginAfter()
	directives := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		directives++
		if !allowed.MatchString(line) {
			t.Errorf("after-plugin line is not a single-rule cookie exclusion: %q", line)
		}
	}
	if !strings.Contains(body, `SecRuleUpdateTargetById 932230 "!REQUEST_COOKIES:/^sbjs_/"`) {
		t.Error("after-plugin lost the WooCommerce sourcebuster (sbjs_*) exclusion from 932230")
	}
	if directives == 0 {
		t.Error("after-plugin has no directives")
	}
}

// crs.yaml loads crs-plugins/*/*-after.conf after the CRS rules; a file outside
// that glob, or one the before glob also matched, would load at the wrong time.
func TestCRSPluginAfterPath_MatchesAfterGlob(t *testing.T) {
	if filepath.Dir(CRSPluginAfterPath) != filepath.Dir(CRSPluginBeforePath) {
		t.Errorf("after-plugin %s is not in the jabali/ plugin dir", CRSPluginAfterPath)
	}
	if !strings.HasSuffix(CRSPluginAfterPath, "-after.conf") || strings.HasSuffix(CRSPluginAfterPath, "-before.conf") {
		t.Errorf("after-plugin %s does not match crs-plugins/*/*-after.conf", CRSPluginAfterPath)
	}
}
