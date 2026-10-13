package dnscompile

import (
	"reflect"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2131: the secondary DNS servers (dns_secondary_ips) get the same
// AXFR permission and NOTIFY as ns2.
func TestTransferTargets(t *testing.T) {
	cases := []struct {
		name       string
		srv        *models.ServerSettings
		axfr, noti []string
	}{
		{"no settings", nil, []string{"127.0.0.1"}, nil},
		{"nothing configured", &models.ServerSettings{}, []string{"127.0.0.1"}, nil},
		{"ns2 only", &models.ServerSettings{NS2IPv4: "198.51.100.7"},
			[]string{"198.51.100.7", "127.0.0.1"}, []string{"198.51.100.7"}},
		{"secondaries only", &models.ServerSettings{DNSSecondaryIPs: "192.0.2.53,2001:db8::53"},
			[]string{"192.0.2.53", "2001:db8::53", "127.0.0.1"}, []string{"192.0.2.53", "2001:db8::53"}},
		{"ns2 first, a repeat of it dropped", &models.ServerSettings{NS2IPv4: "198.51.100.7", DNSSecondaryIPs: "192.0.2.53,198.51.100.7"},
			[]string{"198.51.100.7", "192.0.2.53", "127.0.0.1"}, []string{"198.51.100.7", "192.0.2.53"}},
		{"spaces and empty entries ignored", &models.ServerSettings{DNSSecondaryIPs: " 192.0.2.53 , ,198.51.100.9,"},
			[]string{"192.0.2.53", "198.51.100.9", "127.0.0.1"}, []string{"192.0.2.53", "198.51.100.9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			axfr, noti := TransferTargets(tc.srv)
			if !reflect.DeepEqual(axfr, tc.axfr) {
				t.Errorf("allow-axfr = %v, want %v", axfr, tc.axfr)
			}
			if !reflect.DeepEqual(noti, tc.noti) {
				t.Errorf("also-notify = %v, want %v", noti, tc.noti)
			}
		})
	}
}
