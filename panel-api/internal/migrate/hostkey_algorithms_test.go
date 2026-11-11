package migrate

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
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

// One handshake fails closed: the callback refuses a key that does not match
// the pin and names the key type the source showed. (SSHHandshake then tries
// the source's other key types; see the tests below.)
func TestPinnedDial_OneHandshakeFailsClosedAndNamesTheKeyType(t *testing.T) {
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

type sshTestSource struct {
	addr      string
	accepts   atomic.Int32
	passwords atomic.Int32
}

// startSSHTestSource serves SSH on 127.0.0.1 with hostKeys, accepting
// password "pw" for any user, and counts connections and password attempts.
func startSSHTestSource(t *testing.T, hostKeys ...ssh.Signer) *sshTestSource {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	src := &sshTestSource{addr: ln.Addr().String()}
	scfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			src.passwords.Add(1)
			if string(pw) == "pw" {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
	}
	for _, k := range hostKeys {
		scfg.AddHostKey(k)
	}
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			src.accepts.Add(1)
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				conn, chans, reqs, err := ssh.NewServerConn(c, scfg)
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
		}
	}()
	return src
}

func (s *sshTestSource) handshake(t *testing.T, pin string) error {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:              "acct",
		Auth:              []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback:   PinningHostKeyCallback(pin),
		HostKeyAlgorithms: HostKeyAlgorithms(),
		Timeout:           10 * time.Second,
	}
	dial := func() (net.Conn, error) { return net.DialTimeout("tcp", s.addr, 5*time.Second) }
	conn, _, _, err := SSHHandshake(dial, "source.example:22", cfg, pin)
	if err == nil {
		conn.Close()
	}
	return err
}

// Pins recorded before ed25519 came first are ECDSA: such a pin on a source
// that has both keys must still connect (one refused ed25519 handshake, then
// the ECDSA one), and the password is sent only on the accepted handshake.
func TestSSHHandshake_ECDSAPinOnASourceWithBothKeysConnects(t *testing.T) {
	ed, ec, _ := hostKeySigners(t)
	src := startSSHTestSource(t, ec, ed)
	if err := src.handshake(t, ssh.FingerprintSHA256(ec.PublicKey())); err != nil {
		t.Fatalf("ECDSA pin: %v", err)
	}
	if got := src.accepts.Load(); got != 2 {
		t.Errorf("connections = %d, want 2 (ed25519 refused, ECDSA accepted)", got)
	}
	if got := src.passwords.Load(); got != 1 {
		t.Errorf("password attempts = %d, want 1 (only after the pin matched)", got)
	}
}

// An ed25519 pin connects on the first handshake.
func TestSSHHandshake_Ed25519PinConnectsFirstTry(t *testing.T) {
	ed, ec, _ := hostKeySigners(t)
	src := startSSHTestSource(t, ec, ed)
	if err := src.handshake(t, ssh.FingerprintSHA256(ed.PublicKey())); err != nil {
		t.Fatalf("ed25519 pin: %v", err)
	}
	if got := src.accepts.Load(); got != 1 {
		t.Errorf("connections = %d, want 1", got)
	}
}

// A pin that matches none of the source's keys is refused; the error names
// every key the source presented and the pin; no password is ever sent.
func TestSSHHandshake_WrongPinRefusedListsEveryKeyAndSendsNoPassword(t *testing.T) {
	ed, ec, rs := hostKeySigners(t)
	other, _, _ := hostKeySigners(t)
	src := startSSHTestSource(t, ec, ed, rs)
	pin := ssh.FingerprintSHA256(other.PublicKey())
	err := src.handshake(t, pin)
	if err == nil {
		t.Fatal("a pin matching none of the source's keys was accepted")
	}
	for _, want := range []string{
		"fingerprint mismatch", pin,
		"ssh-ed25519 " + ssh.FingerprintSHA256(ed.PublicKey()),
		"ecdsa-sha2-nistp256 " + ssh.FingerprintSHA256(ec.PublicKey()),
		"ssh-rsa " + ssh.FingerprintSHA256(rs.PublicKey()),
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if got := src.passwords.Load(); got != 0 {
		t.Errorf("password sent %d time(s) to a source whose key never matched", got)
	}
}

// No pin: one handshake, as before (trust on first use).
func TestSSHHandshake_NoPinOneHandshake(t *testing.T) {
	_, ec, rs := hostKeySigners(t)
	src := startSSHTestSource(t, ec, rs)
	if err := src.handshake(t, ""); err != nil {
		t.Fatal(err)
	}
	if got := src.accepts.Load(); got != 1 {
		t.Errorf("connections = %d, want 1", got)
	}
}

// A dial failure comes back as a DialError, so callers keep their wording.
func TestSSHHandshake_DialFailureIsADialError(t *testing.T) {
	boom := errors.New("connection refused")
	_, _, _, err := SSHHandshake(func() (net.Conn, error) { return nil, boom }, "source.example:22",
		&ssh.ClientConfig{HostKeyCallback: PinningHostKeyCallback("")}, "SHA256:x")
	var de *DialError
	if !errors.As(err, &de) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want a DialError wrapping the dial error", err)
	}
}
