package main

import "testing"

// JAB-17: the flip-mature soak gate must recognise a still-denying profile
// (so it refuses to flip it into the #705 crash-loop) and must not
// cross-match a different profile's denials.
func TestCountApparmorDenials(t *testing.T) {
	log := `Jul 12 00:01:02 mx kernel: audit: type=1400 audit(1.2:3): apparmor="DENIED" operation="open" profile="jabali-panel" name="/etc/secret" pid=42 comm="jabali-panel" requested_mask="r" denied_mask="r"
Jul 12 00:01:03 mx kernel: audit: type=1400 audit(1.2:4): apparmor="ALLOWED" operation="open" profile="jabali-panel" name="/ok"
Jul 12 00:02:00 mx kernel: audit: type=1400 audit(1.2:5): apparmor="DENIED" operation="connect" profile="jabali-panel" name="/run/x.sock"
Jul 12 00:03:00 mx kernel: audit: type=1400 audit(1.2:6): apparmor="DENIED" operation="exec" profile="stalwart-mail" name="/usr/bin/foo"`

	// jabali-panel: 2 denials (the ALLOWED line and the stalwart line excluded).
	n, sample := countApparmorDenials(log, "jabali-panel")
	if n != 2 {
		t.Fatalf("jabali-panel denials = %d, want 2", n)
	}
	if sample == "" {
		t.Error("expected a sample denial line")
	}

	// A different profile's denials must not count toward jabali-panel.
	if n, _ := countApparmorDenials(log, "jabali-bulwark"); n != 0 {
		t.Errorf("jabali-bulwark denials = %d, want 0 (no cross-match)", n)
	}

	// stalwart-mail has exactly one.
	if n, _ := countApparmorDenials(log, "stalwart-mail"); n != 1 {
		t.Errorf("stalwart-mail denials = %d, want 1", n)
	}

	// A clean log → zero → soak-clean → flip allowed.
	clean := `Jul 12 00:01:02 mx kernel: audit: apparmor="ALLOWED" profile="jabali-panel" name="/ok"`
	if n, _ := countApparmorDenials(clean, "jabali-panel"); n != 0 {
		t.Errorf("clean log denials = %d, want 0", n)
	}
}
