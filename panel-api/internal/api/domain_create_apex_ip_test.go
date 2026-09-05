package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1540: a DNS-only zone (web off, DNS on) may be created with a
// tenant-chosen apex IP (the "pointed IP"). createDomainOp persists it on the
// domain row (DNSApexIPv4) after validating it is a bare IPv4 and that the
// domain is actually a DNS-only zone — a web domain's apex is panel-managed,
// and external-DNS publishes nothing here.
func TestCreateDomainOp_ApexIP(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}

	newH := func() (*domainHandler, *dcDomains) {
		dom := newDCDomains()
		h := &domainHandler{cfg: DomainHandlerConfig{Users: newAbUsers(owner), Domains: dom}}
		return h, dom
	}

	t.Run("DNS-only with a valid IPv4 apex is persisted", func(t *testing.T) {
		h, dom := newH()
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "zone.example.com",
			MailProvider: models.MailProviderNone,
			WebDisabled:  true,
			DNSApexIPv4:  "203.0.113.10",
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.DNSApexIPv4 == nil || *d.DNSApexIPv4 != "203.0.113.10" {
			t.Fatalf("DNSApexIPv4 should be 203.0.113.10, got %v", d.DNSApexIPv4)
		}
		if len(dom.created) != 1 {
			t.Fatalf("domain should be created, got %d", len(dom.created))
		}
	})

	t.Run("surrounding whitespace is trimmed", func(t *testing.T) {
		h, _ := newH()
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "trim.example.com",
			MailProvider: models.MailProviderNone,
			WebDisabled:  true,
			DNSApexIPv4:  "  198.51.100.7  ",
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.DNSApexIPv4 == nil || *d.DNSApexIPv4 != "198.51.100.7" {
			t.Fatalf("DNSApexIPv4 should be trimmed to 198.51.100.7, got %v", d.DNSApexIPv4)
		}
	})

	t.Run("DNS-only without an apex IP leaves the column NULL", func(t *testing.T) {
		h, _ := newH()
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "noip.example.com",
			MailProvider: models.MailProviderNone,
			WebDisabled:  true,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.DNSApexIPv4 != nil {
			t.Fatalf("DNSApexIPv4 should be nil when no IP is given, got %q", *d.DNSApexIPv4)
		}
	})

	t.Run("apex IP on a web domain is rejected", func(t *testing.T) {
		h, _ := newH()
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "web.example.com",
			MailProvider: models.MailProviderNone,
			SSLMode:      models.SSLModeNone,
			DNSApexIPv4:  "203.0.113.10",
		})
		if oerr == nil || oerr.Code != "web_enabled_apex_ip" {
			t.Fatalf("want web_enabled_apex_ip, got %v", oerr)
		}
	})

	t.Run("apex IP with DNS off (external DNS) is rejected", func(t *testing.T) {
		h, _ := newH()
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "extdns.example.com",
			MailProvider: models.MailProviderNone,
			WebDisabled:  true,
			DNSDisabled:  true,
			DNSApexIPv4:  "203.0.113.10",
		})
		if oerr == nil || oerr.Code != "dns_disabled_apex_ip" {
			t.Fatalf("want dns_disabled_apex_ip, got %v", oerr)
		}
	})

	t.Run("a non-IP apex value is rejected", func(t *testing.T) {
		h, _ := newH()
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "bad.example.com",
			MailProvider: models.MailProviderNone,
			WebDisabled:  true,
			DNSApexIPv4:  "not-an-ip",
		})
		if oerr == nil || oerr.Code != "invalid_apex_ip" {
			t.Fatalf("want invalid_apex_ip, got %v", oerr)
		}
	})

	t.Run("an IPv6 apex value is rejected (apex A row is IPv4)", func(t *testing.T) {
		h, _ := newH()
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "v6.example.com",
			MailProvider: models.MailProviderNone,
			WebDisabled:  true,
			DNSApexIPv4:  "2001:db8::1",
		})
		if oerr == nil || oerr.Code != "invalid_apex_ip" {
			t.Fatalf("want invalid_apex_ip, got %v", oerr)
		}
	})
}
