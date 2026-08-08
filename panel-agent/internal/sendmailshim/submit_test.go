package sendmailshim

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSMTP is a minimal submission server: EHLO → STARTTLS → EHLO → AUTH
// PLAIN → MAIL/RCPT/DATA. It records the envelope + payload.
type fakeSMTP struct {
	addr       string
	mailFrom   string
	rcpts      []string
	data       string
	authProto  string
	rejectAuth bool
	done       chan struct{}
}

func startFakeSMTP(t *testing.T, rejectAuth bool) *fakeSMTP {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mail.panel.tld"},
		DNSNames:     []string{"mail.panel.tld"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	testRootCAs = pool
	t.Cleanup(func() { testRootCAs = nil })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	s := &fakeSMTP{addr: ln.Addr().String(), rejectAuth: rejectAuth, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		w := bufio.NewWriter(conn)
		r := bufio.NewReader(conn)
		say := func(line string) { w.WriteString(line + "\r\n"); w.Flush() }

		say("220 mail.panel.tld ESMTP")
		inTLS := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				if inTLS {
					say("250-mail.panel.tld")
					say("250 AUTH PLAIN LOGIN")
				} else {
					say("250-mail.panel.tld")
					say("250 STARTTLS")
				}
			case cmd == "STARTTLS":
				say("220 go ahead")
				tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}})
				if err := tc.Handshake(); err != nil {
					return
				}
				conn = tc
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				w = bufio.NewWriter(conn)
				r = bufio.NewReader(conn)
				inTLS = true
			case strings.HasPrefix(cmd, "AUTH"):
				s.authProto = strings.Fields(cmd)[1]
				if s.rejectAuth {
					say("535 5.7.8 bad credentials")
					continue
				}
				say("235 ok")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				s.mailFrom = strings.TrimSpace(line[len("MAIL FROM:"):])
				say("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				s.rcpts = append(s.rcpts, strings.TrimSpace(line[len("RCPT TO:"):]))
				say("250 ok")
			case cmd == "DATA":
				say("354 end with .")
				var b strings.Builder
				for {
					dl, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimRight(dl, "\r\n") == "." {
						break
					}
					b.WriteString(dl)
				}
				s.data = b.String()
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return s
}

func testCred() *Cred {
	return &Cred{Email: "noreply@example.com", Password: "sekrit", Host: "mail.panel.tld"}
}

func TestSubmit_ForcesEnvelopeAndStripsNothingElse(t *testing.T) {
	s := startFakeSMTP(t, false)
	body := []byte("From: wordpress@example.com\nTo: buyer@dest.tld\nSubject: hi\n\nline one\n.dot line\n")
	err := Submit(s.addr, testCred(), []string{"buyer@dest.tld", "BUYER@dest.tld", "second@dest.tld"}, body)
	if err != nil {
		t.Fatal(err)
	}
	<-s.done
	if !strings.Contains(s.mailFrom, "noreply@example.com") {
		t.Errorf("envelope = %q, want the cred identity", s.mailFrom)
	}
	if len(s.rcpts) != 2 {
		t.Errorf("rcpts = %v, want deduped 2", s.rcpts)
	}
	if !strings.Contains(s.data, "From: wordpress@example.com") {
		t.Errorf("header From missing:\n%s", s.data)
	}
	if !strings.Contains(s.data, ".dot line") {
		t.Errorf("dot-stuffing roundtrip broke the body:\n%s", s.data)
	}
	if s.authProto != "PLAIN" {
		t.Errorf("auth proto = %q", s.authProto)
	}
}

func TestSubmit_AuthRejectedIsNoPerm(t *testing.T) {
	s := startFakeSMTP(t, true)
	err := Submit(s.addr, testCred(), []string{"x@y.z"}, []byte("To: x@y.z\n\nb\n"))
	if ExitCode(err) != ExitNoPerm {
		t.Fatalf("exit = %d, want %d (err=%v)", ExitCode(err), ExitNoPerm, err)
	}
}

func TestSubmit_NoRecipients(t *testing.T) {
	err := Submit("127.0.0.1:1", testCred(), nil, []byte("x"))
	if ExitCode(err) != ExitUsage {
		t.Fatalf("exit = %d, want %d", ExitCode(err), ExitUsage)
	}
}

func TestSubmit_ConnectRefusedIsTempFail(t *testing.T) {
	// Port 1 on loopback is never listening.
	err := Submit("127.0.0.1:1", testCred(), []string{"x@y.z"}, []byte("x"))
	if ExitCode(err) != ExitTempFail {
		t.Fatalf("exit = %d, want %d (err=%v)", ExitCode(err), ExitTempFail, err)
	}
}

func TestFormatLog_NoSecrets(t *testing.T) {
	line := FormatLog("alice", "example.com", "noreply@example.com", 2, nil)
	if strings.Contains(line, "sekrit") {
		t.Error("password leaked into log line")
	}
	if !strings.Contains(line, "user=alice") || !strings.Contains(line, "ok") {
		t.Errorf("unexpected log line: %s", line)
	}
}
