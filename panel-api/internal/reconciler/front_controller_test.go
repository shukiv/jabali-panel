package reconciler

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1999: a domain's front_controller rule reaches the agent as the
// php_fallback param, never as a server-scope `location /` in rule_directives
// (which would make the agent drop its PHP locations).
func TestCreateDomainOnAgent_SendsFrontControllerFallback(t *testing.T) {
	for _, c := range []struct {
		name  string
		rules models.NginxRules
		want  string
	}{
		{"no rule", nil, ""},
		{"rule", models.NginxRules{{Type: "front_controller", Script: "/index.php", Query: "mod=$uri&$args"}}, "/index.php?mod=$uri&$args"},
		{"invalid stored rule", models.NginxRules{{Type: "front_controller", Script: "/index.php", Query: "a=1; deny all"}}, ""},
	} {
		r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
		dom.NginxRules = c.rules
		r.createDomainOnAgent(context.Background(), dom, true)
		call, ok := findAgentCall(ag, "domain.create")
		if !ok {
			t.Fatalf("%s: domain.create was not dispatched", c.name)
		}
		params := call.params.(map[string]any)
		if params["php_fallback"] != c.want {
			t.Errorf("%s: php_fallback = %v, want %q", c.name, params["php_fallback"], c.want)
		}
		if rd, _ := params["rule_directives"].(string); strings.Contains(rd, "location /") || strings.Contains(rd, "try_files") {
			t.Errorf("%s: front controller leaked into rule_directives: %q", c.name, rd)
		}
	}
}
