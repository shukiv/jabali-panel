package reconciler

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2523: on a domain whose mail is elsewhere (Microsoft 365, Google, or
// "No mail" for a provider such as Proton), the owner's own _dmarc vanished on
// the next DNS pass, and on "No mail" their SPF too. The prune removed every
// unmanaged _dmarc and every apex SPF outside the provider's scope, although
// ADR-0107/0120 say rows the owner created or edited (Managed=false) stay.

const (
	ownerDMARC = `"v=DMARC1; p=reject; rua=mailto:dmarc@example.com"`
	ownerSPF   = `"v=spf1 include:_spf.protonmail.ch -all"`
	m365DMARC  = `"v=DMARC1; p=none; rua=mailto:postmaster@example.com"`
)

func ownerSrv() *models.ServerSettings {
	return &models.ServerSettings{PublicIPv4: "203.0.113.5"}
}

// bootstrapMailRows is the jabali mail set BootstrapRecords writes: Managed,
// no scope marker.
func bootstrapMailRows(srv *models.ServerSettings) []*models.DNSRecord {
	return []*models.DNSRecord{
		{ID: "b-mx", ZoneID: "z1", Name: "@", Type: "MX", Content: "mail.example.com", Priority: 10, Managed: true},
		{ID: "b-spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: dnscompile.BuildSPFString(srv), Managed: true},
		{ID: "b-dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: dnscompile.BuildDMARCString("example.com", "", false), Managed: true},
		{ID: "b-mail", ZoneID: "z1", Name: "mail", Type: "A", Content: "203.0.113.5", Managed: true},
	}
}

func recsNamed(repo *fakeDNSRecordRepo, name, typ string) []*models.DNSRecord {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	var out []*models.DNSRecord
	for _, rec := range repo.records {
		if rec.Name == name && rec.Type == typ {
			out = append(out, rec)
		}
	}
	return out
}

func apexSPFRecs(repo *fakeDNSRecordRepo) []*models.DNSRecord {
	var out []*models.DNSRecord
	for _, rec := range recsNamed(repo, "@", "TXT") {
		if strings.HasPrefix(rec.Content, `"v=spf1`) {
			out = append(out, rec)
		}
	}
	return out
}

func contents(recs []*models.DNSRecord) []string {
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.Content)
	}
	return out
}

// twoPasses runs the provider reconcile twice, as two DNS passes would.
func twoPasses(r *Reconciler, provider string) {
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	dom := &models.Domain{ID: "d1", MailProvider: provider}
	for i := 0; i < 2; i++ {
		r.reconcileMailProviderRecords(context.Background(), zone, dom, ownerSrv())
	}
}

var externalProviders = []string{models.MailProviderNone, models.MailProviderM365, models.MailProviderGoogle}

// The reported case: the owner adds a _dmarc of their own. It stays, and it is
// the only _dmarc (two DMARC records mean receivers apply none).
func TestMailProvider_OwnerDMARCStays(t *testing.T) {
	for _, p := range externalProviders {
		t.Run(p, func(t *testing.T) {
			seed := append(bootstrapMailRows(ownerSrv()),
				&models.DNSRecord{ID: "o-dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: ownerDMARC})
			r, repo := newMailReconciler(seed...)
			twoPasses(r, p)
			got := recsNamed(repo, "_dmarc", "TXT")
			if len(got) != 1 || got[0].Content != ownerDMARC {
				t.Fatalf("_dmarc after two passes = %q, want only the owner's %q", contents(got), ownerDMARC)
			}
		})
	}
}

// The owner edits the _dmarc the provider published. The edit demotes the row
// to owner-owned (ADR-0107, as updateRecord does) and must stick.
func TestMailProvider_EditedProviderDMARCSticks(t *testing.T) {
	for _, p := range []string{models.MailProviderM365, models.MailProviderGoogle} {
		t.Run(p, func(t *testing.T) {
			r, repo := newMailReconciler(bootstrapMailRows(ownerSrv())...)
			twoPasses(r, p)
			gen := recsNamed(repo, "_dmarc", "TXT")
			if len(gen) != 1 || managedBy(*gen[0]) != dnscompile.ApexMailManagedBy {
				t.Fatalf("before the edit want one provider _dmarc, got %+v", gen)
			}
			repo.mu.Lock()
			gen[0].Content = ownerDMARC
			gen[0].Managed = false
			gen[0].ManagedBy = nil
			repo.mu.Unlock()

			twoPasses(r, p)
			got := recsNamed(repo, "_dmarc", "TXT")
			if len(got) != 1 || got[0].Content != ownerDMARC {
				t.Fatalf("_dmarc after the edit = %q, want only the owner's %q", contents(got), ownerDMARC)
			}
		})
	}
}

// When the owner deletes their _dmarc, the provider's comes back.
func TestMailProvider_ProviderDMARCReturnsWhenOwnerDeletesTheirs(t *testing.T) {
	r, repo := newMailReconciler(&models.DNSRecord{ID: "o-dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: ownerDMARC})
	twoPasses(r, models.MailProviderM365)
	if got := recsNamed(repo, "_dmarc", "TXT"); len(got) != 1 || got[0].ID != "o-dmarc" {
		t.Fatalf("with the owner's _dmarc want only it, got %q", contents(got))
	}
	if err := repo.Delete(context.Background(), "o-dmarc"); err != nil {
		t.Fatal(err)
	}
	twoPasses(r, models.MailProviderM365)
	got := recsNamed(repo, "_dmarc", "TXT")
	if len(got) != 1 || got[0].Content != m365DMARC || managedBy(*got[0]) != dnscompile.ApexMailManagedBy {
		t.Fatalf("after the owner deletes theirs want the provider's %q, got %+v", m365DMARC, got)
	}
}

// "No mail" publishes no SPF, so the owner's own SPF (a Proton domain's, say)
// stays; the panel's bootstrap SPF still goes.
func TestMailProvider_NoneKeepsOwnerSPF(t *testing.T) {
	seed := append(bootstrapMailRows(ownerSrv()),
		&models.DNSRecord{ID: "o-spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: ownerSPF})
	r, repo := newMailReconciler(seed...)
	twoPasses(r, models.MailProviderNone)
	got := apexSPFRecs(repo)
	if len(got) != 1 || got[0].Content != ownerSPF {
		t.Fatalf("apex SPF on none = %q, want only the owner's %q", contents(got), ownerSPF)
	}
}

// On Microsoft 365 / Google the provider's SPF stays the only one at the apex
// (RFC 7208), so an owner's SPF is still replaced. This pins that choice.
func TestMailProvider_ExternalReplacesOwnerSPF(t *testing.T) {
	want := map[string]string{
		models.MailProviderM365:   `"v=spf1 include:spf.protection.outlook.com -all"`,
		models.MailProviderGoogle: `"v=spf1 include:_spf.google.com ~all"`,
	}
	for p, spf := range want {
		t.Run(p, func(t *testing.T) {
			r, repo := newMailReconciler(&models.DNSRecord{ID: "o-spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: ownerSPF})
			twoPasses(r, p)
			got := apexSPFRecs(repo)
			if len(got) != 1 || got[0].Content != spf {
				t.Fatalf("apex SPF on %s = %q, want only %q", p, contents(got), spf)
			}
		})
	}
}

// A switch away from Jabali mail still removes the rows the panel wrote, and
// leaves exactly the provider's apex set.
func TestMailProvider_SwitchPrunesPanelRows(t *testing.T) {
	for _, p := range externalProviders {
		t.Run(p, func(t *testing.T) {
			r, repo := newMailReconciler(bootstrapMailRows(ownerSrv())...)
			twoPasses(r, p)
			for _, id := range []string{"b-mx", "b-spf", "b-dmarc", "b-mail"} {
				if _, ok := repo.records[id]; ok {
					t.Errorf("panel row %s survived the switch to %s", id, p)
				}
			}
			dmarc, spf := recsNamed(repo, "_dmarc", "TXT"), apexSPFRecs(repo)
			wantN := 1
			if p == models.MailProviderNone {
				wantN = 0
			}
			if len(dmarc) != wantN || len(spf) != wantN {
				t.Errorf("%s: _dmarc %q, SPF %q, want %d of each", p, contents(dmarc), contents(spf), wantN)
			}
		})
	}
}

// The owner's own mail A and MX are theirs too (ADR-0107): only the panel's go.
func TestMailProvider_OwnerMailHostAndMXStay(t *testing.T) {
	seed := []*models.DNSRecord{
		{ID: "o-mail", ZoneID: "z1", Name: "mail", Type: "A", Content: "198.51.100.7"},
		{ID: "o-mx", ZoneID: "z1", Name: "@", Type: "MX", Content: "mail.example.com", Priority: 5},
	}
	r, repo := newMailReconciler(seed...)
	twoPasses(r, models.MailProviderM365)
	for _, id := range []string{"o-mail", "o-mx"} {
		if _, ok := repo.records[id]; !ok {
			t.Errorf("owner row %s was removed", id)
		}
	}
}
