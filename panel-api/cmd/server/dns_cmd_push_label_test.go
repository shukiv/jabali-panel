package main

import (
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2377: `jabali dns zone list` shows whether the DNS server holds each
// zone, not only that the panel has a row for it.
func TestDNSZonePushLabel(t *testing.T) {
	at := time.Date(2026, 11, 4, 18, 0, 0, 0, time.UTC)
	failed := "Access denied"
	empty := ""
	cases := []struct {
		name string
		zone models.DNSZone
		want string
	}{
		{"never pushed", models.DNSZone{}, "pending"},
		{"cleared error", models.DNSZone{LastPushError: &empty}, "pending"},
		{"accepted", models.DNSZone{LastPushedAt: &at}, "yes"},
		{"refused", models.DNSZone{LastPushError: &failed}, "no (push failed)"},
		{"older copy on the server", models.DNSZone{LastPushedAt: &at, LastPushError: &failed}, "outdated (last push failed)"},
	}
	for _, c := range cases {
		if got := dnsZonePushLabel(&c.zone); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
