package reconciler

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestReconcileSSL_ModeRouting verifies reconcileSSLForDomain dispatches the
// right agent command per ssl_mode (GH #246).
func TestReconcileSSL_ModeRouting(t *testing.T) {
	hasCall := func(ag *fakeAgent, method string) bool {
		for _, c := range ag.calls {
			if c.method == method {
				return true
			}
		}
		return false
	}
	run := func(mode string, cert *models.SSLCertificate) *fakeAgent {
		ag := &fakeAgent{}
		dr := &fakeDomainRepo{domains: map[string]*models.Domain{}}
		sc := newFakeSSLCertRepo()
		ss := &fakeServerSettingsRepo{settings: &models.ServerSettings{Hostname: "host.example.com", AdminEmail: "admin@example.com"}}
		r := New(dr, nil, ag, slog.Default(), Config{}).WithSSLCerts(sc)
		r.serverSettings = ss
		dom := &models.Domain{ID: "d1", Name: "example.com", SSLMode: mode}
		if cert != nil {
			cert.DomainID = "d1"
			sc.byDomain["d1"] = cert
		}
		r.reconcileSSLForDomain(context.Background(), dom)
		return ag
	}

	t.Run("self mode self-signs", func(t *testing.T) {
		ag := run(models.SSLModeSelf, nil)
		require.True(t, hasCall(ag, "ssl.self_sign"), "self mode must call ssl.self_sign")
		require.False(t, hasCall(ag, "ssl.issue"), "self mode must NOT attempt ACME")
	})

	// GH #896/#887: a fresh LE domain (no cert row) must bootstrap a
	// self-signed placeholder on the first tick — NOT attempt ACME, whose
	// webroot (the domain's docroot) is created later in the same tick.
	t.Run("le mode bootstraps self-signed on first tick", func(t *testing.T) {
		ag := run(models.SSLModeLE, nil)
		require.True(t, hasCall(ag, "ssl.self_sign"), "le first tick must bootstrap self-signed")
		require.False(t, hasCall(ag, "ssl.issue"), "le first tick must NOT attempt ACME before the docroot exists")
	})

	// A fresh pending row with no cert file yet takes the same bootstrap path.
	t.Run("le mode fresh pending row bootstraps self-signed", func(t *testing.T) {
		ag := run(models.SSLModeLE, &models.SSLCertificate{ID: "c1", Status: models.SSLStatusPending, RetryCount: 0})
		require.True(t, hasCall(ag, "ssl.self_sign"))
		require.False(t, hasCall(ag, "ssl.issue"))
	})

	// Once a self-signed placeholder exists, the next tick upgrades to LE
	// (the docroot exists by now), so ACME is attempted.
	t.Run("le mode self_signed upgrades via ACME", func(t *testing.T) {
		cp, kp := "/etc/ssl/jabali-selfsigned/example.com/cert.pem", "/etc/ssl/jabali-selfsigned/example.com/key.pem"
		ag := run(models.SSLModeLE, &models.SSLCertificate{ID: "c1", Status: models.SSLStatusSelfSigned, CertPath: &cp, KeyPath: &kp})
		require.True(t, hasCall(ag, "ssl.issue"), "self_signed le domain must attempt the LE upgrade")
	})

	t.Run("none mode revokes issued cert", func(t *testing.T) {
		ag := run(models.SSLModeNone, &models.SSLCertificate{ID: "c1", Status: models.SSLStatusIssued})
		require.True(t, hasCall(ag, "ssl.revoke"), "none mode must revoke an issued cert")
	})

	t.Run("none mode no cert is a no-op", func(t *testing.T) {
		ag := run(models.SSLModeNone, nil)
		require.Empty(t, ag.calls, "none mode with no cert must not call the agent")
	})

	t.Run("custom mode does not touch the cert", func(t *testing.T) {
		ag := run(models.SSLModeCustom, nil)
		require.False(t, hasCall(ag, "ssl.self_sign"))
		require.False(t, hasCall(ag, "ssl.issue"))
		require.False(t, hasCall(ag, "ssl.revoke"), "custom is operator-managed; reconciler must not revoke it")
	})
}

// TestReconcileSSL_NoneClearsEvenIfRevokeFails covers GH #246: switching to
// None must clear the local cert (so the vhost drops :443 + the http->https
// redirect) even when the LE ssl.revoke agent call fails. Previously a failed
// revoke left the cert with its path set, so the site kept redirecting to a
// dead :443.
func TestReconcileSSL_NoneClearsEvenIfRevokeFails(t *testing.T) {
	ag := &fakeAgent{failMethod: "ssl.revoke"}
	dr := &fakeDomainRepo{domains: map[string]*models.Domain{}}
	sc := newFakeSSLCertRepo()
	certPath, keyPath := "/etc/x/cert.pem", "/etc/x/key.pem"
	sc.byDomain["d1"] = &models.SSLCertificate{
		ID: "c1", DomainID: "d1", Status: models.SSLStatusIssued,
		CertPath: &certPath, KeyPath: &keyPath,
	}
	r := New(dr, nil, ag, slog.Default(), Config{}).WithSSLCerts(sc)
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{Hostname: "host.example.com"}}
	dom := &models.Domain{ID: "d1", Name: "example.com", SSLMode: models.SSLModeNone}

	r.reconcileSSLForDomain(context.Background(), dom)

	if !sc.revoked["c1"] {
		t.Fatal("None mode must MarkRevoked (clear local cert) even when ssl.revoke fails")
	}
}
