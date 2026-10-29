package hostedsvc

import (
	"io"
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

	if from, err := mail.ParseAddress(msg.Header.Get("From")); err != nil || from.Address != "webmaster@jabali-panel.com" {
		t.Errorf("From = %q (%v)", msg.Header.Get("From"), err)
	}
}

// Gmail flagged the first wording as phishing ("many people marked similar
// messages as phishing", GH #2319). The mail names the product it comes from,
// says who asked for it, keeps the code out of the subject and drops the
// "claim your free" wording.
func TestCodeMessage_Wording(t *testing.T) {
	raw := codeMessage("webmaster@jabali-panel.com", "user@example.com", "123456", time.Now())
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("not a valid message: %v\n%s", err, raw)
	}
	if from, err := mail.ParseAddress(msg.Header.Get("From")); err != nil || from.Name != "Jabali Panel" {
		t.Errorf("From = %q (%v), want the name Jabali Panel", msg.Header.Get("From"), err)
	}
	if got, want := msg.Header.Get("Subject"), "Your Jabali Panel verification code"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	var body strings.Builder
	if _, err := io.Copy(&body, msg.Body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Jabali Panel installer",
		"jabalihosted.com hostname for this email address",
		"Verification code: 123456",
		"expires in 15 minutes",
		"nothing happens without the code",
	} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("body lacks %q:\n%s", want, body.String())
		}
	}
	for _, word := range []string{"free", "claim"} {
		if strings.Contains(strings.ToLower(raw), word) {
			t.Errorf("message still says %q:\n%s", word, raw)
		}
	}
}
