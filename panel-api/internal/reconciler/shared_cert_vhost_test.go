package reconciler

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2545: a domain attached to a shared certificate serves the paths its
// shared_certificates row records. A wildcard the panel issued from Let's
// Encrypt lives in its certbot lineage, not under sharedCertDir, so building
// the path from the id rendered such a domain HTTP-only.

type vhostSharedCertRepo struct {
	repository.SharedCertificateRepository
	rows  map[string]*models.SharedCertificate
	err   error
	reads int
}

func (f *vhostSharedCertRepo) FindByID(_ context.Context, id string) (*models.SharedCertificate, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	if c, ok := f.rows[id]; ok {
		return c, nil
	}
	return nil, repository.ErrNotFound
}

func sharedVhostFixture(t *testing.T, row *models.SharedCertificate, findErr error) (*Reconciler, *fakeAgent, *vhostSharedCertRepo) {
	t.Helper()
	r, ag, dom := plannerFixture(t)
	id := "shared-1"
	dom.SSLMode = models.SSLModeShared
	dom.SharedCertificateID = &id
	repo := &vhostSharedCertRepo{rows: map[string]*models.SharedCertificate{}, err: findErr}
	if row != nil {
		row.ID = id
		repo.rows[id] = row
	}
	r.WithSharedCerts(repo)
	return r, ag, repo
}

func reconcileExample(t *testing.T, r *Reconciler, ag *fakeAgent) []fakeCall {
	t.Helper()
	if err := r.ReconcileOne(context.Background(), "domain-1"); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return append([]fakeCall(nil), ag.calls...)
}

func TestSharedCertVhost_ACMEWildcardServesItsLineage(t *testing.T) {
	r, ag, _ := sharedVhostFixture(t, &models.SharedCertificate{
		Source:   models.SharedCertSourceACME,
		CertPath: strp("/etc/letsencrypt/live/wild-example/fullchain.pem"),
		KeyPath:  strp("/etc/letsencrypt/live/wild-example/privkey.pem"),
	}, nil)
	p := domainCreateFor(t, reconcileExample(t, r, ag), "example.com")
	if p["ssl_cert_path"] != "/etc/letsencrypt/live/wild-example/fullchain.pem" ||
		p["ssl_key_path"] != "/etc/letsencrypt/live/wild-example/privkey.pem" {
		t.Fatalf("ssl paths = %v / %v, want the certificate's lineage", p["ssl_cert_path"], p["ssl_key_path"])
	}
	if p["serve_https"] != true || p["redirect_https"] != true {
		t.Fatalf("serve_https=%v redirect_https=%v, want both on for a trusted shared cert", p["serve_https"], p["redirect_https"])
	}
}

func TestSharedCertVhost_UploadedCertKeepsItsPath(t *testing.T) {
	cert, key := sharedCertPaths("shared-1")
	r, ag, _ := sharedVhostFixture(t, &models.SharedCertificate{
		CertPath: strp(cert),
		KeyPath:  strp(key),
	}, nil)
	p := domainCreateFor(t, reconcileExample(t, r, ag), "example.com")
	if p["ssl_cert_path"] != cert || p["ssl_key_path"] != key {
		t.Fatalf("ssl paths = %v / %v, want %s / %s", p["ssl_cert_path"], p["ssl_key_path"], cert, key)
	}
}

// Not issued yet: no :443 until it lands; then the payload changes and the
// next run dispatches it.
func TestSharedCertVhost_NotIssuedYetServesHTTP(t *testing.T) {
	r, ag, _ := sharedVhostFixture(t, &models.SharedCertificate{Source: models.SharedCertSourceACME}, nil)
	p := domainCreateFor(t, reconcileExample(t, r, ag), "example.com")
	if _, ok := p["ssl_cert_path"]; ok {
		t.Fatalf("a shared cert with no paths yet must send no ssl_cert_path, got %v", p["ssl_cert_path"])
	}
	if p["serve_https"] != false {
		t.Fatalf("serve_https = %v, want false", p["serve_https"])
	}
}

// A failed lookup must not render the domain HTTP-only: nothing is sent.
func TestSharedCertVhost_LookupErrorDefersTheVhost(t *testing.T) {
	r, ag, _ := sharedVhostFixture(t, nil, errors.New("db down"))
	calls := reconcileExample(t, r, ag)
	if n := len(indexesOf(calls, "domain.create", "domain", "example.com")); n != 0 {
		t.Fatalf("domain.create sent %d times on a failed shared-cert lookup, want 0", n)
	}
}
