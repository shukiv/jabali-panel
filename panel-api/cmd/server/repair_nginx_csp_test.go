package main

import (
	"os"
	"strings"
	"testing"
)

// repair_nginx_csp_test.go — `jabali repair` heals a missing panel CSP snippet
// with the chat-off policy, never with an empty file (an empty snippet would
// serve the panel with no Content-Security-Policy at all).

const chatOffCSP = `add_header Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://gravatar.com https://secure.gravatar.com https://www.gravatar.com https://seccdn.libravatar.org; font-src 'self' data:; connect-src 'self' wss:; frame-ancestors 'none'; form-action 'self';" always;`

func TestPanelCSPSeed_IsTheChatOffPolicyFromInstallSh(t *testing.T) {
	raw, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	before := string(raw)
	seed, err := panelCSPSeed(raw)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !strings.Contains(string(seed), chatOffCSP+"\n") {
		t.Fatalf("seed is not the chat-off policy:\n%s", seed)
	}
	if strings.Contains(string(seed), "support.jabali-panel.com") {
		t.Fatal("the repair seed must not allow the support chat")
	}
	if string(raw) != before {
		t.Fatal("panelCSPSeed must not modify the installer bytes it was given")
	}
}

func TestPanelCSPSeed_RefusesToWriteNoPolicy(t *testing.T) {
	if _, err := panelCSPSeed([]byte("#!/bin/bash\necho no seed here\n")); err == nil {
		t.Error("an installer without the seed must be refused")
	}
	empty := "cat > x <<'PANEL_CSP_OFF'\n# comment only\nPANEL_CSP_OFF\n"
	if _, err := panelCSPSeed([]byte(empty)); err == nil {
		t.Error("a seed without a Content-Security-Policy must be refused")
	}
}

func TestPanelCSPSnippetMissing(t *testing.T) {
	vhost := "server {\n  include /etc/nginx/snippets/jabali-panel-csp.conf;\n}\n"
	absent := func(string) bool { return false }
	present := func(string) bool { return true }
	if !panelCSPSnippetMissing(vhost, absent) {
		t.Error("an included, absent snippet must be reported")
	}
	if panelCSPSnippetMissing(vhost, present) {
		t.Error("a present snippet is not missing")
	}
	if panelCSPSnippetMissing("server {\n  add_header Content-Security-Policy \"x\" always;\n}\n", absent) {
		t.Error("an older vhost that does not include the snippet needs nothing")
	}
}

// Not one of the empty GH #217 placeholders: that fix writes empty files.
func TestPanelCSPSnippet_IsNotAnEmptyPlaceholder(t *testing.T) {
	for _, p := range managedNginxIncludePlaceholders {
		if p == panelCSPSnippetPath {
			t.Fatal("the CSP snippet must never be healed with an empty placeholder")
		}
	}
}
