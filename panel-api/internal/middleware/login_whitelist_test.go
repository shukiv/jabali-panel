package middleware

import "testing"

func TestWhitelistableIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"203.0.113.7", true},          // public v4
		{"2606:4700:4700::1111", true}, // public v6
		{"127.0.0.1", false},           // loopback
		{"::1", false},                 // loopback v6
		{"10.0.0.5", true},             // private LAN/VPN — now allowlisted (admin-gated)
		{"192.168.1.10", true},         // private LAN/VPN — now allowlisted
		{"172.16.4.2", true},           // private LAN/VPN — now allowlisted
		{"169.254.1.1", false},         // link-local
		{"100.64.0.1", false},          // CGNAT / tailnet
		{"100.127.255.254", false},     // CGNAT upper edge
		{"100.128.0.1", true},          // just past CGNAT
		{"::ffff:100.64.0.1", false},   // CGNAT, v4-mapped
		{"0.0.0.0", false},             // unspecified
		{"", false},                    // empty
		{"not-an-ip", false},           // garbage
	}
	for _, c := range cases {
		if got := whitelistableIP(c.ip); got != c.want {
			t.Errorf("whitelistableIP(%q) = %v, want %v", c.ip, got, c.want)
		}
	}
}
