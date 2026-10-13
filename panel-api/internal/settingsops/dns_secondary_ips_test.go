package settingsops

import (
	"strconv"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2131: dns_secondary_ips are the servers allowed to transfer every hosted
// zone, so only plain, routable IP literals get in.

func TestNormalizeIPList(t *testing.T) {
	cases := map[string]string{
		"":                                  "",
		" 192.0.2.53 ":                      "192.0.2.53",
		"192.0.2.53, 198.51.100.9":          "192.0.2.53,198.51.100.9",
		"192.0.2.53 198.51.100.9\n10.0.0.5": "192.0.2.53,198.51.100.9,10.0.0.5",
		"2001:DB8:0:0::53":                  "2001:db8::53",
		",,192.0.2.53,,":                    "192.0.2.53",
		"not-an-ip":                         "not-an-ip",
	}
	for in, want := range cases {
		if got := NormalizeIPList(in); got != want {
			t.Errorf("NormalizeIPList(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate_DNSSecondaryIPs(t *testing.T) {
	ok := []string{
		"",
		"192.0.2.53",
		"192.0.2.53,2001:db8::53",
		"10.0.0.5,172.16.1.1,192.168.1.1", // a hidden secondary may sit on a private network
	}
	for _, v := range ok {
		s := &models.ServerSettings{SSHPort: 22, DNSSecondaryIPs: v}
		if err := Validate(s); err != nil {
			t.Errorf("Validate(%q) = %v, want ok", v, err)
		}
	}
	bad := map[string]string{
		"not-an-ip":             "not an IP address",
		"192.0.2.0/24":          "not an IP address",
		"127.0.0.1":             "loopback",
		"::1":                   "loopback",
		"0.0.0.0":               "unspecified",
		"::":                    "unspecified",
		"224.0.0.1":             "multicast",
		"169.254.1.1":           "link-local",
		"192.0.2.53,192.0.2.53": "listed twice",
	}
	for v, want := range bad {
		s := &models.ServerSettings{SSHPort: 22, DNSSecondaryIPs: v}
		err := Validate(s)
		if err == nil || !strings.Contains(err.Error(), "dns_secondary_ips") || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate(%q) = %v, want a dns_secondary_ips error mentioning %q", v, err, want)
		}
	}
}

func TestValidate_DNSSecondaryIPs_Cap(t *testing.T) {
	var ips []string
	for i := 1; i <= maxDNSSecondaryIPs+1; i++ {
		ips = append(ips, "192.0.2."+strconv.Itoa(i))
	}
	s := &models.ServerSettings{SSHPort: 22, DNSSecondaryIPs: strings.Join(ips[:maxDNSSecondaryIPs], ",")}
	if err := Validate(s); err != nil {
		t.Fatalf("%d addresses should be accepted: %v", maxDNSSecondaryIPs, err)
	}
	s.DNSSecondaryIPs = strings.Join(ips, ",")
	if err := Validate(s); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("%d addresses should be refused, got %v", len(ips), err)
	}
}
