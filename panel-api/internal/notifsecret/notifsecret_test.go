package notifsecret

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

func testKey() ssokey.Key {
	var k ssokey.Key
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func TestSealOpen_RoundTrip(t *testing.T) {
	key := testKey()
	cfg := models.NotificationChannelConfig{
		URL:          "https://ntfy.example/topic", // public — must survive untouched
		BotToken:     "123:SECRET-BOT",
		SMTPPassword: "hunter2",
		Bearer:       "bearer-abc",
		HMACSecret:   "hmac-0123456789abcdef",
	}
	if err := Seal(&cfg, key); err != nil {
		t.Fatal(err)
	}
	// Secrets are now sealed (prefixed, not the original), public field intact.
	if cfg.URL != "https://ntfy.example/topic" {
		t.Error("public URL field must not be sealed")
	}
	for _, s := range []string{cfg.BotToken, cfg.SMTPPassword, cfg.Bearer, cfg.HMACSecret} {
		if !strings.HasPrefix(s, sealPrefix) {
			t.Errorf("secret not sealed: %q", s)
		}
	}
	if strings.Contains(cfg.BotToken, "SECRET-BOT") {
		t.Error("plaintext token still present after seal")
	}
	// Idempotent: sealing again is a no-op.
	before := cfg.BotToken
	if err := Seal(&cfg, key); err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != before {
		t.Error("Seal is not idempotent")
	}
	// Open recovers the plaintext.
	if err := Open(&cfg, key); err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != "123:SECRET-BOT" || cfg.SMTPPassword != "hunter2" {
		t.Errorf("Open did not recover secrets: %+v", cfg)
	}
}

func TestRedact_BlanksAndReports(t *testing.T) {
	cfg := models.NotificationChannelConfig{URL: "https://x", BotToken: "enc:1:whatever"}
	if got := Redact(&cfg); !got {
		t.Error("Redact should report hasSecret=true")
	}
	if cfg.BotToken != "" {
		t.Errorf("BotToken not blanked: %q", cfg.BotToken)
	}
	if cfg.URL != "https://x" {
		t.Error("public field must survive redaction")
	}
	empty := models.NotificationChannelConfig{URL: "https://x"}
	if Redact(&empty) {
		t.Error("Redact should report hasSecret=false when no secret set")
	}
}

func TestPreserveEmptySecrets_WriteOnly(t *testing.T) {
	prev := models.NotificationChannelConfig{BotToken: "enc:1:OLD", SMTPPassword: "enc:1:OLDPW"}
	// Incoming edit: new token, but SMTP password left empty (keep existing).
	next := models.NotificationChannelConfig{BotToken: "new-plaintext", SMTPPassword: ""}
	PreserveEmptySecrets(&next, prev)
	if next.BotToken != "new-plaintext" {
		t.Errorf("a provided secret must be kept, got %q", next.BotToken)
	}
	if next.SMTPPassword != "enc:1:OLDPW" {
		t.Errorf("an empty secret must preserve the previous value, got %q", next.SMTPPassword)
	}
}

func TestHasPlaintextSecret(t *testing.T) {
	if !HasPlaintextSecret(&models.NotificationChannelConfig{BotToken: "raw"}) {
		t.Error("raw token should be flagged plaintext")
	}
	if HasPlaintextSecret(&models.NotificationChannelConfig{BotToken: "enc:1:x"}) {
		t.Error("sealed token must not be flagged plaintext")
	}
	if HasPlaintextSecret(&models.NotificationChannelConfig{URL: "https://x"}) {
		t.Error("no secret should not be flagged")
	}
}
