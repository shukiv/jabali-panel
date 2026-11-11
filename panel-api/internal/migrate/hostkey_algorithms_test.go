package migrate

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestHostKeyAlgorithms_Order(t *testing.T) {
	want := []string{
		"ssh-ed25519",
		"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521",
		"rsa-sha2-512", "rsa-sha2-256",
		"ssh-rsa", "ssh-dss",
	}
	if got := HostKeyAlgorithms(); !reflect.DeepEqual(got, want) {
		t.Fatalf("HostKeyAlgorithms() = %v, want %v", got, want)
	}
	a := HostKeyAlgorithms()
	a[0] = "x"
	if HostKeyAlgorithms()[0] != "ssh-ed25519" {
		t.Fatal("HostKeyAlgorithms returns a shared slice")
	}
}

func hostKeySigners(t *testing.T) (ed, ec, rs ssh.Signer) {
	t.Helper()
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
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
	if ed, err = ssh.NewSignerFromKey(edPriv); err != nil {
		t.Fatal(err)
	}
	if ec, err = ssh.NewSignerFromKey(ecPriv); err != nil {
		t.Fatal(err)
	}
	if rs, err = ssh.NewSignerFromKey(rsPriv); err != nil {
		t.Fatal(err)
	}
	return ed, ec, rs
}

// handshakeWith runs a client handshake against an in-process server that
// holds hostKeys, and returns the client's error.
func handshakeWith(t *testing.T, hostKeys []ssh.Signer, cfg *ssh.ClientConfig) error {
	t.Helper()
	// A real socket: net.Pipe is unbuffered, and both SSH sides write their
	// version line before reading, so a pipe deadlocks.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	scfg := &ssh.ServerConfig{NoClientAuth: true}
	for _, k := range hostKeys {
		scfg.AddHostKey(k)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c2, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer c2.Close()
		conn, chans, reqs, err := ssh.NewServerConn(c2, scfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			for ch := range chans {
				_ = ch.Reject(ssh.Prohibited, "test")
			}
		}()
		_ = conn.Wait()
	}()
	c1, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c1.SetDeadline(time.Now().Add(20 * time.Second))
	conn, _, _, err := ssh.NewClientConn(c1, "source.example:22", cfg)
	if err == nil {
		conn.Close()
	}
	c1.Close()
	<-done
	return err
}

func pinnedConfig(pin string, algos []string) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:              "acct",
		HostKeyCallback:   PinningHostKeyCallback(pin),
		HostKeyAlgorithms: algos,
	}
}

// A source with both an ed25519 and an ECDSA key, pinned with its ed25519
// fingerprint (`ssh-keyscan -t ed25519`, as the wizard says): the dialers'
// config connects. x/crypto's default order shows the ECDSA key instead and
// the pin fails — the bug this fixes.
func TestPinnedDial_Ed25519PinOnASourceWithBothKeys(t *testing.T) {
	ed, ec, _ := hostKeySigners(t)
	edPin := ssh.FingerprintSHA256(ed.PublicKey())

	if err := handshakeWith(t, []ssh.Signer{ec, ed}, pinnedConfig(edPin, HostKeyAlgorithms())); err != nil {
		t.Fatalf("ed25519 pin with HostKeyAlgorithms(): %v", err)
	}

	err := handshakeWith(t, []ssh.Signer{ec, ed}, pinnedConfig(edPin, nil))
	if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("x/crypto default order: want the ECDSA key and a mismatch, got %v", err)
	}
}

// The pin still fails closed: an ECDSA pin on a source that also has an
// ed25519 key is refused, and the error names the key type the source showed.
func TestPinnedDial_MismatchFailsClosedAndNamesTheKeyType(t *testing.T) {
	ed, ec, _ := hostKeySigners(t)
	ecPin := ssh.FingerprintSHA256(ec.PublicKey())
	err := handshakeWith(t, []ssh.Signer{ec, ed}, pinnedConfig(ecPin, HostKeyAlgorithms()))
	if err == nil {
		t.Fatal("an ECDSA pin was accepted while the source showed its ed25519 key")
	}
	for _, want := range []string{"fingerprint mismatch", "ssh-ed25519", ssh.FingerprintSHA256(ed.PublicKey()), ecPin} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// A source with no ed25519 key still connects: ECDSA, then RSA.
func TestPinnedDial_FallsBackWhenTheSourceHasNoEd25519Key(t *testing.T) {
	_, ec, rs := hostKeySigners(t)
	if err := handshakeWith(t, []ssh.Signer{ec, rs}, pinnedConfig(ssh.FingerprintSHA256(ec.PublicKey()), HostKeyAlgorithms())); err != nil {
		t.Fatalf("ECDSA source: %v", err)
	}
	if err := handshakeWith(t, []ssh.Signer{rs}, pinnedConfig(ssh.FingerprintSHA256(rs.PublicKey()), HostKeyAlgorithms())); err != nil {
		t.Fatalf("RSA-only source: %v", err)
	}
}
