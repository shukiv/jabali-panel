package reconciler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #723 (mail half): a domain migrated onto jabali's own DNS ended up with an
// apex A + MX/SPF/DMARC but NO `mail` A record — the MX target. The bind import
// drops the source's mail records and creates the zone row, so reconcileDNSZone
// skips BootstrapRecords (the only other creator of `mail` A), and
// restoreBootstrapApex restored MX/SPF/DMARC but omitted the A the MX points at.
// mail.<domain> then never resolves → delivery + the mail-cert preflight fail.

// mailRestoreExisting returns an apex set with MX/SPF present and a
// deliberately NON-canonical _dmarc (so the DMARC path is a pure no-op), plus
// any extra records — leaving only the `mail` A/AAAA restore under test.
func mailRestoreExisting(extra ...models.DNSRecord) []models.DNSRecord {
	base := []models.DNSRecord{
		{ID: "mx", ZoneID: "z1", Name: "@", Type: "MX", Content: "mail.example.com"},
		{ID: "spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: `"v=spf1 mx ~all"`},
		{ID: "dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: `"v=DMARC1; p=reject; rua=mailto:x@example.com"`},
	}
	return append(base, extra...)
}

func countMailRecs(recs map[string]*models.DNSRecord, typ string) []*models.DNSRecord {
	var out []*models.DNSRecord
	for _, r := range recs {
		if r.Name == "mail" && r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

func newMailReconciler(seed ...*models.DNSRecord) (*Reconciler, *fakeDNSRecordRepo) {
	recs := map[string]*models.DNSRecord{}
	for _, s := range seed {
		recs[s.ID] = s
	}
	repo := &fakeDNSRecordRepo{records: recs}
	return &Reconciler{dnsRecords: repo, log: slog.Default()}, repo
}

func mailZoneDom() (*models.DNSZone, *models.Domain) {
	return &models.DNSZone{ID: "z1", Name: "example.com"}, &models.Domain{ID: "d1"}
}

// (a) jabali, MX present, mail A absent, v4 set → mail A created at the server primary.
func TestRestoreBootstrapApex_CreatesMailAWhenAbsent(t *testing.T) {
	r, repo := newMailReconciler()
	zone, dom := mailZoneDom()
	srv := &models.ServerSettings{PublicIPv4: "203.0.113.5"}

	r.restoreBootstrapApex(context.Background(), zone, dom, srv, mailRestoreExisting(), time.Now().UTC())

	got := countMailRecs(repo.records, "A")
	if len(got) != 1 {
		t.Fatalf("want exactly one mail A created, got %d", len(got))
	}
	rec := got[0]
	if rec.Content != "203.0.113.5" {
		t.Errorf("mail A content = %q, want the server primary 203.0.113.5", rec.Content)
	}
	if !rec.Managed || rec.ManagedBy != nil {
		t.Errorf("mail A must be Managed=true, ManagedBy=nil (bootstrap convention), got Managed=%v ManagedBy=%v", rec.Managed, rec.ManagedBy)
	}
	if rec.ZoneID != "z1" {
		t.Errorf("mail A ZoneID = %q, want z1", rec.ZoneID)
	}
}

// AAAA analog: v6 set, mail AAAA absent → created.
func TestRestoreBootstrapApex_CreatesMailAAAAWhenAbsent(t *testing.T) {
	r, repo := newMailReconciler()
	zone, dom := mailZoneDom()
	srv := &models.ServerSettings{PublicIPv4: "203.0.113.5", PublicIPv6: "2001:db8::5"}

	r.restoreBootstrapApex(context.Background(), zone, dom, srv, mailRestoreExisting(), time.Now().UTC())

	if a := countMailRecs(repo.records, "A"); len(a) != 1 || a[0].Content != "203.0.113.5" {
		t.Errorf("mail A: want one at 203.0.113.5, got %+v", a)
	}
	aaaa := countMailRecs(repo.records, "AAAA")
	if len(aaaa) != 1 || aaaa[0].Content != "2001:db8::5" {
		t.Errorf("mail AAAA: want one at 2001:db8::5, got %+v", aaaa)
	}
}

// (b) an operator's own mail A (foreign content) must be left alone, never duplicated.
func TestRestoreBootstrapApex_LeavesExistingMailA(t *testing.T) {
	seed := &models.DNSRecord{ID: "mailA", ZoneID: "z1", Name: "mail", Type: "A", Content: "198.51.100.9"}
	r, repo := newMailReconciler(seed)
	zone, dom := mailZoneDom()
	srv := &models.ServerSettings{PublicIPv4: "203.0.113.5"}

	r.restoreBootstrapApex(context.Background(), zone, dom, srv,
		mailRestoreExisting(*seed), time.Now().UTC())

	got := countMailRecs(repo.records, "A")
	if len(got) != 1 {
		t.Fatalf("existing mail A must not be duplicated: got %d mail A records", len(got))
	}
	if got[0].Content != "198.51.100.9" {
		t.Errorf("existing mail A content changed to %q, must stay 198.51.100.9", got[0].Content)
	}
}

// (c) a `mail` CNAME blocks A/AAAA creation (A-beside-CNAME is a zone error).
func TestRestoreBootstrapApex_MailCNAMEBlocksAddr(t *testing.T) {
	cname := models.DNSRecord{ID: "mailC", ZoneID: "z1", Name: "mail", Type: "CNAME", Content: "ghs.example.com."}
	r, repo := newMailReconciler()
	zone, dom := mailZoneDom()
	srv := &models.ServerSettings{PublicIPv4: "203.0.113.5", PublicIPv6: "2001:db8::5"}

	r.restoreBootstrapApex(context.Background(), zone, dom, srv,
		mailRestoreExisting(cname), time.Now().UTC())

	if a := countMailRecs(repo.records, "A"); len(a) != 0 {
		t.Errorf("must not create a mail A beside a mail CNAME, got %+v", a)
	}
	if aaaa := countMailRecs(repo.records, "AAAA"); len(aaaa) != 0 {
		t.Errorf("must not create a mail AAAA beside a mail CNAME, got %+v", aaaa)
	}
}

// (e) no server IPv4 configured → no empty-content row.
func TestRestoreBootstrapApex_NoIPNoRow(t *testing.T) {
	r, repo := newMailReconciler()
	zone, dom := mailZoneDom()
	srv := &models.ServerSettings{} // no IPs

	r.restoreBootstrapApex(context.Background(), zone, dom, srv, mailRestoreExisting(), time.Now().UTC())

	if a := countMailRecs(repo.records, "A"); len(a) != 0 {
		t.Errorf("no PublicIPv4 must create no mail A, got %+v", a)
	}
	if aaaa := countMailRecs(repo.records, "AAAA"); len(aaaa) != 0 {
		t.Errorf("no PublicIPv6 must create no mail AAAA, got %+v", aaaa)
	}
}

// (d) provider=none must NOT resurrect a mail A — that path prunes jabali mail
// rows; the GH #723 restore is scoped to jabali via reconcileMailProviderRecords.
func TestReconcileMailProviderRecords_NoneDoesNotCreateMailA(t *testing.T) {
	r, repo := newMailReconciler()
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	dom := &models.Domain{ID: "d1", MailProvider: models.MailProviderNone}
	srv := &models.ServerSettings{PublicIPv4: "203.0.113.5"}

	r.reconcileMailProviderRecords(context.Background(), zone, dom, srv)

	if a := countMailRecs(repo.records, "A"); len(a) != 0 {
		t.Errorf("provider=none must not create a jabali mail A, got %+v", a)
	}
}

// End-to-end through the public entry point: provider=jabali (default) creates
// the mail A on a zone that has MX but no mail A — the migrated-domain shape.
func TestReconcileMailProviderRecords_JabaliCreatesMailA(t *testing.T) {
	seed := []*models.DNSRecord{
		{ID: "mx", ZoneID: "z1", Name: "@", Type: "MX", Content: "mail.example.com"},
		{ID: "spf", ZoneID: "z1", Name: "@", Type: "TXT", Content: `"v=spf1 mx ip4:203.0.113.5 ~all"`},
		{ID: "dmarc", ZoneID: "z1", Name: "_dmarc", Type: "TXT", Content: `"v=DMARC1; p=reject; rua=mailto:x@example.com"`},
	}
	r, repo := newMailReconciler(seed...)
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	dom := &models.Domain{ID: "d1"} // MailProvider "" == jabali
	srv := &models.ServerSettings{PublicIPv4: "203.0.113.5"}

	r.reconcileMailProviderRecords(context.Background(), zone, dom, srv)

	got := countMailRecs(repo.records, "A")
	if len(got) != 1 || got[0].Content != "203.0.113.5" {
		t.Errorf("jabali provider must create the mail A at the server primary, got %+v", got)
	}
}
