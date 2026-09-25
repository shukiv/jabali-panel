package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakeCertLister struct {
	certs   []models.SharedCertificate
	err     error
	ownerID string
	calls   int
}

func (f *fakeCertLister) ListServerWideAndOwned(_ context.Context, ownerID string) ([]models.SharedCertificate, error) {
	f.calls++
	f.ownerID = ownerID
	return f.certs, f.err
}

type fakeCertSetter struct {
	err  error
	args []string
}

func (f *fakeCertSetter) SetSharedCertificate(_ context.Context, id string, certID *string, mode string) error {
	if f.err != nil {
		return f.err
	}
	f.args = append(f.args, id+"|"+*certID+"|"+mode)
	return nil
}

func TestAttachCoveringSharedCert(t *testing.T) {
	wild := `["*.example.com"]`
	other := `["*.other.test"]`
	certs := []models.SharedCertificate{{ID: "c-other", SANs: &other}, {ID: "c-wild", SANs: &wild}}
	newDomain := func() *models.Domain {
		return &models.Domain{ID: "d1", UserID: "u1", Name: "shop.example.com", SSLMode: models.SSLModeLE}
	}

	t.Run("covering cert attaches and updates the domain", func(t *testing.T) {
		l, s, d := &fakeCertLister{certs: certs}, &fakeCertSetter{}, newDomain()
		cert, err := AttachCoveringSharedCert(context.Background(), SharedCertDeps{Certs: l, Domains: s}, d)
		if err != nil || cert == nil || cert.ID != "c-wild" {
			t.Fatalf("want c-wild attached, got %v %v", cert, err)
		}
		if l.ownerID != "u1" {
			t.Fatalf("candidates must be read for the domain's owner, got %q", l.ownerID)
		}
		if len(s.args) != 1 || s.args[0] != "d1|c-wild|"+models.SSLModeShared {
			t.Fatalf("want SetSharedCertificate(d1, c-wild, shared), got %v", s.args)
		}
		if d.SSLMode != models.SSLModeShared || d.SharedCertificateID == nil || *d.SharedCertificateID != "c-wild" {
			t.Fatalf("domain not updated: %s/%v", d.SSLMode, d.SharedCertificateID)
		}
	})

	t.Run("no covering cert is a silent no-op", func(t *testing.T) {
		l, s, d := &fakeCertLister{certs: []models.SharedCertificate{{ID: "c-other", SANs: &other}}}, &fakeCertSetter{}, newDomain()
		cert, err := AttachCoveringSharedCert(context.Background(), SharedCertDeps{Certs: l, Domains: s}, d)
		if cert != nil || err != nil || len(s.args) != 0 || d.SSLMode != models.SSLModeLE {
			t.Fatalf("want no attach, got %v %v %v %s", cert, err, s.args, d.SSLMode)
		}
	})

	t.Run("lookup failure → ErrSharedCertLookup printing the store error alone", func(t *testing.T) {
		store := errors.New("db down")
		l, s, d := &fakeCertLister{err: store}, &fakeCertSetter{}, newDomain()
		cert, err := AttachCoveringSharedCert(context.Background(), SharedCertDeps{Certs: l, Domains: s}, d)
		if cert != nil || !errors.Is(err, ErrSharedCertLookup) || !errors.Is(err, store) || err.Error() != "db down" {
			t.Fatalf("want ErrSharedCertLookup(db down), got %v %v", cert, err)
		}
		if d.SSLMode != models.SSLModeLE || d.SharedCertificateID != nil {
			t.Fatalf("domain must be unchanged, got %s/%v", d.SSLMode, d.SharedCertificateID)
		}
	})

	t.Run("attach failure → ErrSharedCertAttach with the cert, domain unchanged", func(t *testing.T) {
		store := errors.New("db down")
		l, s, d := &fakeCertLister{certs: certs}, &fakeCertSetter{err: store}, newDomain()
		cert, err := AttachCoveringSharedCert(context.Background(), SharedCertDeps{Certs: l, Domains: s}, d)
		if !errors.Is(err, ErrSharedCertAttach) || errors.Is(err, ErrSharedCertLookup) || err.Error() != "db down" {
			t.Fatalf("want ErrSharedCertAttach(db down), got %v", err)
		}
		if cert == nil || cert.ID != "c-wild" {
			t.Fatalf("the covering cert must be returned for the retry hint, got %v", cert)
		}
		if d.SSLMode != models.SSLModeLE || d.SharedCertificateID != nil {
			t.Fatalf("a failed attach must not be reported as shared, got %s/%v", d.SSLMode, d.SharedCertificateID)
		}
	})

	t.Run("web-off domain is skipped without a lookup", func(t *testing.T) {
		l, s, d := &fakeCertLister{certs: certs}, &fakeCertSetter{}, newDomain()
		d.WebDisabled = true
		cert, err := AttachCoveringSharedCert(context.Background(), SharedCertDeps{Certs: l, Domains: s}, d)
		if cert != nil || err != nil || l.calls != 0 || len(s.args) != 0 {
			t.Fatalf("want skip, got %v %v calls=%d %v", cert, err, l.calls, s.args)
		}
	})

	t.Run("nil store is skipped", func(t *testing.T) {
		s, d := &fakeCertSetter{}, newDomain()
		cert, err := AttachCoveringSharedCert(context.Background(), SharedCertDeps{Domains: s}, d)
		if cert != nil || err != nil || len(s.args) != 0 {
			t.Fatalf("want skip, got %v %v %v", cert, err, s.args)
		}
	})
}
