package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1449: Web / Mail / DNS are independent services. createDomainOp must
// honour the WebDisabled / DNSDisabled opt-outs, hold web-only options to
// web-enabled domains, and refuse a domain that hosts nothing.
func TestCreateDomainOp_ServiceFlags(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}

	newH := func() (*domainHandler, *dcDomains) {
		dom := newDCDomains()
		h := &domainHandler{cfg: DomainHandlerConfig{Users: newAbUsers(owner), Domains: dom}}
		return h, dom
	}

	t.Run("DNS-only: web off + no mail → docroot-less, ssl none, dns on", func(t *testing.T) {
		h, dom := newH()
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "zone.example.com",
			MailProvider: models.MailProviderNone,
			WebDisabled:  true,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if !d.WebDisabled {
			t.Error("WebDisabled should be true")
		}
		if d.DNSDisabled {
			t.Error("DNSDisabled should be false (DNS is the whole point)")
		}
		if d.DocRoot != "" {
			t.Errorf("web-off domain must have empty DocRoot, got %q", d.DocRoot)
		}
		if d.SSLMode != models.SSLModeNone {
			t.Errorf("DNS-only domain SSLMode should be forced none, got %q", d.SSLMode)
		}
		if d.EmailEnabled {
			t.Error("DNS-only domain must not have email enabled")
		}
		if len(dom.created) != 1 {
			t.Fatalf("domain should be created, got %d", len(dom.created))
		}
	})

	t.Run("mail-only: web off + jabali mail → docroot-less, ssl kept, email on", func(t *testing.T) {
		h, _ := newH()
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "mail.example.com",
			MailProvider: models.MailProviderJabali,
			SSLMode:      models.SSLModeLE,
			WebDisabled:  true,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if !d.WebDisabled {
			t.Error("WebDisabled should be true")
		}
		if d.DocRoot != "" {
			t.Errorf("mail-only domain must have empty DocRoot, got %q", d.DocRoot)
		}
		if d.SSLMode != models.SSLModeLE {
			t.Errorf("mail-only domain keeps its le mode for mail SANs, got %q", d.SSLMode)
		}
		if !d.EmailEnabled {
			t.Error("mail-only domain must have email enabled")
		}
	})

	t.Run("external DNS: manage-dns off on a web domain", func(t *testing.T) {
		h, _ := newH()
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:      owner.ID,
			Name:         "ext.example.com",
			MailProvider: models.MailProviderNone,
			SSLMode:      models.SSLModeNone,
			DNSDisabled:  true,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.WebDisabled {
			t.Error("WebDisabled should be false (this is a web domain)")
		}
		if !d.DNSDisabled {
			t.Error("DNSDisabled should be true (external DNS)")
		}
		if d.DocRoot == "" {
			t.Error("a web domain must still derive a DocRoot")
		}
	})

	t.Run("web-off rejects a reverse proxy", func(t *testing.T) {
		h, _ := newH()
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID: owner.ID, Name: "rp.example.com",
			MailProvider: models.MailProviderNone, WebDisabled: true, ReverseProxy: true,
		})
		if oerr == nil || oerr.Code != "web_disabled_no_reverse_proxy" {
			t.Fatalf("want web_disabled_no_reverse_proxy, got %v", oerr)
		}
	})

	t.Run("web-off rejects a document root", func(t *testing.T) {
		h, _ := newH()
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID: owner.ID, Name: "dr.example.com",
			MailProvider: models.MailProviderNone, WebDisabled: true,
			DocRoot: "/home/alice/domains/dr.example.com/public_html",
		})
		if oerr == nil || oerr.Code != "web_disabled_no_docroot" {
			t.Fatalf("want web_disabled_no_docroot, got %v", oerr)
		}
	})

	t.Run("all services off is refused", func(t *testing.T) {
		h, dom := newH()
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID: owner.ID, Name: "nothing.example.com",
			MailProvider: models.MailProviderNone, WebDisabled: true, DNSDisabled: true,
		})
		if oerr == nil || oerr.Code != "no_service_selected" {
			t.Fatalf("want no_service_selected, got %v", oerr)
		}
		if len(dom.created) != 0 {
			t.Fatalf("no domain must be created when nothing is hosted, got %d", len(dom.created))
		}
	})

	t.Run("full-service default is unchanged (both flags off)", func(t *testing.T) {
		h, _ := newH()
		d, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID: owner.ID, Name: "full.example.com",
			MailProvider: models.MailProviderNone, SSLMode: models.SSLModeNone, SkipInlineSSL: true,
		})
		if oerr != nil {
			t.Fatalf("unexpected error: %v", oerr)
		}
		if d.WebDisabled || d.DNSDisabled {
			t.Errorf("a default create must be full-service: WebDisabled=%v DNSDisabled=%v", d.WebDisabled, d.DNSDisabled)
		}
		if d.DocRoot == "" {
			t.Error("full-service domain must derive a DocRoot")
		}
	})
}
