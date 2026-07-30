package api

import "testing"

// JAB-176: server-capabilities must mask the real public IPs on the demo so
// the public dashboard never leaks real infra, but leave them untouched in
// production and never invent an IP where none is configured.
func TestDemoMaskIP(t *testing.T) {
	cases := []struct {
		name       string
		demo       bool
		v4, v6     string
		wantV4     string
		wantV6     string
	}{
		{"prod passes real IPs", false, "182.54.236.26", "2a01:4f8::1", "182.54.236.26", "2a01:4f8::1"},
		{"demo masks real IPs", true, "182.54.236.26", "2a01:4f8::1", "203.0.113.10", "2001:db8::10"},
		{"demo keeps empty empty", true, "", "", "", ""},
		{"prod keeps empty empty", false, "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := demoMaskIPv4(c.demo, c.v4); got != c.wantV4 {
				t.Errorf("demoMaskIPv4(%v,%q) = %q, want %q", c.demo, c.v4, got, c.wantV4)
			}
			if got := demoMaskIPv6(c.demo, c.v6); got != c.wantV6 {
				t.Errorf("demoMaskIPv6(%v,%q) = %q, want %q", c.demo, c.v6, got, c.wantV6)
			}
		})
	}
}
