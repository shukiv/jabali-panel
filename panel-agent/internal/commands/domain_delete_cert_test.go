package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveDomainCertArtifacts covers the GH #1579 teardown SSL cleanup: the
// self-signed cert directory for the exact name is removed, and an absent name
// is a harmless no-op. (The LE lineage arm is cleanupCertbotLineage, which
// no-ops without a renewal conf and is covered by its own tests.)
func TestRemoveDomainCertArtifacts(t *testing.T) {
	tmp := t.TempDir()
	orig := baseSelfSignDir
	baseSelfSignDir = tmp
	defer func() { baseSelfSignDir = orig }()

	certDir := filepath.Join(tmp, "old.example.com")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	removeDomainCertArtifacts(context.Background(), "old.example.com")

	if _, err := os.Stat(certDir); !os.IsNotExist(err) {
		t.Fatalf("self-signed cert dir should be removed, stat err = %v", err)
	}

	// A name with no artifacts must not panic or error.
	removeDomainCertArtifacts(context.Background(), "never-existed.example.com")

	// A sibling domain's cert dir must survive (name-scoped removal).
	sib := filepath.Join(tmp, "keep.example.com")
	if err := os.MkdirAll(sib, 0o755); err != nil {
		t.Fatal(err)
	}
	removeDomainCertArtifacts(context.Background(), "old.example.com")
	if _, err := os.Stat(sib); err != nil {
		t.Fatalf("sibling cert dir must survive, stat err = %v", err)
	}
}

// TestRemoveDomainCertArtifacts_ReapsMailLineage covers the GH #1579 mail-cert
// slice: a teardown must also reap the per-domain mail lineage (mail.<domain>),
// not just the web lineage, or it leaves an auto-renewing renewal conf pointing
// at a name that no longer resolves (the #738 scar). Uses the renewal-conf floor
// (cleanupCertbotLineage removes the conf directly when certbot can't complete),
// with sslLERoot pointed at a temp dir. A sibling name's mail conf must survive.
func TestRemoveDomainCertArtifacts_ReapsMailLineage(t *testing.T) {
	tmpLE := t.TempDir()
	origLE := sslLERoot
	sslLERoot = tmpLE
	defer func() { sslLERoot = origLE }()
	// Isolate the self-signed arm to its own temp so it can't interfere.
	origSS := baseSelfSignDir
	baseSelfSignDir = t.TempDir()
	defer func() { baseSelfSignDir = origSS }()

	renewalDir := filepath.Join(tmpLE, "renewal")
	if err := os.MkdirAll(renewalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConf := func(name string) string {
		p := filepath.Join(renewalDir, name+".conf")
		if err := os.WriteFile(p, []byte("# renewal conf"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	webConf := writeConf("old.example.com")
	mailConf := writeConf("mail.old.example.com")
	siblingMailConf := writeConf("mail.keep.example.com")

	removeDomainCertArtifacts(context.Background(), "old.example.com")

	if _, err := os.Stat(webConf); !os.IsNotExist(err) {
		t.Fatalf("web renewal conf should be reaped, stat err = %v", err)
	}
	if _, err := os.Stat(mailConf); !os.IsNotExist(err) {
		t.Fatalf("mail renewal conf should be reaped, stat err = %v", err)
	}
	if _, err := os.Stat(siblingMailConf); err != nil {
		t.Fatalf("a sibling domain's mail lineage must survive (name-scoped), stat err = %v", err)
	}
}

// GH #1993: a certificate and key an owner uploaded or a restore installed
// (ssl.install_custom writes them as plain files, with no renewal conf) are
// removed with the domain. certbot delete never reaches them, so the private
// key stayed on the server, and a later restore of the name found the stale
// files "on this server".
func TestRemoveDomainCertArtifacts_RemovesUntrackedCertificateFiles(t *testing.T) {
	tmpLE := t.TempDir()
	origLE := sslLERoot
	sslLERoot = tmpLE
	defer func() { sslLERoot = origLE }()
	origSS := baseSelfSignDir
	baseSelfSignDir = t.TempDir()
	defer func() { baseSelfSignDir = origSS }()

	write := func(rel string) string {
		p := filepath.Join(tmpLE, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	gone := []string{
		write("live/old.example.com/fullchain.pem"),
		write("live/old.example.com/privkey.pem"),
		write("archive/old.example.com/privkey1.pem"),
		write("live/mail.old.example.com/privkey.pem"),
	}
	kept := write("live/keep.example.com/privkey.pem")

	removeDomainCertArtifacts(context.Background(), "old.example.com")

	for _, p := range gone {
		if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
			t.Fatalf("%s should be removed with the domain, stat err = %v", filepath.Dir(p), err)
		}
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("another domain's certificate must survive, stat err = %v", err)
	}
}

// A lineage certbot still tracks is certbot's to remove.
func TestRemoveUntrackedLineage_LeavesATrackedLineage(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"renewal/old.example.com.conf", "live/old.example.com/privkey.pem"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removeUntrackedLineage(root, "old.example.com")

	if _, err := os.Stat(filepath.Join(root, "live/old.example.com/privkey.pem")); err != nil {
		t.Fatalf("a tracked lineage's files must stay, stat err = %v", err)
	}
}
