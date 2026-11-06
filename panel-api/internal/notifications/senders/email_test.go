package senders

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

func TestEmail_SendsWellFormedMIME(t *testing.T) {
	t.Parallel()
	var gotFrom string
	var gotTo []string
	var gotMsg string
	fake := func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotFrom = from
		gotTo = to
		gotMsg = string(msg)
		return nil
	}
	e := NewEmail("").withSender(fake)
	ch := models.NotificationChannel{Kind: "email", Config: models.NotificationChannelConfig{ToEmail: "admin@example.com", FromEmail: "alerts@example.com"}}
	env := notifications.Envelope{Title: "cert.renew.fail", Body: "Let's Encrypt 429", Severity: "error"}
	require.NoError(t, e.Send(context.Background(), ch, env))

	require.Equal(t, "alerts@example.com", gotFrom)
	require.Equal(t, []string{"admin@example.com"}, gotTo)
	require.Contains(t, gotMsg, "Subject: cert.renew.fail")
	require.Contains(t, gotMsg, "Content-Type: text/html")
	require.Contains(t, gotMsg, "Let&#39;s Encrypt 429")
}

func TestEmail_MissingConfigPermanent(t *testing.T) {
	t.Parallel()
	e := NewEmail("")
	err := e.Send(context.Background(), models.NotificationChannel{Kind: "email"}, notifications.Envelope{Title: "x"})
	require.True(t, errors.Is(err, notifications.ErrPermanent))
}

func TestEmail_RejectsHeaderInjection(t *testing.T) {
	t.Parallel()
	calls := 0
	fake := func(addr string, a smtp.Auth, from string, to []string, msg []byte) error { calls++; return nil }
	e := NewEmail("").withSender(fake)
	ch := models.NotificationChannel{Kind: "email", Config: models.NotificationChannelConfig{ToEmail: "a@b\r\nBcc: evil@x", FromEmail: "from@x"}}
	err := e.Send(context.Background(), ch, notifications.Envelope{Title: "x"})
	require.True(t, errors.Is(err, notifications.ErrPermanent))
	require.Zero(t, calls)
}

func TestEmail_SMTPModeRoutesToExternalRelayWithAuth(t *testing.T) {
	t.Parallel()
	var gotAddr string
	var gotAuth smtp.Auth
	fake := func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr = addr
		gotAuth = a
		return nil
	}
	e := NewEmail("127.0.0.1:587").withSender(fake)
	ch := models.NotificationChannel{Kind: "email", Config: models.NotificationChannelConfig{
		ToEmail:      "admin@example.com",
		FromEmail:    "alerts@example.com",
		SMTPMode:     "smtp",
		SMTPHost:     "smtp.example.com",
		SMTPPort:     587,
		SMTPUsername: "u",
		SMTPPassword: "p",
		SMTPTLS:      "starttls",
	}}
	require.NoError(t, e.Send(context.Background(), ch, notifications.Envelope{Title: "x", Body: "y", Severity: "info"}))
	require.Equal(t, "smtp.example.com:587", gotAddr)
	require.NotNil(t, gotAuth)
}

func TestEmail_SMTPModeImplicitTLSUsesTLSSeam(t *testing.T) {
	t.Parallel()
	var localCalls, tlsCalls int
	e := NewEmail("127.0.0.1:587").
		withSender(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			localCalls++
			return nil
		}).
		withTLSSender(func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
			tlsCalls++
			require.Equal(t, "smtp.example.com:465", addr)
			return nil
		})
	ch := models.NotificationChannel{Kind: "email", Config: models.NotificationChannelConfig{
		ToEmail:   "a@b",
		FromEmail: "c@d",
		SMTPMode:  "smtp",
		SMTPHost:  "smtp.example.com",
		SMTPPort:  465,
		SMTPTLS:   "tls",
	}}
	require.NoError(t, e.Send(context.Background(), ch, notifications.Envelope{Title: "x"}))
	require.Equal(t, 0, localCalls)
	require.Equal(t, 1, tlsCalls)
}

func TestEmail_StripsCRLFFromSubject(t *testing.T) {
	t.Parallel()
	var gotMsg string
	fake := func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotMsg = string(msg)
		return nil
	}
	e := NewEmail("").withSender(fake)
	ch := models.NotificationChannel{Kind: "email", Config: models.NotificationChannelConfig{ToEmail: "a@b", FromEmail: "c@d"}}
	env := notifications.Envelope{Title: "line1\r\nBcc: attacker@x", Body: "ok", Severity: "info"}
	require.NoError(t, e.Send(context.Background(), ch, env))
	// CRLF in subject is folded to spaces — the important property is
	// that the Subject header stays on one line (no new header injected).
	headerBlock := gotMsg
	if i := strings.Index(gotMsg, "\r\n\r\n"); i >= 0 {
		headerBlock = gotMsg[:i]
	}
	var subjectLines []string
	for _, l := range strings.Split(headerBlock, "\r\n") {
		if strings.HasPrefix(l, "Subject:") || strings.HasPrefix(l, "Bcc:") {
			subjectLines = append(subjectLines, l)
		}
	}
	require.Len(t, subjectLines, 1)
	require.True(t, strings.HasPrefix(subjectLines[0], "Subject:"))
}

// TestEmail_LocalAuthUsesNotifyMailbox verifies that in local mode with notify
// creds set (GH #322), the sender authenticates as the notify mailbox and uses
// it as the envelope sender, while the configured from_email stays the visible
// From: header.
func TestEmail_LocalAuthUsesNotifyMailbox(t *testing.T) {
	t.Parallel()
	var gotAddr, gotUser, gotPass, gotEnvFrom string
	var gotTo []string
	var gotMsg string
	authFake := func(addr, user, pass, from string, to []string, msg []byte) error {
		gotAddr, gotUser, gotPass, gotEnvFrom, gotTo, gotMsg = addr, user, pass, from, to, string(msg)
		return nil
	}
	legacyCalled := false
	e := NewEmail("127.0.0.1:587").
		withAuthSender(authFake).
		withSender(func(string, smtp.Auth, string, []string, []byte) error { legacyCalled = true; return nil }).
		WithLocalCreds(func(context.Context) (string, string, bool) {
			return "jabali-notify@panel.example.com", "s3cret", true
		})
	ch := models.NotificationChannel{Kind: "email", Config: models.NotificationChannelConfig{
		ToEmail: "admin@example.com", FromEmail: "alerts@example.com", // local mode (SMTPMode empty)
	}}
	env := notifications.Envelope{Title: "test", Body: "hi", Severity: "info"}
	require.NoError(t, e.Send(context.Background(), ch, env))

	require.False(t, legacyCalled, "legacy unauth path must not be used when notify creds present")
	require.Equal(t, "127.0.0.1:587", gotAddr)
	require.Equal(t, "jabali-notify@panel.example.com", gotUser)
	require.Equal(t, "s3cret", gotPass)
	require.Equal(t, "jabali-notify@panel.example.com", gotEnvFrom, "envelope sender = notify mailbox")
	require.Equal(t, []string{"admin@example.com"}, gotTo)
	require.Contains(t, gotMsg, "From: alerts@example.com", "visible From: stays the configured from_email")
}

// TestEmail_LocalNoCredsFallsBack verifies that without notify creds the sender
// falls back to the legacy (unauthenticated) path — preserving prior behaviour.
func TestEmail_LocalNoCredsFallsBack(t *testing.T) {
	t.Parallel()
	legacyCalled := false
	e := NewEmail("127.0.0.1:587").withSender(func(string, smtp.Auth, string, []string, []byte) error {
		legacyCalled = true
		return nil
	})
	ch := models.NotificationChannel{Kind: "email", Config: models.NotificationChannelConfig{ToEmail: "a@example.com", FromEmail: "b@example.com"}}
	require.NoError(t, e.Send(context.Background(), ch, notifications.Envelope{Title: "t", Body: "b"}))
	require.True(t, legacyCalled, "no notify creds → legacy path")
}
