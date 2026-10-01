package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const testOwnershipGate = "0123456789abcdef0123456789abcdef"

const ownershipGateLine = `if ($http_x_jabali_preview_gate != "` + testOwnershipGate + `") { return 444; }`

func ownershipGateVD() vhostData {
	vd := previewVD()
	vd.RedirectHTTPS = false
	vd.ServeHTTPS = true
	vd.OwnershipGate = testOwnershipGate
	return vd
}

// GH #1816: while a name is unproven, both real-name server blocks drop
// every request that lacks the gate header, before any location (ACME
// included) is chosen; the preview proxy sends the header and its own
// server blocks are not gated.
func TestVhostTemplate_OwnershipGateDropsRealNameRequests(t *testing.T) {
	vd := ownershipGateVD()
	vd.PreviewCertPath = "/etc/jabali/ssl/preview/fullchain.pem"
	vd.PreviewKeyPath = "/etc/jabali/ssl/preview/privkey.pem"
	out := mustRenderVhost(t, vd)

	if got := strings.Count(out, ownershipGateLine); got != 2 {
		t.Fatalf("the gate must guard the :80 and :443 real-name blocks (2), got %d:\n%s", got, out)
	}
	if got := strings.Count(out, "return 444;"); got != 2 {
		t.Fatalf("only the two real-name blocks may return 444, got %d", got)
	}
	if !strings.Contains(out, "proxy_set_header X-Jabali-Preview-Gate "+testOwnershipGate+";") {
		t.Fatal("the preview proxy must send the gate header")
	}

	// The gate sits in each real-name block, ahead of every location.
	blocks := strings.Split(out, "\nserver {")
	gated := 0
	for _, b := range blocks {
		if !strings.Contains(b, "server_name example.com www.example.com") {
			continue
		}
		gate := strings.Index(b, ownershipGateLine)
		loc := strings.Index(b, "location ")
		if gate < 0 {
			t.Fatalf("a real-name block has no gate:\n%s", b)
		}
		if loc >= 0 && loc < gate {
			t.Fatalf("the gate must come before the first location:\n%s", b)
		}
		gated++
	}
	if gated != 2 {
		t.Fatalf("want 2 gated real-name blocks, got %d", gated)
	}
	for _, b := range blocks {
		if strings.Contains(b, "server_name example-com.preview.host.tld") && strings.Contains(b, "return 444") {
			t.Fatalf("a preview block must not be gated:\n%s", b)
		}
	}
}

// Without a gate (a verified domain) the vhost carries no trace of it.
func TestVhostTemplate_NoOwnershipGateRendersNothing(t *testing.T) {
	vd := ownershipGateVD()
	vd.OwnershipGate = ""
	out := mustRenderVhost(t, vd)
	for _, s := range []string{"x_jabali_preview_gate", "X-Jabali-Preview-Gate", "return 444"} {
		if strings.Contains(out, s) {
			t.Fatalf("a verified domain's vhost must not contain %q", s)
		}
	}
}

// The gate is written into an nginx if() condition, so anything but
// lowercase hex is refused before a vhost is rendered.
func TestDomainCreateHandler_RejectsInvalidOwnershipGate(t *testing.T) {
	t.Parallel()
	for _, gate := range []string{
		`0123456789abcdef0123456789abcde"; } location / { return 200; } #`,
		"0123456789ABCDEF0123456789ABCDEF",
		"0123456789abcdef",
		strings.Repeat("a", 65),
		"0123456789abcdef0123456789abcdeg",
		"0123456789abcdef 0123456789abcdef",
	} {
		params := domainCreateParams{
			Username:      "testuser",
			Domain:        "example.com",
			DocRoot:       "/home/testuser/domains/example.com/public_html",
			PHPVersion:    "8.3",
			OwnershipGate: gate,
		}
		raw, _ := json.Marshal(params)
		_, err := domainCreateHandler(context.Background(), raw)
		var aerr *agentwire.AgentError
		if err == nil || !errors.As(err, &aerr) || aerr.Code != agentwire.CodeInvalidArgument ||
			!strings.Contains(aerr.Message, "ownership_gate") {
			t.Fatalf("gate %q: want an invalid ownership_gate error, got %v", gate, err)
		}
	}
}
