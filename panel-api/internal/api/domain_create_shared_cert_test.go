package api

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-170 phase 5 / JAB-279: a web domain created under a covering shared
// certificate is attached to it at create (ssl_mode=shared, HTTPS at once, no
// ACME wait). The attach is a fast path, not an invariant: a lookup or attach
// failure must never fail the create. These pin the REST create door's stored
// state for each case.

type scCerts struct {
	repository.SharedCertificateRepository
	certs   []models.SharedCertificate
	listErr error
}

func (r *scCerts) ListServerWideAndOwned(_ context.Context, _ string) ([]models.SharedCertificate, error) {
	return r.certs, r.listErr
}

type scDomains struct {
	*dcDomains
	setErr  error
	setArgs []string
}

func (r *scDomains) SetSharedCertificate(_ context.Context, id string, certID *string, mode string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.setArgs = append(r.setArgs, id+"|"+*certID+"|"+mode)
	return nil
}

func TestCreateDomainOp_SharedCertAutoAttach(t *testing.T) {
	uname := "alice"
	owner := &models.User{ID: "u-alice", Email: "alice@example.com", Username: &uname}
	wildcard := `["*.example.com","example.com"]`
	other := `["*.other.test"]`
	certs := []models.SharedCertificate{
		{ID: "c-other", SANs: &other},
		{ID: "c-wild", SANs: &wildcard},
	}

	newH := func(sc *scCerts) (*domainHandler, *scDomains) {
		dom := &scDomains{dcDomains: newDCDomains()}
		cfg := DomainHandlerConfig{Users: newAbUsers(owner), Domains: dom}
		if sc != nil {
			cfg.SharedCerts = sc
		}
		return &domainHandler{cfg: cfg}, dom
	}
	create := func(h *domainHandler, in createDomainInput) *models.Domain {
		t.Helper()
		if in.OwnerID == "" {
			in.OwnerID = owner.ID
		}
		if in.Name == "" {
			in.Name = "shop.example.com"
		}
		if in.MailProvider == "" {
			in.MailProvider = models.MailProviderNone
		}
		d, oerr := createDomainOp(context.Background(), h, in)
		if oerr != nil {
			t.Fatalf("create must succeed, got %v", oerr)
		}
		return d
	}

	t.Run("covering cert is attached: row and response say shared", func(t *testing.T) {
		h, dom := newH(&scCerts{certs: certs})
		d := create(h, createDomainInput{})
		if d.SSLMode != models.SSLModeShared || d.SharedCertificateID == nil || *d.SharedCertificateID != "c-wild" {
			t.Fatalf("want shared/c-wild, got %s/%v", d.SSLMode, d.SharedCertificateID)
		}
		if len(dom.setArgs) != 1 || dom.setArgs[0] != d.ID+"|c-wild|"+models.SSLModeShared {
			t.Fatalf("want one SetSharedCertificate(%s, c-wild, shared), got %v", d.ID, dom.setArgs)
		}
	})

	t.Run("no covering cert keeps the requested mode", func(t *testing.T) {
		h, dom := newH(&scCerts{certs: certs})
		d := create(h, createDomainInput{Name: "shop.unrelated.test"})
		if d.SSLMode != models.SSLModeLE || d.SharedCertificateID != nil || len(dom.setArgs) != 0 {
			t.Fatalf("want le and no attach, got %s/%v %v", d.SSLMode, d.SharedCertificateID, dom.setArgs)
		}
	})

	t.Run("lookup failure fails open: create succeeds without a cert", func(t *testing.T) {
		h, dom := newH(&scCerts{listErr: errors.New("db down")})
		d := create(h, createDomainInput{})
		if d.SSLMode != models.SSLModeLE || d.SharedCertificateID != nil || len(dom.setArgs) != 0 {
			t.Fatalf("want le and no attach, got %s/%v %v", d.SSLMode, d.SharedCertificateID, dom.setArgs)
		}
	})

	t.Run("attach failure fails open: the response does not claim shared", func(t *testing.T) {
		h, dom := newH(&scCerts{certs: certs})
		dom.setErr = errors.New("db down")
		d := create(h, createDomainInput{})
		if d.SSLMode != models.SSLModeLE || d.SharedCertificateID != nil {
			t.Fatalf("a failed attach must not be reported as shared, got %s/%v", d.SSLMode, d.SharedCertificateID)
		}
	})

	t.Run("web-off domain is never attached", func(t *testing.T) {
		h, dom := newH(&scCerts{certs: certs})
		d := create(h, createDomainInput{WebDisabled: true})
		if d.SharedCertificateID != nil || len(dom.setArgs) != 0 {
			t.Fatalf("a web-off domain has no web cert, got %v %v", d.SharedCertificateID, dom.setArgs)
		}
	})

	t.Run("no shared-cert store configured skips the attach", func(t *testing.T) {
		h, dom := newH(nil)
		d := create(h, createDomainInput{})
		if d.SSLMode != models.SSLModeLE || len(dom.setArgs) != 0 {
			t.Fatalf("want le and no attach, got %s %v", d.SSLMode, dom.setArgs)
		}
	})
}
