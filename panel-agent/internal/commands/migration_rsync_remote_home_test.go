package commands

import (
	"strings"
	"testing"
)

// GH #429 follow-up: caller-provided excludes are argv-direct (no shell) but
// must stay relative and traversal-free — they may only SHRINK the copy.
func TestValidateRsyncExclude(t *testing.T) {
	for _, ok := range []string{"logs/", "httpdocs/", "site1/", "statistics/", "*.tmp", "private/cache/"} {
		if msg := validateRsyncExclude(ok); msg != "" {
			t.Errorf("validateRsyncExclude(%q) = %q, want accepted", ok, msg)
		}
	}
	for _, bad := range []string{"", "/etc", "../up", "a/../b", strings.Repeat("x", 301)} {
		if msg := validateRsyncExclude(bad); msg == "" {
			t.Errorf("validateRsyncExclude(%q) accepted, want rejected", bad)
		}
	}
}
