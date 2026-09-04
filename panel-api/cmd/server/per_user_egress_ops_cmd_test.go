package main

import "testing"

// parseCLIAllow now only handles the CLI string format: the field split, the
// numeric coercion of PORT, and lower-casing PROTO. The semantic accept/reject
// matrix (valid CIDR, port range, known protocol, comment safety) and the
// empty-proto default moved to egressops.ValidateDestination / SetPolicy, which
// the CLI and PUT /users/:id/egress both call — see
// internal/egressops.TestValidateDestination_Matrix and TestSetPolicy_* for the
// canonical-matrix coverage shared by both adapters.
func TestParseCLIAllow(t *testing.T) {
	ok := []struct {
		in       string
		cidr     string
		port     int    // -1 = nil
		protocol string // "" = not supplied (SetPolicy defaults it to tcp)
	}{
		{"10.0.0.0/8", "10.0.0.0/8", -1, ""},
		{"1.2.3.4/32,443", "1.2.3.4/32", 443, ""},
		{"1.2.3.4/32,53,udp", "1.2.3.4/32", 53, "udp"},
		{"1.2.3.4/32,,udp", "1.2.3.4/32", -1, "udp"},
		{" 10.0.0.0/8 , 80 , TCP ", "10.0.0.0/8", 80, "tcp"},
	}
	for _, c := range ok {
		d, err := parseCLIAllow(c.in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if d.CIDR != c.cidr || d.Protocol != c.protocol {
			t.Errorf("%q: got cidr=%q proto=%q", c.in, d.CIDR, d.Protocol)
		}
		if c.port == -1 && d.Port != nil {
			t.Errorf("%q: expected nil port, got %d", c.in, *d.Port)
		}
		if c.port != -1 && (d.Port == nil || *d.Port != c.port) {
			t.Errorf("%q: expected port %d", c.in, c.port)
		}
	}

	// Only the CLI-string-format errors surface here; a non-numeric port and
	// too-many-fields. Bad CIDR / port range / bad protocol are rejected later
	// by egressops.ValidateDestination, not by the parser.
	bad := []string{
		"1.2.3.4/32,abc",       // non-numeric port
		"1.2.3.4/32,443,tcp,x", // too many fields
	}
	for _, b := range bad {
		if _, err := parseCLIAllow(b); err == nil {
			t.Errorf("%q: expected error, got nil", b)
		}
	}
}
