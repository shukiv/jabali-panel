package commands

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func pinTestKeys(t *testing.T) (ed, ec, rs ssh.PublicKey) {
	t.Helper()
	edPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct {
		dst *ssh.PublicKey
		raw any
	}{{&ed, edPub}, {&ec, &ecPriv.PublicKey}, {&rs, &rsPriv.PublicKey}} {
		pk, perr := ssh.NewPublicKey(k.raw)
		if perr != nil {
			t.Fatal(perr)
		}
		*k.dst = pk
	}
	return ed, ec, rs
}

// stubKeyscan makes migrationKeyscan return known_hosts lines for keys, and
// counts the calls.
func stubKeyscan(t *testing.T, keys ...ssh.PublicKey) *int {
	t.Helper()
	calls := 0
	old := migrationKeyscan
	migrationKeyscan = func(_ context.Context, host string, port int) ([]byte, error) {
		calls++
		var b strings.Builder
		for _, k := range keys {
			// ssh-keyscan's own host field is not trusted; write a bare one.
			b.WriteString(knownhosts.Line([]string{host}, k) + "\n")
		}
		return []byte(b.String()), nil
	}
	t.Cleanup(func() { migrationKeyscan = old })
	return &calls
}

func pinTestSecret(t *testing.T) (secret, knownHosts string) {
	t.Helper()
	dir := t.TempDir()
	secret = filepath.Join(dir, testMailShadowJob+".env")
	if err := os.WriteFile(secret, []byte("SSH_PASSWORD=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return secret, filepath.Join(dir, testMailShadowJob+".known_hosts")
}

// With a pinned ECDSA key on a source that also has ed25519: known_hosts
// holds only the pinned key, under the port's host spec, and ssh runs with
// StrictHostKeyChecking=yes for that key type.
func TestMigrationSSHTransport_PinWritesOnlyThePinnedKey(t *testing.T) {
	ed, ec, _ := pinTestKeys(t)
	stubKeyscan(t, ed, ec)
	secret, kh := pinTestSecret(t)

	tr, aerr := loadMigrationSSHTransport(context.Background(), secret, "198.51.100.7", 2222, ssh.FingerprintSHA256(ec))
	if aerr != nil {
		t.Fatal(aerr)
	}
	defer tr.Close()
	for _, want := range []string{"-o StrictHostKeyChecking=yes", "-o HostKeyAlgorithms=ecdsa-sha2-nistp256", "-o UserKnownHostsFile=" + kh, "-p 2222"} {
		if !strings.Contains(tr.SSHOpt, want) {
			t.Errorf("SSHOpt lacks %q: %s", want, tr.SSHOpt)
		}
	}
	if strings.Contains(tr.SSHOpt, "accept-new") {
		t.Errorf("pinned copy still uses accept-new: %s", tr.SSHOpt)
	}
	body, err := os.ReadFile(kh)
	if err != nil {
		t.Fatal(err)
	}
	want := knownhosts.Line([]string{"[198.51.100.7]:2222"}, ec) + "\n"
	if string(body) != want {
		t.Fatalf("known_hosts =\n%s\nwant\n%s", body, want)
	}
	if fi, _ := os.Stat(kh); fi.Mode().Perm() != 0o600 {
		t.Errorf("known_hosts mode %v", fi.Mode().Perm())
	}
}

// An RSA pin asks for the SHA-2 RSA signature algorithms.
func TestMigrationSSHTransport_RSAPinAlgorithms(t *testing.T) {
	_, _, rs := pinTestKeys(t)
	stubKeyscan(t, rs)
	secret, _ := pinTestSecret(t)
	tr, aerr := loadMigrationSSHTransport(context.Background(), secret, "198.51.100.7", 22, ssh.FingerprintSHA256(rs))
	if aerr != nil {
		t.Fatal(aerr)
	}
	defer tr.Close()
	if !strings.Contains(tr.SSHOpt, "-o HostKeyAlgorithms=rsa-sha2-512,rsa-sha2-256") {
		t.Errorf("SSHOpt = %s", tr.SSHOpt)
	}
}

// A pin that matches none of the source's keys fails the copy, names each key
// the source presented, and leaves an existing known_hosts as it was.
func TestMigrationSSHTransport_NoMatchingKeyFails(t *testing.T) {
	ed, ec, _ := pinTestKeys(t)
	_, _, other := pinTestKeys(t)
	stubKeyscan(t, ed, ec)
	secret, kh := pinTestSecret(t)
	if err := os.WriteFile(kh, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, aerr := loadMigrationSSHTransport(context.Background(), secret, "198.51.100.7", 22, ssh.FingerprintSHA256(other))
	if aerr == nil || aerr.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("want a FailedPrecondition, got %v", aerr)
	}
	for _, want := range []string{"ssh-ed25519 " + ssh.FingerprintSHA256(ed), "ecdsa-sha2-nistp256 " + ssh.FingerprintSHA256(ec), ssh.FingerprintSHA256(other)} {
		if !strings.Contains(aerr.Message, want) {
			t.Errorf("error lacks %q: %s", want, aerr.Message)
		}
	}
	if body, _ := os.ReadFile(kh); string(body) != "earlier\n" {
		t.Errorf("known_hosts changed on a failed pin: %q", body)
	}
}

// No pin: accept-new on the per-job file, no keyscan — as before.
func TestMigrationSSHTransport_NoPinUnchanged(t *testing.T) {
	calls := stubKeyscan(t)
	secret, kh := pinTestSecret(t)
	tr, aerr := loadMigrationSSHTransport(context.Background(), secret, "198.51.100.7", 22, "")
	if aerr != nil {
		t.Fatal(aerr)
	}
	defer tr.Close()
	if want := "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + kh + " -p 22"; tr.SSHOpt != want {
		t.Errorf("SSHOpt = %q, want %q", tr.SSHOpt, want)
	}
	if *calls != 0 {
		t.Errorf("keyscan ran %d time(s) without a pin", *calls)
	}
}

// A malformed pin, an option-like host or an empty keyscan are refused
// before anything is written.
func TestMigrationSSHTransport_PinInputsRefused(t *testing.T) {
	_, ec, _ := pinTestKeys(t)
	secret, kh := pinTestSecret(t)
	good := ssh.FingerprintSHA256(ec)

	calls := stubKeyscan(t, ec)
	if _, aerr := loadMigrationSSHTransport(context.Background(), secret, "198.51.100.7", 22, "SHA256:short"); aerr == nil || aerr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("malformed pin: %v", aerr)
	}
	if _, aerr := loadMigrationSSHTransport(context.Background(), secret, "-oProxyCommand=x", 22, good); aerr == nil || aerr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("option-like host: %v", aerr)
	}
	if *calls != 0 {
		t.Errorf("keyscan ran %d time(s) for refused input", *calls)
	}

	stubKeyscan(t) // the source answers with no keys
	if _, aerr := loadMigrationSSHTransport(context.Background(), secret, "198.51.100.7", 22, good); aerr == nil || !strings.Contains(aerr.Message, "no host keys") {
		t.Errorf("empty keyscan: %v", aerr)
	}
	if _, err := os.Stat(kh); !os.IsNotExist(err) {
		t.Errorf("known_hosts written for refused input: %v", err)
	}
}
