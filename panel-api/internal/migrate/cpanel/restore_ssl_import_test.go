package cpanel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
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

// --- test fixtures -----------------------------------------------------------

type sslTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newSSLTestCA(t *testing.T) sslTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Import Test Root"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return sslTestCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (ca sslTestCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// leaf issues a certificate for names valid from notBefore to notAfter and
// returns its PEM and its key's PEM.
func (ca sslTestCA) leaf(t *testing.T, names []string, notBefore, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]},
		DNSNames: names, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}))
}

// validFor issues a certificate valid for 60 more days.
func (ca sslTestCA) validFor(t *testing.T, names ...string) (string, string) {
	return ca.leaf(t, names, time.Now().Add(-24*time.Hour), time.Now().Add(60*24*time.Hour))
}

const (
	importUser  = "01USERIMPORT00000000000000"
	otherUser   = "01USEROTHER000000000000000"
	importAcct  = "acct"
	certFilesLE = "/etc/letsencrypt/live/"
)

type fakeSSLDomains struct{ rows map[string]*models.Domain }

func (f *fakeSSLDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := f.rows[name]; ok {
		cp := *d
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func newDomainRow(id, name, userID string) *models.Domain {
	d := &models.Domain{ID: id, Name: name, UserID: userID, SSLMode: models.SSLModeLE}
	if err := markMigratedOwnership(d, time.Now()); err != nil {
		panic(err)
	}
	return d
}

type fakeSSLCerts struct {
	rows    map[string]*models.SSLCertificate // by domain ID
	created int
	updated int
}

func (f *fakeSSLCerts) FindByDomainID(_ context.Context, domainID string) (*models.SSLCertificate, error) {
	if c, ok := f.rows[domainID]; ok {
		cp := *c
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeSSLCerts) Create(_ context.Context, c *models.SSLCertificate) error {
	cp := *c
	f.rows[c.DomainID] = &cp
	f.created++
	return nil
}

func (f *fakeSSLCerts) UpdateInstalled(_ context.Context, c *models.SSLCertificate) error {
	cp := *c
	f.rows[c.DomainID] = &cp
	f.updated++
	return nil
}

// installAgent answers ssl.install_custom the way the agent does: with the
// domain's certbot lineage files.
type installAgent struct{ installs []map[string]any }

func (a *installAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	if command != "ssl.install_custom" {
		return json.RawMessage(`{}`), nil
	}
	m, _ := params.(map[string]any)
	a.installs = append(a.installs, m)
	d, _ := m["domain"].(string)
	out, _ := json.Marshal(map[string]string{
		"cert_path": certFilesLE + d + "/fullchain.pem",
		"key_path":  certFilesLE + d + "/privkey.pem",
	})
	return out, nil
}

type importFixture struct {
	extract string
	agent   *installAgent
	domains *fakeSSLDomains
	certs   *fakeSSLCerts
	ca      sslTestCA
	parsed  *ParsedTarball
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	extract := t.TempDir()
	if err := os.MkdirAll(filepath.Join(extract, "cpmove-"+importAcct, "apache_tls"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &importFixture{
		extract: extract,
		agent:   &installAgent{},
		domains: &fakeSSLDomains{rows: map[string]*models.Domain{}},
		certs:   &fakeSSLCerts{rows: map[string]*models.SSLCertificate{}},
		ca:      newSSLTestCA(t),
		parsed:  &ParsedTarball{ExtractDir: extract, SourceUser: importAcct},
	}
}

func (f *importFixture) tlsDir() string {
	return filepath.Join(f.extract, "cpmove-"+importAcct, "apache_tls")
}

// pkgacctEntry writes apache_tls/<name> the way pkgacct does: one file with
// the key, the certificate and the chain.
func (f *importFixture) pkgacctEntry(t *testing.T, name, certPEM, keyPEM string) {
	t.Helper()
	body := keyPEM + certPEM + f.ca.pem
	if err := os.WriteFile(filepath.Join(f.tlsDir(), name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *importFixture) in(roots *x509.CertPool) SSLImport {
	return SSLImport{
		Agent: f.agent, Domains: f.domains, Certs: f.certs,
		UserID: importUser, Preserve: true, Roots: roots,
		Now: time.Now,
	}
}

func (f *importFixture) run(t *testing.T) *SSLResult {
	t.Helper()
	res, err := ImportSSL(context.Background(), f.in(f.ca.pool()), f.parsed)
	if err != nil {
		t.Fatalf("ImportSSL: %v", err)
	}
	return res
}

// --- tests -------------------------------------------------------------------

// pkgacct stores each installed certificate as one file, apache_tls/<vhost>
// (key + certificate + chain), not as a directory. The import installs it and
// records it as a restored certificate the reconciler hands to Let's Encrypt
// later, over the placeholder row the reconciler made for the new domain.
func TestImportSSL_PkgacctFileEntry_InstalledAndRecorded(t *testing.T) {
	f := newImportFixture(t)
	f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
	retry := time.Now().Add(time.Hour)
	f.certs.rows["01DOMSITE00000000000000000"] = &models.SSLCertificate{
		ID: "01CERTSITE0000000000000000", DomainID: "01DOMSITE00000000000000000",
		Status: models.SSLStatusPendingACMERetry, RetryCount: 3, NextRetryAt: &retry,
	}
	certPEM, keyPEM := f.ca.validFor(t, "site.example", "www.site.example")
	f.pkgacctEntry(t, "site.example", certPEM, keyPEM)

	res := f.run(t)

	if res.Installed != 1 || len(f.agent.installs) != 1 {
		t.Fatalf("installed=%d agent installs=%d, want 1/1 (skipped=%v)", res.Installed, len(f.agent.installs), res.Skipped)
	}
	if got := f.agent.installs[0]["domain"]; got != "site.example" {
		t.Errorf("installed for %v, want site.example", got)
	}
	row := f.certs.rows["01DOMSITE00000000000000000"]
	if row.ID != "01CERTSITE0000000000000000" || f.certs.updated != 1 || f.certs.created != 0 {
		t.Fatalf("want the existing row updated in place: id=%s updated=%d created=%d", row.ID, f.certs.updated, f.certs.created)
	}
	if row.Status != models.SSLStatusIssued || row.IssueMethod != models.SSLIssueMethodRestored {
		t.Errorf("row status=%q method=%q, want issued/restored", row.Status, row.IssueMethod)
	}
	if row.CertPath == nil || *row.CertPath != certFilesLE+"site.example/fullchain.pem" {
		t.Errorf("cert path = %v", row.CertPath)
	}
	if row.ExpiresAt == nil || row.ExpiresAt.Before(time.Now().Add(59*24*time.Hour)) {
		t.Errorf("expires_at = %v, want the certificate's own expiry", row.ExpiresAt)
	}
	if row.NextRetryAt != nil || row.RetryCount != 0 || row.LastError != nil {
		t.Errorf("retry state left on the row: next=%v count=%d err=%v", row.NextRetryAt, row.RetryCount, row.LastError)
	}
}

// No certificate row yet: the import creates one in the restored state.
func TestImportSSL_NoRowYet_RestoredRowCreated(t *testing.T) {
	f := newImportFixture(t)
	f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
	certPEM, keyPEM := f.ca.validFor(t, "site.example")
	f.pkgacctEntry(t, "site.example", certPEM, keyPEM)

	res := f.run(t)

	row, ok := f.certs.rows["01DOMSITE00000000000000000"]
	if res.Installed != 1 || !ok || f.certs.created != 1 {
		t.Fatalf("installed=%d row=%v created=%d (skipped=%v)", res.Installed, ok, f.certs.created, res.Skipped)
	}
	if row.ID == "" || row.Status != models.SSLStatusIssued || row.IssueMethod != models.SSLIssueMethodRestored {
		t.Errorf("row id=%q status=%q method=%q, want a new issued/restored row", row.ID, row.Status, row.IssueMethod)
	}
}

// cPanel names an addon domain's vhost after its backing subdomain, so the
// certificate file carries that name. It is installed for the addon domain,
// the name the site has on Jabali.
func TestImportSSL_AddonVhostName_InstalledForAddonDomain(t *testing.T) {
	f := newImportFixture(t)
	ud := filepath.Join(f.extract, "cpmove-"+importAcct, "userdata")
	if err := os.MkdirAll(ud, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ud, "main"),
		[]byte("---\naddon_domains:\n  shop.example: shop.main.example\nmain_domain: main.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.parsed.DomainNames = []string{"main.example", "shop.example"}
	f.domains.rows["main.example"] = newDomainRow("01DOMMAIN00000000000000000", "main.example", importUser)
	f.domains.rows["shop.example"] = newDomainRow("01DOMSHOP00000000000000000", "shop.example", importUser)
	certPEM, keyPEM := f.ca.validFor(t, "shop.example", "www.shop.example", "shop.main.example")
	f.pkgacctEntry(t, "shop.main.example", certPEM, keyPEM)

	res := f.run(t)

	if res.Installed != 1 || len(f.agent.installs) != 1 || f.agent.installs[0]["domain"] != "shop.example" {
		t.Fatalf("want one install for shop.example, got installed=%d calls=%v (skipped=%v)", res.Installed, f.agent.installs, res.Skipped)
	}
	if _, ok := f.certs.rows["01DOMSHOP00000000000000000"]; !ok {
		t.Errorf("no certificate row for the addon domain")
	}
}

// The older directory layout (apache_tls/<domain>/combined), which the
// DirectAdmin converter writes, keeps working through the same checks.
func TestImportSSL_DirectoryLayout_StillInstalled(t *testing.T) {
	f := newImportFixture(t)
	f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
	certPEM, keyPEM := f.ca.validFor(t, "site.example")
	dir := filepath.Join(f.tlsDir(), "site.example")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "combined"), []byte(certPEM+f.ca.pem+keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	res := f.run(t)

	if res.Installed != 1 {
		t.Fatalf("installed=%d, want 1 (skipped=%v)", res.Installed, res.Skipped)
	}
	if row := f.certs.rows["01DOMSITE00000000000000000"]; row == nil || row.IssueMethod != models.SSLIssueMethodRestored {
		t.Errorf("row = %+v, want restored", row)
	}
}

// Every entry that must not be installed: nothing reaches the agent, no row
// changes, and the summary names the entry.
func TestImportSSL_Refusals(t *testing.T) {
	type setup func(t *testing.T, f *importFixture) string // returns the entry name
	other := newSSLTestCA(t)
	for _, tc := range []struct {
		name  string
		setup setup
	}{
		{"another account's domain", func(t *testing.T, f *importFixture) string {
			f.domains.rows["their.example"] = newDomainRow("01DOMTHEIRS000000000000000", "their.example", otherUser)
			c, k := f.ca.validFor(t, "their.example")
			f.pkgacctEntry(t, "their.example", c, k)
			return "their.example"
		}},
		{"another account's domain, directory layout", func(t *testing.T, f *importFixture) string {
			f.domains.rows["their.example"] = newDomainRow("01DOMTHEIRS000000000000000", "their.example", otherUser)
			c, k := f.ca.validFor(t, "their.example")
			dir := filepath.Join(f.tlsDir(), "their.example")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "combined"), []byte(c+f.ca.pem+k), 0o600); err != nil {
				t.Fatal(err)
			}
			return "their.example"
		}},
		{"no such domain on this server", func(t *testing.T, f *importFixture) string {
			c, k := f.ca.validFor(t, "absent.example")
			f.pkgacctEntry(t, "absent.example", c, k)
			return "absent.example"
		}},
		{"symlinked entry", func(t *testing.T, f *importFixture) string {
			f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
			c, k := f.ca.validFor(t, "site.example")
			target := filepath.Join(t.TempDir(), "elsewhere.pem")
			if err := os.WriteFile(target, []byte(k+c), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(f.tlsDir(), "site.example")); err != nil {
				t.Fatal(err)
			}
			return "site.example"
		}},
		{"expired certificate", func(t *testing.T, f *importFixture) string {
			f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
			c, k := f.ca.leaf(t, []string{"site.example"}, time.Now().Add(-90*24*time.Hour), time.Now().Add(-time.Hour))
			f.pkgacctEntry(t, "site.example", c, k)
			return "site.example"
		}},
		{"certificate for another name", func(t *testing.T, f *importFixture) string {
			f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
			c, k := f.ca.validFor(t, "unrelated.example")
			f.pkgacctEntry(t, "site.example", c, k)
			return "site.example"
		}},
		{"not signed by a trusted authority", func(t *testing.T, f *importFixture) string {
			f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
			c, k := other.validFor(t, "site.example")
			f.pkgacctEntry(t, "site.example", c, k)
			return "site.example"
		}},
		{"key does not match", func(t *testing.T, f *importFixture) string {
			f.domains.rows["site.example"] = newDomainRow("01DOMSITE00000000000000000", "site.example", importUser)
			c, _ := f.ca.validFor(t, "site.example")
			_, k := f.ca.validFor(t, "site.example")
			f.pkgacctEntry(t, "site.example", c, k)
			return "site.example"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t)
			entry := tc.setup(t, f)
			res := f.run(t)
			if res.Installed != 0 || len(f.agent.installs) != 0 {
				t.Fatalf("installed=%d agent installs=%d, want none", res.Installed, len(f.agent.installs))
			}
			if f.certs.created+f.certs.updated != 0 {
				t.Fatalf("certificate rows changed: created=%d updated=%d", f.certs.created, f.certs.updated)
			}
			if !strings.Contains(strings.Join(res.Skipped, "\n"), entry) {
				t.Errorf("summary does not name %s: %v", entry, res.Skipped)
			}
			for _, s := range res.Skipped {
				if strings.Contains(s, "PRIVATE KEY") {
					t.Fatalf("summary carries key material: %q", s)
				}
			}
		})
	}
}

// An archive without installed certificates says so in the summary.
func TestImportSSL_NoApacheTLS_Reported(t *testing.T) {
	extract := t.TempDir()
	in := SSLImport{Agent: &installAgent{}, Domains: &fakeSSLDomains{}, Certs: &fakeSSLCerts{rows: map[string]*models.SSLCertificate{}},
		UserID: importUser, Preserve: true, Now: time.Now}
	res, err := ImportSSL(context.Background(), in, &ParsedTarball{ExtractDir: extract, SourceUser: importAcct})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Skipped, "\n"), "no installed certificates") {
		t.Errorf("summary = %v, want a line saying the archive has no installed certificates", res.Skipped)
	}
}
