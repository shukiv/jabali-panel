package tui

import "testing"

func TestStripANSI(t *testing.T) {
	in := "\x1b[1;34m[i]\x1b[0m installing PowerDNS"
	if got := stripANSI(in); got != "[i] installing PowerDNS" {
		t.Errorf("stripANSI = %q", got)
	}
}

func TestPhaseFromLine(t *testing.T) {
	p, ok := phaseFromLine("\x1b[1;34m[i]\x1b[0m installing PowerDNS")
	if !ok || p != "installing PowerDNS" {
		t.Errorf("phaseFromLine = %q ok=%v", p, ok)
	}
	if _, ok := phaseFromLine("\x1b[1;32m[✓]\x1b[0m done"); ok {
		t.Error("a [✓] ok-line should not set a new phase")
	}
	if _, ok := phaseFromLine("plain line"); ok {
		t.Error("a non-marker line should not set a phase")
	}
}

func TestCaptureSummary(t *testing.T) {
	m := &Model{}
	lines := []string{
		"============================================================",
		"  JABALI PANEL  —  installed",
		"============================================================",
		"",
		"  URL:       https://host:8443",
		"  Username:  admin",
		"  Password:  s3cr3t",
		"",
		"  > Log in at the URL above using the HOSTNAME, not the server IP.",
		"============================================================",
		"",
	}
	for _, l := range lines {
		m.captureSummary(l)
	}
	if len(m.summary) == 0 {
		t.Fatal("no summary captured")
	}
	joined := ""
	for _, s := range m.summary {
		joined += s + "\n"
	}
	for _, want := range []string{"https://host:8443", "Username:  admin", "Password:  s3cr3t"} {
		if !contains(joined, want) {
			t.Errorf("summary missing %q; got:\n%s", want, joined)
		}
	}
}

// A tenant seeded at install time gets its own labelled fields, after the
// admin's, instead of being dropped from the card.
func TestRenderSummaryCardTenant(t *testing.T) {
	card := stripANSI(renderSummaryCard([]string{
		"  URL:       https://host:8443",
		"  Username:  admin",
		"  Password:  admin-pw-placeholder",
		"  Tenant:    tenant",
		"  Tenant password:  tenant-pw-placeholder",
		"  > Change this password immediately after first login.",
	}, 80))
	admin, label, user, pw := indexOf(card, "admin-pw-placeholder"), indexOf(card, "Tenant"),
		indexOf(card, "tenant\n"), indexOf(card, "tenant-pw-placeholder")
	if admin < 0 || label < admin || user < label || pw < user {
		t.Errorf("want the tenant's login and password after the admin's; got:\n%s", card)
	}
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
