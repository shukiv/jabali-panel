package models

import (
	"encoding/json"
	"testing"
)

// GH #2262: the client address goes into a varbinary(16) column. As text an
// IPv6 address is longer than 16 bytes and the insert failed, so it is
// stored packed; rows from before hold text, and both read back.
func TestSnuffleupagusSourceIP_RoundTrips(t *testing.T) {
	for _, addr := range []string{"203.0.113.9", "0.0.0.0", "2001:db8:85a3::8a2e:370:7334"} {
		b := SnuffleupagusSourceIP(addr)
		if len(b) != 16 {
			t.Fatalf("%s packed to %d bytes, want 16", addr, len(b))
		}
		if got := (SnuffleupagusIncident{SourceIP: b}).SourceIPString(); got != addr {
			t.Errorf("packed %s read back as %q", addr, got)
		}
	}
	if b := SnuffleupagusSourceIP("not-an-ip"); b != nil {
		t.Errorf("non-address packed to %v, want nil", b)
	}
	if got := (SnuffleupagusIncident{SourceIP: []byte("198.51.100.4")}).SourceIPString(); got != "198.51.100.4" {
		t.Errorf("text address from an older row read back as %q", got)
	}
}

// The admin API and `jabali php-defense incidents` print incidents as JSON;
// source_ip was json:"-", so the Source column showed every request as CLI.
func TestSnuffleupagusIncident_JSONCarriesSourceIPAndDomain(t *testing.T) {
	line := "[snuffleupagus][2001:db8::1][disabled_function][drop] Aborted execution on call of the function 'system' in /home/alice/domains/shop.example.com/public_html/x.php on line 4"
	ver, site := "8.4", "shop.example.com"
	b, err := json.Marshal(SnuffleupagusIncident{
		ID:         7,
		RuleName:   "sp.disabled_function:system",
		Action:     SnuffleupagusActionBlock,
		SourceIP:   SnuffleupagusSourceIP("2001:db8::1"),
		PhpVersion: &ver,
		Domain:     &site,
		Raw:        &line,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"id": float64(7), "rule_name": "sp.disabled_function:system", "action": "block",
		"source_ip": "2001:db8::1", "domain": "shop.example.com", "php_version": "8.4",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v (json %s)", k, got[k], want, b)
		}
	}
}
