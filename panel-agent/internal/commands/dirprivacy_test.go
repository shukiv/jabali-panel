package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalisePrivacyPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"/secret", "/secret", false},
		{"/a/b/c", "/a/b/c", false},
		{"/trim/", "/trim", false},
		{"/", "/", false},
		{"", "", true},
		{"no-leading-slash", "", true},
		{"/has space", "", true},
		{"/has//double", "", true},
		{"/../etc/passwd", "", true},
		{"/ok-but-dotdot/../boom", "", true},
		{"/special$char", "", true},
	}
	for _, tc := range cases {
		got, err := normalisePrivacyPath(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalisePrivacyPath(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalisePrivacyPath(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalisePrivacyPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitisePrivacyRealm(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "Restricted"},
		{"Staging", "Staging"},
		{`bad"quote`, "badquote"},
		{`bad\back`, "badback"},
		{"highÿascii", "highascii"},
		{"  trim  ", "trim"},
	}
	for _, tc := range cases {
		got := sanitisePrivacyRealm(tc.in)
		if got != tc.want {
			t.Errorf("sanitisePrivacyRealm(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWriteHtpasswd_EmptyCreds_DenyByDefault(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "rule.htpasswd")
	if err := writeHtpasswd(f, nil); err != nil {
		t.Fatalf("writeHtpasswd: %v", err)
	}
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "__deny__:!") {
		t.Errorf("want deny-by-default placeholder, got %q", got)
	}
	info, _ := os.Stat(f)
	if info.Mode().Perm() != dirPrivacyFileMode {
		t.Errorf("perm = %v, want %v", info.Mode().Perm(), os.FileMode(dirPrivacyFileMode))
	}
}

func TestWriteHtpasswd_BcryptOnly(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "rule.htpasswd")
	creds := []directoryPrivacyCredential{
		{Username: "alice", PasswordHash: "$2y$10$abcdefghijklmnopqrstuv"},
		{Username: "bob", PasswordHash: "$2a$12$abcdefghijklmnopqrstuv"},
		// rejected: bad username
		{Username: "bad user", PasswordHash: "$2y$10$abc"},
		// rejected: not bcrypt
		{Username: "carol", PasswordHash: "plaintext"},
	}
	if err := writeHtpasswd(f, creds); err != nil {
		t.Fatalf("writeHtpasswd: %v", err)
	}
	got, _ := os.ReadFile(f)
	s := string(got)
	if !strings.Contains(s, "alice:$2y") {
		t.Errorf("missing alice; got %q", s)
	}
	if !strings.Contains(s, "bob:$2a") {
		t.Errorf("missing bob; got %q", s)
	}
	if strings.Contains(s, "bad user") {
		t.Errorf("did not reject bad username: %q", s)
	}
	if strings.Contains(s, "carol") {
		t.Errorf("did not reject non-bcrypt hash: %q", s)
	}
}

func TestBuildDirectoryPrivacyDirectives(t *testing.T) {
	rules := []directoryPrivacyRule{
		{
			RuleID: "01HZZZ8RMJG3K4P5N6Q7R8S9T0",
			Path:   "/secret",
			Realm:  "Staging",
		},
	}
	got := buildDirectoryPrivacyDirectives(rules)
	if !strings.Contains(got, `location ^~ /secret/`) {
		t.Errorf("missing location directive: %q", got)
	}
	if !strings.Contains(got, `auth_basic "Staging"`) {
		t.Errorf("missing auth_basic realm: %q", got)
	}
	if !strings.Contains(got, dirPrivacyDir+"/01HZZZ8RMJG3K4P5N6Q7R8S9T0.htpasswd") {
		t.Errorf("missing htpasswd path: %q", got)
	}
}

func TestBuildDirectoryPrivacyDirectives_RealmEscaping(t *testing.T) {
	rules := []directoryPrivacyRule{
		{
			RuleID: "01HZZZ8RMJG3K4P5N6Q7R8S9T0",
			Path:   "/x",
			Realm:  `evil"quote`, // sanitised to evilquote
		},
	}
	got := buildDirectoryPrivacyDirectives(rules)
	if !strings.Contains(got, `auth_basic "evilquote"`) {
		t.Errorf("realm not sanitised: %q", got)
	}
}

func TestBuildDirectoryPrivacyDirectives_RejectsBadRule(t *testing.T) {
	got := buildDirectoryPrivacyDirectives([]directoryPrivacyRule{
		{RuleID: "not-a-ulid", Path: "/x", Realm: "X"},
	})
	if strings.Contains(got, "not-a-ulid") {
		t.Errorf("emitted bad rule ID: %q", got)
	}
}

func TestBuildDirectoryPrivacyDirectives_Empty(t *testing.T) {
	if got := buildDirectoryPrivacyDirectives(nil); got != "" {
		t.Errorf("empty rules: got %q, want \"\"", got)
	}
}
