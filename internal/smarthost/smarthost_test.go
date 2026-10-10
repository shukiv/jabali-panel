package smarthost

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	ok := Config{Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS, Username: "relay@example.com", Password: "s3cret"}
	for _, tc := range []struct {
		name    string
		edit    func(*Config)
		wantErr string
	}{
		{"starttls with a login", func(*Config) {}, ""},
		{"implicit TLS", func(c *Config) { c.TLS, c.Port = TLSImplicit, 465 }, ""},
		{"no login, no encryption", func(c *Config) { c.TLS, c.Port, c.Username, c.Password = TLSNone, 25, "", "" }, ""},
		{"IPv4 address", func(c *Config) { c.Host = "192.0.2.10" }, ""},
		{"IPv6 address", func(c *Config) { c.Host = "2001:db8::25" }, ""},
		{"single-label internal name", func(c *Config) { c.Host = "relay" }, ""},
		{"port 2525", func(c *Config) { c.Port = 2525 }, ""},
		{"no host", func(c *Config) { c.Host = "" }, "host is required"},
		{"a URL, not a host", func(c *Config) { c.Host = "smtp://smtp.example.com" }, "host must be"},
		{"host with a space", func(c *Config) { c.Host = "smtp example.com" }, "host must be"},
		{"host with a port", func(c *Config) { c.Host = "smtp.example.com:587" }, "host must be"},
		{"label starts with a hyphen", func(c *Config) { c.Host = "-smtp.example.com" }, "host must be"},
		{"empty label", func(c *Config) { c.Host = "smtp..example.com" }, "host must be"},
		{"unspecified address", func(c *Config) { c.Host = "0.0.0.0" }, "usable address"},
		{"multicast address", func(c *Config) { c.Host = "224.0.0.1" }, "usable address"},
		{"long host", func(c *Config) { c.Host = strings.Repeat("a.", 127) + "com" }, "longer than"},
		{"a non-mail port", func(c *Config) { c.Port = 8080 }, "port must be one of 25, 465, 587, 2525"},
		{"unknown encryption", func(c *Config) { c.TLS = "ssl" }, "encryption must be"},
		{"a login without encryption", func(c *Config) { c.TLS, c.Port = TLSNone, 25 }, "only be used with encryption"},
		{"a username without a password", func(c *Config) { c.Password = "" }, "password is required"},
		{"a password without a username", func(c *Config) { c.Username = "" }, "username is required"},
		{"a line break in the username", func(c *Config) { c.Username = "a\r\nRCPT TO:<x@y>" }, "line breaks"},
		{"a NUL in the password", func(c *Config) { c.Password = "a\x00b" }, "line breaks"},
		{"a long password", func(c *Config) { c.Password = strings.Repeat("p", 256) }, "longer than"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := ok
			tc.edit(&c)
			err := Validate(c)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// fakeServer is a minimal SMTP server for one connection.
type fakeServer struct {
	implicitTLS   bool
	offerSTARTTLS bool
	offerAUTH     bool
	rejectAuth    bool
	rejectRcpt    string // a RCPT TO address answered with 550

	from  string
	rcpts []string
	data  string

	port int
	mu   sync.Mutex
	seen []string // commands received, in order; AUTH carries the decoded user
	tlsd bool     // TLS was set up before AUTH
}

func (s *fakeServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func (s *fakeServer) record(cmd string) {
	s.mu.Lock()
	s.seen = append(s.seen, cmd)
	s.mu.Unlock()
}

// start listens on 127.0.0.1 with a certificate for "smtp.test", and makes the
// client trust it unless untrusted is set.
func (s *fakeServer) start(t *testing.T, untrusted bool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "smtp.test"},
		DNSNames:     []string{"smtp.test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
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
	if !untrusted {
		pool := x509.NewCertPool()
		pool.AddCert(leaf)
		rootCAs = pool
		t.Cleanup(func() { rootCAs = nil })
	}
	serverTLS := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s.port = ln.Addr().(*net.TCPAddr).Port

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		inTLS := false
		if s.implicitTLS {
			tc := tls.Server(conn, serverTLS)
			if tc.Handshake() != nil {
				return
			}
			conn, inTLS = tc, true
		}
		r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
		say := func(lines ...string) {
			for _, l := range lines {
				w.WriteString(l + "\r\n")
			}
			w.Flush()
		}
		say("220 smtp.test ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			cmd := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				s.record("EHLO")
				ext := []string{"250-smtp.test"}
				if s.offerSTARTTLS && !inTLS {
					ext = append(ext, "250-STARTTLS")
				}
				if s.offerAUTH {
					ext = append(ext, "250-AUTH PLAIN")
				}
				ext = append(ext, "250 8BITMIME")
				say(ext...)
			case cmd == "STARTTLS":
				s.record("STARTTLS")
				say("220 go ahead")
				tc := tls.Server(conn, serverTLS)
				if tc.Handshake() != nil {
					return
				}
				conn, inTLS = tc, true
				r, w = bufio.NewReader(conn), bufio.NewWriter(conn)
			case strings.HasPrefix(cmd, "AUTH PLAIN"):
				user := ""
				if f := strings.Fields(line); len(f) == 3 {
					if raw, err := base64.StdEncoding.DecodeString(f[2]); err == nil {
						if parts := strings.Split(string(raw), "\x00"); len(parts) == 3 {
							user = parts[1]
						}
					}
				}
				s.record("AUTH " + user)
				s.mu.Lock()
				s.tlsd = inTLS
				s.mu.Unlock()
				if s.rejectAuth {
					say("535 5.7.8 Authentication credentials invalid")
					continue
				}
				say("235 2.7.0 ok")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				s.record("MAIL")
				s.mu.Lock()
				arg := strings.TrimSpace(line[len("MAIL FROM:"):])
				if i := strings.IndexByte(arg, '>'); i >= 0 {
					arg = arg[:i]
				}
				s.from = strings.TrimPrefix(arg, "<")
				s.mu.Unlock()
				say("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				s.record("RCPT")
				rcpt := strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
				if rcpt == s.rejectRcpt {
					say("550 5.1.1 no such user")
					continue
				}
				s.mu.Lock()
				s.rcpts = append(s.rcpts, rcpt)
				s.mu.Unlock()
				say("250 ok")
			case cmd == "DATA":
				s.record("DATA")
				say("354 go ahead")
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
				s.mu.Lock()
				s.data = b.String()
				s.mu.Unlock()
				say("250 queued")
			case cmd == "QUIT":
				s.record("QUIT")
				say("221 bye")
				return
			default:
				s.record(cmd)
				say("502 not here")
			}
		}
	}()
}

func (s *fakeServer) config(tlsMode string, login bool) Config {
	c := Config{Host: "127.0.0.1", Port: s.port, TLS: tlsMode, HeloName: "panel.example.com"}
	if login {
		c.Username, c.Password = "relay@example.com", "s3cret"
	}
	return c
}

// probe runs Probe against the fake server. AllowedPorts is widened to the
// fake's random port for the test only.
func probe(t *testing.T, s *fakeServer, c Config) error {
	t.Helper()
	prev := AllowedPorts
	AllowedPorts = append([]int{s.port}, prev...)
	t.Cleanup(func() { AllowedPorts = prev })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Probe(ctx, c)
}

func stageOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Stage
	}
	return ""
}

func TestProbe_STARTTLSAndLogin(t *testing.T) {
	s := &fakeServer{offerSTARTTLS: true, offerAUTH: true}
	s.start(t, false)
	if err := probe(t, s, s.config(TLSStartTLS, true)); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	got := strings.Join(s.commands(), ",")
	if got != "EHLO,STARTTLS,EHLO,AUTH relay@example.com,QUIT" {
		t.Errorf("commands = %s", got)
	}
	if !s.tlsd {
		t.Error("the login was sent before TLS was set up")
	}
}

// STARTTLS is required, not opportunistic: without it the probe stops and the
// password never crosses the wire in plaintext.
func TestProbe_NoSTARTTLSOffered_NoLoginSent(t *testing.T) {
	s := &fakeServer{offerAUTH: true}
	s.start(t, false)
	err := probe(t, s, s.config(TLSStartTLS, true))
	if stageOf(err) != StageTLS || !strings.Contains(err.Error(), "doesn't offer STARTTLS") {
		t.Fatalf("err = %v, want a TLS error about STARTTLS", err)
	}
	for _, c := range s.commands() {
		if strings.HasPrefix(c, "AUTH") {
			t.Fatalf("the login was sent without TLS: %v", s.commands())
		}
	}
}

func TestProbe_ImplicitTLS(t *testing.T) {
	s := &fakeServer{implicitTLS: true, offerAUTH: true}
	s.start(t, false)
	if err := probe(t, s, s.config(TLSImplicit, true)); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !s.tlsd {
		t.Error("the login was sent before TLS was set up")
	}
}

func TestProbe_UntrustedCertificateFails(t *testing.T) {
	s := &fakeServer{offerSTARTTLS: true, offerAUTH: true}
	s.start(t, true)
	err := probe(t, s, s.config(TLSStartTLS, true))
	if stageOf(err) != StageTLS {
		t.Fatalf("err = %v, want a TLS error", err)
	}
	for _, c := range s.commands() {
		if strings.HasPrefix(c, "AUTH") {
			t.Fatalf("the login was sent over an unverified connection: %v", s.commands())
		}
	}
}

func TestProbe_LoginRejected(t *testing.T) {
	s := &fakeServer{offerSTARTTLS: true, offerAUTH: true, rejectAuth: true}
	s.start(t, false)
	err := probe(t, s, s.config(TLSStartTLS, true))
	if stageOf(err) != StageAuth || !strings.Contains(err.Error(), "535") {
		t.Fatalf("err = %v, want a login error carrying the server's 535", err)
	}
}

func TestProbe_NoAUTHOffered(t *testing.T) {
	s := &fakeServer{offerSTARTTLS: true}
	s.start(t, false)
	err := probe(t, s, s.config(TLSStartTLS, true))
	if stageOf(err) != StageAuth || !strings.Contains(err.Error(), "doesn't offer a login") {
		t.Fatalf("err = %v, want a login error", err)
	}
}

func TestProbe_PlainWithoutLogin(t *testing.T) {
	s := &fakeServer{offerSTARTTLS: true}
	s.start(t, false)
	if err := probe(t, s, s.config(TLSNone, false)); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got := strings.Join(s.commands(), ","); got != "EHLO,QUIT" {
		t.Errorf("commands = %s, want EHLO,QUIT (no TLS asked for)", got)
	}
}

func TestProbe_ConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s := &fakeServer{port: port}
	err = probe(t, s, s.config(TLSStartTLS, false))
	if stageOf(err) != StageConnect || !strings.HasPrefix(err.Error(), "couldn't connect") {
		t.Fatalf("err = %v, want a connect error", err)
	}
}

func TestProbe_InvalidConfigIsNotDialed(t *testing.T) {
	err := Probe(context.Background(), Config{Host: "smtp.example.com", Port: 8080, TLS: TLSStartTLS})
	if stageOf(err) != StageConfig || !strings.Contains(err.Error(), "port must be") {
		t.Fatalf("err = %v, want a config error", err)
	}
}

func TestConfigAddress(t *testing.T) {
	if got := (Config{Host: "2001:db8::25", Port: 587}).Address(); got != "[2001:db8::25]:"+strconv.Itoa(587) {
		t.Errorf("Address = %s", got)
	}
}

func send(t *testing.T, s *fakeServer, c Config, from string, to []string, msg string) error {
	t.Helper()
	prev := AllowedPorts
	AllowedPorts = append([]int{s.port}, prev...)
	t.Cleanup(func() { AllowedPorts = prev })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Send(ctx, c, from, to, []byte(msg))
}

func TestSend(t *testing.T) {
	s := &fakeServer{offerSTARTTLS: true, offerAUTH: true}
	s.start(t, false)
	msg := "From: Shop <shop@example.com>\nSubject: Order\n\nThanks.\n"
	if err := send(t, s, s.config(TLSStartTLS, true), "noreply@example.com", []string{"a@example.org", "b@example.org"}, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.from != "noreply@example.com" || strings.Join(s.rcpts, ",") != "a@example.org,b@example.org" {
		t.Errorf("envelope from=%q rcpts=%v", s.from, s.rcpts)
	}
	if s.data != strings.ReplaceAll(msg, "\n", "\r\n") {
		t.Errorf("data = %q", s.data)
	}
	if !s.tlsd {
		t.Error("logged in before TLS")
	}
}

func TestSend_RecipientRefused(t *testing.T) {
	s := &fakeServer{offerSTARTTLS: true, offerAUTH: true, rejectRcpt: "nobody@example.org"}
	s.start(t, false)
	err := send(t, s, s.config(TLSStartTLS, true), "noreply@example.com", []string{"nobody@example.org"}, "Subject: x\n\nx\n")
	var e *Error
	if !errors.As(err, &e) || e.Stage != StageSMTP || !strings.Contains(err.Error(), "550") {
		t.Fatalf("err = %v, want an smtp-stage error with the 550", err)
	}
}

func TestSend_NoRecipients(t *testing.T) {
	if err := Send(context.Background(), Config{}, "noreply@example.com", nil, nil); stageOf(err) != StageConfig {
		t.Fatalf("err = %v", err)
	}
}
