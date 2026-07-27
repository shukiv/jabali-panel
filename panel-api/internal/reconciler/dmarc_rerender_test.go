package reconciler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #648: restoreBootstrapApex re-renders a jabali-authored (canonical) _dmarc
// to the domain's DMARCbis settings, but leaves an operator-customised record
// alone. MX + apex SPF are pre-seeded so only the _dmarc path is exercised.
func dmarcApexExisting(dmarcContent string) []models.DNSRecord {
	return []models.DNSRecord{
		{ID: "mx", ZoneID: "z1", Name: "@", Type: "MX", Content: "mail.example.com"},
		{ID: "spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: `"v=spf1 mx ~all"`},
		{ID: "dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: dmarcContent},
	}
}

func TestRestoreBootstrapApex_ReRendersCanonicalDMARC(t *testing.T) {
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	legacy := `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"` // canonical default
	dnsRepo := &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{
		"dmarc": {ID: "dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: legacy},
	}}
	r := &Reconciler{dnsRecords: dnsRepo, log: slog.Default()}
	dom := &models.Domain{ID: "d1", DmarcNP: "reject", DmarcTesting: true}

	r.restoreBootstrapApex(context.Background(), zone, dom, &models.ServerSettings{},
		dmarcApexExisting(legacy), time.Now().UTC())

	want := dnscompile.BuildDMARCString("reject", true)
	if got := dnsRepo.records["dmarc"].Content; got != want {
		t.Errorf("canonical _dmarc should re-render to %s, got %s", want, got)
	}
}

func TestRestoreBootstrapApex_LeavesOperatorCustomDMARC(t *testing.T) {
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	custom := `"v=DMARC1; p=reject; rua=mailto:dmarc@example.com"` // hand-edited, not canonical
	dnsRepo := &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{
		"dmarc": {ID: "dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: custom},
	}}
	r := &Reconciler{dnsRecords: dnsRepo, log: slog.Default()}
	dom := &models.Domain{ID: "d1", DmarcNP: "reject", DmarcTesting: true}

	r.restoreBootstrapApex(context.Background(), zone, dom, &models.ServerSettings{},
		dmarcApexExisting(custom), time.Now().UTC())

	if got := dnsRepo.records["dmarc"].Content; got != custom {
		t.Errorf("operator-customised _dmarc must be left untouched, got %s", got)
	}
}
