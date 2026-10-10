// Package smarthost talks to an operator's outbound SMTP relay (a
// "smarthost") for website mail (GH #2056, ADR 0174). A server without the
// mail module has no local mail server, so PHP mail() from the sites can go
// out through the operator's own relay instead.
//
// The panel uses Validate and Probe (the Server Settings "Test" button, and
// the check before switching to a smarthost). The relay that sends the sites'
// mail dials through the same Dial, so the test and the real sends see the
// smarthost the same way.
//
// TLS is always verified against the configured host; it is never skipped.
// A login is only ever sent over TLS.
package smarthost

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// TLS modes.
const (
	// TLSStartTLS upgrades a plain connection with STARTTLS, and fails when
	// the smarthost doesn't offer it (never falls back to plaintext).
	TLSStartTLS = "starttls"
	// TLSImplicit speaks TLS from the first byte (usually port 465).
	TLSImplicit = "tls"
	// TLSNone sends in plaintext. No login is allowed in this mode.
	TLSNone = "none"
)

// AllowedPorts are the SMTP ports a smarthost may use. The panel dials what an
// admin types here, so it is limited to mail ports.
var AllowedPorts = []int{25, 465, 587, 2525}

const (
	maxHostLen       = 253
	maxCredentialLen = 255
	dialTimeout      = 10 * time.Second
	sessionTimeout   = 30 * time.Second
)

// Config is one smarthost.
type Config struct {
	Host     string
	Port     int
	TLS      string
	Username string
	Password string
	// HeloName is the name sent with EHLO, normally the panel hostname.
	// Empty sends "localhost".
	HeloName string
}

// Address is host:port.
func (c Config) Address() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Validate checks a smarthost before it is saved or dialed. The messages are
// meant for the admin who typed the values.
func Validate(c Config) error {
	if err := validateHost(c.Host); err != nil {
		return err
	}
	if !portAllowed(c.Port) {
		return fmt.Errorf("port must be one of %s", portList())
	}
	switch c.TLS {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return fmt.Errorf("encryption must be %q, %q or %q", TLSStartTLS, TLSImplicit, TLSNone)
	}
	if err := validateCredential("username", c.Username); err != nil {
		return err
	}
	if err := validateCredential("password", c.Password); err != nil {
		return err
	}
	if c.TLS == TLSNone && (c.Username != "" || c.Password != "") {
		return errors.New("a login can only be used with encryption: choose STARTTLS or TLS, or remove the username")
	}
	if c.Username != "" && c.Password == "" {
		return errors.New("a password is required with a username")
	}
	if c.Username == "" && c.Password != "" {
		return errors.New("a username is required with a password")
	}
	return nil
}

func validateHost(h string) error {
	if h == "" {
		return errors.New("host is required")
	}
	if len(h) > maxHostLen {
		return fmt.Errorf("host is longer than %d characters", maxHostLen)
	}
	if ip := net.ParseIP(h); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("host must be a usable address")
		}
		return nil
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("host must be a hostname such as smtp.example.com, or an IP address")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return errors.New("host must be a hostname such as smtp.example.com, or an IP address")
			}
		}
	}
	return nil
}

func validateCredential(name, v string) error {
	if len(v) > maxCredentialLen {
		return fmt.Errorf("%s is longer than %d characters", name, maxCredentialLen)
	}
	if strings.ContainsAny(v, "\x00\r\n") {
		return fmt.Errorf("%s must not contain line breaks", name)
	}
	return nil
}

func portAllowed(p int) bool {
	for _, a := range AllowedPorts {
		if p == a {
			return true
		}
	}
	return false
}

func portList() string {
	parts := make([]string, len(AllowedPorts))
	for i, p := range AllowedPorts {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}

// Stage names where a dial stopped.
const (
	StageConfig  = "config"
	StageConnect = "connect"
	StageTLS     = "tls"
	StageAuth    = "auth"
	StageSMTP    = "smtp"
)

// Error says which step of talking to the smarthost failed.
type Error struct {
	Stage string
	Err   error
}

func (e *Error) Error() string {
	switch e.Stage {
	case StageConfig:
		return "invalid smarthost: " + e.Err.Error()
	case StageConnect:
		return "couldn't connect: " + e.Err.Error()
	case StageTLS:
		return "TLS failed: " + e.Err.Error()
	case StageAuth:
		return "login failed: " + e.Err.Error()
	default:
		return "the smarthost refused: " + e.Err.Error()
	}
}

func (e *Error) Unwrap() error { return e.Err }

// rootCAs lets tests trust their own certificate. Nil means the system roots.
var rootCAs *x509.CertPool

func tlsConfig(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: rootCAs}
}

// Dial connects to the smarthost, says EHLO, sets up TLS as configured and
// logs in when a username is set. The caller sends with the returned client
// and must Quit or Close it. Errors are *Error.
func Dial(ctx context.Context, c Config) (*smtp.Client, error) {
	if err := Validate(c); err != nil {
		return nil, &Error{Stage: StageConfig, Err: err}
	}
	deadline := time.Now().Add(sessionTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	d := net.Dialer{Timeout: dialTimeout, Deadline: deadline}
	conn, err := d.DialContext(ctx, "tcp", c.Address())
	if err != nil {
		return nil, &Error{Stage: StageConnect, Err: err}
	}
	_ = conn.SetDeadline(deadline)
	if c.TLS == TLSImplicit {
		tc := tls.Client(conn, tlsConfig(c.Host))
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, &Error{Stage: StageTLS, Err: err}
		}
		conn = tc
	}

	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return nil, &Error{Stage: StageSMTP, Err: err}
	}
	fail := func(stage string, err error) (*smtp.Client, error) {
		client.Close()
		return nil, &Error{Stage: stage, Err: err}
	}

	helo := c.HeloName
	if helo == "" {
		helo = "localhost"
	}
	if err := client.Hello(helo); err != nil {
		return fail(StageSMTP, err)
	}
	if c.TLS == TLSStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fail(StageTLS, errors.New("the smarthost doesn't offer STARTTLS"))
		}
		if err := client.StartTLS(tlsConfig(c.Host)); err != nil {
			return fail(StageTLS, err)
		}
	}
	if c.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return fail(StageAuth, errors.New("the smarthost doesn't offer a login (AUTH)"))
		}
		if err := client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return fail(StageAuth, err)
		}
	}
	return client, nil
}

// Probe checks that the smarthost accepts a connection, TLS and the login,
// then says goodbye without sending anything.
func Probe(ctx context.Context, c Config) error {
	client, err := Dial(ctx, c)
	if err != nil {
		return err
	}
	if err := client.Quit(); err != nil {
		client.Close()
		return &Error{Stage: StageSMTP, Err: err}
	}
	return nil
}

// Send delivers one message through the smarthost: from is the envelope
// sender, to the envelope recipients, msg the message as it should arrive.
// Errors are *Error; at the smtp stage Err is the server's reply (a
// *textproto.Error carrying the code) when the server refused.
func Send(ctx context.Context, c Config, from string, to []string, msg []byte) error {
	if len(to) == 0 {
		return &Error{Stage: StageConfig, Err: errors.New("no recipients")}
	}
	client, err := Dial(ctx, c)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		client.Close()
		return &Error{Stage: StageSMTP, Err: err}
	}
	if err := client.Mail(from); err != nil {
		return fail(err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fail(err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fail(err)
	}
	if _, err := w.Write(msg); err != nil {
		return fail(err)
	}
	if err := w.Close(); err != nil {
		return fail(err)
	}
	if err := client.Quit(); err != nil {
		client.Close()
	}
	return nil
}
