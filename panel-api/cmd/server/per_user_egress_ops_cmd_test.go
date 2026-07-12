package main

import "testing"

func TestParseCLIAllow(t *testing.T) {
	ok := []struct {
		in       string
		cidr     string
		port     int // -1 = nil
		protocol string
	}{
		{"10.0.0.0/8", "10.0.0.0/8", -1, "tcp"},
		{"1.2.3.4/32,443", "1.2.3.4/32", 443, "tcp"},
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

	bad := []string{
		"",                     // empty
		"not-a-cidr",           // bare token
		"1.2.3.4",              // IP without mask (ParseCIDR requires /n)
		"1.2.3.4/32,99999",     // port out of range
		"1.2.3.4/32,0",         // port 0
		"1.2.3.4/32,443,sctp",  // bad protocol
		"1.2.3.4/32,443,tcp,x", // too many fields
	}
	for _, b := range bad {
		if _, err := parseCLIAllow(b); err == nil {
			t.Errorf("%q: expected error, got nil", b)
		}
	}
}

func TestValidCLIEgressState(t *testing.T) {
	for _, s := range []string{"off", "learning", "enforced"} {
		if !validCLIEgressState(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "ENFORCED", "on", "block"} {
		if validCLIEgressState(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
