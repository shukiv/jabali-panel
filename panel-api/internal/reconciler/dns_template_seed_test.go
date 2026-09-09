package reconciler

import (
	"context"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeTemplateRepo is a minimal DNSTemplateRepository for the seed tests.
type fakeTemplateRepo struct {
	repository.DNSTemplateRepository
	byID map[string]*models.DNSTemplate
}

func (f *fakeTemplateRepo) FindByID(_ context.Context, id string) (*models.DNSTemplate, error) {
	if t, ok := f.byID[id]; ok {
		return t, nil
	}
	return nil, repository.ErrNotFound
}

// GH #1627: dnsTemplateSeeds turns a template's blueprints into tenant-owned
// zone records — {domain} substituted, Managed=false (never re-asserted), and
// fail-safe to nil when the repo/template is missing.
func TestDNSTemplateSeeds(t *testing.T) {
	tmplID := "tmpl-01"
	repo := &fakeTemplateRepo{byID: map[string]*models.DNSTemplate{
		tmplID: {
			ID: tmplID, Name: "Acme",
			Records: []models.DNSTemplateRecord{
				{Name: "@", Type: "MX", Content: "mail.{domain}", Priority: 10, TTL: 3600},
				{Name: "_dmarc", Type: "TXT", Content: `"v=DMARC1; p=none; rua=mailto:postmaster@{domain}"`, TTL: 3600},
			},
		},
	}}
	r := &Reconciler{dnsTemplates: repo, log: slog.New(slog.DiscardHandler)}

	dom := &models.Domain{ID: "d1", Name: "shop.example.com", MailProvider: models.MailProviderCustom, MailTemplateID: strptr(tmplID)}
	seeds := r.dnsTemplateSeeds(context.Background(), dom, "zone-1", "shop.example.com")
	if len(seeds) != 2 {
		t.Fatalf("seeds = %d, want 2", len(seeds))
	}
	if seeds[0].Content != "mail.shop.example.com" {
		t.Errorf("{domain} not substituted in content: %q", seeds[0].Content)
	}
	if seeds[1].Content != `"v=DMARC1; p=none; rua=mailto:postmaster@shop.example.com"` {
		t.Errorf("{domain} not substituted in TXT: %q", seeds[1].Content)
	}
	for i, s := range seeds {
		if s.Managed {
			t.Errorf("seed %d must be tenant-owned (Managed=false)", i)
		}
		if !s.IsEnabled {
			t.Errorf("seed %d must be enabled", i)
		}
		if s.ZoneID != "zone-1" {
			t.Errorf("seed %d wrong zone: %q", i, s.ZoneID)
		}
		if s.ID == "" {
			t.Errorf("seed %d got no id", i)
		}
	}

	// Fail-safe nil paths.
	if got := r.dnsTemplateSeeds(context.Background(), &models.Domain{ID: "d2"}, "z", "d2.example"); got != nil {
		t.Error("no template id → nil seeds")
	}
	missing := &models.Domain{ID: "d3", MailTemplateID: strptr("gone")}
	if got := r.dnsTemplateSeeds(context.Background(), missing, "z", "d3.example"); got != nil {
		t.Error("deleted template → nil seeds (no panic)")
	}
	rNil := &Reconciler{log: slog.New(slog.DiscardHandler)}
	if got := rNil.dnsTemplateSeeds(context.Background(), dom, "z", "shop.example.com"); got != nil {
		t.Error("unwired repo → nil seeds")
	}
}

// fakeCountDNSRecords counts mutations so the custom hand-off can be asserted
// as "never touches the zone".
type fakeCountDNSRecords struct {
	repository.DNSRecordRepository
	listCalls, deleteCalls, createCalls int
}

func (f *fakeCountDNSRecords) ListByZoneID(context.Context, string) ([]models.DNSRecord, error) {
	f.listCalls++
	return nil, nil
}
func (f *fakeCountDNSRecords) Delete(context.Context, string) error { f.deleteCalls++; return nil }
func (f *fakeCountDNSRecords) Create(context.Context, *models.DNSRecord) error {
	f.createCalls++
	return nil
}

// GH #1627: reconcileMailProviderRecords must hand off entirely for a custom
// domain — no list, no prune, no assert — so the template's own apex MX/SPF
// survive. (Contrast 'none', which prunes any non-mail-apex apex SPF.)
func TestReconcileMailProviderRecords_CustomHandsOff(t *testing.T) {
	recs := &fakeCountDNSRecords{}
	r := &Reconciler{dnsRecords: recs, log: slog.New(slog.DiscardHandler)}
	zone := &models.DNSZone{ID: "z1", Name: "shop.example.com"}
	dom := &models.Domain{ID: "d1", Name: "shop.example.com", MailProvider: models.MailProviderCustom}

	r.reconcileMailProviderRecords(context.Background(), zone, dom, &models.ServerSettings{})

	if recs.listCalls != 0 || recs.deleteCalls != 0 || recs.createCalls != 0 {
		t.Fatalf("custom domain must not touch the zone: list=%d delete=%d create=%d",
			recs.listCalls, recs.deleteCalls, recs.createCalls)
	}
}
