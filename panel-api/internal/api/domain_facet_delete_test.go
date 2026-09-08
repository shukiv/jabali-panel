package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1603: deleting a Web Domain can keep the Mail Domain and/or DNS Zone,
// because all three are facets of one row. The facet-preserving path tears down
// only the web facet (domain.web_teardown + web_disabled=true) and keeps the
// row, running the mail purge / DNS-zone delete only for the facets the caller
// chose to also delete. Reuses the mail-purge test harness (mpAgent,
// mpMailboxRepo, newMockDomainRepo).

func facetDom() *models.Domain {
	return &models.Domain{
		ID: "d1", UserID: "u1", Name: "foo.test",
		EmailEnabled: true,
		DocRoot:      "/home/u1/domains/foo.test/public_html",
	}
}

func TestFacetDelete_KeepMailKeepDNS(t *testing.T) {
	dr := newMockDomainRepo()
	dom := facetDom()
	_ = dr.Create(context.Background(), dom)
	ag := &mpAgent{}
	h := &domainHandler{cfg: DomainHandlerConfig{Domains: dr, Agent: ag}}

	// keep mail, keep dns → only the web facet is torn down.
	_, err := h.facetPreservingWebDelete(context.Background(), dom, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ag.has("domain.web_teardown") {
		t.Fatalf("web teardown must run; calls=%v", ag.calls)
	}
	if ag.has("mail.domain.purge_accounts") {
		t.Fatalf("mail must be KEPT — purge_accounts must not run; calls=%v", ag.calls)
	}
	if ag.has("dns.zone.delete") {
		t.Fatalf("DNS must be KEPT — dns.zone.delete must not run; calls=%v", ag.calls)
	}
	got, _ := dr.FindByID(context.Background(), "d1")
	if !got.WebDisabled {
		t.Fatalf("web_disabled must be set")
	}
	if !got.EmailEnabled {
		t.Fatalf("email must stay enabled (mail kept)")
	}
	if got.DNSDisabled {
		t.Fatalf("dns must stay managed (DNS kept)")
	}
}

func TestFacetDelete_DeleteMailKeepDNS(t *testing.T) {
	dr := newMockDomainRepo()
	dom := facetDom()
	_ = dr.Create(context.Background(), dom)
	mb := &mpMailboxRepo{byDomain: map[string][]models.Mailbox{
		"d1": {{ID: "mb1", LocalPart: "alice"}},
	}}
	ag := &mpAgent{}
	h := &domainHandler{cfg: DomainHandlerConfig{Domains: dr, Mailboxes: mb, Agent: ag}}

	_, err := h.facetPreservingWebDelete(context.Background(), dom, true, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ag.has("domain.web_teardown") || !ag.has("mail.domain.purge_accounts") {
		t.Fatalf("web teardown + mail purge must both run; calls=%v", ag.calls)
	}
	// The web facet must come down before the mail purge (the purge de-registers
	// the domain in Stalwart; ordering mirrors the shared teardown).
	if ag.indexOf("domain.web_teardown") > ag.indexOf("mail.domain.purge_accounts") {
		t.Fatalf("web_teardown must precede purge_accounts; calls=%v", ag.calls)
	}
	if ag.has("dns.zone.delete") {
		t.Fatalf("DNS kept — dns.zone.delete must not run; calls=%v", ag.calls)
	}
	if len(mb.deleted) != 1 || mb.deleted[0] != "mb1" {
		t.Fatalf("mailbox row must be deleted; got %v", mb.deleted)
	}
	got, _ := dr.FindByID(context.Background(), "d1")
	if !got.WebDisabled || got.EmailEnabled || got.DNSDisabled {
		t.Fatalf("want web_disabled + email off + dns kept; got web=%v email=%v dnsDisabled=%v",
			got.WebDisabled, got.EmailEnabled, got.DNSDisabled)
	}
}

func TestFacetDelete_KeepMailDeleteDNS(t *testing.T) {
	dr := newMockDomainRepo()
	dom := facetDom()
	_ = dr.Create(context.Background(), dom)
	ag := &mpAgent{}
	h := &domainHandler{cfg: DomainHandlerConfig{Domains: dr, Agent: ag}}

	_, err := h.facetPreservingWebDelete(context.Background(), dom, false, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ag.has("domain.web_teardown") || !ag.has("dns.zone.delete") {
		t.Fatalf("web teardown + dns zone delete must both run; calls=%v", ag.calls)
	}
	if ag.has("mail.domain.purge_accounts") {
		t.Fatalf("mail kept — purge_accounts must not run; calls=%v", ag.calls)
	}
	got, _ := dr.FindByID(context.Background(), "d1")
	if !got.WebDisabled || !got.DNSDisabled || !got.EmailEnabled {
		t.Fatalf("want web_disabled + dns_disabled + email kept; got web=%v dns=%v email=%v",
			got.WebDisabled, got.DNSDisabled, got.EmailEnabled)
	}
}

// A web_teardown failure is a hard error and nothing downstream (the mail purge)
// runs — the caller retries the idempotent op.
func TestFacetDelete_WebTeardownFailureStopsBeforeMail(t *testing.T) {
	dr := newMockDomainRepo()
	dom := facetDom()
	_ = dr.Create(context.Background(), dom)
	ag := &mpAgent{failCmd: "domain.web_teardown"}
	h := &domainHandler{cfg: DomainHandlerConfig{Domains: dr, Agent: ag}}

	_, err := h.facetPreservingWebDelete(context.Background(), dom, true, false, false)
	if err == nil {
		t.Fatalf("expected a hard error when web_teardown fails")
	}
	if ag.has("mail.domain.purge_accounts") {
		t.Fatalf("mail purge must not run after web_teardown fails; calls=%v", ag.calls)
	}
}
