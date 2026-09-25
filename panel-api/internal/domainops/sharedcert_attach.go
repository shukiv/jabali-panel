package domainops

import (
	"context"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Create-time shared-certificate auto-attach (JAB-170 phase 5 / JAB-279 AC1).
// A new web domain whose name a server-wide or owner-owned shared certificate
// already covers is attached to it at create, so it serves HTTPS at once with
// no ACME wait. The REST create door and `jabali domain create` each carried
// their own list → cover → attach sequence; both now call
// AttachCoveringSharedCert. The cover decision itself is CoveringSharedCert.
//
// The attach is a fast path, not an invariant: the reconciler still issues or
// attaches a certificate on its next tick. Each adapter therefore treats both
// errors below as soft (the REST door logs, the CLI prints a warning) and the
// create succeeds either way.

var (
	// ErrSharedCertLookup means the candidate certificates could not be read.
	ErrSharedCertLookup = errors.New("domainops: shared-certificate lookup failed")
	// ErrSharedCertAttach means a covering certificate was found but storing the
	// attachment failed. The domain keeps its original SSL mode.
	ErrSharedCertAttach = errors.New("domainops: shared-certificate attach failed")
)

// SharedCertLister reads the certificates a domain owned by ownerID may use.
type SharedCertLister interface {
	ListServerWideAndOwned(ctx context.Context, ownerID string) ([]models.SharedCertificate, error)
}

// SharedCertSetter stores a domain's shared-certificate attachment.
type SharedCertSetter interface {
	SetSharedCertificate(ctx context.Context, id string, sharedCertID *string, mode string) error
}

// SharedCertDeps are the collaborators AttachCoveringSharedCert needs. A nil
// Certs means this host has no shared-certificate store; the attach is skipped.
type SharedCertDeps struct {
	Certs   SharedCertLister
	Domains SharedCertSetter
}

// AttachCoveringSharedCert attaches d to the first shared certificate that
// covers its name, stores ssl_mode=shared, and updates d in place. It returns
// the attached certificate, or nil when nothing was attached.
//
// A web-disabled domain has no web certificate, so it is never attached. Only
// the default mode (le, or unset) is upgraded: a caller that explicitly asked
// for self or none keeps that mode (maintainer decision, 2026-09-26) — a
// covering shared cert must not turn a deliberately TLS-less domain into an
// HTTPS one. On ErrSharedCertAttach the covering certificate is returned
// alongside the error (so an adapter can name it in a retry hint) and d is left
// unchanged. Errors print the store error alone, without the sentinel text.
func AttachCoveringSharedCert(ctx context.Context, deps SharedCertDeps, d *models.Domain) (*models.SharedCertificate, error) {
	if d.WebDisabled || deps.Certs == nil {
		return nil, nil
	}
	if d.SSLMode != "" && d.SSLMode != models.SSLModeLE {
		return nil, nil
	}
	certs, err := deps.Certs.ListServerWideAndOwned(ctx, d.UserID)
	if err != nil {
		return nil, &kindError{kind: ErrSharedCertLookup, cause: err}
	}
	cert := CoveringSharedCert(certs, d.Name)
	if cert == nil {
		return nil, nil
	}
	if err := deps.Domains.SetSharedCertificate(ctx, d.ID, &cert.ID, models.SSLModeShared); err != nil {
		return cert, &kindError{kind: ErrSharedCertAttach, cause: err}
	}
	d.SSLMode = models.SSLModeShared
	d.SharedCertificateID = &cert.ID
	return cert, nil
}
