package backupmetadata

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: an account backup carries each domain's certificate and key. A
// restore whose certificate isn't on this server installs them when it may
// (RestoreCertificates) and they hold up: they pair, cover the domain, are
// valid for a day yet, and, from an uploaded file, chain to a CA this server
// trusts. Otherwise the domain gets a new certificate, as before.

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (ca testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// leaf issues a certificate for names, valid from notBefore to notAfter, and
// returns its PEM and its key's.
func (ca testCA) leaf(t *testing.T, names []string, notBefore, notAfter time.Time) (string, string) {
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

// trustCA makes restoredCertRoots ca's alone for the test.
func trustCA(t *testing.T, ca testCA) {
	t.Helper()
	prev := restoredCertRoots
	restoredCertRoots = ca.pool()
	t.Cleanup(func() { restoredCertRoots = prev })
}

func TestCheckRestoredCert(t *testing.T) {
	ca, other := newTestCA(t), newTestCA(t)
	trustCA(t, ca)
	now := time.Now()
	good, goodKey := ca.leaf(t, []string{"alice.org", "www.alice.org"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	_, strayKey := ca.leaf(t, []string{"alice.org"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	own, ownKey := other.leaf(t, []string{"alice.org"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	bob, bobKey := ca.leaf(t, []string{"bob.org"}, now.Add(-time.Hour), now.Add(60*24*time.Hour))
	expired, expiredKey := ca.leaf(t, []string{"alice.org"}, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	ending, endingKey := ca.leaf(t, []string{"alice.org"}, now.Add(-90*24*time.Hour), now.Add(12*time.Hour))
	early, earlyKey := ca.leaf(t, []string{"alice.org"}, now.Add(24*time.Hour), now.Add(60*24*time.Hour))

	for _, tc := range []struct {
		name, cert, key string
		verifyChain     bool
		wantErr         string
	}{
		{"a CA's certificate from an uploaded file", good, goodKey, true, ""},
		{"a CA's certificate with its chain", good + ca.pem, goodKey, true, ""},
		{"an own CA's certificate from this server's backup", own, ownKey, false, ""},
		{"an own CA's certificate from an uploaded file", own, ownKey, true, "isn't signed by a certificate authority this server trusts"},
		{"a key that isn't the certificate's", good, strayKey, false, "don't pair"},
		{"another domain's certificate", bob, bobKey, false, "doesn't cover alice.org"},
		{"an expired certificate", expired, expiredKey, false, "it expires on"},
		{"a certificate with less than a day left", ending, endingKey, false, "it expires on"},
		{"a certificate not valid yet", early, earlyKey, false, "it isn't valid until"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkRestoredCert(tc.cert, tc.key, "alice.org", tc.verifyChain, now)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v, want none", err)
			case tc.wantErr != "" && (err == nil || !hasError([]string{err.Error()}, tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// srAgent answers ssl.install_custom with the paths the agent writes.
type srAgent struct {
	calls   []map[string]any
	fail    error
	reply   string
	unknown bool
}

func (a *srAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd != "ssl.install_custom" {
		a.unknown = true
		return json.RawMessage(`{}`), nil
	}
	p := params.(map[string]any)
	a.calls = append(a.calls, p)
	if a.fail != nil {
		return nil, a.fail
	}
	if a.reply != "" {
		return json.RawMessage(a.reply), nil
	}
	dom := p["domain"].(string)
	return json.Marshal(map[string]string{
		"cert_path": "/etc/letsencrypt/live/" + dom + "/fullchain.pem",
		"key_path":  "/etc/letsencrypt/live/" + dom + "/privkey.pem",
	})
}

type srDomains struct {
	*ppDomains
	modes map[string]string
}

func (r *srDomains) UpdateSSLMode(_ context.Context, id, mode string) error {
	r.modes[id] = mode
	return nil
}

type srCase struct {
	mode, status, ownership string
	certPEM, keyPEM         string
	keep, untrusted         bool
	agent                   *srAgent
}

type srResult struct {
	cert  models.SSLCertificate
	dom   models.Domain
	modes map[string]string
	r     ApplyResult
}

// srRestore restores alice.org, whose certificate isn't on this server, with
// the backup's certificate and key.
func srRestore(t *testing.T, c srCase) srResult {
	t.Helper()
	stubCertFiles(t, false)
	pools := &ppPools{}
	certs := &spCerts{}
	doms := &srDomains{ppDomains: &ppDomains{pools: pools}, modes: map[string]string{}}
	meta := ppMeta()
	meta.PHPPools = nil
	dm := &meta.Domains[0]
	dm.PHPPoolID, dm.Mailboxes = nil, nil
	dm.SSLMode, dm.SkipAutoSAN, dm.OwnershipStatus = c.mode, true, c.ownership
	status := c.status
	if status == "" {
		status = models.SSLStatusIssued
	}
	cp, kp := "/etc/letsencrypt/live/alice.org/fullchain.pem", "/etc/letsencrypt/live/alice.org/privkey.pem"
	dm.SSLCertificate = &internalbackup.MetadataSSLCert{
		ID: "c1", Status: status, CertPath: &cp, KeyPath: &kp,
		CertPEM: c.certPEM, KeyPEM: c.keyPEM,
	}
	deps := ppDeps(pools, doms.ppDomains, &dcMailboxes{})
	deps.Domains = doms
	deps.SSLCerts = certs
	deps.RestoreCertificates = c.keep
	deps.Untrusted = c.untrusted
	if c.agent != nil {
		deps.Agent = c.agent
	}

	r := Apply(context.Background(), meta, deps)

	if len(doms.created) != 1 || len(certs.created) != 1 {
		t.Fatalf("domains %d certificates %d created (errors %v), want 1 each", len(doms.created), len(certs.created), r.Errors)
	}
	if c.agent != nil && c.agent.unknown {
		t.Fatalf("the restore called an agent command other than ssl.install_custom")
	}
	return srResult{cert: certs.created[0], dom: doms.created[0], modes: doms.modes, r: r}
}

func wantReissued(t *testing.T, got srResult, msg string) {
	t.Helper()
	if got.cert.Status != models.SSLStatusPending || got.cert.CertPath != nil || got.cert.KeyPath != nil {
		t.Fatalf("certificate = %+v, want pending without files so a new one is issued", got.cert)
	}
	if !hasError(got.r.Errors, "ssl_cert c1 (alice.org): "+msg) || !hasError(got.r.Errors, "; a new one will be issued") {
		t.Fatalf("errors %v, want %q and that a new one will be issued", got.r.Errors, msg)
	}
}

func TestApply_InstallsTheBackupsCertificate(t *testing.T) {
	ca := newTestCA(t)
	trustCA(t, ca)
	notAfter := time.Now().Add(60 * 24 * time.Hour).Truncate(time.Second)
	cert, key := ca.leaf(t, []string{"alice.org"}, time.Now().Add(-time.Hour), notAfter)

	for _, untrusted := range []bool{false, true} {
		agent := &srAgent{}
		got := srRestore(t, srCase{mode: models.SSLModeLE, certPEM: cert, keyPEM: key, keep: true, untrusted: untrusted, agent: agent})

		if len(agent.calls) != 1 || agent.calls[0]["domain"] != "alice.org" || agent.calls[0]["cert_pem"] != cert || agent.calls[0]["key_pem"] != key {
			t.Fatalf("untrusted=%v: agent calls %v, want one ssl.install_custom with the backup's certificate", untrusted, agent.calls)
		}
		c := got.cert
		if c.Status != models.SSLStatusIssued || c.IssueMethod != models.SSLIssueMethodRestored {
			t.Fatalf("untrusted=%v: status %q issue method %q, want issued and restored", untrusted, c.Status, c.IssueMethod)
		}
		if c.CertPath == nil || *c.CertPath != "/etc/letsencrypt/live/alice.org/fullchain.pem" || c.KeyPath == nil || *c.KeyPath != "/etc/letsencrypt/live/alice.org/privkey.pem" {
			t.Fatalf("untrusted=%v: paths %v %v, want the installed files", untrusted, c.CertPath, c.KeyPath)
		}
		if c.ExpiresAt == nil || !c.ExpiresAt.Equal(notAfter.UTC()) {
			t.Fatalf("untrusted=%v: expires %v, want the certificate's %v", untrusted, c.ExpiresAt, notAfter)
		}
		// A report line marks the restore as partial: an installed
		// certificate has none.
		if hasError(got.r.Errors, "ssl_cert c1") {
			t.Fatalf("untrusted=%v: errors %v, want no line for an installed certificate", untrusted, got.r.Errors)
		}
		if got.dom.SSLMode != models.SSLModeLE || !got.dom.SkipAutoSAN || len(got.modes) != 0 || got.r.SSLCerts != 1 {
			t.Fatalf("untrusted=%v: domain mode %q skip-auto-san %v, mode writes %v, certificates %d", untrusted, got.dom.SSLMode, got.dom.SkipAutoSAN, got.modes, got.r.SSLCerts)
		}
	}
}

func TestApply_BackupsCertificateLeftOutUnlessChosen(t *testing.T) {
	ca := newTestCA(t)
	trustCA(t, ca)
	cert, key := ca.leaf(t, []string{"alice.org"}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	agent := &srAgent{}

	got := srRestore(t, srCase{mode: models.SSLModeLE, certPEM: cert, keyPEM: key, untrusted: true, agent: agent})

	if len(agent.calls) != 0 {
		t.Fatalf("agent calls %v, want none", agent.calls)
	}
	wantReissued(t, got, `the backup's certificate was left out ("Keep the backup's SSL certificates" was not chosen)`)
}

// An uploaded file is anyone's: its certificate must come from a CA this
// server trusts, not from a CA the file's author made.
func TestApply_UploadedCertificateFromAnUnknownCAIsNotUsed(t *testing.T) {
	trustCA(t, newTestCA(t))
	cert, key := newTestCA(t).leaf(t, []string{"alice.org"}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	agent := &srAgent{}

	got := srRestore(t, srCase{mode: models.SSLModeLE, certPEM: cert, keyPEM: key, keep: true, untrusted: true, agent: agent})

	if len(agent.calls) != 0 {
		t.Fatalf("agent calls %v, want none", agent.calls)
	}
	wantReissued(t, got, "the backup's certificate is not used: it isn't signed by a certificate authority this server trusts")
}

func TestApply_BackupsCertificateThatFailsTheChecksIsNotUsed(t *testing.T) {
	ca := newTestCA(t)
	trustCA(t, ca)
	cert, key := ca.leaf(t, []string{"bob.org"}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	agent := &srAgent{}

	got := srRestore(t, srCase{mode: models.SSLModeLE, certPEM: cert, keyPEM: key, keep: true, agent: agent})

	if len(agent.calls) != 0 {
		t.Fatalf("agent calls %v, want none", agent.calls)
	}
	wantReissued(t, got, "the backup's certificate is not used: it doesn't cover alice.org")
}

// The reconciler serves a placeholder on a domain whose ownership isn't
// proven, over a CA's certificate; installing one there serves nothing.
func TestApply_BackupsCertificateWaitsForOwnership(t *testing.T) {
	ca := newTestCA(t)
	trustCA(t, ca)
	cert, key := ca.leaf(t, []string{"alice.org"}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	agent := &srAgent{}

	got := srRestore(t, srCase{mode: models.SSLModeLE, ownership: models.OwnershipPending, certPEM: cert, keyPEM: key, keep: true, agent: agent})

	if len(agent.calls) != 0 {
		t.Fatalf("agent calls %v, want none", agent.calls)
	}
	wantReissued(t, got, "the backup's certificate is not used: the domain's ownership isn't verified")
}

func TestApply_BackupWithoutTheCertificate(t *testing.T) {
	agent := &srAgent{}

	got := srRestore(t, srCase{mode: models.SSLModeLE, keep: true, agent: agent})

	if len(agent.calls) != 0 {
		t.Fatalf("agent calls %v, want none", agent.calls)
	}
	wantReissued(t, got, "its certificate isn't on this server, and the backup doesn't carry it")
}

func TestApply_BackupsCertificateInstallFailure(t *testing.T) {
	ca := newTestCA(t)
	trustCA(t, ca)
	cert, key := ca.leaf(t, []string{"alice.org"}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))

	got := srRestore(t, srCase{mode: models.SSLModeLE, certPEM: cert, keyPEM: key, keep: true,
		agent: &srAgent{fail: errors.New("nginx -t failed\nnginx: [emerg] secret detail")}})
	wantReissued(t, got, "installing the backup's certificate failed (nginx -t failed)")
	if hasError(got.r.Errors, "secret detail") {
		t.Fatalf("errors %v carry the agent output past its first line", got.r.Errors)
	}

	got = srRestore(t, srCase{mode: models.SSLModeLE, certPEM: cert, keyPEM: key, keep: true,
		agent: &srAgent{reply: `{"cert_path":"/etc/passwd","key_path":"/etc/shadow"}`}})
	wantReissued(t, got, "installing the backup's certificate failed (the agent named no certificate files of this domain)")
}

// A custom-certificate domain keeps its mode and certificate, its owner's CA
// and pending ownership included; one whose certificate can't come back gets
// Let's Encrypt, because nothing issues a certificate in custom mode.
func TestApply_CustomCertificateDomain(t *testing.T) {
	trustCA(t, newTestCA(t))
	cert, key := newTestCA(t).leaf(t, []string{"alice.org"}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))

	agent := &srAgent{}
	got := srRestore(t, srCase{mode: models.SSLModeCustom, status: models.SSLStatusCustom, ownership: models.OwnershipPending,
		certPEM: cert, keyPEM: key, keep: true, agent: agent})
	if len(agent.calls) != 1 || got.cert.Status != models.SSLStatusCustom || got.cert.IssueMethod != "" || hasError(got.r.Errors, "ssl_cert c1") {
		t.Fatalf("calls %d certificate %+v (errors %v), want the owner's certificate installed as custom", len(agent.calls), got.cert, got.r.Errors)
	}
	if got.dom.SSLMode != models.SSLModeCustom || len(got.modes) != 0 {
		t.Fatalf("domain mode %q mode writes %v, want custom kept", got.dom.SSLMode, got.modes)
	}

	got = srRestore(t, srCase{mode: models.SSLModeCustom, status: models.SSLStatusCustom, certPEM: cert, keyPEM: key, agent: &srAgent{}})
	wantReissued(t, got, "the backup's certificate was left out")
	if got.modes["d1"] != models.SSLModeLE || !hasError(got.r.Errors, "domain d1 (alice.org): its custom certificate was not restored; it gets a Let's Encrypt certificate") {
		t.Fatalf("mode writes %v errors %v, want the domain switched to Let's Encrypt and said so", got.modes, got.r.Errors)
	}
}

func TestApply_RestoredCertificateMode(t *testing.T) {
	for _, tc := range []struct{ mode, want, msg string }{
		{models.SSLModeSelf, models.SSLModeSelf, ""},
		{models.SSLModeNone, models.SSLModeNone, ""},
		{"", "", ""},
		{models.SSLModeShared, models.SSLModeLE, "it used a shared certificate, which isn't part of the backup; it gets a Let's Encrypt certificate of its own"},
		{"letsencrypt", "", `certificate mode "letsencrypt" is not one the panel knows; it gets a Let's Encrypt certificate`},
	} {
		got := srRestore(t, srCase{mode: tc.mode, agent: &srAgent{}})
		if got.dom.SSLMode != tc.want {
			t.Fatalf("mode %q restored as %q, want %q", tc.mode, got.dom.SSLMode, tc.want)
		}
		if tc.msg != "" && !hasError(got.r.Errors, "domain d1 (alice.org): "+tc.msg) {
			t.Fatalf("mode %q: errors %v, want %q", tc.mode, got.r.Errors, tc.msg)
		}
	}
}

// The backup records each domain's certificate mode and its opt-out of the
// automatic names, which a restore needs to treat its certificate as the old
// server did.
func TestBuild_CarriesCertificateMode(t *testing.T) {
	var c counts
	deps := Deps{
		Domains: &fDomains{rows: []models.Domain{
			{ID: "d0", Name: "d0.com", SSLMode: models.SSLModeCustom, SkipAutoSAN: true},
			{ID: "d1", Name: "d1.com", SSLMode: models.SSLModeLE},
		}},
		SSLCerts: &fCerts{t: t, c: &c, rows: []models.SSLCertificate{{ID: "c0", DomainID: "d0", Status: models.SSLStatusCustom}}},
	}

	m := Build(context.Background(), &models.User{ID: "u1"}, deps)

	if len(m.Domains) != 2 {
		t.Fatalf("want 2 domains, got %d", len(m.Domains))
	}
	if d := m.Domains[0]; d.SSLMode != models.SSLModeCustom || !d.SkipAutoSAN {
		t.Fatalf("d0 mode %q skip-auto-san %v, want custom and true", d.SSLMode, d.SkipAutoSAN)
	}
	if d := m.Domains[1]; d.SSLMode != models.SSLModeLE || d.SkipAutoSAN {
		t.Fatalf("d1 mode %q skip-auto-san %v, want le and false", d.SSLMode, d.SkipAutoSAN)
	}
}
