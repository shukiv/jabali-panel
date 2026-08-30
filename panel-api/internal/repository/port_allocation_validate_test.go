package repository

import (
	"strings"
	"testing"
)

// GH #1401: a tenant-chosen reverse-proxy port must not target the panel, a
// system service, the allocator pools, or a privileged/out-of-range port.
func TestValidateReverseProxyPort(t *testing.T) {
	for _, p := range []int{1024, 6875, 3000, 5000, 8000, 9000, 40200, 65535} {
		if err := ValidateReverseProxyPort(p); err != nil {
			t.Errorf("port %d should be allowed, got %v", p, err)
		}
	}

	reject := map[int]string{
		80:    "1024",           // privileged
		443:   "1024",           // privileged
		1023:  "1024",           // just under floor
		70000: "65535",          // above range
		8443:  "system service", // panel
		5432:  "system service", // postgres
		3306:  "system service", // mariadb
		8080:  "system service", // stalwart admin
		7422:  "system service", // crowdsec appsec
		18181: "system service", // stalwart — explicit, not pool-coincidence
		10000: "10000-39999",    // docker pool
		20000: "10000-39999",    // python pool
		30005: "10000-39999",    // reverse-proxy pool
		39999: "10000-39999",    // pool top
		40050: "40000-40100",    // ftp passive
	}
	for p, want := range reject {
		err := ValidateReverseProxyPort(p)
		if err == nil {
			t.Errorf("port %d must be rejected", p)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("port %d error %q should mention %q", p, err.Error(), want)
		}
	}
}
