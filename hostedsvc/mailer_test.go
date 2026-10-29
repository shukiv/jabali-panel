package hostedsvc

import (
	"net/mail"
	"strings"
	"testing"
	"time"
)

// RFC 5322 requires a Date header, and Gmail's sender guidelines ask for a
// Message-ID. The code mail carries both itself instead of relying on the
// relay to add them (GH #2319: codes landed in Gmail spam).
func TestCodeMessage_HasDateAndMessageID(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 54, 38, 0, time.UTC)
	parse := func(raw string) *mail.Message {
		t.Helper()
		msg, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("not a valid message: %v\n%s", err, raw)
		}
		return msg
	}
	msg := parse(codeMessage("webmaster@jabali-panel.com", "user@example.com", "123456", now))

	if d, err := msg.Header.Date(); err != nil {
		t.Errorf("Date header: %v", err)
	} else if !d.Equal(now) {
		t.Errorf("Date = %v, want %v", d, now)
	}
	id := msg.Header.Get("Message-Id")
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@jabali-panel.com>") || len(id) < len("<@jabali-panel.com>")+16 {
		t.Errorf("Message-ID = %q, want <unique@jabali-panel.com>", id)
	}
	if again := parse(codeMessage("webmaster@jabali-panel.com", "user@example.com", "123456", now)).Header.Get("Message-Id"); again == id {
		t.Errorf("two messages share Message-ID %q", id)
	}

	if got, want := msg.Header.Get("Subject"), "123456 is your Jabali hostname verification code"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	if from, err := mail.ParseAddress(msg.Header.Get("From")); err != nil || from.Address != "webmaster@jabali-panel.com" {
		t.Errorf("From = %q (%v)", msg.Header.Get("From"), err)
	}
}
