package panelcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func makePEM(t *testing.T, org, cn string, dnsNames []string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	name := pkix.Name{CommonName: cn}
	if org != "" {
		name.Organization = []string{org}
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      name,
		NotBefore:    notAfter.AddDate(-1, 0, 0),
		NotAfter:     notAfter,
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestCheck(t *testing.T) {
	now := time.Now()
	later, earlier := now.AddDate(1, 0, 0), now.Add(-time.Hour)
	both := []string{"panel.example.com", "mail.panel.example.com", "localhost"}

	for _, tc := range []struct {
		name   string
		pem    []byte
		host   string
		want   Action
		reason string
		issuer string
	}{
		{name: "missing", pem: nil, host: "panel.example.com", want: Regenerate, reason: "cert missing or unreadable"},
		{name: "not PEM", pem: []byte("garbage"), host: "panel.example.com", want: Regenerate, reason: "cert not PEM"},
		{name: "self-signed, current", pem: makePEM(t, SelfSignOrg, "panel.example.com", both, later), host: "panel.example.com", want: UpToDate, issuer: SelfSignOrg},
		{name: "self-signed, hostname in another case", pem: makePEM(t, SelfSignOrg, "Panel.Example.com", both, later), host: "panel.example.com", want: UpToDate, issuer: SelfSignOrg},
		{name: "no issuer organization counts as ours", pem: makePEM(t, "", "panel.example.com", both, later), host: "panel.example.com", want: UpToDate},
		{name: "self-signed for another hostname", pem: makePEM(t, SelfSignOrg, "old.example.com", []string{"old.example.com", "mail.old.example.com"}, later), host: "panel.example.com", want: Regenerate, reason: "CN old.example.com != panel.example.com", issuer: SelfSignOrg},
		{name: "self-signed without the mail name", pem: makePEM(t, SelfSignOrg, "panel.example.com", []string{"panel.example.com"}, later), host: "panel.example.com", want: Regenerate, reason: "missing SAN mail.panel.example.com", issuer: SelfSignOrg},
		{name: "self-signed, expired", pem: makePEM(t, SelfSignOrg, "panel.example.com", both, earlier), host: "panel.example.com", want: Regenerate, reason: "cert expired", issuer: SelfSignOrg},
		{name: "Let's Encrypt, current, even for another name", pem: makePEM(t, "Let's Encrypt", "old.example.com", []string{"old.example.com"}, later), host: "panel.example.com", want: KeepCA, reason: "cert issued by Let's Encrypt", issuer: "Let's Encrypt"},
		{name: "Let's Encrypt, expired", pem: makePEM(t, "Let's Encrypt", "panel.example.com", []string{"panel.example.com"}, earlier), host: "panel.example.com", want: Regenerate, reason: "cert issued by Let's Encrypt expired", issuer: "Let's Encrypt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := Check(tc.pem, tc.host, now)
			if v.Action != tc.want {
				t.Fatalf("action = %d (%q), want %d", v.Action, v.Reason, tc.want)
			}
			if tc.reason != "" && v.Reason != tc.reason {
				t.Errorf("reason = %q, want %q", v.Reason, tc.reason)
			}
			if v.Issuer != tc.issuer {
				t.Errorf("issuer = %q, want %q", v.Issuer, tc.issuer)
			}
			if len(tc.pem) > len("garbage") && v.NotAfter.IsZero() {
				t.Errorf("NotAfter not set for a readable cert")
			}
		})
	}
}

// GH #2166: the panel's self-signed mail certificate is checked against its
// own names: the mail row's name and mail.<hostname>.
func TestCheckNames_Mail(t *testing.T) {
	now := time.Now()
	later := now.AddDate(1, 0, 0)
	names := MailNames("mail.panel.example.com", "panel.example.com")
	custom := MailNames("mx.example.net", "panel.example.com")

	for _, tc := range []struct {
		name   string
		pem    []byte
		cn     string
		names  []string
		want   Action
		reason string
	}{
		{name: "self-signed mail cert, current", pem: makePEM(t, SelfSignOrg, "mail.panel.example.com", names, later), cn: "mail.panel.example.com", names: names, want: UpToDate, reason: "up to date for mail.panel.example.com"},
		{name: "the hostname cert is not the mail cert", pem: makePEM(t, SelfSignOrg, "panel.example.com", []string{"panel.example.com", "mail.panel.example.com"}, later), cn: "mail.panel.example.com", names: names, want: Regenerate, reason: "CN panel.example.com != mail.panel.example.com"},
		{name: "custom mail hostname also needs mail.<hostname>", pem: makePEM(t, SelfSignOrg, "mx.example.net", []string{"mx.example.net"}, later), cn: "mx.example.net", names: custom, want: Regenerate, reason: "missing SAN mail.panel.example.com"},
		{name: "live Let's Encrypt mail cert kept", pem: makePEM(t, "Let's Encrypt", "mail.panel.example.com", []string{"mail.panel.example.com"}, later), cn: "mail.panel.example.com", names: names, want: KeepCA},
		{name: "expired Let's Encrypt mail cert replaced", pem: makePEM(t, "Let's Encrypt", "mail.panel.example.com", []string{"mail.panel.example.com"}, now.Add(-time.Hour)), cn: "mail.panel.example.com", names: names, want: Regenerate, reason: "cert issued by Let's Encrypt expired"},
		{name: "missing", pem: nil, cn: "mail.panel.example.com", names: names, want: Regenerate, reason: "cert missing or unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := CheckNames(tc.pem, tc.cn, tc.names, now)
			if v.Action != tc.want || (tc.reason != "" && v.Reason != tc.reason) {
				t.Errorf("got %d %q, want %d %q", v.Action, v.Reason, tc.want, tc.reason)
			}
		})
	}
}

func TestMailNames(t *testing.T) {
	for _, tc := range []struct{ mail, host, want string }{
		{"mail.panel.example.com", "panel.example.com", "mail.panel.example.com"},
		{"mx.example.net", "panel.example.com", "mx.example.net mail.panel.example.com"},
		// JAB-389: a mail cert pinned to the old hostname's name also
		// covers mail.<new hostname>.
		{"mail.old.example.com", "panel.example.com", "mail.old.example.com mail.panel.example.com"},
		{"MX.Example.NET", "Panel.Example.com", "mx.example.net mail.panel.example.com"},
		{"", "panel.example.com", "mail.panel.example.com"},
		{"mx.example.net", "", "mx.example.net"},
		{"", "", ""},
	} {
		if got := strings.Join(MailNames(tc.mail, tc.host), " "); got != tc.want {
			t.Errorf("MailNames(%q, %q) = %q, want %q", tc.mail, tc.host, got, tc.want)
		}
	}
}
