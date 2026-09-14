package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestValidateNginxRules_FieldHardening (GH #1624) pins the boundary validation
// for nginx-rule fields that render UNQUOTED into the vhost: max_upload_size
// Size (client_max_body_size), ip_access IPs (allow/deny), and custom_header
// Name (add_header). These rule types are admin-only, so this is defense in
// depth plus robustness — a malformed value now fails at save time instead of
// silently failing `nginx -t` on the box.
func TestValidateNginxRules_FieldHardening(t *testing.T) {
	reject := func(name string, rule models.NginxRule) {
		t.Helper()
		if err := validateNginxRules(models.NginxRules{rule}); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
	accept := func(name string, rule models.NginxRule) {
		t.Helper()
		if err := validateNginxRules(models.NginxRules{rule}); err != nil {
			t.Errorf("%s: expected accept, got %v", name, err)
		}
	}

	// max_upload_size Size: an anchored byte-size allowlist.
	reject("size injection", models.NginxRule{Type: "max_upload_size", Size: "1m; return 204"})
	reject("size garbage", models.NginxRule{Type: "max_upload_size", Size: "50mb"})
	accept("size 50m", models.NginxRule{Type: "max_upload_size", Size: "50m"})
	accept("size bytes", models.NginxRule{Type: "max_upload_size", Size: "1048576"})

	// ip_access IPs: each entry must be a real IP or CIDR.
	reject("ip injection", models.NginxRule{Type: "ip_access", Path: "/a", Mode: "deny_list", IPs: []string{"1.2.3.4; return 204"}})
	reject("ip garbage", models.NginxRule{Type: "ip_access", Path: "/a", Mode: "deny_list", IPs: []string{"notanip"}})
	accept("ip plain", models.NginxRule{Type: "ip_access", Path: "/a", Mode: "deny_list", IPs: []string{"1.2.3.4"}})
	accept("ip cidr", models.NginxRule{Type: "ip_access", Path: "/a", Mode: "allow_list", IPs: []string{"10.0.0.0/8", "2001:db8::/32"}})

	// custom_header Name: no `{`/`}`/`#`/`"`/`\`.
	reject("name brace", models.NginxRule{Type: "custom_header", Name: "X{", Value: "v"})
	reject("name hash", models.NginxRule{Type: "custom_header", Name: "X#y", Value: "v"})
	accept("name ok", models.NginxRule{Type: "custom_header", Name: "X-Frame-Options", Value: "DENY"})
}
