package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// SECURITY (GH #1993): a certificate row's cert_path and key_path are written
// into nginx configs, as root, by the vhost renderers. An uploaded backup is
// untrusted, so a restored row keeps them only when they name this domain's
// own files; anything else is cleared and the certificate issued again.

type spCerts struct {
	repository.SSLCertificateRepository
	created []models.SSLCertificate
}

func (r *spCerts) Create(_ context.Context, c *models.SSLCertificate) error {
	r.created = append(r.created, *c)
	return nil
}

func spRestore(t *testing.T, status, cert, key string) (models.SSLCertificate, ApplyResult) {
	t.Helper()
	pools := &ppPools{}
	certs := &spCerts{}
	meta := ppMeta()
	meta.PHPPools = nil
	meta.Domains[0].PHPPoolID = nil
	meta.Domains[0].Mailboxes = nil
	meta.Domains[0].SSLCertificate = &internalbackup.MetadataSSLCert{ID: "c1", Status: status, CertPath: &cert, KeyPath: &key}
	deps := ppDeps(pools, &ppDomains{pools: pools}, &dcMailboxes{})
	deps.SSLCerts = certs

	r := Apply(context.Background(), meta, deps)

	if len(certs.created) != 1 {
		t.Fatalf("certificates created = %+v (errors %v), want 1", certs.created, r.Errors)
	}
	return certs.created[0], r
}

func TestApply_CertPathCarryingNginxConfigIsCleared(t *testing.T) {
	got, r := spRestore(t, models.SSLStatusIssued,
		"/etc/letsencrypt/live/alice.org/fullchain.pem;\n}\nserver { listen 80; root /etc; }\n#",
		"/etc/letsencrypt/live/alice.org/privkey.pem")

	if got.CertPath != nil || got.KeyPath != nil {
		t.Fatalf("paths = %v, %v; want both cleared", got.CertPath, got.KeyPath)
	}
	if got.Status != models.SSLStatusPending {
		t.Fatalf("status = %q, want %q so the certificate is issued again", got.Status, models.SSLStatusPending)
	}
	if !hasError(r.Errors, "issued again") {
		t.Fatalf("errors %v should say the certificate will be issued again", r.Errors)
	}
}

func TestApply_AnotherDomainsCertFilesAreCleared(t *testing.T) {
	got, _ := spRestore(t, models.SSLStatusIssued,
		"/etc/letsencrypt/live/bob.org/fullchain.pem", "/etc/letsencrypt/live/bob.org/privkey.pem")

	if got.CertPath != nil || got.KeyPath != nil || got.Status != models.SSLStatusPending {
		t.Fatalf("row = %+v, want paths cleared and status pending", got)
	}
}

func TestApply_UnknownCertStatusBecomesPending(t *testing.T) {
	got, _ := spRestore(t, "owned",
		"/etc/letsencrypt/live/alice.org/fullchain.pem", "/etc/letsencrypt/live/alice.org/privkey.pem")

	if got.Status != models.SSLStatusPending {
		t.Fatalf("status = %q, want %q", got.Status, models.SSLStatusPending)
	}
}

// The domain's own files, in either layout the panel writes, are kept as-is.
func TestApply_DomainsOwnCertFilesAreKept(t *testing.T) {
	for _, dir := range []string{"/etc/letsencrypt/live/alice.org/", "/etc/ssl/jabali-selfsigned/alice.org/"} {
		got, r := spRestore(t, models.SSLStatusIssued, dir+"fullchain.pem", dir+"privkey.pem")
		if got.CertPath == nil || *got.CertPath != dir+"fullchain.pem" || got.KeyPath == nil || *got.KeyPath != dir+"privkey.pem" {
			t.Fatalf("%s: paths = %v, %v; want kept", dir, got.CertPath, got.KeyPath)
		}
		if got.Status != models.SSLStatusIssued || len(r.Errors) != 0 {
			t.Fatalf("%s: status %q errors %v; want issued and no errors", dir, got.Status, r.Errors)
		}
	}
}
