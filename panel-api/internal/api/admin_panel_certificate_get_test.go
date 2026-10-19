package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type servedJSON struct {
	Type        string    `json:"type"`
	Issuer      string    `json:"issuer"`
	Subject     string    `json:"subject"`
	Names       []string  `json:"names"`
	NotAfter    time.Time `json:"not_after"`
	Fingerprint string    `json:"fingerprint_sha256"`
}

type panelCertGetJSON struct {
	Certs []struct {
		Kind        string      `json:"kind"`
		Status      string      `json:"status"`
		Served      *servedJSON `json:"served"`
		ServedError string      `json:"served_error"`
	} `json:"certs"`
}

type servedRow struct {
	status string
	served *servedJSON
	err    string
}

func (f *panelCertFixture) getCerts(t *testing.T) map[string]servedRow {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/panel-certificate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	var body panelCertGetJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]servedRow{}
	for _, c := range body.Certs {
		out[c.Kind] = servedRow{status: c.Status, served: c.Served, err: c.ServedError}
	}
	return out
}

// writeCertWithIP writes a self-signed panel cert that also names ip.
func writeCertWithIP(t *testing.T, ip string, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "panel.example.com", Organization: []string{"Jabali Panel"}},
		NotBefore:    notAfter.AddDate(-10, 0, 0),
		NotAfter:     notAfter,
		DNSNames:     panelCertBothNames,
		IPAddresses:  []net.IP{net.ParseIP(ip)},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "panel.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// GH #2166: the Panel SSL tab shows the certificate each kind serves — read
// from its file, since the row's status can't tell a self-signed cert from a
// kept CA cert's issuer, names or real expiry.
func TestPanelCertGet_DescribesTheServedCerts(t *testing.T) {
	hostExp := time.Now().AddDate(10, 0, 0).UTC().Truncate(time.Second)
	mailExp := time.Now().AddDate(0, 2, 0).UTC().Truncate(time.Second)
	f := newPanelCertFixture(t, panelCertOpts{certPath: writeCertWithIP(t, "198.51.100.7", hostExp)})
	f.certs.rows[models.PanelCertKindMail].CertPEMPath =
		writePanelCertPEM(t, "Let's Encrypt", "mail.panel.example.com", []string{"mail.panel.example.com"}, mailExp)

	got := f.getCerts(t)
	host, mail := got[models.PanelCertKindHostname], got[models.PanelCertKindMail]
	if host.served == nil || mail.served == nil {
		t.Fatalf("served missing: hostname %+v, mail %+v", host, mail)
	}
	if host.served.Type != "self_signed" || host.served.Issuer != "Jabali Panel" ||
		host.served.Subject != "panel.example.com" || !host.served.NotAfter.Equal(hostExp) ||
		strings.Join(host.served.Names, ",") != strings.Join(append(panelCertBothNames, "198.51.100.7"), ",") {
		t.Errorf("hostname served %+v", host.served)
	}
	if mail.served.Type != "lets_encrypt" || mail.served.Issuer != "Let's Encrypt" ||
		!mail.served.NotAfter.Equal(mailExp) || len(mail.served.Fingerprint) != 95 {
		t.Errorf("mail served %+v", mail.served)
	}
	if host.err != "" || mail.err != "" {
		t.Errorf("served_error set: %q %q", host.err, mail.err)
	}
}

// A missing or unusable file says why instead of failing the whole GET.
func TestPanelCertGet_ServedErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "panel.crt")
	garbage := filepath.Join(dir, "panel-mail.crt")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newPanelCertFixture(t, panelCertOpts{certPath: missing})
	f.certs.rows[models.PanelCertKindMail].CertPEMPath = garbage

	got := f.getCerts(t)
	host, mail := got[models.PanelCertKindHostname], got[models.PanelCertKindMail]
	if host.served != nil || host.err != "No certificate at "+missing {
		t.Errorf("missing file: %+v", host)
	}
	if mail.served != nil || mail.err != garbage+" holds no valid certificate" {
		t.Errorf("garbage file: %+v", mail)
	}
}

// JAB-179: the public demo masks the box's IP in the served names too.
func TestPanelCertGet_DemoMasksIPNames(t *testing.T) {
	withDemoRedaction(t, true)
	f := newPanelCertFixture(t, panelCertOpts{certPath: writeCertWithIP(t, "83.151.202.27", time.Now().AddDate(1, 0, 0))})
	host := f.getCerts(t)[models.PanelCertKindHostname]
	if host.served == nil {
		t.Fatalf("served missing: %+v", host)
	}
	joined := strings.Join(host.served.Names, ",")
	if strings.Contains(joined, "83.151.202.27") || !strings.Contains(joined, demoPlaceholderIPv4) {
		t.Errorf("demo names %q, want the IP masked", joined)
	}
	if !strings.Contains(joined, "panel.example.com") {
		t.Errorf("demo names %q lost the DNS names", joined)
	}
}

// A row without a path reads its kind's default file.
func TestServedPanelCert_DefaultPathPerKind(t *testing.T) {
	for kind, want := range map[string]string{
		models.PanelCertKindHostname: "/etc/jabali/tls/panel.crt",
		models.PanelCertKindMail:     "/etc/jabali/tls/panel-mail.crt",
	} {
		if _, err := os.Stat(want); err == nil {
			t.Skipf("%s exists on this machine", want)
		}
		if _, msg := servedPanelCert(&models.PanelCertificate{Kind: kind}); msg != "No certificate at "+want {
			t.Errorf("%s: %q, want it to read %s", kind, msg, want)
		}
	}
}

// GH #2166: with the mail module off there is no mail certificate to show,
// so the Panel SSL tab offers no mail card and no action for it.
func TestPanelCertGet_OmitsMailWithTheModuleOff(t *testing.T) {
	f := newPanelCertFixture(t, panelCertOpts{mailOff: true})
	got := f.getCerts(t)
	if _, ok := got[models.PanelCertKindMail]; ok {
		t.Errorf("mail cert listed with the mail module off: %+v", got)
	}
	if _, ok := got[models.PanelCertKindHostname]; !ok {
		t.Errorf("hostname cert missing: %+v", got)
	}
}
