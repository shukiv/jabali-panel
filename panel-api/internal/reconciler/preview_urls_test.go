package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakePreviewZoneRepo struct {
	repository.DNSZoneRepository
	zone *models.DNSZone
}

func (f *fakePreviewZoneRepo) FindByName(_ context.Context, name string) (*models.DNSZone, error) {
	if f.zone != nil && f.zone.Name == name {
		return f.zone, nil
	}
	return nil, repository.ErrNotFound
}

type fakePreviewRecordRepo struct {
	repository.DNSRecordRepository
	existing []models.DNSRecord
	created  []models.DNSRecord
}

func (f *fakePreviewRecordRepo) ListByZoneID(_ context.Context, _ string) ([]models.DNSRecord, error) {
	return f.existing, nil
}
func (f *fakePreviewRecordRepo) Create(_ context.Context, r *models.DNSRecord) error {
	f.created = append(f.created, *r)
	return nil
}

type fakePreviewCertRepo struct {
	repository.SharedCertificateRepository
	rows    []models.SharedCertificate
	created []models.SharedCertificate
}

func (f *fakePreviewCertRepo) ListAll(_ context.Context) ([]models.SharedCertificate, error) {
	return f.rows, nil
}
func (f *fakePreviewCertRepo) Create(_ context.Context, c *models.SharedCertificate) error {
	f.created = append(f.created, *c)
	return nil
}

type fakePreviewAgent struct {
	calls []map[string]any
}

func (f *fakePreviewAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	if method != "preview.fallback_apply" {
		return nil, nil
	}
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, m)
	return json.RawMessage(`{"ok":true}`), nil
}

func previewReconciler(certs *fakePreviewCertRepo, zones *fakePreviewZoneRepo, recs *fakePreviewRecordRepo) *Reconciler {
	return &Reconciler{
		sharedCerts: certs,
		dnsZones:    zones,
		dnsRecords:  recs,
		serverSettings: &fakeSettingsRepo{srv: &models.ServerSettings{
			Hostname:   "host.tld",
			AdminEmail: "a@host.tld",
			PublicIPv4: "203.0.113.7",
			PublicIPv6: "2001:db8::7",
		}},
		log: slog.New(slog.DiscardHandler),
	}
}

func enabledPreviewDomains() map[string]*models.Domain {
	return map[string]*models.Domain{
		"example.com": {ID: "D1", Name: "example.com", IsEnabled: true, TempURLEnabled: true},
	}
}

func TestReconcilePreviewInfra_BootstrapsRecordAndCert(t *testing.T) {
	certs := &fakePreviewCertRepo{}
	zones := &fakePreviewZoneRepo{zone: &models.DNSZone{ID: "Z1", Name: "host.tld"}}
	recs := &fakePreviewRecordRepo{}
	r := previewReconciler(certs, zones, recs)

	r.reconcilePreviewInfra(context.Background(), enabledPreviewDomains())

	if len(certs.created) != 1 || certs.created[0].Name != "*.preview.host.tld" {
		t.Fatalf("expected one *.preview.host.tld cert row, got %+v", certs.created)
	}
	if certs.created[0].Source != models.SharedCertSourceACME {
		t.Error("preview cert row must be source=acme so the DNS-01 sweep issues it")
	}
	if certs.created[0].UserID != nil {
		t.Error("preview cert must be server-wide (user_id NULL)")
	}
	if len(recs.created) != 2 {
		t.Fatalf("expected wildcard A + AAAA records, got %+v", recs.created)
	}
	for _, rec := range recs.created {
		if rec.Name != "*.preview.host.tld" {
			t.Errorf("record name = %q", rec.Name)
		}
		if rec.ManagedBy == nil || *rec.ManagedBy != previewManagedBy {
			t.Errorf("record must be tagged managed_by=%s", previewManagedBy)
		}
	}
}

func TestReconcilePreviewInfra_IdempotentWhenPresent(t *testing.T) {
	certs := &fakePreviewCertRepo{rows: []models.SharedCertificate{{Name: "*.preview.host.tld", Source: models.SharedCertSourceACME}}}
	zones := &fakePreviewZoneRepo{zone: &models.DNSZone{ID: "Z1", Name: "host.tld"}}
	recs := &fakePreviewRecordRepo{existing: []models.DNSRecord{
		{Name: "*.preview.host.tld", Type: "A"},
		{Name: "*.preview.host.tld", Type: "AAAA"},
	}}
	r := previewReconciler(certs, zones, recs)

	r.reconcilePreviewInfra(context.Background(), enabledPreviewDomains())

	if len(certs.created) != 0 || len(recs.created) != 0 {
		t.Errorf("second pass must be a no-op: certs=%d recs=%d", len(certs.created), len(recs.created))
	}
}

func TestReconcilePreviewInfra_NoopWithoutEnabledDomains(t *testing.T) {
	certs := &fakePreviewCertRepo{}
	zones := &fakePreviewZoneRepo{zone: &models.DNSZone{ID: "Z1", Name: "host.tld"}}
	recs := &fakePreviewRecordRepo{}
	r := previewReconciler(certs, zones, recs)

	r.reconcilePreviewInfra(context.Background(), map[string]*models.Domain{
		"plain.com": {ID: "D2", Name: "plain.com", IsEnabled: true},
	})

	if len(certs.created) != 0 || len(recs.created) != 0 {
		t.Error("no temp-URL domains → no infra writes")
	}
}

func TestPreviewParams(t *testing.T) {
	certPath := "/etc/letsencrypt/live/wildcard.preview.host.tld/fullchain.pem"
	keyPath := "/etc/letsencrypt/live/wildcard.preview.host.tld/privkey.pem"
	certs := &fakePreviewCertRepo{rows: []models.SharedCertificate{{
		Name:     "*.preview.host.tld",
		Source:   models.SharedCertSourceACME,
		CertPath: &certPath,
		KeyPath:  &keyPath,
	}}}
	zones := &fakePreviewZoneRepo{zone: &models.DNSZone{ID: "Z1", Name: "host.tld"}}
	r := previewReconciler(certs, zones, &fakePreviewRecordRepo{})

	d := &models.Domain{Name: "example.com", TempURLEnabled: true}
	got := r.previewParams(context.Background(), d)
	if got["preview_host"] != "example-com.preview.host.tld" {
		t.Errorf("preview_host = %q", got["preview_host"])
	}
	if got["preview_cert_path"] != certPath || got["preview_key_path"] != keyPath {
		t.Errorf("cert paths not threaded: %+v", got)
	}

	// Disabled domain → nil params, and the state cache must not leak
	// stale answers past its TTL semantics for the enabled case.
	if p := r.previewParams(context.Background(), &models.Domain{Name: "x.com"}); p != nil {
		t.Errorf("temp URL off must yield nil params, got %+v", p)
	}
}

func TestReconcilePreviewInfra_AppliesFallbackVhost(t *testing.T) {
	certPath := "/etc/letsencrypt/live/wildcard.preview.host.tld/fullchain.pem"
	keyPath := "/etc/letsencrypt/live/wildcard.preview.host.tld/privkey.pem"
	certs := &fakePreviewCertRepo{rows: []models.SharedCertificate{{
		Name: "*.preview.host.tld", Source: models.SharedCertSourceACME,
		CertPath: &certPath, KeyPath: &keyPath,
	}}}
	zones := &fakePreviewZoneRepo{zone: &models.DNSZone{ID: "Z1", Name: "host.tld"}}
	recs := &fakePreviewRecordRepo{existing: []models.DNSRecord{
		{Name: "*.preview.host.tld", Type: "A"},
		{Name: "*.preview.host.tld", Type: "AAAA"},
	}}
	r := previewReconciler(certs, zones, recs)
	ag := &fakePreviewAgent{}
	r.agent = ag

	r.reconcilePreviewInfra(context.Background(), enabledPreviewDomains())

	if len(ag.calls) != 1 {
		t.Fatalf("expected one preview.fallback_apply call, got %d", len(ag.calls))
	}
	call := ag.calls[0]
	if call["base"] != "preview.host.tld" {
		t.Errorf("base = %v", call["base"])
	}
	if call["ssl_cert_path"] != certPath {
		t.Errorf("issued wildcard pair must ride along: %+v", call)
	}
}

// GH #836: a foreign base (magic DNS / externally-managed domain) skips
// local DNS records and the DNS-01 cert row — both are impossible for a
// zone pdns doesn't host — but the fallback vhost still applies with
// that base, and previews serve via the external resolution.
func TestReconcilePreviewInfra_ForeignBaseSkipsDNSAndCert(t *testing.T) {
	certs := &fakePreviewCertRepo{}
	zones := &fakePreviewZoneRepo{zone: &models.DNSZone{ID: "Z1", Name: "host.tld"}}
	recs := &fakePreviewRecordRepo{}
	r := previewReconciler(certs, zones, recs)
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{
		Hostname: "host.tld", PreviewBase: "203-0-113-7.sslip.io", PublicIPv4: "203.0.113.7",
	}}
	ag := &fakePreviewAgent{}
	r.agent = ag

	r.reconcilePreviewInfra(context.Background(), enabledPreviewDomains())

	if len(certs.created) != 0 {
		t.Errorf("foreign base must not create a DNS-01 cert row: %+v", certs.created)
	}
	if len(recs.created) != 0 {
		t.Errorf("foreign base must not write local DNS records: %+v", recs.created)
	}
	if len(ag.calls) != 1 || ag.calls[0]["base"] != "203-0-113-7.sslip.io" {
		t.Errorf("fallback vhost must still apply with the foreign base: %+v", ag.calls)
	}
}

func TestPreviewStateCacheTTL(t *testing.T) {
	certs := &fakePreviewCertRepo{}
	zones := &fakePreviewZoneRepo{zone: &models.DNSZone{ID: "Z1", Name: "host.tld"}}
	r := previewReconciler(certs, zones, &fakePreviewRecordRepo{})

	st1 := r.previewStateCached(context.Background())
	if st1.base != "preview.host.tld" {
		t.Fatalf("base = %q", st1.base)
	}
	// Mutate the underlying settings; within TTL the cache must hold.
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: "other.tld"}}
	if st2 := r.previewStateCached(context.Background()); st2.base != "preview.host.tld" {
		t.Error("cache must serve within TTL")
	}
	// Expire and re-read — and a preview_base override wins (GH #836).
	r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{Hostname: "other.tld", PreviewBase: "203-0-113-7.sslip.io"}}
	r.previewAt = time.Now().Add(-2 * previewStateTTL)
	if st3 := r.previewStateCached(context.Background()); st3.base != "203-0-113-7.sslip.io" {
		t.Errorf("expired cache must re-read settings incl. preview_base, got %q", st3.base)
	}
}
