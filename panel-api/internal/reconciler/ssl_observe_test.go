package reconciler

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// writeCert emits a self-signed PEM with the given notAfter, plus (optionally)
// a second certificate after it — fullchain.pem holds the leaf followed by the
// issuer chain, and the intermediate outlives the leaf by years.
func writeCert(t *testing.T, notAfter time.Time, withChain bool) string {
	t.Helper()
	mk := func(na time.Time) []byte {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "example.test"},
			NotBefore:    na.Add(-90 * 24 * time.Hour),
			NotAfter:     na,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatalf("cert: %v", err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	body := mk(notAfter)
	if withChain {
		// Issuer valid for years — if the parser took this block instead of the
		// leaf, an expired certificate would read as healthy.
		body = append(body, mk(notAfter.Add(5*365*24*time.Hour))...)
	}
	p := filepath.Join(t.TempDir(), "fullchain.pem")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func issuedCert(id, domain, path string, expires *time.Time) repository.SSLCertificateWithDomain {
	return repository.SSLCertificateWithDomain{
		ID: id, DomainName: domain, Status: models.SSLStatusIssued,
		CertPath: &path, ExpiresAt: expires,
	}
}

// The bug this pass exists for. Measured on a live box: the panel said
// 2026-08-15 while the certificate on disk ran to 2026-10-30 — 2.5 months
// stale, because certbot's timer renews outside the panel and nothing
// refreshed the row. The expiry alerts compute off that column.
func TestObserveDetectsStaleExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	real := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	stale := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)

	res, refresh := observeSSLCerts([]repository.SSLCertificateWithDomain{
		issuedCert("c1", "example.test", writeCert(t, real, true), &stale),
	}, now)

	if res.Refreshed != 1 {
		t.Fatalf("Refreshed = %d, want 1 — a row 2.5 months behind disk must be corrected", res.Refreshed)
	}
	got, ok := refresh["c1"]
	if !ok {
		t.Fatal("no refresh recorded for the stale certificate")
	}
	if !got.Equal(real) {
		t.Errorf("refresh = %v, want the notAfter from disk %v", got, real)
	}
	if len(res.Overdue) != 0 {
		t.Errorf("a cert valid for ~3 months is not overdue, got %v", res.Overdue)
	}
}

// fullchain.pem is leaf-then-chain. Reading the wrong block would report an
// expired certificate as healthy for years.
func TestObserveReadsTheLeafNotTheChain(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	leafExpiry := now.Add(10 * 24 * time.Hour) // inside the renewal window

	res, refresh := observeSSLCerts([]repository.SSLCertificateWithDomain{
		issuedCert("c1", "example.test", writeCert(t, leafExpiry, true), nil),
	}, now)

	if got := refresh["c1"]; !got.Equal(leafExpiry) {
		t.Fatalf("read notAfter %v, want the LEAF's %v — the chain's issuer outlives it by years", got, leafExpiry)
	}
	if len(res.Overdue) != 1 {
		t.Errorf("a cert 10 days from expiry must be overdue, got %v", res.Overdue)
	}
}

// The alert the issue asked for: judged on the FILE, because the row is the
// thing we don't trust. certbot-timer failures leave the row at
// status=issued/last_error=NULL, which is why the existing publisher never
// fired for them.
func TestObserveFlagsOverdueFromDiskNotFromRow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	// Row claims healthy far-future expiry; disk says 5 days.
	rosy := now.Add(200 * 24 * time.Hour)

	res, _ := observeSSLCerts([]repository.SSLCertificateWithDomain{
		issuedCert("c1", "late.test", writeCert(t, now.Add(5*24*time.Hour), false), &rosy),
	}, now)

	if len(res.Overdue) != 1 || res.Overdue[0] != "late.test" {
		t.Fatalf("Overdue = %v, want [late.test] — an optimistic row must not mask the file", res.Overdue)
	}
}

func TestObserveHealthyCertIsQuiet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	exp := now.Add(80 * 24 * time.Hour)

	res, refresh := observeSSLCerts([]repository.SSLCertificateWithDomain{
		issuedCert("c1", "fine.test", writeCert(t, exp, true), &exp),
	}, now)

	if res.Refreshed != 0 || len(refresh) != 0 {
		t.Errorf("a row that already matches disk needs no write, got %d", res.Refreshed)
	}
	if len(res.Overdue) != 0 {
		t.Errorf("80 days of validity is not overdue, got %v", res.Overdue)
	}
	if res.Checked != 1 {
		t.Errorf("Checked = %d, want 1", res.Checked)
	}
}

// "I could not read the file" must be neither "it is fine" nor "it failed to
// renew" — a permissions change must not page an operator about a healthy cert,
// and must not pass silently either.
func TestObserveUnreadableIsNeitherHealthyNorOverdue(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	res, refresh := observeSSLCerts([]repository.SSLCertificateWithDomain{
		issuedCert("c1", "gone.test", "/nonexistent/fullchain.pem", nil),
	}, now)

	if len(res.Overdue) != 0 {
		t.Errorf("an unreadable cert must NOT be reported as failing to renew, got %v", res.Overdue)
	}
	if len(res.Unreadable) != 1 || !strings.Contains(res.Unreadable[0], "gone.test") {
		t.Errorf("an unreadable cert must be recorded by name, got %v", res.Unreadable)
	}
	if len(refresh) != 0 {
		t.Errorf("nothing to refresh from a file we could not read, got %v", refresh)
	}
}

// Only issued certs with a path are the pass's business — pending/failed rows
// belong to the issuance flow.
func TestObserveSkipsNonIssuedAndPathless(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	p := writeCert(t, now.Add(60*24*time.Hour), false)
	empty := ""

	res, _ := observeSSLCerts([]repository.SSLCertificateWithDomain{
		{ID: "c1", DomainName: "pending.test", Status: models.SSLStatusPending, CertPath: &p},
		{ID: "c2", DomainName: "nopath.test", Status: models.SSLStatusIssued, CertPath: &empty},
		{ID: "c3", DomainName: "nil.test", Status: models.SSLStatusIssued, CertPath: nil},
	}, now)

	if res.Checked != 0 {
		t.Errorf("Checked = %d, want 0 — none of these are observable", res.Checked)
	}
}

// JAB-203's own lesson: StartSSLTicker was written, reviewed and merged with
// zero call sites, and drifted silently for months because nothing ran it.
// This pass is the replacement; assert it is actually reached from the tick.
func TestSSLObservationIsWiredIntoTheTick(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("reconciler.go")
	if err != nil {
		t.Fatalf("read reconciler.go: %v", err)
	}
	if !strings.Contains(string(src), "r.ReconcileSSLObservation(ctx)") {
		t.Fatal("reconciler.go never calls ReconcileSSLObservation. That is exactly how " +
			"StartSSLTicker died — present, reviewed, never run, and expires_at drifted " +
			"for months (JAB-203). Wire it into the tick loop.")
	}
}

// The renewal path we decided NOT to own must be gone, not left dormant for
// someone to wire later on the assumption it works.
func TestDeadPanelRenewalPathIsRemoved(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("ssl_ticker.go"); err == nil {
		t.Error("ssl_ticker.go still exists — certbot owns renewal (ADR-0163), so the " +
			"panel-driven ticker should be deleted rather than left as dead code")
	}
}
