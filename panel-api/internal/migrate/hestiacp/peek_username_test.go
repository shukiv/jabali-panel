package hestiacp

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

// GH #327: the contact name lives at hestia/user.conf as NAME='Full Name'.
func TestPeekUserName_HestiaNameFieldAndPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hestia"), 0o750); err != nil {
		t.Fatal(err)
	}
	conf := "NAME='ITFlow Support'\nCONTACT='support@itflow.org'\nPACKAGE='default'\n"
	if err := os.WriteFile(filepath.Join(dir, "hestia", "user.conf"), []byte(conf), 0o640); err != nil {
		t.Fatal(err)
	}
	first, last := PeekUserName(dir)
	if first != "ITFlow" || last != "Support" {
		t.Errorf("got (%q,%q), want (ITFlow, Support)", first, last)
	}
}

// A single-token NAME goes entirely into first.
func TestPeekUserName_SingleToken(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "hestia"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "hestia", "user.conf"), []byte("NAME='ITFlow'\n"), 0o640)
	if first, last := PeekUserName(dir); first != "ITFlow" || last != "" {
		t.Errorf("got (%q,%q), want (ITFlow, \"\")", first, last)
	}
}

// Explicit FNAME/LNAME win over NAME when present.
func TestPeekUserName_FNameLNamePreferred(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "hestia"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "hestia", "user.conf"),
		[]byte("NAME='Ignored Name'\nFNAME='John'\nLNAME='Doe'\n"), 0o640)
	if first, last := PeekUserName(dir); first != "John" || last != "Doe" {
		t.Errorf("got (%q,%q), want (John, Doe)", first, last)
	}
}

// Root-level user.conf still works (fallback layout).
func TestPeekUserName_RootFallback(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "user.conf"), []byte("NAME='Root Layout'\n"), 0o640)
	if first, _ := PeekUserName(dir); first != "Root" {
		t.Errorf("root fallback: got first=%q, want Root", first)
	}
}

func TestPeekUserName_Absent(t *testing.T) {
	if first, last := PeekUserName(t.TempDir()); first != "" || last != "" {
		t.Errorf("absent conf should yield empty, got (%q,%q)", first, last)
	}
}

func TestPeekUserNameFromStaging(t *testing.T) {
	staging := t.TempDir()
	// build a minimal v-backup-user-shaped tar with hestia/user.conf.
	tarPath := filepath.Join(staging, "user.itflowapp.tar")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body := []byte("NAME='ITFlow Support'\nCONTACT='x@y.z'\n")
	if err := tw.WriteHeader(&tar.Header{Name: "./hestia/user.conf", Mode: 0o640, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = f.Close()

	first, last := PeekUserNameFromStaging(staging, "itflowapp")
	if first != "ITFlow" || last != "Support" {
		t.Errorf("from staging tar: got (%q,%q), want (ITFlow, Support)", first, last)
	}
	// absent staging → empty, no panic.
	if f, l := PeekUserNameFromStaging(t.TempDir(), "nobody"); f != "" || l != "" {
		t.Errorf("empty staging should yield empty, got (%q,%q)", f, l)
	}
}

// GH #516: the account's contact email (CONTACT=) must carry over from
// user.conf instead of being synthesized as <user>@<host>.
func TestParseHestiaContactEmail(t *testing.T) {
	cases := map[string]string{
		"NAME='ITFlow Support'\nCONTACT='support@itflow.org'\nPACKAGE='default'\n": "support@itflow.org",
		"CONTACT=\"user@example.com\"\n":                                           "user@example.com",
		"CONTACT=bare@nodots.co\n":                                                 "bare@nodots.co",
		"NAME='x'\nPACKAGE='default'\n":                                            "", // no CONTACT
	}
	for body, want := range cases {
		if got := parseHestiaContactEmail([]byte(body)); got != want {
			t.Errorf("parseHestiaContactEmail(%q) = %q, want %q", body, got, want)
		}
	}
}
