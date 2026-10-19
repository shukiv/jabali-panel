package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestValidateProxyPassTarget_AllowInternal pins the nginx_proxy_allow_internal
// policy: on, loopback / private-range targets and localhost pass; unix
// sockets, link-local (cloud metadata), unspecified and directive smuggling
// stay rejected. Off is the JAB-65 behaviour.
func TestValidateProxyPassTarget_AllowInternal(t *testing.T) {
	internal := []string{
		"http://127.0.0.1:3000",
		"http://localhost:8080/app",
		"http://10.0.0.5",
		"http://192.168.1.20:9000",
		"http://172.16.4.4",
		"http://[::1]:3000",
	}
	for _, tg := range internal {
		if err := validateProxyPassTargetWith(tg, false); err == nil {
			t.Errorf("allow off: %q must be rejected", tg)
		}
		if err := validateProxyPassTargetWith(tg, true); err != nil {
			t.Errorf("allow on: %q should pass, got %v", tg, err)
		}
	}
	for _, tg := range []string{
		"http://unix:/run/php/x.sock:/",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0:80",
		"http://127.0.0.1/; proxy_pass http://169.254.169.254; #",
	} {
		if err := validateProxyPassTargetWith(tg, true); err == nil {
			t.Errorf("allow on: %q must still be rejected", tg)
		}
	}

	rules := models.NginxRules{{Type: "proxy_pass", Path: "/app", Target: "http://127.0.0.1:3000"}}
	if err := validateNginxRules(rules); err == nil {
		t.Errorf("validateNginxRules must stay strict")
	}
	if err := validateNginxRulesWith(rules, true); err != nil {
		t.Errorf("validateNginxRulesWith(allow) should pass, got %v", err)
	}
}
