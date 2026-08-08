package sendmailshim

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantT     bool
		wantFrom  string
		wantRcpts []string
		wantErr   bool
	}{
		{name: "php default", args: []string{"-t", "-i"}, wantT: true},
		{name: "separate f", args: []string{"-f", "a@b.c", "-t"}, wantT: true, wantFrom: "a@b.c"},
		{name: "glued f", args: []string{"-fa@b.c"}, wantFrom: "a@b.c"},
		{name: "positional", args: []string{"-i", "x@y.z", "w@y.z"}, wantRcpts: []string{"x@y.z", "w@y.z"}},
		{name: "double dash", args: []string{"-t", "--", "-weird@y.z"}, wantT: true, wantRcpts: []string{"-weird@y.z"}},
		{name: "unknown flags ignored", args: []string{"-oem", "-Ofoo=bar", "-v", "-t"}, wantT: true},
		{name: "f missing value", args: []string{"-f"}, wantErr: true},
		{name: "oi alias", args: []string{"-oi", "r@d.tld"}, wantRcpts: []string{"r@d.tld"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := ParseArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if ExitCode(err) != ExitUsage {
					t.Fatalf("exit = %d, want %d", ExitCode(err), ExitUsage)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if o.ReadRecipientsFromHeaders != tc.wantT {
				t.Errorf("-t = %v, want %v", o.ReadRecipientsFromHeaders, tc.wantT)
			}
			if o.EnvelopeFromHint != tc.wantFrom {
				t.Errorf("from hint = %q, want %q", o.EnvelopeFromHint, tc.wantFrom)
			}
			if strings.Join(o.Recipients, ",") != strings.Join(tc.wantRcpts, ",") {
				t.Errorf("recipients = %v, want %v", o.Recipients, tc.wantRcpts)
			}
		})
	}
}

func TestParseMessage_FromDomainAndBccStrip(t *testing.T) {
	in := "From: Shop <wordpress@example.com>\n" +
		"To: buyer@dest.tld, second@dest.tld\n" +
		"Cc: cc@dest.tld\n" +
		"Bcc: hidden@dest.tld,\n" +
		"\talso-hidden@dest.tld\n" +
		"Subject: Order\n" +
		"\n" +
		"Bcc: not-a-header, body line.\n"
	msg, err := ParseMessage(strings.NewReader(in), true)
	if err != nil {
		t.Fatal(err)
	}
	if msg.FromDomain != "example.com" {
		t.Errorf("FromDomain = %q", msg.FromDomain)
	}
	if msg.FromAddr != "wordpress@example.com" {
		t.Errorf("FromAddr = %q", msg.FromAddr)
	}
	got := string(msg.Raw)
	if strings.Contains(strings.SplitN(got, "\n\n", 2)[0], "hidden@dest.tld") {
		t.Errorf("Bcc not stripped from headers:\n%s", got)
	}
	if !strings.Contains(got, "Bcc: not-a-header, body line.") {
		t.Errorf("body was mangled:\n%s", got)
	}
	want := []string{"buyer@dest.tld", "second@dest.tld", "cc@dest.tld", "hidden@dest.tld", "also-hidden@dest.tld"}
	if strings.Join(msg.HeaderRecipients, ",") != strings.Join(want, ",") {
		t.Errorf("recipients = %v, want %v", msg.HeaderRecipients, want)
	}
}

func TestParseMessage_CRLF(t *testing.T) {
	in := "From: a@ex.io\r\nTo: b@ex.io\r\nSubject: s\r\n\r\nbody\r\n.\r\nmore\r\n"
	msg, err := ParseMessage(strings.NewReader(in), true)
	if err != nil {
		t.Fatal(err)
	}
	if msg.FromDomain != "ex.io" {
		t.Errorf("FromDomain = %q", msg.FromDomain)
	}
	if !strings.Contains(string(msg.Raw), "body\r\n.\r\nmore") {
		t.Errorf("CRLF body altered:\n%q", msg.Raw)
	}
}

func TestParseMessage_NoFrom(t *testing.T) {
	msg, err := ParseMessage(strings.NewReader("To: x@y.z\nSubject: hi\n\nhello\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if msg.HasFrom || msg.FromDomain != "" {
		t.Errorf("HasFrom=%v FromDomain=%q, want none", msg.HasFrom, msg.FromDomain)
	}
}

func TestParseMessage_Oversize(t *testing.T) {
	r := strings.NewReader("To: x@y.z\n\n" + strings.Repeat("A", MaxMessageBytes))
	_, err := ParseMessage(r, false)
	if ExitCode(err) != ExitDataErr {
		t.Fatalf("exit = %d, want %d (err=%v)", ExitCode(err), ExitDataErr, err)
	}
}

func writeCred(t *testing.T, dir, name, email string) {
	t.Helper()
	content := "email=" + email + "\npassword=sekrit\nhost=mail.panel.tld\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCred(t *testing.T) {
	dir := t.TempDir()
	writeCred(t, dir, "example.com.cred", "noreply@example.com")
	writeCred(t, dir, "default.cred", "noreply@primary.tld")

	c, err := LoadCred(dir, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "noreply@example.com" || c.Host != "mail.panel.tld" {
		t.Errorf("got %+v", c)
	}

	c, err = LoadCred(dir, "other.tld")
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "noreply@primary.tld" {
		t.Errorf("fallback got %+v", c)
	}

	// Hostile From domains must never traverse — they fall to default.
	for _, hostile := range []string{"../../../etc/passwd", "EXAMPLE.COM/../x", "a..b", "-leading.tld", ""} {
		c, err = LoadCred(dir, hostile)
		if err != nil {
			t.Fatalf("hostile %q: %v", hostile, err)
		}
		if c.Email != "noreply@primary.tld" {
			t.Errorf("hostile %q selected %q", hostile, c.Email)
		}
	}
}

func TestLoadCred_MissingAndIncomplete(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadCred(dir, "example.com")
	if ExitCode(err) != ExitConfig {
		t.Fatalf("exit = %d, want %d", ExitCode(err), ExitConfig)
	}

	if err := os.WriteFile(filepath.Join(dir, "default.cred"), []byte("email=x@y.z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadCred(dir, "")
	if ExitCode(err) != ExitConfig {
		t.Fatalf("incomplete cred: exit = %d, want %d", ExitCode(err), ExitConfig)
	}
}

func TestEnsureSender(t *testing.T) {
	t.Run("matching identity untouched", func(t *testing.T) {
		msg, _ := ParseMessage(strings.NewReader("From: noreply@d.tld\nTo: x@y.z\n\nb\n"), false)
		out := string(EnsureSender(msg, "noreply@d.tld"))
		if strings.Contains(out, "Sender:") {
			t.Errorf("unexpected Sender header:\n%s", out)
		}
	})
	t.Run("mismatch gets Sender, spoofed Sender dropped", func(t *testing.T) {
		msg, _ := ParseMessage(strings.NewReader("From: wordpress@d.tld\nSender: ceo@bank.tld\nTo: x@y.z\n\nb\n"), false)
		out := string(EnsureSender(msg, "noreply@d.tld"))
		if !strings.Contains(out, "Sender: <noreply@d.tld>") {
			t.Errorf("missing forced Sender:\n%s", out)
		}
		if strings.Contains(out, "ceo@bank.tld") {
			t.Errorf("spoofed Sender survived:\n%s", out)
		}
		if !strings.Contains(out, "From: wordpress@d.tld") {
			t.Errorf("From was rewritten:\n%s", out)
		}
	})
	t.Run("no From gets one", func(t *testing.T) {
		msg, _ := ParseMessage(strings.NewReader("To: x@y.z\nSubject: s\n\nb\n"), false)
		out := string(EnsureSender(msg, "noreply@d.tld"))
		if !strings.HasPrefix(out, "From: <noreply@d.tld>\n") {
			t.Errorf("missing injected From:\n%s", out)
		}
	})
}

func TestExitCode_Unknown(t *testing.T) {
	if ExitCode(errors.New("boom")) != ExitTempFail {
		t.Error("unknown errors must map to tempfail")
	}
	if ExitCode(nil) != ExitOK {
		t.Error("nil must map to 0")
	}
}
