package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func setupSendmailTest(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("JABALI_SENDMAIL_CRED_ROOT", root)
	orig := sendmailChown
	sendmailChown = func(string, int, int) error { return nil }
	t.Cleanup(func() { sendmailChown = orig })
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return root, u.Username
}

func ensureCred(t *testing.T, params sendmailCredEnsureParams) sendmailCredEnsureResponse {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := sendmailCredEnsureHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return res.(sendmailCredEnsureResponse)
}

func TestSendmailCredEnsure_WritesAndIdempotent(t *testing.T) {
	root, username := setupSendmailTest(t)

	p := sendmailCredEnsureParams{
		Username: username, Domain: "example.com",
		Email: "noreply@example.com", Password: "s3cret", Host: "mail.panel.tld",
	}
	if res := ensureCred(t, p); !res.Changed {
		t.Error("first ensure should report changed")
	}

	credPath := filepath.Join(root, username, "example.com.cred")
	data, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"email=noreply@example.com", "password=s3cret", "host=mail.panel.tld"} {
		if !containsLine(string(data), want) {
			t.Errorf("cred missing %q:\n%s", want, data)
		}
	}
	fi, _ := os.Stat(credPath)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("cred mode = %o, want 0640", fi.Mode().Perm())
	}

	// First cred becomes default.
	target, err := os.Readlink(filepath.Join(root, username, "default.cred"))
	if err != nil || target != "example.com.cred" {
		t.Errorf("default.cred -> %q (err=%v), want example.com.cred", target, err)
	}

	// Second identical call: no change.
	if res := ensureCred(t, p); res.Changed {
		t.Error("identical ensure should be a no-op")
	}

	// Second domain does NOT steal default...
	p2 := p
	p2.Domain = "other.tld"
	p2.Email = "noreply@other.tld"
	ensureCred(t, p2)
	target, _ = os.Readlink(filepath.Join(root, username, "default.cred"))
	if target != "example.com.cred" {
		t.Errorf("default moved to %q without force", target)
	}
	// ...unless MakeDefault is set.
	p2.MakeDefault = true
	ensureCred(t, p2)
	target, _ = os.Readlink(filepath.Join(root, username, "default.cred"))
	if target != "other.tld.cred" {
		t.Errorf("default = %q, want other.tld.cred after force", target)
	}
}

func TestSendmailCredEnsure_Validation(t *testing.T) {
	_, username := setupSendmailTest(t)
	base := sendmailCredEnsureParams{
		Username: username, Domain: "example.com",
		Email: "noreply@example.com", Password: "x", Host: "mail.h.tld",
	}
	tests := []struct {
		name   string
		mutate func(*sendmailCredEnsureParams)
	}{
		{"bad username", func(p *sendmailCredEnsureParams) { p.Username = "../root" }},
		{"traversal domain", func(p *sendmailCredEnsureParams) { p.Domain = "../../etc/cron.d/x" }},
		{"uppercase domain", func(p *sendmailCredEnsureParams) { p.Domain = "EXAMPLE.COM" }},
		{"bad email", func(p *sendmailCredEnsureParams) { p.Email = "not-an-email" }},
		{"newline password", func(p *sendmailCredEnsureParams) { p.Password = "a\nb" }},
		{"empty host", func(p *sendmailCredEnsureParams) { p.Host = "" }},
		{"unknown user", func(p *sendmailCredEnsureParams) { p.Username = "nosuchuserzz" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			raw, _ := json.Marshal(p)
			_, err := sendmailCredEnsureHandler(context.Background(), raw)
			if err == nil {
				t.Fatal("expected rejection")
			}
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) {
				t.Fatalf("not an AgentError: %v", err)
			}
		})
	}
}

func TestSendmailCredRemove_RepointsDefault(t *testing.T) {
	root, username := setupSendmailTest(t)
	base := sendmailCredEnsureParams{
		Username: username, Password: "x", Host: "mail.h.tld",
	}
	for _, d := range []string{"bbb.tld", "aaa.tld"} {
		p := base
		p.Domain = d
		p.Email = "noreply@" + d
		ensureCred(t, p)
	}
	// default -> bbb.tld.cred (first provisioned)
	dir := filepath.Join(root, username)

	raw, _ := json.Marshal(sendmailCredRemoveParams{Username: username, Domain: "bbb.tld"})
	if _, err := sendmailCredRemoveHandler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bbb.tld.cred")); !os.IsNotExist(err) {
		t.Error("bbb.tld.cred survived removal")
	}
	target, err := os.Readlink(filepath.Join(dir, "default.cred"))
	if err != nil || target != "aaa.tld.cred" {
		t.Errorf("default.cred -> %q (err=%v), want aaa.tld.cred", target, err)
	}

	// Removing the last cred clears the directory.
	raw, _ = json.Marshal(sendmailCredRemoveParams{Username: username, Domain: "aaa.tld"})
	if _, err := sendmailCredRemoveHandler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("user cred dir should be gone, stat err=%v", err)
	}

	// Removing for a nonexistent user/domain is a calm no-op (cascade replays).
	raw, _ = json.Marshal(sendmailCredRemoveParams{Username: username, Domain: "never.was"})
	if _, err := sendmailCredRemoveHandler(context.Background(), raw); err != nil {
		t.Errorf("remove of absent cred should succeed: %v", err)
	}
}

func TestRemoveSendmailCredForDomain_SharedCascade(t *testing.T) {
	root, username := setupSendmailTest(t)
	base := sendmailCredEnsureParams{Username: username, Password: "x", Host: "mail.h.tld"}
	for _, d := range []string{"gone.tld", "stays.tld"} {
		p := base
		p.Domain = d
		p.Email = "noreply@" + d
		ensureCred(t, p)
	}
	dir := filepath.Join(root, username)

	// default -> gone.tld.cred (first provisioned); cascade must re-point it.
	removeSendmailCredForDomain("gone.tld")
	if _, err := os.Stat(filepath.Join(dir, "gone.tld.cred")); !os.IsNotExist(err) {
		t.Error("gone.tld.cred survived the cascade")
	}
	target, err := os.Readlink(filepath.Join(dir, "default.cred"))
	if err != nil || target != "stays.tld.cred" {
		t.Errorf("default.cred -> %q (err=%v), want stays.tld.cred", target, err)
	}

	// Hostile input is ignored outright.
	removeSendmailCredForDomain("../" + username)
	if _, err := os.Stat(filepath.Join(dir, "stays.tld.cred")); err != nil {
		t.Errorf("hostile removal touched an unrelated cred: %v", err)
	}
}

func containsLine(haystack, needle string) bool {
	for _, l := range strings.Split(haystack, "\n") {
		if l == needle {
			return true
		}
	}
	return false
}
