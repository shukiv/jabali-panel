package commands

import (
	"strings"
	"testing"
)

// GH #1798: AllowPing renders an echo-request accept (v4 + v6) into the user's
// chain — and ONLY echo-request, not the whole ICMP protocol.

func TestRenderEgressNFT_AllowPingEmitsEchoRequest(t *testing.T) {
	always := func(string) bool { return true }
	users := []EgressUser{
		{Username: "pinguser", State: "enforced", UID: 5001, AllowPing: true},
	}
	out := RenderEgressNFT(users, CanonicalDefaults(), always)

	if !strings.Contains(out, "icmp type echo-request accept") {
		t.Errorf("AllowPing must emit IPv4 echo-request accept\n%s", out)
	}
	if !strings.Contains(out, "icmpv6 type echo-request accept") {
		t.Errorf("AllowPing must emit IPv6 echo-request accept\n%s", out)
	}
	// Scope guard: never open the whole ICMP protocol.
	if strings.Contains(out, "ip protocol icmp accept") {
		t.Errorf("must NOT allow the whole ICMP protocol, only echo-request\n%s", out)
	}
}

func TestRenderEgressNFT_NoAllowPingNoICMP(t *testing.T) {
	always := func(string) bool { return true }
	users := []EgressUser{
		{Username: "noping", State: "enforced", UID: 5002, AllowPing: false},
	}
	out := RenderEgressNFT(users, CanonicalDefaults(), always)

	if strings.Contains(out, "echo-request") {
		t.Errorf("AllowPing=false must emit no ICMP rule\n%s", out)
	}
}
