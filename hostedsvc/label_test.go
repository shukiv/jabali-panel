package hostedsvc

import (
	"net"
	"testing"
)

func TestLabelFromIP(t *testing.T) {
	tests := []struct {
		ip      string
		want    string
		wantErr bool
	}{
		{"45.79.1.2", "45-79-1-2", false},          // ordinary public v4
		{"182.54.236.100", "182-54-236-100", false}, // the hostsclick box
		{"192.168.100.165", "", true},               // RFC1918 — rebinding lure
		{"10.0.3.14", "", true},                     // RFC1918
		{"172.16.5.5", "", true},                    // RFC1918
		{"127.0.0.1", "", true},                     // loopback
		{"169.254.1.1", "", true},                   // link-local
		{"100.64.0.1", "", true},                    // CGNAT — stdlib misses this
		{"100.127.255.254", "", true},               // CGNAT upper edge
		{"240.0.0.1", "", true},                     // Class E — stdlib misses this
		{"0.1.2.3", "", true},                       // "this network"
		{"192.0.2.7", "", true},                     // TEST-NET-1 (bogus)
		{"198.51.100.9", "", true},                  // TEST-NET-2 (bogus)
		{"203.0.113.9", "", true},                   // TEST-NET-3 (bogus)
		{"198.18.0.1", "", true},                    // benchmarking
		{"255.255.255.255", "", true},               // broadcast
		{"2001:db8::1", "", true},                   // v6 not in v1
	}
	for _, tc := range tests {
		got, err := LabelFromIP(net.ParseIP(tc.ip))
		if tc.wantErr != (err != nil) {
			t.Errorf("%s: err = %v, wantErr %v", tc.ip, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: label = %q, want %q", tc.ip, got, tc.want)
		}
	}
}

func TestCollisionLabel(t *testing.T) {
	if got := CollisionLabel("1-2-3-4", 1); got != "1-2-3-4-b" {
		t.Errorf("first fallback = %q", got)
	}
	if got := CollisionLabel("1-2-3-4", 2); got != "1-2-3-4-c" {
		t.Errorf("second fallback = %q", got)
	}
}

func TestValidLabel(t *testing.T) {
	for _, ok := range []string{"192-0-2-7", "1-2-3-4-b", "255-255-255-255"} {
		if !ValidLabel(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "www", "192-0-2", "999-0-2-7", "192-0-2-7-bb", "192-0-2-7.evil", "a-b-c-d"} {
		if ValidLabel(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
