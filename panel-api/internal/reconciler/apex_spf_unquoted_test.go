package reconciler

import (
	"context"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The CLI, the API and imports store TXT content with or without its quotes.
// An apex SPF saved without them was not recognised as one, so
// restoreBootstrapApex added the bootstrap SPF beside it: two v=spf1 records,
// an RFC 7208 permerror that fails SPF for all of the domain's mail.
func TestRestoreBootstrapApex_UnquotedSPFIsNotDuplicated(t *testing.T) {
	for _, spf := range []string{`v=spf1 ip4:203.0.113.9 ~all`, `"V=SPF1 include:_spf.example.net -all"`} {
		r, repo := newMailReconciler()
		zone, dom := mailZoneDom()
		existing := []models.DNSRecord{
			{ID: "mx", ZoneID: "z1", Name: "@", Type: "MX", Content: "mail.example.com"},
			{ID: "spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: spf},
			{ID: "dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: `"v=DMARC1; p=reject; rua=mailto:x@example.com"`},
		}
		r.restoreBootstrapApex(context.Background(), zone, dom, &models.ServerSettings{PublicIPv4: "203.0.113.5"}, existing, time.Now().UTC())
		for _, rec := range repo.records {
			if rec.Name == "@" && rec.Type == "TXT" {
				t.Errorf("owner SPF %q: a second apex SPF was created: %q", spf, rec.Content)
			}
		}
	}
}

func TestIsApexSPF(t *testing.T) {
	for content, want := range map[string]bool{
		`"v=spf1 mx ~all"`:              true,
		`v=spf1 mx ~all`:                true,
		`"v=spf1"`:                      true,
		`v=spf1`:                        true,
		`V=SPF1 -all`:                   true,
		`"v=spf1 a" " -all"`:            true,
		`"v=spf10 mx"`:                  false,
		`v=spf1x`:                       false,
		`"google-site-verification=ab"`: false,
		`"v=DMARC1; p=none"`:            false,
		``:                              false,
	} {
		if got := isApexSPF(models.DNSRecord{Name: "@", Type: "TXT", Content: content}); got != want {
			t.Errorf("isApexSPF(%q) = %v, want %v", content, got, want)
		}
	}
	if isApexSPF(models.DNSRecord{Name: "www", Type: "TXT", Content: `v=spf1 -all`}) {
		t.Error("a non-apex SPF is not the apex SPF")
	}
}
