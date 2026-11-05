package main

import (
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// render-config scopes the panel-path allowlist to the panel, as the agent's
// renders do (GH #706): the host and its public addresses, bare and with the
// panel port.
func TestAppSecRenderConfig_ScopesPanelAllowlist(t *testing.T) {
	prev := appsecPanelScope
	appsecPanelScope = func() (string, []string) { return "panel.example.com", []string{"203.0.113.5"} }
	t.Cleanup(func() { appsecPanelScope = prev })

	body := appseccfg.Render(appsecConfigOpts(appseccfg.Opts{Mode: "off", AdminAllowlist: true}))
	if !strings.Contains(body, `/dav/") && req.Host in ["panel.example.com", "panel.example.com:8443", "203.0.113.5", "203.0.113.5:8443"]`) {
		t.Fatalf("panel allowlist not scoped to the panel:\n%s", body)
	}
	if strings.Contains(body, ` - filter: req.URL.Path startsWith "/api/v1/"`) {
		t.Fatalf("panel allowlist by path alone:\n%s", body)
	}

	src, err := os.ReadFile("appsec_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "body := appseccfg.Render(appsecConfigOpts(appseccfg.Opts{") {
		t.Error("render-config does not render through appsecConfigOpts")
	}
}

// Both writers read the panel's name the same way, so neither re-render
// undoes the other.
func TestAppSecPanelScope_IsTheLocalHost(t *testing.T) {
	host, addrs := appsecPanelScope()
	if host != appseccfg.LocalPanelHost() || strings.Join(addrs, ",") != strings.Join(appseccfg.LocalPublicAddrs(), ",") {
		t.Fatalf("scope = %q %v, want this host's", host, addrs)
	}
}
