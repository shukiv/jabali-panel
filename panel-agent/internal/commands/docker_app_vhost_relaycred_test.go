package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// A deleted docker-app domain's relay credential goes with its vhost, as
// domain.delete removes it for any other domain. The sendmail loop writes
// `<user>/<domain>.cred` for every domain a tenant owns, a docker-app domain
// included; before, the docker-app delete removed the vhost and certificates
// and left the credential file in the tenant's sendmail directory.

// credFixture points the sendmail credential root at a temp dir and writes a
// credential for each name under one user, with default.cred pointing at the
// first.
func credFixture(t *testing.T, user string, names ...string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("JABALI_SENDMAIL_CRED_ROOT", root)
	dir := filepath.Join(root, user)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n+".cred"), []byte("email=noreply@"+n+"\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if len(names) > 0 {
		if err := os.Symlink(names[0]+".cred", filepath.Join(dir, "default.cred")); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func credExists(t *testing.T, dir, name string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, name+".cred"))
	return err == nil
}

func TestDockerAppVhostRemove_PurgeRemovesTheRelayCredential(t *testing.T) {
	withDockerAppVhostTestDirs(t)
	const dom, keep = "app.example.com", "example.com"
	certFixture(t, dom)
	dir := credFixture(t, "tenant1", dom, keep)
	applyDockerVhost(t, dom)

	if err := removeDockerVhost(t, dom, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if credExists(t, dir, dom) {
		t.Fatalf("%s.cred left behind after the domain's purge", dom)
	}
	if !credExists(t, dir, keep) {
		t.Fatalf("%s.cred, another domain's credential, was removed", keep)
	}
	// default.cred pointed at the removed file: it moves to the one left.
	if target, err := os.Readlink(filepath.Join(dir, "default.cred")); err != nil || target != keep+".cred" {
		t.Fatalf("default.cred -> %q (%v), want %s.cred", target, err, keep)
	}
}

// A disabled domain (the reconciler's call, no purge) keeps its credential.
func TestDockerAppVhostRemove_WithoutPurgeKeepsTheRelayCredential(t *testing.T) {
	withDockerAppVhostTestDirs(t)
	const dom = "app.example.com"
	dir := credFixture(t, "tenant1", dom)
	applyDockerVhost(t, dom)

	if err := removeDockerVhost(t, dom, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !credExists(t, dir, dom) {
		t.Fatal("a remove without purge took the domain's relay credential")
	}
}

// A web vhost of the same name still sends with the credential.
func TestDockerAppVhostRemove_PurgeKeepsTheRelayCredentialAWebVhostUses(t *testing.T) {
	_, en := withDockerAppVhostTestDirs(t)
	const dom = "shared.example.com"
	certFixture(t, dom)
	dir := credFixture(t, "tenant1", dom)
	applyDockerVhost(t, dom)
	if err := os.WriteFile(filepath.Join(en, dom+".conf"), []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := removeDockerVhost(t, dom, true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !credExists(t, dir, dom) {
		t.Fatalf("%s.cred removed while the web vhost %s.conf is enabled", dom, dom)
	}
}
