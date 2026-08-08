package sendmailshim

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SubmitTimeout bounds the whole SMTP conversation; PHP mail() blocks the
// worker while the shim runs, so a hung Stalwart must not pin FPM children.
const SubmitTimeout = 30 * time.Second

// testRootCAs lets tests trust their self-signed server cert. Compiled-in
// nil in production; certificate verification is never skipped.
var testRootCAs *x509.CertPool

// Submit hands the message to the submission listener at addr
// (127.0.0.1:587 in production; tests inject a local fake). The envelope
// sender is ALWAYS cred.Email — never a caller-supplied address — because
// Stalwart enforces MAIL FROM == authenticated identity.
func Submit(addr string, cred *Cred, recipients []string, body []byte) error {
	recipients = dedupe(recipients)
	if len(recipients) == 0 {
		return coded(ExitUsage, "no recipients (empty To/Cc/Bcc and no argv recipients)")
	}

	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return coded(ExitTempFail, "connect %s: %v", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(SubmitTimeout))

	// The client is named after the CERTIFICATE host (not the dialed IP):
	// StartTLS verifies against it and PlainAuth refuses a name mismatch.
	c, err := smtp.NewClient(conn, cred.Host)
	if err != nil {
		conn.Close()
		return coded(ExitTempFail, "smtp greeting: %v", err)
	}
	defer c.Close()

	if err := c.Hello("localhost"); err != nil {
		return coded(ExitTempFail, "EHLO: %v", err)
	}
	// STARTTLS with the certificate name the panel provisioned (the cert is
	// for the mail FQDN, not 127.0.0.1). Verification stays ON.
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: cred.Host, MinVersion: tls.VersionTLS12, RootCAs: testRootCAs}); err != nil {
			return coded(ExitTempFail, "starttls: %v", err)
		}
	} else {
		return coded(ExitTempFail, "server offers no STARTTLS")
	}
	if err := c.Auth(smtp.PlainAuth("", cred.Email, cred.Password, cred.Host)); err != nil {
		return coded(ExitNoPerm, "auth as %s rejected: %v", cred.Email, err)
	}
	if err := c.Mail(cred.Email); err != nil {
		return classifySMTP("MAIL FROM", err)
	}
	var accepted int
	var lastErr error
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			lastErr = classifySMTP("RCPT "+rcpt, err)
			continue
		}
		accepted++
	}
	if accepted == 0 {
		if lastErr != nil {
			return lastErr
		}
		return coded(ExitTempFail, "no recipient accepted")
	}
	w, err := c.Data()
	if err != nil {
		return classifySMTP("DATA", err)
	}
	if _, err := w.Write(body); err != nil {
		return coded(ExitTempFail, "write body: %v", err)
	}
	if err := w.Close(); err != nil {
		return classifySMTP("DATA close", err)
	}
	return c.Quit()
}

// classifySMTP maps an SMTP error to tempfail (4xx / io) or a permanent
// data error (5xx) so cron wrappers and callers can distinguish retryable.
func classifySMTP(stage string, err error) error {
	msg := err.Error()
	if len(msg) >= 1 && msg[0] == '5' {
		return coded(ExitDataErr, "%s: %v", stage, err)
	}
	return coded(ExitTempFail, "%s: %v", stage, err)
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		key := strings.ToLower(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// FormatLog builds the single syslog line for a submission attempt. Password
// and message content never appear.
func FormatLog(user, fromDomain, credEmail string, rcpts int, err error) string {
	status := "ok"
	if err != nil {
		status = fmt.Sprintf("fail(%d): %v", ExitCode(err), err)
	}
	return fmt.Sprintf("user=%s from_domain=%q identity=%s rcpts=%d %s", user, fromDomain, credEmail, rcpts, status)
}
