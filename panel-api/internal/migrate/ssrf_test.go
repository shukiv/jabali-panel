package migrate

import (
	"net"
	"strings"
	"testing"
)

func TestCheckIP_BlockedAlways(t *testing.T) {
	for _, ip := range []string{
		"127.0.0.1",
		"127.255.255.254",
		"169.254.169.254", // AWS/GCP metadata
		"::1",
		"fe80::1",
		"0.0.0.0",
	} {
		if err := checkIP(net.ParseIP(ip), true); err == nil {
			t.Errorf("expected %s blocked even with allowPrivate=true", ip)
		}
	}
}

func TestCheckIP_PrivateRespectsToggle(t *testing.T) {
	for _, ip := range []string{
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"fc00::1",
	} {
		if err := checkIP(net.ParseIP(ip), false); err == nil {
			t.Errorf("expected %s blocked with allowPrivate=false", ip)
		}
		if err := checkIP(net.ParseIP(ip), true); err != nil {
			// loopback still rejected even with allowPrivate; everything
			// else in this list is RFC1918/ULA so allowPrivate=true must
			// admit it.
			if !strings.Contains(err.Error(), "loopback") {
				t.Errorf("expected %s allowed with allowPrivate=true: %v", ip, err)
			}
		}
	}
}

func TestCheckIP_NonPublicRangesFollowTheToggle(t *testing.T) {
	for _, ip := range []string{
		"0.1.2.3",           // "this network"
		"::7f00:1",          // IPv4-compatible IPv6 (127.0.0.1)
		"64:ff9b::7f00:1",   // NAT64 of 127.0.0.1
		"2002:a9fe:a9fe::1", // 6to4 of 169.254.169.254
	} {
		if err := checkIP(net.ParseIP(ip), true); err == nil {
			t.Errorf("expected %s blocked even with allowPrivate=true", ip)
		}
	}
	for _, ip := range []string{
		"100.64.0.1",     // CGNAT (Tailscale)
		"198.18.0.1",     // benchmarking
		"240.0.0.1",      // reserved
		"fec0::1",        // IPv6 site-local
		"64:ff9b:1::1",   // local-use NAT64
		"64:ff9b::a00:1", // NAT64 of 10.0.0.1
	} {
		err := checkIP(net.ParseIP(ip), false)
		if err == nil {
			t.Errorf("expected %s blocked with allowPrivate=false", ip)
		} else if !strings.Contains(err.Error(), "migration_allow_private_hosts") {
			t.Errorf("%s: the error should name the override: %v", ip, err)
		}
		if err := checkIP(net.ParseIP(ip), true); err != nil {
			t.Errorf("expected %s allowed with allowPrivate=true: %v", ip, err)
		}
	}
}

// The override can't admit loopback, so its refusal doesn't offer it.
func TestCheckIP_LoopbackRefusalDoesNotOfferTheOverride(t *testing.T) {
	err := checkIP(net.ParseIP("127.0.0.1"), false)
	if err == nil || strings.Contains(err.Error(), "migration_allow_private_hosts") {
		t.Errorf("checkIP(127.0.0.1) = %v; want a refusal without the override hint", err)
	}
}

func TestCheckIP_PublicAllowed(t *testing.T) {
	for _, ip := range []string{
		"1.1.1.1",
		"8.8.8.8",
		"2001:4860:4860::8888",
	} {
		if err := checkIP(net.ParseIP(ip), false); err != nil {
			t.Errorf("expected %s allowed: %v", ip, err)
		}
	}
}
