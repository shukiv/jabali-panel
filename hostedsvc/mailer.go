package hostedsvc

import (
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Mailer delivers verification codes. Tests use a fake.
type Mailer interface {
	SendCode(email, code string) error
}

// SMTPMailer submits through an authenticated relay (riva's Stalwart in
// production). The sender identity MUST be the authenticated mailbox — the
// relay rejects MAIL FROM ≠ authed identity with 501 (the JAB-230 lesson,
// blueprint trap 3). TLS is verified against Host; never skipped.
type SMTPMailer struct {
	Addr     string // host:port of the submission listener, e.g. mail.reeva.me:587
	Host     string // TLS certificate name
	From     string // authenticated mailbox; also the From/Sender header
	Password string
}

func (m *SMTPMailer) SendCode(email, code string) error {
	conn, err := net.DialTimeout("tcp", m.Addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect %s: %w", m.Addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if err := c.Hello("jabalihosted.com"); err != nil {
		return err
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return fmt.Errorf("relay offers no STARTTLS")
	}
	if err := c.StartTLS(&tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if err := c.Auth(smtp.PlainAuth("", m.From, m.Password, m.Host)); err != nil {
		return fmt.Errorf("relay auth: %w", err)
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(email); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(codeMessage(m.From, email, code, time.Now()))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// codeMessage is the verification-code mail from the mailbox from to email.
// It carries its own Date and Message-ID: RFC 5322 requires Date, Gmail asks
// for a Message-ID, and the relay is not relied on to add either (GH #2319).
func codeMessage(from, email, code string, now time.Time) string {
	domain := BaseDomain
	if at := strings.LastIndex(from, "@"); at >= 0 {
		domain = from[at+1:]
	}
	return fmt.Sprintf("Date: %s\r\nMessage-ID: <%s@%s>\r\nFrom: Jabali Hosted <%s>\r\nTo: <%s>\r\nSubject: %s is your Jabali hostname verification code\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"Your verification code is: %s\r\n\r\n"+
		"Enter it in the Jabali Panel installer to claim your free %s hostname.\r\n"+
		"The code expires in 15 minutes. If you didn't request this, ignore this message.\r\n",
		now.Format(time.RFC1123Z), rand.Text(), domain, from, email, code, code, BaseDomain)
}
