package commands

import (
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: a backup carries each domain's certificate and key, read from the
// domain's own certbot files, so a restore on another server can serve it.
// Only the account's own certificate: one that also covers another name (an
// administrator's wildcard, say) stays out of a backup its owner can download.

// enrichLE points sslLERoot at a temp dir for the test.
func enrichLE(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prev := sslLERoot
	sslLERoot = root
	t.Cleanup(func() { sslLERoot = prev })
	return root
}

// writeLineage lays out certbot's files for name: the live files are symlinks
// into archive/. It returns the live paths.
func writeLineage(t *testing.T, root, name, certPEM, keyPEM string) (string, string) {
	t.Helper()
	archive, live := filepath.Join(root, "archive", name), filepath.Join(root, "live", name)
	for _, dir := range []string{archive, live} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for file, body := range map[string]string{"fullchain": certPEM, "privkey": keyPEM} {
		if err := os.WriteFile(filepath.Join(archive, file+"1.pem"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "archive", name, file+"1.pem"), filepath.Join(live, file+".pem")); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(live, "fullchain.pem"), filepath.Join(live, "privkey.pem")
}

func enrichDomain(name, status, certPath, keyPath string) backup.MetadataDomain {
	return backup.MetadataDomain{Name: name, SSLCertificate: &backup.MetadataSSLCert{
		ID: "c-" + name, Status: status, CertPath: &certPath, KeyPath: &keyPath,
	}}
}

func TestEnrichSSLCertificates_CarriesTheAccountsOwnCertificates(t *testing.T) {
	root := enrichLE(t)
	aCert, aKey := genTestCertKey(t, []string{"alice.org", "www.alice.org", "mail.alice.org"}, "alice.org")
	aCP, aKP := writeLineage(t, root, "alice.org", aCert, aKey)
	// A certificate covering two of the account's domains is the account's.
	bCert, bKey := genTestCertKey(t, []string{"shop.alice.net", "alice.org"}, "shop.alice.net")
	bCP, bKP := writeLineage(t, root, "shop.alice.net", bCert, bKey)
	cCert, cKey := genTestCertKey(t, []string{"alice.net"}, "alice.net")
	cCP, cKP := writeLineage(t, root, "alice.net", cCert, cKey)
	meta := &backup.AccountMetadata{Domains: []backup.MetadataDomain{
		enrichDomain("alice.org", "issued", aCP, aKP),
		enrichDomain("shop.alice.net", "renewing", bCP, bKP),
		enrichDomain("alice.net", "custom", cCP, cKP),
	}}

	enrichSSLCertificates(meta)

	for i, want := range [][2]string{{aCert, aKey}, {bCert, bKey}, {cCert, cKey}} {
		c := meta.Domains[i].SSLCertificate
		if c.CertPEM != want[0] || c.KeyPEM != want[1] {
			t.Fatalf("%s: certificate %d bytes key %d bytes, want its own files", meta.Domains[i].Name, len(c.CertPEM), len(c.KeyPEM))
		}
	}
}

func TestEnrichSSLCertificates_LeavesOut(t *testing.T) {
	root := enrichLE(t)
	own, ownKey := genTestCertKey(t, []string{"alice.org"}, "alice.org")
	_, strayKey := genTestCertKey(t, []string{"alice.org"}, "alice.org")
	wide, wideKey := genTestCertKey(t, []string{"alice.org", "*.panel.example"}, "alice.org")
	parent, parentKey := genTestCertKey(t, []string{"*.org"}, "*.org")

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "key.pem"), []byte(ownKey), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		setup func() backup.MetadataDomain
	}{
		{"a self-signed certificate", func() backup.MetadataDomain {
			cp, kp := writeLineage(t, root, "alice.org", own, ownKey)
			return enrichDomain("alice.org", "self_signed", cp, kp)
		}},
		{"a certificate that also covers a name outside the account", func() backup.MetadataDomain {
			cp, kp := writeLineage(t, root, "alice.org", wide, wideKey)
			return enrichDomain("alice.org", "issued", cp, kp)
		}},
		{"a wildcard of the domain's parent", func() backup.MetadataDomain {
			cp, kp := writeLineage(t, root, "alice.org", parent, parentKey)
			return enrichDomain("alice.org", "issued", cp, kp)
		}},
		{"a key that isn't the certificate's", func() backup.MetadataDomain {
			cp, kp := writeLineage(t, root, "alice.org", own, strayKey)
			return enrichDomain("alice.org", "issued", cp, kp)
		}},
		{"files that aren't the domain's own", func() backup.MetadataDomain {
			cp, kp := writeLineage(t, root, "bob.org", own, ownKey)
			return enrichDomain("alice.org", "issued", cp, kp)
		}},
		{"a live file that links outside the certificate root", func() backup.MetadataDomain {
			cp, kp := writeLineage(t, root, "alice.org", own, ownKey)
			if err := os.Remove(kp); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "key.pem"), kp); err != nil {
				t.Fatal(err)
			}
			return enrichDomain("alice.org", "issued", cp, kp)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, dir := range []string{"live", "archive"} {
				if err := os.RemoveAll(filepath.Join(root, dir)); err != nil {
					t.Fatal(err)
				}
			}
			meta := &backup.AccountMetadata{Domains: []backup.MetadataDomain{tc.setup()}}

			enrichSSLCertificates(meta)

			if c := meta.Domains[0].SSLCertificate; c.CertPEM != "" || c.KeyPEM != "" {
				t.Fatalf("certificate %d bytes key %d bytes carried, want neither", len(c.CertPEM), len(c.KeyPEM))
			}
		})
	}
}

// A domain name with a path in it never names certificate files, even ones
// whose path it cleans to.
func TestEnrichSSLCertificates_DomainNameWithAPath(t *testing.T) {
	root := enrichLE(t)
	own, ownKey := genTestCertKey(t, []string{"alice.org"}, "alice.org")
	cp, kp := writeLineage(t, root, "alice.org", own, ownKey)
	meta := &backup.AccountMetadata{Domains: []backup.MetadataDomain{
		enrichDomain("alice.org", "issued", cp, kp),
		enrichDomain("alice.org/.", "issued", cp, kp),
	}}

	enrichSSLCertificates(meta)

	if meta.Domains[0].SSLCertificate.CertPEM != own {
		t.Fatal("alice.org's own certificate was not carried")
	}
	if c := meta.Domains[1].SSLCertificate; c.CertPEM != "" || c.KeyPEM != "" {
		t.Fatalf("%q carried a certificate", meta.Domains[1].Name)
	}
}
