package main

import "testing"

// GH #331 Step 4: the split-brain gate is the safety-critical part of promote.
func TestPromotionGate(t *testing.T) {
	cases := []struct {
		name        string
		isStandby   bool
		peerAlive   bool
		force       bool
		wantAllowed bool
	}{
		{"not a standby is refused", false, false, false, false},
		{"not a standby refused even with force", false, false, true, false},
		{"standby, peer down → promote", true, false, false, true},
		{"standby, peer ALIVE, no force → refuse (split-brain)", true, true, false, false},
		{"standby, peer alive, force → promote", true, true, true, true},
		{"standby, peer down, force → promote", true, false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allowed, reason := promotionGate(c.isStandby, c.peerAlive, c.force)
			if allowed != c.wantAllowed {
				t.Fatalf("promotionGate(standby=%v,alive=%v,force=%v) allowed=%v want %v (reason=%q)",
					c.isStandby, c.peerAlive, c.force, allowed, c.wantAllowed, reason)
			}
			if !allowed && reason == "" {
				t.Error("a refusal must carry a reason")
			}
			if allowed && reason != "" {
				t.Errorf("an allow must have no reason, got %q", reason)
			}
		})
	}
}

// tcpPeerAlive must treat an unusable label as "not alive" without dialing, so a
// missing/freeform peer label never blocks promotion on its own.
func TestTCPPeerAlive_UnusableLabel(t *testing.T) {
	for _, bad := range []string{"", "   ", "not a host", "host/with/slash", "host:1234"} {
		if tcpPeerAlive(t.Context(), bad) {
			t.Errorf("label %q must be treated as not-alive", bad)
		}
	}
}
