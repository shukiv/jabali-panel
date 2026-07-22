package cpanel

import (
	"strings"
	"testing"
)

// GH #522: CyberPanel synthesises BIND zones from its PowerDNS records as
// "<name>. <ttl> IN <type> [<prio> ]<content>" (SOA skipped, TXT quoted).
// Assert ParseBINDZone reads that exact shape back into records.
func TestParseBINDZone_CyberPanelGeneratedFormat(t *testing.T) {
	zone := strings.Join([]string{
		"smoke.jabalitest.com. 3600 IN A 192.0.2.50",
		"www.smoke.jabalitest.com. 3600 IN A 192.0.2.50",
		"smoke.jabalitest.com. 3600 IN MX 10 mail.smoke.jabalitest.com",
		"smoke.jabalitest.com. 3600 IN TXT \"v=spf1 a mx ~all\"",
	}, "\n") + "\n"

	z, _, ok := ParseBINDZone(strings.NewReader(zone), "smoke.jabalitest.com")
	if !ok {
		t.Fatal("ParseBINDZone rejected the CyberPanel-generated zone")
	}
	byType := map[string]int{}
	var mx, txt string
	for _, r := range z.Records {
		byType[r.Type]++
		if r.Type == "MX" {
			mx = r.Content
		}
		if r.Type == "TXT" {
			txt = r.Content
		}
	}
	if byType["A"] != 2 || byType["MX"] != 1 || byType["TXT"] != 1 {
		t.Errorf("record types = %v, want 2 A + 1 MX + 1 TXT", byType)
	}
	if !strings.Contains(mx, "mail.smoke.jabalitest.com") {
		t.Errorf("MX content lost the target: %q", mx)
	}
	if !strings.Contains(txt, "v=spf1") {
		t.Errorf("TXT content lost the SPF record: %q", txt)
	}
}
