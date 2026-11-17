package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #327: HestiaCP stores installed certs flat under hestia/ssl/<domain>.{crt,key,ca}.
// scanHestiaSSL must push cert(+chain)+key per web domain that has them, append the
// CA chain, record the row, and skip domains with no cert.
func TestScanHestiaSSL(t *testing.T) {
	f := newImportFixture(t)
	sslDir := filepath.Join(f.extract, "hestia", "ssl")
	if err := os.MkdirAll(sslDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// site.example has crt+key+ca; noncert.example has nothing.
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(sslDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	certPEM, keyPEM := f.ca.validFor(t, "site.example")
	write("site.example.crt", certPEM)
	write("site.example.key", keyPEM)
	write("site.example.ca", f.ca.pem)
	f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
	f.domains.rows["noncert.example"] = newDomainRow("01DOMNOCERT000000000000000", "noncert.example", importUser)

	parsed := &ParsedTarball{ExtractDir: f.extract, DomainNames: []string{"site.example", "noncert.example"}}
	res := &SSLResult{}
	if !scanHestiaSSL(context.Background(), f.in(f.ca.pool()), parsed, res) {
		t.Fatal("scanHestiaSSL reported no hestia/ssl directory")
	}

	if res.Installed != 1 || len(f.agent.installs) != 1 {
		t.Fatalf("Installed = %d, calls = %d, want 1/1 (only site.example has a cert; skipped=%v)", res.Installed, len(f.agent.installs), res.Skipped)
	}
	c := f.agent.installs[0]
	if c["domain"] != "site.example" {
		t.Errorf("domain = %v, want site.example", c["domain"])
	}
	cert, _ := c["cert_pem"].(string)
	if strings.Count(cert, "BEGIN CERTIFICATE") != 2 {
		t.Errorf("cert_pem should carry the leaf and the chain, got %d certificates", strings.Count(cert, "BEGIN CERTIFICATE"))
	}
	if row := f.certs.rows["01DOMSITE00000000000000000"]; row == nil || row.IssueMethod != models.SSLIssueMethodRestored {
		t.Errorf("row = %+v, want a restored certificate row", row)
	}
}

// No hestia/ssl dir → reports false, no calls, no error.
func TestScanHestiaSSL_NoDir(t *testing.T) {
	f := newImportFixture(t)
	parsed := &ParsedTarball{ExtractDir: t.TempDir(), DomainNames: []string{"x.example"}}
	res := &SSLResult{}
	if scanHestiaSSL(context.Background(), f.in(f.ca.pool()), parsed, res) {
		t.Error("reported a hestia/ssl directory where there is none")
	}
	if res.Installed != 0 || len(f.agent.installs) != 0 {
		t.Errorf("expected no-op, got Installed=%d calls=%d", res.Installed, len(f.agent.installs))
	}
}
