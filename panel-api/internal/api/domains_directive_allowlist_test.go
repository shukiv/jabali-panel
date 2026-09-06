package api

import (
	"strings"
	"testing"
)

// JAB-66: file-reading / injection / SSRF directives must be off the raw
// custom-directives allowlist; safe directives still pass.
func TestValidateNginxDirectives_UnsafeRemoved(t *testing.T) {
	forbidden := map[string]string{
		"auth_basic_user_file": "auth_basic_user_file /etc/shadow;",
		"auth_basic":           `auth_basic "x";`,
		"sub_filter":           `sub_filter "a" "b";`,
		"sub_filter_once":      "sub_filter_once off;",
		"sub_filter_types":     "sub_filter_types text/html;",
		"proxy_pass":           "proxy_pass http://127.0.0.1:3306;",
	}
	for name, line := range forbidden {
		if msg := ValidateNginxDirectives(line); msg == "" || !strings.Contains(msg, "forbidden directive") {
			t.Errorf("directive %s should be forbidden, got %q", name, msg)
		}
	}

	// Sanity: a still-allowed directive passes.
	if msg := ValidateNginxDirectives("proxy_set_header X-Test 1;"); msg != "" {
		t.Errorf("proxy_set_header should still be allowed, got %q", msg)
	}
}
